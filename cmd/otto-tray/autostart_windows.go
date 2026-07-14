//go:build windows

package main

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows/registry"
)

const (
	runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
	// Task B3 (de-brand): value name renamed OttoTray -> GatewayTray;
	// the installer (Task E) removes the old Run key on migration.
	runKeyValueName = "GatewayTray"
)

func installRunKey(execPath string) error { //nolint:unused // wired in by Task 12 tray UI
	return installRunKeyForTest(runKeyValueName, execPath)
}

func uninstallRunKey() error { //nolint:unused // wired in by Task 12 tray UI
	return uninstallRunKeyForTest(runKeyValueName)
}

// installRunKeyForTest / uninstallRunKeyForTest expose the value
// name so tests can use a non-production key without monkey-patching
// the const.
func installRunKeyForTest(name, execPath string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open HKCU run key: %w", err)
	}
	defer func() { _ = k.Close() }()
	if err := k.SetStringValue(name, execPath); err != nil {
		return fmt.Errorf("set HKCU run value: %w", err)
	}
	return nil
}

func uninstallRunKeyForTest(name string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open HKCU run key: %w", err)
	}
	defer func() { _ = k.Close() }()
	if err := k.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("delete HKCU run value: %w", err)
	}
	return nil
}
