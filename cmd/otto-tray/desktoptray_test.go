//go:build darwin || windows

package main

import "testing"

func TestDesktopLabel(t *testing.T) {
	// The tray label is brand-neutral regardless of the underlying identity.
	if got := desktopLabel("· running"); got != "Co-Worker · running" {
		t.Errorf("got %q", got)
	}
	if got := desktopLabel(""); got != "Co-Worker" {
		t.Errorf("default: got %q", got)
	}
}
