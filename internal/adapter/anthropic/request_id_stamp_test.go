// Phase 8 Plan 08-05 Task 4b — round-trip test for the X-Request-Id
// ctx-stamp added to handleMessages.
//
// Asserts:
//   - When the client sends X-Request-Id, the value is propagated to
//     ctx via plugin.WithRequestID and visible to downstream code
//     through plugin.RequestIDFromContext.
//   - When no X-Request-Id is sent, a fresh ULID is minted and stamped.
//
// Note: handleMessages routes Collect through CollectAnthropicChat
// which uses eng.Run internally. We capture the ctx Run received.

package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/plugin"
)

// anthropicCaptureEngine satisfies the package-private Engine interface
// and records the ctx Run received. Tests use it to assert the
// X-Request-Id stamp survives into the engine call.
type anthropicCaptureEngine struct {
	gotCtx context.Context
	resp   *canonical.ChatResponse
}

func (c *anthropicCaptureEngine) Collect(ctx context.Context, _ *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	c.gotCtx = ctx
	return c.resp, nil
}

func (c *anthropicCaptureEngine) Run(ctx context.Context, _ *canonical.ChatRequest) (RunHandle, error) {
	c.gotCtx = ctx
	if c.resp != nil {
		return synthesizeRunHandleFromCollectResp(c.resp, nil), nil
	}
	ch := make(chan canonical.Chunk)
	close(ch)
	return &fakeRunHandle{stream: &fakeStream{chunks: ch, final: &canonical.FinalResult{StopReason: canonical.StopEndTurn}}}, nil
}

// RunPostHooks is a no-op — the request-id stamp tests only care about
// the ctx Run receives, not the PostHook chain.
func (c *anthropicCaptureEngine) RunPostHooks(_ context.Context, _ *canonical.ChatRequest, _ *canonical.ChatResponse) error {
	return nil
}

func newAnthropicCaptureAdapter(eng *anthropicCaptureEngine) *Adapter {
	return New(Config{Engine: eng})
}

func TestHandler_StampsRequestIDOnContext_FromHeader(t *testing.T) {
	const wantID = "01HKQ8ABCDEFGHJKMNPQRSTVWX"
	eng := &anthropicCaptureEngine{resp: &canonical.ChatResponse{
		StopReason: canonical.StopEndTurn,
		Message: canonical.Message{
			Role:    canonical.RoleAssistant,
			Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "pong"}},
		},
	}}
	a := newAnthropicCaptureAdapter(eng)

	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
		"/messages",
		strings.NewReader(`{"model":"auto","max_tokens":16,"stream":false,"messages":[{"role":"user","content":"ping"}]}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("anthropic-version", "2023-06-01")
	r.Header.Set("X-Request-Id", wantID)
	w := httptest.NewRecorder()
	a.ProtectedRouter().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	got := plugin.RequestIDFromContext(eng.gotCtx)
	if got != wantID {
		t.Errorf("RequestIDFromContext: got %q, want %q", got, wantID)
	}
}

func TestHandler_StampsRequestIDOnContext_GeneratedWhenAbsent(t *testing.T) {
	eng := &anthropicCaptureEngine{resp: &canonical.ChatResponse{
		StopReason: canonical.StopEndTurn,
		Message: canonical.Message{
			Role:    canonical.RoleAssistant,
			Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "pong"}},
		},
	}}
	a := newAnthropicCaptureAdapter(eng)

	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
		"/messages",
		strings.NewReader(`{"model":"auto","max_tokens":16,"stream":false,"messages":[{"role":"user","content":"ping"}]}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("anthropic-version", "2023-06-01")
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
	const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	for _, c := range got {
		if !strings.ContainsRune(crockford, c) {
			t.Errorf("RequestIDFromContext: got char %q not in Crockford Base32 (id=%q)", c, got)
		}
	}
}
