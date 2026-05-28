// Package plugin — Wave 0 AuthHook tests (Phase 8 Plan 08-02 Task 2).
//
// These tests scaffold the expectations for plugin.AuthHook BEFORE the
// implementation lands in Task 3. All tests in this file are expected
// to FAIL with `undefined: AuthHook` until auth.go is written; the
// Wave 0 RED state proves Task 3's GREEN delta.
//
// Whitebox (package plugin) so we can hand-construct fakes that satisfy
// engine.PreHook without going through the production hook
// implementations. Mirrors chain_test.go's whitebox discipline.
//
// Tests pin:
//   - Empty-Tokens passthrough (Node parity — bearer.go:35-38).
//   - Valid-token passthrough via canonical.WithBearerToken-stamped ctx.
//   - Invalid / missing / empty-string short-circuit produces a
//     *canonical.ChatResponse with StopReason == canonical.StopError and
//     a text content part containing "Invalid or missing API key".
//   - Multi-token loop matches ANY valid token.
//   - Name() reports "AuthHook" for chain.Filter discovery.
//   - Describe() exposes token_count but NEVER tokens / token (T-8-LEAK).
//   - Source-level audit of auth.go: subtle.ConstantTimeCompare present,
//     no `==` on token bytes (belt-and-suspenders T-8-AUTH guard).

package plugin

import (
	"context"
	"os"
	"strings"
	"testing"

	"otto-gateway/internal/canonical"
)

// authErrorBody extracts the short-circuit envelope's user-facing text
// for assertion. Nil-safe so a test that wires AuthHook incorrectly
// fails on the test's own t.Fatal rather than on a nil-deref panic.
// This helper becomes the shared assertion seam with the per-surface
// e2e tests in slice 5.
func authErrorBody(t *testing.T, resp *canonical.ChatResponse) string {
	t.Helper()
	if resp == nil {
		t.Fatal("authErrorBody: response is nil (AuthHook did not short-circuit)")
	}
	if len(resp.Message.Content) == 0 {
		t.Fatalf("authErrorBody: response.Message.Content is empty (envelope shape broken)")
	}
	return resp.Message.Content[0].Text
}

// TestAuthHook_EmptyTokens_Passthrough — when Tokens is nil OR empty,
// the hook is a passthrough (Node parity; matches bearer.go:35-38).
// Asserted as two subtests because the empty-vs-nil distinction is a
// common bug source.
func TestAuthHook_EmptyTokens_Passthrough(t *testing.T) {
	ctx := canonical.WithBearerToken(context.Background(), "anything")
	req := &canonical.ChatRequest{}

	t.Run("nil_tokens", func(t *testing.T) {
		h := &AuthHook{Tokens: nil}
		resp, err := h.Before(ctx, req)
		if err != nil {
			t.Fatalf("err: got %v, want nil (auth disabled)", err)
		}
		if resp != nil {
			t.Fatalf("resp: got %+v, want nil (auth disabled — no short-circuit)", resp)
		}
	})

	t.Run("empty_tokens", func(t *testing.T) {
		h := &AuthHook{Tokens: []string{}}
		resp, err := h.Before(ctx, req)
		if err != nil {
			t.Fatalf("err: got %v, want nil (auth disabled)", err)
		}
		if resp != nil {
			t.Fatalf("resp: got %+v, want nil (auth disabled — no short-circuit)", resp)
		}
	})
}

// TestAuthHook_ValidToken_Passthrough — valid token on ctx → passthrough.
func TestAuthHook_ValidToken_Passthrough(t *testing.T) {
	ctx := canonical.WithBearerToken(context.Background(), "s3cret")
	req := &canonical.ChatRequest{}
	h := &AuthHook{Tokens: []string{"s3cret"}}

	resp, err := h.Before(ctx, req)
	if err != nil {
		t.Fatalf("err: got %v, want nil", err)
	}
	if resp != nil {
		t.Fatalf("resp: got %+v, want nil (valid token should NOT short-circuit)", resp)
	}
}

// TestAuthHook_InvalidToken_ShortCircuit — wrong token on ctx →
// short-circuit with canonical-shape error envelope.
func TestAuthHook_InvalidToken_ShortCircuit(t *testing.T) {
	ctx := canonical.WithBearerToken(context.Background(), "wrong")
	req := &canonical.ChatRequest{}
	h := &AuthHook{Tokens: []string{"s3cret"}}

	resp, err := h.Before(ctx, req)
	if err != nil {
		t.Fatalf("err: got %v, want nil (short-circuit, not error)", err)
	}
	if resp == nil {
		t.Fatal("resp: got nil, want non-nil short-circuit envelope")
	}
	if resp.StopReason != canonical.StopError {
		t.Errorf("StopReason: got %v, want canonical.StopError", resp.StopReason)
	}
	body := authErrorBody(t, resp)
	if !strings.Contains(body, "Invalid or missing API key") {
		t.Errorf("body: got %q, want contains %q", body, "Invalid or missing API key")
	}
}

// TestAuthHook_MissingToken_ShortCircuit — no stamp on ctx →
// short-circuit (same shape as invalid-token).
func TestAuthHook_MissingToken_ShortCircuit(t *testing.T) {
	ctx := context.Background() // no WithBearerToken stamp
	req := &canonical.ChatRequest{}
	h := &AuthHook{Tokens: []string{"s3cret"}}

	resp, err := h.Before(ctx, req)
	if err != nil {
		t.Fatalf("err: got %v, want nil", err)
	}
	if resp == nil {
		t.Fatal("resp: got nil, want non-nil short-circuit envelope")
	}
	if resp.StopReason != canonical.StopError {
		t.Errorf("StopReason: got %v, want canonical.StopError", resp.StopReason)
	}
	body := authErrorBody(t, resp)
	if !strings.Contains(body, "Invalid or missing API key") {
		t.Errorf("body: got %q, want contains %q", body, "Invalid or missing API key")
	}
}

// TestAuthHook_EmptyStringToken_ShortCircuit — adapter stamped an empty
// string (header was missing in the wire request). AuthHook treats this
// the same as the no-stamp case per RESEARCH Code Example 1:
// `if !ok || provided == ""`.
func TestAuthHook_EmptyStringToken_ShortCircuit(t *testing.T) {
	ctx := canonical.WithBearerToken(context.Background(), "")
	req := &canonical.ChatRequest{}
	h := &AuthHook{Tokens: []string{"s3cret"}}

	resp, err := h.Before(ctx, req)
	if err != nil {
		t.Fatalf("err: got %v, want nil", err)
	}
	if resp == nil {
		t.Fatal("resp: got nil, want non-nil short-circuit (empty stamp == missing credential)")
	}
	if resp.StopReason != canonical.StopError {
		t.Errorf("StopReason: got %v, want canonical.StopError", resp.StopReason)
	}
}

// TestAuthHook_MultipleTokens_AcceptsAnyMatch — Tokens loop accepts ANY
// match; non-match short-circuits.
func TestAuthHook_MultipleTokens_AcceptsAnyMatch(t *testing.T) {
	req := &canonical.ChatRequest{}
	h := &AuthHook{Tokens: []string{"a", "b", "c"}}

	t.Run("middle_token_accepted", func(t *testing.T) {
		ctx := canonical.WithBearerToken(context.Background(), "b")
		resp, err := h.Before(ctx, req)
		if err != nil {
			t.Fatalf("err: got %v, want nil", err)
		}
		if resp != nil {
			t.Fatalf("resp: got %+v, want nil (token 'b' should match Tokens[1])", resp)
		}
	})

	t.Run("first_token_accepted", func(t *testing.T) {
		ctx := canonical.WithBearerToken(context.Background(), "a")
		resp, err := h.Before(ctx, req)
		if err != nil || resp != nil {
			t.Fatalf("err=%v resp=%+v: want (nil, nil) for token 'a'", err, resp)
		}
	})

	t.Run("last_token_accepted", func(t *testing.T) {
		ctx := canonical.WithBearerToken(context.Background(), "c")
		resp, err := h.Before(ctx, req)
		if err != nil || resp != nil {
			t.Fatalf("err=%v resp=%+v: want (nil, nil) for token 'c'", err, resp)
		}
	})

	t.Run("unknown_token_rejected", func(t *testing.T) {
		ctx := canonical.WithBearerToken(context.Background(), "d")
		resp, err := h.Before(ctx, req)
		if err != nil {
			t.Fatalf("err: got %v, want nil", err)
		}
		if resp == nil {
			t.Fatal("resp: got nil, want short-circuit for unknown token 'd'")
		}
	})
}

// TestAuthHook_Name — chain.Filter discovers the hook by its declared
// name; renaming the type without updating Name() would silently break
// ENABLED_HOOKS=AuthHook in main.go.
func TestAuthHook_Name(t *testing.T) {
	got := (&AuthHook{}).Name()
	if got != "AuthHook" {
		t.Errorf("Name(): got %q, want %q", got, "AuthHook")
	}
}

// TestAuthHook_Describe_NoSecrets — Describe() reports kind="Pre" + a
// token_count int, NEVER the tokens themselves (T-8-LEAK per RESEARCH
// Pitfall 9). The loop walks every key looking for variants of "token"
// that aren't "token_count" — a future PR that adds Config["tokens"]
// for "debugging" trips this guard.
func TestAuthHook_Describe_NoSecrets(t *testing.T) {
	h := &AuthHook{Tokens: []string{"a", "b"}}
	kind, cfg := h.Describe()

	if kind != "Pre" {
		t.Errorf("kind: got %q, want %q", kind, "Pre")
	}

	tc, ok := cfg["token_count"]
	if !ok {
		t.Errorf("cfg missing token_count key: got %+v", cfg)
	} else if tc != 2 {
		t.Errorf("token_count: got %v, want 2", tc)
	}

	// T-8-LEAK guard: NO key may be a token-bearing variant. The
	// allowed key is "token_count"; any other case-insensitive
	// substring "token" is a leak.
	for k := range cfg {
		if k == "token_count" {
			continue
		}
		lower := strings.ToLower(k)
		if strings.Contains(lower, "token") {
			t.Errorf("Describe leaked token-bearing config key %q (full config: %+v)", k, cfg)
		}
	}
}

// TestAuthHook_ConstantTimeCompareSourceAudit — belt-and-suspenders
// source-level guard against `==` regression. Opens auth.go via
// os.ReadFile, asserts (a) subtle.ConstantTimeCompare appears and
// (b) no `provided == valid` / `token == valid` / `== string(` pattern
// appears. Skipped before Task 3 lands (auth.go does not yet exist).
//
// This is the test that catches a future "I just need to compare two
// strings, why not `==`" refactor that would silently reintroduce the
// timing side-channel T-8-AUTH mitigates.
func TestAuthHook_ConstantTimeCompareSourceAudit(t *testing.T) {
	path := "auth.go"
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skipf("auth.go not yet implemented (Task 3); will run after Task 3 lands")
		}
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	src := string(data)

	if !strings.Contains(src, "subtle.ConstantTimeCompare") {
		t.Errorf("auth.go missing subtle.ConstantTimeCompare (T-8-AUTH regression — bearer.go:51 precedent broken)")
	}

	// Belt-and-suspenders: any `==` on token bytes is a regression.
	// Match the common shapes a refactor would produce.
	forbidden := []string{
		"provided == valid",
		"valid == provided",
		"token == valid",
		"valid == token",
		// `== string(` is the bytes-to-string-compare shape that
		// silently uses == on the byte slices' string view.
		"== string(",
	}
	for _, pat := range forbidden {
		if strings.Contains(src, pat) {
			t.Errorf("auth.go contains forbidden pattern %q (T-8-AUTH regression — must use subtle.ConstantTimeCompare)", pat)
		}
	}
}
