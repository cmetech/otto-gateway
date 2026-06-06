//go:build darwin || windows

package main

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWrapperPath_DarwinUsesShellScript(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only path resolution")
	}
	cmd, args := wrapperCommand("/opt/otto", "start")
	if !strings.HasSuffix(cmd, filepath.Join("scripts", "otto-gw")) {
		t.Fatalf("darwin wrapper: got %q, want suffix scripts/otto-gw", cmd)
	}
	if len(args) != 1 || args[0] != "start" {
		t.Fatalf("darwin args: got %v, want [start]", args)
	}
}

func TestWrapperPath_WindowsUsesPwsh(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only path resolution")
	}
	cmd, args := wrapperCommand(`C:\opt\otto`, "stop")
	if cmd != "pwsh" && cmd != "powershell" {
		t.Fatalf("windows shell: got %q, want pwsh or powershell", cmd)
	}
	if len(args) < 4 || args[len(args)-1] != "stop" {
		t.Fatalf("windows args: got %v, want trailing 'stop'", args)
	}
}
