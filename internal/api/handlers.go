package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"scorpion/agent/internal/config"
	"scorpion/agent/internal/llm"
	"scorpion/agent/internal/memory"
)

func handleStatus(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := d.Store.Snapshot()
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		llmOK := d.LLM.Ping(ctx) == nil
		sttOK := d.STT.Ping(ctx) == nil
		piperOK := d.TTS.Available()
		llmBase := cfg.LLMBaseURL
		llmModel := cfg.LLMModel
		switch config.EffectiveLLMProvider(cfg) {
		case "gemini":
			llmBase = cfg.GeminiOpenAIBase
			if len(cfg.GeminiModels) > 0 {
				llmModel = strings.Join(cfg.GeminiModels, " → ")
			} else {
				llmModel = "gemini"
			}
		case "groq":
			llmBase = cfg.GroqOpenAIBase
			llmModel = strings.TrimSpace(cfg.LLMModel)
			if llmModel == "" {
				llmModel = "groq"
			}
		}
		writeJSON(w, 200, map[string]any{
			"ok":          true,
			"llm":         map[string]any{"ok": llmOK, "base_url": llmBase, "model": llmModel},
			"stt":         map[string]any{"ok": sttOK, "base_url": cfg.WhisperBaseURL, "model": cfg.WhisperModel},
			"piper":       map[string]any{"ok": piperOK, "bin": cfg.PiperBin, "model": cfg.PiperModel},
			"server_time": time.Now().Unix(),
		})
	}
}

func handleConfig(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, d.Store.Snapshot().PublicAPI())
	}
}

func handleGetSettings(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		snap := d.Store.Snapshot()
		writeJSON(w, 200, map[string]any{
			"base":                   snap.PublicAPI(),
			"overrides":              config.MaskOverrides(d.Store.Overrides()),
			"effective_llm_provider": config.EffectiveLLMProvider(snap),
		})
	}
}

func handlePatchSettings(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var delta config.Overrides
		if err := readJSON(r, &delta); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if err := d.Store.Patch(delta); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{
			"ok":                     true,
			"overrides":              config.MaskOverrides(d.Store.Overrides()),
			"effective_llm_provider": config.EffectiveLLMProvider(d.Store.Snapshot()),
		})
	}
}

// serveRemoteLLMModelList writes {"models":[...]} from the provider's OpenAI /v1/models API.
func serveRemoteLLMModelList(d *Deps, w http.ResponseWriter, r *http.Request, p string) {
	p = strings.ToLower(strings.TrimSpace(p))
	if p != "gemini" && p != "groq" {
		writeErr(w, 400, "provider must be gemini or groq")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	models, err := d.LLM.ListRemoteModels(ctx, p)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"models": models})
}

func handleLLMRemoteModels(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("provider")
		serveRemoteLLMModelList(d, w, r, p)
	}
}

// ---------- Clients ----------

func handleListClients(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cs, err := d.Mem.ListClients()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"clients": cs})
	}
}

func handleCreateClient(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var c memory.Client
		if err := readJSON(r, &c); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if c.Name == "" {
			writeErr(w, 400, "name required")
			return
		}
		if err := d.Mem.CreateClient(&c); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 201, c)
	}
}

func handleGetClient(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := d.Mem.GetClient(chi.URLParam(r, "id"))
		if err != nil {
			writeErr(w, 404, "not found")
			return
		}
		writeJSON(w, 200, c)
	}
}

func handleUpdateClient(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var c memory.Client
		if err := readJSON(r, &c); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		c.ID = chi.URLParam(r, "id")
		if err := d.Mem.UpdateClient(&c); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, c)
	}
}

func handleDeleteClient(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := d.Mem.DeleteClient(chi.URLParam(r, "id")); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	}
}

// handleClearClient wipes per-client data by kind.
// kind ∈ {calls, memory, actions, docs, all}
func handleClearClient(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cid := chi.URLParam(r, "id")
		if _, err := d.Mem.GetClient(cid); err != nil {
			writeErr(w, 404, "client not found")
			return
		}
		kind := r.URL.Query().Get("kind")
		if kind == "" {
			kind = "all"
		}
		result := map[string]int64{}
		do := func(k string) error {
			switch k {
			case "calls", "conversations":
				n, err := d.Mem.ClearConversations(cid)
				result["conversations"] += n
				return err
			case "memory", "facts":
				n, err := d.Mem.ClearFacts(cid)
				result["facts"] += n
				return err
			case "actions":
				n, err := d.Mem.ClearActions(cid)
				result["actions"] += n
				return err
			case "docs":
				n, err := d.Mem.ClearDocs(cid)
				result["docs"] += n
				return err
			}
			return nil
		}
		if kind == "all" {
			for _, k := range []string{"calls", "memory", "actions", "docs"} {
				if err := do(k); err != nil {
					writeErr(w, 500, err.Error())
					return
				}
			}
		} else {
			if err := do(kind); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
		}
		writeJSON(w, 200, map[string]any{"ok": true, "cleared": result})
	}
}

// ---------- Docs ----------

func handleListDocs(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		docs, err := d.Mem.ListDocs(chi.URLParam(r, "id"))
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"docs": docs})
	}
}

func handleCreateDoc(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clientID := chi.URLParam(r, "id")
		ct := r.Header.Get("Content-Type")
		doc := &memory.Doc{ClientID: clientID}
		if ct == "application/json" || ct == "application/json; charset=utf-8" {
			if err := readJSON(r, doc); err != nil {
				writeErr(w, 400, err.Error())
				return
			}
			doc.ClientID = clientID
		} else {
			// treat body as text
			b, err := io.ReadAll(r.Body)
			if err != nil {
				writeErr(w, 400, err.Error())
				return
			}
			doc.Title = r.URL.Query().Get("title")
			if doc.Title == "" {
				doc.Title = "untitled"
			}
			doc.Body = string(b)
		}
		if doc.Title == "" || doc.Body == "" {
			writeErr(w, 400, "title and body required")
			return
		}
		if err := d.Mem.InsertDoc(doc); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		// Kick off async embedding
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			_ = d.KB.IngestDoc(ctx, doc.ClientID, doc.ID, doc.Body)
		}()
		writeJSON(w, 201, doc)
	}
}

func handleDeleteDoc(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := d.Mem.DeleteDoc(chi.URLParam(r, "id")); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	}
}

// ---------- Conversations / facts / actions ----------

func handleListConversations(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		convs, err := d.Mem.ListConversations(chi.URLParam(r, "id"), parseInt(r.URL.Query().Get("limit"), 100))
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"conversations": convs})
	}
}

func handleGetConversation(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := d.Mem.GetConversation(chi.URLParam(r, "id"))
		if err != nil {
			writeErr(w, 404, "not found")
			return
		}
		writeJSON(w, 200, c)
	}
}

func handleListTurns(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ts, err := d.Mem.ListTurns(chi.URLParam(r, "id"))
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"turns": ts})
	}
}

func handleListFacts(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fs, err := d.Mem.ListFacts(chi.URLParam(r, "id"), r.URL.Query().Get("conversation_id"), parseInt(r.URL.Query().Get("limit"), 200))
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"facts": fs})
	}
}

func handleListActions(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		as, err := d.Mem.ListActions(chi.URLParam(r, "id"), r.URL.Query().Get("conversation_id"), parseInt(r.URL.Query().Get("limit"), 200))
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"actions": as})
	}
}

// ---------- Graph ----------

type graphNode struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Type  string `json:"type"`
	Meta  any    `json:"meta,omitempty"`
}

type graphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

func handleMemoryGraph(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clientID := r.URL.Query().Get("client_id")
		nodes := []graphNode{}
		edges := []graphEdge{}
		clients, _ := d.Mem.ListClients()
		for _, c := range clients {
			if clientID != "" && c.ID != clientID {
				continue
			}
			nodes = append(nodes, graphNode{ID: "client:" + c.ID, Label: c.Name, Type: "client", Meta: c})
			convs, _ := d.Mem.ListConversations(c.ID, 500)
			byDay := map[string]bool{}
			for _, cv := range convs {
				nodes = append(nodes, graphNode{ID: "conv:" + cv.ID, Label: shortTime(cv.StartedAt), Type: "conversation"})
				edges = append(edges, graphEdge{From: "client:" + c.ID, To: "conv:" + cv.ID, Kind: "had_call"})
				day := dayKey(cv.StartedAt)
				dayID := "day:" + c.ID + ":" + day
				if !byDay[day] {
					nodes = append(nodes, graphNode{ID: dayID, Label: day, Type: "day"})
					edges = append(edges, graphEdge{From: "client:" + c.ID, To: dayID, Kind: "day"})
					byDay[day] = true
				}
				edges = append(edges, graphEdge{From: dayID, To: "conv:" + cv.ID, Kind: "on"})
			}
			facts, _ := d.Mem.ListFacts(c.ID, "", 500)
			for _, f := range facts {
				id := "fact:" + f.ID
				nodes = append(nodes, graphNode{ID: id, Label: f.Subject + " " + f.Predicate + " " + f.Object, Type: "fact", Meta: f})
				if f.ConvID != "" {
					edges = append(edges, graphEdge{From: "conv:" + f.ConvID, To: id, Kind: "produced"})
				} else {
					edges = append(edges, graphEdge{From: "client:" + c.ID, To: id, Kind: "knows"})
				}
			}
			acts, _ := d.Mem.ListActions(c.ID, "", 500)
			for _, a := range acts {
				id := "action:" + a.ID
				nodes = append(nodes, graphNode{ID: id, Label: a.Type, Type: "action", Meta: a})
				if a.ConvID != "" {
					edges = append(edges, graphEdge{From: "conv:" + a.ConvID, To: id, Kind: "triggered"})
				} else {
					edges = append(edges, graphEdge{From: "client:" + c.ID, To: id, Kind: "action"})
				}
			}
		}
		writeJSON(w, 200, map[string]any{"nodes": nodes, "edges": edges})
	}
}

func handleGraph(d *Deps) http.HandlerFunc {
	// Per-conversation subgraph.
	return func(w http.ResponseWriter, r *http.Request) {
		convID := chi.URLParam(r, "id")
		conv, err := d.Mem.GetConversation(convID)
		if err != nil {
			writeErr(w, 404, "not found")
			return
		}
		nodes := []graphNode{
			{ID: "conv:" + conv.ID, Label: "Call " + shortTime(conv.StartedAt), Type: "conversation"},
		}
		edges := []graphEdge{}
		if conv.ClientID != "" {
			if c, err := d.Mem.GetClient(conv.ClientID); err == nil {
				nodes = append(nodes, graphNode{ID: "client:" + c.ID, Label: c.Name, Type: "client"})
				edges = append(edges, graphEdge{From: "client:" + c.ID, To: "conv:" + conv.ID, Kind: "had_call"})
			}
		}
		ts, _ := d.Mem.ListTurns(convID)
		for _, t := range ts {
			id := "turn:" + t.ID
			nodes = append(nodes, graphNode{ID: id, Label: t.Speaker + ": " + trimLen(t.Text, 48), Type: "turn", Meta: t})
			edges = append(edges, graphEdge{From: "conv:" + conv.ID, To: id, Kind: "said"})
		}
		fs, _ := d.Mem.ListFacts("", convID, 200)
		for _, f := range fs {
			id := "fact:" + f.ID
			nodes = append(nodes, graphNode{ID: id, Label: f.Subject + " " + f.Predicate + " " + f.Object, Type: "fact", Meta: f})
			edges = append(edges, graphEdge{From: "conv:" + conv.ID, To: id, Kind: "produced"})
		}
		as, _ := d.Mem.ListActions("", convID, 200)
		for _, a := range as {
			id := "action:" + a.ID
			nodes = append(nodes, graphNode{ID: id, Label: a.Type, Type: "action", Meta: a})
			edges = append(edges, graphEdge{From: "conv:" + conv.ID, To: id, Kind: "triggered"})
		}
		writeJSON(w, 200, map[string]any{"nodes": nodes, "edges": edges})
	}
}

// ---------- Text turn (no audio) ----------

func handleTextTurn(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sid := chi.URLParam(r, "id")
		v, ok := d.Sessions.Load(sid)
		if !ok {
			writeErr(w, 404, "session not found")
			return
		}
		sess := v.(*sessionEntry)
		var body struct {
			Text string `json:"text"`
		}
		if err := readJSON(r, &body); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if body.Text == "" {
			writeErr(w, 400, "text required")
			return
		}
		sess.sess.HandleTextTurn(context.Background(), body.Text)
		writeJSON(w, 202, map[string]any{"ok": true})
	}
}

func handleGroqStats(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := d.Store.Snapshot()
		if !config.HasGroqCredentials(cfg) {
			writeErr(w, 503, "groq not configured")
			return
		}
		dash, ok := d.LLM.GroqDashboard(&cfg)
		if !ok {
			writeErr(w, 503, "groq key manager unavailable")
			return
		}
		model := strings.TrimSpace(cfg.LLMModel)
		if model == "" {
			model = "llama-3.1-8b-instant"
		}
		dash["model"] = model
		dash["cascade"] = map[string]any{
			"env_flag":       cfg.LLMCascade,
			"routing_active": cfg.CascadeOn(),
			"local_model":    cfg.CascadeLocalModel,
			"groq_model":     cfg.CascadeGroqModel,
			"llm_provider":   config.EffectiveLLMProvider(cfg),
		}
		if cfg.CascadeOn() {
			dash["router"] = llm.RouterStatsSnapshot()
		}
		writeJSON(w, 200, dash)
	}
}

// ---------- helpers ----------

func parseInt(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func shortTime(ts float64) string {
	return time.Unix(int64(ts), 0).UTC().Format("Jan 2 15:04")
}

func dayKey(ts float64) string {
	return time.Unix(int64(ts), 0).UTC().Format("2006-01-02")
}

func trimLen(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

var _ = json.Marshal // silence if unused
