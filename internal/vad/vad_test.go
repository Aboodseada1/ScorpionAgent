package vad

import (
	"testing"
	"time"

	"scorpion/agent/internal/config"
)

func frameVoice() []float32 {
	f := make([]float32, 640)
	for i := range f {
		f[i] = 0.4
	}
	return f
}

func frameSilence() []float32 {
	return make([]float32, 640)
}

// Regression: first voiced frame of a new utterance must set lastVoiceT, otherwise
// a single silent frame after speech onset sees a stale lastVoiceT from the
// previous turn and flushes a ~40ms fragment.
func TestSegmenter_SpeechOnsetRefreshesHangover(t *testing.T) {
	cfg := &config.Config{
		VADThreshold:    0.55,
		VADMinSilenceMs: 250,
		VADSpeechPadMs:  80,
	}
	store := config.NewEphemeralStore(cfg)
	s := NewSegmenter(store)

	var uttCount int
	var lastLen int
	s.OnUtterance = func(pcm []float32) {
		uttCount++
		lastLen = len(pcm)
	}
	s.OnVoiceStart = func() {}

	voice := frameVoice()
	silence := frameSilence()

	// --- First utterance: speak, then stay silent until hangover fires ---
	for range 8 {
		s.Write(voice)
	}
	time.Sleep(300 * time.Millisecond)
	for range 20 {
		s.Write(silence)
	}
	if uttCount != 1 {
		t.Fatalf("expected 1 utterance after first phrase, got %d (lastLen=%d)", uttCount, lastLen)
	}
	if lastLen < 8*640 {
		t.Fatalf("first utterance unexpectedly short: %d samples", lastLen)
	}

	// --- Second utterance: sustained voice (onset gate), then one silent frame ---
	for range 6 {
		s.Write(voice)
	}
	s.Write(silence)

	if uttCount != 1 {
		t.Fatalf("regression: second utterance finalized after one silent frame (uttCount=%d, lastLen=%d)", uttCount, lastLen)
	}

	// Finish with real silence to flush the second utterance
	time.Sleep(300 * time.Millisecond)
	for range 30 {
		s.Write(silence)
	}
	if uttCount != 2 {
		t.Fatalf("expected 2 utterances total, got %d (lastLen=%d)", uttCount, lastLen)
	}
}
