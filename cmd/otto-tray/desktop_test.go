//go:build darwin || windows

package main

import (
	"path/filepath"
	"testing"
)

func TestDesktopAppCandidates(t *testing.T) {
	id := defaultBrandIdentity()
	env := func(k string) string {
		switch k {
		case "LOCALAPPDATA":
			return `C:\Users\me\AppData\Local`
		}
		return ""
	}
	win := desktopAppCandidates("windows", id, env, "")
	if len(win) == 0 || filepath.Base(win[0]) != "OTTO.exe" {
		t.Fatalf("windows candidates bad: %v", win)
	}
	mac := desktopAppCandidates("darwin", id, func(string) string { return "" }, "/Users/me")
	if len(mac) < 2 || mac[0] != "/Applications/OTTO.app" {
		t.Fatalf("darwin candidates bad: %v", mac)
	}
}

func TestInstalledAppPath(t *testing.T) {
	id := defaultBrandIdentity()
	present := "/Applications/OTTO.app"
	exists := func(p string) bool { return p == present }
	got := installedAppPath("darwin", id, func(string) string { return "" }, "/Users/me", exists)
	if got != present {
		t.Fatalf("expected %q, got %q", present, got)
	}
	none := installedAppPath("darwin", id, func(string) string { return "" }, "/Users/me", func(string) bool { return false })
	if none != "" {
		t.Fatalf("expected empty, got %q", none)
	}
}

func TestResolveDesktopIdentity(t *testing.T) {
	const present = "/Applications/OTTO.app"
	exists := func(p string) bool { return p == present }
	// The tray resolves the desktop app from fixed OTTO defaults and never
	// reads brand.json (quick task 260721-an5).
	id, appPath := resolveDesktopIdentity("darwin", func(string) string { return "" }, "/Users/me", exists)
	if id.DisplayName != "OTTO" {
		t.Fatalf("expected DisplayName OTTO, got %q", id.DisplayName)
	}
	if appPath != present {
		t.Fatalf("expected appPath %q, got %q", present, appPath)
	}

	notFound := func(string) bool { return false }
	_, appPath = resolveDesktopIdentity("darwin", func(string) string { return "" }, "/Users/me", notFound)
	if appPath != "" {
		t.Fatalf("expected empty appPath when not installed, got %q", appPath)
	}
}
