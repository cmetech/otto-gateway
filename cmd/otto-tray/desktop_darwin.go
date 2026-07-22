//go:build darwin

package main

import (
	"context"
	"errors"
	"os/exec"
	"time"
)

func init() { desktopRunningFn = platformDesktopRunning }

// platformDesktopRunning reports whether the selected candidate's exact
// executable command path is alive.
func platformDesktopRunning(candidate desktopCandidate) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The leading anchor also prevents the pattern from being parsed as an option.
	err := exec.CommandContext(ctx, "pgrep", "-f", macExecutablePattern(candidate.ExecutablePath)).Run() //nolint:gosec // executable path comes from a validated installed candidate
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}
