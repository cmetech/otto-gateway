---
finding: T-4
severity: M
rel_id: REL-TRAY-04
status: confirmed
target_phase: 16
verified_at: 2026-06-11
---

# T-4: Windows notify() Blocks the uiLoop Synchronously (REL-TRAY-04)

## Review Citation

> **[Medium] T-4: Windows `notify()` is a blocking modal MessageBox invoked synchronously on the uiLoop — status pipeline stalls up to 30s and a modal pops on every stop**
>
> Files: `cmd/otto-tray/tray.go:199-201` (applyState → notify, on the uiLoop goroutine), `cmd/otto-tray/uihelpers_windows.go:50-68`.
>
> Failure scenario: `uihelpers_windows.go:57-59` asserts the caller runs notify from a background goroutine — false for the `applyState` call site: `uiLoop` (tray.go:169-173) calls `applyState` → `notify` synchronously. The MessageBox blocks until dismissed (or the 30s ctx kills PowerShell). While blocked, `stateCh` (cap 4) fills and the poller blocks on send (`poller.go:56-59`): polling and menu updates freeze.

## Current-Source Check

Verified against current source (worktree at commit `3a72d03`):

- **`cmd/otto-tray/tray.go:169-173` (uiLoop):** Lines 169–173 show:
  ```go
  func (s *trayState) uiLoop() {
      for out := range s.stateCh {
          s.applyState(out)
      }
  }
  ```
  `applyState` is called directly — no goroutine wrapper. **Failure path intact.**

- **`cmd/otto-tray/tray.go:199-201` (applyState notify call):** Lines 199–201 show:
  ```go
  if prev == StateRunning && (out.State == StateError || out.State == StateStopped) {
      notify("OTTO Gateway", fmt.Sprintf("Gateway is %s", out.State))
  }
  ```
  `notify(...)` is called synchronously inside `applyState`, which runs on the `uiLoop` goroutine. **Failure path intact.**

- **`cmd/otto-tray/uihelpers_windows.go:50-68` (notify MessageBox):** Lines 49–69 show `notify()` on Windows spawns a PowerShell `MessageBox::Show(...)` via `exec.CommandContext` with a 30-second timeout (`ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)`). `cmd.Run()` blocks until dismissed or timeout. **Failure path intact.**

- **`cmd/otto-tray/poller.go:56-59`:** The poller's send to `stateCh` is:
  ```go
  select {
  case out <- s:
  case <-ctx.Done():
      return
  }
  ```
  With `stateCh` capacity 4 (tray.go:68), a 30-second `uiLoop` stall fills the buffer in 4 ticks (~12s at 3s/tick), then blocks the poller. **Failure path confirmed.**

No goroutine dispatch has been added to the notify call site in applyState since the review.

## Evidence

Regression test: `cmd/otto-tray/regression_rel_tray_04_test.go` — `TestRegression_REL_TRAY_04_WindowsNotifyBlocking`

The test is skipped with `t.Skip("REL-TRAY-04 (T-4): regression test — unskip in Phase 16 fix commit")`. The body documents the required injection point (a `notifyFn` package-level var for test injection) and the elapsed-time assertion that Phase 16 will activate: `applyState` must return in < 100ms even when the injected notify blocks indefinitely.

## Verdict

**confirmed** — `applyState` calls `notify()` synchronously on the `uiLoop` goroutine. On Windows, `notify()` blocks via a synchronous MessageBox for up to 30 seconds. No goroutine dispatch wraps the call site. During the block, the poller pipeline freezes after 4 unfilled ticks (~12s). Phase 16 scope: wrap the `notify(...)` call in `go notify(...)` at `applyState` line 200.
