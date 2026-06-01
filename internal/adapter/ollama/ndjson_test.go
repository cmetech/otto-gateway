package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
)

// ----------------------------------------------------------------------------
// Test doubles for RunHandle, Stream, and non-flusher ResponseWriter
// ----------------------------------------------------------------------------

// fakeStream implements Stream using a pre-populated channel.
type fakeStream struct {
	ch     chan canonical.Chunk
	result *canonical.FinalResult
	err    error
}

func (s *fakeStream) Chunks() <-chan canonical.Chunk { return s.ch }
func (s *fakeStream) Result() (*canonical.FinalResult, error) {
	return s.result, s.err
}

// fakeRunHandle wraps a fakeStream and satisfies the RunHandle interface.
//
// scResp is the synthetic ShortCircuitResponse return value used by Plan 02
// (Phase 08.1 INTEG-01) handler short-circuit tests. Default zero-value nil
// preserves every Plan 01 / pre-08.1 test's behavior: ShortCircuitResponse()
// returns nil → handler falls through to runNDJSONEmitter as before. Tests
// that need to exercise the streaming short-circuit guard at handlers.go
// (handleChat ~193-207, handleGenerate ~331-343) set scResp to a non-nil
// *canonical.ChatResponse before calling the handler.
type fakeRunHandle struct {
	stream    *fakeStream
	sessionID string
	scResp    *canonical.ChatResponse
}

func (h *fakeRunHandle) Stream() Stream       { return h.stream }
func (h *fakeRunHandle) SessionID() string    { return h.sessionID }
func (h *fakeRunHandle) StopWatchdog() func() bool { return func() bool { return true } }
func (h *fakeRunHandle) ShortCircuitResponse() *canonical.ChatResponse { return h.scResp }

// newFakeRunHandle builds a fakeRunHandle whose stream channel is pre-populated
// with chunks, then closed. result is the FinalResult returned by Result().
func newFakeRunHandle(chunks []canonical.Chunk, result *canonical.FinalResult, err error) *fakeRunHandle {
	ch := make(chan canonical.Chunk, len(chunks)+1)
	for _, c := range chunks {
		ch <- c
	}
	close(ch)
	return &fakeRunHandle{
		stream:    &fakeStream{ch: ch, result: result, err: err},
		sessionID: "test-session",
	}
}

// noopCancelFn is a CancelFunc that does nothing (used in tests that do not
// need to observe cancellation).
func noopCancelFn() {}

// flagCancelFn returns a CancelFunc that sets *called=true when invoked.
func flagCancelFn(called *bool) context.CancelFunc {
	return func() { *called = true }
}

// nonFlusherWriter implements http.ResponseWriter but NOT http.Flusher.
type nonFlusherWriter struct {
	header http.Header
	buf    bytes.Buffer
	code   int
}

func newNonFlusherWriter() *nonFlusherWriter {
	return &nonFlusherWriter{header: make(http.Header)}
}

func (w *nonFlusherWriter) Header() http.Header { return w.header }
func (w *nonFlusherWriter) Write(b []byte) (int, error) {
	n, err := w.buf.Write(b)
	if err != nil {
		return n, fmt.Errorf("nonFlusherWriter: %w", err)
	}
	return n, nil
}
func (w *nonFlusherWriter) WriteHeader(code int) { w.code = code }

// errorWriter implements http.ResponseWriter + http.Flusher; Write always
// fails after the initial headers are set.
type errorWriter struct {
	header      http.Header
	code        int
	headersSent bool
}

func newErrorWriter() *errorWriter {
	return &errorWriter{header: make(http.Header)}
}

func (w *errorWriter) Header() http.Header  { return w.header }
func (w *errorWriter) WriteHeader(code int) { w.code = code; w.headersSent = true }
func (w *errorWriter) Write([]byte) (int, error) {
	if w.headersSent {
		return 0, errors.New("errorWriter: simulated write failure")
	}
	// Before WriteHeader is called: headers are buffered, let that through.
	return 0, nil
}
func (w *errorWriter) Flush() {} // satisfies http.Flusher

// ----------------------------------------------------------------------------
// Tests
// ----------------------------------------------------------------------------

// TestNDJSON_Chat_TextChunks: 2 text chunks then channel closed → ≥3 NDJSON
// lines, last has done:true.
func TestNDJSON_Chat_TextChunks(t *testing.T) {
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "Hello"}},
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: " world"}},
	}
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}
	run := newFakeRunHandle(chunks, final, nil)

	w := httptest.NewRecorder()
	ctx := context.Background()

	_, err := runNDJSONEmitter(ctx, noopCancelFn, w, run, "auto", true, time.Now(), nilLogger(), nil, 0)
	if err != nil {
		t.Fatalf("runNDJSONEmitter: %v", err)
	}

	// Content-Type must be application/x-ndjson.
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/x-ndjson") {
		t.Errorf("Content-Type: got %q, want application/x-ndjson prefix", ct)
	}

	lines := scanNDJSON(t, w.Body.Bytes())
	if len(lines) < 3 {
		t.Fatalf("NDJSON lines: got %d, want ≥3 (2 done:false + 1 done:true); body=%s", len(lines), w.Body.String())
	}

	var last struct {
		Done       bool   `json:"done"`
		DoneReason string `json:"done_reason"`
	}
	if err := json.Unmarshal(lines[len(lines)-1], &last); err != nil {
		t.Fatalf("decode last line: %v", err)
	}
	if !last.Done {
		t.Error("last NDJSON line: done==false, want true")
	}
}

// TestNDJSON_Chat_ThoughtChunk: thought chunk with isChat=true → line has
// message.thinking non-empty and message.content empty.
func TestNDJSON_Chat_ThoughtChunk(t *testing.T) {
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindThought, Thought: &canonical.ThoughtChunk{Content: "deep thought"}},
	}
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}
	run := newFakeRunHandle(chunks, final, nil)

	w := httptest.NewRecorder()
	ctx := context.Background()

	_, err := runNDJSONEmitter(ctx, noopCancelFn, w, run, "auto", true, time.Now(), nilLogger(), nil, 0)
	if err != nil {
		t.Fatalf("runNDJSONEmitter: %v", err)
	}

	lines := scanNDJSON(t, w.Body.Bytes())
	// At least the thought line + the done:true line.
	if len(lines) < 2 {
		t.Fatalf("NDJSON lines: got %d, want ≥2; body=%s", len(lines), w.Body.String())
	}

	// The first line should be the thought chunk line.
	var thoughtLine struct {
		Message struct {
			Content  string `json:"content"`
			Thinking string `json:"thinking"`
		} `json:"message"`
		Done bool `json:"done"`
	}
	if err := json.Unmarshal(lines[0], &thoughtLine); err != nil {
		t.Fatalf("decode thought line: %v", err)
	}
	if thoughtLine.Message.Thinking == "" {
		t.Error("message.thinking: empty, want non-empty for ChunkKindThought with isChat=true")
	}
	if thoughtLine.Message.Content != "" {
		t.Errorf("message.content: got %q, want empty for thought-only line", thoughtLine.Message.Content)
	}
	if thoughtLine.Done {
		t.Error("thought line done: got true, want false (intermediate line)")
	}
}

// TestNDJSON_Generate_ThoughtDropped: thought chunk with isChat=false →
// no NDJSON line written (thought dropped per D-04 — /api/generate has no
// thinking field).
func TestNDJSON_Generate_ThoughtDropped(t *testing.T) {
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindThought, Thought: &canonical.ThoughtChunk{Content: "invisible thought"}},
	}
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}
	run := newFakeRunHandle(chunks, final, nil)

	w := httptest.NewRecorder()
	ctx := context.Background()

	_, err := runNDJSONEmitter(ctx, noopCancelFn, w, run, "auto", false, time.Now(), nilLogger(), nil, 0)
	if err != nil {
		t.Fatalf("runNDJSONEmitter: %v", err)
	}

	lines := scanNDJSON(t, w.Body.Bytes())
	// Only the done:true final line; the thought must be dropped.
	if len(lines) != 1 {
		t.Fatalf("NDJSON lines: got %d, want exactly 1 (done:true only, thought dropped); body=%s", len(lines), w.Body.String())
	}
	var last struct {
		Done     bool   `json:"done"`
		Response string `json:"response"`
	}
	if err := json.Unmarshal(lines[0], &last); err != nil {
		t.Fatalf("decode last line: %v", err)
	}
	if !last.Done {
		t.Error("last NDJSON line: done==false, want true")
	}
	// No response content from a thought-only stream.
	if last.Response != "" {
		t.Errorf("response: got %q, want empty (thought dropped)", last.Response)
	}
}

// TestNDJSON_FlusherAssertionFails: a ResponseWriter that does NOT implement
// http.Flusher causes runNDJSONEmitter to return an error before any bytes
// are written.
func TestNDJSON_FlusherAssertionFails(t *testing.T) {
	run := newFakeRunHandle(nil, &canonical.FinalResult{StopReason: canonical.StopEndTurn}, nil)

	w := newNonFlusherWriter()
	ctx := context.Background()

	_, err := runNDJSONEmitter(ctx, noopCancelFn, w, run, "auto", true, time.Now(), nilLogger(), nil, 0)
	if err == nil {
		t.Fatal("runNDJSONEmitter: want error for non-flusher writer, got nil")
	}
	if !strings.Contains(err.Error(), "not flusher") {
		t.Errorf("error: %q, want to contain 'not flusher'", err.Error())
	}
	// No bytes should have been written before the Flusher assertion.
	if w.buf.Len() > 0 {
		t.Errorf("bytes written before Flusher assertion failure: %q", w.buf.String())
	}
}

// TestNDJSON_WriteError_CancelsCtx: a writer that returns an error on Write
// must cause cancelFn to be called and the error to propagate.
func TestNDJSON_WriteError_CancelsCtx(t *testing.T) {
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "trigger write"}},
	}
	// Result is irrelevant — we expect the emitter to fail before reaching finalizeNDJSON.
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}
	run := newFakeRunHandle(chunks, final, nil)

	cancelCalled := false
	cancelFn := flagCancelFn(&cancelCalled)

	// Use a custom writer: it implements Flusher (so the Flusher assertion passes
	// and headers are written), but Write fails after WriteHeader is called.
	ew := newErrorWriter()
	ctx := context.Background()

	_, err := runNDJSONEmitter(ctx, cancelFn, ew, run, "auto", true, time.Now(), nilLogger(), nil, 0)
	if err == nil {
		t.Fatal("runNDJSONEmitter: want error for failing writer, got nil")
	}
	if !cancelCalled {
		t.Error("cancelFn: not called on write error (D-07 requirement)")
	}
}

// TestNDJSON_StreamResultError: when run.Stream().Result() returns an error,
// finalizeNDJSON must not emit a done:true line and must return the error.
// This exercises the non-nil err path in newFakeRunHandle.
func TestNDJSON_StreamResultError(t *testing.T) {
	// No content chunks; channel closes immediately so finalizeNDJSON is called.
	streamErr := errors.New("kiro: stream terminated")
	run := newFakeRunHandle(nil, nil, streamErr)

	w := httptest.NewRecorder()
	ctx := context.Background()

	_, err := runNDJSONEmitter(ctx, noopCancelFn, w, run, "kiro-model", true, time.Now(), nilLogger(), nil, 0)
	if err == nil {
		t.Fatal("runNDJSONEmitter: want error when stream.Result() fails, got nil")
	}
	if !strings.Contains(err.Error(), "stream result") {
		t.Errorf("error: %q, want to contain 'stream result'", err.Error())
	}
	// No done:true line must have been written.
	lines := scanNDJSON(t, w.Body.Bytes())
	// The body may be empty (headers already written, but no NDJSON lines).
	for _, line := range lines {
		var frame struct {
			Done bool `json:"done"`
		}
		if err2 := json.Unmarshal(line, &frame); err2 != nil {
			continue
		}
		if frame.Done {
			t.Errorf("stream error path: emitted done:true line, want none; body=%s", w.Body.String())
		}
	}
}

// ----------------------------------------------------------------------------
// Phase 6 Slice 2 — streaming coerce + kiro-native [tool:] narration + iteration-3
// sawKiroNativeToolCall skip-or-coerce-or-flush logic
// ----------------------------------------------------------------------------

// makeToolsCatalog builds a *canonical.ChatRequest with a single get_weather
// tool spec — used by the streaming-coerce tests to populate req.Tools so
// the buffering heuristic engages.
func makeToolsCatalog() *canonical.ChatRequest {
	return &canonical.ChatRequest{
		Tools: []canonical.ToolSpec{
			{
				Name: "get_weather",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"location": map[string]any{"type": "string"},
					},
				},
			},
		},
	}
}

// TestNDJSON_StreamingCoerce_BareJSON: REVIEW HIGH #1 verification. A
// streamed sequence of text chunks that together form a JSON object
// matching a tool spec must NOT be emitted as per-delta NDJSON lines
// (they get BUFFERED). At stream close, engine.CoerceToolCall fires on
// the buffered text, the synthesized tool_calls[] is attached, the done:true
// final line carries it, and the buffered deltas are DISCARDED.
//
// Wire-shape canary on the done line: arguments are a plain JSON object
// (Ollama D-04), NOT a JSON-encoded string.
func TestNDJSON_StreamingCoerce_BareJSON(t *testing.T) {
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "{"}},
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: `"location":`}},
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: `"NYC"`}},
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "}"}},
	}
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}
	run := newFakeRunHandle(chunks, final, nil)

	w := httptest.NewRecorder()
	ctx := context.Background()
	req := makeToolsCatalog()

	if _, err := runNDJSONEmitter(ctx, noopCancelFn, w, run, "auto", true, time.Now(), nilLogger(), req, 0); err != nil {
		t.Fatalf("runNDJSONEmitter: %v", err)
	}

	body := w.Body.String()
	lines := scanNDJSON(t, w.Body.Bytes())
	if len(lines) != 1 {
		t.Fatalf("NDJSON lines: got %d, want exactly 1 (only the done:true line; buffered text discarded on coerce hit); body=%s", len(lines), body)
	}

	// Wire-shape canary on the done line — plain-object arguments.
	if !strings.Contains(body, `"arguments":{"location":"NYC"}`) {
		t.Errorf("done line missing plain-object arguments; body=%s", body)
	}
	if strings.Contains(body, `"arguments":"`) {
		t.Errorf("done line carries OpenAI-style JSON-string arguments; body=%s", body)
	}

	// Decode the done line and confirm tool_calls[] populated.
	var done struct {
		Done    bool `json:"done"`
		Message struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Function struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	}
	if err := json.Unmarshal(lines[0], &done); err != nil {
		t.Fatalf("decode done line: %v", err)
	}
	if !done.Done {
		t.Error("done flag: got false, want true")
	}
	if len(done.Message.ToolCalls) != 1 {
		t.Fatalf("tool_calls len: got %d, want 1", len(done.Message.ToolCalls))
	}
	if done.Message.ToolCalls[0].Function.Name != "get_weather" {
		t.Errorf("tool_calls[0].function.name: got %q, want get_weather", done.Message.ToolCalls[0].Function.Name)
	}
	if done.Message.ToolCalls[0].Function.Arguments["location"] != "NYC" {
		t.Errorf("tool_calls[0].function.arguments[location]: got %v, want NYC", done.Message.ToolCalls[0].Function.Arguments["location"])
	}
}

// TestNDJSON_StreamingCoerce_NotJSON_PassThrough: when the text deltas
// do NOT look like JSON (no `{` or fence prefix), the buffering heuristic
// must NOT engage. Each text chunk streams as a normal NDJSON line and
// the done:true line carries NO tool_calls. Behavior is identical to
// Phase 4 pre-Phase-6.
func TestNDJSON_StreamingCoerce_NotJSON_PassThrough(t *testing.T) {
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "Hello "}},
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "world!"}},
	}
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}
	run := newFakeRunHandle(chunks, final, nil)

	w := httptest.NewRecorder()
	ctx := context.Background()
	req := makeToolsCatalog()

	if _, err := runNDJSONEmitter(ctx, noopCancelFn, w, run, "auto", true, time.Now(), nilLogger(), req, 0); err != nil {
		t.Fatalf("runNDJSONEmitter: %v", err)
	}

	body := w.Body.String()
	lines := scanNDJSON(t, w.Body.Bytes())
	// 2 text deltas + 1 done line = 3 lines.
	if len(lines) != 3 {
		t.Fatalf("NDJSON lines: got %d, want 3 (2 text deltas + done line, no buffering); body=%s", len(lines), body)
	}
	// No tool_calls on any line.
	if strings.Contains(body, `"tool_calls"`) {
		t.Errorf("non-JSON text path must NOT produce tool_calls; body=%s", body)
	}
	// Both text fragments appear.
	if !strings.Contains(body, "Hello ") {
		t.Errorf("missing first text fragment; body=%s", body)
	}
	if !strings.Contains(body, "world!") {
		t.Errorf("missing second text fragment; body=%s", body)
	}
}

// TestNDJSON_KiroNative_ThoughtTextOnly: REVIEW HIGH #2 verification.
// A kiro-native ChunkKindToolCall must render as a `[tool: <name>]\n`
// thought-text NDJSON line. The done:true final line must NOT carry
// tool_calls[] (the two-path rule — only coerce-synthesized populates
// tool_calls on the wire).
func TestNDJSON_KiroNative_ThoughtTextOnly(t *testing.T) {
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "starting "}},
		{Kind: canonical.ChunkKindToolCall, ToolCall: &canonical.ToolCallChunk{
			ID:   "tc_1",
			Name: "get_weather",
			Args: map[string]any{"location": "NYC"},
		}},
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: " done"}},
	}
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}
	run := newFakeRunHandle(chunks, final, nil)

	w := httptest.NewRecorder()
	ctx := context.Background()
	req := makeToolsCatalog()

	if _, err := runNDJSONEmitter(ctx, noopCancelFn, w, run, "auto", true, time.Now(), nilLogger(), req, 0); err != nil {
		t.Fatalf("runNDJSONEmitter: %v", err)
	}

	body := w.Body.String()
	lines := scanNDJSON(t, w.Body.Bytes())

	// Expect 4 lines: "starting ", "[tool: get_weather]\n", " done", done:true.
	if len(lines) != 4 {
		t.Fatalf("NDJSON lines: got %d, want 4 (2 text + 1 tool-narration + 1 done); body=%s", len(lines), body)
	}

	// No line carries tool_calls (two-path rule: kiro-native renders as
	// narration only, done line has no tool_calls because no coerce fired).
	if strings.Contains(body, `"tool_calls"`) {
		t.Errorf("kiro-native two-path rule violated — body contains tool_calls; body=%s", body)
	}

	// Narration line shape.
	if !strings.Contains(body, `[tool: get_weather]`) {
		t.Errorf("missing [tool: get_weather] narration; body=%s", body)
	}

	// The narration line must NOT set done:true.
	var second struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Done bool `json:"done"`
	}
	if err := json.Unmarshal(lines[1], &second); err != nil {
		t.Fatalf("decode narration line: %v", err)
	}
	if second.Done {
		t.Error("narration line: done==true, want false (intermediate line)")
	}
	if second.Message.Content != "[tool: get_weather]\n" {
		t.Errorf("narration line content: got %q, want %q", second.Message.Content, "[tool: get_weather]\n")
	}
}

// TestStream_NativeToolCall_ThenJSONText_NoCoerce: iteration-3 fix to
// HIGH #2. After a kiro-native tool_call passes through during the stream,
// sawKiroNativeToolCall is set to true. At stream end, even if subsequent
// JSON-shaped text was buffered, coerce is SKIPPED entirely. The buffered
// text is FLUSHED as plain text lines and the done:true line carries NO
// tool_calls.
func TestStream_NativeToolCall_ThenJSONText_NoCoerce(t *testing.T) {
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindToolCall, ToolCall: &canonical.ToolCallChunk{
			ID:   "tc_1",
			Name: "get_weather",
			Args: map[string]any{"location": "NYC"},
		}},
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "{"}},
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: `"location":`}},
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: `"Tokyo"`}},
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "}"}},
	}
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}
	run := newFakeRunHandle(chunks, final, nil)

	w := httptest.NewRecorder()
	ctx := context.Background()
	req := makeToolsCatalog()

	if _, err := runNDJSONEmitter(ctx, noopCancelFn, w, run, "auto", true, time.Now(), nilLogger(), req, 0); err != nil {
		t.Fatalf("runNDJSONEmitter: %v", err)
	}

	body := w.Body.String()

	// No tool_calls anywhere (kiro-native render via narration; coerce skipped).
	if strings.Contains(body, `"tool_calls"`) {
		t.Errorf("iteration-3 fix violated — body contains tool_calls after kiro-native chunk; body=%s", body)
	}

	// The narration must appear.
	if !strings.Contains(body, `[tool: get_weather]`) {
		t.Errorf("missing [tool: get_weather] narration; body=%s", body)
	}

	// The buffered JSON-shaped text MUST be flushed (not discarded) because
	// coerce was skipped — the text "Tokyo" should appear verbatim.
	if !strings.Contains(body, "Tokyo") {
		t.Errorf("buffered text was dropped instead of flushed (iteration-3 contract); body=%s", body)
	}
}

// TestStream_NativeToolCall_Only_NoCoerce: iteration-3 fix to HIGH #2,
// minimal case. Only a kiro-native tool_call chunk passes through (no
// text). sawKiroNativeToolCall = true, no buffering, no text to flush.
// Final output: exactly the `[tool: <name>]\n` narration line plus the
// done:true line. No tool_calls anywhere.
func TestStream_NativeToolCall_Only_NoCoerce(t *testing.T) {
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindToolCall, ToolCall: &canonical.ToolCallChunk{
			ID:   "tc_1",
			Name: "get_weather",
			Args: map[string]any{"location": "NYC"},
		}},
	}
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}
	run := newFakeRunHandle(chunks, final, nil)

	w := httptest.NewRecorder()
	ctx := context.Background()
	req := makeToolsCatalog()

	if _, err := runNDJSONEmitter(ctx, noopCancelFn, w, run, "auto", true, time.Now(), nilLogger(), req, 0); err != nil {
		t.Fatalf("runNDJSONEmitter: %v", err)
	}

	body := w.Body.String()
	lines := scanNDJSON(t, w.Body.Bytes())
	if len(lines) != 2 {
		t.Fatalf("NDJSON lines: got %d, want 2 (narration + done); body=%s", len(lines), body)
	}
	if strings.Contains(body, `"tool_calls"`) {
		t.Errorf("native-only path must NOT produce tool_calls; body=%s", body)
	}
	if !strings.Contains(body, `[tool: get_weather]`) {
		t.Errorf("missing narration; body=%s", body)
	}
}

// TestNDJSON_KiroNative_DefensiveNilName: defensive nil-name fallback —
// when ToolCall.Name is empty/missing, the narration emits "[tool: unknown]\n".
// This mirrors the discipline established in translate.go's
// firstNonEmpty(body.Title, "unknown") fallback (06-01 Task 1).
func TestNDJSON_KiroNative_DefensiveNilName(t *testing.T) {
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindToolCall, ToolCall: nil},
	}
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}
	run := newFakeRunHandle(chunks, final, nil)

	w := httptest.NewRecorder()
	ctx := context.Background()

	if _, err := runNDJSONEmitter(ctx, noopCancelFn, w, run, "auto", true, time.Now(), nilLogger(), nil, 0); err != nil {
		t.Fatalf("runNDJSONEmitter: %v", err)
	}

	body := w.Body.String()
	if !strings.Contains(body, `[tool: unknown]`) {
		t.Errorf("nil ToolCall must emit [tool: unknown] fallback; body=%s", body)
	}
}

// TestStream_ProseThenJSON_NoCoerce_NoLeak (WR-01 regression): when a
// non-JSON-shaped text chunk has already flushed to the wire, a
// subsequent JSON-shaped chunk MUST NOT retroactively engage the
// streaming-coerce buffer. The done line must NOT carry tool_calls
// (coerce did not fire), and the prose preamble must NOT precede a
// tool_call envelope on the wire.
//
// This locks the Pitfall 3 "entire text" invariant in the streaming
// path: once prose has leaked, coerce cannot safely fire (it would
// produce a split-shape response: prose + synthesized tool_calls).
func TestStream_ProseThenJSON_NoCoerce_NoLeak(t *testing.T) {
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "Here's the answer: "}},
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: `{"location":"NYC"}`}},
	}
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}
	run := newFakeRunHandle(chunks, final, nil)

	w := httptest.NewRecorder()
	ctx := context.Background()
	req := makeToolsCatalog()

	if _, err := runNDJSONEmitter(ctx, noopCancelFn, w, run, "auto", true, time.Now(), nilLogger(), req, 0); err != nil {
		t.Fatalf("runNDJSONEmitter: %v", err)
	}

	body := w.Body.String()
	// Coerce must NOT fire — done line cannot carry tool_calls when prose
	// has already leaked to the wire.
	if strings.Contains(body, `"tool_calls"`) {
		t.Errorf("WR-01: prose-then-JSON must not fire streaming coerce; body=%s", body)
	}
	// Both text fragments must reach the wire (preserve all observable
	// content; do not discard the JSON fragment as if it had been coerced).
	if !strings.Contains(body, "Here's the answer:") {
		t.Errorf("WR-01: prose preamble missing from wire; body=%s", body)
	}
	if !strings.Contains(body, `{\"location\":\"NYC\"}`) && !strings.Contains(body, `{"location":"NYC"}`) {
		t.Errorf("WR-01: JSON fragment missing from wire; body=%s", body)
	}
}

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

// scanNDJSON splits the body bytes into individual JSON lines (non-empty).
func scanNDJSON(t *testing.T, body []byte) [][]byte {
	t.Helper()
	var lines [][]byte
	s := bufio.NewScanner(bytes.NewReader(body))
	for s.Scan() {
		line := bytes.TrimSpace(s.Bytes())
		if len(line) == 0 {
			continue
		}
		cp := make([]byte, len(line))
		copy(cp, line)
		lines = append(lines, cp)
	}
	if err := s.Err(); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("scan NDJSON: %v", err)
	}
	return lines
}

// nilLogger returns a slog.Logger that discards all output.
func nilLogger() *slog.Logger {
	return newTestAdapter(nil, nil).cfg.Logger
}

// ----------------------------------------------------------------------------
// Quick 260531-ruv — idle-timeout watchdog
// ----------------------------------------------------------------------------

// TestNDJSON_IdleTimeout_EmitsErrorLine drives runNDJSONEmitter with a
// never-producing fake Stream and streamIdle=100ms. The emitter MUST
// write a terminal error line `{"error":"stream idle timeout","done":true}`
// and return an error that errors.Is(canonical.ErrStreamIdleTimeout).
func TestNDJSON_IdleTimeout_EmitsErrorLine(t *testing.T) {
	ch := make(chan canonical.Chunk) // never produces
	t.Cleanup(func() {
		defer func() { _ = recover() }()
		close(ch)
	})
	run := &fakeRunHandle{
		stream:    &fakeStream{ch: ch, result: &canonical.FinalResult{StopReason: canonical.StopUnknown}},
		sessionID: "idle-test",
	}
	w := httptest.NewRecorder()

	start := time.Now()
	resp, err := runNDJSONEmitter(context.Background(), noopCancelFn, w, run,
		"auto", true, time.Now(), nilLogger(), nil, 100*time.Millisecond)
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Fatalf("emitter took too long: %v", elapsed)
	}
	if !errors.Is(err, canonical.ErrStreamIdleTimeout) {
		t.Fatalf("expected ErrStreamIdleTimeout, got %v", err)
	}
	if resp == nil {
		t.Errorf("aggregated response should be non-nil for PostHook forensics")
	}
	body := w.Body.String()
	if !strings.Contains(body, `"error":"stream idle timeout"`) {
		t.Errorf("expected idle-timeout error line, body=%q", body)
	}
	if !strings.Contains(body, `"done":true`) {
		t.Errorf("expected done:true on idle frame, body=%q", body)
	}
}
