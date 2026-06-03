// Package jsonformat — whitebox test file.
//
// TestMain installs the goleak goroutine-leak gate for the entire
// internal/plugin/jsonformat package test suite. Any test that leaves a
// goroutine running after it returns will cause the suite to fail.
// (Phase 8 PATTERNS Pattern D; mirrors internal/plugin/pii/testmain_test.go.)
package jsonformat

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain is the entry point for all tests in the jsonformat package.
// goleak.VerifyTestMain runs m.Run() and then checks for goroutine leaks.
// The JSON-format steering hook is synchronous (no goroutines); this gate
// enforces that no future async regression goes undetected.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
