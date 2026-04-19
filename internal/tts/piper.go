// Package tts wraps a Piper subprocess.
//
// The hot path is Pool.OpenVoice: it spawns a SINGLE long-lived piper process
// and feeds sentences to it via stdin, so there is no per-phrase model-load
// gap between "I'm sorry," and "I think there was a technical issue."
// Audio streams continuously out of one process for the entire turn.
//
// Pool.Synth is a one-shot convenience (used for voice samples / warmup) that
// internally just opens a Voice, writes one line, closes, and drains.
package tts

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"scorpion/agent/internal/config"
)

type Chunk struct {
	PCM48k []float32
	Err    error
	Text   string
}

type Pool struct {
	store *config.Store

	// rateCache memoises the native sample rate of each voice config file so
	// we don't reparse JSON on every voice open.
	rateMu    sync.RWMutex
	rateCache map[string]int
}

func NewPool(store *config.Store) *Pool {
	return &Pool{store: store, rateCache: map[string]int{}}
}

// Close is a no-op — voices are owned by their opener and cleaned up via Close().
func (p *Pool) Close() {}

// voiceSampleRate reads "audio.sample_rate" from the given .onnx.json.
// Returns 22050 when the config is missing or malformed (piper default).
func (p *Pool) voiceSampleRate(cfgPath string) int {
	if cfgPath == "" {
		return 22050
	}
	p.rateMu.RLock()
	if v, ok := p.rateCache[cfgPath]; ok {
		p.rateMu.RUnlock()
		return v
	}
	p.rateMu.RUnlock()
	rate := 22050
	if b, err := os.ReadFile(cfgPath); err == nil {
		var doc struct {
			Audio struct {
				SampleRate int `json:"sample_rate"`
			} `json:"audio"`
		}
		if json.Unmarshal(b, &doc) == nil && doc.Audio.SampleRate > 0 {
			rate = doc.Audio.SampleRate
		}
	}
	p.rateMu.Lock()
	p.rateCache[cfgPath] = rate
	p.rateMu.Unlock()
	return rate
}

// Voice is a long-lived Piper subprocess that consumes sentences via Say and
// emits PCM48k chunks on Chunks. Safe for one producer (session) and the
// internal reader goroutine. Close → EOF piper's stdin, let it drain, let the
// channel close. Abort → kill the process immediately (barge-in).
type Voice struct {
	sampleRate int

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	out    chan Chunk
	done   chan struct{} // closed when reader goroutine exits

	mu     sync.Mutex
	closed bool
}

// Chunks is the audio output stream. The channel closes when the subprocess
// exits (after Close or Abort or crash).
func (v *Voice) Chunks() <-chan Chunk { return v.out }

// Say writes a sentence to the piper subprocess. Safe to call repeatedly;
// piper will buffer and synthesize lines as it finishes the previous one.
// Never blocks on audio playback.
func (v *Voice) Say(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return errors.New("voice closed")
	}
	// Piper CLI reads one line per utterance.
	_, err := v.stdin.Write([]byte(text + "\n"))
	return err
}

// Close signals "no more input coming". Piper finishes its queued lines then
// exits cleanly. The chunks channel closes when the reader drains.
func (v *Voice) Close() {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return
	}
	v.closed = true
	v.mu.Unlock()
	if v.stdin != nil {
		_ = v.stdin.Close()
	}
}

// Abort kills the piper subprocess NOW. Used for barge-in: we don't want the
// AI to finish its current sentence after the user starts speaking.
func (v *Voice) Abort() {
	v.mu.Lock()
	v.closed = true
	v.mu.Unlock()
	if v.cmd != nil && v.cmd.Process != nil {
		_ = v.cmd.Process.Kill()
	}
}

// Wait blocks until the subprocess has exited and the reader goroutine is done.
func (v *Voice) Wait() { <-v.done }

// OpenVoice spawns a persistent piper subprocess configured with the current
// voice and returns a Voice handle. The context governs the process lifetime;
// cancelling the context kills piper.
func (p *Pool) OpenVoice(ctx context.Context) (*Voice, error) {
	cfg := p.store.Snapshot()
	if cfg.PiperBin == "" {
		return nil, errors.New("PIPER_BIN not configured")
	}
	args := []string{"--output_raw", "--quiet"}
	if cfg.PiperModel != "" {
		args = append(args, "-m", cfg.PiperModel)
	}
	if cfg.PiperConfig != "" {
		args = append(args, "-c", cfg.PiperConfig)
	}
	cmd := exec.CommandContext(ctx, cfg.PiperBin, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("piper start: %w", err)
	}
	go io.Copy(io.Discard, stderr)

	v := &Voice{
		sampleRate: p.voiceSampleRate(cfg.PiperConfig),
		cmd:        cmd,
		stdin:      stdin,
		out:        make(chan Chunk, 32),
		done:       make(chan struct{}),
	}

	// Reader goroutine: pump stdout -> resample -> v.out.
	go func() {
		defer func() {
			close(v.out)
			_ = cmd.Wait()
			close(v.done)
		}()
		bufSize := v.sampleRate / 10 // 100ms worth of samples
		raw := make([]byte, bufSize*2)
		for {
			n, err := io.ReadFull(stdout, raw)
			if n > 0 {
				samples := pcm16leToFloat32(raw[:n])
				if v.sampleRate == 48000 {
					select {
					case v.out <- Chunk{PCM48k: samples}:
					case <-ctx.Done():
						return
					}
				} else {
					select {
					case v.out <- Chunk{PCM48k: resampleToTarget(samples, v.sampleRate, 48000)}:
					case <-ctx.Done():
						return
					}
				}
			}
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return
			}
			if err != nil {
				// Only report genuine errors — context cancellation is normal.
				if ctx.Err() == nil {
					select {
					case v.out <- Chunk{Err: err}:
					case <-ctx.Done():
					}
				}
				return
			}
		}
	}()

	return v, nil
}

// Synth streams audio chunks for a single text. Convenience wrapper around
// OpenVoice for one-shot callers (voice samples, warmup). The returned
// channel closes when audio is fully synthesized.
func (p *Pool) Synth(ctx context.Context, text string) <-chan Chunk {
	out := make(chan Chunk, 8)
	go func() {
		defer close(out)
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		v, err := p.OpenVoice(ctx)
		if err != nil {
			out <- Chunk{Err: err}
			return
		}
		if err := v.Say(text); err != nil {
			v.Abort()
			out <- Chunk{Err: err}
			return
		}
		v.Close()
		for c := range v.Chunks() {
			out <- c
		}
	}()
	return out
}

// Warmup spawns a piper process, feeds it a trivial utterance, drains the
// audio (discarded), and exits. This triggers model paging into RAM so the
// first real call doesn't pay the ~400ms cold-start cost mid-sentence.
func (p *Pool) Warmup(ctx context.Context) error {
	wctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	ch := p.Synth(wctx, "Ready.")
	for c := range ch {
		if c.Err != nil {
			return c.Err
		}
	}
	return nil
}

// Available probes piper --help (cheap).
func (p *Pool) Available() bool {
	cfg := p.store.Snapshot()
	if cfg.PiperBin == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, cfg.PiperBin, "--help").Run(); err != nil {
		return false
	}
	return true
}

func pcm16leToFloat32(buf []byte) []float32 {
	n := len(buf) / 2
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		s := int16(binary.LittleEndian.Uint16(buf[i*2:]))
		out[i] = float32(s) / 32768.0
	}
	return out
}

// resampleToTarget does a linear resample. Keeps dependencies zero.
func resampleToTarget(in []float32, fromHz, toHz int) []float32 {
	if fromHz == toHz || len(in) == 0 {
		return in
	}
	ratio := float64(toHz) / float64(fromHz)
	outLen := int(float64(len(in)) * ratio)
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
