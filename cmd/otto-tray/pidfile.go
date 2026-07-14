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
	body, err := os.ReadFile(path) //nolint:gosec // path is operator-configured under GW_HOME
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

// processAlive is implemented per-OS in pidfile_darwin.go and
// pidfile_windows.go. Windows does not honor Signal(0) (it returns
// ErrUnsupported for everything but Kill), so the per-OS split is
// load-bearing — the earlier shared codepath reported every Windows
// gateway PID as dead.
