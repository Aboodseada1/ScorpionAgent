package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"scorpion/agent/internal/config"
)

// streamGemini tries each model in cfg.GeminiModels until one returns HTTP 200, then streams SSE.
func (c *Client) streamGemini(ctx context.Context, req StreamRequest, cfg *config.Config) <-chan Delta {
	out := make(chan Delta, 8)
	go func() {
		defer close(out)
		models := cfg.GeminiModels
		if len(models) == 0 {
			models = []string{"gemini-3-flash-preview", "gemini-2.5-flash", "gemini-2.5-flash-lite"}
		}
		bodyTemplate := buildChatBody(cfg, "", req)
		for _, model := range models {
			body := injectModel(bodyTemplate, model)
			resp, err := c.postChatCompletions(ctx, cfg.GeminiOpenAIBase, cfg.GeminiAPIKey, body, true)
			if err != nil {
				slog.Warn("gemini request failed", "model", model, "err", err)
				continue
			}
			if resp.StatusCode == http.StatusUnauthorized {
				resp.Body.Close()
				out <- Delta{Err: fmt.Errorf("gemini: invalid or missing API key (401)"), Done: true}
				return
			}
			if resp.StatusCode != http.StatusOK {
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				slog.Warn("gemini model failed, trying fallback", "model", model, "status", resp.StatusCode, "body", truncateBytes(b, 400))
				continue
			}
			err = pumpSSEToDeltas(resp.Body, out)
			resp.Body.Close()
			if err != nil {
				out <- Delta{Err: fmt.Errorf("gemini stream: %w", err), Done: true}
				return
			}
			out <- Delta{Done: true}
			return
		}
		out <- Delta{Err: fmt.Errorf("gemini: all configured models failed — check GEMINI_MODEL_FALLBACK and API access"), Done: true}
	}()
	return out
}

func truncateBytes(b []byte, n int) string {
	s := string(b)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// buildChatBody marshals the chat/completions JSON. If modelOverride is empty, uses cfg.LLMModel.
func buildChatBody(cfg *config.Config, modelOverride string, req StreamRequest) []byte {
	model := modelOverride
	if model == "" {
		model = cfg.LLMModel
	}
	body := map[string]any{
		"model":       model,
		"temperature": cfg.LLMTemperature,
		"max_tokens":  cfg.LLMMaxTokens,
		"stream":      true,
		"messages":    req.Messages,
	}
	if len(req.Tools) > 0 {
		body["tools"] = req.Tools
		if req.ToolChoice != "" {
			body["tool_choice"] = req.ToolChoice
		} else {
			body["tool_choice"] = "auto"
		}
	}
	buf, _ := json.Marshal(body)
	return buf
}

func injectModel(bodyJSON []byte, model string) []byte {
	var m map[string]any
	if json.Unmarshal(bodyJSON, &m) != nil {
		return bodyJSON
	}
	m["model"] = model
	buf, err := json.Marshal(m)
	if err != nil {
		return bodyJSON
	}
	return buf
}

func (c *Client) postChatCompletions(ctx context.Context, baseURL, apiKey string, body []byte, stream bool) (*http.Response, error) {
	u := strings.TrimRight(baseURL, "/") + "/chat/completions"
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	if stream {
		r.Header.Set("Accept", "text/event-stream")
	}
	if apiKey != "" {
		r.Header.Set("Authorization", "Bearer "+apiKey)
	}
	return c.http.Do(r)
}

// oneShotGemini non-streaming completion with JSON response, trying each model.
func (c *Client) oneShotGemini(ctx context.Context, cfg *config.Config, body map[string]any) (string, error) {
	body["stream"] = false
	models := cfg.GeminiModels
	if len(models) == 0 {
		models = []string{"gemini-3-flash-preview", "gemini-2.5-flash", "gemini-2.5-flash-lite"}
	}
	for _, model := range models {
		body["model"] = model
		buf, _ := json.Marshal(body)
		resp, err := c.postChatCompletions(ctx, cfg.GeminiOpenAIBase, cfg.GeminiAPIKey, buf, false)
		if err != nil {
			slog.Warn("gemini one-shot request failed", "model", model, "err", err)
			continue
		}
		if resp.StatusCode == http.StatusUnauthorized {
			resp.Body.Close()
			return "", fmt.Errorf("gemini: invalid API key (401)")
		}
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			slog.Warn("gemini one-shot model failed", "model", model, "status", resp.StatusCode, "body", truncateBytes(b, 300))
			continue
		}
		var out struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		err = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if err != nil {
			continue
		}
		if len(out.Choices) == 0 {
			continue
		}
		return out.Choices[0].Message.Content, nil
	}
	return "", fmt.Errorf("gemini: all models failed for one-shot request")
}

func (c *Client) warmupGemini(ctx context.Context, cfg *config.Config) error {
	models := cfg.GeminiModels
	if len(models) == 0 {
		models = []string{"gemini-3-flash-preview", "gemini-2.5-flash", "gemini-2.5-flash-lite"}
	}
	for _, model := range models {
		body := map[string]any{
			"model":       model,
			"temperature": 0.0,
			"max_tokens":  1,
			"stream":      false,
			"messages": []Message{
				{Role: "user", Content: "ok"},
			},
		}
		buf, _ := json.Marshal(body)
		resp, err := c.postChatCompletions(ctx, cfg.GeminiOpenAIBase, cfg.GeminiAPIKey, buf, false)
		if err != nil {
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return nil
		}
	}
	return fmt.Errorf("gemini warmup: no model responded OK")
}

func (c *Client) pingGemini(ctx context.Context, cfg *config.Config) error {
	u := strings.TrimRight(cfg.GeminiOpenAIBase, "/") + "/models"
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+cfg.GeminiAPIKey)
	resp, err := c.http.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("gemini ping %d", resp.StatusCode)
	}
	return nil
}
