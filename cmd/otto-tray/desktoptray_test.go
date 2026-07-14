//go:build darwin || windows

package main

import "testing"

func TestDesktopLabel(t *testing.T) {
	if got := desktopLabel(brandIdentity{DisplayName: "LOOP24"}, "· running"); got != "LOOP24 Desktop · running" {
		t.Errorf("got %q", got)
	}
	if got := desktopLabel(defaultBrandIdentity(), ""); got != "OTTO Desktop" {
		t.Errorf("default: got %q", got)
	}
}
