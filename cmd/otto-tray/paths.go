//go:build darwin || windows

package main

import (
	"path/filepath"
	"strings"
)

// resolveGWHome returns the user-data config home: env GW_HOME → ~/.gw.
func resolveGWHome(env func(string) string, home string) string {
	if v := strings.TrimSpace(env("GW_HOME")); v != "" {
		return v
	}
	return filepath.Join(home, ".gw")
}

func gwPidFile(gwHome string) string        { return filepath.Join(gwHome, "state", "gateway.pid") }
func gwSentinel(gwHome string) string       { return filepath.Join(gwHome, ".config-error") }
func gwEnvPath(gwHome string) string        { return filepath.Join(gwHome, ".env") }
func gwOverridesPath(gwHome string) string  { return filepath.Join(gwHome, "overrides.env") }
func gwTrayConfigPath(gwHome string) string { return filepath.Join(gwHome, "tray.json") }

// resolveInstallDirFrom returns the code install dir given the tray executable
// path. Walks up from bin/; steps over the macOS "Gateway Tray.app" bundle.
func resolveInstallDirFrom(execPath string) (string, error) {
	resolved, err := filepath.EvalSymlinks(execPath)
	if err != nil {
		resolved = execPath
	}
	binDir := filepath.Dir(resolved)
	// macOS app bundle: …/Gateway Tray.app/Contents/MacOS/gateway-tray
	if filepath.Base(binDir) == "MacOS" {
		contents := filepath.Dir(binDir)
		if filepath.Base(contents) == "Contents" {
			appDir := filepath.Dir(contents)
			if strings.HasSuffix(appDir, ".app") {
				return filepath.Dir(appDir), nil
			}
		}
	}
	return filepath.Dir(binDir), nil
}
