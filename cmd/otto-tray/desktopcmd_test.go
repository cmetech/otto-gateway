//go:build darwin || windows

package main

import (
	"strings"
	"testing"
)

func TestDesktopInstallCommand(t *testing.T) {
	n, a := desktopInstallCommand("windows")
	if n != "powershell" || !strings.Contains(strings.Join(a, " "), "cmetech/otto/main/install.ps1") {
		t.Fatalf("win install: %s %v", n, a)
	}
	n, a = desktopInstallCommand("darwin")
	if n != "/bin/sh" || !strings.Contains(strings.Join(a, " "), "cmetech/otto/main/install.sh") {
		t.Fatalf("mac install: %s %v", n, a)
	}
}

func TestDesktopStartCommand(t *testing.T) {
	n, a := desktopStartCommand("darwin", "/Applications/OTTO.app")
	if n != "open" || len(a) != 1 || a[0] != "/Applications/OTTO.app" {
		t.Fatalf("mac start: %s %v", n, a)
	}
	n, a = desktopStartCommand("windows", `C:\P\OTTO\OTTO.exe`)
	if n != `C:\P\OTTO\OTTO.exe` || len(a) != 0 {
		t.Fatalf("win start: %s %v", n, a)
	}
}

func TestDesktopStopCommand(t *testing.T) {
	candidate := desktopCandidate{
		Identity:       identityFromDisplayName("LOOP.24"),
		AppPath:        "/Applications/LOOP.24.app",
		ExecutablePath: "/Applications/LOOP.24.app/Contents/MacOS/LOOP.24",
	}
	n, a := desktopStopCommand("windows", candidate, []uint32{42, 81}, false)
	if n != "taskkill" || strings.Join(a, " ") != "/PID 42 /PID 81 /T" {
		t.Fatalf("win stop graceful: %s %v", n, a)
	}
	_, a = desktopStopCommand("windows", candidate, []uint32{42}, true)
	if strings.Join(a, " ") != "/PID 42 /T /F" || strings.Contains(strings.Join(a, " "), "/IM") {
		t.Fatalf("win stop force: %v", a)
	}
	n, a = desktopStopCommand("darwin", candidate, nil, false)
	if n != "osascript" || !strings.Contains(strings.Join(a, " "), `application "/Applications/LOOP.24.app"`) {
		t.Fatalf("mac stop graceful: %s %v", n, a)
	}
	n, a = desktopStopCommand("darwin", candidate, nil, true)
	wantPattern := `^/Applications/LOOP\.24\.app/Contents/MacOS/LOOP\.24$`
	if n != "pkill" || a[0] != "-f" || a[1] != wantPattern {
		t.Fatalf("mac stop force: %s %v", n, a)
	}
}

func TestMacExecutablePatternEscapesAndAnchorsExactPath(t *testing.T) {
	path := "/Applications/LOOP.24.app/Contents/MacOS/LOOP.24"
	if got, want := macExecutablePattern(path), `^/Applications/LOOP\.24\.app/Contents/MacOS/LOOP\.24$`; got != want {
		t.Fatalf("mac executable pattern = %q, want %q", got, want)
	}
}

func TestMatchingWindowsProcessIDsRequiresExactNormalizedPath(t *testing.T) {
	wantPath := `C:\Users\me\AppData\Local\Programs\LOOP24\LOOP24.exe`
	processes := []desktopProcess{
		{PID: 17, ExecutablePath: `C:\Other\LOOP24.exe`},
		{PID: 23, ExecutablePath: `c:/users/me/appdata/local/programs/loop24/loop24.EXE`},
		{PID: 29, ExecutablePath: `C:\Users\me\AppData\Local\Programs\LOOP24\helper.exe`},
	}
	got := matchingWindowsProcessIDs(wantPath, processes)
	if len(got) != 1 || got[0] != 23 {
		t.Fatalf("matching process IDs = %v, want [23]", got)
	}
}
