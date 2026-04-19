package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"scorpion/agent/internal/config"
)

func (c *Client) groqChatCompletions(ctx context.Context, cfg *config.Config, body []byte, stream bool) (*http.Response, int, error) {
	km := c.managerForGroq(cfg)
	if km == nil {
		return nil, -1, fmt.Errorf("groq: no API keys configured")
	}
	n := km.KeyCount()
	if n == 0 {
		return nil, -1, fmt.Errorf("groq: no API keys configured")
	}
	maxAttempts := n * 4
	for attempt := 0; attempt < maxAttempts; attempt++ {
		idx, key, ok := km.GetKey()
		if !ok {
			return nil, -1, fmt.Errorf("groq: all keys exhausted")
		}
		resp, err := c.postChatCompletions(ctx, cfg.GroqOpenAIBase, key, body, stream)
		if err != nil {
			return nil, idx, err
		}
		km.UpdateFromHeaders(idx, resp.Header)
		if resp.StatusCode == http.StatusTooManyRequests {
			km.Handle429(idx)
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			continue
		}
		if resp.StatusCode == http.StatusOK {
			km.MarkSuccess(idx)
		}
		return resp, idx, nil
	}
	return nil, -1, fmt.Errorf("groq: rate limited on all keys")
}

func (c *Client) streamGroq(ctx context.Context, req StreamRequest, cfg *config.Config, modelOverride string) <-chan Delta {
	out := make(chan Delta, 8)
	go func() {
		defer close(out)
		model := strings.TrimSpace(modelOverride)
		if model == "" {
			model = strings.TrimSpace(cfg.LLMModel)
		}
		if model == "" {
			out <- Delta{Err: fmt.Errorf("groq: choose a model in Settings"), Done: true}
			return
		}
		buf := buildChatBody(cfg, model, req)
		resp, _, err := c.groqChatCompletions(ctx, cfg, buf, true)
		if err != nil {
			out <- Delta{Err: err, Done: true}
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized {
			out <- Delta{Err: fmt.Errorf("groq: invalid API key (401)"), Done: true}
			return
		}
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			out <- Delta{Err: fmt.Errorf("groq %d: %s", resp.StatusCode, truncateBytes(b, 500)), Done: true}
			return
		}
		if err := pumpSSEToDeltas(resp.Body, out); err != nil {
			out <- Delta{Err: fmt.Errorf("groq stream: %w", err), Done: true}
			return
		}
		out <- Delta{Done: true}
	}()
	return out
}

func (c *Client) oneShotGroq(ctx context.Context, cfg *config.Config, body map[string]any) (string, error) {
	body["stream"] = false
	if strings.TrimSpace(cfg.LLMModel) == "" {
		return "", fmt.Errorf("groq: no model configured")
	}
	body["model"] = strings.TrimSpace(cfg.LLMModel)
	buf, _ := json.Marshal(body)
	resp, _, err := c.groqChatCompletions(ctx, cfg, buf, false)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("groq: invalid API key (401)")
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("groq %d: %s", resp.StatusCode, string(b))
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
		return "", fmt.Errorf("groq: empty response")
	}
	return out.Choices[0].Message.Content, nil
}

func (c *Client) warmupGroq(ctx context.Context, cfg *config.Config) error {
	if strings.TrimSpace(cfg.LLMModel) == "" {
		return fmt.Errorf("groq: no model for warmup")
	}
	body := map[string]any{
		"model":       strings.TrimSpace(cfg.LLMModel),
		"temperature": 0.0,
		"max_tokens":  1,
		"stream":      false,
		"messages": []Message{
			{Role: "user", Content: "ok"},
		},
	}
	buf, _ := json.Marshal(body)
	resp, _, err := c.groqChatCompletions(ctx, cfg, buf, false)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("groq warmup %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) pingGroq(ctx context.Context, cfg *config.Config) error {
	km := c.managerForGroq(cfg)
	if km == nil {
		return fmt.Errorf("groq: API key not configured")
	}
	u := strings.TrimRight(cfg.GroqOpenAIBase, "/") + "/models"
	max := km.KeyCount() * 3
	for attempt := 0; attempt < max; attempt++ {
		idx, key, ok := km.GetKey()
		if !ok {
			return fmt.Errorf("groq: all keys exhausted")
		}
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		r.Header.Set("Authorization", "Bearer "+key)
		resp, err := c.http.Do(r)
		if err != nil {
			return err
		}
		km.UpdateFromHeaders(idx, resp.Header)
		if resp.StatusCode == http.StatusTooManyRequests {
			km.Handle429(idx)
			resp.Body.Close()
			continue
		}
		if resp.StatusCode >= 400 {
			slog.Warn("groq ping", "status", resp.StatusCode)
			resp.Body.Close()
			return fmt.Errorf("groq ping %d", resp.StatusCode)
		}
		km.MarkSuccess(idx)
		resp.Body.Close()
		return nil
	}
	return fmt.Errorf("groq ping: rate limited on all keys")
}
