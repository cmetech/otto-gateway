//go:build darwin || linux

package acp

import (
	"os/exec"
	"syscall"
)

// applyPgidAttr arranges for the child process started from cmd to be the
// leader of its OWN process group (its PGID equals its PID). 260531-ra6
// Issue 2 (RA6-02).
//
// Why: Go's exec.Cmd default places children in the parent's process
// group. SIGTERM/SIGKILL delivered to the gateway therefore does NOT
// propagate to kiro-cli on macOS/Linux. On hard exit of the gateway,
// kiro-cli is reparented to init (pid 1) and outlives the gateway.
// Promoting the child to its own pgrp leader means exec.CommandContext's
// existing SIGKILL-on-ctx-cancel is delivered to the kiro-cli leader,
// who is the root of the kiro process tree — any grandchildren go too
// when killProcessGroup is used at shutdown.
//
// Defensive: if cmd.SysProcAttr is non-nil (a future spawn site may set
// other attributes), overlay Setpgid rather than overwriting the whole
// struct. Today client.go:284 is the only spawn site and it leaves
// SysProcAttr nil, but the guard documents intent and is free.
func applyPgidAttr(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		return
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup sends sig to the process group whose leader is pid.
// Implemented as syscall.Kill(-pid, sig) per the POSIX convention
// (negative pid means "deliver to the pgrp identified by |pid|").
//
// Exposed for future shutdown paths that need to group-kill a kiro-cli
// tree explicitly; the current Task 2 wiring relies on the implicit
// SIGKILL exec.CommandContext already issues on ctx cancellation, now
// delivered to the leader (whose own process group equals its pid).
func killProcessGroup(pid int, sig syscall.Signal) error {
	return syscall.Kill(-pid, sig)
}
