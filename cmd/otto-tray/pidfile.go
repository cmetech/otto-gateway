//go:build darwin || windows

package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// readPIDFile reads the gateway's PID file. Missing file ⇒ (0, nil);
// garbage contents ⇒ (0, nil) — the FSM interprets 0 as "stopped"
// and that is the correct interpretation of an unreadable PID. Hard
// read failures (permission, IO) propagate as a non-nil error.
func readPIDFile(path string) (int, error) {
	body, err := os.ReadFile(path) //nolint:gosec // path is operator-configured under installRoot
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read pid file %s: %w", path, err)
	}
	s := strings.TrimSpace(string(body))
	if s == "" {
		return 0, nil
	}
	pid, perr := strconv.Atoi(s)
	if perr != nil {
		return 0, nil //nolint:nilerr // garbage pid is treated as 'stopped' per design
	}
	return pid, nil
}

// processAlive reports whether the given PID is currently running.
// Uses os.FindProcess + Signal(0) — the canonical liveness probe.
// Windows treats Signal(0) through os.Process as a probe via
// OpenProcess + exit-code check at the syscall layer.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall0()) == nil
}
