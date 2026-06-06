//go:build windows

package main

import (
	"os/exec"
	"path/filepath"
	"syscall"
)

const (
	createNewProcessGroup = 0x00000200
	detachedProcess       = 0x00000008
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
func wrapperCommand(installRoot, verb string) (string, []string) {
	script := filepath.Join(installRoot, "scripts", "otto-gw.ps1")
	shell := "powershell"
	if _, err := exec.LookPath("pwsh"); err == nil {
		shell = "pwsh"
	}
	return shell, []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script, verb}
}

func detachProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup | detachedProcess,
	}
}
