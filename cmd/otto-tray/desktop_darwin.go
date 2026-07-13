//go:build darwin

package main

import "os/exec"

func init() { desktopRunningFn = platformDesktopRunning }

// platformDesktopRunning reports whether the packaged desktop process is alive.
// Matches the distinctive bundle path (…/OTTO.app/Contents/MacOS/OTTO) to avoid
// matching an unrelated process merely named "OTTO". The match string is a
// validated brand identity value (see validateDisplayName), so it is safe to
// pass to pgrep.
func platformDesktopRunning(id brandIdentity) bool {
	// #nosec G204 -- id.MacProcMatch derives from a validateDisplayName-checked
	// display name; no unsanitized input reaches exec.
	err := exec.Command("pgrep", "-f", id.MacProcMatch).Run()
	return err == nil // pgrep exits 0 iff ≥1 match
}
