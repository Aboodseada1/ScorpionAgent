package llm

import (
	"sync/atomic"
)

var (
	routerLocalTurns  atomic.Int64
	routerGroqTurns   atomic.Int64
	routerGroqTokens  atomic.Int64
)

// RecordRouterTurn increments per completed user→assistant turn by backend.
func RecordRouterTurn(backend string) {
	switch backend {
	case "local":
		routerLocalTurns.Add(1)
	case "groq":
		routerGroqTurns.Add(1)
	}
}

// RecordGroqTokens adds token usage from a Groq completion (best-effort).
func RecordGroqTokens(u *Usage) {
	if u == nil {
		return
	}
	if u.TotalTokens > 0 {
		routerGroqTokens.Add(int64(u.TotalTokens))
	} else {
		routerGroqTokens.Add(int64(u.PromptTokens + u.CompletionTokens))
	}
}

// RouterStatsSnapshot returns aggregate routing counters for the dashboard.
func RouterStatsSnapshot() map[string]any {
	loc := routerLocalTurns.Load()
	gr := routerGroqTurns.Load()
	tok := routerGroqTokens.Load()
	total := loc + gr
	var localPct, groqPct float64
	if total > 0 {
		localPct = float64(loc) / float64(total) * 100
		groqPct = float64(gr) / float64(total) * 100
	}
	// "Quota saved" ≈ share handled locally (no Groq tokens for those).
	var quotaSavedPct float64
	if total > 0 {
		quotaSavedPct = localPct
	}
	return map[string]any{
		"local_requests":    loc,
		"groq_requests":     gr,
		"groq_tokens":       tok,
		"local_pct":       localPct,
		"groq_pct":        groqPct,
		"quota_saved_pct": quotaSavedPct,
		"total_routed":    total,
	}
}
