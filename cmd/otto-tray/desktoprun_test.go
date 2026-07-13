//go:build darwin || windows

package main

import "testing"

func TestIsDesktopRunning_UsesSeam(t *testing.T) {
	old := desktopRunningFn
	defer func() { desktopRunningFn = old }()

	desktopRunningFn = func(id brandIdentity) bool { return true }
	if !isDesktopRunning(defaultBrandIdentity()) {
		t.Fatal("expected running=true from stub")
	}
	desktopRunningFn = func(id brandIdentity) bool { return false }
	if isDesktopRunning(defaultBrandIdentity()) {
		t.Fatal("expected running=false from stub")
	}
}
