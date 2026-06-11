---
phase: 17-trust-gate-restoration
reviewed: 2026-06-11T00:00:00Z
depth: standard
files_reviewed: 10
files_reviewed_list:
  - internal/canonical/errors.go
  - internal/canonical/errors_test.go
  - internal/pool/pool.go
  - internal/pool/regression_rel_pool_02_test.go
  - internal/pool/regression_rel_pool_01_test.go
  - internal/server/server.go
  - internal/adapter/anthropic/handlers.go
  - internal/adapter/ollama/handlers.go
  - internal/adapter/openai/handlers.go
  - cmd/otto-tray/tray.go
findings:
  critical: 0
  warning: 1
  info: 2
  total: 3
status: clean
---

# Phase 17: Code Review Report

**Reviewed:** 2026-06-11
**Depth:** standard
**Files Reviewed:** 10
**Status:** clean

## Summary

Phase 17 is mechanical-bounded — three plans restoring `make ci` to clean
exit-0:

- **17-01** (f727b24): Relocate `ErrPoolExhausted` to `internal/canonical`;
  `internal/pool` re-exports as `var ErrPoolExhausted =
  canonical.ErrPoolExhausted`.
- **17-02** (ca258f9): Test-hygiene fix for the REL-POOL-02 goleak flake —
  added `resultWg` tracking + unique per-instance sids + drain-Chunks-then-
  Result pattern.
- **17-03** (b78fd09): Mechanical batch — gofmt (server.go), gofumpt
  (regression_rel_pool_01_test.go), gosec G301/G306 tightening (tray.go),
  removeSlot dead-code removal.

### Adversarial probes attempted (all clean)

| Probe | Result |
| --- | --- |
| Is `canonical.ErrPoolExhausted` defined with stable `errors.New(...)`? | Yes — single `errors.New("pool: all workers busy; retry in 5s")` at canonical/errors.go:32, message-string locked by canonical/errors_test.go. |
| Does the pool alias preserve `errors.Is` identity? | Yes — `var ErrPoolExhausted = canonical.ErrPoolExhausted` (pool.go:21) is a pointer copy; both vars hold the same `*errorString`. `errors.Is` falls through to `==` and matches. |
| Do adapter handlers reference `canonical.ErrPoolExhausted` exclusively? | Yes — `grep -rn "pool.ErrPoolExhausted"` returns hits only in pool internals + documentation; all six adapter error-mapping sites (anthropic ×2, ollama ×4, openai ×2) use `canonical.ErrPoolExhausted`. |
| Does the 17-02 test fix introduce a new race? | No — `resultWg.Add(1)` is on the outer goroutine (before its `wg.Done`), so `resultWg.Wait` after `wg.Wait` is well-ordered. Inner goroutines drain Chunks before Result, which closes the close-vs-read window cleanly per the inline rationale. |
| Does the 17-02 sid-tagging change handler routing? | No — sids are still opaque strings registered in `pool.sessionSlots`; the test just stops both clients overwriting the same map key, which previously masked the WR-04 per-client cancel assertion. |
| Is gosec tightening correct for support-bundle staging? | Yes — `0o600` on `last-error.log` and `0o750` on `$installRoot/support/` are correct for a per-user staging file containing potentially sensitive stderr (paths, env). Verified no cross-user reader exists in production code (`grep -rn 'last-error.log'` returns only the writer site). |
| Did `removeSlot` removal leave dangling callers? | No — the symbol is gone from production code (only the in-source historical comment at pool.go:277 and test comments referencing the pre-fix path remain). |
| Did the pool's NewSession dead-slot branch lose a recovery path with `removeSlot` gone? | No — both transient and ctx-cancel arms re-queue the slot under p.mu with `slot.dead = true` (pool.go:720, pool.go:757) so the next acquirer trips a fresh respawn deterministically. The old drop-from-p.all path was replaced with re-queue in a prior phase; Phase 17 just removed the orphaned helper. |

The Phase 17 change-set ships safely.

## Warnings

### WR-01: Sleep-based synchronization in REL-POOL-02 regression test

**File:** `internal/pool/regression_rel_pool_02_test.go:159, 180`
**Severity:** WARNING
**Pre-existing:** Yes — not introduced by Phase 17; 17-02 simply unskipped
the test by fixing the resultWg/sid issues. Flagged because Phase 17 makes
this test ACTIVE in CI, so its flake surface area is now load-bearing.

**Issue:** The test uses two `time.Sleep` calls as synchronization edges:

1. `time.Sleep(100 * time.Millisecond)` at line 159 to "wait for both
   sessions to be established" before measuring `cancelsBefore`.
2. `time.Sleep(50 * time.Millisecond)` at line 180 to "give sessions a
   moment to receive the Cancel signal" before the `cancelsAfter` assertion.

Both are time-based heuristics. Under CI load (`go test -race
-count=20 -p=8`) the 100ms window can be exhausted before both
NewSession+Prompt pairs have registered in `pool.sessionSlots` — the
`cancelsAfter` assertion would then fail with `cancelsAfter < 2` because
`p.Close()` ran with fewer than two in-flight sessions.

The 17-02 SUMMARY documents the deflake at 17/17 across one local run,
but the new test still depends on these sleeps being long enough. A
future CI runner under load may regress flakiness here without any
production-code change.

**Fix:** Replace the 100ms sleep with a deterministic wait on the
sessions slice reaching len == 2 under sessionsMu (poll loop with
1-second deadline), and replace the 50ms post-Close sleep with a
poll on each fake client's `cancelCallList()` length reaching ≥1
(again with a deadline). Sketch:

```go
// Replace line 159:
deadline := time.Now().Add(time.Second)
for {
    sessionsMu.Lock()
    n := len(sessions)
    sessionsMu.Unlock()
    if n >= 2 {
        break
    }
    if time.Now().After(deadline) {
        t.Fatal("sessions did not establish within 1s")
    }
    time.Sleep(5 * time.Millisecond)
}

// Replace line 180:
deadline = time.Now().Add(time.Second)
for {
    if len(bc0.cancelCallList()) >= 1 && len(bc1.cancelCallList()) >= 1 {
        break
    }
    if time.Now().After(deadline) {
        break // fall through to the existing assertion which will fail with diagnostic
    }
    time.Sleep(5 * time.Millisecond)
}
```

Same pattern as `regression_rel_pool_01_test.go:91-97` already uses to
poll for `p.Stats().Alive == 1`. Not blocking for ship — the SUMMARY's
20-iteration clean run gives confidence — but worth a follow-up.

## Info

### IN-01: Dead `sessions`/`sessionsMu` variables in REL-POOL-02 test

**File:** `internal/pool/regression_rel_pool_02_test.go:109-110, 122-124`
**Severity:** INFO
**Pre-existing:** Yes — not changed by Phase 17.

**Issue:** The `sessions []string` slice (line 109) and its guarding
`sessionsMu sync.Mutex` (line 110) are appended to (line 122-124) but
never read. The collected sids are unused by any subsequent assertion.

```go
sessionsMu.Lock()
sessions = append(sessions, sid)
sessionsMu.Unlock()
```

If kept, the slice will be useful for the WR-01 deterministic-wait fix
suggested above. If WR-01 is declined, this is just dead state.

**Fix:** Either consume `sessions` for the deterministic-wait pattern in
WR-01, or delete both `sessions` and `sessionsMu` and the append site.

### IN-02: Stale comment reference to `removeSlot`

**File:** `internal/pool/respawn_ctx_cancel_test.go:119` (not in Phase 17
files-to-review list but surfaced by grep for `removeSlot`)
**Severity:** INFO

**Issue:** `respawn_ctx_cancel_test.go` (file outside the Phase 17 review
scope but transitively affected by the dead-code removal) still has a
narrative comment referencing the now-deleted `removeSlot` helper. Phase
17 left similar historical comments in `pool.go:277`,
`regression_rel_pool_01_test.go:38/46/50/101` — those are arguably
load-bearing pre-fix archaeology that documents the historical bug being
guarded against. The `respawn_ctx_cancel_test.go:119` reference is the
same shape and is acceptable; calling it out so a future reader does not
mistake it for a live API reference.

**Fix:** None required. If a future cleanup pass refreshes historical
comments, replace "called removeSlot" with "called removeSlot (helper
removed in Phase 17 — slot is now re-queued under p.mu)" for clarity.

---

_Reviewed: 2026-06-11_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
