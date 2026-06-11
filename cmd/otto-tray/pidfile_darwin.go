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
// command name ending in "otto-gateway". Conservative: returns false
// on any error so a non-verifiable PID is never killed.
// gosec G204: args are static strings + strconv.Itoa(int); no tainted input.
func verifyGatewayIdentity(pid int, _ string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output() //nolint:gosec // args are static+integer
	if err != nil {
		return false
	}
	comm := strings.TrimSpace(string(out))
	return strings.HasSuffix(comm, "otto-gateway") || comm == "otto-gateway"
}
