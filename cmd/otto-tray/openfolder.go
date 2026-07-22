//go:build darwin || windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

var (
	errDesktopNotRunning   = errors.New("Co-Worker is no longer running")
	errDesktopRevalidation = errors.New("could not verify Co-Worker process")
)

type desktopFolderKind int

const (
	desktopAppFolder desktopFolderKind = iota
	desktopDataFolder
)

// resolveHermesHome mirrors hermes-agent's resolution (main.ts / hermes_constants.py):
// env HERMES_HOME → (windows) HKCU\Environment\HERMES_HOME → default per-OS/brand.
// winReg returns "" off windows / when unset. Pure (all inputs injected) for tests.
func resolveHermesHome(
	goos string,
	env func(string) string,
	home, slug, brandHomeDir string,
	winReg func(string) string,
	exists func(string) bool,
) string {
	localAppData := strings.TrimSpace(env("LOCALAPPDATA"))
	isForeignPopulatedDefault := func(path string) bool {
		if goos != "windows" || localAppData == "" || path == "" {
			return false
		}
		cleanPath := filepath.Clean(path)
		cleanLocal := filepath.Clean(localAppData)
		return strings.EqualFold(filepath.Dir(cleanPath), cleanLocal) &&
			!strings.EqualFold(filepath.Base(cleanPath), slug) &&
			exists(filepath.Join(cleanPath, "hermes-agent"))
	}
	if v := strings.TrimSpace(env("HERMES_HOME")); v != "" && !isForeignPopulatedDefault(v) {
		return v
	}
	if goos == "windows" {
		if v := strings.TrimSpace(winReg("HERMES_HOME")); v != "" && !isForeignPopulatedDefault(v) {
			return v
		}
		if localAppData != "" {
			return filepath.Join(localAppData, slug)
		}
		if up := strings.TrimSpace(env("USERPROFILE")); up != "" {
			return filepath.Join(up, "AppData", "Local", slug)
		}
		return filepath.Join(home, "AppData", "Local", slug)
	}
	return filepath.Join(home, brandHomeDir)
}

// appFolderTarget maps the resolved app path to what to open.
// windows: the install dir (dir of the exe), opened directly.
// darwin:  the .app bundle, revealed in Finder (reveal=true → `open -R`).
func appFolderTarget(goos, appPath string) (target string, reveal bool) {
	if goos == "windows" {
		return filepath.Dir(appPath), false
	}
	return appPath, true
}

// fileManagerCommand builds the OS file-manager command. Pure → unit-tested both OSes.
func fileManagerCommand(goos, path string, reveal bool) (string, []string) {
	if goos == "windows" {
		if reveal {
			return "explorer", []string{"/select," + path}
		}
		return "explorer", []string{path}
	}
	if reveal {
		return "open", []string{"-R", path}
	}
	return "open", []string{path}
}

// openInFileManager launches Explorer/Finder fire-and-forget. NO hideConsole:
// these are the GUI we want visible; HideWindow would hide it. Explorer returns
// a nonzero exit even on success, so we Start (never Run/wait).
func openInFileManager(path string, reveal bool) error {
	name, args := fileManagerCommand(runtime.GOOS, path, reveal)
	cmd := exec.CommandContext(context.Background(), name, args...) //nolint:gosec // name is constant; path from allowlisted app / known home dirs
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open %s: %w", name, err)
	}
	return nil
}

func runningDesktopCandidate(out *desktopOutput, running func(desktopCandidate) (bool, error)) (*desktopCandidate, error) {
	if out == nil || out.State != DesktopRunning || out.Candidate == nil {
		return nil, errDesktopNotRunning
	}
	alive, err := running(*out.Candidate)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errDesktopRevalidation, err)
	}
	if !alive {
		return nil, errDesktopNotRunning
	}
	return out.Candidate, nil
}

func runOpenDesktopFolder(
	kind desktopFolderKind,
	out *desktopOutput,
	goos string,
	env func(string) string,
	home string,
	winReg func(string) string,
	exists func(string) bool,
	running func(desktopCandidate) (bool, error),
	open func(string, bool) error,
) error {
	candidate, err := runningDesktopCandidate(out, running)
	if err != nil {
		return err
	}

	switch kind {
	case desktopAppFolder:
		target, reveal := appFolderTarget(goos, candidate.AppPath)
		return open(target, reveal)
	case desktopDataFolder:
		target := resolveHermesHome(goos, env, home, candidate.Slug, candidate.HomeDir, winReg, exists)
		if !exists(target) {
			return fmt.Errorf("Co-Worker data folder not found: %s", target)
		}
		return open(target, false)
	default:
		return fmt.Errorf("unknown Co-Worker folder kind %d", kind)
	}
}

func (s *trayState) handleOpenAppFolder() {
	s.handleOpenDesktopFolder(desktopAppFolder, "Open Co-Worker App Folder")
}

func (s *trayState) handleOpenDataFolder() {
	s.handleOpenDesktopFolder(desktopDataFolder, "Open Co-Worker Data Folder")
}

func (s *trayState) handleOpenDesktopFolder(kind desktopFolderKind, title string) {
	out := s.desktopCurrent.Load()
	err := runOpenDesktopFolder(
		kind,
		out,
		runtime.GOOS,
		os.Getenv,
		homeDir(),
		readUserEnvVar,
		statExists,
		isDesktopRunning,
		openInFileManager,
	)
	if err == nil {
		return
	}

	switch {
	case errors.Is(err, errDesktopNotRunning):
		stopped := desktopOutput{State: DesktopStopped}
		if out != nil {
			stopped.Candidate = out.Candidate
		}
		s.publishDesktopOutput(stopped)
	case errors.Is(err, errDesktopRevalidation):
		s.publishDesktopOutput(desktopOutput{State: DesktopDetectionError, Detail: err.Error()})
	default:
		notify(title, "Could not open: "+err.Error())
		return
	}
	requestDesktopRefresh(s.desktopRefreshCh)
	notify(title, err.Error())
}

func (s *trayState) handleOpenGatewayFolder() {
	dir := s.gwHome
	if !statExists(dir) {
		notify("Open Gateway Folder", "Not found: "+dir)
		return
	}
	if err := openInFileManager(dir, false); err != nil {
		notify("Open Gateway Folder", "Could not open: "+err.Error())
	}
}
