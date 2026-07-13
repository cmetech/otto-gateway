//go:build darwin

package main

import "os/exec"

// hideConsole is a no-op on darwin — only a Windows GUI app flashes a console
// when it spawns a console child. Present so darwin||windows callers compile.
func hideConsole(cmd *exec.Cmd) {}
