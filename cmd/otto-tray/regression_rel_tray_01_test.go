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
	// REL-TRAY-01 (T-1): verifyGatewayIdentity must reject any live PID
	// whose process name is not "otto-gateway". The test binary's own PID
	// passes processAlive but must be rejected by verifyGatewayIdentity.

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

	// processAlive returns true (the test binary is alive).
	alive := processAlive(pid)
	if !alive {
		t.Fatalf("processAlive(%d): expected true (test binary is alive), got false", pid)
	}

	// Post-fix: verifyGatewayIdentity must return false because the
	// test binary's process name is "go test" / the test runner, not "otto-gateway".
	if ok := verifyGatewayIdentity(pid, "/any/path/otto-gateway"); ok {
		t.Fatal("verifyGatewayIdentity: identity check should have rejected the test binary as gateway (process name is not otto-gateway)")
	}
}
