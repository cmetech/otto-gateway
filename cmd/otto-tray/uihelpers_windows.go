//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/energye/systray"

	"otto-gateway/cmd/otto-tray/icon"
)

func setIcon(b []byte) { systray.SetIcon(b) }

// setIconForState updates the system-tray icon to reflect the current FSM state.
// Windows does not use template images so all states use SetIcon with .ico assets.
// Icon assets: cmd/otto-tray/icon/{Running,Warning,Error}.ico (embedded).
func setIconForState(state State) {
	switch state {
	case StateRunning:
		systray.SetIcon(icon.Running)
	case StateStarting, StateDegraded:
		systray.SetIcon(icon.Warning)
	default: // StateError, StateStopped, StateUnknown
		systray.SetIcon(icon.Error)
	}
}

func openURL(url string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// cmd /c start "" "<url>" is the canonical Windows shell-open path.
	// It hands the URL to the user's default browser via the registered
	// `start` shell verb. rundll32 url.dll,FileProtocolHandler also
	// works but is undocumented; `cmd /c start` is stable across every
	// supported Windows. The "" first arg is an empty window title
	// (start's quirk: the first quoted arg is the title, not the URL).
	cmd := exec.CommandContext(ctx, "cmd", "/c", "start", "", url) //nolint:gosec // url is operator-configured
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Run()
}

func copyToClipboard(s string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "clip")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		return
	}
	_, _ = stdin.Write([]byte(s))
	_ = stdin.Close()
	_ = cmd.Wait()
}

// notifyFn is the package-level seam tests inject to capture or block notify.
// REL-TRAY-04 (T-4) fix: applyState dispatches through notifyFn in a goroutine
// so a slow / modal MessageBox cannot wedge the uiLoop. The variable is also
// the injection point the REL-TRAY-04 regression test uses to simulate a
// 30-second MessageBox stall without spawning PowerShell.
var notifyFn = notifyImpl

// notify is the public entrypoint kept for backwards compatibility with
// callers outside applyState (handleStart/Stop/Restart error toasts, support
// bundle dialogs). It routes through notifyFn so test injection covers every
// call path; the regression test asserts on the applyState path specifically.
func notify(title, body string) { notifyFn(title, body) }

// notifyImpl is the platform MessageBox implementation. Blocking on purpose —
// MessageBox returns only after the user clicks OK. applyState wraps the call
// in a fire-and-forget goroutine so the modal does not freeze the uiLoop.
func notifyImpl(title, body string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// MessageBox is synchronous and modal — reliable on every
	// supported Windows. The previous NotifyIcon.ShowBalloonTip
	// approach was fire-and-forget via cmd.Start(); the PowerShell
	// process exited before the balloon rendered, so the user saw
	// nothing. MessageBox blocks until the user clicks OK, which is
	// the right semantics for "Failed to start" feedback anyway —
	// the caller already runs notify from a background goroutine
	// (T-4 fix: applyState wraps notifyFn in `go func()`), so blocking
	// here does not freeze the UI.
	//
	// MB_OK (0x00) + MB_ICONINFORMATION (0x40) + MB_SETFOREGROUND
	// (0x10000) keeps it terse and pulls the dialog above whatever
	// the user is currently looking at.
	script := "[reflection.assembly]::loadwithpartialname('System.Windows.Forms') | Out-Null; " +
		"[System.Windows.Forms.MessageBox]::Show('" + escapePS(body) + "', '" + escapePS(title) + "', 'OK', 'Information') | Out-Null"
	cmd := exec.CommandContext(ctx, powershellExe(), "-NoProfile", "-Command", script) //nolint:gosec // script body escaped via escapePS
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Run()
}

// infoDialog shows a blocking single-button informational dialog. Used
// for About and other "user asked for this, show it modally" surfaces.
// Windows already presents notify() via MessageBox (modal), so we share
// the same primitive — distinct from the macOS split where notify is a
// notification-center banner and infoDialog is a real dialog.
func infoDialog(title, body string) { notify(title, body) }

// powershellExe returns "pwsh" if PowerShell 7 is on PATH, else falls
// back to "powershell" (the Windows PowerShell 5.x that ships with
// every supported Windows). Mirrors runner_windows.go's selection so
// the tray works on installs without PowerShell 7.
func powershellExe() string {
	if _, err := exec.LookPath("pwsh"); err == nil {
		return "pwsh"
	}
	return "powershell"
}

// confirmDialog shows a blocking yes/no MessageBox. Returns true if
// the user clicked the affirmative button. Falls back to false on
// any error (we never block silently).
func confirmDialog(title, body, _, _ string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// MessageBox returns 6 for Yes, 7 for No. The PowerShell exit
	// code propagates that integer. Captures the result via stdout
	// so we don't depend on $LASTEXITCODE plumbing.
	script := "[reflection.assembly]::loadwithpartialname('System.Windows.Forms') | Out-Null; " +
		"$result = [System.Windows.Forms.MessageBox]::Show('" + escapePS(body) + "', '" + escapePS(title) + "', 'YesNo', 'Question'); " +
		"Write-Output $result"
	cmd := exec.CommandContext(ctx, powershellExe(), "-NoProfile", "-Command", script) //nolint:gosec // script body escaped via escapePS
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "Yes")
}

func escapePS(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out = append(out, '\'')
		}
		out = append(out, s[i])
	}
	return string(out)
}

func exeForAutostart() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve exe: %w", err)
	}
	return exe, nil
}

func installAutostart(exe string) error { return installRunKey(exe) }
func uninstallAutostart() error         { return uninstallRunKey() }

// revealBundle opens Explorer with the given file selected. Mirrors openURL's
// shape (10s timeout, hidden window, best-effort). `explorer.exe /select,<path>`
// is the documented "open parent folder and highlight this file" verb.
// explorer.exe returns non-zero exit codes on success, so we deliberately
// ignore the cmd.Run error — fire and forget.
func revealBundle(path string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "explorer.exe", "/select,"+path) //nolint:gosec // path originates from the wrapper we just ran
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: false}
	_ = cmd.Run()
}

// bundleExt returns the archive extension that the pwsh wrapper produces.
// Used for the fallback path when the wrapper's stdout is empty.
func bundleExt() string { return ".zip" }
