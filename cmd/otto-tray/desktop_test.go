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

func TestBrandJSONPathForApp(t *testing.T) {
	if p := brandJSONPathForApp("darwin", "/Applications/OTTO.app"); p != "/Applications/OTTO.app/Contents/Resources/brand.json" {
		t.Fatalf("darwin brand.json path: %q", p)
	}
	win := brandJSONPathForApp("windows", `C:\P\OTTO\OTTO.exe`)
	if filepath.Base(win) != "brand.json" || filepath.Base(filepath.Dir(win)) != "resources" {
		t.Fatalf("windows brand.json path: %q", win)
	}
}
