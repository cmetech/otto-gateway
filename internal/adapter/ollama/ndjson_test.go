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
type fakeRunHandle struct {
	stream    *fakeStream
	sessionID string
}

func (h *fakeRunHandle) Stream() Stream       { return h.stream }
func (h *fakeRunHandle) SessionID() string    { return h.sessionID }
func (h *fakeRunHandle) StopWatchdog() func() bool { return func() bool { return true } }

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

	err := runNDJSONEmitter(ctx, noopCancelFn, w, run, "auto", true, time.Now(), nilLogger())
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

	err := runNDJSONEmitter(ctx, noopCancelFn, w, run, "auto", true, time.Now(), nilLogger())
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

	err := runNDJSONEmitter(ctx, noopCancelFn, w, run, "auto", false, time.Now(), nilLogger())
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

	err := runNDJSONEmitter(ctx, noopCancelFn, w, run, "auto", true, time.Now(), nilLogger())
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

	err := runNDJSONEmitter(ctx, cancelFn, ew, run, "auto", true, time.Now(), nilLogger())
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

	err := runNDJSONEmitter(ctx, noopCancelFn, w, run, "kiro-model", true, time.Now(), nilLogger())
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
