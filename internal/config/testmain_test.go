// Package config — TestMain installs the goleak goroutine-leak gate
// for every test in the package. The config package is pure data
// parsing (env lookups, validators) with no goroutine surface, but
// the gate is installed prophylactically: if a future feature stashes
// a goroutine here (e.g. a config-file watcher), the test suite will
// surface the leak immediately rather than letting it ride into
// production. Mirrors internal/canonical/testmain_test.go.
//
// TRST-05 closure (Phase 9).

package config_test

import (
	"os"
	"testing"

	"go.uber.org/goleak"
)

// TestMain sets package-wide env so the secure-by-default boot
// validation (PII_REDACTION_ENABLED=true + PII_REDACTION_MODE=encrypt)
// doesn't fail every test that calls config.Load() without explicitly
// setting these vars. Tests that exercise the boot-error path can
// still t.Setenv("PII_ENCRYPT_KEY", "") to restore the empty value
// for their own scope.
func TestMain(m *testing.M) {
	// Stamp a deterministic encrypt key so the default-encrypt-mode
	// boot validation passes for every Load() call in this package.
	// t.Setenv inside individual tests can still override.
	if os.Getenv("PII_ENCRYPT_KEY") == "" {
		_ = os.Setenv("PII_ENCRYPT_KEY", "test-suite-default-encrypt-key")
	}
	goleak.VerifyTestMain(m)
}
