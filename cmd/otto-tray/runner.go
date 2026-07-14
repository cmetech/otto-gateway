//go:build darwin || windows

package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// runResult captures everything the tray needs to surface an error
// in a notification. Empty Stderr + ExitCode 0 = success.
type runResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Err      error
}

// runWrapper invokes the otto-gw wrapper with the given verb
// ("start" / "stop" / "restart"). installDir locates the wrapper
// script (under $GW_INSTALL_DIR/scripts); gwHome becomes the
// subprocess's working directory so any relative logs/state paths
// the wrapper resolves land in the data home, not the code dir. The
// subprocess is detached (new process group on darwin, DETACHED_PROCESS
// on win) so quitting the tray does not signal the gateway. A 30s
// timeout matches the wrapper's own readiness wait — anything longer
// is reported as a failure to the user.
func runWrapper(installDir, gwHome, verb string) runResult {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmdName, args := wrapperCommand(installDir, verb)
	cmd := exec.CommandContext(ctx, cmdName, args...) //nolint:gosec // cmdName + args come from constants and operator-controlled installDir
	cmd.Dir = gwHome
	detachProcessGroup(cmd)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exit := 0
	if cmd.ProcessState != nil {
		exit = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		err = fmt.Errorf("run wrapper %s %s: %w", cmdName, verb, err)
	}
	return runResult{
		ExitCode: exit,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Err:      err,
	}
}
