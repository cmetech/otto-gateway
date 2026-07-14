//go:build windows

package main

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows/registry"
)

func TestRunKey_InstallSetsAndUninstallClears(t *testing.T) {
	t.Cleanup(func() { _ = uninstallRunKeyForTest("GatewayTrayTest") })

	if err := installRunKeyForTest("GatewayTrayTest", `C:\opt\gateway\bin\gateway-tray.exe`); err != nil {
		t.Fatalf("install: %v", err)
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("open run key: %v", err)
	}
	defer func() { _ = k.Close() }()
	got, _, err := k.GetStringValue("GatewayTrayTest")
	if err != nil {
		t.Fatalf("read value: %v", err)
	}
	if got != `C:\opt\gateway\bin\gateway-tray.exe` {
		t.Fatalf("value: got %q, want exec path", got)
	}
	if err := uninstallRunKeyForTest("GatewayTrayTest"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, _, err := k.GetStringValue("GatewayTrayTest"); !errors.Is(err, registry.ErrNotExist) {
		t.Fatalf("value still present after uninstall, err=%v", err)
	}
}
