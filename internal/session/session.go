// Package session is the per-call orchestrator. It wires VAD -> STT -> LLM
// (with tool-calling) -> TTS, handles barge-in cancellation, runs the post-call
// extraction pass, and emits events for the UI over an event sink callback.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"scorpion/agent/internal/actions"
	"scorpion/agent/internal/config"
	"scorpion/agent/internal/kb"
	"scorpion/agent/internal/llm"
	"scorpion/agent/internal/memory"
	"scorpion/agent/internal/tts"
	"scorpion/agent/internal/vad"
)

// isCancel reports whether an error is just our own turn-cancellation
// (barge-in, new utterance replacing an old one, session end). These are
// expected control-flow signals and should not be surfaced as user errors.

func isCancel(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "context canceled") || strings.Contains(s, "context deadline exceeded")
}

type State string

const (
	StateIdle      State = "idle"
	StateListening State = "listening"
	StateThinking  State = "thinking"
	StateSpeaking  State = "speaking"
	StateEnded     State = "ended"
)

type Event struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Time    float64         `json:"t"`
}

type Deps struct {
	Store *config.Store
	Mem   *memory.DB
	LLM   *llm.Client
	// STT   stt.Client // Removed - using Chrome Web Speech API
	TTS   *tts.Pool
	KB    *kb.Store
}

type Session struct {
	ID       string
	ClientID string
	ConvID   string
	Deps     *Deps

	vad  *vad.Segmenter
	exec actions.Executor
	self *SelfFilter

	mu     sync.Mutex
	state  atomic.Value // State
	cancel context.CancelFunc

	history []llm.Message

	// PCM48k outputs to the transport (webrtc peer). If nil, audio is dropped.
	AudioOut func(pcm48k []float32)
	Emit     func(Event)

	// turn-level state for barge-in
	turnCtx    context.Context
	turnCancel context.CancelFunc

	// Increments on each speech onset so in-flight partial STT can be dropped
	// when a new utterance starts.
	speechEpoch atomic.Uint64
	lastPartial   string
	muPartial     sync.Mutex
	partialCancel context.CancelFunc
}

func New(id, clientID, convID string, deps *Deps) *Session {
	cfg := deps.Store.Snapshot()
	s := &Session{
		ID: id, ClientID: clientID, ConvID: convID, Deps: deps,
		vad:  vad.NewSegmenter(deps.Store),
		exec: &actions.LocalExecutor{Mem: deps.Mem},
		self: NewSelfFilter(cfg.SelfFilterNGram),
	}
	s.state.Store(StateIdle)
	// VAD callbacks removed - using Chrome Web Speech API directly
	return s
}

// State returns current state.
func (s *Session) State() State {
	v, _ := s.state.Load().(State)
	return v
}

func (s *Session) setState(st State) {
	s.state.Store(st)
	s.emit("state", map[string]any{"state": string(st)})
}

// WriteMic pushes raw mic audio (already resampled to 16 kHz mono float32).
func (s *Session) WriteMic(pcm16k []float32) {
	s.vad.Write(pcm16k)
}

func (s *Session) onVoiceStart() {
	s.speechEpoch.Add(1)
	s.muPartial.Lock()
	s.lastPartial = ""
	if s.partialCancel != nil {
		s.partialCancel()
		s.partialCancel = nil
	}
	s.muPartial.Unlock()
	// Always emit a voice_start so the UI can instantly render a "you're
	// speaking…" ghost bubble before STT finishes. Barge-in still cancels the
	// in-flight turn when the AI happens to be speaking/thinking.
	s.emit("voice_start", map[string]any{})
	if s.State() == StateSpeaking || s.State() == StateThinking {
		s.mu.Lock()
		if s.turnCancel != nil {
			s.turnCancel()
		}
		s.mu.Unlock()
		s.emit("barge_in", map[string]any{})
	}
}

func (s *Session) onPartialAudio(pcm16k []float32) {
	if s.State() == StateEnded {
		return
	}
	
	s.muPartial.Lock()
	if s.partialCancel != nil {
		s.partialCancel()
	}
	_, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	s.partialCancel = cancel
	s.muPartial.Unlock()

	// Removed partial STT - using Chrome Web Speech API for real-time transcription
}

// HandleTextTurn processes a text-only user turn from Chrome Web Speech API.
func (s *Session) HandleTextTurn(ctx context.Context, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	// Same layer-3 echo guard as server STT: laptop speakers often feed TTS back into the mic path.
	if s.self.IsSelf(text) {
		s.emit("self_filtered", map[string]any{"text": text})
		return
	}
	go s.runTurn(ctx, text)
}

func (s *Session) runTurn(ctx context.Context, userText string) {
	s.setState(StateThinking)
	cfg := s.Deps.Store.Snapshot()
	over := s.Deps.Store.Overrides()

	// Build system prompt
	var clientName, clientInfo string
	if s.ClientID != "" {
		if c, err := s.Deps.Mem.GetClient(s.ClientID); err == nil {
			clientName = c.Name
			clientInfo = fmt.Sprintf("Business: %s\nIndustry: %s\nStage: %s\nRole: %s\nNotes: %s",
				c.Business, c.Industry, c.Stage, c.Role, c.Notes)
		}
	}
	var factLines []string
	if facts, err := s.Deps.Mem.ListFacts(s.ClientID, "", 25); err == nil {
		lines := make([]llm.FactLine, 0, len(facts))
		for _, f := range facts {
			lines = append(lines, llm.FactLine{Category: f.Category, Subject: f.Subject, Predicate: f.Predicate, Object: f.Object})
		}
		factLines = llm.CollectFacts(lines)
	}
	kbExcerpts, _ := s.Deps.KB.Retrieve(ctx, s.ClientID, userText)

	sysPrompt := llm.BuildSystemPrompt(llm.PromptContext{
		PromptsDir:   cfg.PromptsDir,
		ClientName:   clientName,
		ClientInfo:   clientInfo,
		Facts:        factLines,
		KBExcerpts:   kbExcerpts,
		OverrideSys:  strPtr(over.SystemPrompt),
		OverrideTone: strPtr(over.SystemTone),
	})

	s.mu.Lock()
	msgs := append([]llm.Message{{Role: "system", Content: sysPrompt}}, s.history...)
	msgs = append(msgs, llm.Message{Role: "user", Content: userText})
	s.history = append(s.history, llm.Message{Role: "user", Content: userText})
	s.mu.Unlock()

	turnBackend := ""
	if cfg.CascadeOn() {
		rpd := s.Deps.LLM.GroqTotalRemainingRPD()
		turnBackend = llm.RouteMessage(userText, rpd)
		if turnBackend == "local" {
			pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			err := s.Deps.LLM.PingLocal(pingCtx)
			cancel()
			if err != nil {
				slog.Warn("cascade: local LLM unreachable, using Groq", "err", err)
				turnBackend = "groq"
			}
		}
		llm.RecordRouterTurn(turnBackend)
	}

	llmStartPayload := map[string]any{}
	if turnBackend != "" {
		llmStartPayload["route"] = turnBackend
	}
	s.emit("llm_start", llmStartPayload)

	// Open ONE piper subprocess for this whole turn. All sentences flow
	// through it continuously — zero cold-start between phrases. Audio is
	// drained in a decoupled pump goroutine so LLM streaming never blocks
	// on playback.
	voice, verr := s.Deps.TTS.OpenVoice(ctx)
	if verr != nil {
		s.emit("error", map[string]any{"stage": "tts", "err": verr.Error()})
		s.setState(StateListening)
		return
	}
	audioDone := make(chan struct{})
	go s.pumpAudio(ctx, voice, audioDone)

	// Up to two tool-calling rounds then final answer.
	for round := 0; round < 3; round++ {
		req := llm.StreamRequest{
			Messages: msgs,
			Tools:    actions.Tools(),
			Stream:   true,
		}
		var stream <-chan llm.Delta
		if cfg.CascadeOn() && turnBackend != "" {
			stream = s.Deps.LLM.StreamForRoute(ctx, req, turnBackend)
		} else {
			stream = s.Deps.LLM.Stream(ctx, req)
		}

		var (
			fullText strings.Builder
			ttsBuf   strings.Builder
			tcs      []llm.ToolCall
			usage    *llm.Usage
		)

		for d := range stream {
			if d.Err != nil {
				if !isCancel(d.Err) {
					s.emit("error", map[string]any{"stage": "llm", "err": d.Err.Error()})
				}
				voice.Abort()
				<-audioDone
				s.setState(StateListening)
				return
			}
			if d.TextDelta != "" {
				clean := SanitizeAssistantStreamDelta(d.TextDelta)
				if clean != "" {
					fullText.WriteString(clean)
					ttsBuf.WriteString(clean)
					deltaPayload := map[string]any{"delta": clean}
					if turnBackend != "" {
						deltaPayload["route"] = turnBackend
					}
					s.emit("llm_delta", deltaPayload)
					// Pop every complete sentence ready to speak NOW and hand it
					// to piper. LLM keeps streaming without waiting for playback.
					s.drainSentences(&ttsBuf, voice, false)
				}
			}
			if len(d.ToolCalls) > 0 {
				tcs = append(tcs, d.ToolCalls...)
			}
			if d.Usage != nil {
				usage = d.Usage
			}
			if ctx.Err() != nil {
				voice.Abort()
				<-audioDone
				s.setState(StateListening)
				return
			}
		}
		if cfg.CascadeOn() && turnBackend == "groq" && usage != nil {
			llm.RecordGroqTokens(usage)
		}
		// End-of-stream: flush any trailing text as a single final utterance.
		s.drainSentences(&ttsBuf, voice, true)

		finalText := SanitizeAssistantFinal(strings.TrimSpace(fullText.String()))
		if finalText != "" {
			s.self.Record(finalText)
			trPayload := map[string]any{"speaker": "assistant", "text": finalText}
			if turnBackend != "" {
				trPayload["route"] = turnBackend
			}
			s.emit("transcript", trPayload)
			_ = s.Deps.Mem.AppendTurn(&memory.Turn{ConvID: s.ConvID, Speaker: "assistant", Text: finalText})
		}

		// Append ONE assistant message carrying both content and any tool_calls.
		if finalText != "" || len(tcs) > 0 {
			asst := llm.Message{Role: "assistant", Content: finalText}
			if len(tcs) > 0 {
				asst.ToolCalls = tcs
			}
			s.mu.Lock()
			s.history = append(s.history, asst)
			msgs = append(msgs, asst)
			s.mu.Unlock()
		}

		if len(tcs) == 0 {
			if usage != nil {
				s.emit("usage", usage)
			}
			break
		}

		// Execute tools, feed replies back, loop once more.
		for _, tc := range tcs {
			act, reply, err := s.exec.Execute(ctx, tc, s.ClientID, s.ConvID)
			if err != nil {
				reply = `{"ok":false,"err":"` + err.Error() + `"}`
			}
			if act != nil {
				s.emitAction(act)
			}
			s.mu.Lock()
			msgs = append(msgs, llm.Message{
				Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: reply,
			})
			s.mu.Unlock()
		}
	}

	// LLM is fully done. Tell piper "no more input" and let the audio pump
	// drain naturally while we return to listening.
	voice.Close()
	<-audioDone
	s.setState(StateListening)
}

// drainSentences pulls every complete sentence out of buf and streams them to
// piper. If force=true, any remaining non-empty text is also flushed (used at
// end-of-stream for "Hi!" / "Yeah." style short utterances that never hit the
// min-length threshold).
func (s *Session) drainSentences(buf *strings.Builder, voice *tts.Voice, force bool) {
	for {
		sent, rest, ok := extractSentence(buf.String())
		if !ok {
			break
		}
		buf.Reset()
		buf.WriteString(rest)
		_ = voice.Say(sent)
	}
	if force {
		tail := strings.TrimSpace(buf.String())
		if tail != "" {
			buf.Reset()
			_ = voice.Say(tail)
		}
	}
}

// pumpAudio drains voice.Chunks() and forwards PCM to the transport + VAD
// ref. Emits tts_start once audio actually begins, tts_end when the voice
// channel closes. Runs until the voice subprocess exits.
func (s *Session) pumpAudio(ctx context.Context, voice *tts.Voice, done chan<- struct{}) {
	defer close(done)
	var spoke bool
	defer func() {
		// Always clear the TTS gate when this pump exits (cancel/barge-in/abort/drain).
		// Otherwise MarkTTSSpeaking(true) sticks and VAD stays in "during TTS" mode,
		// blocking barge-in and warping segmentation on later turns.
		if spoke {
			s.vad.MarkTTSSpeaking(false)
			s.emit("tts_end", map[string]any{})
		}
	}()
	for c := range voice.Chunks() {
		if c.Err != nil {
			if !isCancel(c.Err) {
				s.emit("error", map[string]any{"stage": "tts", "err": c.Err.Error()})
			}
			continue
		}
		if len(c.PCM48k) == 0 {
			continue
		}
		if !spoke {
			spoke = true
			s.setState(StateSpeaking)
			s.vad.MarkTTSSpeaking(true)
			s.emit("tts_start", map[string]any{})
		}
		s.vad.WriteRef(downmixTo16k(c.PCM48k))
		if s.AudioOut != nil {
			s.AudioOut(c.PCM48k)
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// End closes the session and kicks off post-call extraction.
func (s *Session) End() {
	s.mu.Lock()
	if s.turnCancel != nil {
		s.turnCancel()
	}
	s.mu.Unlock()
	s.setState(StateEnded)
	_ = s.Deps.Mem.EndConversation(s.ConvID, summarize(s.history))
	go s.runExtraction(context.Background())
}

func (s *Session) runExtraction(ctx context.Context) {
	turns, err := s.Deps.Mem.ListTurns(s.ConvID)
	if err != nil || len(turns) == 0 {
		return
	}
	var transcript strings.Builder
	for _, t := range turns {
		fmt.Fprintf(&transcript, "%s: %s\n", t.Speaker, t.Text)
	}
	system := `You extract structured facts from an SDR-to-prospect phone call.
Return STRICT JSON of the form:
{"summary": "...", "category": "business_seller|business_buyer|exploring_financing|legal_compliance|valuation|general|unknown",
 "facts": [{"subject":"...","predicate":"...","object":"...","confidence":0.0}]}
No prose. JSON only.`
	user := transcript.String()
	ctx2, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	out, err := s.Deps.LLM.OneShotJSON(ctx2, system, user)
	if err != nil {
		return
	}
	var parsed struct {
		Summary  string `json:"summary"`
		Category string `json:"category"`
		Facts    []struct {
			Subject    string  `json:"subject"`
			Predicate  string  `json:"predicate"`
			Object     string  `json:"object"`
			Confidence float64 `json:"confidence"`
		} `json:"facts"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return
	}
	if parsed.Summary != "" {
		_ = s.Deps.Mem.EndConversation(s.ConvID, parsed.Summary)
	}
	day := time.Now().UTC().Format("2006-01-02")
	for _, f := range parsed.Facts {
		if strings.TrimSpace(f.Subject) == "" {
			continue
		}
		_ = s.Deps.Mem.InsertFact(&memory.Fact{
			ClientID:   s.ClientID,
			ConvID:     s.ConvID,
			Day:        day,
			Subject:    f.Subject,
			Predicate:  f.Predicate,
			Object:     f.Object,
			Category:   parsed.Category,
			Confidence: f.Confidence,
		})
	}
	s.emit("extraction_done", map[string]any{"facts": len(parsed.Facts)})
}

// ---------- helpers ----------

func (s *Session) emit(kind string, payload any) {
	if s.Emit == nil {
		return
	}
	b, _ := json.Marshal(payload)
	s.Emit(Event{Kind: kind, Payload: b, Time: float64(time.Now().UnixMilli()) / 1000.0})
}

func (s *Session) emitAction(a *memory.Action) {
	b, _ := json.Marshal(a)
	s.emit("action", json.RawMessage(b))
}

func strPtr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// extractSentence pulls the earliest complete sentence out of s. Returns
// (sentence, remainder, ok). A "sentence" ends at ., !, ?, or a hard break
// (\n\n) and must be at least minSentenceChars long — shorter fragments wait
// for more deltas so "I'm sorry," doesn't trigger a flush and "Mr. Smith"
// isn't split.
func extractSentence(s string) (sentence, remainder string, ok bool) {
	// Lower threshold = earlier first TTS chunk (better realtime feel) at the cost of
	// occasionally splitting mid-thought; 10 chars still skips very short noise.
	const minSentenceChars = 10
	runes := []rune(s)
	n := len(runes)
	for i := 0; i < n; i++ {
		r := runes[i]
		if r != '.' && r != '!' && r != '?' && r != '\n' {
			continue
		}
		// The punctuation must be followed by whitespace/EOL, or a line break
		// (this avoids splitting on things like "v1.2" or email "a.b@").
		terminator := false
		if i == n-1 {
			terminator = true
		} else {
			next := runes[i+1]
			if next == ' ' || next == '\n' || next == '\t' || next == '"' || next == '\'' {
				terminator = true
			}
		}
		if !terminator {
			continue
		}
		// Ignore very short fragments (Dr., Mr., Mrs., e.g., etc.).
		headLen := lenNonSpace(runes[:i+1])
		if headLen < minSentenceChars {
			continue
		}
		cut := i + 1
		// Consume any trailing closers (quote / paren).
		for cut < n && (runes[cut] == '"' || runes[cut] == '\'' || runes[cut] == ')') {
			cut++
		}
		return strings.TrimSpace(string(runes[:cut])),
			strings.TrimLeft(string(runes[cut:]), " \t\n"),
			true
	}
	return "", s, false
}

func lenNonSpace(rs []rune) int {
	n := 0
	for _, r := range rs {
		if r != ' ' && r != '\n' && r != '\t' {
			n++
		}
	}
	return n
}

func summarize(msgs []llm.Message) string {
	last := ""
	for i := len(msgs) - 1; i >= 0 && i > len(msgs)-4; i-- {
		if msgs[i].Content != "" {
			last = msgs[i].Content + " " + last
		}
	}
	last = strings.TrimSpace(last)
	if len(last) > 200 {
		last = last[:200] + "…"
	}
	return last
}

// downmixTo16k resamples 48k -> 16k (naive) so the echo gate can correlate.
func downmixTo16k(pcm48k []float32) []float32 {
	if len(pcm48k) == 0 {
		return pcm48k
	}
	out := make([]float32, len(pcm48k)/3)
	for i := 0; i < len(out); i++ {
		out[i] = pcm48k[i*3]
	}
	return out
}
