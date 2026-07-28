//go:build darwin

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestWrapperCancellationTerminatesDarwinProcessTree catches CommandContext's
// default root-only kill leaving a collector compression child behind after
// the tray's 210-second cancellation. It uses a real process group and child.
func TestWrapperCancellationTerminatesDarwinProcessTree(t *testing.T) {
	root := t.TempDir()
	pidFile := filepath.Join(root, "child.pid")
	script := filepath.Join(root, "parent.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 60 &\nprintf '%s\\n' \"$!\" > \"$1\"\nwait\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, script, pidFile) //nolint:gosec // test-owned fixed executable and path
	detachProcessGroup(cmd)
	configureWrapperCancellation(cmd)
	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()

	var childPID int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(pidFile)
		if err == nil {
			childPID, err = strconv.Atoi(strings.TrimSpace(string(body)))
			if err != nil {
				t.Fatalf("parse child PID: %v", err)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 {
		cancel()
		<-done
		t.Fatal("wrapper child did not start")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("wrapper root did not stop after cancellation")
	}

	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		err := syscall.Kill(childPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("wrapper child PID %d survived cancellation", childPID)
}
