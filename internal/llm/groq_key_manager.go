package llm

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Groq free-tier reference limits (used for progress bars and defaults).
const (
	groqRPDLimit  = 14400
	groqRPMLimit  = 30
	groqTPMLimit  = 6000
	groqTPDLimit  = 500_000
	groqKeySuffix = 4
)

type groqKeySlot struct {
	Key string

	RemainingRequestsDay int
	RemainingRequestsMin int
	RemainingTokensMin   int
	RemainingTokensDay   int
	ResetRequests        *string
	ResetTokens          *string
	LastUpdated          *time.Time
	Consecutive429s      int
	IsExhausted          bool
}

// GroqKeyManager tracks per-key Groq rate-limit state from response headers and
// selects the best key for each request.
type GroqKeyManager struct {
	mu sync.Mutex

	keys         []*groqKeySlot
	currentIndex int
}

// NewGroqKeyManager builds a manager from raw API keys (server-side only).
func NewGroqKeyManager(keys []string) *GroqKeyManager {
	m := &GroqKeyManager{currentIndex: 0}
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		m.keys = append(m.keys, &groqKeySlot{
			Key:                  k,
			RemainingRequestsDay: groqRPDLimit,
			RemainingRequestsMin: groqRPMLimit,
			RemainingTokensMin:   groqTPMLimit,
			RemainingTokensDay:   groqTPDLimit,
		})
	}
	return m
}

// KeyCount returns the number of configured keys.
func (m *GroqKeyManager) KeyCount() int {
	if m == nil {
		return 0
	}
	return len(m.keys)
}

// GetKey picks the best available key: skip exhausted, prefer highest remaining
// daily requests; skip keys with RPM headroom ≤ 2 unless none remain.
func (m *GroqKeyManager) GetKey() (index int, key string, ok bool) {
	if m == nil || len(m.keys) == 0 {
		return -1, "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	type cand struct {
		i   int
		rpd int
	}
	var candidates []cand
	for i, ks := range m.keys {
		if ks.IsExhausted {
			continue
		}
		if ks.RemainingRequestsMin <= 2 {
			continue
		}
		candidates = append(candidates, cand{i: i, rpd: ks.RemainingRequestsDay})
	}
	pick := func(list []cand) (int, bool) {
		if len(list) == 0 {
			return -1, false
		}
		best := list[0]
		for _, c := range list[1:] {
			if c.rpd > best.rpd {
				best = c
			}
		}
		return best.i, true
	}
	idx, ok := pick(candidates)
	if !ok {
		// Fallback: ignore RPM floor, still skip exhausted.
		var loose []cand
		for i, ks := range m.keys {
			if ks.IsExhausted {
				continue
			}
			loose = append(loose, cand{i: i, rpd: ks.RemainingRequestsDay})
		}
		idx, ok = pick(loose)
	}
	if !ok {
		return -1, "", false
	}
	m.currentIndex = idx
	return idx, m.keys[idx].Key, true
}

// UpdateFromHeaders updates state from Groq OpenAI response headers (call after every response).
func (m *GroqKeyManager) UpdateFromHeaders(keyIndex int, h http.Header) {
	if m == nil || keyIndex < 0 || keyIndex >= len(m.keys) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ks := m.keys[keyIndex]
	now := time.Now()
	ks.LastUpdated = &now

	if v := h.Get("X-Ratelimit-Remaining-Requests"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			ks.RemainingRequestsDay = n
		}
	}
	if v := h.Get("X-Ratelimit-Remaining-Tokens"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			ks.RemainingTokensMin = n
		}
	}
	// Some Groq responses include a separate daily token bucket; if absent, keep prior estimate.
	if v := h.Get("X-Ratelimit-Remaining-Tokens-Day"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			ks.RemainingTokensDay = n
		}
	}
	if v := h.Get("X-Ratelimit-Reset-Requests"); v != "" {
		s := strings.TrimSpace(v)
		ks.ResetRequests = &s
	}
	if v := h.Get("X-Ratelimit-Reset-Tokens"); v != "" {
		s := strings.TrimSpace(v)
		ks.ResetTokens = &s
	}
	if v := h.Get("X-Ratelimit-Remaining-Requests-Minute"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			ks.RemainingRequestsMin = n
		}
	}
}

// Handle429 marks a key as temporarily exhausted after a 429.
func (m *GroqKeyManager) Handle429(keyIndex int) {
	if m == nil || keyIndex < 0 || keyIndex >= len(m.keys) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ks := m.keys[keyIndex]
	ks.Consecutive429s++
	ks.IsExhausted = true
}

// MarkSuccess clears temporary exhaustion after a successful completion.
func (m *GroqKeyManager) MarkSuccess(keyIndex int) {
	if m == nil || keyIndex < 0 || keyIndex >= len(m.keys) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ks := m.keys[keyIndex]
	if ks.RemainingRequestsDay > 2 && ks.RemainingRequestsMin > 2 {
		ks.IsExhausted = false
		ks.Consecutive429s = 0
	}
}

// GroqKeyStat is JSON for the dashboard.
type GroqKeyStat struct {
	Index          int      `json:"index"`
	KeySuffix      string   `json:"key_suffix"`
	RemainingRPD   int      `json:"remaining_rpd"`
	RemainingRPM   int      `json:"remaining_rpm"`
	RemainingTPM   int      `json:"remaining_tpm"`
	RemainingTPD   int      `json:"remaining_tpd"`
	ResetRequests  *string  `json:"reset_requests"`
	ResetTokens    *string  `json:"reset_tokens"`
	IsExhausted    bool     `json:"is_exhausted"`
	LastUpdated    *float64 `json:"last_updated,omitempty"` // unix seconds
	RPDPct         float64  `json:"rpd_pct"`
	TPDPct         float64  `json:"tpd_pct"`
	Consecutive429 int      `json:"consecutive_429s"`
}

// GroqTotals aggregates all keys.
type GroqTotals struct {
	TotalRemainingRPD   int     `json:"total_remaining_rpd"`
	TotalRemainingTPD   int     `json:"total_remaining_tpd"`
	EstimatedHoursLeft  float64 `json:"estimated_hours_left"`
	KeysAlive           int     `json:"keys_alive"`
	TotalRPDLimit       int     `json:"total_rpd_limit"`
	TotalTPDLimit       int     `json:"total_tpd_limit"`
}

// GetStats returns per-key stats for the dashboard.
func (m *GroqKeyManager) GetStats() []GroqKeyStat {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]GroqKeyStat, 0, len(m.keys))
	for i, ks := range m.keys {
		var last *float64
		if ks.LastUpdated != nil {
			u := float64(ks.LastUpdated.Unix())
			last = &u
		}
		rpdPct := (float64(ks.RemainingRequestsDay) / float64(groqRPDLimit)) * 100
		tpdPct := (float64(ks.RemainingTokensDay) / float64(groqTPDLimit)) * 100
		out = append(out, GroqKeyStat{
			Index:          i,
			KeySuffix:      keySuffixMasked(ks.Key, groqKeySuffix),
			RemainingRPD:   ks.RemainingRequestsDay,
			RemainingRPM:   ks.RemainingRequestsMin,
			RemainingTPM:   ks.RemainingTokensMin,
			RemainingTPD:   ks.RemainingTokensDay,
			ResetRequests:  ks.ResetRequests,
			ResetTokens:    ks.ResetTokens,
			IsExhausted:    ks.IsExhausted,
			LastUpdated:    last,
			RPDPct:         rpdPct,
			TPDPct:         tpdPct,
			Consecutive429: ks.Consecutive429s,
		})
	}
	return out
}

// GetTotals sums limits across keys.
func (m *GroqKeyManager) GetTotals() GroqTotals {
	if m == nil {
		return GroqTotals{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var totRPD, totTPD, alive int
	n := len(m.keys)
	for _, ks := range m.keys {
		totRPD += ks.RemainingRequestsDay
		totTPD += ks.RemainingTokensDay
		if !ks.IsExhausted {
			alive++
		}
	}
	// (total_remaining_rpd / 30) / 60 — assumes 30 req/min steady usage
	est := 0.0
	if totRPD > 0 {
		est = (float64(totRPD) / 30.0) / 60.0
	}
	return GroqTotals{
		TotalRemainingRPD:  totRPD,
		TotalRemainingTPD:  totTPD,
		EstimatedHoursLeft: est,
		KeysAlive:          alive,
		TotalRPDLimit:      groqRPDLimit * n,
		TotalTPDLimit:      groqTPDLimit * n,
	}
}

func keySuffixMasked(key string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(strings.TrimSpace(key))
	if len(r) <= n {
		return string(r)
	}
	return string(r[len(r)-n:])
}
