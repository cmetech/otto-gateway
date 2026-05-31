//go:build windows

package acp

import (
	"os/exec"
	"syscall"
)

// applyPgidAttr is a no-op on Windows. Windows has no POSIX process
// group concept; subprocess teardown semantics are handled by job
// objects / kernel close on the gateway exit. The stub keeps the
// cross-compile clean (260531-ra6 RA6-02 — `GOOS=windows go build ./...`
// must remain green).
func applyPgidAttr(cmd *exec.Cmd) {}

// killProcessGroup is a no-op on Windows for the same reason as
// applyPgidAttr. The signature mirrors the unix version so callers
// compile cleanly across platforms; cmd / pid / sig are intentionally
// ignored.
func killProcessGroup(pid int, sig syscall.Signal) error { return nil }
