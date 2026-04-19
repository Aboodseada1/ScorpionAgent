package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"scorpion/agent/internal/config"
)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Admin-Token")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// checkAdmin returns true if the request presents the configured admin token, or if
// admin auth is disabled (empty ADMIN_TOKEN / AdminToken).
func checkAdmin(d *Deps, r *http.Request) bool {
	want := d.Store.Base().AdminToken
	if want == "" {
		return true
	}
	h := r.Header.Get("Authorization")
	tok := strings.TrimPrefix(h, "Bearer ")
	if tok == "" {
		tok = r.Header.Get("X-Admin-Token")
	}
	return tok == want
}

// requireAdmin is a no-op if ADMIN_TOKEN is empty (trusted LAN mode).
func requireAdmin(d *Deps, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !checkAdmin(d, r) {
			writeErr(w, 403, "admin token required")
			return
		}
		next(w, r)
	}
}

var _ = config.Config{} // anchor
