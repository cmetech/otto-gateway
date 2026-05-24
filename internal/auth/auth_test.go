package auth_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"testing"

	"loop24-gateway/internal/auth"
)

// okHandler is the "next" handler in the chain — returns 200 + a sentinel
// body so failing tests can distinguish "middleware short-circuited" from
// "next handler ran".
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
})

func decodeErrorBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v (raw=%q)", err, rec.Body.String())
	}
	return body
}

// --- Bearer ---------------------------------------------------------------

func TestBearer_EmptyTokens_PassesThrough(t *testing.T) {
	cfg := auth.Config{} // Tokens nil
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/chat", nil)
	// Even a junk Authorization header is irrelevant when Tokens is empty.
	req.Header.Set("Authorization", "Bearer anything")
	auth.Bearer(cfg)(okHandler).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200 (auth disabled), got %d", rec.Code)
	}
}

func TestBearer_ValidToken_PassesThrough(t *testing.T) {
	cfg := auth.Config{Tokens: []string{"s3cret"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/chat", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	auth.Bearer(cfg)(okHandler).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rec.Code)
	}
}

func TestBearer_InvalidToken_Rejects(t *testing.T) {
	cfg := auth.Config{Tokens: []string{"s3cret"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/chat", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	auth.Bearer(cfg)(okHandler).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: want 401, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: want application/json, got %q", ct)
	}
	want := map[string]string{"error": "Invalid or missing API key"}
	if got := decodeErrorBody(t, rec); !reflect.DeepEqual(got, want) {
		t.Errorf("body: want %v, got %v", want, got)
	}
}

func TestBearer_MissingHeader_Rejects(t *testing.T) {
	cfg := auth.Config{Tokens: []string{"s3cret"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/chat", nil)
	// No Authorization header set.
	auth.Bearer(cfg)(okHandler).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: want 401, got %d", rec.Code)
	}
}

func TestBearer_MultiToken_ValidatesAny(t *testing.T) {
	cfg := auth.Config{Tokens: []string{"a", "b", "c"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/chat", nil)
	req.Header.Set("Authorization", "Bearer b")
	auth.Bearer(cfg)(okHandler).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200 (middle token should match), got %d", rec.Code)
	}
}

// TestBearer_DualHeader (Phase 3.1 D-15): the Bearer middleware accepts
// BOTH "Authorization: Bearer <token>" AND "x-api-key: <token>" globally
// against the same Config.Tokens set, using constant-time-compare on
// every token-comparison branch. Authorization takes precedence when
// both headers are present — even when the Authorization Bearer value
// is INVALID, the request is rejected (a valid x-api-key MUST NOT
// rescue a bad Bearer).
func TestBearer_DualHeader(t *testing.T) {
	cfg := auth.Config{Tokens: []string{"s3cret"}}

	cases := []struct {
		name             string
		setAuthorization string // "" means do not set
		setXAPIKey       string // "" means do not set
		wantStatus       int
	}{
		{
			name:             "bearer_only_valid",
			setAuthorization: "Bearer s3cret",
			wantStatus:       http.StatusOK,
		},
		{
			name:       "x_api_key_only_valid",
			setXAPIKey: "s3cret",
			wantStatus: http.StatusOK,
		},
		{
			name:             "bearer_wrong_x_api_key_valid_rejects",
			setAuthorization: "Bearer wrong",
			setXAPIKey:       "s3cret",
			wantStatus:       http.StatusUnauthorized,
		},
		{
			name:             "bearer_valid_x_api_key_wrong_accepts",
			setAuthorization: "Bearer s3cret",
			setXAPIKey:       "wrong",
			wantStatus:       http.StatusOK,
		},
		{
			name:       "neither_header_rejects",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:             "non_bearer_authorization_falls_through_to_x_api_key",
			setAuthorization: "Basic dXNlcjpwYXNz",
			setXAPIKey:       "s3cret",
			wantStatus:       http.StatusOK,
		},
		{
			name:             "non_bearer_authorization_and_no_x_api_key_rejects",
			setAuthorization: "Basic dXNlcjpwYXNz",
			wantStatus:       http.StatusUnauthorized,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/messages", nil)
			if tc.setAuthorization != "" {
				req.Header.Set("Authorization", tc.setAuthorization)
			}
			if tc.setXAPIKey != "" {
				req.Header.Set("x-api-key", tc.setXAPIKey)
			}
			auth.Bearer(cfg)(okHandler).ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status: want %d, got %d (body=%s)", tc.wantStatus, rec.Code, rec.Body.String())
			}
			if tc.wantStatus == http.StatusUnauthorized {
				// The Bearer middleware is surface-agnostic — it emits
				// the Ollama-shape error envelope regardless of the
				// requested path (D-15 + RESEARCH.md Pattern 3 option 2).
				if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
					t.Errorf("Content-Type: want application/json, got %q", ct)
				}
				want := map[string]string{"error": "Invalid or missing API key"}
				if got := decodeErrorBody(t, rec); !reflect.DeepEqual(got, want) {
					t.Errorf("body: want %v, got %v", want, got)
				}
			}
		})
	}
}

// TestBearer_DualHeader_EmptyTokens_PassesThrough confirms that even
// with both headers present, an empty Tokens slice still falls through
// to the next handler — D-15 preserves the Phase 2 no-auth-mode
// semantics (AUTH_TOKEN unset → no auth, matching Node parity).
func TestBearer_DualHeader_EmptyTokens_PassesThrough(t *testing.T) {
	cfg := auth.Config{} // no Tokens
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer anything")
	req.Header.Set("x-api-key", "anything-else")
	auth.Bearer(cfg)(okHandler).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200 (auth disabled — D-15 preserves no-auth mode), got %d", rec.Code)
	}
}

// --- IPAllowlist ----------------------------------------------------------

func cidr(t *testing.T, s string) netip.Prefix {
	t.Helper()
	return netip.MustParsePrefix(s)
}

func TestIPAllowlist_EmptyPrefixes_PassesThrough(t *testing.T) {
	cfg := auth.Config{} // AllowedPrefixes nil
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/chat", nil)
	req.RemoteAddr = "192.168.99.99:12345"
	rec := httptest.NewRecorder()
	auth.IPAllowlist(cfg)(okHandler).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200 (allow-all), got %d", rec.Code)
	}
}

func TestIPAllowlist_MatchingCIDR_PassesThrough(t *testing.T) {
	cfg := auth.Config{AllowedPrefixes: []netip.Prefix{cidr(t, "10.0.0.0/8")}}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/chat", nil)
	req.RemoteAddr = "10.1.2.3:12345"
	rec := httptest.NewRecorder()
	auth.IPAllowlist(cfg)(okHandler).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestIPAllowlist_NonMatchingIP_Rejects(t *testing.T) {
	cfg := auth.Config{AllowedPrefixes: []netip.Prefix{cidr(t, "10.0.0.0/8")}}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/chat", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	rec := httptest.NewRecorder()
	auth.IPAllowlist(cfg)(okHandler).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: want 403, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: want application/json, got %q", ct)
	}
	got := decodeErrorBody(t, rec)
	if got["error"] == "" {
		t.Errorf("body.error: want non-empty rejection message, got empty (full body=%v)", got)
	}
}

func TestIPAllowlist_IPv4InIPv6Mapping(t *testing.T) {
	cfg := auth.Config{AllowedPrefixes: []netip.Prefix{cidr(t, "127.0.0.0/8")}}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/chat", nil)
	// Go's dual-stack socket emits the ::ffff: prefix for IPv4 connections.
	req.RemoteAddr = "[::ffff:127.0.0.1]:12345"
	rec := httptest.NewRecorder()
	auth.IPAllowlist(cfg)(okHandler).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200 (::ffff: should be stripped), got %d (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestIPAllowlist_XFFNotTrustedByDefault(t *testing.T) {
	// Codex H-7: a localhost client setting X-Forwarded-For: 10.0.0.5 must
	// NOT bypass the allowlist when TrustXForwardedFor is false (default).
	cfg := auth.Config{AllowedPrefixes: []netip.Prefix{cidr(t, "10.0.0.0/8")}}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/chat", nil)
	req.Header.Set("X-Forwarded-For", "10.0.0.5")
	req.RemoteAddr = "192.168.1.1:12345" // NOT in 10.0.0.0/8
	rec := httptest.NewRecorder()
	auth.IPAllowlist(cfg)(okHandler).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: want 403 (XFF must be ignored by default), got %d", rec.Code)
	}
}

func TestIPAllowlist_XFFRespectedWhenEnabled(t *testing.T) {
	// Opt-in: with TrustXForwardedFor=true, the first XFF hop wins.
	cfg := auth.Config{
		AllowedPrefixes:    []netip.Prefix{cidr(t, "10.0.0.0/8")},
		TrustXForwardedFor: true,
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/chat", nil)
	req.Header.Set("X-Forwarded-For", "10.0.0.5, 192.168.1.1")
	req.RemoteAddr = "192.168.1.1:12345"
	rec := httptest.NewRecorder()
	auth.IPAllowlist(cfg)(okHandler).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200 (XFF first-hop matches), got %d (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestIPAllowlist_XFFIgnored_FallsBackToRemoteAddr_WhenDisabled(t *testing.T) {
	// Proves the default path uses RemoteAddr unconditionally even when XFF is present.
	cfg := auth.Config{AllowedPrefixes: []netip.Prefix{cidr(t, "127.0.0.0/8")}}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/chat", nil)
	req.Header.Set("X-Forwarded-For", "10.0.0.5") // would NOT match if honored
	req.RemoteAddr = "127.0.0.1:12345"            // DOES match
	rec := httptest.NewRecorder()
	auth.IPAllowlist(cfg)(okHandler).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200 (RemoteAddr should win when TrustXFF=false), got %d", rec.Code)
	}
}

func TestIPAllowlist_MalformedXFF_FallsBackToRemoteAddr(t *testing.T) {
	// Even with TrustXForwardedFor=true, a pathological XFF must gracefully
	// fall back to RemoteAddr rather than 403-ing the request.
	cfg := auth.Config{
		AllowedPrefixes:    []netip.Prefix{cidr(t, "127.0.0.0/8")},
		TrustXForwardedFor: true,
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/chat", nil)
	req.Header.Set("X-Forwarded-For", "not-an-ip")
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	auth.IPAllowlist(cfg)(okHandler).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200 (malformed XFF should fall through to RemoteAddr), got %d", rec.Code)
	}
}
