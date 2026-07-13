---
quick_id: 260713-pbk
slug: tray-windows-console-flicker
date: 2026-07-13
status: complete
commit: 33185b2
type: bugfix
---

# Summary — Fix Windows Console-Window Flicker (otto-tray desktop)

## Outcome

Fixed a Windows-only regression shipped in **v2.1.0**: the otto-tray systray
(a GUI app with no console) spawned the console program `tasklist` **every 3
seconds** — via the desktop liveness poller — without
`HideWindow`/`CREATE_NO_WINDOW`, so Windows allocated a console window that
flashed and vanished each tick. `taskkill` (Stop) and `powershell` (Install)
had the same defect on click. macOS was never affected (`pgrep`/`osascript`/
`open` open no window).

Root cause was confirmed via systematic-debugging: the pre-existing gateway
poller never flickered because it probes over HTTP + `processAlive(pid)` (no
subprocess), and the codebase already sets `HideWindow: true` on every *other*
Windows spawn (`detachProcessGroup`, `uihelpers_windows.go`). The two new
desktop spawns simply omitted the convention.

## Change

Added a per-OS `hideConsole(cmd *exec.Cmd)` seam (mirrors `detachProcessGroup`):
Windows sets `SysProcAttr.HideWindow = true` and ORs `CREATE_NO_WINDOW`
(0x08000000); darwin is a no-op. Applied it to the two offending spawns.

Files (5 changed, +61/-1):
- `cmd/otto-tray/desktopconsole_windows.go` — NEW: `createNoWindow` + `hideConsole`
- `cmd/otto-tray/desktopconsole_darwin.go` — NEW: no-op `hideConsole`
- `cmd/otto-tray/desktopconsole_windows_test.go` — NEW: regression guard
- `cmd/otto-tray/desktop_windows.go` — `platformDesktopRunning` now sets the attr on `tasklist`
- `cmd/otto-tray/desktopcmd.go` — `runCmd` calls `hideConsole` before `Run()`

## Verification (all pass)

| Gate | Result |
|------|--------|
| `gofmt -l cmd/otto-tray/*.go` | clean |
| `gofumpt -l cmd/otto-tray/` (CI gate) | clean |
| `go vet ./cmd/otto-tray/...` (darwin) | clean |
| `go test ./cmd/otto-tray/...` (darwin) | pass |
| `GOOS=windows go build ./cmd/otto-tray/...` | compiles |
| `GOOS=windows go vet ./cmd/otto-tray/...` | clean |
| `GOOS=windows go test -c ./cmd/otto-tray/` | compiles (incl. regression test) |

Note: CI's `test-race` runs on linux, where the `darwin || windows` tray package
is not built, so the regression test executes only on a real Windows runner/dev
box; it still guards via `GOOS=windows` compilation.

**Runtime confirmation pending:** operator to re-test on Windows after v2.1.1
(install OTTO Desktop present but stopped → no console flicker while tray runs).

## Follow-up

- Commit `33185b2` on `main`, not pushed at task close (operator pushes + cuts
  **v2.1.1** separately after review).
