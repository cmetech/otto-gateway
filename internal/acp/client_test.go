// Whitebox unit tests for the Client type (package acp).
// D-18: whitebox package gives access to unexported types (writeCh, disp, etc.)
package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/testutil"
)

// mockRWC is a simple io.ReadWriteCloser backed by io.Pipe, used in unit tests.
type mockRWC struct {
	r           *io.PipeReader
	w           *io.PipeWriter
	serverRead  *io.PipeReader
	serverWrite *io.PipeWriter
}

// newMockRWC creates a bidirectional pipe pair.
// clientRWC is passed to NewWithConn.
// serverRead/serverWrite are the test's side for sending/receiving frames.
func newMockRWC() *mockRWC {
	// client reads from clientRead, writes to clientWrite
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()
	return &mockRWC{
		r:           clientRead,
		w:           clientWrite,
		serverRead:  serverRead,
		serverWrite: serverWrite,
	}
}

func (m *mockRWC) Read(b []byte) (int, error)  { return m.r.Read(b) }  //nolint:wrapcheck
func (m *mockRWC) Write(b []byte) (int, error) { return m.w.Write(b) } //nolint:wrapcheck
func (m *mockRWC) Close() error {
	rerr := m.r.Close()
	werr := m.w.Close()
	if rerr != nil {
		return rerr //nolint:wrapcheck
	}
	return werr //nolint:wrapcheck
}

// serverWriteJSON writes a JSON line to serverWrite (simulating kiro-cli → client).
func (m *mockRWC) serverWriteJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err //nolint:wrapcheck
	}
	data = append(data, '\n')
	_, err = m.serverWrite.Write(data)
	return err //nolint:wrapcheck
}

// serverClose closes the server side so the client scanner sees EOF.
func (m *mockRWC) serverClose() {
	_ = m.serverWrite.Close()
	_ = m.serverRead.Close()
}

func newTestConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		Logger:       testutil.Logger(t),
		Command:      "kiro-cli",
		Args:         []string{"acp"},
		PingInterval: 10 * time.Minute, // disable ping in unit tests
	}
}

// TestNewWithConn creates a Client via NewWithConn, simulates an initialize response,
// verifies Initialize returns nil, and verifies Close() returns nil.
func TestNewWithConn(t *testing.T) {
	mock := newMockRWC()
	cfg := newTestConfig(t)

	c := NewWithConn(mock, cfg)

	// Simulate initialize response from the server side.
	idCh := make(chan uint64, 1)
	go func() {
		// Read the initialize request.
		line, err := readLineFromPipe(mock.serverRead)
		if err != nil {
			t.Errorf("serverRead initialize: %v", err)
			return
		}
		var req map[string]any
		if err := json.Unmarshal(line, &req); err != nil {
			t.Errorf("unmarshal initialize: %v", err)
			return
		}
		id := req["id"]
		_ = id
		if err := mock.serverWriteJSON(map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"result":  map[string]any{"capabilities": map[string]any{}},
		}); err != nil {
			t.Errorf("serverWrite initialize response: %v", err)
		}
		idCh <- 0
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	<-idCh
	mock.serverClose()

	if err := c.Close(); err != nil {
		// Subprocess-related errors are expected (no cmd.Wait); cmd-path errors don't apply here.
		t.Logf("Close (expected minor error from pipe): %v", err)
	}
	goleak.VerifyNone(t)
}

// TestNewWithConnCloseUnblocksScanner creates a Client via NewWithConn with io.Pipe
// but does NOT write any response. Calls Close() and verifies it returns within 2s.
// This proves rwc.Close() unblocks the readLoop scanner (the original deadlock).
func TestNewWithConnCloseUnblocksScanner(t *testing.T) {
	mock := newMockRWC()
	cfg := newTestConfig(t)

	c := NewWithConn(mock, cfg)

	done := make(chan error, 1)
	go func() {
		done <- c.Close()
	}()

	select {
	case err := <-done:
		// Close returned — good. Minor pipe errors are expected.
		t.Logf("Close returned: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("Close() did not return within 2s — scanner was not unblocked by rwc.Close()")
	}
	goleak.VerifyNone(t)
}

// TestCloseIdempotent calls Close() twice and verifies no panic or hang.
func TestCloseIdempotent(t *testing.T) {
	mock := newMockRWC()
	cfg := newTestConfig(t)

	c := NewWithConn(mock, cfg)
	_ = c.Close()
	_ = c.Close() // second call must be a no-op
	goleak.VerifyNone(t)
}

// TestPingTimeout calls Ping with a pre-cancelled context and verifies error is context.Canceled.
func TestPingTimeout(t *testing.T) {
	mock := newMockRWC()
	cfg := newTestConfig(t)

	c := NewWithConn(mock, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	err := c.Ping(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}

	mock.serverClose()
	_ = c.Close()
	goleak.VerifyNone(t)
}

// TestPendingRequestsFailedOnClose verifies that when Close() is called while an RPC is
// in-flight, the caller receives ErrClientClosed (not a hang).
// REVIEW FIX (Codex HIGH): TestPendingRequestsFailedOnClose.
func TestPendingRequestsFailedOnClose(t *testing.T) {
	mock := newMockRWC()
	cfg := newTestConfig(t)

	c := NewWithConn(mock, cfg)

	// Start Initialize without responding — it will block waiting for a response.
	initResult := make(chan error, 1)
	go func() {
		ctx := context.Background()
		initResult <- c.Initialize(ctx)
	}()

	// Give the goroutine a moment to register in the pending map.
	time.Sleep(50 * time.Millisecond)

	// Close the client — this should drain pending and return ErrClientClosed.
	_ = c.Close()

	select {
	case err := <-initResult:
		if !errors.Is(err, ErrClientClosed) {
			t.Errorf("expected ErrClientClosed, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Initialize did not return ErrClientClosed within 2s after Close()")
	}
	goleak.VerifyNone(t)
}

// TestWriterGoroutine verifies that concurrent RPC sends do not cause data races.
// REVIEW FIX (Codex HIGH / ACP-02): writer goroutine serialises all framer.writeFrame calls.
func TestWriterGoroutine(t *testing.T) {
	mock := newMockRWC()
	cfg := newTestConfig(t)

	c := NewWithConn(mock, cfg)

	// Drain the server side in a goroutine to prevent blocking.
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		buf := make([]byte, 4096)
		for {
			if _, err := mock.serverRead.Read(buf); err != nil {
				return
			}
		}
	}()

	// Send 10 concurrent Ping requests (they will be sent but no response — just testing write).
	const n = 10
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			_ = c.Ping(ctx) // will time out, but we only care about write safety
			done <- struct{}{}
		}()
	}

	// Wait for all sends to complete (via timeout).
	for i := 0; i < n; i++ {
		<-done
	}

	mock.serverClose()
	_ = c.Close()
	<-serverDone
	goleak.VerifyNone(t)
}

// TestAutoGrantPermission verifies the Plan 1.1-04 D-20 contract: when the
// fake server sends a session/request_permission REQUEST (with an id), the
// client RESPONDS on that same id with an rpcResponse envelope carrying
// result.optionId == "allow_always" and result.granted == true. The Phase 1
// grant-permission request path is gone.
func TestAutoGrantPermission(t *testing.T) {
	mock := newMockRWC()
	cfg := newTestConfig(t)

	c := NewWithConn(mock, cfg)

	// Send a session/request_permission REQUEST (with id 42) — not a
	// notification. D-20: kiro-cli waits for a response to this id.
	const permFrameID = float64(42)
	permReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      permFrameID,
		"method":  "session/request_permission",
		"params": map[string]any{
			"requestId":  "req-1",
			"permission": map[string]any{"type": "shell_exec"},
		},
	}
	if err := mock.serverWriteJSON(permReq); err != nil {
		t.Fatalf("serverWriteJSON: %v", err)
	}

	// The client should write back an rpcResponse: {jsonrpc, id, result} —
	// no method field.
	respLine, err := readLineFromPipeWithTimeout(mock.serverRead, 2*time.Second)
	if err != nil {
		t.Fatalf("read permission response: %v", err)
	}

	var resp map[string]any
	if err := json.Unmarshal(respLine, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	// D-20: absence of `method` is the load-bearing assertion — distinguishes
	// the new response envelope from the legacy grant_permission request.
	if _, hasMethod := resp["method"]; hasMethod {
		t.Errorf("response unexpectedly carries method=%v — expected no method on rpcResponse", resp["method"])
	}
	if resp["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc: got %v, want 2.0", resp["jsonrpc"])
	}
	// JSON numerics decode to float64 in Go's interface{} path.
	gotID, _ := resp["id"].(float64)
	if gotID != permFrameID {
		t.Errorf("response id: got %v, want %v (echoed from inbound request)", resp["id"], permFrameID)
	}
	result, _ := resp["result"].(map[string]any)
	if result == nil {
		t.Fatal("response.result is missing or not a JSON object")
	}
	if result["optionId"] != "allow_always" {
		t.Errorf("result.optionId: got %v, want allow_always", result["optionId"])
	}
	if result["granted"] != true {
		t.Errorf("result.granted: got %v, want true", result["granted"])
	}

	mock.serverClose()
	_ = c.Close()
	goleak.VerifyNone(t)
}

// TestSessionUpdateAfterStreamClose verifies that when a session/update arrives after
// the active stream has been cleared, the client does NOT panic and logs a warning.
// REVIEW FIX (Codex MEDIUM — activeStream invariant).
func TestSessionUpdateAfterStreamClose(t *testing.T) {
	mock := newMockRWC()
	cfg := Config{
		Logger:       testutil.Logger(t),
		Command:      "kiro-cli",
		Args:         []string{"acp"},
		PingInterval: 10 * time.Minute,
	}

	c := NewWithConn(mock, cfg)

	// Ensure no activeStream is set (default nil).
	// Deliver a session/update notification — must not panic.
	update := map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params": map[string]any{
			"sessionId": "no-active-session",
			"type":      "text",
			"content":   "orphaned update",
		},
	}
	if err := mock.serverWriteJSON(update); err != nil {
		t.Fatalf("serverWriteJSON: %v", err)
	}

	// Give client time to process the notification.
	time.Sleep(100 * time.Millisecond)

	// If we reach here without panic, the invariant is respected.
	mock.serverClose()
	_ = c.Close()
	goleak.VerifyNone(t)
}

// TestSetModel sends a session/set_model request via a mock RWC and verifies success.
func TestSetModel(t *testing.T) {
	mock := newMockRWC()
	cfg := newTestConfig(t)

	c := NewWithConn(mock, cfg)

	// Simulate server responding to session/set_model.
	go func() {
		line, err := readLineFromPipe(mock.serverRead)
		if err != nil {
			return
		}
		var req map[string]any
		if err := json.Unmarshal(line, &req); err != nil {
			return
		}
		if err := mock.serverWriteJSON(map[string]any{
			"jsonrpc": "2.0",
			"id":      req["id"],
			"result":  map[string]any{},
		}); err != nil {
			t.Errorf("serverWrite: %v", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := c.SetModel(ctx, "test-session", "claude-sonnet"); err != nil {
		t.Errorf("SetModel: %v", err)
	}

	mock.serverClose()
	_ = c.Close()
	goleak.VerifyNone(t)
}

// TestSetModelCancelledContext calls SetModel with a pre-cancelled context and verifies error.
func TestSetModelCancelledContext(t *testing.T) {
	mock := newMockRWC()
	cfg := newTestConfig(t)

	c := NewWithConn(mock, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := c.SetModel(ctx, "test-session", "model")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}

	mock.serverClose()
	_ = c.Close()
	goleak.VerifyNone(t)
}

// TestInitialize_CapturesPromptCapabilities verifies the Plan 1.1-02 D-09
// contract: after a successful Initialize, the client surfaces the agent's
// promptCapabilities via the PromptCapabilities() accessor. Three sub-tests
// cover the three relevant states.
//
// Layout note: each sub-test stands up its own mockRWC + responder goroutine
// because newFakeACPServer (in fakeacp_test.go) lives in the blackbox
// acp_test package and isn't reachable from this whitebox file.
func TestInitialize_CapturesPromptCapabilities(t *testing.T) {
	t.Run("before_initialize", func(t *testing.T) {
		mock := newMockRWC()
		cfg := newTestConfig(t)

		c := NewWithConn(mock, cfg)
		defer func() {
			mock.serverClose()
			_ = c.Close()
			goleak.VerifyNone(t)
		}()

		// Without calling Initialize, the accessor must return the zero value
		// (all false) per D-09 defensive-parse contract.
		got := c.PromptCapabilities()
		want := canonical.PromptCapabilities{}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("PromptCapabilities() before Initialize: got %+v, want zero %+v", got, want)
		}
		if models := c.AvailableModels(); models != nil {
			t.Errorf("AvailableModels() before NewSession: got %+v, want nil", models)
		}
	})

	t.Run("after_initialize_captures_caps", func(t *testing.T) {
		mock := newMockRWC()
		cfg := newTestConfig(t)

		c := NewWithConn(mock, cfg)
		defer func() {
			mock.serverClose()
			_ = c.Close()
			goleak.VerifyNone(t)
		}()

		// Responder reads the initialize request line and writes back a result
		// containing the full promptCapabilities object (D-09).
		serveErr := make(chan error, 1)
		go func() {
			line, err := readLineFromPipe(mock.serverRead)
			if err != nil {
				serveErr <- fmt.Errorf("read initialize: %w", err)
				return
			}
			var req map[string]any
			if err := json.Unmarshal(line, &req); err != nil {
				serveErr <- fmt.Errorf("unmarshal initialize: %w", err)
				return
			}
			if err := mock.serverWriteJSON(map[string]any{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result": map[string]any{
					"protocolVersion": 1,
					"agentCapabilities": map[string]any{
						"promptCapabilities": map[string]any{
							"image":           true,
							"audio":           false,
							"embeddedContext": true,
						},
					},
				},
			}); err != nil {
				serveErr <- fmt.Errorf("write initialize response: %w", err)
				return
			}
			serveErr <- nil
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := c.Initialize(ctx); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		if err := <-serveErr; err != nil {
			t.Fatalf("responder: %v", err)
		}

		got := c.PromptCapabilities()
		want := canonical.PromptCapabilities{
			Image:           true,
			Audio:           false,
			EmbeddedContext: true,
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("PromptCapabilities() after Initialize: got %+v, want %+v", got, want)
		}

		// AvailableModels stays nil until NewSession runs (Plan 03 wires it).
		if models := c.AvailableModels(); models != nil {
			t.Errorf("AvailableModels() before NewSession: got %+v, want nil", models)
		}
	})

	t.Run("empty_caps_yields_zero_value", func(t *testing.T) {
		mock := newMockRWC()
		cfg := newTestConfig(t)

		c := NewWithConn(mock, cfg)
		defer func() {
			mock.serverClose()
			_ = c.Close()
			goleak.VerifyNone(t)
		}()

		// Responder writes back an empty result — D-09 says this must NOT
		// be an error; the caps stay at zero.
		serveErr := make(chan error, 1)
		go func() {
			line, err := readLineFromPipe(mock.serverRead)
			if err != nil {
				serveErr <- fmt.Errorf("read initialize: %w", err)
				return
			}
			var req map[string]any
			if err := json.Unmarshal(line, &req); err != nil {
				serveErr <- fmt.Errorf("unmarshal initialize: %w", err)
				return
			}
			if err := mock.serverWriteJSON(map[string]any{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result":  map[string]any{},
			}); err != nil {
				serveErr <- fmt.Errorf("write initialize response: %w", err)
				return
			}
			serveErr <- nil
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := c.Initialize(ctx); err != nil {
			t.Fatalf("Initialize (empty caps): %v", err)
		}
		if err := <-serveErr; err != nil {
			t.Fatalf("responder: %v", err)
		}

		got := c.PromptCapabilities()
		want := canonical.PromptCapabilities{}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("PromptCapabilities() with empty result: got %+v, want zero %+v", got, want)
		}
	})
}

// TestNewSession_PopulatesAvailableModels verifies the Plan 1.1-03 D-12
// contract: NewSession parses result.models.availableModels[] into
// []canonical.ModelInfo and surfaces the slice via AvailableModels().
func TestNewSession_PopulatesAvailableModels(t *testing.T) {
	mock := newMockRWC()
	cfg := newTestConfig(t)

	c := NewWithConn(mock, cfg)
	defer func() {
		mock.serverClose()
		_ = c.Close()
		goleak.VerifyNone(t)
	}()

	// Responder for session/new — returns sessionId AND an availableModels array.
	serveErr := make(chan error, 1)
	go func() {
		line, err := readLineFromPipe(mock.serverRead)
		if err != nil {
			serveErr <- fmt.Errorf("read session/new: %w", err)
			return
		}
		var req map[string]any
		if err := json.Unmarshal(line, &req); err != nil {
			serveErr <- fmt.Errorf("unmarshal session/new: %w", err)
			return
		}
		if err := mock.serverWriteJSON(map[string]any{
			"jsonrpc": "2.0",
			"id":      req["id"],
			"result": map[string]any{
				"sessionId": "sess-1",
				"models": map[string]any{
					"availableModels": []map[string]any{
						{"modelId": "claude-sonnet-4-7", "name": "Claude Sonnet 4.7"},
						{"modelId": "gpt-4o", "name": "GPT-4o"},
					},
					"currentModelId": "auto",
				},
			},
		}); err != nil {
			serveErr <- fmt.Errorf("write response: %w", err)
			return
		}
		serveErr <- nil
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sid, err := c.NewSession(ctx, "/tmp")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := <-serveErr; err != nil {
		t.Fatalf("responder: %v", err)
	}
	if sid != "sess-1" {
		t.Errorf("sessionID: got %q, want %q", sid, "sess-1")
	}
	got := c.AvailableModels()
	want := []canonical.ModelInfo{
		{ID: "claude-sonnet-4-7", Name: "Claude Sonnet 4.7"},
		{ID: "gpt-4o", Name: "GPT-4o"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AvailableModels: got %+v, want %+v", got, want)
	}
}

// TestNewSession_FallsBackToResultID locks the D-11 fallback contract:
// when the result envelope carries `id` instead of `sessionId`, NewSession
// returns that id rather than erroring.
func TestNewSession_FallsBackToResultID(t *testing.T) {
	mock := newMockRWC()
	cfg := newTestConfig(t)

	c := NewWithConn(mock, cfg)
	defer func() {
		mock.serverClose()
		_ = c.Close()
		goleak.VerifyNone(t)
	}()

	serveErr := make(chan error, 1)
	go func() {
		line, err := readLineFromPipe(mock.serverRead)
		if err != nil {
			serveErr <- fmt.Errorf("read session/new: %w", err)
			return
		}
		var req map[string]any
		if err := json.Unmarshal(line, &req); err != nil {
			serveErr <- fmt.Errorf("unmarshal session/new: %w", err)
			return
		}
		// Note: no `sessionId` field — only `id`.
		if err := mock.serverWriteJSON(map[string]any{
			"jsonrpc": "2.0",
			"id":      req["id"],
			"result": map[string]any{
				"id": "sess-fallback",
			},
		}); err != nil {
			serveErr <- fmt.Errorf("write response: %w", err)
			return
		}
		serveErr <- nil
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sid, err := c.NewSession(ctx, "/tmp")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := <-serveErr; err != nil {
		t.Fatalf("responder: %v", err)
	}
	if sid != "sess-fallback" {
		t.Errorf("sessionID: got %q, want %q", sid, "sess-fallback")
	}
	// No models array was sent — accessor must still return nil.
	if models := c.AvailableModels(); models != nil {
		t.Errorf("AvailableModels (no models in result): got %+v, want nil", models)
	}
}

// TestPrompt_SurfacesStopReason verifies D-07 + D-02: Prompt parses
// result.stopReason and surfaces it through Stream.Result().StopReason.
// Two sub-tests cover the known-string happy path and the unknown-string
// forward-compat path.
func TestPrompt_SurfacesStopReason(t *testing.T) {
	t.Run("end_turn_maps_to_StopEndTurn", func(t *testing.T) {
		runPromptStopReasonScenario(t, "end_turn", canonical.StopEndTurn)
	})
	t.Run("unknown_stop_reason_maps_to_StopUnknown", func(t *testing.T) {
		runPromptStopReasonScenario(t, "banana", canonical.StopUnknown)
	})
}

// runPromptStopReasonScenario drives a full initialize → session/new →
// session/prompt dialogue against a mockRWC and asserts the parsed StopReason
// matches expectations. Used by TestPrompt_SurfacesStopReason's two sub-tests.
func runPromptStopReasonScenario(t *testing.T, wireStop string, want canonical.StopReason) {
	t.Helper()

	mock := newMockRWC()
	cfg := newTestConfig(t)

	c := NewWithConn(mock, cfg)
	defer func() {
		mock.serverClose()
		_ = c.Close()
		goleak.VerifyNone(t)
	}()

	// Scripted responder: initialize → session/new → session/prompt, in order.
	serveErr := make(chan error, 1)
	go func() {
		for _, step := range []string{"initialize", "session/new", "session/prompt"} {
			line, err := readLineFromPipe(mock.serverRead)
			if err != nil {
				serveErr <- fmt.Errorf("read %s: %w", step, err)
				return
			}
			var req map[string]any
			if err := json.Unmarshal(line, &req); err != nil {
				serveErr <- fmt.Errorf("unmarshal %s: %w", step, err)
				return
			}
			gotMethod, _ := req["method"].(string)
			if gotMethod != step {
				serveErr <- fmt.Errorf("expected method %q, got %q", step, gotMethod)
				return
			}
			var result map[string]any
			switch step {
			case "initialize":
				result = map[string]any{}
			case "session/new":
				result = map[string]any{"sessionId": "sess-1"}
			case "session/prompt":
				result = map[string]any{"stopReason": wireStop}
			}
			if err := mock.serverWriteJSON(map[string]any{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result":  result,
			}); err != nil {
				serveErr <- fmt.Errorf("write %s response: %w", step, err)
				return
			}
		}
		serveErr <- nil
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	sid, err := c.NewSession(ctx, "/tmp")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	stream, err := c.Prompt(ctx, sid, []canonical.Block{
		{Kind: canonical.BlockKindText, Text: &canonical.TextBlock{Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	// Drain Chunks (fake sends no chunks; channel closes when Prompt closes
	// the stream). Track the count so the for-range body isn't empty per
	// revive's empty-block lint, even though the value itself is unused.
	var drained int
	for range stream.Chunks {
		drained++
	}
	_ = drained

	result, err := stream.Result()
	if err != nil {
		t.Fatalf("stream.Result: %v", err)
	}
	if result == nil {
		t.Fatal("stream.Result returned nil FinalResult")
	}
	if result.StopReason != want {
		t.Errorf("StopReason: got %v, want %v (wire string %q)", result.StopReason, want, wireStop)
	}

	if err := <-serveErr; err != nil {
		t.Fatalf("responder: %v", err)
	}
}

// TestClient_Done_FiresOnClose verifies the Phase 5 D-01 push-exit signal:
// after Close() returns, Done() must observe a closed channel within 1s.
func TestClient_Done_FiresOnClose(t *testing.T) {
	mock := newMockRWC()
	cfg := newTestConfig(t)

	c := NewWithConn(mock, cfg)
	done := c.Done()

	// Sanity: before Close, Done() is open.
	select {
	case <-done:
		t.Fatal("Done() fired before Close()")
	default:
	}

	mock.serverClose()
	_ = c.Close()

	select {
	case <-done:
		// expected
	case <-time.After(1 * time.Second):
		t.Fatal("Done() did not fire within 1s after Close()")
	}
	goleak.VerifyNone(t)
}

// TestClient_Done_FiresOnPingLoopKill verifies that Done() also fires when the
// readLoop kills the clientCtx (e.g., subprocess exit / pipe EOF). The
// mockRWC.serverClose() path simulates the same EOF path that triggers
// readLoop's defer c.cancel().
func TestClient_Done_FiresOnPingLoopKill(t *testing.T) {
	mock := newMockRWC()
	cfg := newTestConfig(t)

	c := NewWithConn(mock, cfg)
	done := c.Done()

	// Close the server side — readLoop sees EOF and defers c.cancel(),
	// which closes clientCtx and therefore Done().
	mock.serverClose()

	select {
	case <-done:
		// expected — readLoop's defer c.cancel() fired
	case <-time.After(1 * time.Second):
		t.Fatal("Done() did not fire within 1s after server-side EOF")
	}

	_ = c.Close()
	goleak.VerifyNone(t)
}

// TestClient_Done_DoesNotFireBeforeClose verifies that select on Done() with
// a default branch picks default while the client is healthy.
func TestClient_Done_DoesNotFireBeforeClose(t *testing.T) {
	mock := newMockRWC()
	cfg := newTestConfig(t)

	c := NewWithConn(mock, cfg)
	defer func() {
		mock.serverClose()
		_ = c.Close()
		goleak.VerifyNone(t)
	}()

	done := c.Done()

	// Healthy client — Done() must not fire.
	select {
	case <-done:
		t.Fatal("Done() fired on a healthy client")
	default:
		// expected
	}
}

// TestClient_Done_IdempotentMultipleReaders verifies that three concurrent
// readers all observe the close exactly once. context.Done() is a broadcast
// channel: every receiver gets the close signal.
func TestClient_Done_IdempotentMultipleReaders(t *testing.T) {
	mock := newMockRWC()
	cfg := newTestConfig(t)

	c := NewWithConn(mock, cfg)

	const readers = 3
	observed := make(chan struct{}, readers)
	for i := 0; i < readers; i++ {
		go func() {
			<-c.Done()
			observed <- struct{}{}
		}()
	}

	// Trigger close.
	mock.serverClose()
	_ = c.Close()

	for i := 0; i < readers; i++ {
		select {
		case <-observed:
			// expected
		case <-time.After(2 * time.Second):
			t.Fatalf("reader %d did not observe Done() close within 2s", i)
		}
	}
	goleak.VerifyNone(t)
}

// TestClient_CloseDuringInFlightPrompt_FinalizesStreamCleanly covers Phase 8.3
// Pitfall 1 (close-race between awaitPromptResult and readLoop EOF defer).
// Starts a Prompt against a mockRWC that never responds, then calls Close()
// before any session/prompt response can arrive. Asserts that stream.Result()
// returns ErrClientClosed (delivered via failPending's close-sentinel through
// respCh) and that goleak.VerifyNone passes — proving the awaitPromptResult
// goroutine exits via its close-sentinel arm and the s.closeOnce guard
// collapses the readLoop-defer + goroutine close paths to a single state.
func TestClient_CloseDuringInFlightPrompt_FinalizesStreamCleanly(t *testing.T) {
	mock := newMockRWC()
	cfg := newTestConfig(t)

	c := NewWithConn(mock, cfg)
	defer goleak.VerifyNone(t)

	// Drain the mock's server-side writes so the writer goroutine never
	// blocks on a full pipe. We discard the bytes — this test isn't about
	// the wire shape.
	serverDrained := make(chan struct{})
	go func() {
		defer close(serverDrained)
		buf := make([]byte, 4096)
		for {
			if _, err := mock.serverRead.Read(buf); err != nil {
				return
			}
		}
	}()

	ctx := context.Background()
	stream, err := c.Prompt(ctx, "test-sid", []canonical.Block{
		{Kind: canonical.BlockKindText, Text: &canonical.TextBlock{Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if stream == nil {
		t.Fatal("Prompt returned nil Stream — Phase 8.3 non-blocking contract violated")
	}

	// Give awaitPromptResult time to register in the dispatcher's pending map
	// (matches TestPendingRequestsFailedOnClose at client_test.go:202).
	time.Sleep(50 * time.Millisecond)

	// Close — failPending delivers the close-sentinel via respCh; the
	// goroutine's frame arm handles closeSentinelCode → stream.close(nil,
	// ErrClientClosed).
	_ = c.Close()

	// Drain Chunks (none expected) so the range loop below terminates.
	for range stream.Chunks {
		// no chunks expected
	}

	final, err := stream.Result()
	if !errors.Is(err, ErrClientClosed) {
		t.Errorf("stream.Result err: got %v, want ErrClientClosed", err)
	}
	if final != nil && final.StopReason != canonical.StopUnknown {
		t.Errorf("FinalResult.StopReason: got %v, want StopUnknown (no response landed)", final.StopReason)
	}
	mock.serverClose()
	<-serverDrained
}

// TestClient_PromptCtxCancel_FinalizesStreamWithCtxErr covers the ctx-cancel
// arm of awaitPromptResult: when the caller cancels its context mid-flight,
// the goroutine clears c.activeStream, sends a defensive session/cancel
// notification (CONTEXT.md D-01 two-owner pattern), and finalizes the stream
// with a wrapped ctx.Err(). Verifies (a) stream.Result errs with the wrapped
// cancellation, (b) the session/cancel notification reaches the wire (read
// off mockRWC's server-side reader), (c) goleak clean.
func TestClient_PromptCtxCancel_FinalizesStreamWithCtxErr(t *testing.T) {
	mock := newMockRWC()
	cfg := newTestConfig(t)

	c := NewWithConn(mock, cfg)
	defer goleak.VerifyNone(t)

	// Collect every line the client writes to the wire so we can later assert
	// that a session/cancel notification was emitted. We deliberately read
	// line-by-line in a single goroutine so the bytes are ordered and the
	// test can race-free inspect them post-cancel.
	type wireLines struct {
		mu    sync.Mutex
		lines [][]byte
	}
	collected := &wireLines{}
	collectedDone := make(chan struct{})
	go func() {
		defer close(collectedDone)
		var buf []byte
		tmp := make([]byte, 1)
		for {
			n, err := mock.serverRead.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[0])
				if tmp[0] == '\n' {
					line := make([]byte, len(buf))
					copy(line, buf)
					collected.mu.Lock()
					collected.lines = append(collected.lines, line)
					collected.mu.Unlock()
					buf = buf[:0]
				}
			}
			if err != nil {
				return
			}
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := c.Prompt(ctx, "ctx-cancel-sid", []canonical.Block{
		{Kind: canonical.BlockKindText, Text: &canonical.TextBlock{Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if stream == nil {
		t.Fatal("Prompt returned nil Stream — Phase 8.3 non-blocking contract violated")
	}

	// Give awaitPromptResult time to register in the dispatcher and the
	// select to settle on the two arms.
	time.Sleep(50 * time.Millisecond)

	// Cancel — fires ctx.Done arm inside awaitPromptResult.
	cancel()

	// Drain Chunks (none expected).
	for range stream.Chunks {
		// no chunks expected
	}

	_, err = stream.Result()
	if err == nil {
		t.Fatal("stream.Result: got nil err, want wrapped context.Canceled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("stream.Result err: got %v, want errors.Is(context.Canceled)", err)
	}

	// Poll for the session/cancel notification to reach the wire before we
	// tear down the mock. The notification is queued via sendNotification's
	// non-blocking select; the writerLoop drains writeCh on its own goroutine,
	// so the bytes land on serverRead asynchronously. 500ms is generous.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		collected.mu.Lock()
		n := len(collected.lines)
		collected.mu.Unlock()
		// Two writes expected: the session/prompt request + the
		// session/cancel notification. Bail out the moment the second lands.
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Tear down the client cleanly before asserting wire content so the
	// reader goroutine exits and we observe all writes.
	mock.serverClose()
	_ = c.Close()
	<-collectedDone

	// Assert that the wire contained a session/cancel notification with our
	// sessionId. This validates CONTEXT.md D-01 "defensive layer" — the
	// goroutine emits the cancel even though engine.Run's watchdog would
	// duplicate it in production.
	var sawCancel bool
	collected.mu.Lock()
	for _, line := range collected.lines {
		var frame map[string]any
		if err := json.Unmarshal(line, &frame); err != nil {
			continue
		}
		method, _ := frame["method"].(string)
		if method != "session/cancel" {
			continue
		}
		params, _ := frame["params"].(map[string]any)
		if params == nil {
			continue
		}
		if params["sessionId"] == "ctx-cancel-sid" {
			sawCancel = true
			break
		}
	}
	collected.mu.Unlock()
	if !sawCancel {
		t.Errorf("expected session/cancel notification for sessionId=ctx-cancel-sid on the wire; none found in %d lines",
			len(collected.lines))
	}
}

// TestClient_PromptCompletedLog_FieldShape covers the Phase 8.3 D-02
// engine.prompt.completed log line field shape. Drives a full prompt
// round-trip against a mockRWC, captures slog output into a bytes.Buffer,
// and asserts the line contains all three required fields (session_id,
// chunks, stop_reason) plus the raw wire stop_reason value (end_turn).
func TestClient_PromptCompletedLog_FieldShape(t *testing.T) {
	mock := newMockRWC()

	// Wire a Logger that writes structured records into a buffer so we can
	// grep its output post-hoc. slog.NewTextHandler produces key=value pairs
	// which are easy to assert on.
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := Config{
		Logger:       logger,
		Command:      "kiro-cli",
		Args:         []string{"acp"},
		PingInterval: 10 * time.Minute,
	}

	c := NewWithConn(mock, cfg)
	defer func() {
		mock.serverClose()
		_ = c.Close()
		goleak.VerifyNone(t)
	}()

	// Scripted responder: initialize → session/new → session/prompt.
	serveErr := make(chan error, 1)
	go func() {
		for _, step := range []string{"initialize", "session/new", "session/prompt"} {
			line, err := readLineFromPipe(mock.serverRead)
			if err != nil {
				serveErr <- fmt.Errorf("read %s: %w", step, err)
				return
			}
			var req map[string]any
			if err := json.Unmarshal(line, &req); err != nil {
				serveErr <- fmt.Errorf("unmarshal %s: %w", step, err)
				return
			}
			var result map[string]any
			switch step {
			case "initialize":
				result = map[string]any{}
			case "session/new":
				result = map[string]any{"sessionId": "log-shape-sid"}
			case "session/prompt":
				result = map[string]any{"stopReason": "end_turn"}
			}
			if err := mock.serverWriteJSON(map[string]any{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result":  result,
			}); err != nil {
				serveErr <- fmt.Errorf("write %s response: %w", step, err)
				return
			}
		}
		serveErr <- nil
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	sid, err := c.NewSession(ctx, "/tmp")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	stream, err := c.Prompt(ctx, sid, []canonical.Block{
		{Kind: canonical.BlockKindText, Text: &canonical.TextBlock{Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	// Drain Chunks (none from the mock).
	for range stream.Chunks {
	}
	final, err := stream.Result()
	if err != nil {
		t.Fatalf("stream.Result: %v", err)
	}
	if final.StopReason != canonical.StopEndTurn {
		t.Errorf("StopReason: got %v, want StopEndTurn", final.StopReason)
	}
	if err := <-serveErr; err != nil {
		t.Fatalf("responder: %v", err)
	}

	got := buf.String()
	// Three field-shape assertions (slog text format key=value).
	if !strings.Contains(got, "engine.prompt.completed") {
		t.Errorf("captured log missing engine.prompt.completed line; got:\n%s", got)
	}
	if !strings.Contains(got, "session_id=log-shape-sid") {
		t.Errorf("captured log missing session_id=log-shape-sid; got:\n%s", got)
	}
	if !strings.Contains(got, "chunks=0") {
		t.Errorf("captured log missing chunks=0 (mock sent no session/update); got:\n%s", got)
	}
	if !strings.Contains(got, "stop_reason=end_turn") {
		t.Errorf("captured log missing stop_reason=end_turn (raw wire string per Phase 8.3 D-02); got:\n%s", got)
	}
}

// readLineFromPipe reads one newline-terminated line from r.
func readLineFromPipe(r io.Reader) ([]byte, error) {
	return readLineFromPipeWithTimeout(r, 5*time.Second)
}

// readLineFromPipeWithTimeout reads one line with a deadline.
func readLineFromPipeWithTimeout(r io.Reader, timeout time.Duration) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		// Read byte-by-byte until newline (simple for tests).
		var buf []byte
		tmp := make([]byte, 1)
		for {
			n, err := r.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[0])
				if tmp[0] == '\n' {
					ch <- result{buf, nil}
					return
				}
			}
			if err != nil {
				ch <- result{buf, err}
				return
			}
		}
	}()

	select {
	case res := <-ch:
		return res.data, res.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("readLine timeout after %s", timeout)
	}
}
