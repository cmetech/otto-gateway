//go:build windows

package main

import (
	"os/exec"
	"strings"
)

func init() { desktopRunningFn = platformDesktopRunning }

// platformDesktopRunning reports whether the desktop .exe is running via
// tasklist. id.WinExeName derives from a validated display name.
func platformDesktopRunning(id brandIdentity) bool {
	// #nosec G204 -- id.WinExeName derives from a validateDisplayName-checked
	// display name; the filter value is quoted and bounded.
	out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq "+id.WinExeName, "/NH").Output()
	if err != nil {
		return false
	}
	// tasklist prints "INFO: No tasks..." when nothing matches; a real row
	// contains the image name.
	return strings.Contains(string(out), id.WinExeName)
}
