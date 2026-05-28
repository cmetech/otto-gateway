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
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
