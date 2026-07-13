---
quick_id: 260713-qw7
slug: tray-desktop-start-hidden-window
date: 2026-07-13
status: complete
commit: 797d4f1
type: bugfix
---

# Summary — Start OTTO Desktop launched with no visible window (Windows)

## Outcome

Fixed a Windows-only regression (present since v2.1.0): "Start OTTO Desktop"
launched the Electron app but with **no visible window**. Confirmed on the user's
machine — Task Manager showed 5 live `OTTO.exe` processes (normal Electron
multi-process layout) and the tray correctly read "running", but the window was
hidden.

Root cause: `spawnDetached` (the sole GUI-app launcher) launched via the shared
`detachProcessGroup`, which sets `SysProcAttr.HideWindow = true` (`SW_HIDE`).
Electron honors that and starts hidden. `HideWindow` is correct for the headless
gateway wrapper (avoids a console flash) but wrong for a user-facing GUI app.

## Change

Added `detachGUIProcess(cmd)` (per-OS): Windows detaches into a new process group
**without** `HideWindow` (and without `CREATE_NO_WINDOW`, which is only for console
children); darwin mirrors `detachProcessGroup` (Setpgid — the app is launched via
`open`, no window concern). Pointed `spawnDetached` at it. `detachProcessGroup`
(gateway wrapper) is unchanged and keeps `HideWindow`.

Files (4 changed, +44/-1):
- `cmd/otto-tray/runner_windows.go` — NEW `detachGUIProcess` (no HideWindow)
- `cmd/otto-tray/runner_darwin.go` — NEW `detachGUIProcess` (Setpgid)
- `cmd/otto-tray/desktopcmd.go` — `spawnDetached` now calls `detachGUIProcess`
- `cmd/otto-tray/detachgui_windows_test.go` — NEW regression guard (asserts no HideWindow)

## Verification (all pass)

gofmt / gofumpt clean · `go vet` (darwin) clean · `go test ./cmd/otto-tray/...`
(darwin) pass · `GOOS=windows` build + vet + test-compile clean.

Note: CI test-race runs on linux where the `darwin || windows` tray package isn't
built, so the regression test runs only on a real Windows runner/dev box.

**Runtime confirmation pending:** operator to re-test on Windows after v2.1.2 —
Start OTTO Desktop should now open a visible window.

## Follow-up

- Commit `797d4f1` on `main`; operator cuts **v2.1.2** separately after review.
- Relation: sibling of [260713-pbk] (console-flicker) — same "Windows spawn
  attributes" area of the desktop feature.
