//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestWrapperCancellationTerminatesWindowsProcessTree exercises the native
// taskkill /T path used when the tray deadline cancels a PowerShell collector.
func TestWrapperCancellationTerminatesWindowsProcessTree(t *testing.T) {
	root := t.TempDir()
	pidFile := filepath.Join(root, "child.pid")
	childScript := filepath.Join(root, "child.ps1")
	parentScript := filepath.Join(root, "parent.ps1")
	if err := os.WriteFile(childScript, []byte("Start-Sleep -Seconds 60\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := `param([string]$PidFile,[string]$Child)` + "\r\n" +
		`$p = Start-Process powershell.exe -ArgumentList @('-NoProfile','-File',$Child) -PassThru` + "\r\n" +
		`[System.IO.File]::WriteAllText($PidFile, [string]$p.Id)` + "\r\n" +
		`$p.WaitForExit()` + "\r\n"
	if err := os.WriteFile(parentScript, []byte(parent), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", parentScript, pidFile, childScript) //nolint:gosec // test-owned scripts
	detachProcessGroup(cmd)
	configureWrapperCancellation(cmd)
	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()

	var childPID int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(pidFile)
		if err == nil {
			childPID, err = strconv.Atoi(strings.TrimSpace(string(body)))
			if err != nil {
				t.Fatalf("parse child PID: %v", err)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if childPID == 0 {
		cancel()
		<-done
		t.Fatal("wrapper child did not start")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("wrapper root did not stop after cancellation")
	}

	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		probe := exec.Command("powershell.exe", "-NoProfile", "-Command", fmt.Sprintf("if (Get-Process -Id %d -ErrorAction SilentlyContinue) { exit 0 }; exit 1", childPID)) //nolint:gosec // childPID is parsed integer
		if err := probe.Run(); err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("wrapper child PID %d survived cancellation", childPID)
}
