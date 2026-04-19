// Package kb is a per-client retrieval layer.
//
// It tries semantic retrieval first (OpenAI-compatible /v1/embeddings — works
// with llama.cpp --embeddings or any hosted embedder) and transparently falls
// back to a lexical BM25-ish score over the raw `client_docs` rows when no
// embedding server is reachable. This guarantees that uploaded docs are
// surfaced to the LLM even on fully offline boxes that don't run an embedder.
package kb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"

	"scorpion/agent/internal/config"
	"scorpion/agent/internal/memory"
)

type Store struct {
	mem   *memory.DB
	store *config.Store
	http  *http.Client
}

func New(mem *memory.DB, store *config.Store) *Store {
	return &Store{mem: mem, store: store, http: &http.Client{Timeout: 30 * time.Second}}
}

// IngestDoc chunks body, computes embeddings, and stores them tagged with
// (client_id, doc_id). If the embedding server is unreachable we swallow the
// error — the lexical fallback in Retrieve will still surface the doc body.
func (s *Store) IngestDoc(ctx context.Context, clientID, docID, body string) error {
	chunks := chunk(body, 900, 120)
	if len(chunks) == 0 {
		return nil
	}
	_ = s.mem.DeleteEmbeds("doc", docID)
	for _, ch := range chunks {
		vec, err := s.embed(ctx, ch)
		if err != nil {
			// Embedder offline is not a user-visible error — docs still work
			// via the lexical path. Log-style noise is suppressed upstream.
			return nil
		}
		if err := s.mem.InsertEmbed(&memory.EmbedRow{
			Kind:     "doc",
			RefID:    docID,
			ClientID: clientID,
			Text:     ch,
			Vec:      vec,
		}); err != nil {
			return err
		}
	}
	return nil
}

// Retrieve returns up to K chunks most similar to query, concatenated into a
// single excerpt string (truncated to maxChars). If the embedder is offline
// or no embeddings have been stored yet, it falls back to a lexical ranking
// over the raw client_docs table so docs always reach the LLM.
func (s *Store) Retrieve(ctx context.Context, clientID, query string) (string, error) {
	cfg := s.store.Snapshot()
	override := s.store.Overrides()
	if override.KBPaused != nil && *override.KBPaused {
		return "", nil
	}

	type hit struct {
		text string
		sim  float32
	}
	var hits []hit

	// ----- Path 1: semantic retrieval over stored embeddings -----
	if qvec, err := s.embed(ctx, query); err == nil {
		_ = s.mem.IterEmbeds(clientID, func(e *memory.EmbedRow) error {
			hits = append(hits, hit{text: e.Text, sim: cosine(qvec, e.Vec)})
			return nil
		})
	}

	// ----- Path 2: lexical fallback over client_docs -----
	// Always run when semantic path found nothing (or < 2 hits above floor).
	// This is the robustness net that makes docs work without an embedder.
	usable := 0
	for _, h := range hits {
		if h.sim >= 0.15 {
			usable++
		}
	}
	if usable < 2 {
		qTokens := tokenize(query)
		// "Who is …", "what do you know about …" and other generic questions
		// produce almost no useful tokens. In that case include *everything*
		// (up to the context cap) — the user explicitly asked us to recall.
		generic := isGenericRecall(query, qTokens)
		if docs, err := s.mem.ListDocs(clientID); err == nil {
			for _, d := range docs {
				for _, ch := range chunk(d.Title+"\n"+d.Body, 900, 120) {
					sim := lexicalScore(ch, qTokens)
					if generic && sim == 0 {
						sim = 0.2 // keep it, but rank below true matches
					}
					if sim > 0 {
						hits = append(hits, hit{text: ch, sim: sim})
					}
				}
			}
		}
	}

	if len(hits) == 0 {
		return "", nil
	}

	// Dedupe on normalised text so the same chunk from vector+lexical doesn't
	// double-count.
	seen := map[string]float32{}
	for _, h := range hits {
		k := strings.TrimSpace(h.text)
		if prev, ok := seen[k]; !ok || h.sim > prev {
			seen[k] = h.sim
		}
	}
	uniq := make([]hit, 0, len(seen))
	for t, s := range seen {
		uniq = append(uniq, hit{text: t, sim: s})
	}
	sort.Slice(uniq, func(i, j int) bool { return uniq[i].sim > uniq[j].sim })

	top := cfg.KBTopK
	if top <= 0 {
		top = 5
	}
	if len(uniq) > top {
		uniq = uniq[:top]
	}

	var b strings.Builder
	for i, h := range uniq {
		fmt.Fprintf(&b, "(%d, %.2f) %s\n", i+1, h.sim, h.text)
		if b.Len() > cfg.KBMaxContextChars {
			break
		}
	}
	return b.String(), nil
}

func (s *Store) embed(ctx context.Context, text string) ([]float32, error) {
	cfg := s.store.Snapshot()
	body, _ := json.Marshal(map[string]any{"model": cfg.EmbedModel, "input": text})
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(cfg.EmbedBaseURL, "/")+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embed %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("no embedding")
	}
	return out.Data[0].Embedding, nil
}

// BackfillEmbeddings walks every doc that has no stored embedding row and
// tries to compute + store one. Safe to call on startup and whenever an
// embedding server comes online. No-ops silently on errors.
func (s *Store) BackfillEmbeddings(ctx context.Context) {
	docs, err := s.mem.ListDocs("")
	if err != nil {
		return
	}
	// Collect existing embedding ref_ids so we don't re-embed what already has rows.
	have := map[string]bool{}
	_ = s.mem.IterEmbeds("", func(e *memory.EmbedRow) error {
		if e.Kind == "doc" {
			have[e.RefID] = true
		}
		return nil
	})
	for _, d := range docs {
		if have[d.ID] {
			continue
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
		_ = s.IngestDoc(ctx, d.ClientID, d.ID, d.Body)
	}
}

func cosine(a, b []float32) float32 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var dot, na, nb float64
	for i := 0; i < n; i++ {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}

func chunk(s string, maxChars, overlap int) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if len(s) <= maxChars {
		return []string{s}
	}
	var out []string
	start := 0
	for start < len(s) {
		end := start + maxChars
		if end >= len(s) {
			out = append(out, s[start:])
			break
		}
		cut := end
		for i := end; i > start+maxChars/2; i-- {
			if s[i] == ' ' || s[i] == '\n' {
				cut = i
				break
			}
		}
		out = append(out, strings.TrimSpace(s[start:cut]))
		start = cut - overlap
		if start < 0 {
			start = 0
		}
	}
	return out
}

// ---------- Lexical scoring (embedder-free fallback) ----------

// tokenize lowercases and splits on non-letter/digit, dropping stop words and
// very short tokens. Good enough for the small, personal docs this app has.
func tokenize(s string) []string {
	s = strings.ToLower(s)
	var cur strings.Builder
	var toks []string
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		w := cur.String()
		cur.Reset()
		if len(w) < 2 || stopWords[w] {
			return
		}
		toks = append(toks, w)
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return toks
}

// lexicalScore returns a TF-IDF-lite similarity: number of query tokens present
// in the chunk, normalised by the log of the chunk length. The exact number is
// unimportant — we only use it to rank chunks relative to each other.
func lexicalScore(chunkText string, qTokens []string) float32 {
	if len(qTokens) == 0 {
		return 0
	}
	docTokens := tokenize(chunkText)
	if len(docTokens) == 0 {
		return 0
	}
	docSet := make(map[string]int, len(docTokens))
	for _, t := range docTokens {
		docSet[t]++
	}
	var score float64
	for _, qt := range qTokens {
		if docSet[qt] > 0 {
			score += 1 + math.Log1p(float64(docSet[qt]))
		}
	}
	if score == 0 {
		return 0
	}
	// Normalise so long chunks don't always win.
	norm := 1 + math.Log1p(float64(len(docTokens)))
	return float32(score / norm)
}

// isGenericRecall detects questions like "who am I", "tell me about me",
// "what do you know about…" where the query has no strong keywords but we
// still want to surface all the client's docs.
func isGenericRecall(query string, qTokens []string) bool {
	q := strings.ToLower(query)
	triggers := []string{
		"who am i", "who is me",
		"tell me everything", "tell me about me",
		"what do you know", "what do you remember",
		"about me", "about myself",
		"summary of me", "summarize me",
	}
	for _, t := range triggers {
		if strings.Contains(q, t) {
			return true
		}
	}
	// Fewer than two content-bearing tokens → treat as generic.
	return len(qTokens) < 2
}

// stopWords is a tiny English stop list (plus a few Arabic/fr/es fillers that
// appear in mixed-language docs).
var stopWords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true, "but": true,
	"is": true, "are": true, "was": true, "were": true, "be": true, "been": true,
	"being": true, "am": true, "i": true, "me": true, "my": true, "mine": true,
	"you": true, "your": true, "yours": true, "he": true, "she": true, "it": true,
	"we": true, "they": true, "them": true, "us": true, "our": true, "their": true,
	"this": true, "that": true, "these": true, "those": true, "of": true, "to": true,
	"in": true, "on": true, "at": true, "for": true, "with": true, "about": true,
	"from": true, "by": true, "as": true, "if": true, "then": true, "so": true,
	"do": true, "does": true, "did": true, "has": true, "have": true, "had": true,
	"not": true, "no": true, "yes": true, "can": true, "will": true, "would": true,
	"should": true, "could": true, "what": true, "who": true, "when": true,
	"where": true, "why": true, "how": true, "which": true, "tell": true,
	"say": true, "know": true, "think": true, "feel": true, "want": true,
	"please": true, "okay": true, "ok": true, "just": true, "very": true, "really": true,
	"el": true, "la": true, "de": true, "y": true, "que": true, "en": true,
	"le": true, "les": true, "un": true, "une": true, "des": true,
}
