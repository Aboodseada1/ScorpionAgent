package audio

// TrimTrailingSilence removes low-energy frames from the end of a 16 kHz mono
// buffer so Whisper is not asked to decode long trailing silence (slow + inaccurate).
func TrimTrailingSilence(pcm []float32, frameSamples int, maxTrimMs int) []float32 {
	if len(pcm) < frameSamples*4 || frameSamples < 64 {
		return pcm
	}
	minKeep := 1600 // ~100 ms minimum retained speech - balanced
	if len(pcm) <= minKeep+frameSamples {
		return pcm
	}
	maxSamples := maxTrimMs * 16 // 16 kHz
	// Balanced threshold to remove trailing silence
	thr := float32(0.0001)
	end := len(pcm)
	trimmed := 0
	for end > minKeep && trimmed < maxSamples {
		start := end - frameSamples
		if start < 0 {
			break
		}
		if RMS(pcm[start:end]) > thr {
			break
		}
		end = start
		trimmed += frameSamples
	}
	return pcm[:end]
}
