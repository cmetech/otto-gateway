//go:build windows

package main

import (
	"os/exec"
	"testing"
)

// Regression guard for the v2.1.0 console-flicker bug: every Windows spawn from
// this GUI app must suppress the console window when it runs a console child.
// See quick task 260713-pbk.
func TestHideConsole_SuppressesConsoleWindow(t *testing.T) {
	cmd := exec.Command("taskkill")
	hideConsole(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("hideConsole left SysProcAttr nil")
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Error("hideConsole did not set HideWindow=true")
	}
	if cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Error("hideConsole did not set CREATE_NO_WINDOW")
	}
}
