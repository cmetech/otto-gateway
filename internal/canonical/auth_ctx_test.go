// Phase 8 Plan 08-02 Task 1 — canonical ctx-credential bridge tests.
//
// Mirrors internal/auth/auth_test.go's table-driven discipline +
// internal/plugin/request_id_test.go's typed-key contract style. The
// three tests pin the load-bearing distinction between "stamp absent"
// (ok=false) and "stamp present with empty value" (ok=true, token="") —
// AuthHook (Task 3) treats both as auth failure when Tokens is
// non-empty, but the distinction matters for future hooks.

package canonical_test

import (
	"context"
	"testing"

	"otto-gateway/internal/canonical"
)

// TestWithBearerToken_RoundTrip is the happy-path round-trip: a value
// stamped onto ctx via WithBearerToken is recovered via
// BearerTokenFromContext, with the presence boolean set to true.
func TestWithBearerToken_RoundTrip(t *testing.T) {
	ctx := canonical.WithBearerToken(context.Background(), "s3cret")
	tok, ok := canonical.BearerTokenFromContext(ctx)
	if !ok {
		t.Fatal("BearerTokenFromContext ok: got false, want true (value WAS stamped)")
	}
	if tok != "s3cret" {
		t.Errorf("BearerTokenFromContext token: got %q, want %q", tok, "s3cret")
	}
}

// TestBearerTokenFromContext_AbsentReturnsEmpty pins the "no stamp"
// shape: a bare ctx returns ("", false). Callers that don't care about
// presence still see the empty string and won't NPE.
func TestBearerTokenFromContext_AbsentReturnsEmpty(t *testing.T) {
	tok, ok := canonical.BearerTokenFromContext(context.Background())
	if ok {
		t.Errorf("BearerTokenFromContext ok: got true, want false (no stamp on bare ctx)")
	}
	if tok != "" {
		t.Errorf("BearerTokenFromContext token: got %q, want empty string", tok)
	}
}

// TestWithBearerToken_EmptyTokenStillStored pins the load-bearing
// distinction in this package's docstring: stamping an empty string is
// MEANINGFUL — it signals "adapter observed a missing credential"
// (distinct from "adapter never stamped"). BearerTokenFromContext
// returns (token="", ok=true) for the empty-stamp case.
//
// AuthHook (Task 3) treats both empty-stamp AND no-stamp as auth
// failure when its Tokens slice is non-empty, but the contract here
// preserves the distinction for future consumers (e.g., a hook that
// emits a "missing credential" warning only when the adapter ran
// auth-resolution but found nothing).
func TestWithBearerToken_EmptyTokenStillStored(t *testing.T) {
	ctx := canonical.WithBearerToken(context.Background(), "")
	tok, ok := canonical.BearerTokenFromContext(ctx)
	if !ok {
		t.Fatalf("BearerTokenFromContext ok: got false, want true (empty stamp is still a stamp)")
	}
	if tok != "" {
		t.Errorf("BearerTokenFromContext token: got %q, want empty string", tok)
	}
}
