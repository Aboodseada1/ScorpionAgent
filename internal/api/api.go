// Package api exposes the HTTP + WebSocket surface for the agent.
package api

import (
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

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

type Deps struct {
	Store     *config.Store
	Mem       *memory.DB
	LLM       *llm.Client
	STT       stt.Client
	TTS       *tts.Pool
	KB        *kb.Store
	Voices    *voices.Store
	Models    *llmmodels.Manager
	Downloads *downloader.Manager
	Metrics   *sysmetrics.Collector

	Sessions   sync.Map // session.ID -> *session.Session
	Transports sync.Map // session.ID -> *Transport
}

// Router wires the whole public surface.
func Router(d *Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(cors)

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{"ok": true, "ts": time.Now().Unix()}) })
	r.Get("/api/status", handleStatus(d))
	r.Get("/api/config", handleConfig(d))
	r.Get("/api/settings", handleGetSettings(d))
	r.Patch("/api/settings", requireAdmin(d, handlePatchSettings(d)))
	r.Get("/api/llm/remote-models", requireAdmin(d, handleLLMRemoteModels(d)))
	r.Get("/api/groq/stats", requireAdmin(d, handleGroqStats(d)))

	// Clients
	r.Get("/api/clients", handleListClients(d))
	r.Post("/api/clients", handleCreateClient(d))
	r.Get("/api/clients/{id}", handleGetClient(d))
	r.Put("/api/clients/{id}", handleUpdateClient(d))
	r.Delete("/api/clients/{id}", handleDeleteClient(d))
	r.Post("/api/clients/{id}/clear", requireAdmin(d, handleClearClient(d)))

	// Client docs
	r.Get("/api/clients/{id}/docs", handleListDocs(d))
	r.Post("/api/clients/{id}/docs", handleCreateDoc(d))
	r.Delete("/api/docs/{id}", handleDeleteDoc(d))

	// Conversations / facts / actions
	r.Get("/api/clients/{id}/conversations", handleListConversations(d))
	r.Get("/api/clients/{id}/facts", handleListFacts(d))
	r.Get("/api/clients/{id}/actions", handleListActions(d))
	r.Get("/api/conversations/{id}", handleGetConversation(d))
	r.Get("/api/conversations/{id}/turns", handleListTurns(d))
	r.Get("/api/conversations/{id}/graph", handleGraph(d))

	// Memory graph (all clients or filtered)
	r.Get("/api/memory/graph", handleMemoryGraph(d))

	// Realtime: session lifecycle + WS audio/event multiplexer
	r.Post("/api/session/warmup", handleSessionWarmup(d))
	r.Post("/api/session/start", handleSessionStart(d))
	r.Post("/api/session/{id}/end", handleSessionEnd(d))
	r.Post("/api/session/{id}/text", handleTextTurn(d))
	r.Get("/api/session/{id}/ws", handleSessionWS(d))

	// Voices & sample playback
	r.Get("/api/voices", handleListVoices(d))
	r.Post("/api/voices/{id}/select", requireAdmin(d, handleSelectVoice(d)))
	r.Get("/api/voices/{id}/sample", handleVoiceSample(d))

	// LLM models
	r.Get("/api/models", handleListModels(d))
	r.Post("/api/models/{id}/select", requireAdmin(d, handleSelectModel(d)))

	// Downloads
	r.Get("/api/downloads", handleListDownloads(d))
	r.Post("/api/downloads", requireAdmin(d, handleStartDownload(d)))
	r.Post("/api/downloads/{id}/cancel", requireAdmin(d, handleCancelDownload(d)))
	r.Get("/api/downloads/stream", handleDownloadSSE(d))

	// System metrics
	r.Get("/api/metrics", handleMetrics(d))
	r.Get("/api/metrics/stream", handleMetricsSSE(d))

	// Static SPA
	r.Handle("/assets/*", spaFileServer(d.Store.Base().WebDistDir))
	r.Get("/*", spaIndex(d.Store.Base().WebDistDir))

	return r
}

func spaFileServer(dir string) http.Handler {
	return http.StripPrefix("/assets", http.FileServer(http.Dir(filepath.Join(dir, "assets"))))
}

func spaIndex(dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Serve favicon and index fallback.
		p := r.URL.Path
		if p == "/favicon.ico" {
			http.ServeFile(w, r, filepath.Join(dir, "favicon.ico"))
			return
		}
		if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/assets/") {
			http.NotFound(w, r)
			return
		}
		index := filepath.Join(dir, "index.html")
		http.ServeFile(w, r, index)
	}
}
