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
// command name that is exactly "gateway" or ends in
// "/gateway" (i.e. the basename is exactly "gateway").
// Conservative: returns false on any error so a non-verifiable PID
// is never killed.
//
// WR-06 fix (phase 15 review): the previous form
// `HasSuffix(comm, "gateway")` accepted basenames like
// `fake-gateway` or `not-gateway`. ps -o comm= on darwin
// returns either the bare executable name or its path basename, so
// we accept exact match against "gateway" or the path-suffixed
// variant "/gateway". The expectedPath parameter remains
// reserved for a future full-path comparison (IN-03); the current
// caller passes "" and we ignore it.
//
// Task B3 (de-brand): the gateway binary is renamed otto-gateway ->
// gateway, so the match target below moved from "otto-gateway" to
// "gateway" in lockstep.
// gosec G204: args are static strings + strconv.Itoa(int); no tainted input.
func verifyGatewayIdentity(pid int, _ string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output() //nolint:gosec // args are static+integer
	if err != nil {
		return false
	}
	comm := strings.TrimSpace(string(out))
	return isGatewayProcessName(comm)
}

// isGatewayProcessName reports whether comm (a `ps -o comm=` value)
// identifies the gateway binary: either the bare basename "gateway"
// or a path ending in "/gateway" (NOT just any suffix that ends with
// it, e.g. "fake-gateway" must not match). Extracted as a pure
// function so the match logic is unit-testable without spawning a
// real process.
func isGatewayProcessName(comm string) bool {
	if comm == "gateway" {
		return true
	}
	// Path-suffixed form: comm may be a full path; basename must be
	// exactly "gateway" (NOT just a suffix that ends with it).
	return strings.HasSuffix(comm, "/gateway")
}
