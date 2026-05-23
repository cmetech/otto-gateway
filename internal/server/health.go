package server

import (
	"encoding/json"
	"net/http"
	"time"
)

// HealthResponse is the JSON body returned by GET /health.
// Shape is locked per D-12; future phases add fields to sub-structs only.
type HealthResponse struct {
	// Status is always "ok" in Phase 1.
	Status string `json:"status"`
	// Version is the embedded binary version (set via -ldflags).
	Version string `json:"version"`
	// UptimeSeconds is the number of seconds the server has been running.
	UptimeSeconds float64 `json:"uptime_seconds"`
	// Pool reports ACP worker subprocess pool state.
	Pool PoolStats `json:"pool"`
	// Sessions reports active ACP session state.
	Sessions SessionStats `json:"sessions"`
	// Embeddings reports loaded embedding model state.
	Embeddings EmbeddingStats `json:"embeddings"`
}

// PoolStats reports ACP worker subprocess pool state.
// Populated by Phase 5; zero values correct for Phase 1.
type PoolStats struct {
	// Size is the configured pool size.
	Size int `json:"size"`
	// Alive is the number of alive workers.
	Alive int `json:"alive"`
	// Busy is the number of workers currently handling a request.
	Busy int `json:"busy"`
}

// SessionStats reports active ACP session state.
// Populated by Phase 5; zero values correct for Phase 1.
type SessionStats struct {
	// Active is the number of active sessions.
	Active int `json:"active"`
}

// EmbeddingStats reports loaded embedding model state.
// Populated by Phase 7; zero values correct for Phase 1.
type EmbeddingStats struct {
	// ModelsLoaded is the number of embedding models currently loaded.
	ModelsLoaded int `json:"models_loaded"`
}

// healthHandler handles GET /health.
// Phase 1: always returns 200 with zero sub-stats.
func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
	resp := HealthResponse{
		Status:        "ok",
		Version:       s.version,
		UptimeSeconds: time.Since(s.start).Seconds(),
		// Pool, Sessions, Embeddings are zero-value — correct for Phase 1.
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		// Cannot write error response after WriteHeader — just log it.
		LoggerFromCtx(r.Context(), s.logger).Error("health encode", "err", err)
	}
}

// versionHandler handles GET /api/version.
// Phase 1: returns version and commit fields.
// Note: Phase 2 moves this to internal/adapter/ollama (trivial refactor, D-11).
func (s *Server) versionHandler(w http.ResponseWriter, r *http.Request) {
	resp := struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
	}{
		Version: s.version,
		Commit:  s.commit,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		LoggerFromCtx(r.Context(), s.logger).Error("version encode", "err", err)
	}
}
