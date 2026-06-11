//go:build windows

package acp

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// applyPgidAttr is a no-op on Windows. Windows has no POSIX process
// group concept; subprocess teardown semantics are handled by the
// taskkill /T tree-walk in killProcessGroup below.
// The stub keeps the cross-compile clean (260531-ra6 RA6-02 —
// `GOOS=windows go build ./...` must remain green).
func applyPgidAttr(cmd *exec.Cmd) {}

// killProcessGroup kills the process tree rooted at pid on Windows.
//
// P-6 fix (REL-POOL-06): the previous stub returned nil unconditionally,
// orphaning kiro-cli grandchildren on gateway shutdown. Windows lacks
// POSIX pgroups, so we shell out to taskkill /T (tree-walk, terminates
// children of children) /F (force, no graceful WM_CLOSE). The 5-second
// context timeout bounds the operation so a hung taskkill cannot wedge
// the shutdown path.
//
// The sig parameter is part of the cross-platform signature (the unix
// analog forwards it to syscall.Kill(-pid, sig)); on Windows the
// taskkill /F is unconditional, so sig is intentionally ignored — there
// is no Windows analog of SIGTERM-vs-SIGKILL escalation that taskkill
// /T accepts as a flag.
//
// Errors are wrapped with the call site context so operators can
// distinguish taskkill failure (no such pid, permission denied) from
// the surrounding shutdown sequence.
func killProcessGroup(pid int, _ syscall.Signal) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	//nolint:gosec // args are static flags + integer pid (strconv.Itoa) — no operator/client input flows in
	cmd := exec.CommandContext(ctx, "taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("acp.pool.pgid: taskkill /T /F: %w", err)
	}
	return nil
}
