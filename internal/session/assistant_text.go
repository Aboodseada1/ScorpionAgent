package session

import (
	"strings"
)

// SanitizeAssistantStreamDelta removes fragments some models leak into content
// instead of using native tool_calls (XML-ish tags, fake JSON). Applied per chunk
// before TTS and WebSocket llm_delta.
func SanitizeAssistantStreamDelta(s string) string {
	if s == "" {
		return ""
	}
	lo := strings.ToLower(s)
	// Drop from first leaked tool / XML marker onward for this chunk.
	cutters := []string{
		"</function", "<function", "add_note>", "log_qualification>",
		"draft_email>", "schedule_call>", "</tool", "<tool",
	}
	best := len(s)
	for _, m := range cutters {
		if i := strings.Index(lo, m); i >= 0 && i < best {
			best = i
		}
	}
	if best < len(s) {
		s = s[:best]
	}
	return strings.TrimRight(s, " \t\n\r")
}

// SanitizeAssistantFinal cleans the full assistant string for transcript + memory.
func SanitizeAssistantFinal(s string) string {
	s = strings.TrimSpace(s)
	lo := strings.ToLower(s)
	cutters := []string{
		"</function", "<function", "add_note>", "log_qualification>",
		"draft_email>", "schedule_call>", "</tool", "<tool",
	}
	best := len(s)
	for _, m := range cutters {
		if i := strings.Index(lo, m); i >= 0 && i < best {
			best = i
		}
	}
	if best < len(s) {
		s = strings.TrimSpace(s[:best])
	}
	return strings.TrimSpace(s)
}
