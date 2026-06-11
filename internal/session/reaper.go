package session

import (
	"time"
)

// reaperLoop is the per-Registry reaper goroutine. It ticks every
// cfg.TickInterval and reaps idle entries (D-10, D-12). The loop exits
// on <-r.closing (signalled by Registry.Close).
//
// Structure mirrors internal/acp/client.go pingLoop (lines 433-453):
// defer wg.Done(); time.NewTicker; defer ticker.Stop; two-branch select.
//
// Pitfall 5 (bounded shutdown): the outer select-on-closing means
// Close returns within at most TickInterval + worst-case reapOnce
// iteration. Each reapOnce iteration is cheap (TryLock + at most one
// Cancel+Close per entry), so the bound is acceptable for production
// (60s TickInterval) and tight for tests (50ms TickInterval).
func (r *Registry) reaperLoop() {
	defer r.wg.Done()
	ticker := time.NewTicker(r.cfg.TickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			r.reapOnce()
		case <-r.closing:
			return
		}
	}
}

// reapOnce performs one reaper iteration (D-10, D-11, D-12).
//
// Lock-order discipline (load-bearing — Pitfall 5 + 05-RESEARCH §Anti-Patterns):
//
//  1. Snapshot entries under r.mu.RLock and release before touching
//     any per-entry mutex. Holding r.mu across a per-entry TryLock or
//     Client.Close is the documented reverse-lock-order deadlock
//     against surface handlers (which hold Entry.Mu and need r.mu for
//     map-delete on death).
//  2. For each snapshot entry try TryLock. On failure, skip — D-12
//     guarantees a continuously-streaming session is never reaped
//     because LastUsed only updates at response complete (D-11).
//  3. On TryLock success, check LastUsed.Before(cutoff). If expired,
//     defensively Cancel + Close + map-delete (under r.mu.Lock briefly).
//  4. Unlock Entry.Mu so any subsequent caller sees the (now-dead)
//     entry — but since we map-deleted, a same-sid Get re-lazy-creates.
//
// Entries that are mid-creation (e.creating==true) are SKIPPED — they
// have no Client yet and their LastUsed is the zero time which would
// trip the cutoff. Creation is bounded by Spawn+Initialize+NewSession
// duration, well under any reasonable TTL.
func (r *Registry) reapOnce() {
	type entryAndSID struct {
		sid   string
		entry *Entry
	}

	r.mu.RLock()
	snapshot := make([]entryAndSID, 0, len(r.entries))
	for sid, e := range r.entries {
		if e == nil || e.creating {
			continue
		}
		snapshot = append(snapshot, entryAndSID{sid: sid, entry: e})
	}
	r.mu.RUnlock()

	now := time.Now()
	cutoff := now.Add(-r.cfg.TTL)
	for _, es := range snapshot {
		if !es.entry.Mu.TryLock() {
			// D-12: in-flight stream, skip this tick. The next tick
			// (within TickInterval) retries once the stream closes
			// and the surface handler's defer e.Mu.Unlock runs.
			continue
		}
		if es.entry.LastUsed().Before(cutoff) {
			// Truly idle: D-11 LastUsed only advances at response
			// complete, D-12 TryLock confirmed no in-flight stream.
			// Defensive Cancel before Close (the Cancel is best-effort
			// — Close is the load-bearing teardown).
			es.entry.Client.Cancel(es.entry.SessionID)
			closeErr := es.entry.Client.Close()
			r.mu.Lock()
			// CR-04 fix: write Entry.Dead UNDER r.mu (same mutex
			// readers in Registry.Get and Detail use). Previously
			// the Dead write happened after r.mu was released,
			// racing readers under the race detector. Defensive:
			// another path (Delete) may have already removed the
			// entry. Only delete + flip Dead if the map still points
			// to OUR entry.
			if cur, ok := r.entries[es.sid]; ok && cur == es.entry {
				delete(r.entries, es.sid)
				es.entry.Dead = true
			}
			r.mu.Unlock()
			if r.cfg.Logger != nil {
				if closeErr != nil {
					r.cfg.Logger.Warn("session: reap close failed",
						"sid", es.sid, "err", closeErr,
						"idle_for", now.Sub(es.entry.LastUsed()))
				} else {
					r.cfg.Logger.Info("session: reaped",
						"sid", es.sid,
						"idle_for", now.Sub(es.entry.LastUsed()))
				}
			}
		}
		es.entry.Mu.Unlock()
	}
}
