//go:build darwin || windows

package main

import (
	"os"
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

// resolveGatewayID reads the persisted Gateway ID the same way the gateway
// writes it (cmd/otto-gateway resolveGatewayID): GW_ID env →
// $GW_HOME/gateway-id → <UserConfigDir>/gateway/gateway-id. The tray-managed
// gwHome is also probed so both exported-GW_HOME installs and the default
// install (GW_HOME unset → gateway persists under UserConfigDir) resolve to
// the same value the metric label uses. Returns "" when the gateway has never
// persisted an id; the tray deliberately does NOT generate/write one (that is
// the gateway's job — writing here could race the gateway's own generation).
func resolveGatewayID(gwHome string, env func(string) string) string {
	if v := strings.TrimSpace(env("GW_ID")); v != "" {
		return v
	}
	var candidates []string
	if h := strings.TrimSpace(env("GW_HOME")); h != "" {
		candidates = append(candidates, filepath.Join(h, "gateway-id"))
	}
	if gwHome != "" {
		candidates = append(candidates, filepath.Join(gwHome, "gateway-id"))
	}
	if cd, err := os.UserConfigDir(); err == nil {
		candidates = append(candidates, filepath.Join(cd, "gateway", "gateway-id"))
	}
	for _, p := range candidates {
		if b, err := os.ReadFile(p); err == nil { //nolint:gosec // fixed gateway-id path under a config home
			if id := strings.TrimSpace(string(b)); id != "" {
				return id
			}
		}
	}
	return ""
}

func gwPidFile(gwHome string) string        { return filepath.Join(gwHome, "state", "gateway.pid") }
func gwSentinel(gwHome string) string       { return filepath.Join(gwHome, ".config-error") }
func gwEnvPath(gwHome string) string        { return filepath.Join(gwHome, ".env") }
func gwOverridesPath(gwHome string) string  { return filepath.Join(gwHome, "overrides.env") }
func gwTrayConfigPath(gwHome string) string { return filepath.Join(gwHome, "tray.json") }

// resolveInstallDirFrom returns the code install dir given the tray executable
// path. Walks up from bin/; steps over the macOS "Gateway Tray.app" bundle.
func resolveInstallDirFrom(execPath string) (string, error) { //nolint:unparam // error kept for caller symmetry; EvalSymlinks failure is intentionally swallowed (falls back to execPath)
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
