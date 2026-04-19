// Package config loads env + runtime overrides for the Go agent.
package config

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type Config struct {
	Port        int
	AdminToken  string
	DataDir     string
	ModelsDir   string
	PromptsDir  string
	WebDistDir  string

	// LLM (local OpenAI-compatible server, e.g. llama.cpp)
	LLMBaseURL     string
	LLMModel       string
	LLMTemperature float64
	LLMMaxTokens   int
	// LLMProvider: "local" | "gemini" | "groq". Empty uses EffectiveLLMProvider heuristics.
	LLMProvider string

	// Gemini via OpenAI-compatible endpoint (https://ai.google.dev/gemini-api/docs/openai).
	GeminiAPIKey     string
	GeminiOpenAIBase string
	GeminiModels     []string

	// Groq via OpenAI-compatible API (https://console.groq.com/docs/openai).
	GroqAPIKey     string
	GroqAPIKeys    []string // optional: GROQ_API_KEYS comma-separated (rotation); not overridable from UI
	GroqOpenAIBase string

	// STT (whisper.cpp HTTP server)
	WhisperBaseURL string
	WhisperModel   string

	// TTS (piper subprocess)
	PiperBin    string
	PiperModel  string
	PiperConfig string

	// VAD / AEC
	VADThreshold      float64
	VADMinSilenceMs   int
	VADSpeechPadMs    int
	TTSAcousticTailMs int
	BargeInGuardMs    int
	SelfFilterNGram   int

	// KB embeddings
	EmbedBaseURL       string
	EmbedModel         string
	KBTopK             int
	KBMaxContextChars  int

	// Cascade router (when LLM provider is Groq): route simple turns to local llama.cpp).
	LLMCascade        bool
	CascadeLocalModel string
	CascadeGroqModel  string
}

// Load reads .env (if present), environment, and overlays runtime overrides.
func Load() *Config {
	loadDotenv(".env.go")
	loadDotenv(".env")

	geminiKey := strings.TrimSpace(env("GEMINI_API_KEY", env("GOOGLE_API_KEY", "")))
	geminiBase := strings.TrimRight(env("GEMINI_OPENAI_BASE_URL", "https://generativelanguage.googleapis.com/v1beta/openai"), "/")
	var geminiModels []string
	if fb := env("GEMINI_MODEL_FALLBACK", ""); fb != "" {
		geminiModels = splitCommaNonEmpty(fb)
	} else {
		geminiModels = []string{"gemini-3-flash-preview", "gemini-2.5-flash", "gemini-2.5-flash-lite"}
	}
	groqKey := strings.TrimSpace(env("GROQ_API_KEY", ""))
	groqKeys := splitCommaNonEmpty(env("GROQ_API_KEYS", ""))
	groqBase := strings.TrimRight(env("GROQ_OPENAI_BASE_URL", "https://api.groq.com/openai/v1"), "/")

	c := &Config{
		Port:               envInt("PORT", 8001),
		AdminToken:         env("ADMIN_TOKEN", ""),
		DataDir:            env("DATA_DIR", "./data"),
		ModelsDir:          env("MODELS_DIR", "./models"),
		PromptsDir:         env("PROMPTS_DIR", "./prompts"),
		WebDistDir:         env("WEB_DIST_DIR", "./web/dist"),
		LLMBaseURL:         env("LLM_BASE_URL", env("LOCAL_LLM_BASE_URL", "http://127.0.0.1:8080/v1")),
		LLMModel:           env("LLM_MODEL", env("LOCAL_LLM_MODEL", "local")),
		LLMTemperature:     envFloat("LLM_TEMPERATURE", envFloat("LOCAL_LLM_TEMPERATURE", 0.35)),
		LLMMaxTokens:       envInt("LLM_MAX_TOKENS", envInt("LOCAL_LLM_MAX_TOKENS", 160)),
		LLMProvider:        env("LLM_PROVIDER", ""),
		WhisperBaseURL:     env("WHISPER_BASE_URL", "http://127.0.0.1:9000"),
		// Display / client hint only: whisper.cpp server uses the model file from its systemd unit.
		WhisperModel:       env("WHISPER_MODEL", "ggml-base.en"),
		PiperBin:           env("PIPER_BIN", env("PIPER_BINARY", "piper")),
		PiperModel:         env("PIPER_MODEL", ""),
		PiperConfig:        env("PIPER_CONFIG", ""),
		VADThreshold:       envFloat("VAD_THRESHOLD", 0.58),
		// Long enough that natural mid-sentence pauses do not end the utterance early.
		VADMinSilenceMs:    envInt("VAD_MIN_SILENCE_MS", envInt("MIN_SILENCE_MS", 1050)),
		VADSpeechPadMs:     envInt("VAD_SPEECH_PAD_MS", envInt("SPEECH_PAD_MS", 80)),
		TTSAcousticTailMs:  envInt("TTS_ACOUSTIC_TAIL_MS", 850),
		BargeInGuardMs:     envInt("BARGE_IN_GUARD_MS", 550),
		SelfFilterNGram:    envInt("SELF_FILTER_NGRAM", 6),
		EmbedBaseURL:       env("EMBED_BASE_URL", env("LLM_BASE_URL", "http://127.0.0.1:8080/v1")),
		EmbedModel:         env("EMBED_MODEL", "local"),
		KBTopK:             envInt("KB_TOP_K", 5),
		KBMaxContextChars:  envInt("KB_MAX_CONTEXT_CHARS", 2800),

		GeminiAPIKey:     geminiKey,
		GeminiOpenAIBase: geminiBase,
		GeminiModels:     geminiModels,
		GroqAPIKey:       groqKey,
		GroqAPIKeys:      groqKeys,
		GroqOpenAIBase:   groqBase,

		LLMCascade:        parseEnvBool("LLM_CASCADE", false),
		CascadeLocalModel: env("CASCADE_LOCAL_MODEL", "Qwen2.5-3B-Instruct"),
		CascadeGroqModel:  env("CASCADE_GROQ_MODEL", "llama-3.1-8b-instant"),
	}
	return c
}

// CascadeOn is true when cascade routing is enabled and the active provider is Groq with keys.
func (c Config) CascadeOn() bool {
	return c.LLMCascade && EffectiveLLMProvider(c) == "groq" && HasGroqCredentials(c)
}

func parseEnvBool(k string, def bool) bool {
	v, ok := os.LookupEnv(k)
	if !ok {
		return def
	}
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return def
	}
	return v == "1" || v == "true" || v == "yes"
}

// EffectiveLLMProvider returns which backend handles chat: local llama, Gemini API, or Groq.
func EffectiveLLMProvider(c Config) string {
	p := strings.ToLower(strings.TrimSpace(c.LLMProvider))
	switch p {
	case "gemini", "groq", "local":
		return p
	}
	// Unset: prefer explicit local; otherwise infer from a single API key.
	hasGroq := HasGroqCredentials(c)
	if c.GeminiAPIKey != "" && !hasGroq {
		return "gemini"
	}
	if hasGroq && c.GeminiAPIKey == "" {
		return "groq"
	}
	return "local"
}

// HasGroqCredentials is true when GROQ_API_KEY, runtime override, or GROQ_API_KEYS is set.
func HasGroqCredentials(c Config) bool {
	return len(GroqKeyList(c)) > 0
}

// GroqKeyList returns API keys for Groq: GROQ_API_KEYS if set, else a single GROQ_API_KEY / override.
func GroqKeyList(c Config) []string {
	if len(c.GroqAPIKeys) > 0 {
		out := make([]string, len(c.GroqAPIKeys))
		copy(out, c.GroqAPIKeys)
		return out
	}
	if k := strings.TrimSpace(c.GroqAPIKey); k != "" {
		return []string{k}
	}
	return nil
}

// PublicAPI returns a copy safe to send to browsers (API key redacted).
func (c Config) PublicAPI() Config {
	out := c
	if out.GeminiAPIKey != "" {
		out.GeminiAPIKey = "(configured)"
	}
	if out.GroqAPIKey != "" {
		out.GroqAPIKey = "(configured)"
	}
	if len(out.GroqAPIKeys) > 0 {
		out.GroqAPIKeys = nil
	}
	return out
}

// Overrides is a JSON struct mirrored to data/runtime_overrides.json so the UI
// can change things without restart.
type Overrides struct {
	LLMProvider *string `json:"llm_provider,omitempty"` // "local" | "gemini" | "groq"
	LLMModel       *string  `json:"llm_model,omitempty"`
	LLMTemperature *float64 `json:"llm_temperature,omitempty"`
	LLMMaxTokens   *int     `json:"llm_max_tokens,omitempty"`
	GeminiAPIKey   *string  `json:"gemini_api_key,omitempty"`
	GroqAPIKey     *string  `json:"groq_api_key,omitempty"`
	// Optional comma-separated extra Gemini model IDs to try after llm_model (same as GEMINI_MODEL_FALLBACK).
	GeminiModelFallback *string `json:"gemini_model_fallback,omitempty"`
	PiperModel     *string  `json:"piper_model,omitempty"`
	PiperConfig    *string  `json:"piper_config,omitempty"`
	WhisperModel   *string  `json:"whisper_model,omitempty"`
	VADThreshold   *float64 `json:"vad_threshold,omitempty"`
	BargeInGuardMs *int     `json:"barge_in_guard_ms,omitempty"`
	TTSAcousticTailMs *int  `json:"tts_acoustic_tail_ms,omitempty"`
	SystemPrompt   *string  `json:"system_prompt,omitempty"`
	SystemTone     *string  `json:"system_tone,omitempty"`
	KBPaused       *bool    `json:"kb_paused,omitempty"`
}

type Store struct {
	mu       sync.RWMutex
	path     string
	base     *Config
	override Overrides
}

func NewStore(c *Config) *Store {
	s := &Store{path: filepath.Join(c.DataDir, "runtime_overrides.json"), base: c}
	_ = os.MkdirAll(c.DataDir, 0o755)
	s.load()
	return s
}

// NewEphemeralStore wraps a Config without persisting overrides. Used for
// scratch clones (e.g. rendering a voice sample without mutating the live
// store).
func NewEphemeralStore(c *Config) *Store {
	return &Store{path: "", base: c}
}

func (s *Store) load() {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var o Overrides
	if json.Unmarshal(b, &o) == nil {
		s.override = o
	}
}

func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	b, _ := json.MarshalIndent(s.override, "", "  ")
	return os.WriteFile(s.path, b, 0o644)
}

// Snapshot returns a Config with overrides applied.
func (s *Store) Snapshot() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := *s.base
	if s.override.LLMProvider != nil {
		c.LLMProvider = *s.override.LLMProvider
	}
	if s.override.LLMModel != nil {
		c.LLMModel = *s.override.LLMModel
	}
	if s.override.GeminiAPIKey != nil {
		c.GeminiAPIKey = *s.override.GeminiAPIKey
	}
	if s.override.GroqAPIKey != nil {
		c.GroqAPIKey = *s.override.GroqAPIKey
	}
	if s.override.LLMTemperature != nil {
		c.LLMTemperature = *s.override.LLMTemperature
	}
	if s.override.LLMMaxTokens != nil {
		c.LLMMaxTokens = *s.override.LLMMaxTokens
	}
	if s.override.PiperModel != nil {
		c.PiperModel = *s.override.PiperModel
	}
	if s.override.PiperConfig != nil {
		c.PiperConfig = *s.override.PiperConfig
	}
	if s.override.WhisperModel != nil {
		c.WhisperModel = *s.override.WhisperModel
	}
	if s.override.VADThreshold != nil {
		c.VADThreshold = *s.override.VADThreshold
	}
	if s.override.BargeInGuardMs != nil {
		c.BargeInGuardMs = *s.override.BargeInGuardMs
	}
	if s.override.TTSAcousticTailMs != nil {
		c.TTSAcousticTailMs = *s.override.TTSAcousticTailMs
	}
	// Gemini model chain: UI sets llm_model to primary; optional gemini_model_fallback = CSV of fallbacks.
	if EffectiveLLMProvider(c) == "gemini" {
		if s.override.GeminiModelFallback != nil && strings.TrimSpace(*s.override.GeminiModelFallback) != "" {
			c.GeminiModels = splitCommaNonEmpty(*s.override.GeminiModelFallback)
		} else if strings.TrimSpace(c.LLMModel) != "" {
			c.GeminiModels = []string{strings.TrimSpace(c.LLMModel)}
		}
	}
	return c
}

// Overrides returns the raw override map (for /api/settings GET).
func (s *Store) Overrides() Overrides {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.override
}

// MaskOverrides hides API key material for JSON sent to the browser.
func MaskOverrides(o Overrides) Overrides {
	out := o
	if o.GeminiAPIKey != nil && *o.GeminiAPIKey != "" {
		s := "(configured)"
		out.GeminiAPIKey = &s
	}
	if o.GroqAPIKey != nil && *o.GroqAPIKey != "" {
		s := "(configured)"
		out.GroqAPIKey = &s
	}
	return out
}

// Base returns the immutable base config.
func (s *Store) Base() *Config { return s.base }

// Patch merges non-nil fields from delta and saves.
func (s *Store) Patch(delta Overrides) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if delta.LLMProvider != nil {
		s.override.LLMProvider = delta.LLMProvider
	}
	if delta.LLMModel != nil {
		s.override.LLMModel = delta.LLMModel
	}
	if delta.GeminiAPIKey != nil {
		v := strings.TrimSpace(*delta.GeminiAPIKey)
		// Ignore masked placeholder and empty (keeps existing key on disk).
		if v != "" && v != "(configured)" {
			s.override.GeminiAPIKey = delta.GeminiAPIKey
		}
	}
	if delta.GroqAPIKey != nil {
		v := strings.TrimSpace(*delta.GroqAPIKey)
		if v != "" && v != "(configured)" {
			s.override.GroqAPIKey = delta.GroqAPIKey
		}
	}
	if delta.GeminiModelFallback != nil {
		s.override.GeminiModelFallback = delta.GeminiModelFallback
	}
	if delta.LLMTemperature != nil {
		s.override.LLMTemperature = delta.LLMTemperature
	}
	if delta.LLMMaxTokens != nil {
		s.override.LLMMaxTokens = delta.LLMMaxTokens
	}
	if delta.PiperModel != nil {
		s.override.PiperModel = delta.PiperModel
	}
	if delta.PiperConfig != nil {
		s.override.PiperConfig = delta.PiperConfig
	}
	if delta.WhisperModel != nil {
		s.override.WhisperModel = delta.WhisperModel
	}
	if delta.VADThreshold != nil {
		s.override.VADThreshold = delta.VADThreshold
	}
	if delta.BargeInGuardMs != nil {
		s.override.BargeInGuardMs = delta.BargeInGuardMs
	}
	if delta.TTSAcousticTailMs != nil {
		s.override.TTSAcousticTailMs = delta.TTSAcousticTailMs
	}
	if delta.SystemPrompt != nil {
		s.override.SystemPrompt = delta.SystemPrompt
	}
	if delta.SystemTone != nil {
		s.override.SystemTone = delta.SystemTone
	}
	if delta.KBPaused != nil {
		s.override.KBPaused = delta.KBPaused
	}
	return s.save()
}

func splitCommaNonEmpty(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// --- env parsing helpers ---

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envFloat(k string, def float64) float64 {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

// loadDotenv is a tiny .env loader (no external dep). Existing env vars win.
func loadDotenv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.Index(line, "="); i > 0 {
			k := strings.TrimSpace(line[:i])
			v := strings.TrimSpace(line[i+1:])
			v = strings.Trim(v, `"'`)
			if _, set := os.LookupEnv(k); !set {
				_ = os.Setenv(k, v)
			}
		}
	}
}
