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
//
//nolint:unused // wired in by Task 12 tray UI
type runResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Err      error
}

// runWrapper invokes the otto-gw wrapper with the given verb
// ("start" / "stop" / "restart"). The subprocess is detached (new
// process group on darwin, DETACHED_PROCESS on win) so quitting the
// tray does not signal the gateway. A 30s timeout matches the
// wrapper's own readiness wait — anything longer is reported as a
// failure to the user.
//
//nolint:unused // wired in by Task 12 tray UI
func runWrapper(installRoot, verb string) runResult {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmdName, args := wrapperCommand(installRoot, verb)
	cmd := exec.CommandContext(ctx, cmdName, args...) //nolint:gosec // cmdName + args come from constants and operator-controlled installRoot
	cmd.Dir = installRoot
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
