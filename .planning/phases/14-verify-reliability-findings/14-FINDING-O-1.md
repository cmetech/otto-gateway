---
finding: O-1
severity: M
rel_id: REL-CFG-04
status: confirmed
target_phase: 16
verified_at: 2026-06-11
---

# Finding O-1: Pool exhaustion is completely silent at default log level

## Review citation

From `docs/reviews/2026-06-11-reliability-review.md` §6 (Observability):

> **[Medium] O-1: Pool exhaustion is completely silent at default log level — requests hang with zero diagnostic**
>
> **Files:** `internal/pool/pool.go:490-505` (acquire parks with no log at any level; acquire/release markers at pool.go:506, 611, 687 are `debugLog` only).
>
> **Failure scenario:** 4 slots wedged by long generations; the 5th request hangs indefinitely (no acquire timeout — see P-1; the stream-idle watchdog only covers streams after they start). The user's only diagnostic tool — the log — shows the access-log line never completing and nothing else.

## Current-source check

Verified against current source (commit `3a72d03`):

**`internal/pool/pool.go:490-506` (NewSession acquire path):**
```go
func (p *Pool) NewSession(ctx context.Context, cwd string) (string, error) {
    var slot *Slot
    select {
    case slot = <-p.slots:
        // acquired
    case <-ctx.Done():
        return "", fmt.Errorf("pool: acquire cancelled: %w", ctx.Err())
    case <-p.closing:
        return "", errors.New("pool: closed")
    }
    p.debugLog("pool.acquire", "slot", slot.Label)
```

**Analysis:**
- The `select` at lines 492-504 blocks on `<-p.slots` with NO log emitted before blocking
- `p.debugLog("pool.acquire", ...)` at line 506 is emitted only AFTER successful acquisition — it does not fire when the goroutine parks waiting
- `debugLog` is gated on `cfg.Logger != nil` and emits at `Debug` level — it would not be visible at the default `Info` log level even if it ran pre-acquire
- Lines 611 and 687 are `pool.release` markers — also `debugLog` only

**No Warn log path:** A repo-wide grep for `"pool: waiting"`, `Warn.*pool.*slot`, `Warn.*exhausted`, `Warn.*busy` in `internal/pool/pool.go` returns zero results. There is no Warn (or Info) log when a goroutine parks at the slot-acquire point.

**Relation to P-1:** O-1 is the observability companion to P-1 (pool shrinks to zero). Even when P-1 is fixed (acquire has a timeout), operators still need a signal before the timeout fires. O-1 is independently actionable at the log-level even if P-1 is not fixed.

## Evidence

This is a Medium finding per D-02 (code-walk + t.Skip'd regression test).

**Go regression test:** `internal/pool/regression_rel_cfg_04_test.go`
- Function: `TestRegression_REL_CFG_04_PoolExhaustionSilent`
- Located in `internal/pool/` (not `internal/config/`) because the failure path is at the pool-acquire site
- Uses `fakeClient` (defined in pool_test.go, reusable in `package pool_test`) with a blocking `promptFn` gate
- Setup: size-1 pool; goroutine 1 acquires + holds the slot via blocking Prompt; goroutine 2 calls NewSession against the exhausted pool
- Pre-fix observable: slog buffer contains ZERO Warn records mentioning "pool: waiting for free slot" after g2 parks
- Post-fix: pool emits Warn with the message at first park attempt

**Filename ownership note:** This file is `internal/pool/regression_rel_cfg_04_test.go` — the `_cfg_04` suffix matches the REL-CFG-04 requirement ID, NOT a pool-config file collision. Plan 14-01's pool test files are named `regression_rel_pool_01..04_test.go` — distinct filenames, no conflict.

**Code-walk summary:** In `pool.go:490`, the goroutine enters the `select` without emitting any log. If `p.slots` is empty (all slots busy), the goroutine blocks silently for the duration of the acquire wait. This is the pre-fix state. Post-fix (Phase 16): add a `select` with `default` BEFORE the blocking wait to check if an immediate acquire would succeed; if not, emit `p.cfg.Logger.Warn("pool: waiting for free slot", "busy", p.Stats().Busy, "size", p.Stats().Size)` before entering the blocking `select`.

## Verdict

**confirmed** — The cite is intact. `pool.go:490-505` blocks on slot acquisition with no Warn-level log emitted before or during the wait. No observability improvement has been added since the review. Phase 16 fix: add a non-blocking pre-check before the blocking select and emit a Warn when the pool is exhausted (and/or bound the acquire with a timeout per P-1's companion fix).
