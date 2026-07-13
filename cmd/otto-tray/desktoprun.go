//go:build darwin || windows

package main

// desktopRunningFn is the process-liveness probe for the desktop app. It is a
// package var so tests can substitute a stub and so each platform file can
// assign its own implementation in init(). isDesktopRunning is the caller-facing
// entry.
var desktopRunningFn = func(id brandIdentity) bool { return false }

func isDesktopRunning(id brandIdentity) bool { return desktopRunningFn(id) }
