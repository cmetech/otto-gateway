// Package session implements a registry of dedicated kiro-cli sessions keyed
// by client-supplied X-Session-Id. Lives entirely outside the warm pool
// (Phase 5 D-04). Sessions are lazy-created on first request, capped at
// SESSION_MAX, and reaped after SESSION_TTL_MS of idleness.
//
// # Race resolution
//
// Four terminal paths can race for the same *Entry:
//
//	     Reaper (ticker tick) ──┐
//	                            │
//	   DELETE /v1/sessions/:id ─┤
//	                            ├──> *Entry (Cancel + Close)
//	          stream.Result() ──┤
//	                            │
//	     disconnect-ctx watch ──┘
//
// The Codex M-3 map-delete-first pattern (mirrored from internal/pool/pool.go)
// makes this race deterministic: whichever path acquires r.mu first deletes
// from the entries map; subsequent paths observe ErrSessionNotFound (or, for
// the reaper, skip via TryLock) and abort. Slow Cancel + Close calls happen
// OUTSIDE r.mu so subprocess teardown never blocks other registry operations.
//
// # Lock ordering
//
// Two mutex layers exist:
//
//   - Registry.mu (sync.RWMutex): guards the entries map + closed flag.
//     Held briefly for lookups, never across slow client operations.
//   - Entry.Mu (sync.Mutex): guards per-session Prompt serialization (D-07).
//     Surface handlers Lock before Prompt and Unlock after stream completes.
//     The reaper takes Entry.Mu.TryLock() — never blocks (D-12).
//
// At no point are both layers held simultaneously. The reaper snapshots
// entries under Registry.mu.RLock, releases, then iterates with TryLock.
// This prevents the reverse-lock-order deadlock against surface handlers
// that hold Entry.Mu and need Registry.mu for map-delete.
package session
