// GET /health/pool — pool occupancy + last-spawn-error introspection.
//
// Read-only. Auth-exempt (mounted on the OUTER router alongside
// /health, /health/hooks, /health/agents). Mutating verbs return 405
// with Allow: GET — same pattern as hooks_handler / agentsHandler.
//
// Why this exists: Stats() and /health both surface "the gateway is
// up" but neither answers "is the pool actually serving chat
// requests?" When KIRO_CMD breaks mid-flight, every slot can fail to
// respawn and pool.all shrinks to zero. /health still returns 200, so
// monitors don't fire until end-users complain about 503s on chat. The
// `healthy` boolean here is the single-field signal a monitor / smoke
// script needs to detect that state; `last_spawn_error` is the
// diagnostic context so operators don't have to grep logs.
//
// Healthy semantic (mirrors pool.Pool.HealthSummary):
//   - Size == 0      → healthy (operator deliberately ran without
//                      KIRO_CMD; this is degraded mode by design, not
//                      a failure)
//   - Alive  > 0     → healthy
//   - otherwise      → unhealthy
//
// T-8-LEAK note: last_spawn_error can include the KIRO_CMD path
// (e.g., "fork/exec /usr/local/bin/kiro: no such file or directory").
// That is operator-known configuration, not a secret. We do NOT
// surface AUTH_TOKEN, PII_HASH_KEY, kiro stderr, or any env value —
// only the spawn-failure error string captured by recordSpawnErr.

package server

import (
	"encoding/json"
	"net/http"
	"time"
)

// PoolHealth is the wire shape returned by GET /health/pool. JSON tags
// are the load-bearing contract — monitors and dashboards depend on
// the snake_case names. `omitempty` on the LastSpawnError* pair so the
// healthy-baseline response stays tidy. LastSpawnErrAt is a pointer
// because Go's encoding/json treats a zero time.Time struct as a
// non-empty value (omitempty drops only the underlying-type zero —
// which for structs means "every field zeroed", but it serializes the
// field as "0001-01-01T00:00:00Z" rather than dropping it). The
// pointer makes the omission unambiguous.
type PoolHealth struct {
	Size           int        `json:"size"`
	Alive          int        `json:"alive"`
	Busy           int        `json:"busy"`
	Healthy        bool       `json:"healthy"`
	LastSpawnError string     `json:"last_spawn_error,omitempty"`
	LastSpawnErrAt *time.Time `json:"last_spawn_error_at,omitempty"`
}

// PoolHealthSource is the consumer-defined interface poolHandler uses
// to fetch the snapshot. The cmd/otto-gateway cmdPoolHealthAdapter
// wraps *pool.Pool to satisfy this without importing internal/pool
// into server's public surface (TRST-04). A nil source produces the
// canonical "no pool wired" response (Size=0, Healthy=true).
type PoolHealthSource interface {
	Health() PoolHealth
}

// PoolResponse wraps PoolHealth as the body of GET /health/pool. We
// nest under a top-level `pool` key for forward-compatibility — future
// fields (per-slot detail, model catalog summary) live alongside `pool`
// without shape changes for existing consumers.
type PoolResponse struct {
	Pool PoolHealth `json:"pool"`
}

// poolHandler handles GET /health/pool. SC7-equivalent: non-GET → 405
// with Allow: GET. Defense-in-depth — chi's .Get() registration
// already routes only GET, but spelling 405 out at the handler keeps
// the no-mutate contract explicit and matches the hooks_handler /
// agentsHandler patterns operators learn once.
func (s *Server) poolHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var ph PoolHealth
	if s.poolHealth != nil {
		ph = s.poolHealth.Health()
	} else {
		// Nil source: canonical "no pool wired" envelope. Healthy is
		// true because the absence of a pool is an operator choice
		// (degraded mode), not a fault — same rationale as Size == 0
		// in HealthSummary.
		ph = PoolHealth{Healthy: true}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(PoolResponse{Pool: ph}); err != nil {
		LoggerFromCtx(r.Context(), s.logger).Error("pool encode", "err", err)
	}
}
