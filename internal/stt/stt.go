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
	r, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(cfg.WhisperBaseURL, "/")+"/", nil)
	if err != nil {
		return err
	}
	resp, err := w.http.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func (w *WhisperHTTP) Transcribe(ctx context.Context, pcm16k []float32) (*Result, error) {
	cfg := w.store.Snapshot()
	if len(pcm16k) < 1600 { // <100ms
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

