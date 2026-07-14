# Tray "Install Co-Worker" progress feedback — design

**Date:** 2026-07-14
**Status:** Approved (design)
**Scope:** One implementation plan.

## Problem

The tray offers an **Install Co-Worker…** menu item when the desktop app is
not installed. Clicking it (`cmd/otto-tray/desktoptray.go` →
`handleDesktopInstall()`) shows a confirm dialog, then runs the official
installer synchronously via `runCmd(...)`:

- On Windows that is `powershell … irm …/cmetech/otto/main/install.ps1 | iex`.
  The console for that CLI child is **hidden by design** (the tray's
  established convention: hide the console for CLI children, never for a GUI
  app), so the download/bootstrap is completely silent.
- The only OS-level signal (`notify`) fires **after** the installer finishes
  ("Co-Worker installed." / "Install failed: …").
- The tray menu header switches to "· installing…" via the `desktopInstalling`
  flag, but that text lives **inside the dropdown**, which is closed the moment
  the user clicks Install — so it is effectively invisible.

Result: between the confirm click and the eventual OTTO setup GUI appearing
(often ~a minute later), the user sees nothing and has no idea whether anything
is happening.

## Non-goals

- No change to **what** gets installed or the installer one-liner.
- No change to the console-hiding convention (the powershell child stays
  hidden — see [[project-tray-windows-hideconsole]]).
- No change to the poll loop / desktop FSM / respawn behavior.
- No tray **tooltip** change: the tooltip is owned by the gateway FSM loop
  (`applyState` calls `systray.SetTooltip` on every state tick), so an
  install-state tooltip would be clobbered on the next tick.
- No mid-download heartbeat toast (YAGNI — the start toast sets the "this takes
  a minute" expectation).

## Behavior

Add **start + finish** signals around the existing install run:

1. The moment the user confirms — **before** the blocking `runCmd` — fire an
   immediate toast:
   - title: `Co-Worker` (i.e. `desktopLabel("")`)
   - body: `Downloading the installer — this can take a minute. You'll be
     notified when it's done.`
2. Keep the existing terminal toast unchanged:
   - success → `Co-Worker installed.`
   - failure → `Install failed: <first line of stderr>`

The `desktopInstalling` flag and the menu-header "· installing…" text are
retained as-is (they still gate the desktop probe and the in-menu state).

## Structure (testability)

`handleDesktopInstall()` currently calls `confirmDialog` and `runCmd`
directly, so it cannot be unit-tested without a real dialog and a real
10-minute installer download. `notify` is already injectable via the
package-level `notifyFn` seam; `confirmDialog` and `runCmd` are not.

Extract the orchestration into a seam-injected helper:

```
type desktopInstallDeps struct {
    confirm func() bool
    run     func() runResult
    notify  func(title, body string)
    label   string          // desktopLabel("")
}

func runDesktopInstall(d desktopInstallDeps) {
    if !d.confirm() {
        return
    }
    d.notify(d.label, "Downloading the installer — this can take a minute. You'll be notified when it's done.")
    res := d.run()
    if res.ExitCode != 0 || res.Err != nil {
        d.notify(d.label, "Install failed: "+firstLine(res.Stderr))
        return
    }
    d.notify(d.label, "Co-Worker installed.")
}
```

`handleDesktopInstall()` becomes a thin wrapper that wires the real seams and
keeps the tray-state concern (the `desktopInstalling` flag) it owns:

```
func (s *trayState) handleDesktopInstall() {
    s.desktopInstalling.Store(true)
    defer s.desktopInstalling.Store(false)
    name, args := desktopInstallCommand(runtime.GOOS)
    runDesktopInstall(desktopInstallDeps{
        confirm: func() bool {
            return confirmDialog("Install "+desktopLabel("")+"…",
                "Download and run the official Co-Worker installer now?", "Install", "Cancel")
        },
        run:    func() runResult { return runCmd(10*time.Minute, "", name, args...) },
        notify: notify,
        label:  desktopLabel(""),
    })
}
```

Note: the `desktopInstalling` flag is stored around the whole call, including
the confirm dialog. This differs slightly from today's code, which sets the
flag only *after* confirm. Covering the dialog window too is benign — the tray
dropdown is closed while the modal dialog is up, so nobody sees the transient
"· installing…" header during the dialog; a cancelled dialog flips the flag
back off within the same call and the next poll re-detects `not-installed`.
Keeping the flag in the wrapper (not the extracted helper) means it stays out
of the unit-tested ordering logic.

## Tests (TDD)

Whitebox `package main` test (`desktoptray_test.go` or a focused new file),
driving `runDesktopInstall` with fake seams that record an ordered call log:

1. **Confirm cancelled → nothing runs.** `confirm` returns false; assert
   `run` is never called and `notify` is never called.
2. **Confirm + success → start toast precedes run, then installed toast.**
   `confirm` true, `run` returns `runResult{ExitCode: 0}`; assert the call
   order is exactly: start-notify (`Downloading…`) → run → finish-notify
   (`Co-Worker installed.`). The start-notify MUST be recorded before `run`.
3. **Confirm + failure → start toast then failure toast.** `run` returns
   `runResult{ExitCode: 1, Stderr: "boom\n…"}`; assert order: start-notify →
   run → finish-notify containing `Install failed: boom`.

The seams make all three deterministic with no real dialog, no real download.

## Files touched (anticipated)

- `cmd/otto-tray/desktoptray.go` — extract `runDesktopInstall` +
  `desktopInstallDeps`; add the start toast; thin `handleDesktopInstall`.
- `cmd/otto-tray/desktoptray_test.go` — the three ordering tests.

## Verification

- `GOOS=windows go build ./cmd/otto-tray/...` and
  `GOOS=darwin go build ./cmd/otto-tray/...` green.
- `go test ./cmd/otto-tray/...` green (CI does not build the tray package, so
  the darwin dev box is the gate — build both tags locally).
- gofumpt-clean; `go vet` clean for the tray build tags.
- Manual (Windows, feasible for the operator): click **Install Co-Worker…**,
  confirm, and observe an immediate "Downloading the installer…" toast, then
  the "Co-Worker installed." toast when the OTTO setup completes.
