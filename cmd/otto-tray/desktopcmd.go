//go:build darwin || windows

package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	pathpkg "path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// desktopInstallCommand returns the CONSTANT OTTO desktop installer one-liner
// per OS (assume-otto, v1). No brand-derived interpolation → no injectable
// input reaches exec (gosec G204).
func desktopInstallCommand(goos string) (string, []string) {
	if goos == "windows" {
		return "powershell", []string{
			"-NoProfile", "-Command",
			"irm https://raw.githubusercontent.com/cmetech/otto/main/install.ps1 | iex",
		}
	}
	return "/bin/sh", []string{
		"-c",
		"curl -fsSL https://raw.githubusercontent.com/cmetech/otto/main/install.sh | sh",
	}
}

// desktopStartCommand launches the installed app. appPath comes from the
// allowlisted well-known locations (installedAppPath), not user input.
func desktopStartCommand(goos, appPath string) (string, []string) {
	if goos == "darwin" {
		return "open", []string{appPath}
	}
	return appPath, nil // windows: run the exe directly
}

type desktopProcessEntry struct {
	PID       uint32
	ImageName string
}

func normalizeWindowsExecutablePath(executablePath string) string {
	executablePath = strings.TrimSpace(executablePath)
	executablePath = strings.ReplaceAll(executablePath, `\`, "/")
	lowerPath := strings.ToLower(executablePath)
	switch {
	case strings.HasPrefix(lowerPath, `//?/unc/`):
		executablePath = "//" + executablePath[len(`//?/unc/`):]
	case strings.HasPrefix(lowerPath, `//?/`):
		executablePath = executablePath[len(`//?/`):]
	}
	return strings.ToLower(pathpkg.Clean(executablePath))
}

func windowsCandidateProcessIDs(
	candidate desktopCandidate,
	entries []desktopProcessEntry,
	queryPath func(uint32) (string, error),
	processGone func(error) bool,
) ([]uint32, error) {
	wantPath := normalizeWindowsExecutablePath(candidate.ExecutablePath)
	var pids []uint32
	for _, entry := range entries {
		if entry.PID == 0 || !strings.EqualFold(entry.ImageName, candidate.Identity.WinExeName) {
			continue
		}
		executablePath, err := queryPath(entry.PID)
		if err != nil {
			if processGone != nil && processGone(err) {
				continue
			}
			return nil, fmt.Errorf("query candidate process %d path: %w", entry.PID, err)
		}
		if wantPath != "" && wantPath != "." && normalizeWindowsExecutablePath(executablePath) == wantPath {
			pids = append(pids, entry.PID)
		}
	}
	return pids, nil
}

func macExecutablePattern(executablePath string) string {
	return "^" + regexp.QuoteMeta(executablePath) + "([[:space:]]|$)"
}

// desktopStopCommand builds a graceful (force=false) or forced (force=true)
// stop for the selected candidate. Windows PIDs must come from the exact-path
// process lookup; macOS targets the candidate's exact bundle/executable paths.
func desktopStopCommand(goos string, candidate desktopCandidate, pids []uint32, force bool) (string, []string) {
	if goos == "windows" {
		if len(pids) == 0 {
			return "", nil
		}
		args := make([]string, 0, len(pids)*2+2)
		for _, pid := range pids {
			args = append(args, "/PID", strconv.FormatUint(uint64(pid), 10))
		}
		args = append(args, "/T")
		if force {
			args = append(args, "/F")
		}
		return "taskkill", args
	}
	if force {
		return "pkill", []string{"-f", macExecutablePattern(candidate.ExecutablePath)}
	}
	return "osascript", []string{"-e", `tell application "` + escapeAppleScriptArg(candidate.AppPath) + `" to quit`}
}

// escapeAppleScriptArg escapes a string for embedding inside an AppleScript
// double-quoted literal. Defense-in-depth: display names are already bounded by
// validateDisplayName (no " or \), so this is a backstop if that ever loosens.
func escapeAppleScriptArg(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
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
	hideConsole(cmd) // Windows: no console-window flash when spawning taskkill/powershell
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
	cmd := exec.CommandContext(context.Background(), name, args...) //nolint:gosec // name is an allowlisted app path / "open"; args validated
	if dir != "" {
		cmd.Dir = dir
	}
	detachGUIProcess(cmd) // GUI app: detach without HideWindow so its window is visible (win)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn %s: %w", name, err)
	}
	return nil
}
