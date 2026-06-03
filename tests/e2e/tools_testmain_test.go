//go:build e2e

// tools_testmain_test.go — placeholder file for the Phase 6 Plan 06-05
// iteration-3 fix to MEDIUM #6 (fake-kiro binary lifetime).
//
// LIFETIME CONTRACT
// =================
//
// The fake-kiro-cli binary used by tools_*_test.go is compiled exactly once
// per `go test -tags e2e ./tests/e2e/...` invocation. The compilation step
// lives inside the existing TestMain in e2e_test.go (extending it directly —
// Go forbids two TestMain functions in the same test package). The binary
// path is stored in the package-level fakeKiroBinaryPath var declared in
// tools_fixtures.go. Lifetime is package-scoped: the binary is created at
// TestMain entry and deleted at TestMain exit. Every subtest that calls
// FakeKiro sees the SAME absolute path.
//
// Why not sync.Once + t.TempDir() (iteration-2 MEDIUM #6 bug)?
//
//	sync.Once + t.TempDir() compiled the binary inside the first test's temp
//	dir. That temp dir gets cleaned up when the first test completes. Every
//	subsequent test that called FakeKiro then got a cached path to a binary
//	that no longer existed on disk → exec.ErrNotFound on every subsequent
//	subtest.
//
// Why os.TempDir() with a per-pid suffix?
//
//   - os.TempDir() is shared across the test process, not per-test, so the
//     binary survives subtest teardown.
//   - The per-pid suffix (`fake-kiro-cli-<pid>`) makes the path unique per
//     `go test` invocation. Parallel `go test` runs (CI matrix) don't clobber
//     each other's binaries.
//   - TestMain defers `os.Remove(fakeKiroBinaryPath)` so the file is cleaned
//     up on m.Run() exit.
//
// Smoke test:
//
//	TestFakeKiro_BinaryExistsAfterMultipleSubtests (in tools_fixtures.go's
//	companion test space, defined below) proves the binary path is valid
//	across two sequential t.Run subtests. If the iteration-2 bug were to
//	recur, the second subtest would fail with "binary not found".
package e2e_test

import (
	"os"
	"testing"
)

// TestFakeKiro_BinaryExistsAfterMultipleSubtests proves the iteration-3 fix
// to MEDIUM #6 — the fake-kiro binary path survives across sequential t.Run
// subtests. Each subtest calls FakeKiro independently and stats the returned
// cmd path; both must succeed.
//
// If MEDIUM #6 regresses (e.g. someone switches to sync.Once + t.TempDir()),
// the second subtest will fail because the binary is gone after the first
// subtest's temp dir cleanup. The test passes ONLY when the binary lifetime
// is package-scoped.
func TestFakeKiro_BinaryExistsAfterMultipleSubtests(t *testing.T) {
	gateOrSkip(t)

	t.Run("first", func(t *testing.T) {
		cmd, _ := FakeKiro(t, Script{})
		if cmd == "" {
			t.Fatal("FakeKiro returned empty cmd path")
		}
		if _, err := os.Stat(cmd); err != nil {
			t.Fatalf("fake-kiro binary missing in first subtest: %v (path=%q)", err, cmd)
		}
	})

	t.Run("second", func(t *testing.T) {
		cmd, _ := FakeKiro(t, Script{})
		if cmd == "" {
			t.Fatal("FakeKiro returned empty cmd path")
		}
		if _, err := os.Stat(cmd); err != nil {
			t.Fatalf("fake-kiro binary missing in second subtest: %v (path=%q) — MEDIUM #6 lifetime regression", err, cmd)
		}
	})
}
