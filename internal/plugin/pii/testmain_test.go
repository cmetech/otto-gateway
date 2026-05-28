// Package pii — whitebox test file.
//
// TestMain installs the goleak goroutine-leak gate for the entire
// internal/plugin/pii package test suite. Any test that leaves a
// goroutine running after it returns will cause the suite to fail.
// (Phase 8 PATTERNS Pattern D; mirrors internal/plugin/testmain_test.go.)
package pii

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain is the entry point for all tests in the pii package.
// goleak.VerifyTestMain runs m.Run() and then checks for goroutine leaks.
// T-8-GO-LEAK mitigation: v1 PII walker + LoggingHook summary emission
// are synchronous; this gate enforces that no async hook regresses without
// the explicit dispensation of a goleak.IgnoreTopFunction option.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
