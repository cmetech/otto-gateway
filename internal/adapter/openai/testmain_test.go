// Package openai — whitebox test file.
// TestMain installs the goleak goroutine-leak gate for the entire
// openai adapter package test suite. Any test that leaves a
// goroutine running after it returns will cause the suite to fail.
// (TRST-05 — particularly important for Plan 03-02's SSE handler
// which the goleak gate proves is leak-free by construction.)
package openai

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain is the entry point for all tests in the openai package.
// goleak.VerifyTestMain runs m.Run() and then checks for goroutine
// leaks. To suppress a known-benign goroutine, add a
// goleak.IgnoreTopFunction option. Do NOT suppress without diagnosing
// the root cause.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
