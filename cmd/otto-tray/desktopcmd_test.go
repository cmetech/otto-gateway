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
	id := defaultBrandIdentity()
	n, a := desktopStopCommand("windows", id, false)
	if n != "taskkill" || strings.Join(a, " ") != "/IM OTTO.exe /T" {
		t.Fatalf("win stop graceful: %s %v", n, a)
	}
	_, a = desktopStopCommand("windows", id, true)
	if strings.Join(a, " ") != "/IM OTTO.exe /T /F" {
		t.Fatalf("win stop force: %v", a)
	}
	n, a = desktopStopCommand("darwin", id, false)
	if n != "osascript" || !strings.Contains(strings.Join(a, " "), `quit app "OTTO"`) {
		t.Fatalf("mac stop graceful: %s %v", n, a)
	}
	n, a = desktopStopCommand("darwin", id, true)
	if n != "pkill" || a[0] != "-f" || a[1] != id.MacProcMatch {
		t.Fatalf("mac stop force: %s %v", n, a)
	}
}
