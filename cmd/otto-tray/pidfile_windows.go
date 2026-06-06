//go:build windows

package main

import "golang.org/x/sys/windows"

// processAlive on Windows uses OpenProcess + GetExitCodeProcess.
// os.Process.Signal on Windows returns ErrUnsupported for every
// signal except Kill, so the POSIX-style Signal(0) probe used to
// always return false — the tray never saw a running gateway even
// when one was up and serving on /health (the v2.0.7 symptom).
//
// STILL_ACTIVE (259) is the documented exit code for a process that
// has not yet terminated. Anything else means the process exited
// (or never existed). OpenProcess errors mean the PID is invalid;
// GetExitCodeProcess errors get treated as alive so the tray
// surfaces the gateway rather than silently hiding it.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	const stillActive uint32 = 259
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var exitCode uint32
	if err := windows.GetExitCodeProcess(h, &exitCode); err != nil {
		return true
	}
	return exitCode == stillActive
}
