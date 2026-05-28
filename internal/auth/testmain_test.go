// Package auth — TestMain installs the goleak goroutine-leak gate for
// every test in the package. The auth package itself is goroutine-free
// (bearer-token compare + IP allowlist evaluation are pure), but the
// middleware integration tests exercise net/http handlers that can
// spawn goroutines on the request path; goleak catches any that fail
// to exit cleanly. Mirrors internal/canonical/testmain_test.go.
//
// TRST-05 closure (Phase 9): handler-level test packages must wire
// goleak.VerifyTestMain. To suppress a known-benign goroutine, add a
// goleak.IgnoreTopFunction option — never suppress without diagnosing
// the root cause.

package auth_test

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
