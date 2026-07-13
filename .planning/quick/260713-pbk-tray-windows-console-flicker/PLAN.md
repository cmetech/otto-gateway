---
quick_id: 260713-pbk
slug: tray-windows-console-flicker
date: 2026-07-13
description: "Fix Windows console-window flicker from the otto-tray desktop poller (missing HideWindow/CREATE_NO_WINDOW on tasklist/taskkill/powershell spawns)"
type: bugfix
---

# Quick Task — Fix Windows Console-Window Flicker (otto-tray desktop)

## Context

Shipped in v2.1.0 (tray "OTTO Desktop" management). On Windows a user reported
a console/terminal window flashing and disappearing on a ~3s cadence with a busy
cursor whenever the tray runs; quitting the tray stops it.

**Root cause (confirmed via systematic-debugging):** The otto-tray binary is a
GUI systray app with no console. On Windows, when a GUI process spawns a *console*
program (`tasklist`, `taskkill`, `powershell`) via `os/exec` **without**
`SysProcAttr.HideWindow` / `CREATE_NO_WINDOW`, Windows allocates a console window
that flashes and vanishes when the child exits. The desktop poller calls
`platformDesktopRunning` → `exec.Command("tasklist", ...)` **every 3 seconds**
whenever OTTO Desktop is *installed* (running or not) — producing the repeated
flicker. `runCmd` (used by the Stop → `taskkill` and Install → `powershell`
handlers) has the same defect (click-driven, would flash once per action).

The codebase already knows this and sets `HideWindow: true` on **every other**
Windows spawn:
- `runner_windows.go` `detachProcessGroup` → `HideWindow: true`
- `uihelpers_windows.go` (3 sites) → `HideWindow: true`
The pre-existing gateway poller never flickered because it probes via HTTP
(`/health`) + `processAlive(pid)` (OS API) — no console subprocess per tick.

The two new desktop spawns (`platformDesktopRunning`, `runCmd`) simply omit the
convention. This is a Windows-only bug; macOS (`pgrep`/`osascript`/`open`) never
opens a visible window.

## Scope

**In scope (otto-tray only):**
1. Add a per-OS `hideConsole(cmd *exec.Cmd)` seam mirroring the existing
   `detachProcessGroup` per-OS pattern.
2. Apply it to the two offending spawns.
3. Windows-only regression test.

**Out of scope:** any non-tray code; gateway poller (already clean); spawns that
already go through `detachProcessGroup` (`spawnDetached`, `runWrapper` — already
set `HideWindow`).

## Tasks

### Task 1 — Add per-OS `hideConsole` helper

Create `cmd/otto-tray/desktopconsole_windows.go`:
```go
//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// createNoWindow is CREATE_NO_WINDOW: run a console child with no console
// window allocated. Paired with HideWindow so a GUI systray parent never
// flashes a console when it spawns tasklist/taskkill/powershell each poll tick.
const createNoWindow = 0x08000000

// hideConsole suppresses the console window Windows would otherwise allocate
// when this GUI process spawns a console program. No-op off Windows.
func hideConsole(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
```

Create `cmd/otto-tray/desktopconsole_darwin.go`:
```go
//go:build darwin

package main

import "os/exec"

// hideConsole is a no-op on darwin — only a Windows GUI app flashes a console
// when it spawns a console child. Present so darwin||windows callers compile.
func hideConsole(cmd *exec.Cmd) {}
```

Rationale for the seam (not inline): `runCmd` lives in the shared
`//go:build darwin || windows` file `desktopcmd.go`, so it needs a
platform-dispatched helper exactly like `detachProcessGroup`.

### Task 2 — Apply `hideConsole` to the two offending spawns

In `cmd/otto-tray/desktopcmd.go`, function `runCmd`, after `cmd.Dir` is set and
before `cmd.Run()`, add:
```go
	hideConsole(cmd) // Windows: no console-window flash when spawning taskkill/powershell
```

In `cmd/otto-tray/desktop_windows.go`, function `platformDesktopRunning`, rewrite
the one-liner so the command carries the no-window attr (this is the every-3s
flicker loop):
```go
	// #nosec G204 -- id.WinExeName derives from a validateDisplayName-checked
	// display name; the filter value is quoted and bounded.
	cmd := exec.Command("tasklist", "/FI", "IMAGENAME eq "+id.WinExeName, "/NH")
	hideConsole(cmd) // no console-window flash on this per-tick liveness probe
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), id.WinExeName)
```
Keep the existing comments/nosec annotation. Do not change the darwin
`platformDesktopRunning` (pgrep; hideConsole would be a no-op there anyway).

### Task 3 — Windows-only regression test

Create `cmd/otto-tray/desktopconsole_windows_test.go`:
```go
//go:build windows

package main

import (
	"os/exec"
	"testing"
)

// Regression guard for the v2.1.0 console-flicker bug: every Windows spawn from
// this GUI app must suppress the console window, or tasklist (every 3s) flashes
// a terminal. See quick task 260713-pbk.
func TestHideConsole_SuppressesConsoleWindow(t *testing.T) {
	cmd := exec.Command("tasklist")
	hideConsole(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("hideConsole left SysProcAttr nil")
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Error("hideConsole did not set HideWindow=true")
	}
	if cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Error("hideConsole did not set CREATE_NO_WINDOW")
	}
}
```
(Note: CI's `test-race` runs on linux, where the `darwin || windows` tray package
is not built, so this test executes only on a real Windows runner / dev box. It
still guards via `GOOS=windows` compilation and documents the invariant.)

## Verification

Run from repo root; all must pass:
1. `gofmt -l cmd/otto-tray/*.go` → empty (also run `go run mvdan.cc/gofumpt@latest -l cmd/otto-tray/` → empty, since CI enforces gofumpt).
2. `go vet ./cmd/otto-tray/...` (darwin) → clean.
3. `go test ./cmd/otto-tray/...` (darwin) → pass (no regression in existing tray tests).
4. `GOOS=windows go build ./cmd/otto-tray/...` → compiles.
5. `GOOS=windows go vet ./cmd/otto-tray/...` → clean (compiles the windows files incl. the fix).
6. `GOOS=windows go test -c -o /dev/null ./cmd/otto-tray/` → compiles the windows regression test.

## Commit

Atomic commit, message shape:
```
fix(tray): suppress Windows console-window flash in desktop poller

The otto-tray GUI app spawned tasklist (every 3s, desktop liveness probe)
and taskkill/powershell (stop/install) without HideWindow/CREATE_NO_WINDOW,
so Windows allocated a console window that flashed each spawn. Add a per-OS
hideConsole() seam (matching detachProcessGroup) and apply it to
platformDesktopRunning + runCmd. macOS unaffected (no-op). Regresses v2.1.0.
```
Follow the repo's trailer convention (Co-Authored-By + Claude-Session) used by
recent commits.

**Do NOT tag/release** — the operator cuts v2.1.1 separately after review.
