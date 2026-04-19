// Package llm is a thin streaming client for any OpenAI-compatible /v1 chat endpoint
// (llama.cpp llama-server, ollama, vLLM, etc.) with tool-calling support.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"scorpion/agent/internal/config"
)

type Client struct {
	store *config.Store
	http  *http.Client

	groqMu   sync.Mutex
	groqKM   *GroqKeyManager
	groqKMFp string
}

func NewClient(store *config.Store) *Client {
	return &Client{store: store, http: &http.Client{Timeout: 120 * time.Second}}
}

func (c *Client) managerForGroq(cfg *config.Config) *GroqKeyManager {
	keys := config.GroqKeyList(*cfg)
	if len(keys) == 0 {
		return nil
	}
	fp := strings.Join(keys, "|")
	c.groqMu.Lock()
	defer c.groqMu.Unlock()
	if c.groqKM == nil || c.groqKMFp != fp {
		c.groqKM = NewGroqKeyManager(keys)
		c.groqKMFp = fp
	}
	return c.groqKM
}

// GroqDashboard returns rate-limit stats + totals for the admin UI.
func (c *Client) GroqDashboard(cfg *config.Config) (map[string]any, bool) {
	km := c.managerForGroq(cfg)
	if km == nil {
		return nil, false
	}
	return map[string]any{
		"stats":  km.GetStats(),
		"totals": km.GetTotals(),
	}, true
}

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolSpec struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type Delta struct {
	TextDelta  string     `json:"text_delta,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	Usage      *Usage     `json:"usage,omitempty"`
	Done       bool       `json:"done,omitempty"`
	Err        error      `json:"-"`
}

type StreamRequest struct {
	Messages   []Message
	Tools      []ToolSpec
	Stream     bool
	ToolChoice string // "" | "auto" | "none"
}

// Stream calls /chat/completions with SSE. Provider comes from config.EffectiveLLMProvider
// (local llama, Gemini API, or Groq OpenAI-compatible).
func (c *Client) Stream(ctx context.Context, req StreamRequest) <-chan Delta {
	cfg := c.store.Snapshot()
	switch config.EffectiveLLMProvider(cfg) {
	case "gemini":
		if cfg.GeminiAPIKey == "" {
			return singleErrDelta(fmt.Errorf("gemini: API key not configured (Settings → LLM)"))
		}
		return c.streamGemini(ctx, req, &cfg)
	case "groq":
		if !config.HasGroqCredentials(cfg) {
			return singleErrDelta(fmt.Errorf("groq: API key not configured (Settings → LLM or GROQ_API_KEYS)"))
		}
		return c.streamGroq(ctx, req, &cfg, "")
	}
	return c.streamLocal(ctx, req, &cfg, "")
}

func (c *Client) streamLocal(ctx context.Context, req StreamRequest, cfg *config.Config, modelOverride string) <-chan Delta {
	out := make(chan Delta, 8)
	go func() {
		defer close(out)
		model := strings.TrimSpace(modelOverride)
		if model == "" {
			model = strings.TrimSpace(cfg.LLMModel)
		}
		if model == "" {
			out <- Delta{Err: fmt.Errorf("llm: no model configured"), Done: true}
			return
		}
		buf := buildChatBody(cfg, model, req)
		resp, err := c.postChatCompletions(ctx, cfg.LLMBaseURL, "", buf, true)
		if err != nil {
			out <- Delta{Err: err, Done: true}
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			b, _ := io.ReadAll(resp.Body)
			out <- Delta{Err: fmt.Errorf("llm %d: %s", resp.StatusCode, string(b)), Done: true}
			return
		}
		if err := pumpSSEToDeltas(resp.Body, out); err != nil && !errors.Is(err, context.Canceled) {
			out <- Delta{Err: err, Done: true}
			return
		}
		out <- Delta{Done: true}
	}()
	return out
}

// StreamForRoute streams from local llama.cpp or Groq using cascade model IDs
// (CASCADE_LOCAL_MODEL / CASCADE_GROQ_MODEL). Used when config.CascadeOn().
func (c *Client) StreamForRoute(ctx context.Context, req StreamRequest, backend string) <-chan Delta {
	cfg := c.store.Snapshot()
	switch strings.ToLower(strings.TrimSpace(backend)) {
	case "local":
		return c.streamLocal(ctx, req, &cfg, cfg.CascadeLocalModel)
	case "groq":
		if !config.HasGroqCredentials(cfg) {
			return singleErrDelta(fmt.Errorf("groq: API key not configured"))
		}
		return c.streamGroq(ctx, req, &cfg, cfg.CascadeGroqModel)
	default:
		return c.Stream(ctx, req)
	}
}

// PingLocal checks the local OpenAI-compatible /v1/models endpoint.
func (c *Client) PingLocal(ctx context.Context) error {
	cfg := c.store.Snapshot()
	r, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(cfg.LLMBaseURL, "/")+"/models", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("local llm ping %d", resp.StatusCode)
	}
	return nil
}

// GroqTotalRemainingRPD returns the sum of remaining daily requests across Groq keys (0 if N/A).
func (c *Client) GroqTotalRemainingRPD() int {
	cfg := c.store.Snapshot()
	km := c.managerForGroq(&cfg)
	if km == nil {
		return 0
	}
	return km.GetTotals().TotalRemainingRPD
}

func singleErrDelta(err error) <-chan Delta {
	out := make(chan Delta, 1)
	out <- Delta{Err: err, Done: true}
	close(out)
	return out
}

// OneShotJSON calls the LLM without streaming and asks for a pure JSON object.
// Used for post-call extraction.
func (c *Client) OneShotJSON(ctx context.Context, system, user string) (string, error) {
	cfg := c.store.Snapshot()
	body := map[string]any{
		"model":       cfg.LLMModel,
		"temperature": 0.2,
		"max_tokens":  900,
		"stream":      false,
		"messages": []Message{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		"response_format": map[string]string{"type": "json_object"},
	}
	switch config.EffectiveLLMProvider(cfg) {
	case "gemini":
		if cfg.GeminiAPIKey == "" {
			return "", fmt.Errorf("gemini: API key not configured")
		}
		return c.oneShotGemini(ctx, &cfg, body)
	case "groq":
		if !config.HasGroqCredentials(cfg) {
			return "", fmt.Errorf("groq: API key not configured")
		}
		return c.oneShotGroq(ctx, &cfg, body)
	}
	buf, _ := json.Marshal(body)
	r, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(cfg.LLMBaseURL, "/")+"/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(r)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("llm %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", errors.New("empty LLM response")
	}
	return out.Choices[0].Message.Content, nil
}

// Warmup fires a 1-token chat completion with the configured model to load
// it into llama.cpp's kv-cache. Cheap on a hot server (~5ms), expensive on a
// cold one (~600-1500ms for Qwen2.5-1.5B on 3 threads) — which is exactly
// what we want to pay BEFORE the call starts instead of during the first
// turn.
func (c *Client) Warmup(ctx context.Context) error {
	cfg := c.store.Snapshot()
	switch config.EffectiveLLMProvider(cfg) {
	case "gemini":
		if cfg.GeminiAPIKey == "" {
			return fmt.Errorf("gemini: API key not configured")
		}
		return c.warmupGemini(ctx, &cfg)
	case "groq":
		if !config.HasGroqCredentials(cfg) {
			return fmt.Errorf("groq: API key not configured")
		}
		return c.warmupGroq(ctx, &cfg)
	}
	body := map[string]any{
		"model":       cfg.LLMModel,
		"temperature": 0.0,
		"max_tokens":  1,
		"stream":      false,
		"messages": []Message{
			{Role: "system", Content: "ok"},
			{Role: "user", Content: "ok"},
		},
	}
	buf, _ := json.Marshal(body)
	r, err := http.NewRequestWithContext(ctx, "POST",
		strings.TrimRight(cfg.LLMBaseURL, "/")+"/chat/completions",
		bytes.NewReader(buf))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("llm warmup %d", resp.StatusCode)
	}
	return nil
}

// Ping checks LLM availability (local /v1/models or Gemini /openai/models).
func (c *Client) Ping(ctx context.Context) error {
	cfg := c.store.Snapshot()
	switch config.EffectiveLLMProvider(cfg) {
	case "gemini":
		if cfg.GeminiAPIKey == "" {
			return fmt.Errorf("gemini: API key not configured")
		}
		return c.pingGemini(ctx, &cfg)
	case "groq":
		if !config.HasGroqCredentials(cfg) {
			return fmt.Errorf("groq: API key not configured")
		}
		return c.pingGroq(ctx, &cfg)
	}
	r, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(cfg.LLMBaseURL, "/")+"/models", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("llm ping %d", resp.StatusCode)
	}
	return nil
}
