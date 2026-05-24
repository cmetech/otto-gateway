// Package engine — whitebox tests for the *acp.Client → engine.ACPClient
// wrapper (Codex H-3 Option B). These tests focus on:
//
//  1. Compile-time interface satisfaction (production check is also in
//     acp_adapter.go via `var _ ACPClient = (*acpClientAdapter)(nil)`;
//     this test asserts NewACPClientAdapter's RETURN VALUE satisfies
//     ACPClient at test-build time, providing defense-in-depth).
//  2. acpStreamShim chunk-field delegation: shim.Chunks() returns the
//     SAME underlying channel as *acp.Stream.Chunks, proving no copy /
//     no buffering boundary was introduced.
//  3. acpStreamShim Result translation: shim.Result() returns a
//     *canonical.FinalResult populated with SessionID, ChunkCount,
//     StopReason from the underlying *acp.FinalResult.
//
// We drive a real *acp.Stream end-to-end via acp.NewWithConn + io.Pipe —
// no kiro-cli subprocess required. This exercises the same Prompt code
// path that production traffic does, just with a test-controlled
// JSON-RPC peer.
package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"testing"
	"time"

	"otto-gateway/internal/acp"
	"otto-gateway/internal/canonical"
	"otto-gateway/internal/testutil"
)

// TestACPClientAdapter_Compiles asserts at test-build time that
// NewACPClientAdapter's RETURN VALUE satisfies the ACPClient interface.
// Production code (acp_adapter.go) also has `var _ ACPClient =
// (*acpClientAdapter)(nil)` — this is defense-in-depth.
func TestACPClientAdapter_Compiles(t *testing.T) {
	// Compile-time interface satisfaction assertion. The explicit type
	// annotation matters — `var _ = NewACPClientAdapter(...)` would
	// pass even if the return type drifted. Calling a function that
	// takes ACPClient lets the compiler enforce the constraint.
	asserter := func(_ ACPClient) {}
	asserter(NewACPClientAdapter((*acp.Client)(nil)))
	t.Log("adapter compiles against interface")
}

// mockPipeRWC is an io.ReadWriteCloser backed by two paired io.Pipes
// (one direction each). The acp.Client reads from clientRead and writes
// to clientWrite; the test (acting as the kiro-cli peer) reads from
// serverRead and writes to serverWrite.
type mockPipeRWC struct {
	clientRead  *io.PipeReader
	clientWrite *io.PipeWriter
	serverRead  *io.PipeReader
	serverWrite *io.PipeWriter
}

func newMockPipeRWC() *mockPipeRWC {
	cr, sw := io.Pipe() // server writes → client reads
	sr, cw := io.Pipe() // client writes → server reads
	return &mockPipeRWC{
		clientRead:  cr,
		clientWrite: cw,
		serverRead:  sr,
		serverWrite: sw,
	}
}

func (m *mockPipeRWC) Read(p []byte) (int, error)  { return m.clientRead.Read(p) }   //nolint:wrapcheck // test pipe delegation
func (m *mockPipeRWC) Write(p []byte) (int, error) { return m.clientWrite.Write(p) } //nolint:wrapcheck // test pipe delegation
func (m *mockPipeRWC) Close() error {
	_ = m.clientRead.Close()
	_ = m.clientWrite.Close()
	return nil
}

// serverWriteJSON marshals v and writes it (newline-terminated) on the
// server-side of the pipe pair.
func (m *mockPipeRWC) serverWriteJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err //nolint:wrapcheck // test helper
	}
	data = append(data, '\n')
	_, err = m.serverWrite.Write(data)
	return err //nolint:wrapcheck // test helper
}

// serverReadFrame reads one newline-terminated JSON frame from the
// client → server pipe and returns its parsed shape.
func (m *mockPipeRWC) serverReadFrame(buf *bufio.Reader) (map[string]any, error) {
	line, err := buf.ReadBytes('\n')
	if err != nil {
		return nil, err //nolint:wrapcheck // test helper
	}
	var frame map[string]any
	if err := json.Unmarshal(line, &frame); err != nil {
		return nil, err //nolint:wrapcheck // test helper
	}
	return frame, nil
}

// serverClose closes the server-side pipe pair (peer disconnect).
func (m *mockPipeRWC) serverClose() {
	_ = m.serverWrite.Close()
	_ = m.serverRead.Close()
}

// drivePromptStream brings up an *acp.Client via NewWithConn against a
// mock pipe peer, walks Initialize → NewSession → Prompt to obtain a
// real *acp.Stream that has run a real session/prompt response cycle,
// and returns the stream + client + mock so the caller can inspect the
// shim against the real underlying values.
//
// Drives ONE session/update text chunk into the stream BEFORE the
// session/prompt response closes it, so the FinalResult ends up with
// ChunkCount==1 and StopReason==StopEndTurn.
func drivePromptStream(t *testing.T) (*acp.Stream, *acp.Client, *mockPipeRWC) {
	t.Helper()

	mock := newMockPipeRWC()
	cfg := acp.Config{
		Logger:       testutil.Logger(t),
		Command:      "kiro-cli",
		Args:         []string{"acp"},
		PingInterval: 10 * time.Minute, // disable ping in tests
	}
	client := acp.NewWithConn(mock, cfg)

	// Peer goroutine. Responds to each client frame in sequence:
	// (1) initialize → result with capabilities
	// (2) session/new → result with sessionId + availableModels
	// (3) session/prompt → emit one session/update notification, then
	//     respond to the prompt request id with stopReason=end_turn.
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		bufReader := bufio.NewReader(mock.serverRead)

		// (1) initialize
		initFrame, err := mock.serverReadFrame(bufReader)
		if err != nil {
			t.Logf("peer initialize read: %v", err)
			return
		}
		initID := initFrame["id"]
		if err := mock.serverWriteJSON(map[string]any{
			"jsonrpc": "2.0",
			"id":      initID,
			"result": map[string]any{
				"protocolVersion": 1,
				"agentCapabilities": map[string]any{
					"loadSession":        false,
					"promptCapabilities": map[string]any{"image": true},
				},
			},
		}); err != nil {
			t.Logf("peer initialize write: %v", err)
			return
		}

		// (2) session/new
		newFrame, err := mock.serverReadFrame(bufReader)
		if err != nil {
			t.Logf("peer session/new read: %v", err)
			return
		}
		newID := newFrame["id"]
		if err := mock.serverWriteJSON(map[string]any{
			"jsonrpc": "2.0",
			"id":      newID,
			"result": map[string]any{
				"sessionId": "sess-1",
				"models": map[string]any{
					"availableModels": []map[string]any{
						{"modelId": "claude-sonnet-4-7", "name": "Claude Sonnet"},
					},
					"currentModelId": "claude-sonnet-4-7",
				},
			},
		}); err != nil {
			t.Logf("peer session/new write: %v", err)
			return
		}

		// (3) session/prompt
		promptFrame, err := mock.serverReadFrame(bufReader)
		if err != nil {
			t.Logf("peer session/prompt read: %v", err)
			return
		}
		promptID := promptFrame["id"]

		// Emit one session/update text chunk (notification, no id).
		if err := mock.serverWriteJSON(map[string]any{
			"jsonrpc": "2.0",
			"method":  "session/update",
			"params": map[string]any{
				"sessionId": "sess-1",
				"update": map[string]any{
					"sessionUpdate": "agent_message_chunk",
					"content": map[string]any{
						"type": "text",
						"text": "hello",
					},
				},
			},
		}); err != nil {
			t.Logf("peer session/update write: %v", err)
			return
		}

		// Respond to the prompt id with stopReason=end_turn.
		if err := mock.serverWriteJSON(map[string]any{
			"jsonrpc": "2.0",
			"id":      promptID,
			"result": map[string]any{
				"stopReason": "end_turn",
			},
		}); err != nil {
			t.Logf("peer session/prompt write: %v", err)
			return
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	sid, err := client.NewSession(ctx, "/tmp")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if sid == "" {
		t.Fatal("NewSession returned empty session id")
	}

	stream, err := client.Prompt(ctx, sid, []canonical.Block{
		{Kind: canonical.BlockKindText, Text: &canonical.TextBlock{Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	<-serverDone

	return stream, client, mock
}

// teardownClient closes the client + mock pipes cleanly so goleak does
// not flag a dangling readLoop / writerLoop.
func teardownClient(t *testing.T, client *acp.Client, mock *mockPipeRWC) {
	t.Helper()
	mock.serverClose()
	if err := client.Close(); err != nil {
		t.Logf("client.Close: %v", err)
	}
}

// TestACPStreamShim_DelegatesChunksField asserts that wrapping a real
// *acp.Stream in an acpStreamShim preserves the underlying Chunks
// channel byte-for-byte (no buffering boundary, no copy). We drain the
// chunks first (consuming the single "hello" chunk the mock peer
// emitted) so that the underlying acp.Client read loop unblocks before
// teardown.
func TestACPStreamShim_DelegatesChunksField(t *testing.T) {
	stream, client, mock := drivePromptStream(t)
	defer teardownClient(t, client, mock)

	shim := &acpStreamShim{s: stream}

	// Pointer-equality via reflect — the underlying channel value must
	// be the SAME channel the *acp.Stream.Chunks field points to.
	if reflect.ValueOf(shim.Chunks()).Pointer() != reflect.ValueOf(stream.Chunks).Pointer() {
		t.Fatalf("acpStreamShim.Chunks() returned a different channel than the underlying *acp.Stream.Chunks field — delegation is broken (copy or rebind)")
	}

	// Drain — confirms a real chunk actually flowed through the shim's
	// Chunks() call (defense against the underlying channel being open
	// but empty for the duration of this test).
	gotChunks := 0
	for c := range shim.Chunks() {
		if c.Kind == canonical.ChunkKindText && c.Text != nil && c.Text.Content == "hello" {
			gotChunks++
		}
	}
	if gotChunks != 1 {
		t.Fatalf("drained chunks: got %d, want 1 'hello' text chunk", gotChunks)
	}
}

// TestACPStreamShim_ResultReturnsCanonicalFinalResult asserts that
// shim.Result() returns a *canonical.FinalResult populated with the
// underlying *acp.FinalResult fields. (Codex H-3 — the shim's whole
// purpose is the type translation at the engine boundary.)
func TestACPStreamShim_ResultReturnsCanonicalFinalResult(t *testing.T) {
	stream, client, mock := drivePromptStream(t)
	defer teardownClient(t, client, mock)

	shim := &acpStreamShim{s: stream}

	// Drain chunks so the stream-close path completes and Result()
	// returns the populated FinalResult.
	drained := 0
	for range shim.Chunks() {
		drained++
	}
	_ = drained // count discarded — test only needs the drain to complete

	got, err := shim.Result()
	if err != nil {
		t.Fatalf("shim.Result error: %v", err)
	}
	if got == nil {
		t.Fatal("shim.Result returned nil *canonical.FinalResult")
	}

	want, _ := stream.Result()
	if want == nil {
		t.Fatal("underlying stream.Result returned nil *acp.FinalResult")
	}

	if got.SessionID != want.SessionID {
		t.Errorf("shim FinalResult.SessionID: got %q, want %q", got.SessionID, want.SessionID)
	}
	if got.ChunkCount != want.ChunkCount {
		t.Errorf("shim FinalResult.ChunkCount: got %d, want %d", got.ChunkCount, want.ChunkCount)
	}
	if got.StopReason != want.StopReason {
		t.Errorf("shim FinalResult.StopReason: got %v, want %v", got.StopReason, want.StopReason)
	}
	if got.StopReason != canonical.StopEndTurn {
		t.Errorf("expected StopEndTurn from peer's stopReason=end_turn; got %v", got.StopReason)
	}
}
