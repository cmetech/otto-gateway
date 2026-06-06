//go:build windows

package main

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows/registry"
)

func TestRunKey_InstallSetsAndUninstallClears(t *testing.T) {
	t.Cleanup(func() { _ = uninstallRunKeyForTest("OttoTrayTest") })

	if err := installRunKeyForTest("OttoTrayTest", `C:\opt\otto\bin\otto-tray.exe`); err != nil {
		t.Fatalf("install: %v", err)
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("open run key: %v", err)
	}
	defer func() { _ = k.Close() }()
	got, _, err := k.GetStringValue("OttoTrayTest")
	if err != nil {
		t.Fatalf("read value: %v", err)
	}
	if got != `C:\opt\otto\bin\otto-tray.exe` {
		t.Fatalf("value: got %q, want exec path", got)
	}
	if err := uninstallRunKeyForTest("OttoTrayTest"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, _, err := k.GetStringValue("OttoTrayTest"); !errors.Is(err, registry.ErrNotExist) {
		t.Fatalf("value still present after uninstall, err=%v", err)
	}
}
