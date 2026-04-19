// Package llmmodels manages local GGUF models that the llama-server systemd
// unit reads. It can list them, set the active model (by rewriting a drop-in
// EnvironmentFile), and restart the service.
package llmmodels

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Model struct {
	ID       string  `json:"id"`       // filename without extension
	File     string  `json:"file"`     // absolute path to .gguf
	Name     string  `json:"name"`     // filename
	Family   string  `json:"family"`   // best-effort (qwen, llama, mistral, etc.)
	SizeGB   float64 `json:"size_gb"`
	Quant    string  `json:"quant"`    // Q4_K_M / Q5_K_M / etc
	Active   bool    `json:"active"`
}

type CatalogEntry struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Family   string  `json:"family"`
	Params   string  `json:"params"` // "1.5B" etc
	Quant    string  `json:"quant"`
	URL      string  `json:"url"`
	ApproxGB float64 `json:"approx_gb"`
	Blurb    string  `json:"blurb"`
}

type Manager struct {
	mu        sync.RWMutex
	dirs      []string
	unitName  string   // e.g. "llama-qwen.service"
	envFile   string   // EnvironmentFile path, e.g. /var/www/agent.scorpion.codes/data/llama.env
	activeLnk string   // symlink to currently active gguf file (for introspection)
}

func NewManager(unit, envFile string, dirs ...string) *Manager {
	return &Manager{
		dirs:     dirs,
		unitName: unit,
		envFile:  envFile,
	}
}

// List discovers .gguf files under all configured dirs (recursive, 2 levels).
func (m *Manager) List() ([]Model, error) {
	active, _ := m.activePath()
	seen := map[string]bool{}
	out := []Model{}
	for _, d := range m.dirs {
		if d == "" {
			continue
		}
		filepath.WalkDir(d, func(path string, de os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if de.IsDir() {
				// depth guard: skip anything 3+ deep
				rel, _ := filepath.Rel(d, path)
				if strings.Count(rel, string(os.PathSeparator)) > 2 {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(strings.ToLower(de.Name()), ".gguf") {
				return nil
			}
			if seen[path] {
				return nil
			}
			seen[path] = true
			info, err := de.Info()
			if err != nil {
				return nil
			}
			model := Model{
				ID:     strings.TrimSuffix(de.Name(), filepath.Ext(de.Name())),
				File:   path,
				Name:   de.Name(),
				SizeGB: float64(info.Size()) / (1 << 30),
				Family: guessFamily(de.Name()),
				Quant:  guessQuant(de.Name()),
			}
			if active != "" && sameFile(active, path) {
				model.Active = true
			}
			out = append(out, model)
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Family != out[j].Family {
			return out[i].Family < out[j].Family
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// ActivePath returns the path currently configured in the environment file.
func (m *Manager) ActivePath() (string, error) {
	return m.activePath()
}

func (m *Manager) activePath() (string, error) {
	if m.envFile == "" {
		return "", nil
	}
	b, err := os.ReadFile(m.envFile)
	if err != nil {
		return "", nil
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "LLAMA_MODEL=") {
			v := strings.TrimPrefix(line, "LLAMA_MODEL=")
			return strings.Trim(v, `"'`), nil
		}
	}
	return "", nil
}

// SetActive writes the env file and restarts the systemd unit.
// Blocks until llama-server is reachable at probeURL (or timeout).
func (m *Manager) SetActive(ctx context.Context, path, probeURL string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("model file not found: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(m.envFile), 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf("LLAMA_MODEL=%q\n", path)
	if err := os.WriteFile(m.envFile, []byte(content), 0o644); err != nil {
		return err
	}
	return m.restartUnit(ctx)
}

func (m *Manager) restartUnit(ctx context.Context) error {
	if m.unitName == "" {
		return errors.New("unit not configured")
	}
	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(rctx, "systemctl", "restart", m.unitName)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl restart %s: %v (%s)", m.unitName, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// PrimaryDir returns the preferred download destination.
func (m *Manager) PrimaryDir() string {
	if len(m.dirs) == 0 {
		return ""
	}
	return m.dirs[0]
}

func guessFamily(name string) string {
	low := strings.ToLower(name)
	switch {
	case strings.Contains(low, "qwen"):
		return "qwen"
	case strings.Contains(low, "llama"):
		return "llama"
	case strings.Contains(low, "phi"):
		return "phi"
	case strings.Contains(low, "mistral"):
		return "mistral"
	case strings.Contains(low, "gemma"):
		return "gemma"
	case strings.Contains(low, "deepseek"):
		return "deepseek"
	case strings.Contains(low, "smollm"):
		return "smollm"
	}
	return "other"
}

func guessQuant(name string) string {
	for _, q := range []string{"Q2_K", "Q3_K_S", "Q3_K_M", "Q3_K_L", "Q4_0", "Q4_K_S", "Q4_K_M", "Q5_K_S", "Q5_K_M", "Q6_K", "Q8_0", "F16", "BF16"} {
		if strings.Contains(strings.ToUpper(name), q) {
			return q
		}
	}
	return ""
}

func sameFile(a, b string) bool {
	ra, _ := filepath.EvalSymlinks(a)
	rb, _ := filepath.EvalSymlinks(b)
	if ra == rb && ra != "" {
		return true
	}
	return a == b
}

// Catalog returns a curated set of small-but-capable GGUF models optimized for
// voice agents on CPU-only / limited boxes (1-8B range).
func Catalog() []CatalogEntry {
	return []CatalogEntry{
		{
			ID:       "qwen2.5-0.5b-instruct-q4km",
			Name:     "Qwen2.5 0.5B Instruct (Q4_K_M)",
			Family:   "qwen",
			Params:   "0.5B",
			Quant:    "Q4_K_M",
			ApproxGB: 0.40,
			Blurb:    "Realtime stack default: lowest latency on CPU; best for voice turn-taking.",
			URL:      "https://huggingface.co/bartowski/Qwen2.5-0.5B-Instruct-GGUF/resolve/main/Qwen2.5-0.5B-Instruct-Q4_K_M.gguf",
		},
		{
			ID:       "qwen2.5-1.5b-instruct-q4km",
			Name:     "Qwen2.5 1.5B Instruct (Q4_K_M)",
			Family:   "qwen",
			Params:   "1.5B",
			Quant:    "Q4_K_M",
			ApproxGB: 1.1,
			Blurb:    "Current default. Balanced speed/quality sweet-spot for 4-core CPUs.",
			URL:      "https://huggingface.co/Qwen/Qwen2.5-1.5B-Instruct-GGUF/resolve/main/qwen2.5-1.5b-instruct-q4_k_m.gguf",
		},
		{
			ID:       "qwen2.5-3b-instruct-q4km",
			Name:     "Qwen2.5 3B Instruct (Q4_K_M)",
			Family:   "qwen",
			Params:   "3B",
			Quant:    "Q4_K_M",
			ApproxGB: 2.0,
			Blurb:    "Noticeably smarter. Use if you have spare RAM and don't mind ~2x latency.",
			URL:      "https://huggingface.co/Qwen/Qwen2.5-3B-Instruct-GGUF/resolve/main/qwen2.5-3b-instruct-q4_k_m.gguf",
		},
		{
			ID:       "llama-3.2-1b-instruct-q4km",
			Name:     "Llama 3.2 1B Instruct (Q4_K_M)",
			Family:   "llama",
			Params:   "1B",
			Quant:    "Q4_K_M",
			ApproxGB: 0.85,
			Blurb:    "Meta's ultralight. Great tool-calling, safer refusals.",
			URL:      "https://huggingface.co/bartowski/Llama-3.2-1B-Instruct-GGUF/resolve/main/Llama-3.2-1B-Instruct-Q4_K_M.gguf",
		},
		{
			ID:       "llama-3.2-3b-instruct-q4km",
			Name:     "Llama 3.2 3B Instruct (Q4_K_M)",
			Family:   "llama",
			Params:   "3B",
			Quant:    "Q4_K_M",
			ApproxGB: 2.0,
			Blurb:    "Meta's compact flagship. Strong reasoning for the size.",
			URL:      "https://huggingface.co/bartowski/Llama-3.2-3B-Instruct-GGUF/resolve/main/Llama-3.2-3B-Instruct-Q4_K_M.gguf",
		},
		{
			ID:       "phi-3.5-mini-instruct-q4km",
			Name:     "Phi 3.5 Mini Instruct (Q4_K_M)",
			Family:   "phi",
			Params:   "3.8B",
			Quant:    "Q4_K_M",
			ApproxGB: 2.3,
			Blurb:    "Microsoft's strong reasoner. Longer context (128k).",
			URL:      "https://huggingface.co/bartowski/Phi-3.5-mini-instruct-GGUF/resolve/main/Phi-3.5-mini-instruct-Q4_K_M.gguf",
		},
		{
			ID:       "smollm2-1.7b-instruct-q4km",
			Name:     "SmolLM2 1.7B Instruct (Q4_K_M)",
			Family:   "smollm",
			Params:   "1.7B",
			Quant:    "Q4_K_M",
			ApproxGB: 1.1,
			Blurb:    "HF's efficient small model. Fast first token.",
			URL:      "https://huggingface.co/bartowski/SmolLM2-1.7B-Instruct-GGUF/resolve/main/SmolLM2-1.7B-Instruct-Q4_K_M.gguf",
		},
	}
}
