// Phase 8 Plan 08-05 Task 4b — round-trip test for the X-Request-Id
// ctx-stamp added to handleChat / handleGenerate.
//
// Asserts:
//   - When the client sends a literal X-Request-Id header, the value
//     is propagated to ctx via plugin.WithRequestID and visible to
//     downstream code through plugin.RequestIDFromContext.
//   - When no X-Request-Id is sent, a fresh ULID (26-char Crockford
//     Base32) is minted via plugin.NewRequestID and stamped.
//
// Uses a dedicated capture-fakeEngine that records the ctx passed to
// Collect — this satisfies the package-private Engine interface
// without disturbing the existing fakeEngine (which other tests
// reference). Whitebox package by necessity (the Engine interface is
// unexported).

package ollama

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/plugin"
)

// captureEngine satisfies the package-private Engine interface and
// records the ctx passed to Collect for assertion.
type captureEngine struct {
	gotCtx context.Context
	resp   *canonical.ChatResponse
}

func (c *captureEngine) Collect(ctx context.Context, _ *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	c.gotCtx = ctx
	return c.resp, nil
}

func (c *captureEngine) Run(ctx context.Context, _ *canonical.ChatRequest) (RunHandle, error) {
	c.gotCtx = ctx
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}
	return newFakeRunHandle(nil, final, nil), nil
}

func newCaptureAdapter(t *testing.T, eng *captureEngine) *Adapter {
	t.Helper()
	return New(Config{Engine: eng, Version: "0.0.0-test", Commit: "abc1234"})
}

func TestHandler_StampsRequestIDOnContext_FromHeader(t *testing.T) {
	const wantID = "01HKQ8ABCDEFGHJKMNPQRSTVWX"
	eng := &captureEngine{resp: &canonical.ChatResponse{
		StopReason: canonical.StopEndTurn,
		Message:    canonical.Message{Role: canonical.RoleAssistant},
	}}
	a := newCaptureAdapter(t, eng)

	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/chat",
		strings.NewReader(`{"model":"auto","stream":false,"messages":[{"role":"user","content":"hi"}]}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Request-Id", wantID)
	w := httptest.NewRecorder()
	a.ProtectedRouter().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	got := plugin.RequestIDFromContext(eng.gotCtx)
	if got != wantID {
		t.Errorf("RequestIDFromContext: got %q, want %q (inbound header)", got, wantID)
	}
}

func TestHandler_StampsRequestIDOnContext_GeneratedWhenAbsent(t *testing.T) {
	eng := &captureEngine{resp: &canonical.ChatResponse{
		StopReason: canonical.StopEndTurn,
		Message:    canonical.Message{Role: canonical.RoleAssistant},
	}}
	a := newCaptureAdapter(t, eng)

	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/chat",
		strings.NewReader(`{"model":"auto","stream":false,"messages":[{"role":"user","content":"hi"}]}`))
	r.Header.Set("Content-Type", "application/json")
	// X-Request-Id deliberately NOT set.
	w := httptest.NewRecorder()
	a.ProtectedRouter().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	got := plugin.RequestIDFromContext(eng.gotCtx)
	if len(got) != 26 {
		t.Errorf("RequestIDFromContext: got %q (len %d), want 26-char ULID", got, len(got))
	}
	// Crockford Base32 alphabet (ULID): 0-9 + A-Z minus I, L, O, U.
	const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	for _, c := range got {
		if !strings.ContainsRune(crockford, c) {
			t.Errorf("RequestIDFromContext: got char %q not in Crockford Base32 alphabet (id=%q)", c, got)
		}
	}
}
