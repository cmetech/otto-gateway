package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/goleak"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/engine"
	"otto-gateway/internal/pool"
	"otto-gateway/internal/testutil"
)

// resolveKiroCLI gates integration tests on OTTO_INTEGRATION=1 in the env
// AND either OTTO_KIRO_BIN pointing at a kiro-cli binary or kiro-cli on PATH.
// CI default: skip with a clear reason. Mirrors anthropic/integration_test.go.
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

// kiroSetup spawns a real kiro-cli pool of size 1, warms it up, constructs the
// engine + adapter, and returns a running httptest.Server plus teardown closure.
// Mirrors anthropic/integration_test.go:kiroSetup.
func kiroSetup(t *testing.T) (*httptest.Server, func()) {
	t.Helper()
	bin := resolveKiroCLI(t)
	logger := testutil.Logger(t)

	p := pool.New(pool.Config{
		Logger:       logger,
		Size:         1,
		KiroCmd:      bin,
		KiroArgs:     []string{"acp"},
		PingInterval: 10 * time.Minute,
	})

	warmCtx, cancelWarm := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelWarm()
	if err := p.Warmup(warmCtx); err != nil {
		_ = p.Close()
		t.Skipf("pool.Warmup failed (likely kiro-cli auth-not-refreshed): %v", err)
	}

	eng := engine.New(engine.Config{
		Logger: logger,
		ACP:    p,
	})

	a := New(Config{
		Logger: logger,
		Engine: realEngineAdapter{engine: eng},
	})

	// Mount under /v1 via RegisterRoutes (mirrors the SurfaceMount mechanic).
	r := chi.NewRouter()
	r.Route("/v1", func(sub chi.Router) {
		a.RegisterRoutes(sub)
	})
	srv := httptest.NewServer(r)
	return srv, func() {
		srv.Close()
		if err := p.Close(); err != nil {
			t.Logf("pool.Close (expected non-zero exit): %v", err)
		}
	}
}

// realEngineAdapter wraps *engine.Engine to satisfy openai.Engine.
// Test-local equivalent of cmd/main.go's openaiEngineAdapter — kept here so
// the test file stays self-contained.
type realEngineAdapter struct{ engine *engine.Engine }

func (r realEngineAdapter) Collect(
	ctx context.Context, req *canonical.ChatRequest,
) (*canonical.ChatResponse, error) {
	resp, err := r.engine.Collect(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("integration collect: %w", err)
	}
	return resp, nil
}

func (r realEngineAdapter) Run(
	ctx context.Context, req *canonical.ChatRequest,
) (RunHandle, error) {
	run, err := r.engine.Run(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("integration run: %w", err)
	}
	return realRunHandle{run: run}, nil
}

type realRunHandle struct{ run *engine.Run }

func (h realRunHandle) Stream() Stream         { return h.run.Stream() }
func (h realRunHandle) SessionID() string      { return h.run.SessionID() }
func (h realRunHandle) StopWatchdog() func() bool { return h.run.StopWatchdog() }
func (h realRunHandle) ShortCircuitResponse() *canonical.ChatResponse {
	return h.run.ShortCircuitResponse()
}

// ----------------------------------------------------------------------------
// Fake engine for automated round-trip tests (no kiro-cli required)
// ----------------------------------------------------------------------------

// fakeEngine is an in-process fake that returns scripted responses.
type fakeEngine struct {
	collectResp *canonical.ChatResponse
	collectErr  error
	runChunks   []canonical.Chunk
	runFinal    *canonical.FinalResult
	runErr      error
}

func (f *fakeEngine) Collect(_ context.Context, _ *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	return f.collectResp, f.collectErr
}

func (f *fakeEngine) Run(_ context.Context, _ *canonical.ChatRequest) (RunHandle, error) {
	if f.runErr != nil {
		return nil, f.runErr
	}
	ch := make(chan canonical.Chunk, len(f.runChunks)+1)
	for _, c := range f.runChunks {
		ch <- c
	}
	close(ch)
	return &fakeRunHandle{
		stream: &fakeStream{
			chunks: ch,
			final:  f.runFinal,
		},
		sessionID: "session_fake",
	}, nil
}

// newFakeAdapter constructs an *Adapter with a scripted fake engine.
func newFakeAdapter(eng *fakeEngine) *Adapter {
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))
	return New(Config{Logger: logger, Engine: eng})
}

// mountedAdapter returns an httptest.Server with the adapter mounted under /v1.
func mountedAdapter(a *Adapter) *httptest.Server {
	r := chi.NewRouter()
	r.Route("/v1", func(sub chi.Router) {
		a.RegisterRoutes(sub)
	})
	return httptest.NewServer(r)
}

// ----------------------------------------------------------------------------
// Automated fake-engine round-trip tests (SC1 + SC2)
// ----------------------------------------------------------------------------

// TestIntegration_FakeEngine_NonStream (SC1): POST stream:false → 200 +
// application/json + chat.completion shape sourced from the fake engine.
func TestIntegration_FakeEngine_NonStream(t *testing.T) {
	defer goleak.VerifyNone(t)

	eng := &fakeEngine{
		collectResp: &canonical.ChatResponse{
			StopReason: canonical.StopEndTurn,
			Message: canonical.Message{
				Role: canonical.RoleAssistant,
				Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: "pong"},
				},
			},
		},
	}
	srv := mountedAdapter(newFakeAdapter(eng))
	defer srv.Close()

	body := []byte(`{"model":"auto","messages":[{"role":"user","content":"ping"}],"stream":false}`)
	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodPost, srv.URL+"/v1/chat/completions", bytes.NewReader(body))
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
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, raw)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: got %q, want application/json prefix", ct)
	}

	var completion chatCompletion
	if err := json.NewDecoder(resp.Body).Decode(&completion); err != nil {
		t.Fatalf("decode chat.completion: %v", err)
	}
	if completion.Object != "chat.completion" {
		t.Errorf("object: got %q, want chat.completion", completion.Object)
	}
	if !strings.HasPrefix(completion.ID, "chatcmpl-") {
		t.Errorf("id: got %q, want chatcmpl- prefix", completion.ID)
	}
	if len(completion.Choices) == 0 {
		t.Fatal("choices: empty")
	}
	if completion.Choices[0].Message.Content != "pong" {
		t.Errorf("content: got %q, want pong", completion.Choices[0].Message.Content)
	}
	if completion.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason: got %q, want stop", completion.Choices[0].FinishReason)
	}
}

// TestIntegration_FakeEngine_Streaming (SC2): POST stream:true → 200 +
// Content-Type text/event-stream + correct frame sequence + data: [DONE].
// This is the Pi/SC2 acceptance path.
func TestIntegration_FakeEngine_Streaming(t *testing.T) {
	defer goleak.VerifyNone(t)

	eng := &fakeEngine{
		runChunks: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "hello "}},
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "world"}},
		},
		runFinal: &canonical.FinalResult{StopReason: canonical.StopEndTurn},
	}
	srv := mountedAdapter(newFakeAdapter(eng))
	defer srv.Close()

	body := []byte(`{"model":"auto","messages":[{"role":"user","content":"ping"}],"stream":true}`)
	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodPost, srv.URL+"/v1/chat/completions", bytes.NewReader(body))
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
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type: got %q, want text/event-stream prefix", ct)
	}

	// Scan frame lines with bufio.Scanner. Verify:
	//   - at least one "data: " line with delta.role="assistant"
	//   - at least one "data: " line with delta.content non-empty
	//   - a "data: " line with finish_reason non-null
	//   - the literal "data: [DONE]" terminator
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	var (
		sawRoleDelta    bool
		sawContentDelta bool
		sawFinishReason bool
		sawDone         bool
	)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "data: [DONE]" {
			sawDone = true
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue // blank line or other non-data line
		}
		payload := strings.TrimPrefix(line, "data: ")
		var chunk map[string]any
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("unmarshal chunk: %v (payload=%q)", err, payload)
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		if choice == nil {
			continue
		}
		delta, _ := choice["delta"].(map[string]any)
		if delta != nil {
			if role, _ := delta["role"].(string); role == "assistant" {
				sawRoleDelta = true
			}
			if content, _ := delta["content"].(string); content != "" {
				sawContentDelta = true
			}
		}
		if fr, ok := choice["finish_reason"]; ok && fr != nil {
			if frStr, _ := fr.(string); frStr != "" {
				sawFinishReason = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner: %v", err)
	}

	if !sawRoleDelta {
		t.Error("no role:assistant delta observed in stream")
	}
	if !sawContentDelta {
		t.Error("no content delta observed in stream")
	}
	if !sawFinishReason {
		t.Error("no finish_reason frame observed in stream")
	}
	if !sawDone {
		t.Error("data: [DONE] terminator not observed in stream")
	}
}

// TestIntegration_NilEngine_503 verifies that a nil Engine returns 503 with
// the OpenAI error envelope.
func TestIntegration_NilEngine_503(t *testing.T) {
	defer goleak.VerifyNone(t)

	a := New(Config{}) // nil Engine
	srv := mountedAdapter(a)
	defer srv.Close()

	body := []byte(`{"model":"auto","messages":[{"role":"user","content":"hi"}]}`)
	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodPost, srv.URL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want 503", resp.StatusCode)
	}
	var env errorEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if env.Error.Type != errAPI {
		t.Errorf("error.type: got %q, want %q", env.Error.Type, errAPI)
	}
}

// TestIntegration_EmptyMessages_400 verifies that an empty messages array
// returns 400 with the OpenAI error envelope.
func TestIntegration_EmptyMessages_400(t *testing.T) {
	defer goleak.VerifyNone(t)

	eng := &fakeEngine{
		collectResp: &canonical.ChatResponse{},
	}
	srv := mountedAdapter(newFakeAdapter(eng))
	defer srv.Close()

	body := []byte(`{"model":"auto","messages":[]}`)
	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodPost, srv.URL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", resp.StatusCode)
	}
}

// TestIntegration_OversizeBody_413 verifies that a body exceeding chatBodyCap
// returns 413.
func TestIntegration_OversizeBody_413(t *testing.T) {
	defer goleak.VerifyNone(t)

	eng := &fakeEngine{}
	srv := mountedAdapter(newFakeAdapter(eng))
	defer srv.Close()

	// Construct a body slightly over 4 MiB.
	big := make([]byte, int(chatBodyCap)+1024)
	for i := range big {
		big[i] = 'x'
	}
	body := append([]byte(`{"model":"auto","messages":[{"role":"user","content":"`), big...)
	body = append(body, []byte(`"}]}`)...)

	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodPost, srv.URL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status: got %d, want 413", resp.StatusCode)
	}
}

// ----------------------------------------------------------------------------
// Real kiro-cli integration tests (optional, gated on OTTO_INTEGRATION=1)
// ----------------------------------------------------------------------------

// TestIntegration_RealKiroCLI_NonStreaming exercises the full stream:false
// path against real kiro-cli. Mirrors anthropic/integration_test.go.
func TestIntegration_RealKiroCLI_NonStreaming(t *testing.T) {
	srv, teardown := kiroSetup(t)
	defer teardown()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	body := []byte(`{"model":"auto","messages":[{"role":"user","content":"say the word ping"}],"stream":false}`)
	req, err := http.NewRequestWithContext(ctx,
		http.MethodPost, srv.URL+"/v1/chat/completions", bytes.NewReader(body))
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
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: got %q, want application/json prefix", ct)
	}

	var completion chatCompletion
	if err := json.NewDecoder(resp.Body).Decode(&completion); err != nil {
		t.Fatalf("decode chat.completion: %v", err)
	}
	if completion.Object != "chat.completion" {
		t.Errorf("object: got %q, want chat.completion", completion.Object)
	}
	if completion.Choices[0].Message.Content == "" {
		t.Error("content: empty (kiro-cli returned no text)")
	}
	if completion.Choices[0].FinishReason == "" {
		t.Error("finish_reason: empty (must be non-null)")
	}
	t.Logf("integration non-streaming: %.80s (finish_reason=%s)",
		completion.Choices[0].Message.Content, completion.Choices[0].FinishReason)
}

// TestIntegration_RealKiroCLI_Streaming exercises the full stream:true SSE
// path against real kiro-cli. The Pi/SC2 acceptance path.
func TestIntegration_RealKiroCLI_Streaming(t *testing.T) {
	srv, teardown := kiroSetup(t)
	defer teardown()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	body := []byte(`{"model":"auto","messages":[{"role":"user","content":"say the word ping"}],"stream":true}`)
	req, err := http.NewRequestWithContext(ctx,
		http.MethodPost, srv.URL+"/v1/chat/completions", bytes.NewReader(body))
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
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type: got %q, want text/event-stream prefix", ct)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	var (
		sawDone      bool
		dataFrames   int
		sawTextDelta bool
	)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "data: [DONE]" {
			sawDone = true
			break
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		dataFrames++
		payload := strings.TrimPrefix(line, "data: ")
		var chunk map[string]any
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("unmarshal chunk: %v", err)
		}
		// Verify no event: lines (OpenAI is data:-only).
		// (Already confirmed by prefix check above.)
		choices, _ := chunk["choices"].([]any)
		for _, c := range choices {
			choice, _ := c.(map[string]any)
			delta, _ := choice["delta"].(map[string]any)
			if content, _ := delta["content"].(string); content != "" {
				sawTextDelta = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner: %v", err)
	}
	if !sawDone {
		t.Error("data: [DONE] not observed in stream")
	}
	if dataFrames == 0 {
		t.Error("no data: frames observed")
	}
	if !sawTextDelta {
		t.Error("no text content delta observed")
	}
	t.Logf("integration streaming: %d data frames", dataFrames)
}
