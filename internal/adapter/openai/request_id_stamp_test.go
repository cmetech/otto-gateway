// Phase 8 Plan 08-05 Task 4b — round-trip test for the X-Request-Id
// ctx-stamp added to handleChatCompletions / handleCompletions.
//
// Asserts:
//   - When the client sends X-Request-Id, the value is propagated to
//     ctx via plugin.WithRequestID and visible to downstream code
//     through plugin.RequestIDFromContext.
//   - When no X-Request-Id is sent, a fresh ULID is minted and stamped.
//
// Uses a dedicated capture-fakeEngine that records the ctx Collect
// received. Whitebox by necessity (Engine interface is unexported).

package openai

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/plugin"
)

// openaiCaptureEngine satisfies the package-private Engine interface
// and records the ctx Collect / Run received.
type openaiCaptureEngine struct {
	gotCtx context.Context
	resp   *canonical.ChatResponse
}

func (c *openaiCaptureEngine) Collect(ctx context.Context, _ *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	c.gotCtx = ctx
	return c.resp, nil
}

func (c *openaiCaptureEngine) Run(ctx context.Context, _ *canonical.ChatRequest) (RunHandle, error) {
	c.gotCtx = ctx
	// Empty-stream RunHandle; we never assert streaming behavior in
	// these tests (Collect is the test target).
	ch := make(chan canonical.Chunk)
	close(ch)
	return &fakeRunHandle{
		stream:    &fakeStream{chunks: ch, final: &canonical.FinalResult{StopReason: canonical.StopEndTurn}},
		sessionID: "session_capture",
	}, nil
}

// RunPostHooks is a no-op — request-id stamp tests only care about ctx.
func (c *openaiCaptureEngine) RunPostHooks(_ context.Context, _ *canonical.ChatRequest, _ *canonical.ChatResponse) error {
	return nil
}

func newOpenAICaptureAdapter(eng *openaiCaptureEngine) *Adapter {
	// discardWriter is declared in adapter.go (production code).
	return New(Config{
		Logger: slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{})),
		Engine: eng,
	})
}

func TestHandler_StampsRequestIDOnContext_FromHeader(t *testing.T) {
	const wantID = "01HKQ8ABCDEFGHJKMNPQRSTVWX"
	eng := &openaiCaptureEngine{resp: &canonical.ChatResponse{
		StopReason: canonical.StopEndTurn,
		Message:    canonical.Message{Role: canonical.RoleAssistant},
	}}
	a := newOpenAICaptureAdapter(eng)
	srv := mountedAdapter(a)
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		srv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"auto","stream":false,"messages":[{"role":"user","content":"ping"}]}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-Id", wantID)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	got := plugin.RequestIDFromContext(eng.gotCtx)
	if got != wantID {
		t.Errorf("RequestIDFromContext: got %q, want %q", got, wantID)
	}
}

func TestHandler_StampsRequestIDOnContext_GeneratedWhenAbsent(t *testing.T) {
	eng := &openaiCaptureEngine{resp: &canonical.ChatResponse{
		StopReason: canonical.StopEndTurn,
		Message:    canonical.Message{Role: canonical.RoleAssistant},
	}}
	a := newOpenAICaptureAdapter(eng)
	srv := mountedAdapter(a)
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		srv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"auto","stream":false,"messages":[{"role":"user","content":"ping"}]}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
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
