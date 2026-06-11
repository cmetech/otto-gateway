// Package acp_test — discoverability stub for REL-POOL-06 (P-6, Medium).
// The actual reproducer is a standalone Windows-only Go program at:
//
//	tests/reliability/manual/REL-POOL-06-repro.go
//
// This stub ensures `go test -run TestRegression_REL_ -v ./...` lists the
// finding alongside the other Pool/ACP regression tests for discoverability.
package acp_test

import "testing"

// TestRegression_REL_POOL_06_WindowsPgidNoop is a discoverability stub for the
// Windows pgid kill-group no-op finding (P-6, Medium, REL-POOL-06).
//
// The failure mode is Windows-only and requires a real kiro-cli process tree
// to observe; it cannot be exercised in go test on Linux/macOS CI runners.
// The actual runnable manual reproducer lives at:
//
//	tests/reliability/manual/REL-POOL-06-repro.go
//
// Run it on a Windows host:
//
//	GOOS=windows GOARCH=amd64 go build -o repro.exe ./tests/reliability/manual/
//	.\repro.exe
//
// Phase 16's fix: create a Windows job object with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
// per spawn, or implement taskkill /T /F /PID as cmd.Cancel fallback.
func TestRegression_REL_POOL_06_WindowsPgidNoop(t *testing.T) {
	t.Skip("REL-POOL-06 (P-6): manual validation required — run tests/reliability/manual/REL-POOL-06-repro.go on Windows; fix shipped in pool_pgid_windows.go killProcessGroup (taskkill /T /F)")
}
