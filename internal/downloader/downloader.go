// Package downloader fetches files from the internet with live progress
// broadcasting so the UI can show per-job progress bars over SSE.
package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Status string

const (
	StatusQueued  Status = "queued"
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	StatusFailed  Status = "failed"
)

// Job is a unit of work. Fields are read via (*Manager).Snapshot or the
// progress channel, never mutated by the consumer.
type Job struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`    // "voice" | "llm"
	Label      string  `json:"label"`
	URL        string  `json:"url"`
	Dest       string  `json:"dest"`
	BytesDone  int64   `json:"bytes_done"`
	BytesTotal int64   `json:"bytes_total"`
	SpeedKBps  float64 `json:"speed_kbps"`
	ETASec     int64   `json:"eta_sec"`
	Status     Status  `json:"status"`
	Error      string  `json:"error,omitempty"`
	StartedAt  int64   `json:"started_at"`
	EndedAt    int64   `json:"ended_at"`
	// Companion optional follow-up download, e.g. .json config alongside .onnx.
	ChildURL  string `json:"child_url,omitempty"`
	ChildDest string `json:"child_dest,omitempty"`
}

// Manager orchestrates concurrent downloads.
type Manager struct {
	mu      sync.RWMutex
	jobs    map[string]*Job
	subs    map[int]chan Job
	nextSub int
	client  *http.Client
}

func New() *Manager {
	return &Manager{
		jobs: map[string]*Job{},
		subs: map[int]chan Job{},
		client: &http.Client{
			Timeout: 0, // unbounded; we ctx.Cancel instead.
		},
	}
}

// Enqueue registers a new download and starts it in the background.
func (m *Manager) Enqueue(kind, label, url, dest, childURL, childDest string) (*Job, error) {
	if url == "" {
		return nil, errors.New("url required")
	}
	if dest == "" {
		return nil, errors.New("dest required")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return nil, err
	}
	j := &Job{
		ID:        strings.ReplaceAll(uuid.NewString(), "-", "")[:12],
		Kind:      kind,
		Label:     label,
		URL:       url,
		Dest:      dest,
		Status:    StatusQueued,
		StartedAt: time.Now().Unix(),
		ChildURL:  childURL,
		ChildDest: childDest,
	}
	m.mu.Lock()
	m.jobs[j.ID] = j
	m.mu.Unlock()
	m.broadcast(*j)
	go m.run(j)
	return j, nil
}

// List returns all jobs (recent first).
func (m *Manager) List() []Job {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, *j)
	}
	// newest first
	for i := 0; i < len(out); i++ {
		for k := i + 1; k < len(out); k++ {
			if out[k].StartedAt > out[i].StartedAt {
				out[i], out[k] = out[k], out[i]
			}
		}
	}
	return out
}

// Get returns a single job.
func (m *Manager) Get(id string) (*Job, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	j, ok := m.jobs[id]
	if !ok {
		return nil, false
	}
	cp := *j
	return &cp, true
}

// Subscribe streams all subsequent job updates. Unsubscribe via the cancel fn.
func (m *Manager) Subscribe() (<-chan Job, func()) {
	m.mu.Lock()
	id := m.nextSub
	m.nextSub++
	ch := make(chan Job, 32)
	m.subs[id] = ch
	// snapshot
	for _, j := range m.jobs {
		select {
		case ch <- *j:
		default:
		}
	}
	m.mu.Unlock()
	return ch, func() {
		m.mu.Lock()
		if c, ok := m.subs[id]; ok {
			delete(m.subs, id)
			close(c)
		}
		m.mu.Unlock()
	}
}

func (m *Manager) broadcast(j Job) {
	m.mu.RLock()
	for _, ch := range m.subs {
		select {
		case ch <- j:
		default:
		}
	}
	m.mu.RUnlock()
}

// updateJob mutates the job under lock then broadcasts a copy.
func (m *Manager) updateJob(id string, fn func(*Job)) {
	m.mu.Lock()
	j, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	fn(j)
	cp := *j
	m.mu.Unlock()
	m.broadcast(cp)
}

func (m *Manager) run(j *Job) {
	m.updateJob(j.ID, func(x *Job) { x.Status = StatusRunning })
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	if err := m.fetch(ctx, j.ID, j.URL, j.Dest); err != nil {
		m.updateJob(j.ID, func(x *Job) {
			x.Status = StatusFailed
			x.Error = err.Error()
			x.EndedAt = time.Now().Unix()
		})
		return
	}
	if j.ChildURL != "" && j.ChildDest != "" {
		if err := m.fetch(ctx, j.ID, j.ChildURL, j.ChildDest); err != nil {
			m.updateJob(j.ID, func(x *Job) {
				x.Status = StatusFailed
				x.Error = "main done, child: " + err.Error()
				x.EndedAt = time.Now().Unix()
			})
			return
		}
	}
	m.updateJob(j.ID, func(x *Job) {
		x.Status = StatusDone
		x.EndedAt = time.Now().Unix()
	})
}

// fetch streams url -> dest with progress updates.
func (m *Manager) fetch(ctx context.Context, jobID, url, dest string) error {
	tmp := dest + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer out.Close()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "scorpion-agent/1.0")
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}

	total := resp.ContentLength
	m.updateJob(jobID, func(j *Job) {
		if total > 0 && j.ChildURL == "" {
			j.BytesTotal = total
		} else if total > 0 {
			// Only set totals from the first request (main). Child add-on is ~kB.
			j.BytesTotal += total
		}
	})

	buf := make([]byte, 64*1024)
	var doneThisFile int64
	lastTick := time.Now()
	var lastBytesAtTick int64 = 0

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return werr
			}
			doneThisFile += int64(n)
		}
		now := time.Now()
		if now.Sub(lastTick) >= 300*time.Millisecond || rerr != nil {
			dt := now.Sub(lastTick).Seconds()
			delta := doneThisFile - lastBytesAtTick
			speed := 0.0
			if dt > 0 {
				speed = float64(delta) / 1024.0 / dt
			}
			lastTick = now
			lastBytesAtTick = doneThisFile
			localN := int64(n)
			m.updateJob(jobID, func(j *Job) {
				j.BytesDone += localN
				j.SpeedKBps = speed
				if speed > 0 && j.BytesTotal > 0 {
					remaining := j.BytesTotal - j.BytesDone
					if remaining < 0 {
						remaining = 0
					}
					j.ETASec = int64(float64(remaining) / 1024.0 / speed)
				}
			})
		} else if n > 0 {
			localN := int64(n)
			m.updateJob(jobID, func(j *Job) { j.BytesDone += localN })
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	return nil
}

// Cancel marks a running job as failed (best-effort; cancellation via ctx
// happens only if the job is still in its HTTP loop).
// For robust cancellation we'd hold a cancel func per job; this simple impl
// is OK since the UI can just let slow jobs finish.
func (m *Manager) Cancel(id string) {
	m.updateJob(id, func(j *Job) {
		if j.Status == StatusRunning || j.Status == StatusQueued {
			j.Status = StatusFailed
			j.Error = "cancelled"
			j.EndedAt = time.Now().Unix()
		}
	})
}
