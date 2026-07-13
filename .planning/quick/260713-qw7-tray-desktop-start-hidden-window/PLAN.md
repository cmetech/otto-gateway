---
quick_id: 260713-qw7
slug: tray-desktop-start-hidden-window
date: 2026-07-13
description: "Start OTTO Desktop launches the Electron app with HideWindow=true, so it runs invisibly (Windows)"
type: bugfix
---

# Quick Task — Start OTTO Desktop launches app with no visible window (Windows)

## Context

User clicked "Start OTTO Desktop" (Windows, v2.1.1). Tray flipped to "running"
and Task Manager showed **5 `OTTO.exe` processes** (normal Electron multi-process
layout), but **no window appeared**.

**Root cause (confirmed on the user's machine):** the app process launches fine —
the tray's `tasklist` detection is correct. But `spawnDetached` (the GUI-app
launcher, `desktopcmd.go`) launches it via the shared `detachProcessGroup`
helper, which on Windows sets `SysProcAttr.HideWindow = true`:

```go
// runner_windows.go — detachProcessGroup (shared with the gateway wrapper)
cmd.SysProcAttr = &syscall.SysProcAttr{
    CreationFlags: createNewProcessGroup,
    HideWindow:    true,   // correct for headless gateway wrapper, WRONG for a GUI app
}
```

`HideWindow` sets `STARTF_USESHOWWINDOW` + `SW_HIDE`; Electron honors it and
starts with the main window hidden. Correct for the background gateway wrapper
(no console flash); wrong for a user-facing GUI app.

`spawnDetached` is used in exactly one place — `desktoptray.go:handleDesktopStart`
— so it *only* launches the desktop GUI app. `detachProcessGroup` is also used by
`runWrapper` (gateway), which must keep `HideWindow: true`. So the fix must be
scoped to the GUI launcher, not the shared helper.

macOS is unaffected: `spawnDetached` runs `open <appPath>`, which launches the
app via LaunchServices; `HideWindow` has no meaning there.

## Fix

Add a per-OS `detachGUIProcess(cmd)` helper that detaches into a new process
group (outlives the tray) **without** `HideWindow`, and point `spawnDetached`
at it instead of `detachProcessGroup`.

### Task 1 — `runner_windows.go`: add `detachGUIProcess`
```go
// detachGUIProcess detaches a *GUI* child (the desktop app) into its own process
// group so it outlives the tray, WITHOUT HideWindow. HideWindow sets SW_HIDE,
// which Electron/GUI apps honor by starting with no visible window — correct for
// the headless gateway wrapper (detachProcessGroup) but wrong for a user-facing
// app. No CREATE_NO_WINDOW either: that is for console children; a GUI exe needs
// normal window creation. See quick task 260713-qw7.
func detachGUIProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup,
	}
}
```

### Task 2 — `runner_darwin.go`: add `detachGUIProcess`
```go
// detachGUIProcess mirrors detachProcessGroup on darwin (Setpgid) — the desktop
// app is launched via `open`, so there is no window-hiding concern here; this
// exists so the shared darwin||windows spawnDetached compiles on both OSes.
func detachGUIProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
```

### Task 3 — `desktopcmd.go`: point `spawnDetached` at the GUI helper
In `spawnDetached`, replace:
```go
	detachProcessGroup(cmd) // existing helper (darwin: Setpgid; windows: DETACHED_PROCESS)
```
with:
```go
	detachGUIProcess(cmd) // GUI app: detach without HideWindow so its window is visible (win)
```

### Task 4 — windows-only regression test `detachgui_windows_test.go`
```go
//go:build windows

package main

import (
	"os/exec"
	"testing"
)

// Regression guard: the desktop GUI app must be launched WITHOUT HideWindow, or
// Electron starts hidden (5 live OTTO.exe procs, no window). See quick 260713-qw7.
func TestDetachGUIProcess_DoesNotHideWindow(t *testing.T) {
	cmd := exec.Command("OTTO.exe")
	detachGUIProcess(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("detachGUIProcess left SysProcAttr nil")
	}
	if cmd.SysProcAttr.HideWindow {
		t.Error("GUI launch must NOT set HideWindow (Electron would start hidden)")
	}
	if cmd.SysProcAttr.CreationFlags&createNewProcessGroup == 0 {
		t.Error("expected CREATE_NEW_PROCESS_GROUP so the app outlives the tray")
	}
}
```

## Verification (all must pass)
1. `gofmt -l cmd/otto-tray/*.go` → empty
2. `go run mvdan.cc/gofumpt@latest -l cmd/otto-tray/` → empty
3. `go vet ./cmd/otto-tray/...` (darwin) → clean
4. `go test ./cmd/otto-tray/...` (darwin) → pass
5. `GOOS=windows go build ./cmd/otto-tray/...` → compiles
6. `GOOS=windows go vet ./cmd/otto-tray/...` → clean
7. `GOOS=windows go test -c -o /dev/null ./cmd/otto-tray/` → compiles (incl. new test)

Note: CI's test-race runs on linux where the `darwin || windows` tray package is
not built, so the regression test runs only on a real Windows runner/dev box.

## Commit
One atomic commit:
```
fix(tray): launch desktop app with a visible window on Windows

Start OTTO Desktop launched the Electron app via spawnDetached ->
detachProcessGroup, which sets HideWindow (SW_HIDE) — correct for the
headless gateway wrapper but wrong for a GUI app, so OTTO.exe ran
(5 procs, "running") with no window. Add detachGUIProcess (new process
group, NO HideWindow) and use it for the GUI launch; gateway wrapper
keeps HideWindow. macOS unaffected (open). Regresses v2.1.0.
```
Trailers: Co-Authored-By + Claude-Session (repo convention).

**Runtime confirmation** is on Windows after v2.1.2. Do NOT tag/release here.
