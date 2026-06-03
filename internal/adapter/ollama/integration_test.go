package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/engine"
	"otto-gateway/internal/pool"
	"otto-gateway/internal/testutil"
)

// testEngineAdapter adapts *engine.Engine to ollama.Engine for integration
// tests. Mirrors cmd/otto-gateway/main.go's ollamaEngineAdapter — exists here
// because integration_test.go is whitebox (package ollama) and cannot use the
// cmd-level shim directly.
type testEngineAdapter struct{ eng *engine.Engine }

func (a testEngineAdapter) Collect(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	resp, err := a.eng.Collect(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("integration collect: %w", err)
	}
	return resp, nil
}

func (a testEngineAdapter) Run(ctx context.Context, req *canonical.ChatRequest) (RunHandle, error) {
	run, err := a.eng.Run(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("integration run: %w", err)
	}
	return testRunHandleAdapter{run: run}, nil
}

// RunPostHooks delegates to *engine.Engine.RunPostHooks (quick
// 260530-df2) so the integration adapter satisfies the expanded Engine
// interface.
func (a testEngineAdapter) RunPostHooks(ctx context.Context, req *canonical.ChatRequest, resp *canonical.ChatResponse) error {
	return a.eng.RunPostHooks(ctx, req, resp)
}

// CollectFromRun delegates to *engine.Engine.CollectFromRun (T-5b). The
// testRunHandleAdapter type-asserts back to recover the concrete
// *engine.Run.
func (a testEngineAdapter) CollectFromRun(ctx context.Context, run RunHandle, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	h, ok := run.(testRunHandleAdapter)
	if !ok {
		return nil, fmt.Errorf("integration collect from run: unexpected RunHandle type %T", run)
	}
	resp, err := a.eng.CollectFromRun(ctx, h.run, req)
	if err != nil {
		return nil, fmt.Errorf("integration collect from run: %w", err)
	}
	return resp, nil
}

type testRunHandleAdapter struct{ run *engine.Run }

func (h testRunHandleAdapter) Stream() Stream            { return h.run.Stream() }
func (h testRunHandleAdapter) SessionID() string         { return h.run.SessionID() }
func (h testRunHandleAdapter) StopWatchdog() func() bool { return h.run.StopWatchdog() }
func (h testRunHandleAdapter) ShortCircuitResponse() *canonical.ChatResponse {
	return h.run.ShortCircuitResponse()
}

// resolveKiroCLI gates integration tests on (1) OTTO_INTEGRATION=1 in
// the env AND (2) either OTTO_KIRO_BIN pointing at a kiro-cli binary
// or kiro-cli being discoverable on PATH. Mirrors the Phase 1
// internal/acp/integration_test.go pattern verbatim.
func resolveKiroCLI(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("OTTO_KIRO_BIN"); bin != "" {
		return bin
	}
	if os.Getenv("OTTO_INTEGRATION") != "1" {
		t.Skip("set OTTO_INTEGRATION=1 to run integration tests")
	}
	p, err := exec.LookPath("kiro-cli")
	if err != nil {
		t.Skip("kiro-cli not on PATH (set OTTO_KIRO_BIN to override)")
	}
	return p
}

// TestIntegration_ChatEndToEnd exercises the full Phase 2 acceptance
// path against real kiro-cli: spawn a 1-slot pool → wire engine →
// construct adapter → mount on httptest.NewServer → POST /api/chat →
// assert Ollama-shape response.
//
// Whitebox (package ollama) per the locked Task 1 decision — uses
// ollamaChatResponse from wire.go directly so the wire contract owns
// the assertion.
func TestIntegration_ChatEndToEnd(t *testing.T) {
	bin := resolveKiroCLI(t)

	logger := testutil.Logger(t)

	// Pool of 1 — Phase 2 default.
	p := pool.New(pool.Config{
		Logger:       logger,
		Size:         1,
		KiroCmd:      bin,
		KiroArgs:     []string{"acp"},
		PingInterval: 10 * time.Minute, // disable periodic ping during test
	})
	defer func() {
		if err := p.Close(); err != nil {
			t.Logf("pool.Close (expected non-zero exit): %v", err)
		}
	}()

	warmCtx, cancelWarm := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelWarm()
	if err := p.Warmup(warmCtx); err != nil {
		t.Skipf("pool.Warmup failed (likely kiro-cli auth-not-refreshed): %v", err)
	}

	eng := engine.New(engine.Config{
		Logger: logger,
		ACP:    p,
	})

	adapter := New(Config{
		Logger:       logger,
		Engine:       testEngineAdapter{eng: eng},
		ModelCatalog: p,
		Version:      "test",
		Commit:       "deadbee",
	})

	// httptest.NewServer binds an ephemeral port — never hardcode 11434
	// here (forbidden by the plan).
	srv := httptest.NewServer(adapter.ProtectedRouter())
	defer srv.Close()

	// 30-second timeout overall (LLM response budget).
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	body := []byte(`{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/chat", bytes.NewReader(body))
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
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}

	var out ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.Message.Role != "assistant" {
		t.Errorf("message.role: got %q, want assistant", out.Message.Role)
	}
	if out.Message.Content == "" {
		t.Error("message.content: empty (kiro-cli did not return text)")
	}
	if !out.Done {
		t.Error("done: got false, want true")
	}
	if out.DoneReason != "stop" && out.DoneReason != "length" {
		t.Errorf("done_reason: got %q, want stop or length", out.DoneReason)
	}
	if out.TotalDuration <= 0 {
		t.Errorf("total_duration: got %d, want > 0", out.TotalDuration)
	}
	t.Logf("integration response: %.80s (done_reason=%s, total_duration=%dns)",
		out.Message.Content, out.DoneReason, out.TotalDuration)
}

// TestIntegration_TagsEndpoint — secondary integration check that
// GET /api/tags returns a non-empty models[] containing "auto" plus at
// least one kiro-reported model.
func TestIntegration_TagsEndpoint(t *testing.T) {
	bin := resolveKiroCLI(t)

	logger := testutil.Logger(t)

	p := pool.New(pool.Config{
		Logger:       logger,
		Size:         1,
		KiroCmd:      bin,
		KiroArgs:     []string{"acp"},
		PingInterval: 10 * time.Minute,
	})
	defer func() {
		if err := p.Close(); err != nil {
			t.Logf("pool.Close (expected non-zero exit): %v", err)
		}
	}()

	warmCtx, cancelWarm := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelWarm()
	if err := p.Warmup(warmCtx); err != nil {
		t.Skipf("pool.Warmup failed: %v", err)
	}

	adapter := New(Config{
		Logger:       logger,
		ModelCatalog: p,
		Version:      "test",
		Commit:       "deadbee",
	})

	srv := httptest.NewServer(adapter.ProtectedRouter())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/tags", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	var out ollamaTagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Models) < 2 {
		t.Errorf("models len: got %d, want >= 2 (auto + at least one kiro model)", len(out.Models))
	}
	if out.Models[0].Name != "auto" {
		t.Errorf("models[0]: got %q, want auto (must be prepended)", out.Models[0].Name)
	}
}
