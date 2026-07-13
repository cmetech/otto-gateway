//go:build darwin || windows

package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// desktopInstallCommand returns the CONSTANT OTTO desktop installer one-liner
// per OS (assume-otto, v1). No brand-derived interpolation → no injectable
// input reaches exec (gosec G204).
func desktopInstallCommand(goos string) (string, []string) {
	if goos == "windows" {
		return "powershell", []string{"-NoProfile", "-Command",
			"irm https://raw.githubusercontent.com/cmetech/otto/main/install.ps1 | iex"}
	}
	return "/bin/sh", []string{"-c",
		"curl -fsSL https://raw.githubusercontent.com/cmetech/otto/main/install.sh | sh"}
}

// desktopStartCommand launches the installed app. appPath comes from the
// allowlisted well-known locations (installedAppPath), not user input.
func desktopStartCommand(goos, appPath string) (string, []string) {
	if goos == "darwin" {
		return "open", []string{appPath}
	}
	return appPath, nil // windows: run the exe directly
}

// desktopStopCommand builds a graceful (force=false) or forced (force=true)
// stop. id fields derive from a validateDisplayName-checked name.
func desktopStopCommand(goos string, id brandIdentity, force bool) (string, []string) {
	if goos == "windows" {
		args := []string{"/IM", id.WinExeName, "/T"}
		if force {
			args = append(args, "/F")
		}
		return "taskkill", args
	}
	if force {
		return "pkill", []string{"-f", id.MacProcMatch}
	}
	return "osascript", []string{"-e", `quit app "` + id.DisplayName + `"`}
}

// runCmd runs a command to completion with a timeout and captures output.
// Mirrors runWrapper for arbitrary (constant-shape) desktop commands.
func runCmd(timeout time.Duration, dir, name string, args ...string) runResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // name+args are constants or validated brand tokens (see brand.go / callers)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	exit := 0
	if cmd.ProcessState != nil {
		exit = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		err = fmt.Errorf("run %s: %w", name, err)
	}
	return runResult{ExitCode: exit, Stdout: stdout.String(), Stderr: stderr.String(), Err: err}
}

// spawnDetached starts a process in its own group and returns immediately
// (used to launch the desktop app so quitting the tray never signals it).
func spawnDetached(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...) //nolint:gosec // name is an allowlisted app path / "open"; args validated
	if dir != "" {
		cmd.Dir = dir
	}
	detachProcessGroup(cmd) // existing helper (darwin: Setpgid; windows: DETACHED_PROCESS)
	return cmd.Start()
}
