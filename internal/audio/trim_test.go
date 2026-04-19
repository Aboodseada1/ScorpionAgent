package audio

import "testing"

func TestTrimTrailingSilence(t *testing.T) {
	sil := make([]float32, 320*10)
	voice := make([]float32, 320*3)
	for i := range voice {
		voice[i] = 0.15
	}
	pcm := append(voice, sil...)
	out := TrimTrailingSilence(pcm, 320, 3000)
	if len(out) >= len(pcm) {
		t.Fatalf("expected trim, got len %d", len(out))
	}
	if len(out) < len(voice) {
		t.Fatalf("trimmed voice: %d vs %d", len(out), len(voice))
	}
}
