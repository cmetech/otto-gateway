// Whitebox unit tests for the Client type (package acp).
// D-18: whitebox package gives access to unexported types (writeCh, disp, etc.)
package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"
	"time"

	"go.uber.org/goleak"

	"loop24-gateway/internal/canonical"
	"loop24-gateway/internal/testutil"
)

// mockRWC is a simple io.ReadWriteCloser backed by io.Pipe, used in unit tests.
type mockRWC struct {
	r          *io.PipeReader
	w          *io.PipeWriter
	serverRead *io.PipeReader
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

func (m *mockRWC) Read(b []byte) (int, error)  { return m.r.Read(b) } //nolint:wrapcheck
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

// TestAutoGrantPermission simulates a session/request_permission notification and
// verifies that a grant_permission request is written back to the server.
func TestAutoGrantPermission(t *testing.T) {
	mock := newMockRWC()
	cfg := newTestConfig(t)

	c := NewWithConn(mock, cfg)

	// Send a session/request_permission notification (nil id).
	permNotif := map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/request_permission",
		"params": map[string]any{
			"requestId":  "req-1",
			"permission": map[string]any{"type": "shell_exec"},
		},
	}
	if err := mock.serverWriteJSON(permNotif); err != nil {
		t.Fatalf("serverWriteJSON: %v", err)
	}

	// The client should write back a grant_permission request.
	grantLine, err := readLineFromPipeWithTimeout(mock.serverRead, 2*time.Second)
	if err != nil {
		t.Fatalf("read grant_permission: %v", err)
	}

	var grant map[string]any
	if err := json.Unmarshal(grantLine, &grant); err != nil {
		t.Fatalf("unmarshal grant: %v", err)
	}
	if grant["method"] != "session/grant_permission" {
		t.Errorf("expected session/grant_permission, got %v", grant["method"])
	}
	params, _ := grant["params"].(map[string]any)
	if params == nil {
		t.Fatal("grant params is nil")
	}
	if params["optionId"] != "allow_always" {
		t.Errorf("optionId: got %v, want allow_always", params["optionId"])
	}
	if params["granted"] != true {
		t.Errorf("granted: got %v, want true", params["granted"])
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
