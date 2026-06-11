---
finding: T-3
severity: H
rel_id: REL-TRAY-03
status: confirmed
target_phase: 15
verified_at: 2026-06-11
---

# T-3: Gateway Death Effectively Invisible on macOS (REL-TRAY-03)

## Review Citation

> **[High] T-3: Gateway death is effectively invisible on macOS — the only proactive signal is a notification the codebase itself documents as silently no-op'ing, and the icon/tooltip never change**
>
> Files: `cmd/otto-tray/tray.go:199-201` (death notify), `cmd/otto-tray/tray.go:74-75` (icon/tooltip set once), `cmd/otto-tray/uihelpers_darwin.go:43-58`.
>
> Failure scenario: On FSM transition Running→Stopped/Error, the only signal is `notify(...)`, which on macOS is `osascript display notification`. The comment at `uihelpers_darwin.go:51-58` states that notification banners silently no-op for LSUIElement agents without notification permission. Meanwhile `setIcon`/`SetTooltip` are called exactly once in `onReady`; `applyState` only edits menu-item titles, invisible until the menu is opened.

## Current-Source Check

Verified against current source (worktree at commit `3a72d03`):

- **`cmd/otto-tray/tray.go:199-201` (applyState notify):** Lines 199–201 show:
  ```go
  if prev == StateRunning && (out.State == StateError || out.State == StateStopped) {
      notify("OTTO Gateway", fmt.Sprintf("Gateway is %s", out.State))
  }
  ```
  This is the sole proactive death signal. **Failure path intact.**

- **`cmd/otto-tray/tray.go:74-75` (icon/tooltip set once):** Lines 74–75 in `onReady` show:
  ```go
  setIcon(icon.Template)
  systray.SetTooltip("OTTO Gateway")
  ```
  These are called once. `applyState` (lines 175–202) calls `s.miHeader.SetTitle(...)` and `s.miSubheader.SetTitle(...)` but never calls `setIcon` or `systray.SetTooltip`. A user watching the menu bar has no visible indicator of gateway state unless they open the menu. **Failure path intact.**

- **`cmd/otto-tray/uihelpers_darwin.go:43-58` (notify no-op comment):** Lines 51–58 contain the developer comment acknowledging that `display notification` silently no-ops for LSUIElement agents: "notification banners silently no-op for LSUIElement agents that haven't been granted notification permission, which is the v2.0.8 'About does nothing' symptom". No alternative notification channel has been added. **Failure path intact.**

`applyState` has not been updated to call `setIcon` with a degraded/stopped icon or to update `systray.SetTooltip` dynamically. The fix path (swap icon + tooltip on state change, or switch to `display dialog`/user notifications with permission) is not present.

## Evidence

Manual reproducer: `tests/reliability/manual/REL-TRAY-03-repro.sh`

The bash script verifies the tray is running, reads the PID file, kills the gateway with `kill -9`, then prompts the operator to watch the menu bar icon for 30 seconds across 10 poll cycles. The operator records whether a visible signal appeared (POST-FIX) or the icon remained unchanged (PRE-FIX confirmed).

Discoverability stub: `cmd/otto-tray/regression_rel_tray_03_test.go` — `TestRegression_REL_TRAY_03_MacosSilentGatewayDeath` skips with pointer to the manual script.

## Verdict

**confirmed** — The two failure paths are both intact: (1) `notify()` on macOS is documented as silently no-op'ing for LSUIElement agents without notification permission; (2) `setIcon`/`SetTooltip` are never called dynamically in `applyState`, so the menu bar icon looks identical whether the gateway is running or dead. This is the primary user-visible reliability failure for the macOS tray: the entire point of the tray is to surface gateway health at a glance, but a gateway death produces zero visible signal. Phase 15 scope: dynamic icon swap and/or tooltip update in `applyState` on state change, and a persistent-alternative notification path.
