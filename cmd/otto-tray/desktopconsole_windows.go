//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// createNoWindow is CREATE_NO_WINDOW: run a console child with no console
// window allocated. Paired with HideWindow so a GUI systray parent never
// flashes a console when it spawns taskkill or powershell.
const createNoWindow = 0x08000000

// hideConsole suppresses the console window Windows would otherwise allocate
// when this GUI process spawns a console program. No-op off Windows.
func hideConsole(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
