//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

const (
	createNewProcessGroup = 0x00000200
)

// wrapperCommand returns the executable and args to run the gw
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
func wrapperCommand(installDir, verb string, extraArgs ...string) (string, []string) {
	script := filepath.Join(installDir, "scripts", "gw.ps1")
	shell := "powershell"
	if _, err := exec.LookPath("pwsh"); err == nil {
		shell = "pwsh"
	}
	return shell, append([]string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script, verb}, extraArgs...)
}

// detachProcessGroup puts the wrapper in its own process group so that
// quitting the tray does not propagate SIGINT/Ctrl-Break to the gateway.
// We deliberately do NOT pass DETACHED_PROCESS — that flag strips all
// console handles from the child, and the wrapper script's internal
// Start-Process -NoNewWindow then has no console to inherit, so
// launching gateway.exe from inside the wrapper silently misfires.
// CREATE_NEW_PROCESS_GROUP alone is enough to outlive the tray.
func detachProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup,
		HideWindow:    true,
	}
}

// configureWrapperCancellation terminates the complete wrapper process tree.
// os.Process.Kill only stops powershell.exe itself; taskkill /T also reaps any
// support-compression job that PowerShell started before the tray deadline.
func configureWrapperCancellation(cmd *exec.Cmd) {
	cmd.WaitDelay = 3 * time.Second
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		killer := exec.Command("taskkill.exe", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F") //nolint:gosec // PID is an integer from the child process
		killer.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := killer.Run(); err == nil {
			return nil
		}
		if err := cmd.Process.Kill(); err != nil {
			if errors.Is(err, os.ErrProcessDone) {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
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
