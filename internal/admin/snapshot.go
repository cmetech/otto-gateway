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

	// Gateway process resource usage (procstat, cgo-free). ProcessCPUSeconds is
	// cumulative CPU time — the dashboard derives a live percent by diffing
	// successive polls. ProcessStatOK is false on platforms where the read is
	// unavailable (darwin dev box); the UI then shows "n/a" instead of a
	// misleading zero. GeneratedAt is the wall-clock reference for the CPU diff.
	ProcessCPUSeconds float64 `json:"process_cpu_seconds"`
	ProcessRSSBytes   uint64  `json:"process_rss_bytes"`
	ProcessStatOK     bool    `json:"process_stat_ok"`
}

// SnapshotPool is the pool sub-object of Snapshot.
//
// SpawnFailing is an additive current-health flag (recency-bounded in the pool,
// NOT the sticky historical error). The dashboard reads it alongside Status to
// choose a not-alive slot's tier: yellow "Recovering…" while the pool is still
// serving with no current spawn failure, red "Failed" when status=="down" or
// spawn_failing is true.
type SnapshotPool struct {
	Size         int            `json:"size"`
	Alive        int            `json:"alive"`
	Busy         int            `json:"busy"`
	SpawnFailing bool           `json:"spawn_failing"`
	Slots        []SnapshotSlot `json:"slots"`
}

// SnapshotSlot is the per-slot detail row in the admin snapshot.
// CurrentSessionID is *string so an idle slot renders as
// "current_session_id": null rather than an empty string.
type SnapshotSlot struct {
	Label            string  `json:"label"`
	Alive            bool    `json:"alive"`
	Busy             bool    `json:"busy"`
	CurrentSessionID *string `json:"current_session_id"`

	// Per-worker resource usage, merged in by snapshotHandler from the
	// ProcSampler keyed on Label. CPUSeconds is cumulative (percent derived
	// client-side by diffing polls). StatOK is false when the worker's process
	// could not be read (dead/respawning slot, or an unsupported platform), and
	// the dashboard renders "n/a" for that slot.
	CPUSeconds float64 `json:"cpu_seconds"`
	RSSBytes   uint64  `json:"rss_bytes"`
	StatOK     bool    `json:"stat_ok"`
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

// ProcSample is the admin-package projection of a process's CPU/RSS reading.
// OK is false when the sample is unavailable (unreadable pid, or an unsupported
// platform such as the darwin dev box).
type ProcSample struct {
	CPUSeconds float64
	RSSBytes   uint64
	OK         bool
}

// ProcSampler surfaces process resource usage for the dashboard perf tiles.
// Self is the gateway process; Workers maps a pool slot's Label to that
// worker's sample. Implemented by the cmd wiring over internal/procstat so admin
// stays free of internal/pool and internal/procstat imports (TRST-04 boundary).
type ProcSampler interface {
	Self() ProcSample
	Workers() map[string]ProcSample
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
		snap.Pool.SpawnFailing = h.deps.PoolDetail.SpawnFailing()
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

	// Process perf — gateway self + per-worker, merged by slot label. Nil-safe:
	// when Proc is unset the fields stay zero-valued with StatOK=false and the
	// dashboard renders the tiles as unavailable.
	if h.deps.Proc != nil {
		self := h.deps.Proc.Self()
		snap.ProcessCPUSeconds = self.CPUSeconds
		snap.ProcessRSSBytes = self.RSSBytes
		snap.ProcessStatOK = self.OK

		workers := h.deps.Proc.Workers()
		for i := range snap.Pool.Slots {
			if ws, ok := workers[snap.Pool.Slots[i].Label]; ok && ws.OK {
				snap.Pool.Slots[i].CPUSeconds = ws.CPUSeconds
				snap.Pool.Slots[i].RSSBytes = ws.RSSBytes
				snap.Pool.Slots[i].StatOK = true
			}
		}
	}

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
