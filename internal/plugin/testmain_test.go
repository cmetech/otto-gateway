// Package plugin — whitebox test file.
//
// TestMain installs the goleak goroutine-leak gate for the entire plugin
// package test suite. Any test that leaves a goroutine running after it
// returns will cause the suite to fail. (Phase 8 PATTERNS Pattern D;
// mirrors internal/engine/testmain_test.go.)
package plugin

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain is the entry point for all tests in the plugin package.
// goleak.VerifyTestMain runs m.Run() and then checks for goroutine leaks.
// To suppress a known-benign goroutine, add a goleak.IgnoreTopFunction option.
// Do NOT suppress without diagnosing the root cause.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
