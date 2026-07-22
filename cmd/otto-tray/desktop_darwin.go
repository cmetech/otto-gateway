//go:build darwin

package main

import (
	"context"
	"errors"
	"os/exec"
	"time"
)

func init() { desktopRunningFn = platformDesktopRunning }

// platformDesktopRunning reports whether the packaged desktop process is alive.
// Matches the distinctive bundle path (…/OTTO.app/Contents/MacOS/OTTO) to avoid
// matching an unrelated process merely named "OTTO". The match string is a
// validated brand identity value (see validateDisplayName), so it is safe to
// pass to pgrep.
func platformDesktopRunning(id brandIdentity) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// #nosec G204 -- id.MacProcMatch derives from a validateDisplayName-checked display name; no unsanitized input reaches exec.
	err := exec.CommandContext(ctx, "pgrep", "-f", id.MacProcMatch).Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}
