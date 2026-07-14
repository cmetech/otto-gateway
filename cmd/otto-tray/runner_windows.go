//go:build windows

package main

import (
	"os/exec"
	"path/filepath"
	"syscall"
)

const (
	createNewProcessGroup = 0x00000200
)

// wrapperCommand returns the executable and args to run the otto-gw
// PowerShell wrapper on windows.
//
// PowerShell selection: prefer pwsh.exe (PowerShell 7+, which the
// install footer recommends), but fall back to powershell.exe (the
// Windows PowerShell 5.x that ships with every supported Windows).
// Without this fallback, Windows installs without PowerShell 7 hit
// "pwsh: file not found", exec.Command returns an error, and the
// tray's menu actions silently no-op.
//
// -ExecutionPolicy Bypass: the .ps1 wrapper ships unsigned via the
// install tarball. The user's default execution policy
// (often RemoteSigned) would refuse to run it without the override.
// We rely on the wrapper script's own internal authentication
// guards (env-driven AUTH_TOKEN) — the script itself is not the
// trust boundary; the user already trusted the install.
func wrapperCommand(installDir, verb string) (string, []string) {
	script := filepath.Join(installDir, "scripts", "otto-gw.ps1")
	shell := "powershell"
	if _, err := exec.LookPath("pwsh"); err == nil {
		shell = "pwsh"
	}
	return shell, []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script, verb}
}

// detachProcessGroup puts the wrapper in its own process group so that
// quitting the tray does not propagate SIGINT/Ctrl-Break to the gateway.
// We deliberately do NOT pass DETACHED_PROCESS — that flag strips all
// console handles from the child, and the wrapper script's internal
// Start-Process -NoNewWindow then has no console to inherit, so
// launching otto-gateway.exe from inside the wrapper silently misfires.
// CREATE_NEW_PROCESS_GROUP alone is enough to outlive the tray.
func detachProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup,
		HideWindow:    true,
	}
}

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
