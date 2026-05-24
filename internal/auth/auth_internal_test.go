package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// TestWriteOllamaError_Shape is a whitebox test — writeOllamaError is
// package-private, so this lives in `package auth` (not `package auth_test`)
// to exercise it directly. The contract is locked verbatim against the Node
// reference: Content-Type=application/json + status + JSON body {"error": "<msg>"}.
func TestWriteOllamaError_Shape(t *testing.T) {
	rec := httptest.NewRecorder()
	writeOllamaError(rec, http.StatusUnauthorized, "Invalid or missing API key")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: want 401, got %d", rec.Code)
	}

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: want application/json, got %q", ct)
	}

	var got map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v (raw=%q)", err, rec.Body.String())
	}
	want := map[string]string{"error": "Invalid or missing API key"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("body: want %v, got %v", want, got)
	}
}

// --- D-15: extractToken whitebox tests (Phase 3.1) -----------------------
//
// extractToken implements the precedence rule introduced in D-15: try
// Authorization: Bearer FIRST, fall back to x-api-key ONLY when the
// Authorization header is absent or non-Bearer. The middleware-layer
// TestBearer_DualHeader in auth_test.go validates the same contract end-
// to-end; these whitebox cases pin the helper's behaviour directly.

func TestExtractToken_AuthorizationBearer(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer my-token-1")
	if got := extractToken(r); got != "my-token-1" {
		t.Errorf("Authorization: Bearer extraction: got %q, want %q", got, "my-token-1")
	}
}

func TestExtractToken_XAPIKeyFallback(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	// No Authorization header at all — fall back to x-api-key.
	r.Header.Set("x-api-key", "anthropic-shape-key")
	if got := extractToken(r); got != "anthropic-shape-key" {
		t.Errorf("x-api-key fallback: got %q, want %q", got, "anthropic-shape-key")
	}
}

func TestExtractToken_AuthorizationWinsOverXAPIKey(t *testing.T) {
	t.Parallel()
	// D-15 precedence: when BOTH headers are present, Authorization wins.
	// The x-api-key fallback is consulted ONLY when Authorization is
	// absent or non-Bearer. This guards against a downgrade attack where
	// an attacker supplies a bad Bearer alongside a stolen x-api-key —
	// Authorization MUST be evaluated first and its validity decides the
	// request.
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer auth-token")
	r.Header.Set("x-api-key", "key-token")
	if got := extractToken(r); got != "auth-token" {
		t.Errorf("precedence: got %q, want %q (Authorization must win)", got, "auth-token")
	}
}

func TestExtractToken_NonBearerAuthorizationFallsThrough(t *testing.T) {
	t.Parallel()
	// When Authorization is present but NOT "Bearer ...", the helper
	// must fall through to x-api-key. This covers the Basic-auth case
	// (Anthropic SDK doesn't send Basic but a misconfigured proxy
	// could) and the "Bearer<no-space>token" malformation.
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	r.Header.Set("x-api-key", "fallback-key")
	if got := extractToken(r); got != "fallback-key" {
		t.Errorf("non-Bearer Authorization should fall through to x-api-key: got %q, want %q", got, "fallback-key")
	}
}

func TestExtractToken_NeitherHeader_Empty(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	if got := extractToken(r); got != "" {
		t.Errorf("no headers: got %q, want empty", got)
	}
}

// --- CR-02 / Gap 2: lowercase Bearer scheme acceptance ------------------
//
// RFC 7235 §2.1 and RFC 6750 require the auth-scheme token to be matched
// case-insensitively. The previous strings.HasPrefix exact-case match in
// extractToken silently dropped `Authorization: bearer <token>` and fell
// through to x-api-key — which (a) rejected valid lowercase-Bearer creds
// when no x-api-key was supplied, and (b) BROKE the D-15 Authorization-
// wins downgrade-attack guard for lowercase clients (bad lowercase Bearer
// + stolen x-api-key would silently authenticate as the x-api-key value).
//
// These two tests pin the contract for the lowercase variant: the scheme
// must be recognized AND the precedence semantics must hold.

func TestExtractToken_LowercaseBearerSchemeAccepted(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "bearer my-token")
	if got := extractToken(r); got != "my-token" {
		t.Errorf("lowercase scheme: got %q, want %q (RFC 7235 §2.1 case-insensitive scheme match required)", got, "my-token")
	}
}

func TestExtractToken_LowercaseBearer_DoesNotFallThroughToXAPIKey(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "bearer auth-token")
	r.Header.Set("x-api-key", "key-token")
	if got := extractToken(r); got != "auth-token" {
		t.Errorf("D-15 precedence: lowercase Bearer must still win over x-api-key; got %q, want %q", got, "auth-token")
	}
}
