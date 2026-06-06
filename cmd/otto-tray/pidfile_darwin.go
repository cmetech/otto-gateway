//go:build darwin

package main

import (
	"os"
	"syscall"
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
