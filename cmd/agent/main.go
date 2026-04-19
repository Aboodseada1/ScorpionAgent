// Command agent is the Go voice-SDR server.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"scorpion/agent/internal/api"
	"scorpion/agent/internal/config"
	"scorpion/agent/internal/downloader"
	"scorpion/agent/internal/kb"
	"scorpion/agent/internal/llm"
	"scorpion/agent/internal/llmmodels"
	"scorpion/agent/internal/memory"
	"scorpion/agent/internal/stt"
	"scorpion/agent/internal/sysmetrics"
	"scorpion/agent/internal/tts"
	"scorpion/agent/internal/voices"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg := config.Load()
	store := config.NewStore(cfg)

	mem, err := memory.Open(cfg.DataDir)
	if err != nil {
		slog.Error("open memory db", "err", err)
		os.Exit(1)
	}
	defer mem.Close()

	llmClient := llm.NewClient(store)
	sttClient := stt.NewWhisperHTTP(store)
	ttsPool := tts.NewPool(store)
	kbStore := kb.New(mem, store)

	// Voice file discovery: primary under /opt/piper/voices, fallback to
	// $MODELS_DIR/voices for user-downloaded ones via the UI.
	voiceDirs := []string{
		"/opt/piper/voices",
		filepath.Join(cfg.ModelsDir, "voices"),
	}
	if v := os.Getenv("PIPER_VOICES_DIR"); v != "" {
		voiceDirs = append([]string{v}, voiceDirs...)
	}
	voiceStore := voices.NewStore(voiceDirs...)

	// LLM model management (lists .gguf files, swaps via systemctl).
	llmDirs := []string{
		filepath.Join(cfg.ModelsDir),
		filepath.Join(cfg.DataDir, "models", "llm"),
	}
	if v := os.Getenv("LLM_MODELS_DIR"); v != "" {
		llmDirs = append([]string{v}, llmDirs...)
	}
	llmUnit := envOrDefault("LLM_SYSTEMD_UNIT", "llama-qwen.service")
	llmEnvFile := envOrDefault("LLM_SYSTEMD_ENVFILE", filepath.Join(cfg.DataDir, "llama.env"))
	models := llmmodels.NewManager(llmUnit, llmEnvFile, llmDirs...)

	downloads := downloader.New()

	// System metrics collector — watch the services we care about.
	svcList := []string{
		envOrDefault("AGENT_SYSTEMD_UNIT", "agent-voice.service"),
		llmUnit,
	}
	metrics := sysmetrics.NewCollector(svcList, []string{"/"})

	deps := &api.Deps{
		Store:     store,
		Mem:       mem,
		LLM:       llmClient,
		STT:       sttClient,
		TTS:       ttsPool,
		KB:        kbStore,
		Voices:    voiceStore,
		Models:    models,
		Downloads: downloads,
		Metrics:   metrics,
	}

	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.Port),
		Handler:           api.Router(deps),
		ReadHeaderTimeout: 15 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go metrics.Run(ctx, 2*time.Second)

	// Opportunistic embedding backfill: if an embedder is reachable and any
	// existing docs are missing embeddings, compute them in the background.
	// Silently no-ops when the embedder isn't configured/available.
	go func() {
		time.Sleep(3 * time.Second)
		bctx, bcancel := context.WithTimeout(ctx, 5*time.Minute)
		defer bcancel()
		kbStore.BackfillEmbeddings(bctx)
	}()

	// No boot-time LLM/TTS warmup: weights stay cold until a call needs them.
	// The web UI calls POST /api/session/warmup when the user starts a call,
	// which primes LLM + Piper in parallel right before the WebSocket opens.

	go func() {
		slog.Info("http listen", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	slog.Info("shutdown")
	sh, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	_ = srv.Shutdown(sh)
	ttsPool.Close()
}

func envOrDefault(k, def string) string {
	v := os.Getenv(k)
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
