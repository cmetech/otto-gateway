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
//
// Status is the D-05 operational-status enum added by Plan 16-02 Task 3.
// One of: "ok" | "degraded" | "exhausted". JSON-backwards-compatible:
// new field; existing consumers ignore unknown keys. The empty string
// is the zero value rendered when no PoolStatsSource is wired (Phase 1
// pre-pool degraded-mode), so the field omitempty rule is intentionally
// NOT applied — explicit "" signals "pool not wired" to consumers
// that distinguish that case (Plan 16-04 tray probe).
type PoolStats struct {
	// Size is the configured pool size.
	Size int `json:"size"`
	// Alive is the number of alive workers.
	Alive int `json:"alive"`
	// Busy is the number of workers currently handling a request.
	Busy int `json:"busy"`
	// Status is the D-05 operational status: "ok", "degraded", or
	// "exhausted". Empty when no pool source is wired (KIRO_CMD unset).
	Status string `json:"status"`
}

// poolDegradedStallThreshold is the D-05a window after which a
// fully-saturated pool (Busy == Alive == Size) with no forward
// progress flips Status to "degraded". A compile-time constant — not
// operator-tunable in v1.9 per the D-04 env-surface restraint
// (16-CONTEXT.md "Noted for Later" reserves POOL_DEGRADED_STALL_SEC
// for v1.10+ if operators report false-positives on slow networks).
const poolDegradedStallThreshold = 30 * time.Second

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
// Phase 2 (Plan 06 OBSV-01): renders Pool.Stats() into PoolStats when
// the server was constructed with a non-nil PoolStatsSource. Nil-safe —
// when KIRO_CMD is unset the pool is also unset and PoolStats stays at
// the zero value (Size/Alive/Busy all 0), matching the Phase 1 review-
// fix posture.
//
// Phase 5 (Plan 05-03): also populates Sessions.Active from the
// configured RegistryStatsSource. Nil-safe — when KIRO_CMD is unset the
// registry is also unset and SessionStats stays at the zero value.
func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
	var ps PoolStats
	if s.pool != nil {
		ps = s.pool.Stats()
		// D-05 Status enum (Plan 16-02 Task 3). Priority order:
		//   1. exhausted — every spawned slot is checked out (D-05b).
		//   2. degraded  — fully saturated AND no forward progress
		//      in poolDegradedStallThreshold (D-05a).
		//   3. ok        — default.
		switch {
		case s.pool.IsExhausted():
			ps.Status = "exhausted"
		case ps.Size > 0 && ps.Busy == ps.Alive && ps.Busy == ps.Size &&
			time.Since(s.pool.LastProgressAt()) > poolDegradedStallThreshold:
			ps.Status = "degraded"
		default:
			ps.Status = "ok"
		}
	}
	var ss SessionStats
	if s.registry != nil {
		ss = s.registry.Stats()
	}
	resp := HealthResponse{
		Status:        "ok",
		Version:       s.version,
		UptimeSeconds: time.Since(s.start).Seconds(),
		Pool:          ps,
		Sessions:      ss,
		// Embeddings is zero-value — Phase 7 surface.
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
