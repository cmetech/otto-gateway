//go:build windows

package main

import (
	"os/exec"
	"testing"
)

// Regression guard: the desktop GUI app must be launched WITHOUT HideWindow, or
// Electron starts hidden (5 live OTTO.exe procs, no window). See quick 260713-qw7.
func TestDetachGUIProcess_DoesNotHideWindow(t *testing.T) {
	cmd := exec.Command("OTTO.exe")
	detachGUIProcess(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("detachGUIProcess left SysProcAttr nil")
	}
	if cmd.SysProcAttr.HideWindow {
		t.Error("GUI launch must NOT set HideWindow (Electron would start hidden)")
	}
	if cmd.SysProcAttr.CreationFlags&createNewProcessGroup == 0 {
		t.Error("expected CREATE_NEW_PROCESS_GROUP so the app outlives the tray")
	}
}
