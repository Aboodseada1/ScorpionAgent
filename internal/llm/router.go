package llm

import (
	"log/slog"
	"regexp"
	"strings"
	"unicode"
)

// RPDLowThreshold: when total remaining requests across keys falls below this
// (10% of 5×14400), simple messages prefer local.
const RPDLowThreshold = 7200

var (
	reSimpleGreeting = regexp.MustCompile(`^(hi|hello|hey|yes|no|ok|okay|sure|thanks|thank you|bye|goodbye)[\s\.,!]*$`)
	reConfirm        = regexp.MustCompile(`^(correct|right|exactly|perfect|got it|i see|understood|makes sense)[\s\.,!]*$`)
	reRepeat         = regexp.MustCompile(`^(repeat|say that again|what|huh|pardon|sorry)[\s\?,!]*$`)
	reShortYN        = regexp.MustCompile(`^(is it|are you|do you|can you|will you).{0,30}\?$`)
)

type patternFn func(string) bool

// simpleLocalPatterns: only **single-line** acknowledgments / fillers — route these to
// local llama to save Groq quota. Do **not** use a broad "≤N words" rule: short chat
// (e.g. "Hello, how are you doing today?") must stay on Groq for low latency.
var simpleLocalPatterns = []patternFn{
	func(msg string) bool { return reSimpleGreeting.MatchString(msg) },
	func(msg string) bool { return reConfirm.MatchString(msg) },
	func(msg string) bool { return reRepeat.MatchString(msg) },
	func(msg string) bool { return reShortYN.MatchString(msg) },
}

var reComplexKeywords = regexp.MustCompile(`\b(explain|calculate|analyze|compare|difference|how does|why|because|therefore|contract|policy|details|schedule|appointment|price|cost|plan)\b`)

// complexPatterns run first; any match forces Groq.
var complexPatterns = []patternFn{
	func(msg string) bool { return wordCount(msg) > 20 },
	func(msg string) bool { return reComplexKeywords.MatchString(msg) },
	func(msg string) bool {
		// Multi-part questions
		if strings.Count(msg, "?") >= 2 {
			return true
		}
		return regexp.MustCompile(`\band\b.*\?.*\band\b`).MatchString(msg)
	},
	func(msg string) bool {
		hasDigit := false
		for _, r := range msg {
			if unicode.IsDigit(r) {
				hasDigit = true
				break
			}
		}
		if !hasDigit {
			return false
		}
		// Numbers and math / currency
		if strings.ContainsAny(msg, "$€£%") {
			return true
		}
		if regexp.MustCompile(`(?i)\bpercent\b`).MatchString(msg) {
			return true
		}
		digitCount := 0
		for _, r := range msg {
			if unicode.IsDigit(r) {
				digitCount++
			}
		}
		return digitCount >= 2
	},
}

func wordCount(msg string) int {
	fields := strings.Fields(strings.TrimSpace(msg))
	if len(fields) == 0 {
		return 0
	}
	return len(fields)
}

func matchesAny(patterns []patternFn, msg string) bool {
	for _, p := range patterns {
		if p(msg) {
			return true
		}
	}
	return false
}

func isSimpleMessage(msg string) bool {
	if matchesAny(simpleLocalPatterns, msg) {
		return true
	}
	// Under low Groq quota, also treat very short utterances as simple.
	return wordCount(msg) <= 7
}

func truncatePreview(s string, n int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// RouteMessage chooses "local" vs "groq" for cascade mode. totalRemainingRPD is
// the sum of remaining daily requests across Groq keys (0 if unknown).
func RouteMessage(msg string, totalRemainingRPD int) (backend string) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		slog.Info("llm router", "backend", "groq", "reason", "empty_message")
		return "groq"
	}

	lowQuota := totalRemainingRPD < RPDLowThreshold && totalRemainingRPD >= 0
	if lowQuota && isSimpleMessage(msg) {
		slog.Info("llm router", "backend", "local", "reason", "low_quota_simple", "total_remaining_rpd", totalRemainingRPD, "preview", truncatePreview(msg, 72))
		return "local"
	}
	if lowQuota && !isSimpleMessage(msg) {
		slog.Warn("llm router: Groq quota low; routing complex message to Groq anyway", "total_remaining_rpd", totalRemainingRPD, "preview", truncatePreview(msg, 72))
	}

	if matchesAny(complexPatterns, msg) {
		slog.Info("llm router", "backend", "groq", "reason", "complex", "preview", truncatePreview(msg, 72))
		return "groq"
	}
	if matchesAny(simpleLocalPatterns, msg) {
		slog.Info("llm router", "backend", "local", "reason", "tight_ack", "preview", truncatePreview(msg, 72))
		return "local"
	}
	slog.Info("llm router", "backend", "groq", "reason", "default_medium", "preview", truncatePreview(msg, 72))
	return "groq"
}
