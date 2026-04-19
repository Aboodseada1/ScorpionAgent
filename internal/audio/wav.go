// Package audio has tiny helpers for PCM <-> WAV <-> float32 so other packages
// stay free of encoding boilerplate.
package audio

import (
	"encoding/binary"
	"io"
	"math"
)

// Float32ToInt16 clamps and scales [-1,1] to int16 PCM.
func Float32ToInt16(in []float32) []int16 {
	out := make([]int16, len(in))
	for i, s := range in {
		if s > 1 {
			s = 1
		} else if s < -1 {
			s = -1
		}
		out[i] = int16(s * 32767)
	}
	return out
}

// Int16ToFloat32 converts PCM16 to normalized float32.
func Int16ToFloat32(in []int16) []float32 {
	out := make([]float32, len(in))
	for i, s := range in {
		out[i] = float32(s) / 32768.0
	}
	return out
}

// Float32ToWav builds a 16-bit mono WAV file from float32 samples.
func Float32ToWav(samples []float32, sampleRate int) []byte {
	pcm := Float32ToInt16(samples)
	return Int16ToWav(pcm, sampleRate)
}

// Int16ToWav builds a 16-bit mono WAV file from int16 samples.
func Int16ToWav(samples []int16, sampleRate int) []byte {
	dataLen := len(samples) * 2
	buf := make([]byte, 44+dataLen)
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], uint32(36+dataLen))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16)
	binary.LittleEndian.PutUint16(buf[20:22], 1)
	binary.LittleEndian.PutUint16(buf[22:24], 1)
	binary.LittleEndian.PutUint32(buf[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(buf[28:32], uint32(sampleRate*2))
	binary.LittleEndian.PutUint16(buf[32:34], 2)
	binary.LittleEndian.PutUint16(buf[34:36], 16)
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], uint32(dataLen))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(buf[44+i*2:], uint16(s))
	}
	return buf
}

// WavToFloat32 parses a 16-bit mono WAV file and returns samples + sample rate.
func WavToFloat32(data []byte) ([]float32, int, error) {
	if len(data) < 44 {
		return nil, 0, io.ErrShortBuffer
	}
	sampleRate := int(binary.LittleEndian.Uint32(data[24:28]))
	bits := int(binary.LittleEndian.Uint16(data[34:36]))
	if bits != 16 {
		return nil, sampleRate, io.ErrUnexpectedEOF
	}
	dataLen := int(binary.LittleEndian.Uint32(data[40:44]))
	if 44+dataLen > len(data) {
		dataLen = len(data) - 44
	}
	n := dataLen / 2
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		s := int16(binary.LittleEndian.Uint16(data[44+i*2:]))
		out[i] = float32(s) / 32768.0
	}
	return out, sampleRate, nil
}

// Resample is a naive linear interpolation resampler (good enough for 16k<->48k).
func Resample(in []float32, fromHz, toHz int) []float32 {
	if fromHz == toHz || len(in) == 0 {
		return in
	}
	ratio := float64(toHz) / float64(fromHz)
	outLen := int(math.Round(float64(len(in)) * ratio))
	out := make([]float32, outLen)
	for i := 0; i < outLen; i++ {
		srcPos := float64(i) / ratio
		j := int(srcPos)
		if j >= len(in)-1 {
			out[i] = in[len(in)-1]
			continue
		}
		frac := float32(srcPos - float64(j))
		out[i] = in[j]*(1-frac) + in[j+1]*frac
	}
	return out
}

// RMS returns the root-mean-square energy of samples (in [0,1]).
func RMS(in []float32) float32 {
	if len(in) == 0 {
		return 0
	}
	var sum float64
	for _, s := range in {
		sum += float64(s) * float64(s)
	}
	return float32(math.Sqrt(sum / float64(len(in))))
}
