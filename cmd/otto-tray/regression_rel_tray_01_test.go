//go:build darwin || windows

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestRegression_REL_TRAY_01_PIDIdentityUnchecked demonstrates that the tray's
// PID-trust path (readPIDFile + processAlive) accepts any live PID — including
// the test binary's own PID — as "the gateway". No name/cmdline identity check
// exists in the current source. Phase 15 fix: add process-name or cmdline
// verification before trusting the PID.
//
// Pre-fix observable: write pidfile containing os.Getpid() (the test binary),
// confirm processAlive returns true AND that no identity guard rejects it.
// Post-fix: the identity check should return false for a PID whose cmdline
// does not contain the gateway binary name.
func TestRegression_REL_TRAY_01_PIDIdentityUnchecked(t *testing.T) {
	t.Skip("REL-TRAY-01 (T-1): regression test — unskip in Phase 15 fix commit")

	// Write pidfile containing this test binary's own PID.
	tmp := t.TempDir()
	pidPath := filepath.Join(tmp, "gw.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatalf("write pidfile: %v", err)
	}

	// Read the PID back.
	pid, err := readPIDFile(pidPath)
	if err != nil {
		t.Fatalf("readPIDFile: %v", err)
	}
	if pid != os.Getpid() {
		t.Fatalf("readPIDFile: want %d, got %d", os.Getpid(), pid)
	}

	// Pre-fix: processAlive returns true (the test binary is alive).
	// There is no identity check — the tray would treat this test binary
	// as "the gateway", potentially killing it on Stop/Restart.
	alive := processAlive(pid)
	if !alive {
		t.Fatalf("processAlive(%d): expected true (test binary is alive), got false", pid)
	}

	// Demonstrate the missing identity guard:
	// After Phase 15, calling verifyGatewayIdentity(pid, wrapperBinPath) should
	// return false here (because our cmdline is "go test", not the gateway).
	// Pre-fix: no such function exists — this comment is the reproducer.
	// The assertion below documents the EXPECTED post-fix behavior:
	//   if ok := verifyGatewayIdentity(pid, "/path/to/otto-gateway"); ok {
	//       t.Fatal("identity check should have rejected the test binary as gateway")
	//   }
}
