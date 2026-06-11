//go:build windows

package main

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

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

// verifyGatewayIdentity returns true if the process at pid has an
// executable name ending in "otto-gateway.exe". Uses the same
// PROCESS_QUERY_LIMITED_INFORMATION handle already used by processAlive.
func verifyGatewayIdentity(pid int, _ string) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(h) }()
	buf := make([]uint16, windows.MAX_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return false
	}
	fullPath := windows.UTF16ToString(buf[:n])
	return strings.EqualFold(filepath.Base(fullPath), "otto-gateway.exe")
}
