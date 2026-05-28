// Phase 8 Plan 08-05 Task 1 — Wave 0 contract tests for the FOUR-hook
// chain Filter behavior (slice 1 shipped the implementation; this slice
// proves it works end-to-end against the four real hook constructors).
//
// Tests:
//   - 12: Empty allowlist passthrough (full four-hook chain).
//   - 13: Known allowlist preserves REGISTRATION order (D-02 + SC5
//          — allowlist order does NOT rewrite execution order).
//   - 14: Unknown allowlist name returns error containing literal
//          substring "unknown hook" AND the offending name.
//   - 15: Single-hook allowlist filters out everything else.
//   - 16: LoggingHook on both Pre and Post → one allowlist entry
//          keeps both placements.
//
// Uses external test package `plugin_test` so we depend on EXPORTED
// hook constructors only (no unexported test-package internals).
package plugin_test

import (
	"strings"
	"testing"

	"otto-gateway/internal/engine"
	"otto-gateway/internal/plugin"
	"otto-gateway/internal/plugin/pii"
)

// buildFullChain constructs the four-hook chain as main.go will (D-04
// order: RequestID → Auth → PII → Logging on Pre; LoggingHook only on
// Post). Used as the input to every Filter test below.
func buildFullChain() plugin.Chain {
	logging := &plugin.LoggingHook{}
	return plugin.Chain{
		Pre: []engine.PreHook{
			&plugin.RequestIDHook{},
			&plugin.AuthHook{},
			&pii.PIIRedactionHook{
				Recognizers: pii.Recognizers,
				Enabled:     true,
				Mode:        "replace",
			},
			logging,
		},
		Post: []engine.PostHook{
			logging,
		},
	}
}

func TestChainFilter_EmptyAllowlist_Passthrough(t *testing.T) {
	c := buildFullChain()
	filtered, err := c.Filter(nil)
	if err != nil {
		t.Fatalf("Filter(nil): %v", err)
	}
	if len(filtered.Pre) != 4 {
		t.Errorf("Pre len: got %d, want 4 (passthrough)", len(filtered.Pre))
	}
	if len(filtered.Post) != 1 {
		t.Errorf("Post len: got %d, want 1 (passthrough)", len(filtered.Post))
	}
}

func TestChainFilter_KnownAllowlist_PreservesRegistrationOrder(t *testing.T) {
	c := buildFullChain()
	// DELIBERATE allowlist-order != registration-order. The chain has
	// [RequestIDHook, AuthHook, PIIRedactionHook, LoggingHook] on Pre;
	// allowlist asks for LoggingHook FIRST then RequestIDHook. The
	// filtered chain MUST preserve registration order: RequestIDHook
	// first, then LoggingHook (D-02 + SC5).
	filtered, err := c.Filter([]string{"LoggingHook", "RequestIDHook"})
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(filtered.Pre) != 2 {
		t.Fatalf("Pre len: got %d, want 2", len(filtered.Pre))
	}

	type namer interface{ Name() string }
	first, ok := filtered.Pre[0].(namer)
	if !ok {
		t.Fatalf("Pre[0] does not implement Name()")
	}
	second, ok := filtered.Pre[1].(namer)
	if !ok {
		t.Fatalf("Pre[1] does not implement Name()")
	}
	if first.Name() != "RequestIDHook" {
		t.Errorf("Pre[0].Name(): got %q, want %q (registration order)", first.Name(), "RequestIDHook")
	}
	if second.Name() != "LoggingHook" {
		t.Errorf("Pre[1].Name(): got %q, want %q (registration order)", second.Name(), "LoggingHook")
	}
}

func TestChainFilter_UnknownAllowlist_ErrorContainsUnknownHook(t *testing.T) {
	c := buildFullChain()
	_, err := c.Filter([]string{"RequestIDHook", "BogusHook"})
	if err == nil {
		t.Fatal("expected error for unknown hook in allowlist")
	}
	msg := err.Error()
	if !strings.Contains(msg, "unknown hook") {
		t.Errorf("error should contain literal substring 'unknown hook'; got %v", err)
	}
	if !strings.Contains(msg, "BogusHook") {
		t.Errorf("error should name the offending hook 'BogusHook'; got %v", err)
	}
}

func TestChainFilter_SingleHookAllowlist(t *testing.T) {
	c := buildFullChain()
	filtered, err := c.Filter([]string{"PIIRedactionHook"})
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(filtered.Pre) != 1 {
		t.Fatalf("Pre len: got %d, want 1", len(filtered.Pre))
	}
	type namer interface{ Name() string }
	first, ok := filtered.Pre[0].(namer)
	if !ok {
		t.Fatalf("Pre[0] does not implement Name()")
	}
	if first.Name() != "PIIRedactionHook" {
		t.Errorf("Pre[0].Name(): got %q, want PIIRedactionHook", first.Name())
	}
	// PIIRedactionHook is Pre-only, so the Post slice MUST be empty.
	if len(filtered.Post) != 0 {
		t.Errorf("Post len: got %d, want 0 (PIIRedactionHook is Pre-only)", len(filtered.Post))
	}
}

func TestChainFilter_LoggingHookInBothPreAndPost_AllowlistKeepsBoth(t *testing.T) {
	c := buildFullChain()
	// A single allowlist entry "LoggingHook" must keep the hook on BOTH
	// Pre and Post (the entry matches by name regardless of placement).
	filtered, err := c.Filter([]string{"LoggingHook"})
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(filtered.Pre) != 1 {
		t.Errorf("Pre len: got %d, want 1 (LoggingHook on Pre)", len(filtered.Pre))
	}
	if len(filtered.Post) != 1 {
		t.Errorf("Post len: got %d, want 1 (LoggingHook on Post)", len(filtered.Post))
	}
}
