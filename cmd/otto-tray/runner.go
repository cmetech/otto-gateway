//go:build darwin || windows

package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
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

const (
	lifecycleWrapperTimeout = 30 * time.Second
	// The support scripts enforce a 180-second collection deadline. Leave a
	// cleanup margin so their staging removal and atomic archive publication
	// can finish before the tray cancels the process.
	supportWrapperTimeout = 210 * time.Second
)

// runWrapper invokes the gw wrapper with the given verb
// (lifecycle verbs or "support"). installDir locates the wrapper
// script (under $GW_INSTALL_DIR/scripts); gwHome becomes the
// subprocess's working directory so any relative logs/state paths
// the wrapper resolves land in the data home, not the code dir. The
// subprocess is detached (new process group on darwin, DETACHED_PROCESS
// on win) so quitting the tray does not signal the gateway. Lifecycle verbs
// retain the wrapper's 30-second readiness budget; support allows the
// collector's 180-second deadline plus cleanup margin.
func runWrapper(installDir, gwHome, verb string, extraArgs ...string) runResult {
	ctx, cancel := wrapperContext(context.Background(), verb)
	defer cancel()

	cmdName, args := wrapperCommand(installDir, verb, extraArgs...)
	cmd := exec.CommandContext(ctx, cmdName, args...) //nolint:gosec // cmdName + args come from constants and operator-controlled installDir
	cmd.Dir = gwHome
	detachProcessGroup(cmd)
	configureWrapperCancellation(cmd)

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

func wrapperTimeoutForVerb(verb string) time.Duration {
	if strings.EqualFold(strings.TrimSpace(verb), "support") {
		return supportWrapperTimeout
	}
	return lifecycleWrapperTimeout
}

func wrapperContext(parent context.Context, verb string) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, wrapperTimeoutForVerb(verb))
}

func supportCoworkerArgs(goos, home string) []string {
	if strings.TrimSpace(home) == "" {
		return nil
	}
	if goos == "windows" {
		return []string{"-CoworkerHome", home}
	}
	return []string{"--co-worker-home", home}
}
