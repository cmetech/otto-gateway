//go:build darwin || windows

package main

import (
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
