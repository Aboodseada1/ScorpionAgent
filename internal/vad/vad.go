// Package vad is a simple energy-gate VAD with TTS-reference echo suppression.
// Sufficient for utterance segmentation; Silero ONNX can drop in via the same interface.
package vad

import (
	"math"
	"sync"
	"sync/atomic"
	"time"

	"scorpion/agent/internal/audio"
	"scorpion/agent/internal/config"
)

// Segmenter buffers mono 16 kHz frames and emits whole utterances when the user
// has been silent long enough. It also knows when TTS is playing locally so it
// can refuse to open a new utterance during that window (layer 2 of the echo fix).
type Segmenter struct {
	store *config.Store

	mu           sync.Mutex
	buf          []float32
	speech       bool
	speechSince  time.Time
	lastVoiceT   time.Time
	minSilenceMs int
	speechPadMs  int
	threshold    float32

	ttsUntil atomic.Int64 // nano epoch until which TTS is playing (+ tail)
	ref      []float32    // recent reference audio for cross-correlation (last N frames)
	refMu    sync.Mutex

	lastPartialAt time.Time
	// prospectBuf holds voiced frames before we commit to an utterance (onset gating).
	prospectBuf []float32

	OnUtterance func(pcm16k []float32) // set by session orchestrator
	OnVoiceStart func()                // called when VAD first detects voice (barge-in signal)
	// OnPartial receives a snapshot of buffered speech audio while the user is
	// still talking (throttled). Used for live captions; optional.
	OnPartial func(pcm16k []float32)
}

const (
	// Require ~100ms of continuous voiced audio before we open an utterance.
	// Responsive but stable
	minOnsetSamples = 1600
	// First partial caption — balanced for good performance
	partialMinSamples = 2400
	partialEmitEvery = 150 * time.Millisecond  // Reasonable update frequency
)

func NewSegmenter(store *config.Store) *Segmenter {
	cfg := store.Snapshot()
	return &Segmenter{
		store:        store,
		minSilenceMs: cfg.VADMinSilenceMs,
		speechPadMs:  cfg.VADSpeechPadMs,
		threshold:    float32(cfg.VADThreshold / 10.0), // Balanced sensitivity
	}
}

// MarkTTSSpeaking extends the gate window while TTS is playing.
func (s *Segmenter) MarkTTSSpeaking(on bool) {
	if on {
		s.ttsUntil.Store(time.Now().Add(24 * time.Hour).UnixNano())
	} else {
		cfg := s.store.Snapshot()
		tail := time.Duration(cfg.TTSAcousticTailMs) * time.Millisecond
		if tail <= 0 {
			tail = 850 * time.Millisecond
		}
		s.ttsUntil.Store(time.Now().Add(tail).UnixNano())
	}
}

// WriteRef stores the most recent TTS reference audio so that the echo gate can
// correlate and suppress matching mic energy.
func (s *Segmenter) WriteRef(pcm16k []float32) {
	s.refMu.Lock()
	defer s.refMu.Unlock()
	const keepMs = 1200
	const keepSamples = 16 * keepMs
	s.ref = append(s.ref, pcm16k...)
	if len(s.ref) > keepSamples {
		s.ref = s.ref[len(s.ref)-keepSamples:]
	}
}

// Write pushes mic audio. It always buffers, and decides on utterance boundaries
// using RMS + hangover, with the TTS gate raising the threshold.
func (s *Segmenter) Write(pcm16k []float32) {
	if len(pcm16k) == 0 {
		return
	}
	now := time.Now()
	thr := s.threshold
	rawRms := audio.RMS(pcm16k)
	tts := now.UnixNano() < s.ttsUntil.Load()

	// During TTS we must balance echo rejection with barge-in sensitivity. A flat
	// 4× threshold made real user speech rarely exceed the gate, so interrupts
	// never fired. Use echo-subtracted RMS with a moderate lift, plus a raw-mic
	// boost so near-field speech still opens the gate after AEC/subtraction.
	var work []float32
	if tts {
		work = s.subtractRef(pcm16k)
	} else {
		work = pcm16k
	}
	subRms := audio.RMS(work)

	var voiced bool
	if tts {
		voiced = subRms > thr*2.0 || rawRms > thr*2.75
	} else {
		voiced = subRms > thr
	}

	s.mu.Lock()

	// Do not buffer silence before speech onset — long leading silence makes Whisper
	// hallucinate words on clicks and background noise.
	if !s.speech && !voiced {
		s.prospectBuf = s.prospectBuf[:0]
		s.mu.Unlock()
		return
	}

	if !s.speech && voiced {
		s.prospectBuf = append(s.prospectBuf, work...)
		if len(s.prospectBuf) < minOnsetSamples {
			s.mu.Unlock()
			return
		}
		s.buf = append([]float32(nil), s.prospectBuf...)
		s.prospectBuf = s.prospectBuf[:0]
		s.speech = true
		s.speechSince = now
		s.lastVoiceT = now
		cb := s.OnVoiceStart
		s.mu.Unlock()
		if cb != nil {
			cb()
		}
		return
	}

	// Active utterance: always append audio while speech is open — including
	// short dips between syllables/words. Previously we only appended voiced
	// frames, which dropped quiet samples and caused Whisper to mis-hear or
	// truncate (garbled words, missing endings).
	if s.speech {
		s.buf = append(s.buf, work...)
		const maxBufSeconds = 30
		if len(s.buf) > 16000*maxBufSeconds {
			s.buf = s.buf[len(s.buf)-16000*maxBufSeconds:]
		}
		if voiced {
			s.lastVoiceT = now
		}
		if voiced && s.OnPartial != nil && len(s.buf) >= partialMinSamples &&
			now.Sub(s.lastPartialAt) >= partialEmitEvery {
			s.lastPartialAt = now
			pcm := append([]float32(nil), s.buf...)
			cbP := s.OnPartial
			s.mu.Unlock()
			cbP(pcm)
			return
		}
		if !voiced && now.Sub(s.lastVoiceT) > time.Duration(s.minSilenceMs)*time.Millisecond {
			utt := s.buf
			s.buf = nil
			s.speech = false
			s.prospectBuf = s.prospectBuf[:0]
			s.lastPartialAt = time.Time{}
			cb := s.OnUtterance
			s.mu.Unlock()
			if cb != nil && len(utt) > 0 {
				cb(utt)
			}
			return
		}
		s.mu.Unlock()
		return
	}
}

// Reset drops any buffered audio.
func (s *Segmenter) Reset() {
	s.mu.Lock()
	s.buf = nil
	s.prospectBuf = nil
	s.speech = false
	s.lastPartialAt = time.Time{}
	s.mu.Unlock()
}

// subtractRef performs a cheap NLMS-lite: align by peak of short cross-correlation
// over last ~120ms of reference, then subtract scaled reference from mic. Keeps
// layer-2 suppression for echo that AEC3 missed.
func (s *Segmenter) subtractRef(mic []float32) []float32 {
	s.refMu.Lock()
	ref := append([]float32(nil), s.ref...)
	s.refMu.Unlock()
	if len(ref) < 320 || len(mic) < 320 {
		return mic
	}
	// Use the tail of ref as the template.
	refTail := ref[max(0, len(ref)-len(mic)):]
	if len(refTail) != len(mic) {
		// pad
		if len(refTail) < len(mic) {
			pad := make([]float32, len(mic)-len(refTail))
			refTail = append(pad, refTail...)
		} else {
			refTail = refTail[len(refTail)-len(mic):]
		}
	}
	// Scale factor = correlation / ref energy.
	var num, den float64
	for i := range mic {
		num += float64(mic[i]) * float64(refTail[i])
		den += float64(refTail[i]) * float64(refTail[i])
	}
	if den < 1e-6 {
		return mic
	}
	a := num / den
	if math.Abs(a) > 2.0 {
		a = 0
	}
	out := make([]float32, len(mic))
	for i := range mic {
		out[i] = mic[i] - float32(a)*refTail[i]
	}
	return out
}
