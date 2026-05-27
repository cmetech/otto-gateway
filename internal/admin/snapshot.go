package admin

import (
	"encoding/json"
	"net/http"
	"time"
)

// AdminSnapshot is the unified JSON shape returned by GET /admin/api/snapshot.
// It composes pool detail + session detail into one atomic snapshot taken at
// one instant on the server (D-05 — single aggregator endpoint).
// Snake_case JSON tags are the load-bearing wire contract.
type AdminSnapshot struct {
	Status        string        `json:"status"`
	Version       string        `json:"version"`
	Commit        string        `json:"commit"`
	UptimeSeconds float64       `json:"uptime_seconds"`
	GeneratedAt   time.Time     `json:"generated_at"`
	Pool          SnapshotPool  `json:"pool"`
	Sessions      []SnapshotSess `json:"sessions"`
}

// SnapshotPool is the pool sub-object of AdminSnapshot.
type SnapshotPool struct {
	Size  int            `json:"size"`
	Alive int            `json:"alive"`
	Busy  int            `json:"busy"`
	Slots []SnapshotSlot `json:"slots"`
}

// SnapshotSlot is the per-slot detail row in the admin snapshot.
// CurrentSessionID is *string so an idle slot renders as
// "current_session_id": null rather than an empty string.
type SnapshotSlot struct {
	Label            string  `json:"label"`
	Alive            bool    `json:"alive"`
	Busy             bool    `json:"busy"`
	CurrentSessionID *string `json:"current_session_id"`
}

// SnapshotSess is the per-session detail row in the admin snapshot.
// Model is *string so an unset model renders as "model": null.
// LastUsed marshals to RFC 3339 via stdlib time.Time.MarshalJSON.
type SnapshotSess struct {
	ID       string    `json:"id"`
	Alive    bool      `json:"alive"`
	Busy     bool      `json:"busy"`
	LastUsed time.Time `json:"last_used"`
	Model    *string   `json:"model"`
}

// snapshotHandler handles GET /api/snapshot.
//
// Wire shape:
//
//	{
//	  "status": "ok"|"degraded"|"down",
//	  "version": "...",
//	  "commit": "...",
//	  "uptime_seconds": 123.4,
//	  "generated_at": "2026-05-27T19:00:00Z",
//	  "pool": {"size": N, "alive": A, "busy": B, "slots": [...]},
//	  "sessions": [...]
//	}
//
// Nil-safety:
//   - When h.deps.PoolDetail is nil, pool defaults to {0,0,0,[]}.
//   - When h.deps.Registry is nil, sessions defaults to [].
//
// Cache-Control: no-store is set so client-side pollers do not cache
// the response (RESEARCH anti-pattern: pollers must not cache).
func (h *handler) snapshotHandler(w http.ResponseWriter, r *http.Request) {
	snap := AdminSnapshot{
		Version:       h.deps.Version,
		Commit:        h.deps.Commit,
		UptimeSeconds: time.Since(h.deps.Start).Seconds(),
		GeneratedAt:   time.Now().UTC(),
	}

	// Pool detail — nil-safe.
	snap.Pool.Slots = []SnapshotSlot{} // ensure non-nil JSON array
	if h.deps.PoolDetail != nil {
		slots := h.deps.PoolDetail.Detail()
		snap.Pool.Slots = slots
		snap.Pool.Size = len(slots)
		for _, sl := range slots {
			if sl.Alive {
				snap.Pool.Alive++
			}
			if sl.Busy {
				snap.Pool.Busy++
			}
		}
	}

	// Session detail — nil-safe.
	snap.Sessions = []SnapshotSess{} // ensure non-nil JSON array
	if h.deps.Registry != nil {
		snap.Sessions = h.deps.Registry.Detail()
		if snap.Sessions == nil {
			snap.Sessions = []SnapshotSess{}
		}
	}

	// Derive status from pool counts.
	snap.Status = computeStatus(snap)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(snap); err != nil {
		// Cannot write error response after WriteHeader — just log it.
		h.deps.Logger.Error("admin: snapshot encode", "err", err)
	}
}

// computeStatus derives the gateway status string from the snapshot's
// pool counts. It is a pure function so it is directly unit-testable.
//
// Rules (per behavior contract):
//   - "down" when Pool.Size == 0 OR Pool.Alive == 0
//   - "degraded" when Pool.Alive < Pool.Size
//   - "ok" otherwise (all slots alive)
func computeStatus(snap AdminSnapshot) string {
	if snap.Pool.Size == 0 || snap.Pool.Alive == 0 {
		return "down"
	}
	if snap.Pool.Alive < snap.Pool.Size {
		return "degraded"
	}
	return "ok"
}
