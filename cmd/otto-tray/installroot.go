//go:build darwin || windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolveInstallRoot returns the install root of the OTTO Gateway
// distribution. Precedence:
//  1. $OTTO_HOME (used by dev/worktree runs)
//  2. <executable>'s parent directory's parent (the "bin/" walk-up)
//
// Symlinks in the executable path are resolved so the result matches
// the canonical install root the shell wrapper computes.
func resolveInstallRoot() (string, error) {
	if v := os.Getenv("OTTO_HOME"); v != "" {
		return v, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("os.Executable: %w", err)
	}
	return resolveInstallRootFrom(exe)
}

func resolveInstallRootFrom(execPath string) (string, error) {
	if execPath == "" {
		return "", errors.New("empty exec path")
	}
	resolved, err := filepath.EvalSymlinks(execPath)
	if err != nil {
		// EvalSymlinks fails when the file does not exist on the
		// canonical path (e.g. during tests with a tmpfile). Fall
		// back to the raw exec path; the parent walk still works.
		resolved = execPath
	}
	binDir := filepath.Dir(resolved)
	// macOS .app bundle layout: <install_root>/OTTO Tray.app/Contents/MacOS/otto-tray.
	// The installer wraps the binary in a bundle so `open` reaches it as
	// a proper menu-bar agent. The default Dir(Dir(exe)) walk lands on
	// .app/Contents, which has no scripts/ or .env.otto-gw — start fails
	// and dashboard reads the wrong env. Detect the bundle layout and
	// step over .app to reach the real install root.
	if filepath.Base(binDir) == "MacOS" {
		contents := filepath.Dir(binDir)
		if filepath.Base(contents) == "Contents" {
			appDir := filepath.Dir(contents)
			if strings.HasSuffix(appDir, ".app") {
				root := filepath.Dir(appDir)
				if root == "" || root == "." {
					return "", errors.New("cannot resolve install root from " + execPath)
				}
				return root, nil
			}
		}
	}
	root := filepath.Dir(binDir)
	if root == "" || root == "." {
		return "", errors.New("cannot resolve install root from " + execPath)
	}
	return root, nil
}
