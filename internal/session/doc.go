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
//
// # Shutdown semantics (WR-05)
//
// Registry.Close intentionally does NOT acquire Entry.Mu before
// closing each entry's *acp.Client. Holding r.mu across slow
// Client.Close calls is the documented reverse-lock-order anti-pattern
// (pool.closeAll for the same reason). Consequence: if an HTTP
// handler is still mid-stream when Registry.Close runs — which only
// happens if the outer http.Server.Shutdown deadline expired before
// the handler completed — the Close pulls the rug out from under it.
// The handler's next stream-channel read returns EOF, the stream
// aborts, the deferred entry.Mu.Unlock + entry.MarkUsed run (writing
// time.Now to a soon-to-be-discarded Entry — harmless), and the
// HTTP response is truncated.
//
// This is by-design for shutdown. The cmd/otto-gateway main.go
// ordering is:
//
//  1. SIGINT → cancel ctx → srv.Shutdown(30s timeout) returns.
//  2. cleanup() defer fires → registry.Close() → pool.Close().
//
// In the steady state step 1 drains all handlers within the deadline
// and step 2 sees only idle entries. The truncated-response edge case
// requires a misconfigured slow handler exceeding the 30s Shutdown
// deadline AND a request still in flight when registry.Close fires;
// the resulting client-visible truncation is the lesser evil
// compared to blocking shutdown indefinitely on a stuck stream.
package session
