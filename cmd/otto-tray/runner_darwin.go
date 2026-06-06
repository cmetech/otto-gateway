//go:build darwin

package main

import (
	"os/exec"
	"path/filepath"
	"syscall"
)

// wrapperCommand returns the executable and args to run the otto-gw
// shell wrapper on darwin. The wrapper itself lives at
// scripts/otto-gw under the install root.
func wrapperCommand(installRoot, verb string) (string, []string) {
	return filepath.Join(installRoot, "scripts", "otto-gw"), []string{verb}
}

func detachProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
