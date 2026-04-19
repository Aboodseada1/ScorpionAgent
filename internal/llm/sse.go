package llm

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// pumpSSEToDeltas reads an OpenAI-style chat.completion SSE stream into out.
// The caller must send Delta{Done: true} (or Err) after this returns.
func pumpSSEToDeltas(body io.Reader, out chan<- Delta) error {
	toolCallsByIdx := map[int]*ToolCall{}
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			return nil
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *Usage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) == 0 {
			if chunk.Usage != nil {
				out <- Delta{Usage: chunk.Usage}
			}
			continue
		}
		ch := chunk.Choices[0]
		if ch.Delta.Content != "" {
			out <- Delta{TextDelta: ch.Delta.Content}
		}
		for _, tc := range ch.Delta.ToolCalls {
			cur, ok := toolCallsByIdx[tc.Index]
			if !ok {
				cur = &ToolCall{ID: tc.ID, Type: "function"}
				toolCallsByIdx[tc.Index] = cur
			}
			if tc.Function.Name != "" {
				cur.Function.Name += tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				cur.Function.Arguments += tc.Function.Arguments
			}
			if tc.ID != "" {
				cur.ID = tc.ID
			}
		}
		if ch.FinishReason == "tool_calls" {
			var tcs []ToolCall
			for _, v := range toolCallsByIdx {
				tcs = append(tcs, *v)
			}
			if len(tcs) > 0 {
				out <- Delta{ToolCalls: tcs}
			}
		}
		if chunk.Usage != nil {
			out <- Delta{Usage: chunk.Usage}
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
