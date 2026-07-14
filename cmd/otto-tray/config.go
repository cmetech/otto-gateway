//go:build darwin || windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// TrayConfig holds tray-only UI preferences. It does NOT shadow
// gateway config — only the two login-item toggles live here.
type TrayConfig struct {
	LaunchAtLogin        bool `json:"launch_tray_at_login"`
	StartGatewayOnLaunch bool `json:"start_gateway_when_tray_launches"`
}

// loadTrayConfig reads tray.json. Missing file ⇒ defaults + isFirstRun
// true so the caller can fire the onboarding toast. Corrupt file ⇒
// defaults + isFirstRun false (the user already answered the prompt;
// we just lost their answer).
func loadTrayConfig(path string) (TrayConfig, bool) {
	body, err := os.ReadFile(path) //nolint:gosec // path is operator-configured tray.json under GW_HOME
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return TrayConfig{}, true
		}
		return TrayConfig{}, false
	}
	var cfg TrayConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		return TrayConfig{}, false
	}
	return cfg, false
}

// saveTrayConfig writes atomically (write tmp + rename) so a crash
// mid-write never leaves a half-encoded JSON file.
func saveTrayConfig(path string, cfg TrayConfig) error {
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal tray config: %w", err)
	}
	return writeFileAtomic(path, body)
}

// writeFileAtomic writes body to path via a tmp-and-rename. The
// parent directory is created if it does not exist.
func writeFileAtomic(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp.*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp: %w", err)
	}
	return nil
}
