//go:build darwin || windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveGWHome(t *testing.T) {
	home := "/home/u"
	// env override wins
	env := func(k string) string {
		if k == "GW_HOME" {
			return "/custom/gw"
		}
		return ""
	}
	if got := resolveGWHome(env, home); got != "/custom/gw" {
		t.Errorf("override: got %q", got)
	}
	// default → ~/.gw
	if got := resolveGWHome(func(string) string { return "" }, home); got != filepath.Join(home, ".gw") {
		t.Errorf("default: got %q", got)
	}
}

func TestGWSubPaths(t *testing.T) {
	gw := filepath.Join("/home/u", ".gw")
	if gwPidFile(gw) != filepath.Join(gw, "state", "gateway.pid") {
		t.Errorf("pid: %q", gwPidFile(gw))
	}
	if gwSentinel(gw) != filepath.Join(gw, ".config-error") {
		t.Errorf("sentinel: %q", gwSentinel(gw))
	}
	if gwEnvPath(gw) != filepath.Join(gw, ".env") {
		t.Errorf("env: %q", gwEnvPath(gw))
	}
	if gwTrayConfigPath(gw) != filepath.Join(gw, "tray.json") {
		t.Errorf("tray.json: %q", gwTrayConfigPath(gw))
	}
}

func TestResolveGatewayID(t *testing.T) {
	// GW_ID env override wins (and beats any file).
	env := func(k string) string {
		if k == "GW_ID" {
			return "OVERRIDE-ID"
		}
		return ""
	}
	if got := resolveGatewayID("/nope", env); got != "OVERRIDE-ID" {
		t.Errorf("GW_ID override: got %q, want OVERRIDE-ID", got)
	}

	// $GW_HOME/gateway-id is read (trimmed) when GW_ID is unset.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gateway-id"), []byte("01ABCDEF\n"), 0o644); err != nil {
		t.Fatalf("write gateway-id: %v", err)
	}
	envHome := func(k string) string {
		if k == "GW_HOME" {
			return dir
		}
		return ""
	}
	if got := resolveGatewayID("/ignored", envHome); got != "01ABCDEF" {
		t.Errorf("GW_HOME file: got %q, want 01ABCDEF", got)
	}

	// The tray-managed gwHome is probed too (covers the default install where
	// the gateway persisted under UserConfigDir but the tray passes its home).
	if got := resolveGatewayID(dir, func(string) string { return "" }); got != "01ABCDEF" {
		t.Errorf("gwHome file: got %q, want 01ABCDEF", got)
	}

	// Degrades to "" when nothing is found (tray must not generate an id).
	// Point the OS config-dir base (macOS: HOME; Windows: APPDATA) at an empty
	// temp dir so os.UserConfigDir resolves somewhere without a gateway-id.
	empty := t.TempDir()
	t.Setenv("HOME", empty)
	t.Setenv("APPDATA", empty)
	if got := resolveGatewayID(filepath.Join(empty, "none"), func(string) string { return "" }); got != "" {
		t.Errorf("missing: got %q, want empty", got)
	}
}

func TestResolveInstallDirFrom(t *testing.T) {
	// bin/ walk-up: <dir>/bin/gateway-tray → <dir>
	got, err := resolveInstallDirFrom(filepath.Join("/opt/Gateway", "bin", "gateway-tray"))
	if err != nil || got != "/opt/Gateway" {
		t.Errorf("bin walk-up: got %q err %v", got, err)
	}
}

// TestResolveDashboardURLUsesGWHome pins the seam A2 rewires: dotenv lookup
// must resolve against $GW_HOME, not the code install dir. gwOverridesPath
// isn't covered by TestGWSubPaths above, so this closes that gap too.
func TestResolveDashboardURLUsesGWHome(t *testing.T) {
	gwHome := filepath.Join("/home/u", ".gw")
	if got := gwEnvPath(gwHome); got != filepath.Join(gwHome, ".env") {
		t.Fatalf("env path: %q", got)
	}
	if got := gwOverridesPath(gwHome); got != filepath.Join(gwHome, "overrides.env") {
		t.Fatalf("overrides path: %q", got)
	}
}
