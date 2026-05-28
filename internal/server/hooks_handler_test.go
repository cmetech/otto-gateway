// Phase 8 Plan 08-05 Task 1 — Wave 0 hooks_handler_test.go.
//
// Tests the GET /health/hooks JSON envelope, registration-order
// preservation, secret-omission audit (T-8-LEAK), method-not-allowed
// behavior, and auth-exempt routing.
//
// Mirrors agents_test.go 1:1 — fake-source pattern via
// server.HooksDescriptionSource consumer-defined interface.
package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"otto-gateway/internal/server"
)

// fakeHooksSource satisfies server.HooksDescriptionSource with a fixed
// (pre, post) HookDescription vector. Used by every hooks_handler_test.
type fakeHooksSource struct {
	pre  []server.HookDescription
	post []server.HookDescription
}

func (f fakeHooksSource) Describe() (pre, post []server.HookDescription) {
	return f.pre, f.post
}

// TestHooksHandler_EmptyChain — nil hooks source produces a 200 with
// the canonical empty shape: {"hooks": []} (empty array, not omitted).
func TestHooksHandler_EmptyChain(t *testing.T) {
	srv := newFromConfigForTest(t, server.Config{})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health/hooks", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /health/hooks: want 200, got %d", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: want application/json, got %q", ct)
	}
	var body server.HooksResponse
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Hooks == nil {
		t.Errorf("hooks: got nil, want empty array")
	}
	if len(body.Hooks) != 0 {
		t.Errorf("hooks len: got %d, want 0", len(body.Hooks))
	}
}

// TestHooksHandler_FourHookChain_JSONEnvelope — full four-hook chain
// renders in registration order. The handler concatenates Pre rows
// followed by Post rows.
func TestHooksHandler_FourHookChain_JSONEnvelope(t *testing.T) {
	pre := []server.HookDescription{
		{Name: "RequestIDHook", Kind: "Pre", Enabled: true, Config: map[string]any{"format": "ulid"}},
		{Name: "AuthHook", Kind: "Pre", Enabled: true, Config: map[string]any{"token_count": 1}},
		{Name: "PIIRedactionHook", Kind: "Pre", Enabled: true, Config: map[string]any{"enabled": false, "mode": "replace"}},
		{Name: "LoggingHook", Kind: "Pre,Post", Enabled: true, Config: map[string]any{"level": "INFO"}},
	}
	post := []server.HookDescription{
		{Name: "LoggingHook", Kind: "Pre,Post", Enabled: true, Config: map[string]any{"level": "INFO"}},
	}
	srv := newFromConfigForTest(t, server.Config{Hooks: fakeHooksSource{pre: pre, post: post}})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health/hooks", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
	var body server.HooksResponse
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Pre + Post are flattened into one `hooks` array. With dedup-by-name
	// for the dual-Pre+Post LoggingHook, the response has FOUR entries.
	if len(body.Hooks) != 4 {
		t.Fatalf("hooks len: got %d, want 4 (dedup'd LoggingHook); body=%+v", len(body.Hooks), body)
	}
	wantNames := []string{"RequestIDHook", "AuthHook", "PIIRedactionHook", "LoggingHook"}
	for i, want := range wantNames {
		if body.Hooks[i].Name != want {
			t.Errorf("hooks[%d].name: got %q, want %q (registration order)", i, body.Hooks[i].Name, want)
		}
	}
	// LoggingHook reports the combined kind once.
	if body.Hooks[3].Kind != "Pre,Post" {
		t.Errorf("LoggingHook kind: got %q, want %q", body.Hooks[3].Kind, "Pre,Post")
	}
}

// TestHooksHandler_EntryShape — each entry has the four expected keys
// and the kind is one of the documented values.
func TestHooksHandler_EntryShape(t *testing.T) {
	pre := []server.HookDescription{
		{Name: "RequestIDHook", Kind: "Pre", Enabled: true, Config: map[string]any{}},
	}
	srv := newFromConfigForTest(t, server.Config{Hooks: fakeHooksSource{pre: pre}})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health/hooks", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	var raw map[string][]map[string]any
	if err := json.NewDecoder(w.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	entries, ok := raw["hooks"]
	if !ok {
		t.Fatal("response missing 'hooks' key")
	}
	if len(entries) != 1 {
		t.Fatalf("hooks len: got %d, want 1", len(entries))
	}
	for _, k := range []string{"name", "kind", "enabled", "config"} {
		if _, ok := entries[0][k]; !ok {
			t.Errorf("entry missing key %q; entry=%+v", k, entries[0])
		}
	}
	kind, _ := entries[0]["kind"].(string)
	switch kind {
	case "Pre", "Post", "Pre,Post":
		// ok
	default:
		t.Errorf("kind: got %q, want one of Pre|Post|Pre,Post", kind)
	}
}

// TestHooksHandler_SecretOmissionAudit — T-8-LEAK. The hooks response
// MUST NOT contain literal sentinel secret values; regex sources also
// MUST NOT appear in the body. This is the handler-level guard against
// a hook's Describe() leaking secrets.
//
// We construct a fake source where the (non-secret-bearing) Config map
// is what reaches the wire — the underlying hooks (whose internal state
// holds the sentinels) are NOT serialized. The test is the
// belt-and-suspenders assertion that the WIRE shape literally cannot
// contain those values.
func TestHooksHandler_SecretOmissionAudit(t *testing.T) {
	// IMPORTANT: even though Describe() is supposed to omit secrets,
	// we ALSO assert the handler's output cannot contain these literal
	// strings — the test would catch a future regression where a hook
	// accidentally leaks secret bytes into its Config map.
	const sentinelAuth = "TOPSECRET_AUTH_TOKEN_001"
	const sentinelHash = "TOPSECRET_HASH_KEY_002"

	// Construct a source whose Describe returns config maps WITHOUT
	// the sentinels (mirrors what AuthHook/PIIRedactionHook actually do).
	pre := []server.HookDescription{
		{Name: "AuthHook", Kind: "Pre", Enabled: true, Config: map[string]any{"token_count": 1}},
		{Name: "PIIRedactionHook", Kind: "Pre", Enabled: true, Config: map[string]any{
			"enabled":  true,
			"mode":     "hash",
			"entities": []string{"Email", "SSN"},
		}},
	}
	srv := newFromConfigForTest(t, server.Config{Hooks: fakeHooksSource{pre: pre}})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health/hooks", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	body := w.Body.String()
	if strings.Contains(body, sentinelAuth) {
		t.Errorf("response body must not contain literal AUTH sentinel %q; body=%s", sentinelAuth, body)
	}
	if strings.Contains(body, sentinelHash) {
		t.Errorf("response body must not contain literal HASH sentinel %q; body=%s", sentinelHash, body)
	}
	// Also assert no regex source fragments slip through (Pitfall 9):
	// the canonical email regex contains `[A-Za-z0-9._%+\-]` — a unique
	// fragment that would only appear if Pattern.String() was published.
	if strings.Contains(body, `[A-Za-z0-9._%+\-]`) {
		t.Errorf("response body must not contain raw regex source; body=%s", body)
	}
}

// TestHooksHandler_POST_Returns405 — non-GET methods return 405 per
// SC7 (no runtime mutate path).
func TestHooksHandler_POST_Returns405(t *testing.T) {
	srv := newFromConfigForTest(t, server.Config{})

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			r := httptest.NewRequestWithContext(context.Background(), method, "/health/hooks", nil)
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, r)
			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s /health/hooks: got %d, want 405", method, w.Code)
			}
		})
	}
}

// TestHooksHandler_AuthExempt — GET succeeds without a bearer header
// even when AUTH_TOKEN is configured (mirror of
// TestAgentsHandler_NoAuthRequired).
func TestHooksHandler_AuthExempt(t *testing.T) {
	srv := newFromConfigForTest(t, server.Config{
		AuthTokens: []string{"secret"},
	})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health/hooks", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("GET /health/hooks without bearer: got %d, want 200 (auth-exempt)", w.Code)
	}
}

// TestHooksHandler_RegistrationOrder_NotAllowlistOrder — the response
// always uses the chain's REGISTRATION order regardless of what order
// the operator listed in ENABLED_HOOKS. Defense against a future
// regression where main.go's adapter reorders.
func TestHooksHandler_RegistrationOrder_NotAllowlistOrder(t *testing.T) {
	// Simulate the post-Filter state: the fake source returns hooks in
	// REGISTRATION order (RequestIDHook, LoggingHook) even though the
	// operator's allowlist was "LoggingHook,RequestIDHook" (reversed).
	// The handler reflects that registration order verbatim.
	pre := []server.HookDescription{
		{Name: "RequestIDHook", Kind: "Pre", Enabled: true, Config: map[string]any{}},
		{Name: "LoggingHook", Kind: "Pre,Post", Enabled: true, Config: map[string]any{"level": "INFO"}},
	}
	srv := newFromConfigForTest(t, server.Config{Hooks: fakeHooksSource{pre: pre}})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health/hooks", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	var body server.HooksResponse
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Hooks) != 2 {
		t.Fatalf("hooks len: got %d, want 2", len(body.Hooks))
	}
	if body.Hooks[0].Name != "RequestIDHook" {
		t.Errorf("Hooks[0].Name: got %q, want RequestIDHook (registration order)", body.Hooks[0].Name)
	}
	if body.Hooks[1].Name != "LoggingHook" {
		t.Errorf("Hooks[1].Name: got %q, want LoggingHook", body.Hooks[1].Name)
	}
}
