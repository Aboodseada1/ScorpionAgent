// Package stt transcribes utterance audio via whisper.cpp's HTTP server.
// Falls back to a subprocess invocation if WHISPER_BIN is set.
package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"scorpion/agent/internal/audio"
	"scorpion/agent/internal/config"
)

type Result struct {
	Text       string  `json:"text"`
	DurationMs int64   `json:"duration_ms"`
	Language   string  `json:"language,omitempty"`
	Latency    float64 `json:"latency_ms"`
}

type Client interface {
	Transcribe(ctx context.Context, pcm16k []float32) (*Result, error)
	Ping(ctx context.Context) error
}

// WhisperHTTP calls a whisper.cpp server exposing /inference (the canonical example).
type WhisperHTTP struct {
	store *config.Store
	http  *http.Client
}

func NewWhisperHTTP(store *config.Store) *WhisperHTTP {
	return &WhisperHTTP{store: store, http: &http.Client{Timeout: 60 * time.Second}}
}

func (w *WhisperHTTP) Ping(ctx context.Context) error {
	cfg := w.store.Snapshot()
	base := strings.TrimRight(strings.TrimSpace(cfg.WhisperBaseURL), "/")
	if base == "" {
		return fmt.Errorf("whisper: WHISPER_BASE_URL is empty")
	}
	// Short client: status/warmup must not block on the 60s transcribe timeout.
	short := &http.Client{Timeout: 5 * time.Second}

	try := func(path string) (ok bool, err error) {
		u := base + path
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return false, err
		}
		resp, err := short.Do(req)
		if err != nil {
			return false, fmt.Errorf("GET %s: %w", u, err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
		switch resp.StatusCode {
		case http.StatusOK, http.StatusNoContent, http.StatusPartialContent:
			return true, nil
		case http.StatusServiceUnavailable:
			return false, fmt.Errorf("GET %s: model still loading (HTTP 503)", u)
		case http.StatusNotFound:
			return false, fmt.Errorf("GET %s: HTTP 404", u)
		default:
			return false, fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
		}
	}

	// Current whisper.cpp example server: GET /health → 200 JSON or 503 while loading.
	if ok, err := try("/health"); ok {
		return nil
	} else if err != nil && !strings.Contains(err.Error(), "HTTP 404") {
		return fmt.Errorf("whisper base %q: %w", base, err)
	}

	okRoot, errRoot := try("/")
	if okRoot {
		return nil
	}
	if errRoot != nil {
		return fmt.Errorf("whisper base %q: /health missing and root check failed: %w", base, errRoot)
	}
	return fmt.Errorf("whisper base %q: ping failed", base)
}

func (w *WhisperHTTP) Transcribe(ctx context.Context, pcm16k []float32) (*Result, error) {
	cfg := w.store.Snapshot()
	if len(pcm16k) < 800 { // <50ms - much lower threshold
		return &Result{Text: ""}, nil
	}
	wav := audio.Float32ToWav(pcm16k, 16000)

	// multipart: file=audio.wav, response_format=json, temperature=0.0
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	h := make(map[string][]string)
	h["Content-Disposition"] = []string{`form-data; name="file"; filename="audio.wav"`}
	h["Content-Type"] = []string{"audio/wav"}
	part, err := mw.CreatePart(h)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(wav); err != nil {
		return nil, err
	}
	_ = mw.WriteField("response_format", "json")
	_ = mw.WriteField("temperature", "0.0")
	_ = mw.WriteField("language", "en")
	// whisper.cpp example server runs inference on the model loaded at startup; this
	// field is kept for compatibility with other gateways that honor per-request model IDs.
	_ = mw.WriteField("model", cfg.WhisperModel)
	// Add word timestamps if available for better partial results
	_ = mw.WriteField("word_timestamps", "true")
	mw.Close()

	start := time.Now()
	url := strings.TrimRight(cfg.WhisperBaseURL, "/") + "/inference"
	req, err := http.NewRequestWithContext(ctx, "POST", url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := w.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("whisper server unreachable at %s (%w)", cfg.WhisperBaseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("whisper %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &Result{
		Text:       strings.TrimSpace(out.Text),
		DurationMs: int64(float64(len(pcm16k)) / 16.0),
		Latency:    float64(time.Since(start).Milliseconds()),
	}, nil
}

