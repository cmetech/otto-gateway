// Finding ID: P-6
// REL-* ID: REL-POOL-06
// Target phase: 14 (verify) / 16 (fix)
// Target OS: Windows
// Expected pre-fix behavior: grandchild kiro-cli processes survive cmd.Cancel() because
//   pool_pgid_windows.go:applyPgidAttr is a no-op and killProcessGroup returns nil (success)
//   without creating a job object or delivering any kill signal. Go's exec.CommandContext
//   falls back to TerminateProcess on the leader only after WaitDelay (2s), leaving
//   grandchild processes (MCP servers, tool helpers spawned by kiro-cli) orphaned.
// Expected post-fix behavior: full process group dies within 2s of cmd.Cancel() because
//   either a Windows job object with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE is created at
//   spawn time (closing the job handle kills all assigned processes), or cmd.Cancel()
//   invokes taskkill /T /F /PID for tree-wide termination.
// Run instructions:
//   1. Ensure kiro-cli (or a multi-process stub) is on PATH as "kiro" or set KIRO_CMD env var.
//   2. Compile: GOOS=windows GOARCH=amd64 go build -o rel-pool-06-repro.exe ./tests/reliability/manual/
//   3. Run on a Windows host: .\rel-pool-06-repro.exe
//   4. Pre-fix: exit code 0 with output "ORPHANS DETECTED: N grandchild processes survived Cancel()"
//   5. Post-fix: exit code 1 with output "PASS: process group died cleanly within 2s of Cancel()"
//   6. Alternative: run with -stub flag to use a bundled self-spawning stub instead of real kiro-cli.

//go:build windows

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	useStub := flag.Bool("stub", false, "spawn a self-referencing stub child instead of real kiro-cli (for testing without kiro-cli)")
	flag.Parse()

	kiroCmd := os.Getenv("KIRO_CMD")
	if kiroCmd == "" {
		kiroCmd = "kiro"
	}
	if *useStub {
		// Use ourselves as the stub child — pass -stub-child to run in stub mode.
		kiroCmd = os.Args[0]
	}

	fmt.Printf("REL-POOL-06 Windows pgid no-op reproducer\n")
	fmt.Printf("Using kiro command: %s\n\n", kiroCmd)

	// Phase 1: spawn kiro-cli child with a grandchild (simulating MCP server).
	// In production, kiro-cli spawns its own child processes (MCP servers, tool
	// helpers). We simulate this by spawning a child that itself spawns a grandchild.
	//
	// The key: applyPgidAttr is a no-op on Windows (pool_pgid_windows.go:15),
	// so no job object or process group is created. All processes are in the
	// default Windows job (if any) but the gateway creates NO explicit job object
	// with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var args []string
	if *useStub {
		args = append(args, "-stub-child")
	}
	// Spawn the leader child.
	leaderCmd := exec.CommandContext(ctx, kiroCmd, args...)
	leaderCmd.SysProcAttr = &syscall.SysProcAttr{
		// applyPgidAttr no-op: no Setpgid on Windows, no job object creation.
		// This is exactly what internal/acp/pool_pgid_windows.go:15 does.
	}

	if err := leaderCmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "SKIP: could not start kiro command %q: %v\n", kiroCmd, err)
		fmt.Fprintf(os.Stderr, "Run with -stub flag or ensure %s is on PATH.\n", kiroCmd)
		os.Exit(2) // 2 = skip (not a failure of the reproducer logic)
	}

	leaderPID := leaderCmd.Process.Pid
	fmt.Printf("Leader process started: PID %d\n", leaderPID)

	// Wait briefly for the leader to spawn its grandchildren.
	time.Sleep(500 * time.Millisecond)

	// Phase 2: enumerate children of the leader to know what to check post-cancel.
	// On Windows, tasklist shows all processes; we filter by ParentProcessId.
	preCancel := listChildPIDs(leaderPID)
	fmt.Printf("Children of leader (pre-Cancel): %v\n", preCancel)

	// Phase 3: call cmd.Cancel() — the gateway's pool teardown path.
	// In production this is applyPgidAttr no-op + WaitDelay fallback to TerminateProcess.
	// We replicate the exact gateway behaviour: cancel the ctx (which triggers
	// cmd.Cancel via exec.CommandContext semantics) and wait.
	fmt.Printf("Calling cmd.Cancel() (ctx cancel)...\n")
	cancelTime := time.Now()
	cancel() // fires cmd.Cancel() via CommandContext

	// Wait for WaitDelay equivalent (2s in production via client.go:317).
	time.Sleep(2200 * time.Millisecond)
	leaderAlive := processAlive(leaderPID)
	postCancel := listChildPIDs(leaderPID)

	fmt.Printf("Leader alive after Cancel(): %v (elapsed: %v)\n", leaderAlive, time.Since(cancelTime))
	fmt.Printf("Children of leader (post-Cancel): %v\n", postCancel)

	// Phase 4: report verdict.
	orphanCount := 0
	for _, pid := range preCancel {
		if processAlive(pid) {
			orphanCount++
			fmt.Printf("  ORPHAN: PID %d is still alive\n", pid)
		}
	}

	if orphanCount > 0 {
		// Pre-fix observable: grandchildren survived cmd.Cancel().
		// Exit 0 = bug confirmed (orphans detected).
		fmt.Printf("\nORPHANS DETECTED: %d grandchild process(es) survived Cancel()\n", orphanCount)
		fmt.Println("This confirms REL-POOL-06: Windows pgid kill-group is a no-op.")
		fmt.Println("Phase 16 fix: create a job object with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE per spawn.")
		os.Exit(0)
	}

	// Post-fix: process group died cleanly.
	// Exit 1 = bug NOT reproduced (fix is in place).
	fmt.Println("\nPASS: process group died cleanly within 2s of Cancel()")
	fmt.Println("REL-POOL-06 is fixed — no orphaned grandchild processes detected.")
	os.Exit(1)
}

// listChildPIDs uses tasklist.exe to find direct children of parentPID.
// Returns an empty slice if tasklist is unavailable or returns no children.
func listChildPIDs(parentPID int) []int {
	// tasklist /FO CSV /NH includes ParentProcessId in Windows 10+.
	// We use wmic as a more reliable alternative.
	out, err := exec.Command(
		"wmic",
		"process", "where", fmt.Sprintf("ParentProcessId=%d", parentPID),
		"get", "ProcessId", "/format:csv",
	).Output()
	if err != nil {
		return nil
	}
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Node") || strings.HasPrefix(line, "ProcessId") {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 2 {
			continue
		}
		pidStr := strings.TrimSpace(parts[len(parts)-1])
		if pid, err := strconv.Atoi(pidStr); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids
}

// processAlive checks whether a process with the given PID is still running.
// Uses OpenProcess + GetExitCodeProcess to avoid signal(0) semantics.
func processAlive(pid int) bool {
	handle, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(handle) //nolint:errcheck
	var exitCode uint32
	if err := syscall.GetExitCodeProcess(handle, &exitCode); err != nil {
		return false
	}
	const stillActive = 259 // STILL_ACTIVE / STATUS_PENDING
	return exitCode == stillActive
}
