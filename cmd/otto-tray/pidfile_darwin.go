//go:build darwin

package main

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// processAlive uses the canonical POSIX kill(0) probe.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// verifyGatewayIdentity returns true if the process at pid has a
// command name that is exactly "otto-gateway" or ends in
// "/otto-gateway" (i.e. the basename is exactly "otto-gateway").
// Conservative: returns false on any error so a non-verifiable PID
// is never killed.
//
// WR-06 fix (phase 15 review): the previous form
// `HasSuffix(comm, "otto-gateway")` accepted basenames like
// `fake-otto-gateway` or `not-otto-gateway`. ps -o comm= on darwin
// returns either the bare executable name or its path basename, so
// we accept exact match against "otto-gateway" or the path-suffixed
// variant "/otto-gateway". The expectedPath parameter remains
// reserved for a future full-path comparison (IN-03); the current
// caller passes "" and we ignore it.
// gosec G204: args are static strings + strconv.Itoa(int); no tainted input.
func verifyGatewayIdentity(pid int, _ string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output() //nolint:gosec // args are static+integer
	if err != nil {
		return false
	}
	comm := strings.TrimSpace(string(out))
	if comm == "otto-gateway" {
		return true
	}
	// Path-suffixed form: comm may be a full path; basename must be
	// exactly "otto-gateway" (NOT just a suffix that ends with it).
	return strings.HasSuffix(comm, "/otto-gateway")
}
