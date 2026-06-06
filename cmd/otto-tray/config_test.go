//go:build darwin || windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTrayConfig_DefaultsWhenMissing(t *testing.T) {
	cfg, isFirstRun := loadTrayConfig(filepath.Join(t.TempDir(), "tray.json"))
	if !isFirstRun {
		t.Fatalf("missing file should report first run")
	}
	if cfg.LaunchAtLogin || cfg.StartGatewayOnLaunch {
		t.Fatalf("defaults: got %+v, want both false", cfg)
	}
}

func TestTrayConfig_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tray.json")
	in := TrayConfig{LaunchAtLogin: true, StartGatewayOnLaunch: false}
	if err := saveTrayConfig(path, in); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, isFirstRun := loadTrayConfig(path)
	if isFirstRun {
		t.Fatalf("after save should not report first run")
	}
	if out != in {
		t.Fatalf("round trip: got %+v, want %+v", out, in)
	}
}

func TestTrayConfig_CorruptFileFallsBackToDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tray.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, isFirstRun := loadTrayConfig(path)
	if isFirstRun {
		t.Fatalf("corrupt file should not be treated as first run")
	}
	if cfg.LaunchAtLogin || cfg.StartGatewayOnLaunch {
		t.Fatalf("corrupt config should default false, got %+v", cfg)
	}
}
