package api

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"scorpion/agent/internal/audio"
	"scorpion/agent/internal/config"
	"scorpion/agent/internal/downloader"
	"scorpion/agent/internal/llmmodels"
	"scorpion/agent/internal/tts"
	"scorpion/agent/internal/voices"
)

const voiceSampleText = "Hi, this is your Scorpion voice agent. I can negotiate, reschedule, and remember every detail of our calls."

// ---------- Voices ----------

func handleListVoices(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := d.Voices.List()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		cfg := d.Store.Snapshot()
		for i := range list {
			if sameVoice(cfg, list[i]) {
				list[i].Name = list[i].Name // no-op; placeholder
			}
		}
		writeJSON(w, 200, map[string]any{
			"voices":       list,
			"active_id":    activeVoiceID(cfg, list),
			"catalog":      voices.Catalog(),
			"install_dir":  d.Voices.PrimaryDir(),
		})
	}
}

func handleSelectVoice(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		v, err := d.Voices.Get(id)
		if err != nil {
			writeErr(w, 404, err.Error())
			return
		}
		delta := config.Overrides{
			PiperModel:  strPtr(v.ModelPath),
			PiperConfig: strPtr(v.ConfigPath),
		}
		if err := d.Store.Patch(delta); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "voice": v})
	}
}

// handleVoiceSample streams a short WAV rendered with the requested voice.
// Temporarily overrides the store's Piper settings for the duration of synth.
func handleVoiceSample(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		v, err := d.Voices.Get(id)
		if err != nil {
			writeErr(w, 404, err.Error())
			return
		}
		// Use a disposable pool pointed at a scratch store so we don't
		// clobber the live voice during a sample click.
		base := *d.Store.Base()
		base.PiperModel = v.ModelPath
		base.PiperConfig = v.ConfigPath
		scratch := config.NewEphemeralStore(&base)
		pool := tts.NewPool(scratch)

		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		// Collect all PCM48k chunks into one WAV response.
		ch := pool.Synth(ctx, voiceSampleText)
		all := make([]float32, 0, 48000*6)
		for c := range ch {
			if c.Err != nil {
				writeErr(w, 500, c.Err.Error())
				return
			}
			all = append(all, c.PCM48k...)
		}
		wav := audio.Float32ToWav(all, 48000)
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Length", intToString(len(wav)))
		_, _ = w.Write(wav)
	}
}

// ---------- LLM models ----------

func handleListModels(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Remote catalog (Gemini / Groq OpenAI-compatible /v1/models). Same auth as PATCH settings.
		// Also registered at GET /api/llm/remote-models — this query form works if that path 404s behind an old binary.
		if rp := strings.TrimSpace(r.URL.Query().Get("remote_provider")); rp != "" {
			if !checkAdmin(d, r) {
				writeErr(w, 403, "admin token required")
				return
			}
			serveRemoteLLMModelList(d, w, r, rp)
			return
		}
		list, err := d.Models.List()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		active, _ := d.Models.ActivePath()
		writeJSON(w, 200, map[string]any{
			"models":      list,
			"active_path": active,
			"catalog":     llmmodels.Catalog(),
			"install_dir": d.Models.PrimaryDir(),
		})
	}
}

func handleSelectModel(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		list, _ := d.Models.List()
		var target string
		for _, m := range list {
			if m.ID == id {
				target = m.File
				break
			}
		}
		if target == "" {
			writeErr(w, 404, "model not found")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := d.Models.SetActive(ctx, target, d.Store.Snapshot().LLMBaseURL); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		// Reflect in overrides too, so /api/status shows the chosen model.
		name := filepath.Base(target)
		_ = d.Store.Patch(config.Overrides{LLMModel: strPtr(name)})
		writeJSON(w, 200, map[string]any{"ok": true, "active_path": target})
	}
}

// ---------- Downloads ----------

type downloadReq struct {
	Kind       string `json:"kind"`        // "voice" | "llm"
	CatalogID  string `json:"catalog_id"`  // optional — pick from curated list
	URL        string `json:"url"`         // custom URL (if no catalog_id)
	Filename   string `json:"filename"`    // required if custom URL for llm
	VoiceID    string `json:"voice_id"`    // for `custom voice`
}

func handleStartDownload(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req downloadReq
		if err := readJSON(r, &req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		var job *downloader.Job
		var err error
		switch req.Kind {
		case "voice":
			job, err = queueVoiceDownload(d, req)
		case "llm":
			job, err = queueLLMDownload(d, req)
		default:
			err = errStr("kind must be 'voice' or 'llm'")
		}
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 202, job)
	}
}

func queueVoiceDownload(d *Deps, req downloadReq) (*downloader.Job, error) {
	var entry *voices.CatalogEntry
	if req.CatalogID != "" {
		for _, e := range voices.Catalog() {
			if e.ID == req.CatalogID {
				c := e
				entry = &c
				break
			}
		}
	}
	dir := d.Voices.PrimaryDir()
	if dir == "" {
		return nil, errStr("voices dir not configured")
	}
	var mainURL, cfgURL, id, label string
	if entry != nil {
		mainURL = entry.ModelURL
		cfgURL = entry.ConfigURL
		id = entry.ID
		label = entry.Name + " (" + entry.Language + "/" + entry.Quality + ")"
	} else {
		if req.URL == "" || req.VoiceID == "" {
			return nil, errStr("url and voice_id required for custom voice")
		}
		mainURL = req.URL
		cfgURL = strings.TrimSuffix(req.URL, ".onnx") + ".onnx.json"
		if !strings.HasSuffix(mainURL, ".onnx") {
			return nil, errStr("voice url must end in .onnx")
		}
		id = req.VoiceID
		label = req.VoiceID
	}
	modelDest := filepath.Join(dir, id+".onnx")
	cfgDest := filepath.Join(dir, id+".onnx.json")
	return d.Downloads.Enqueue("voice", label, mainURL, modelDest, cfgURL, cfgDest)
}

func queueLLMDownload(d *Deps, req downloadReq) (*downloader.Job, error) {
	var entry *llmmodels.CatalogEntry
	if req.CatalogID != "" {
		for _, e := range llmmodels.Catalog() {
			if e.ID == req.CatalogID {
				c := e
				entry = &c
				break
			}
		}
	}
	dir := d.Models.PrimaryDir()
	if dir == "" {
		return nil, errStr("models dir not configured")
	}
	var url, id, label, filename string
	if entry != nil {
		url = entry.URL
		id = entry.ID
		label = entry.Name
		filename = filepath.Base(entry.URL)
	} else {
		if req.URL == "" || req.Filename == "" {
			return nil, errStr("url and filename required for custom model")
		}
		url = req.URL
		id = strings.TrimSuffix(req.Filename, ".gguf")
		label = req.Filename
		filename = req.Filename
	}
	if !strings.HasSuffix(strings.ToLower(filename), ".gguf") {
		return nil, errStr("filename must end in .gguf")
	}
	subdir := filepath.Join(dir, id)
	dest := filepath.Join(subdir, filename)
	return d.Downloads.Enqueue("llm", label, url, dest, "", "")
}

func handleListDownloads(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"downloads": d.Downloads.List()})
	}
}

func handleDownloadSSE(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeErr(w, 500, "streaming unsupported")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		ch, unsub := d.Downloads.Subscribe()
		defer unsub()
		// keep-alive ping every 15s
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()

		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = io.WriteString(w, ": ping\n\n")
				flusher.Flush()
			case j, ok := <-ch:
				if !ok {
					return
				}
				b, _ := json.Marshal(j)
				_, _ = io.WriteString(w, "data: ")
				_, _ = w.Write(b)
				_, _ = io.WriteString(w, "\n\n")
				flusher.Flush()
			}
		}
	}
}

func handleCancelDownload(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d.Downloads.Cancel(chi.URLParam(r, "id"))
		writeJSON(w, 200, map[string]any{"ok": true})
	}
}

// ---------- Metrics ----------

func handleMetrics(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, d.Metrics.Snapshot())
	}
}

func handleMetricsSSE(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeErr(w, 500, "streaming unsupported")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		ch, unsub := d.Metrics.Subscribe()
		defer unsub()
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()

		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = io.WriteString(w, ": ping\n\n")
				flusher.Flush()
			case snap, ok := <-ch:
				if !ok {
					return
				}
				b, _ := json.Marshal(snap)
				_, _ = io.WriteString(w, "data: ")
				_, _ = w.Write(b)
				_, _ = io.WriteString(w, "\n\n")
				flusher.Flush()
			}
		}
	}
}

// ---------- helpers ----------

type errString string

func (e errString) Error() string { return string(e) }
func errStr(s string) error       { return errString(s) }

func strPtr(s string) *string { return &s }

func intToString(n int) string {
	// avoid strconv in hot path
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func sameVoice(cfg config.Config, v voices.Voice) bool {
	return cfg.PiperModel == v.ModelPath
}

func activeVoiceID(cfg config.Config, list []voices.Voice) string {
	for _, v := range list {
		if sameVoice(cfg, v) {
			return v.ID
		}
	}
	return ""
}

// Used only for little bit of framing test.
var _ = binary.LittleEndian
