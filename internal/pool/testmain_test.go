// Package pool — whitebox test file.
// TestMain installs the goleak goroutine-leak gate for the entire pool package test suite.
// Modelled after internal/acp/testmain_test.go (D-18 — whitebox testmain covers both
// blackbox `package pool_test` and whitebox files in the same directory).
package pool

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain is the entry point for all tests in the pool package.
// goleak.VerifyTestMain runs m.Run() and then checks for goroutine leaks.
// Any test that leaves a goroutine running after it returns will cause the suite to fail.
// To suppress a known-benign goroutine: goleak.VerifyTestMain(m, goleak.IgnoreTopFunction("..."))
// Do NOT suppress without diagnosing the root cause.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
