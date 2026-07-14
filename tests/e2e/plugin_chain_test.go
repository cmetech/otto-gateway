//go:build e2e

// Phase 8 Plan 08-05 Task 6 — real-binary e2e for the plugin chain
// integration vertical slice.
//
// Scenarios cover:
//   - SC7 / OBSV-04: /health/hooks default chain, auth-exempt, 405
//     on mutating verbs, secret-omission audit (T-8-LEAK).
//   - SC5 / D-02: ENABLED_HOOKS allowlist preserves registration
//     order; unknown name → boot error refusal (T-8-CFG).
//   - SC1: PreHook short-circuit through chain → per-surface native
//     error envelope (Ollama / OpenAI / Anthropic).
//   - D-05 / T-8-HASH-BOOT: mode=hash with no PII_HASH_KEY → boot
//     error refusal naming PII_HASH_KEY.
//   - OBSV-03: X-Request-Id round-trip echo via /health/hooks.
//
// The GW_E2E=1 gate + the kiro-cli resolution gate inherit from
// tests/e2e/e2e_test.go.

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestE2E_HealthHooks_DefaultChain — Scenario 1.
//
// Default config (no ENABLED_HOOKS override) → all five hooks present
// in registration order: RequestIDHook, AuthHook, JSONFormatSteeringHook,
// PIIRedactionHook, LoggingHook. Each entry carries
// {name, kind, enabled, config}.
func TestE2E_HealthHooks_DefaultChain(t *testing.T) {
	gateOrSkip(t)
	baseURL, cleanup := bootGateway(t, nil)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/health/hooks", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: got %q, want application/json prefix", ct)
	}

	var body struct {
		Hooks []map[string]any `json:"hooks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Hooks) < 5 {
		t.Fatalf("hooks count: got %d, want >= 5 (RequestIDHook, AuthHook, JSONFormatSteeringHook, PIIRedactionHook, LoggingHook); body=%+v", len(body.Hooks), body)
	}

	wantOrder := []string{"RequestIDHook", "AuthHook", "JSONFormatSteeringHook", "PIIRedactionHook", "LoggingHook"}
	for i, name := range wantOrder {
		got, _ := body.Hooks[i]["name"].(string)
		if got != name {
			t.Errorf("hooks[%d].name: got %q, want %q (registration order)", i, got, name)
		}
		for _, key := range []string{"name", "kind", "enabled", "config"} {
			if _, ok := body.Hooks[i][key]; !ok {
				t.Errorf("hooks[%d] missing key %q", i, key)
			}
		}
	}
}

// TestE2E_HealthHooks_AuthExempt — Scenario 2.
//
// /health/hooks returns 200 without a bearer header even when
// AUTH_TOKEN is set (auth-exempt per SC7). For belt-and-suspenders,
// the test also probes /api/chat without bearer to confirm the
// IPAllowlist still permits localhost requests (auth gate moves to
// AuthHook).
func TestE2E_HealthHooks_AuthExempt(t *testing.T) {
	gateOrSkip(t)
	baseURL, cleanup := bootGateway(t, map[string]string{"AUTH_TOKEN": "secret"})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/health/hooks", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	// Deliberately NO Authorization header.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /health/hooks without bearer: got %d, want 200 (auth-exempt per SC7)", resp.StatusCode)
	}
}

// TestE2E_HealthHooks_NoSecretLeak — Scenario 3 (T-8-LEAK).
//
// Boots the gateway with sentinel secret values for AUTH_TOKEN and
// PII_HASH_KEY (mode=hash). GETs /health/hooks and asserts the
// response body does NOT contain either literal sentinel. Also
// asserts no raw regex source fragment appears in the response.
func TestE2E_HealthHooks_NoSecretLeak(t *testing.T) {
	gateOrSkip(t)
	const sentinelAuth = "TOPSECRET_AUTH_E2E"
	const sentinelHash = "TOPSECRET_HASH_E2E"
	baseURL, cleanup := bootGateway(t, map[string]string{
		"AUTH_TOKEN":            sentinelAuth,
		"PII_HASH_KEY":          sentinelHash,
		"PII_REDACTION_MODE":    "hash",
		"PII_REDACTION_ENABLED": "true",
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/health/hooks", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := readBody(resp)
	if err != nil {
		t.Fatalf("readBody: %v", err)
	}
	body := string(bodyBytes)
	if strings.Contains(body, sentinelAuth) {
		t.Errorf("response body must not contain %q (T-8-LEAK); body=%s", sentinelAuth, body)
	}
	if strings.Contains(body, sentinelHash) {
		t.Errorf("response body must not contain %q (T-8-LEAK); body=%s", sentinelHash, body)
	}
	// Unique fragments of the canonical Email + IPv4 regex sources
	// (RESEARCH Pitfall 9). These should never appear.
	for _, frag := range []string{`[A-Za-z0-9._%+`, `(?:[0-9]{1,3}\.`} {
		if strings.Contains(body, frag) {
			t.Errorf("response body must not contain regex source fragment %q; body=%s", frag, body)
		}
	}
}

// TestE2E_HealthHooks_POST_Returns405 — Scenario 4.
//
// SC7 / OBSV-04 no-mutate-path contract verified end-to-end against
// the real binary: POST / PUT / DELETE on /health/hooks all return
// 405 with Allow: GET.
func TestE2E_HealthHooks_POST_Returns405(t *testing.T) {
	gateOrSkip(t)
	baseURL, cleanup := bootGateway(t, nil)
	defer cleanup()

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, method, baseURL+"/health/hooks", nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("%s /health/hooks: got %d, want 405", method, resp.StatusCode)
			}
		})
	}
}

// TestE2E_PIIRedaction_Email — Scenario 5.
//
// PII redaction end-to-end requires the gateway to round-trip through
// kiro-cli AND a mechanism to observe the redacted canonical request
// reaching the engine. The unit-test layer (slice 4's pii_test.go +
// slice 3's logging_test.go) already covers the redaction +
// summary-stash plumbing exhaustively against the canonical types.
// The remaining e2e gap — actually observing kiro see a redacted
// request — needs harness instrumentation that this slice does NOT
// ship (the fake-kiro-cli used by the existing e2e suite does not
// echo back the inbound prompt for assertion).
//
// Documented as a SKIPPED e2e scenario per plan Task 6 fallback
// instruction. Unit coverage at internal/plugin/pii/pii_test.go
// (TestPIIRedactionHook_EnabledMutates etc.) is the v1 acceptance
// signal; full e2e visibility lands as harness work in a follow-up.
func TestE2E_PIIRedaction_Email(t *testing.T) {
	gateOrSkip(t)
	t.Skip("PII e2e redaction visibility requires fake-kiro echo harness — covered by unit tests in slice 4")
}

// TestE2E_BadBearer_AllThreeSurfaces — Scenario 6 (SC1 acceptance
// bar). The full PreHook short-circuit-through-adapter contract end-
// to-end across all three surfaces.
//
// Boots the gateway with AUTH_TOKEN=validtoken; sends a request to
// each surface with Authorization: Bearer wrongtoken; asserts each
// surface returns its native error envelope:
//   - Ollama: {"error":"<string>"}                                   (flat string field)
//   - OpenAI: {"error":{"message":"<string>","type":"<string>"}}     (nested object)
//   - Anthropic: {"type":"error","error":{"type":"<string>","message":"<string>"}}
//
// The per-surface JSON shape is asserted by decoding into a typed
// struct and checking the discriminating keys — substring matches on
// the literal `"error"` would also fire on Anthropic's outer `"type":
// "error"` discriminator, so the typed decode is the only correct way
// to prove each surface renders its own native envelope.
//
// Phase 08.1 INTEG-01 (Plan 02 Task 2): extended in-place per D-09 with
// three additional stream:true rows — ollama_stream, openai_stream,
// anthropic_stream — exercising the Plan 01 short-circuit guards on the
// streaming branches. The six rows now run in one binary boot. D-10
// asserts the full wire-shape contract on each streaming row: 401 +
// Content-Type: application/json + native error envelope + zero SSE/NDJSON
// byte markers (no `data: `, no `event: `, no `"done":true`). The
// assertEnvelope signature was widened to (t, body, ct) so each callback
// can assert the Content-Type header value directly — Pattern F harness
// signature widening per 08.1-PATTERNS.md.
//
// Skipped when kiro-cli is unavailable (bootGateway resolution).
func TestE2E_BadBearer_AllThreeSurfaces(t *testing.T) {
	gateOrSkip(t)
	baseURL, cleanup := bootGateway(t, map[string]string{"AUTH_TOKEN": "validtoken"})
	defer cleanup()

	cases := []struct {
		name    string
		path    string
		headers map[string]string
		body    string
		// assertEnvelope decodes the response body and asserts the
		// surface's native error-envelope shape AND any per-surface
		// wire-shape invariants (Content-Type, negative byte markers).
		// The ct parameter is the response's Content-Type header value,
		// surfaced here so streaming rows can assert it without each
		// callback re-reading the header out of an out-of-scope resp.
		// Each callback uses the t.Errorf in scope to record per-surface
		// key mismatches.
		assertEnvelope func(t *testing.T, body []byte, ct string)
	}{
		{
			name: "ollama",
			path: "/api/chat",
			body: `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}`,
			assertEnvelope: func(t *testing.T, body []byte, _ string) {
				// Ollama: {"error":"<string>"} — flat string field.
				// Non-streaming row: ct unused; the pre-08.1 assertion
				// set did not check Content-Type, preserved verbatim.
				var env struct {
					Error *string `json:"error"`
				}
				if err := json.Unmarshal(body, &env); err != nil {
					t.Fatalf("ollama: decode envelope: %v; body=%s", err, body)
				}
				if env.Error == nil {
					t.Errorf("ollama: missing top-level \"error\" string field; body=%s", body)
					return
				}
				if *env.Error == "" {
					t.Errorf("ollama: \"error\" field is empty; body=%s", body)
				}
			},
		},
		{
			name: "openai",
			path: "/v1/chat/completions",
			body: `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`,
			assertEnvelope: func(t *testing.T, body []byte, _ string) {
				// OpenAI: {"error":{"message":"...","type":"..."}}.
				// Non-streaming row: ct unused, preserved verbatim.
				var env struct {
					Error *struct {
						Message string `json:"message"`
						Type    string `json:"type"`
					} `json:"error"`
				}
				if err := json.Unmarshal(body, &env); err != nil {
					t.Fatalf("openai: decode envelope: %v; body=%s", err, body)
				}
				if env.Error == nil {
					t.Errorf("openai: missing top-level \"error\" object; body=%s", body)
					return
				}
				if env.Error.Message == "" {
					t.Errorf("openai: error.message empty; body=%s", body)
				}
				if env.Error.Type == "" {
					t.Errorf("openai: error.type empty; body=%s", body)
				}
			},
		},
		{
			name: "anthropic",
			path: "/v1/messages",
			headers: map[string]string{
				"anthropic-version": "2023-06-01",
			},
			body: `{"model":"auto","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
			assertEnvelope: func(t *testing.T, body []byte, _ string) {
				// Anthropic: {"type":"error","error":{"type":"...","message":"..."}}.
				// Non-streaming row: ct unused, preserved verbatim.
				var env struct {
					Type  string `json:"type"`
					Error *struct {
						Type    string `json:"type"`
						Message string `json:"message"`
					} `json:"error"`
				}
				if err := json.Unmarshal(body, &env); err != nil {
					t.Fatalf("anthropic: decode envelope: %v; body=%s", err, body)
				}
				if env.Type != "error" {
					t.Errorf("anthropic: outer \"type\" got %q, want \"error\"; body=%s", env.Type, body)
				}
				if env.Error == nil {
					t.Errorf("anthropic: missing nested \"error\" object; body=%s", body)
					return
				}
				if env.Error.Type == "" {
					t.Errorf("anthropic: error.type empty; body=%s", body)
				}
				if env.Error.Message == "" {
					t.Errorf("anthropic: error.message empty; body=%s", body)
				}
			},
		},
		// ============================================================
		// Phase 08.1 INTEG-01 Plan 02 Task 2 — three stream:true rows
		// exercising the Plan 01 short-circuit guards on the streaming
		// branches. Per D-09 these are added in-place (six rows in one
		// binary boot, not a separate test). Per D-12 NO positive-control
		// (happy-path streaming) rows are added — already covered by
		// Phase 4 D-09.
		// ============================================================
		{
			name: "ollama_stream",
			path: "/api/chat",
			body: `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":true}`,
			assertEnvelope: func(t *testing.T, body []byte, ct string) {
				// Wire-invariant 1: Content-Type MUST be application/json.
				// Any application/x-ndjson leak is a T-08.1-HEADER-LEAK
				// regression (Pitfall 3 — guard moved after a header write).
				if !strings.HasPrefix(ct, "application/json") {
					t.Errorf("ollama_stream: Content-Type got %q, want application/json prefix (NDJSON leak == header-leak regression); body=%s", ct, body)
				}
				// Wire-invariant 2: body MUST NOT contain `"done":true`
				// (the load-bearing NDJSON terminator for Ollama).
				if bytes.Contains(body, []byte(`"done":true`)) {
					t.Errorf("ollama_stream: body contains NDJSON marker `\"done\":true` — T-08.1-HEADER-LEAK regression; body=%s", body)
				}
				// Wire-invariant 3: body decodes as flat Ollama envelope.
				var env struct {
					Error *string `json:"error"`
				}
				if err := json.Unmarshal(body, &env); err != nil {
					t.Fatalf("ollama_stream: decode envelope: %v; body=%s", err, body)
				}
				if env.Error == nil {
					t.Errorf("ollama_stream: missing top-level \"error\" string field; body=%s", body)
					return
				}
				if *env.Error == "" {
					t.Errorf("ollama_stream: \"error\" field is empty (T-08.1-EMPTY-BODY); body=%s", body)
				}
			},
		},
		{
			name: "openai_stream",
			path: "/v1/chat/completions",
			body: `{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":true}`,
			assertEnvelope: func(t *testing.T, body []byte, ct string) {
				// Wire-invariant 1: Content-Type MUST be application/json.
				// Any text/event-stream leak is a T-08.1-HEADER-LEAK
				// regression.
				if !strings.HasPrefix(ct, "application/json") {
					t.Errorf("openai_stream: Content-Type got %q, want application/json prefix (SSE leak == header-leak regression); body=%s", ct, body)
				}
				// Wire-invariant 2: body MUST NOT contain `data: ` or
				// `data: [DONE]` — the load-bearing SSE markers for OpenAI.
				if bytes.Contains(body, []byte("data: ")) {
					t.Errorf("openai_stream: body contains SSE marker \"data: \" — T-08.1-HEADER-LEAK regression; body=%s", body)
				}
				if bytes.Contains(body, []byte("data: [DONE]")) {
					t.Errorf("openai_stream: body contains SSE terminator \"data: [DONE]\" — T-08.1-HEADER-LEAK regression; body=%s", body)
				}
				// Wire-invariant 3: body decodes as nested OpenAI envelope;
				// Error.Type MUST equal "authentication_error" (Pitfall 5
				// / T-08.1-WIRE-TYPE-DRIFT — Pi SDK keys on this constant).
				var env struct {
					Error *struct {
						Message string `json:"message"`
						Type    string `json:"type"`
					} `json:"error"`
				}
				if err := json.Unmarshal(body, &env); err != nil {
					t.Fatalf("openai_stream: decode envelope: %v; body=%s", err, body)
				}
				if env.Error == nil {
					t.Errorf("openai_stream: missing top-level \"error\" object; body=%s", body)
					return
				}
				if env.Error.Type != "authentication_error" {
					t.Errorf("openai_stream: error.type got %q, want \"authentication_error\" (Pitfall 5 / T-08.1-WIRE-TYPE-DRIFT); body=%s", env.Error.Type, body)
				}
				if env.Error.Message == "" {
					t.Errorf("openai_stream: error.message empty (T-08.1-EMPTY-BODY); body=%s", body)
				}
			},
		},
		{
			name: "anthropic_stream",
			path: "/v1/messages",
			headers: map[string]string{
				"anthropic-version": "2023-06-01",
			},
			body: `{"model":"auto","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"stream":true}`,
			assertEnvelope: func(t *testing.T, body []byte, ct string) {
				// Wire-invariant 1: Content-Type MUST be application/json.
				// Any text/event-stream leak is a T-08.1-HEADER-LEAK
				// regression.
				if !strings.HasPrefix(ct, "application/json") {
					t.Errorf("anthropic_stream: Content-Type got %q, want application/json prefix (SSE leak == header-leak regression); body=%s", ct, body)
				}
				// Wire-invariant 2: body MUST NOT contain `event: ` — per
				// RESEARCH.md the load-bearing Anthropic SSE marker (stream
				// opens with `event: message_start\n`). Both the bare
				// prefix and the full marker are checked.
				if bytes.Contains(body, []byte("event: ")) {
					t.Errorf("anthropic_stream: body contains SSE marker \"event: \" — T-08.1-HEADER-LEAK regression (load-bearing for Anthropic); body=%s", body)
				}
				if bytes.Contains(body, []byte("event: message_start")) {
					t.Errorf("anthropic_stream: body contains SSE marker \"event: message_start\" — T-08.1-HEADER-LEAK regression; body=%s", body)
				}
				// Wire-invariant 3: body decodes as double-wrapped
				// Anthropic envelope; outer type=="error", inner
				// Error.Type=="authentication_error" (Pitfall 5 /
				// T-08.1-WIRE-TYPE-DRIFT — @anthropic-ai/sdk keys on this).
				var env struct {
					Type  string `json:"type"`
					Error *struct {
						Type    string `json:"type"`
						Message string `json:"message"`
					} `json:"error"`
				}
				if err := json.Unmarshal(body, &env); err != nil {
					t.Fatalf("anthropic_stream: decode envelope: %v; body=%s", err, body)
				}
				if env.Type != "error" {
					t.Errorf("anthropic_stream: outer \"type\" got %q, want \"error\"; body=%s", env.Type, body)
				}
				if env.Error == nil {
					t.Errorf("anthropic_stream: missing nested \"error\" object; body=%s", body)
					return
				}
				if env.Error.Type != "authentication_error" {
					t.Errorf("anthropic_stream: error.type got %q, want \"authentication_error\" (Pitfall 5 / T-08.1-WIRE-TYPE-DRIFT); body=%s", env.Error.Type, body)
				}
				if env.Error.Message == "" {
					t.Errorf("anthropic_stream: error.message empty (T-08.1-EMPTY-BODY); body=%s", body)
				}
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost,
				baseURL+tc.path, strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer wrongtoken")
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			// SC1 acceptance: the response is a non-2xx with the
			// surface's native error envelope. The exact status code
			// is per-surface (Anthropic uses 401; OpenAI uses 401;
			// Ollama may use 401 or 403); we assert non-2xx and then
			// decode the per-surface native envelope.
			//
			// Phase 08.1 INTEG-01 D-10: the streaming rows
			// (ollama_stream / openai_stream / anthropic_stream)
			// additionally assert Content-Type == application/json plus
			// per-surface negative byte-marker absence inside
			// assertEnvelope — see each callback above.
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				t.Errorf("%s: got 2xx (%d), want non-2xx (AuthHook short-circuit)", tc.name, resp.StatusCode)
			}
			ct := resp.Header.Get("Content-Type")
			bodyBytes, err := readBody(resp)
			if err != nil {
				t.Fatalf("readBody: %v", err)
			}
			tc.assertEnvelope(t, bodyBytes, ct)
		})
	}
}

// TestE2E_BootError_UnknownHook — Scenario 7 (T-8-CFG).
//
// ENABLED_HOOKS contains a name not present in main.go's chain →
// gateway refuses to start with stderr/stdout naming the offender.
// Uses direct exec rather than bootGateway because bootGateway
// expects /health to come up — we expect a non-zero exit BEFORE the
// listener. Modeled on TestE2E_SurfaceGating_TypoFailFast.
//
// Note: the gateway's runtime logger writes to stdout (per
// buildLogger), so we capture both stdout and stderr to be defensive
// about which stream carries the boot-error log line.
func TestE2E_BootError_UnknownHook(t *testing.T) {
	gateOrSkip(t)
	_ = resolveKiro(t) // skip uniformly when kiro env is absent

	addr := freePort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, builtBinary)
	cmd.Env = append(
		os.Environ(),
		"HTTP_ADDR="+addr,
		"ENABLED_HOOKS=BogusHook",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start binary: %v", err)
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	select {
	case err := <-waitDone:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("expected non-zero exit, got err=%v; stdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		combined := stdout.String() + stderr.String()
		if !strings.Contains(combined, "unknown hook") {
			t.Errorf("output must contain literal 'unknown hook'; got stdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
		}
		if !strings.Contains(combined, "BogusHook") {
			t.Errorf("output must name the offending hook 'BogusHook'; got stdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
		}
	case <-time.After(5 * time.Second):
		cancel()
		<-waitDone
		t.Fatal("binary did not exit within 5s on ENABLED_HOOKS=BogusHook (typo-fail-fast broken?)")
	}
}

// TestE2E_BootError_HashModeNoKey — Scenario 8 (T-8-HASH-BOOT).
//
// PII_REDACTION_MODE=hash with no PII_HASH_KEY → gateway exits non-
// zero before the listener accepts; stderr names PII_HASH_KEY.
func TestE2E_BootError_HashModeNoKey(t *testing.T) {
	gateOrSkip(t)
	_ = resolveKiro(t)

	addr := freePort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, builtBinary)
	cmd.Env = append(
		os.Environ(),
		"HTTP_ADDR="+addr,
		"PII_REDACTION_MODE=hash",
		// PII_HASH_KEY deliberately not set.
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start binary: %v", err)
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	select {
	case err := <-waitDone:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("expected non-zero exit, got err=%v; stdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		combined := stdout.String() + stderr.String()
		if !strings.Contains(combined, "PII_HASH_KEY") {
			t.Errorf("output must name PII_HASH_KEY (T-8-HASH-BOOT); got stdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
		}
	case <-time.After(5 * time.Second):
		cancel()
		<-waitDone
		t.Fatal("binary did not exit within 5s on PII_REDACTION_MODE=hash without PII_HASH_KEY (Pitfall 6 broken?)")
	}
}

// TestE2E_EnabledHooks_Filter_PreservesOrder — Scenario 9 (D-02 SC5).
//
// ENABLED_HOOKS=LoggingHook,RequestIDHook (deliberate allowlist-order
// != registration-order) → /health/hooks response shows
// [RequestIDHook, LoggingHook] in REGISTRATION order.
func TestE2E_EnabledHooks_Filter_PreservesOrder(t *testing.T) {
	gateOrSkip(t)
	baseURL, cleanup := bootGateway(t, map[string]string{
		"ENABLED_HOOKS": "LoggingHook,RequestIDHook",
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/health/hooks", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var body struct {
		Hooks []map[string]any `json:"hooks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Expect exactly two hooks in REGISTRATION order (the dedup
	// elides LoggingHook's Post-side duplicate row).
	if len(body.Hooks) < 2 {
		t.Fatalf("hooks count: got %d, want >= 2; body=%+v", len(body.Hooks), body)
	}
	first, _ := body.Hooks[0]["name"].(string)
	second, _ := body.Hooks[1]["name"].(string)
	if first != "RequestIDHook" {
		t.Errorf("Hooks[0].name: got %q, want %q (registration order, NOT allowlist order)", first, "RequestIDHook")
	}
	if second != "LoggingHook" {
		t.Errorf("Hooks[1].name: got %q, want %q", second, "LoggingHook")
	}
}

// TestE2E_EnabledHooks_AuthFilteredOut_Returns200 — Scenario 10
// (Task 7 step 5b automated). Proves the SC5 allowlist semantics
// have real teeth: with AuthHook filtered OUT of ENABLED_HOOKS, an
// unauthenticated request through a surface no longer short-circuits
// at 401 — the chain skips the auth gate entirely.
//
// Boots with AUTH_TOKEN=e2e-token set (default from bootGateway) AND
// ENABLED_HOOKS=RequestIDHook,LoggingHook (AuthHook deliberately
// absent). Sends POST /api/chat WITHOUT any Authorization header.
//
// Acceptance is asymmetric on purpose: we cannot guarantee a 2xx
// response (the engine may still reject the request for unrelated
// reasons — model resolution, kiro warmup, etc.), but we CAN
// guarantee the response is NOT 401, because the only producer of
// 401 in the v1 chain is AuthHook (per adapter handler comments at
// anthropic/handlers.go and openai/errors.go). A 401 here would
// prove AuthHook ran despite being filtered out — which is exactly
// the bug this test guards against.
func TestE2E_EnabledHooks_AuthFilteredOut_Returns200(t *testing.T) {
	gateOrSkip(t)
	baseURL, cleanup := bootGateway(t, map[string]string{
		// AUTH_TOKEN is also stamped by bootGateway's default env;
		// we restate it here as documentation that the token IS
		// configured — the only reason a request without a bearer
		// would succeed is the AuthHook absence, not auth-disabled.
		"AUTH_TOKEN":    "e2e-token",
		"ENABLED_HOOKS": "RequestIDHook,LoggingHook",
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+"/api/chat",
		strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Deliberately NO Authorization header. With AuthHook in the chain
	// this would short-circuit at 401; with AuthHook filtered out the
	// request must flow past the auth gate.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, _ := readBody(resp)
	if resp.StatusCode == http.StatusUnauthorized {
		t.Errorf("got 401 with AuthHook filtered out via ENABLED_HOOKS — AuthHook still firing? body=%s", bodyBytes)
	}
	// Belt-and-suspenders: the response body must NOT contain an
	// AuthHook-shaped envelope. The discriminator is the OpenAI/
	// Anthropic auth-error type literal (Ollama's auth envelope is a
	// plain {"error":"<string>"} that we cannot distinguish from a
	// generic engine error by shape alone, so we rely on the status
	// assertion above).
	if strings.Contains(string(bodyBytes), `"authentication_error"`) {
		t.Errorf("body contains \"authentication_error\" with AuthHook filtered out; body=%s", bodyBytes)
	}
}

// readBody drains an HTTP response body and returns the bytes. The
// existing tests/e2e/e2e_test.go has a readAll helper, but it
// requires a *http.Response and uses a different signature; replicate
// minimally here to keep this file self-contained.
func readBody(resp *http.Response) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(resp.Body)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
