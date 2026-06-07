package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"
)

// Snapshot is the unified JSON shape returned by GET /admin/api/snapshot.
// It composes pool detail + session detail into one atomic snapshot taken at
// one instant on the server (D-05 — single aggregator endpoint).
// Snake_case JSON tags are the load-bearing wire contract.
//
// LogSources is the deterministic-order list of tailable log sources the
// UI dropdown populates from (quick 260529-ll2). Empty array (not null)
// when no sources are configured — defensive-copied from
// Deps.LogPathOrder so a snapshot consumer mutating the slice cannot
// reach into the live admin Deps.
type Snapshot struct {
	Status        string         `json:"status"`
	Version       string         `json:"version"`
	Commit        string         `json:"commit"`
	Debug         bool           `json:"debug"`
	ChatTrace     bool           `json:"chat_trace"`
	UptimeSeconds float64        `json:"uptime_seconds"`
	GeneratedAt   time.Time      `json:"generated_at"`
	Pool          SnapshotPool   `json:"pool"`
	Sessions      []SnapshotSess `json:"sessions"`
	LogSources    []string       `json:"log_sources"`
}

// SnapshotPool is the pool sub-object of Snapshot.
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
//	  "debug": true|false,
//	  "chat_trace": true|false,
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
	snap := Snapshot{
		Version:       h.deps.Version,
		Commit:        h.deps.Commit,
		Debug:         h.deps.Debug,
		ChatTrace:     h.deps.ChatTrace,
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

	// Quick 260529-ll2 — log sources. Defensive copy via append so a
	// caller that mutates the snapshot slice does not reach into
	// h.deps.LogPathOrder. Empty Deps.LogPathOrder renders [] (not
	// null) because the JSON encoder marshals a zero-length non-nil
	// slice as an empty array.
	snap.LogSources = append([]string{}, h.deps.LogPathOrder...)

	// Derive status from pool counts.
	snap.Status = computeStatus(snap)

	// WR-05 mitigation: encode into a buffer first so an encoder failure
	// can still surface as a clean 500 instead of a truncated 200 body.
	// In practice json.Marshal on this shape is highly unlikely to fail
	// (no custom MarshalJSON paths, no unsupported types), but the cost
	// of buffering one snapshot is negligible.
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(snap); err != nil {
		h.deps.Logger.Error("admin: snapshot encode", "err", err)
		http.Error(w, "admin snapshot encode failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(buf.Bytes()); err != nil {
		h.deps.Logger.Debug("admin: snapshot write", "err", err)
	}
}

// computeStatus derives the gateway status string from the snapshot's
// pool counts. It is a pure function so it is directly unit-testable.
//
// Rules (per behavior contract):
//   - "down" when Pool.Size == 0 OR Pool.Alive == 0
//   - "degraded" when Pool.Alive < Pool.Size
//   - "ok" otherwise (all slots alive)
func computeStatus(snap Snapshot) string {
	if snap.Pool.Size == 0 || snap.Pool.Alive == 0 {
		return "down"
	}
	if snap.Pool.Alive < snap.Pool.Size {
		return "degraded"
	}
	return "ok"
}
