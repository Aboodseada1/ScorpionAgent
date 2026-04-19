package session

import (
	"strings"
	"sync"
)

// SelfFilter drops transcripts that clearly repeat recent TTS output.
// Layer 3 of the echo fix: even if AEC3 AND the energy gate miss some self-
// audio, if the resulting transcript overlaps heavily with what we just said,
// we know it's our own voice coming back.
type SelfFilter struct {
	mu    sync.Mutex
	recent []string
	ngram  int
}

func NewSelfFilter(ngram int) *SelfFilter {
	if ngram < 3 {
		ngram = 6
	}
	return &SelfFilter{ngram: ngram}
}

// Record adds a TTS utterance to the recent buffer (last ~5 entries).
func (f *SelfFilter) Record(text string) {
	text = normalize(text)
	if text == "" {
		return
	}
	f.mu.Lock()
	f.recent = append(f.recent, text)
	if len(f.recent) > 5 {
		f.recent = f.recent[len(f.recent)-5:]
	}
	f.mu.Unlock()
}

// IsSelf returns true if transcript is likely a reflection of our own TTS.
func (f *SelfFilter) IsSelf(transcript string) bool {
	t := normalize(transcript)
	if t == "" {
		return false
	}
	f.mu.Lock()
	recent := append([]string(nil), f.recent...)
	f.mu.Unlock()
	words := strings.Fields(t)
	if len(words) < f.ngram {
		// short phrase: require direct substring match against any recent TTS
		for _, r := range recent {
			if strings.Contains(r, t) {
				return true
			}
		}
		return false
	}
	shingles := ngrams(words, f.ngram)
	for _, r := range recent {
		rw := strings.Fields(r)
		if len(rw) < f.ngram {
			continue
		}
		rs := ngrams(rw, f.ngram)
		if jaccard(shingles, rs) > 0.35 {
			return true
		}
	}
	return false
}

func ngrams(words []string, n int) map[string]struct{} {
	out := map[string]struct{}{}
	for i := 0; i+n <= len(words); i++ {
		out[strings.Join(words[i:i+n], " ")] = struct{}{}
	}
	return out
}

func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	var inter int
	for k := range a {
		if _, ok := b[k]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func normalize(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '\t' || r == '\n':
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
