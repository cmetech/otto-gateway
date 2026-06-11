---
finding: P-5
severity: M
rel_id: REL-POOL-05
status: confirmed
target_phase: 16
verified_at: 2026-06-11
---

# Finding P-5: Data Race on Entry.LastUsed — Written Under Different Locks in Different Sites

## Review citation

From `docs/reviews/2026-06-11-reliability-review.md` §1 P-5 (Medium):

> **Files:** `internal/session/registry.go:206` (`e.LastUsed = time.Now()` in `Get`'s alive-entry handoff, under `r.mu` only), `internal/session/entry_acp.go:77-79` (`MarkUsed` writes under `e.Mu` — handlers defer it), `internal/session/reaper.go:79` (read under `e.Mu` via TryLock), `internal/session/registry.go:358` (`time.Since(e.LastUsed)` in `watchEntry` with no lock), `internal/session/stats.go:100`.
>
> **Failure scenario:** Handler A is finishing for sid X (deferred `MarkUsed` under `e.Mu`); handler B issues a request for the same sid and `Registry.Get` writes `e.LastUsed` under `r.mu` concurrently — a write/write race on a multi-word `time.Time`. A guaranteed `-race` trip, poisoning the project's own trust gates. The codebase already fixed the identical pattern for `Entry.Dead` but missed `LastUsed`.

## Current-source check

**Current file:line:** `internal/session/registry.go:206` (write under `r.mu`), `internal/session/entry_acp.go:77-79` (MarkUsed write under `e.Mu`), `internal/session/registry.go:358` (read in watchEntry with no lock).

The data race is intact in current main. The four sites accessing `Entry.LastUsed` use inconsistent locking:

1. `registry.go:206`: `e.LastUsed = time.Now()` — executed while `r.mu` is held (the registry-wide lock), NOT `e.Mu` (the entry-level lock).
2. `entry_acp.go:77-79` (MarkUsed): `e.LastUsed = time.Now()` — executed while `e.Mu` is held (by the surface handler's `e.Mu.Lock()`).
3. `registry.go:358` (watchEntry): `time.Since(e.LastUsed)` — read in the exit-watcher goroutine with NO lock at all.
4. `reaper.go:79`: read under `e.Mu` via TryLock.

**D-09 false-positive bar explicitly checked:** `Entry.LastUsed` is NOT declared as `atomic.Int64` — it is a plain `time.Time` field. No atomic or mutex-consistent guard has been added. The codebase comment at `registry.go:195-209` acknowledges the `e.LastUsed = time.Now()` update but does not document any synchronization ensuring race-freedom with `MarkUsed`.

Concurrent `Registry.Get` (holding `r.mu`, writing `e.LastUsed`) and `MarkUsed` (holding `e.Mu`, writing `e.LastUsed`) on the same entry produce a write/write race on a multi-word `time.Time` value, exactly as described in the review.

## Evidence

Regression test: `internal/session/regression_rel_pool_05_test.go::TestRegression_REL_POOL_05_LastUsedRace`

Skip string: `t.Skip("REL-POOL-05 (P-5): regression test — unskip in Phase 16 fix commit")`

The reproducer fires 64 goroutines tight-looping `Registry.Get` + `e.MarkUsed` against the same session ID under `go test -race`. The t.Skip is the first line of the test body so the race detector never reaches the actual concurrent code during Phase 14 (the test runs as SKIP). When Phase 16 deletes the t.Skip line, if `LastUsed` is still a plain `time.Time` with inconsistent locks, `go test -race` will report a DATA RACE; the fix (atomic.Int64 or consistent e.Mu discipline) makes the test pass clean under -race.

## Verdict

**confirmed**

`Entry.LastUsed` is a plain `time.Time` written under `r.mu` at `registry.go:206` and under `e.Mu` at `entry_acp.go:77-79`, with a lockless read at `registry.go:358`. No atomic guard has been added. Per D-09, a `false-positive` requires a concrete guard at file:line — none exists. Per D-11 bias: confirmed. The finding was independently discovered by two review passes (pool-lifecycle and concurrency-discipline), as noted in the review document.
