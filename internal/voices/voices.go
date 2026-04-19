// Package voices discovers Piper voice files, parses their .onnx.json metadata,
// and exposes a curated catalog for one-click installation.
package voices

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Voice is a locally available Piper voice.
type Voice struct {
	ID           string  `json:"id"`         // stable slug from filename
	Name         string  `json:"name"`       // human name from config
	Language     string  `json:"language"`   // e.g. "en_US"
	Quality      string  `json:"quality"`    // low/medium/high/x_low
	SampleRate   int     `json:"sample_rate"`
	ModelPath    string  `json:"model_path"`
	ConfigPath   string  `json:"config_path"`
	SizeMB       float64 `json:"size_mb"`
}

// CatalogEntry is a downloadable voice preset (remote).
type CatalogEntry struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Language   string  `json:"language"`
	Quality    string  `json:"quality"`
	ModelURL   string  `json:"model_url"`
	ConfigURL  string  `json:"config_url"`
	ApproxMB   float64 `json:"approx_mb"`
	Accent     string  `json:"accent"`
}

// Store scans a voices directory. Scans are cheap (reads one dir, parses a
// tiny JSON per voice) so we do NOT cache — the UI expects to see voices
// appear the moment a download completes.
type Store struct {
	mu   sync.RWMutex
	dirs []string
}

func NewStore(dirs ...string) *Store {
	return &Store{dirs: dirs}
}

// List scans the configured dirs and returns installed voices.
func (s *Store) List() ([]Voice, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.scanLocked()
}

// Refresh is kept for API compatibility; List is always fresh now.
func (s *Store) Refresh() ([]Voice, error) {
	return s.List()
}

func (s *Store) scanLocked() ([]Voice, error) {
	seen := map[string]bool{}
	out := []Voice{}
	for _, dir := range s.dirs {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if !strings.HasSuffix(name, ".onnx") {
				continue
			}
			onnxPath := filepath.Join(dir, name)
			cfgPath := onnxPath + ".json"
			if _, err := os.Stat(cfgPath); err != nil {
				continue
			}
			v, err := parseVoice(onnxPath, cfgPath)
			if err != nil {
				continue
			}
			if seen[v.ID] {
				continue
			}
			seen[v.ID] = true
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Language != out[j].Language {
			return out[i].Language < out[j].Language
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func parseVoice(onnxPath, cfgPath string) (Voice, error) {
	info, err := os.Stat(onnxPath)
	if err != nil {
		return Voice{}, err
	}
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		return Voice{}, err
	}
	var cfg struct {
		Audio struct {
			SampleRate int `json:"sample_rate"`
		} `json:"audio"`
		Language struct {
			Code string `json:"code"`
			Family string `json:"family"`
			NativeName string `json:"native_name"`
		} `json:"language"`
		Dataset string `json:"dataset"`
		Quality string `json:"quality"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Voice{}, fmt.Errorf("parse %s: %w", cfgPath, err)
	}
	base := strings.TrimSuffix(filepath.Base(onnxPath), ".onnx")
	// typical name: en_US-lessac-medium
	parts := strings.Split(base, "-")
	lang := cfg.Language.Code
	if lang == "" && len(parts) > 0 {
		lang = parts[0]
	}
	quality := cfg.Quality
	if quality == "" && len(parts) > 2 {
		quality = parts[len(parts)-1]
	}
	var name string
	if cfg.Dataset != "" {
		name = cfg.Dataset
	} else if len(parts) >= 2 {
		name = parts[1]
	} else {
		name = base
	}
	return Voice{
		ID:         base,
		Name:       name,
		Language:   lang,
		Quality:    quality,
		SampleRate: cfg.Audio.SampleRate,
		ModelPath:  onnxPath,
		ConfigPath: cfgPath,
		SizeMB:     float64(info.Size()) / (1 << 20),
	}, nil
}

// Get returns a voice by ID (or filename-less onnx path).
func (s *Store) Get(id string) (Voice, error) {
	vs, err := s.List()
	if err != nil {
		return Voice{}, err
	}
	for _, v := range vs {
		if v.ID == id {
			return v, nil
		}
	}
	return Voice{}, errors.New("voice not found")
}

// PrimaryDir is the first configured dir (used as download destination).
func (s *Store) PrimaryDir() string {
	if len(s.dirs) == 0 {
		return ""
	}
	return s.dirs[0]
}

// Catalog returns a curated list of downloadable Piper voices from the
// official rhasspy repo (https://huggingface.co/rhasspy/piper-voices).
//
// Size note: Piper voices share the same VITS architecture regardless of the
// "quality" label. The quality string mostly controls output sample rate and
// training-data quantity, NOT model size. So "low" voices are typically the
// same ~63 MB as "medium" (16 kHz output vs 22 kHz). Only the `x_low` tier
// uses a smaller distilled architecture (~22 MB). `high` is the larger
// 24 kHz model (~115 MB). The approx sizes below reflect reality; the live
// download progress card will still report the exact bytes.
func Catalog() []CatalogEntry {
	base := "https://huggingface.co/rhasspy/piper-voices/resolve/main"
	mk := func(lang, name, quality, accent string, mb float64) CatalogEntry {
		langShort := strings.Split(lang, "_")[0]
		id := fmt.Sprintf("%s-%s-%s", lang, name, quality)
		path := fmt.Sprintf("%s/%s/%s/%s/%s/%s.onnx",
			base, langShort, lang, name, quality, id)
		return CatalogEntry{
			ID:        id,
			Name:      name,
			Language:  lang,
			Quality:   quality,
			Accent:    accent,
			ApproxMB:  mb,
			ModelURL:  path,
			ConfigURL: path + ".json",
		}
	}
	// sizeFor returns a realistic estimate based on Piper quality tier.
	sizeFor := func(q string) float64 {
		switch q {
		case "x_low":
			return 22
		case "low":
			return 63
		case "medium":
			return 63
		case "high":
			return 115
		default:
			return 63
		}
	}
	mkQ := func(lang, name, quality, accent string) CatalogEntry {
		return mk(lang, name, quality, accent, sizeFor(quality))
	}
	return []CatalogEntry{
		// English (US)
		mkQ("en_US", "lessac", "medium", "American - warm female"),
		mkQ("en_US", "lessac", "low", "American - compact female"),
		mkQ("en_US", "amy", "medium", "American - friendly female"),
		mkQ("en_US", "amy", "low", "American - compact female"),
		mkQ("en_US", "ryan", "medium", "American - clear male"),
		mkQ("en_US", "ryan", "low", "American - compact male"),
		mkQ("en_US", "libritts_r", "medium", "American - expressive"),
		mkQ("en_US", "hfc_female", "medium", "American - neutral female"),
		mkQ("en_US", "hfc_male", "medium", "American - neutral male"),
		mkQ("en_US", "kristin", "medium", "American - young female"),
		mkQ("en_US", "kathleen", "low", "American - calm female"),
		mkQ("en_US", "joe", "medium", "American - deep male"),
		// English (GB)
		mkQ("en_GB", "alan", "medium", "British - mature male"),
		mkQ("en_GB", "alan", "low", "British - compact male"),
		mkQ("en_GB", "jenny_dioco", "medium", "British - female"),
		mkQ("en_GB", "northern_english_male", "medium", "Northern England - male"),
		mkQ("en_GB", "southern_english_female", "low", "Southern England - female"),
		// Arabic
		mkQ("ar_JO", "kareem", "medium", "Arabic - Jordanian male"),
		mkQ("ar_JO", "kareem", "low", "Arabic - compact male"),
		// Spanish
		mkQ("es_ES", "davefx", "medium", "Spanish - Spain male"),
		mkQ("es_MX", "claude", "high", "Spanish - Mexican male"),
		// French
		mkQ("fr_FR", "siwis", "medium", "French - female"),
		mkQ("fr_FR", "upmc", "medium", "French - academic"),
		// German
		mkQ("de_DE", "thorsten", "medium", "German - male"),
		mkQ("de_DE", "thorsten", "high", "German - HD male"),
		// Portuguese
		mkQ("pt_BR", "faber", "medium", "Brazilian - male"),
		// Italian
		mkQ("it_IT", "riccardo", "x_low", "Italian - compact male"),
	}
}
