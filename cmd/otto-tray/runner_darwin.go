//go:build darwin

package main

import (
	"os/exec"
	"path/filepath"
	"syscall"
)

// wrapperCommand returns the executable and args to run the otto-gw
// shell wrapper on darwin. The wrapper itself lives at
// scripts/otto-gw under $GW_INSTALL_DIR.
func wrapperCommand(installDir, verb string) (string, []string) {
	return filepath.Join(installDir, "scripts", "otto-gw"), []string{verb}
}

func detachProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// detachGUIProcess mirrors detachProcessGroup on darwin (Setpgid) — the desktop
// app is launched via `open`, so there is no window-hiding concern here; this
// exists so the shared darwin||windows spawnDetached compiles on both OSes.
func detachGUIProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
