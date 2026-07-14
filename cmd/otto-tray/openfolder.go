//go:build darwin || windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// brandSlug lowercases the brand display name → "otto" / "loop24".
func brandSlug(id brandIdentity) string { return strings.ToLower(id.DisplayName) }

// resolveHermesHome mirrors hermes-agent's resolution (main.ts / hermes_constants.py):
// env HERMES_HOME → (windows) HKCU\Environment\HERMES_HOME → default per-OS/brand.
// winReg returns "" off windows / when unset. Pure (all inputs injected) for tests.
func resolveHermesHome(goos string, env func(string) string, home, slug string, winReg func(string) string) string {
	if v := strings.TrimSpace(env("HERMES_HOME")); v != "" {
		return v
	}
	if goos == "windows" {
		if v := strings.TrimSpace(winReg("HERMES_HOME")); v != "" {
			return v
		}
		if la := strings.TrimSpace(env("LOCALAPPDATA")); la != "" {
			return filepath.Join(la, slug)
		}
		if up := strings.TrimSpace(env("USERPROFILE")); up != "" {
			return filepath.Join(up, "AppData", "Local", slug)
		}
		return filepath.Join(home, "AppData", "Local", slug)
	}
	return filepath.Join(home, "."+slug)
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

func (s *trayState) handleOpenAppFolder() {
	id, appPath := resolveDesktopIdentity(runtime.GOOS, os.Getenv, homeDir(), statExists, os.ReadFile)
	if appPath == "" {
		notify("Open App Folder", id.DisplayName+" desktop app not found. Install it first.")
		return
	}
	target, reveal := appFolderTarget(runtime.GOOS, appPath)
	if err := openInFileManager(target, reveal); err != nil {
		notify("Open App Folder", "Could not open: "+err.Error())
	}
}

func (s *trayState) handleOpenDataFolder() {
	id, _ := resolveDesktopIdentity(runtime.GOOS, os.Getenv, homeDir(), statExists, os.ReadFile)
	home := resolveHermesHome(runtime.GOOS, os.Getenv, homeDir(), brandSlug(id), readUserEnvVar)
	if !statExists(home) {
		notify("Open Data Folder", "Not found: "+home)
		return
	}
	if err := openInFileManager(home, false); err != nil {
		notify("Open Data Folder", "Could not open: "+err.Error())
	}
}

func (s *trayState) handleOpenGatewayFolder() {
	dir := filepath.Join(homeDir(), ".otto-gw")
	if !statExists(dir) {
		notify("Open Gateway Folder", "Not found: "+dir)
		return
	}
	if err := openInFileManager(dir, false); err != nil {
		notify("Open Gateway Folder", "Could not open: "+err.Error())
	}
}
