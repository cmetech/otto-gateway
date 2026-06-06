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
// PowerShell wrapper on windows. pwsh is PowerShell 7+; the .ps1
// wrapper supports both pwsh and powershell.exe — if pwsh is not on
// PATH the OS surfaces the error cleanly through Stderr.
func wrapperCommand(installRoot, verb string) (string, []string) {
	script := filepath.Join(installRoot, "scripts", "otto-gw.ps1")
	return "pwsh", []string{"-NoProfile", "-File", script, verb}
}

//nolint:unused // wired in by Task 12 tray UI
func detachProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup | detachedProcess,
	}
}
