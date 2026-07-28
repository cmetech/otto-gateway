//go:build darwin

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// wrapperCommand returns the executable and args to run the gw
// shell wrapper on darwin. The wrapper itself lives at
// scripts/gw under $GW_INSTALL_DIR.
func wrapperCommand(installDir, verb string, extraArgs ...string) (string, []string) {
	return filepath.Join(installDir, "scripts", "gw"), append([]string{verb}, extraArgs...)
}

func detachProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// configureWrapperCancellation replaces CommandContext's root-only kill with
// a process-group kill. Support collection may be running a compression child
// when its deadline expires; killing only the wrapper would leave that child
// alive with inherited output handles and make Cmd.Wait hang.
func configureWrapperCancellation(cmd *exec.Cmd) {
	cmd.WaitDelay = 2 * time.Second
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}

// detachGUIProcess mirrors detachProcessGroup on darwin (Setpgid) — the desktop
// app is launched via `open`, so there is no window-hiding concern here; this
// exists so the shared darwin||windows spawnDetached compiles on both OSes.
func detachGUIProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
