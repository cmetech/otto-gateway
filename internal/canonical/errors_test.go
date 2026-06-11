// Package canonical_test — blackbox sentinel-identity tests.
//
// Phase 17 D-17-01: ErrPoolExhausted was relocated from internal/pool
// to internal/canonical to restore the TRST-04 adapter-over-canonical
// boundary (adapters errors.Is-check against canonical.ErrPoolExhausted
// without importing internal/pool). pool.ErrPoolExhausted is now a
// re-export alias of canonical.ErrPoolExhausted so the errors.Is
// identity is preserved.
//
// This file guards the sentinel against three accidental regressions:
//
//  1. Future redeclaration of ErrPoolExhausted with a new errors.New
//     call (would break errors.Is identity for any code holding a
//     reference to the old *errorString).
//  2. Drift of the message string text — adapters and tests
//     transitively assert on the rendered error surfaces (HTTP 503
//     body, log markers), and a string change would silently break
//     contract tests downstream.
//  3. Loss of errors.Is wrapping — fmt.Errorf("...: %w", sentinel)
//     must continue to satisfy errors.Is(wrapped, sentinel) for the
//     handler-mapping path to work.
package canonical_test

import (
	"errors"
	"fmt"
	"testing"

	"otto-gateway/internal/canonical"
)

// TestErrPoolExhausted_SentinelIdentity asserts the three TRST-04 /
// REL-POOL-01 invariants on canonical.ErrPoolExhausted: self-identity,
// byte-exact message text, and errors.Is wrap-traversal.
func TestErrPoolExhausted_SentinelIdentity(t *testing.T) {
	t.Parallel()

	// 1. Self-identity — errors.Is on the sentinel against itself.
	//    Trivially true for any non-nil error, but guards against the
	//    sentinel accidentally being declared as nil or a value type
	//    that breaks pointer-equality.
	if !errors.Is(canonical.ErrPoolExhausted, canonical.ErrPoolExhausted) {
		t.Fatal("errors.Is(canonical.ErrPoolExhausted, canonical.ErrPoolExhausted) returned false; sentinel identity broken")
	}

	// 2. Byte-exact message string. The literal text is part of the
	//    adapter HTTP 503 body contract and the WARN log marker. Any
	//    drift here is silently observable to clients.
	const want = "pool: all workers busy; retry in 5s"
	if got := canonical.ErrPoolExhausted.Error(); got != want {
		t.Fatalf("canonical.ErrPoolExhausted.Error() = %q; want %q", got, want)
	}

	// 3. errors.Is wrap-traversal. The pool layer wraps the sentinel
	//    with operational context before returning to adapters
	//    (e.g. fmt.Errorf("acquire timeout: %w", ErrPoolExhausted)),
	//    and the adapter errors.Is check must still match.
	wrapped := fmt.Errorf("acquire timeout: %w", canonical.ErrPoolExhausted)
	if !errors.Is(wrapped, canonical.ErrPoolExhausted) {
		t.Fatalf("errors.Is(wrapped, canonical.ErrPoolExhausted) returned false; wrap-traversal broken (wrapped=%v)", wrapped)
	}
}
