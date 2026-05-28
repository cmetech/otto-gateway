// Package canonical — whitebox test file.
//
// TestMain installs the goleak goroutine-leak gate for the entire
// canonical package test suite. Any test that leaves a goroutine
// running after it returns will cause the suite to fail.
// (Phase 8 PATTERNS Pattern D; mirrors internal/plugin/testmain_test.go
// and internal/engine/testmain_test.go.)
//
// Added by Phase 8 Plan 08-02 Task 1 alongside auth_ctx_test.go — the
// canonical package previously had no testmain because all canonical
// tests were pure-data round-trips with no goroutine surface; Phase 8
// hooks may stash channels / cancelable contexts here in the future,
// so the gate is established now.

package canonical_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain is the entry point for all tests in the canonical_test
// package. goleak.VerifyTestMain runs m.Run() and then checks for
// goroutine leaks. To suppress a known-benign goroutine, add a
// goleak.IgnoreTopFunction option. Do NOT suppress without diagnosing
// the root cause.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
