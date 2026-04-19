package llm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"scorpion/agent/internal/config"
)

// PromptContext is everything the system prompt needs to be produced for one conversation.
type PromptContext struct {
	PromptsDir   string
	AgentName    string
	ClientName   string
	ClientInfo   string
	Facts        []string
	KBExcerpts   string
	OverrideSys  string
	OverrideTone string
}

// BuildSystemPrompt composes the full system message. The AI plays the SDR (Jamie at Centergrowth)
// and the user speaks AS the client (prospect).
func BuildSystemPrompt(ctx PromptContext) string {
	var b strings.Builder
	b.WriteString(basePrompt(ctx))
	b.WriteString("\n\n## About the prospect you are calling\n")
	if ctx.ClientName != "" {
		fmt.Fprintf(&b, "Name: %s\n", ctx.ClientName)
	}
	if strings.TrimSpace(ctx.ClientInfo) != "" {
		fmt.Fprintf(&b, "Context:\n%s\n", ctx.ClientInfo)
	}
	if len(ctx.Facts) > 0 {
		b.WriteString("\n## What you already learned about this prospect\n")
		for _, f := range ctx.Facts {
			fmt.Fprintf(&b, "- %s\n", f)
		}
	}
	if strings.TrimSpace(ctx.KBExcerpts) != "" {
		b.WriteString("\n## Knowledge-base excerpts\n")
		b.WriteString(ctx.KBExcerpts)
		b.WriteString("\n")
	}
	b.WriteString("\nRules:\n")
	b.WriteString("- Keep each turn to ONE or TWO short spoken sentences (under 35 words).\n")
	b.WriteString("- Sound human. No markdown, no lists, no stage directions.\n")
	b.WriteString("- NEVER write XML, JSON, tool names, <function>, add_note, or similar in your reply text. Tools are invoked by the system only. Speak natural English.\n")
	b.WriteString("- If the prospect asks to reschedule, push back, or mentions a time, CALL the matching tool.\n")
	b.WriteString("- Only call a tool when you have enough specifics to fill it. Never invent names, dates, or numbers.\n")
	return b.String()
}

func basePrompt(ctx PromptContext) string {
	if strings.TrimSpace(ctx.OverrideSys) != "" {
		return ctx.OverrideSys
	}
	if ctx.PromptsDir != "" {
		if b, err := os.ReadFile(filepath.Join(ctx.PromptsDir, "sdr.txt")); err == nil && len(b) > 0 {
			return string(b)
		}
		if b, err := os.ReadFile(filepath.Join(ctx.PromptsDir, "system.txt")); err == nil && len(b) > 0 {
			return string(b)
		}
	}
	return defaultSDRPrompt
}

// CollectFacts formats facts for injection.
func CollectFacts(facts []FactLine) []string {
	out := make([]string, 0, len(facts))
	for _, f := range facts {
		out = append(out, fmt.Sprintf("[%s] %s %s %s", f.Category, f.Subject, f.Predicate, f.Object))
	}
	return out
}

type FactLine struct {
	Category, Subject, Predicate, Object string
}

// LoadTone reads prompts/tone.txt (used where callers want a separate tone line).
func LoadTone(cfg *config.Config, override string) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	if cfg == nil {
		return ""
	}
	if b, err := os.ReadFile(filepath.Join(cfg.PromptsDir, "tone.txt")); err == nil {
		return string(b)
	}
	return ""
}

const defaultSDRPrompt = `You are Jamie, a sales development rep at Centergrowth, a B2B lead generation and appointment-setting firm.
You are on a live phone call with a business owner. Your job: uncover pain, qualify fit, book or reschedule the next step.
You are friendly, professional, curious, and persistent — but never pushy.`
