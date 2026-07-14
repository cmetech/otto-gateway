//go:build darwin

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchAgentPlist_ContainsExecPath(t *testing.T) {
	body := launchAgentPlist("/opt/gateway/bin/gateway-tray")
	if !strings.Contains(body, "<string>/opt/gateway/bin/gateway-tray</string>") {
		t.Fatalf("plist missing exec path:\n%s", body)
	}
	if !strings.Contains(body, "<key>RunAtLoad</key>") {
		t.Fatalf("plist missing RunAtLoad key")
	}
	if !strings.Contains(body, "io.cmetech.gateway-tray") {
		t.Fatalf("plist missing bundle id")
	}
}

func TestLaunchAgentInstall_WritesAndRemoves(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := installLaunchAgent("/opt/gateway/bin/gateway-tray", true /* skipLaunchctl */); err != nil {
		t.Fatalf("install: %v", err)
	}
	plistPath := filepath.Join(tmpHome, "Library", "LaunchAgents", "io.cmetech.gateway-tray.plist")
	if _, err := os.Stat(plistPath); err != nil {
		t.Fatalf("plist not written: %v", err)
	}

	if err := uninstallLaunchAgent(true /* skipLaunchctl */); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
		t.Fatalf("plist still exists after uninstall: %v", err)
	}
}
