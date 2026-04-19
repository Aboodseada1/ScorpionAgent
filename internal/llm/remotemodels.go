package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"scorpion/agent/internal/config"
)

// ListRemoteModels calls the provider's OpenAI-compatible GET …/models and returns model IDs.
func (c *Client) ListRemoteModels(ctx context.Context, provider string) ([]string, error) {
	cfg := c.store.Snapshot()
	p := strings.ToLower(strings.TrimSpace(provider))
	var base, key string
	switch p {
	case "groq":
		keys := config.GroqKeyList(cfg)
		if len(keys) == 0 {
			return nil, fmt.Errorf("groq: API key not configured")
		}
		key = keys[0]
		base = cfg.GroqOpenAIBase
	case "gemini":
		key = cfg.GeminiAPIKey
		base = cfg.GeminiOpenAIBase
	default:
		return nil, fmt.Errorf("provider must be gemini or groq")
	}
	if strings.TrimSpace(key) == "" {
		return nil, fmt.Errorf("%s: API key not configured", p)
	}
	u := strings.TrimRight(base, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%s models %d: %s", p, resp.StatusCode, truncateBytes(body, 400))
	}
	var wrap struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		return nil, fmt.Errorf("%s: parse models: %w", p, err)
	}
	var ids []string
	for _, row := range wrap.Data {
		id := strings.TrimSpace(row.ID)
		if id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}
