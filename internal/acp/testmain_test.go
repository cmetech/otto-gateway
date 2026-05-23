// Package acp — whitebox test file.
// TestMain installs the goleak goroutine-leak gate for the entire acp package test suite.
// D-18: this file is package acp (whitebox) so it covers both whitebox and blackbox test files
// in the same directory. D-10 / TRST-05.
package acp

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain is the entry point for all tests in the acp package.
// goleak.VerifyTestMain runs m.Run() and then checks for goroutine leaks.
// Any test that leaves a goroutine running after it returns will cause the suite to fail.
// To suppress a known-benign goroutine: goleak.VerifyTestMain(m, goleak.IgnoreTopFunction("..."))
// Do NOT suppress without diagnosing the root cause.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
