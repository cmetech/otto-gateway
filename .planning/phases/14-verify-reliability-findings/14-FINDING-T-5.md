---
finding: T-5
severity: M
rel_id: REL-TRAY-05
status: confirmed
target_phase: 16
verified_at: 2026-06-11
---

# T-5: Tray Shows "Running" While Pool Is Wedged (REL-TRAY-05)

## Review Citation

> **[Medium] T-5: Tray can show "running" while the pool is wedged — snapshot errors are swallowed, `/health` hardcodes "ok", and the purpose-built `/health/pool` probe is never used**
>
> Files: `cmd/otto-tray/tray.go:153` (`snap, _ := client.snapshot()`), `cmd/otto-tray/fsm.go:52-54`, `internal/server/health.go:70-77` (Status always "ok"), `internal/server/server.go:248-251` (`/health/pool` exists, unused by tray).
>
> Failure scenario: Two gaps. (1) `makeProbe` ignores the snapshot error: on any snapshot failure `snap` is zero ⇒ `PoolSize == 0` ⇒ the `PoolAlive == 0` degraded check is skipped ⇒ StateRunning. (2) Degraded only fires on `Alive == 0`; workers alive-but-hung (`Busy == Size` forever after sleep/wake) show "running" while every chat request times out.

## Current-Source Check

Verified against current source (worktree at commit `3a72d03`):

- **`cmd/otto-tray/tray.go:153` (snapshot error swallowed):** Line 153 shows `snap, _ := client.snapshot()` — the error return is discarded. When the snapshot endpoint fails (transient network, gateway overloaded), `snap` is zero-value: `PoolSize=0`, `PoolAlive=0`. At `fsm.go:52`, the degraded check requires `PoolSize > 0 && PoolAlive == 0` — with `PoolSize=0` the check is skipped and `StateRunning` is returned. **Failure path 1 intact.**

- **`cmd/otto-tray/fsm.go:52-54`:** Lines 52–54 show:
  ```go
  if in.Snapshot.PoolSize > 0 && in.Snapshot.PoolAlive == 0 {
      return stateOutput{State: StateDegraded, Detail: "pool empty"}
  }
  ```
  This guard correctly detects zero-alive pools but has NO check for `Busy == Size` (workers wedged/hung). A pool with `Alive=4, Busy=4, Size=4` (all slots busy, all hung) returns `StateRunning`. **Failure path 2 intact.**

- **`internal/server/health.go:70-77`:** The `/health` endpoint always returns `status: "ok"` regardless of pool state. The tray's `healthOK()` call uses this endpoint and thus cannot detect a wedged pool. **Failure path confirmed.**

- `/health/pool` endpoint exists at `internal/server/server.go:248-251` but `makeProbe` in `cmd/otto-tray/tray.go` never calls it. The snapshot endpoint (`/admin/api/snapshot`) is used instead, with the error silently discarded.

## Evidence

Regression test: `cmd/otto-tray/regression_rel_tray_05_test.go` — `TestRegression_REL_TRAY_05_DegradedWhenPoolWedged`

The test (modeled directly after `poller_test.go:TestPoller_EmitsStateOnEachTick`) injects a `fakeProbe` with `PoolAlive=0, PoolSize=4, PoolBusy=4` and asserts the state output. The test is skipped with `t.Skip("REL-TRAY-05 (T-5): regression test — unskip in Phase 16 fix commit")`. It documents both failure paths: the snapshot-error swallow path (zero-value snap → PoolSize=0 → degraded check skipped) and the Busy==Size gap (no busy-wedged sentinel in computeState).

## Verdict

**confirmed** — Two independent failure paths both still exist: (1) `tray.go:153` silently discards snapshot errors, yielding `PoolSize=0` and skipping the degraded check; (2) `fsm.go:52` has no guard for the `Busy==Size` (all-slots-wedged) scenario. Phase 16 scope: (1) treat snapshot errors as degraded, not running; (2) add `PoolBusy == PoolSize && PoolSize > 0` → `StateDegraded` in `computeState`.
