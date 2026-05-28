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
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("github.com/DeRuina/timberjack.(*Logger).millRun"),
	)
}
