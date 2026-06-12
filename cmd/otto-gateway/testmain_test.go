// Package main — TestMain installs the goleak goroutine-leak gate for
// every test in the cmd/otto-gateway package. This is the binary's
// entry point; integration-level tests exercise newApp() which wires
// the pool, engine, server, and admin handler. Any goroutine those
// constructors spawn must exit on the cleanup() closure or the suite
// fails — protecting against a regression where adding a new
// component leaks goroutines on each test boot.
//
// TRST-05 closure (Phase 9). Mirrors internal/canonical/testmain_test.go.
//
// Known suppressions: timberjack's millRun goroutine background-prunes
// rotated logs; it exits cleanly on Logger.Close() but the goleak
// snapshot can race with that. Ignore by top-function name. (Same
// pattern as internal/admin/tail_timberjack_test.go.)

package main

import (
	"os"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	// Stamp PII_ENCRYPT_KEY so the secure-by-default encrypt mode
	// (PII_REDACTION_MODE=encrypt, PII_REDACTION_ENABLED=true) doesn't
	// boot-fail in cmd tests that call config.Load() without setting
	// it explicitly. The production install path generates a random
	// key; this is the test stand-in.
	if os.Getenv("PII_ENCRYPT_KEY") == "" {
		_ = os.Setenv("PII_ENCRYPT_KEY", "test-suite-default-encrypt-key")
	}
	// Phase 18-01 D-18-02: KIRO_CMD now passes through exec.LookPath in
	// config.Load(). Stamp it to "go" — guaranteed in PATH on any Go CI
	// runner — so default-load paths don't boot-fail. Tests that
	// exercise the KIRO_CMD-not-found path can still override via t.Setenv.
	if os.Getenv("KIRO_CMD") == "" {
		_ = os.Setenv("KIRO_CMD", "go")
	}
	goleak.VerifyTestMain(
		m,
		goleak.IgnoreTopFunction("github.com/DeRuina/timberjack.(*Logger).millRun"),
	)
}
