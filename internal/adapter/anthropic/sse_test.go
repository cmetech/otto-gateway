package anthropic

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"otto-gateway/internal/canonical"
)

// ----------------------------------------------------------------------------
// Test helpers — controlled chunk channels, fake tickers, instrumented writers
// ----------------------------------------------------------------------------

// scriptedChunks builds a closed channel of the supplied chunks.
// Closed = the SSE loop will treat the stream as ended after the last
// chunk. The channel is unbuffered-but-pre-filled via a goroutine that
// sends and closes; goleak verifies the sender goroutine exits.
func scriptedChunks(chunks ...canonical.Chunk) <-chan canonical.Chunk {
	ch := make(chan canonical.Chunk, len(chunks))
	for _, c := range chunks {
		ch <- c
	}
	close(ch)
	return ch
}

// nullLogger returns an *slog.Logger that discards every record so
// debug logs from runSSEEmitterLoop / applyChunk don't pollute test
// output. Tests that care about a debug call (e.g., unsupported chunk
// kind drop) construct their own logger with a recording handler.
func nullLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelInfo + 4}))
}

// countingFlusher counts Flush() calls so tests can assert
// `flushCount == writeEventCount`. The body buffer is guarded by a
// mutex so tests can safely Body()-poll from a separate goroutine
// while the SSE loop writes (the production single-goroutine
// invariant is preserved in production via D-05; the polling pattern
// only appears in tests that drive runSSEEmitterLoop concurrently to
// inject ctx cancel / ticker pings).
type countingFlusher struct {
	mu        sync.Mutex
	flushed   int32 // atomic — incremented from the writer goroutine, read from the test goroutine
	headerMap http.Header
	status    int
	body      []byte
}

func newCountingFlusher() *countingFlusher {
	return &countingFlusher{
		headerMap: http.Header{},
	}
}

func (c *countingFlusher) Header() http.Header { return c.headerMap }
func (c *countingFlusher) WriteHeader(s int)   { c.status = s }
func (c *countingFlusher) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.body = append(c.body, p...)
	c.mu.Unlock()
	return len(p), nil
}
func (c *countingFlusher) Flush()       { atomic.AddInt32(&c.flushed, 1) }
func (c *countingFlusher) Flushes() int { return int(atomic.LoadInt32(&c.flushed)) }
func (c *countingFlusher) Body() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.body)
}

// nonFlusherWriter wraps an http.ResponseWriter so it does NOT satisfy
// http.Flusher. Used by TestRunSSEEmitter_NoFlusherError. (httptest's
// ResponseRecorder DOES implement Flusher; this wrapper hides it.)
type nonFlusherWriter struct {
	headerMap http.Header
	status    int
	body      []byte
}

func (n *nonFlusherWriter) Header() http.Header { return n.headerMap }
func (n *nonFlusherWriter) WriteHeader(s int)   { n.status = s }
func (n *nonFlusherWriter) Write(p []byte) (int, error) {
	n.body = append(n.body, p...)
	return len(p), nil
}

// sseEventLines parses an SSE body into a slice of `event: <name>`
// strings in emission order. Used by state-machine sequence tests.
func sseEventLines(body string) []string {
	var events []string
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			events = append(events, strings.TrimPrefix(line, "event: "))
		}
	}
	return events
}

// sseDataLines returns the slice of `data: <body>` strings in emission
// order so tests can assert on the JSON payloads.
func sseDataLines(body string) []string {
	var datas []string
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			datas = append(datas, strings.TrimPrefix(line, "data: "))
		}
	}
	return datas
}

// newRunHandle constructs a fakeRunHandle with the supplied scripted
// chunks and a clean StopEndTurn FinalResult.
func newRunHandle(chunks ...canonical.Chunk) *fakeRunHandle {
	return &fakeRunHandle{
		stream: &fakeStream{
			chunks: scriptedChunks(chunks...),
			final:  &canonical.FinalResult{StopReason: canonical.StopEndTurn},
		},
		sessionID: "session_test",
	}
}

// newEmitter constructs an sseEmitter backed by the supplied
// http.ResponseWriter (recorder or countingFlusher) for direct
// applyChunk / writeEvent unit tests. The Flusher type-assertion
// happens implicitly via the writer.
func newEmitter(w http.ResponseWriter) *sseEmitter {
	flusher, _ := w.(http.Flusher)
	return &sseEmitter{
		w:         w,
		flusher:   flusher,
		logger:    nullLogger(),
		messageID: "msg_01test",
		model:     "auto",
	}
}

// ----------------------------------------------------------------------------
// W7 — PingInterval named constant
// ----------------------------------------------------------------------------

// TestPingInterval_Constant pins the PingInterval value at the
// expected 15-second cadence. Compile-time identifier reference
// guarantees the constant exists; the equality check pins the value.
// The grep portion of the verify command (in the plan) enforces that
// runSSEEmitter actually USES the constant in time.NewTicker.
func TestPingInterval_Constant(t *testing.T) {
	defer goleak.VerifyNone(t)
	if PingInterval != 15*time.Second {
		t.Errorf("PingInterval: got %v, want 15s (W7 anchor)", PingInterval)
	}
}

// ----------------------------------------------------------------------------
// writeEvent — framing + JSON marshaling pin
// ----------------------------------------------------------------------------

func TestWriteEvent_FramingAndFlush(t *testing.T) {
	defer goleak.VerifyNone(t)
	cf := newCountingFlusher()
	e := newEmitter(cf)
	if err := e.writeEvent("ping", pingEvent{Type: "ping"}); err != nil {
		t.Fatalf("writeEvent: %v", err)
	}
	want := "event: ping\ndata: {\"type\":\"ping\"}\n\n"
	if got := cf.Body(); got != want {
		t.Errorf("frame: got %q, want %q", got, want)
	}
	if cf.Flushes() != 1 {
		t.Errorf("flushes: got %d, want 1", cf.Flushes())
	}
}

// ----------------------------------------------------------------------------
// applyChunk — state machine for text-only / kind transitions / dropped kinds
// ----------------------------------------------------------------------------

func TestApplyChunk_TextOnly_ThreeChunks(t *testing.T) {
	defer goleak.VerifyNone(t)
	cf := newCountingFlusher()
	e := newEmitter(cf)

	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "Hel"}},
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "lo, "}},
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "world"}},
	}
	for _, c := range chunks {
		if err := e.applyChunk(c); err != nil {
			t.Fatalf("applyChunk: %v", err)
		}
	}

	events := sseEventLines(cf.Body())
	want := []string{
		"content_block_start",
		"content_block_delta",
		"content_block_delta",
		"content_block_delta",
	}
	if !equalSlice(events, want) {
		t.Errorf("events: got %v, want %v", events, want)
	}
	if e.blockIndex != 0 {
		t.Errorf("blockIndex: got %d, want 0 (no kind transition)", e.blockIndex)
	}
	if !e.blockOpen {
		t.Error("blockOpen: got false, want true (no close yet)")
	}
}

func TestApplyChunk_TextThenThinkingThenText_IndexBumps(t *testing.T) {
	defer goleak.VerifyNone(t)
	cf := newCountingFlusher()
	e := newEmitter(cf)

	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "hello"}},
		{Kind: canonical.ChunkKindThought, Thought: &canonical.ThoughtChunk{Content: "thinking"}},
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "world"}},
	}
	for _, c := range chunks {
		if err := e.applyChunk(c); err != nil {
			t.Fatalf("applyChunk: %v", err)
		}
	}

	events := sseEventLines(cf.Body())
	want := []string{
		"content_block_start", // block 0 text
		"content_block_delta", // text_delta "hello"
		"content_block_stop",  // close block 0
		"content_block_start", // block 1 thinking
		"content_block_delta", // thinking_delta "thinking"
		"content_block_stop",  // close block 1
		"content_block_start", // block 2 text
		"content_block_delta", // text_delta "world"
	}
	if !equalSlice(events, want) {
		t.Errorf("events: got %v, want %v", events, want)
	}
	if e.blockIndex != 2 {
		t.Errorf("blockIndex: got %d, want 2 (two transitions)", e.blockIndex)
	}
}

func TestApplyChunk_UnsupportedKindDropped_NoIndexBump(t *testing.T) {
	defer goleak.VerifyNone(t)
	cf := newCountingFlusher()
	e := newEmitter(cf)

	// Phase 6 Plan 04: ChunkKindToolCall is now SUPPORTED (D-07);
	// ChunkKindPlan is the remaining dormant kind that exercises the
	// drop-without-state-change contract.
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "ok"}},
		{Kind: canonical.ChunkKindPlan, Plan: &canonical.PlanChunk{Content: "plan"}},
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "more"}},
	}
	for _, c := range chunks {
		if err := e.applyChunk(c); err != nil {
			t.Fatalf("applyChunk: %v", err)
		}
	}

	events := sseEventLines(cf.Body())
	// Expected: block 0 opens, text "ok" delta, plan dropped (NO
	// stop, NO start, NO index bump), text "more" delta on block 0.
	want := []string{
		"content_block_start",
		"content_block_delta",
		"content_block_delta",
	}
	if !equalSlice(events, want) {
		t.Errorf("events: got %v, want %v (unsupported kind must drop without state change)", events, want)
	}
	if e.blockIndex != 0 {
		t.Errorf("blockIndex: got %d, want 0 (dropped chunk MUST NOT bump index)", e.blockIndex)
	}
}

func TestApplyChunk_FirstChunkUnsupported_NoBlockOpened(t *testing.T) {
	defer goleak.VerifyNone(t)
	cf := newCountingFlusher()
	e := newEmitter(cf)

	// First chunk is an unsupported kind — no block should open.
	if err := e.applyChunk(canonical.Chunk{
		Kind: canonical.ChunkKindPlan,
		Plan: &canonical.PlanChunk{Content: "plan"},
	}); err != nil {
		t.Fatalf("applyChunk: %v", err)
	}
	if e.blockOpen {
		t.Error("blockOpen: got true, want false (unsupported first chunk must not open)")
	}
	if cf.Body() != "" {
		t.Errorf("body: got %q, want empty (unsupported first chunk must not write)", cf.Body())
	}
}

func TestApplyChunk_TextDeltaPayloadShape(t *testing.T) {
	defer goleak.VerifyNone(t)
	cf := newCountingFlusher()
	e := newEmitter(cf)

	if err := e.applyChunk(canonical.Chunk{
		Kind: canonical.ChunkKindText,
		Text: &canonical.TextChunk{Content: "hi"},
	}); err != nil {
		t.Fatalf("applyChunk: %v", err)
	}

	// Two data: frames — content_block_start, content_block_delta.
	datas := sseDataLines(cf.Body())
	if len(datas) != 2 {
		t.Fatalf("data lines: got %d, want 2; body=%s", len(datas), cf.Body())
	}
	// content_block_start payload must have content_block:{type:text,text:""}
	wantStart := `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`
	if datas[0] != wantStart {
		t.Errorf("content_block_start payload: got %q, want %q", datas[0], wantStart)
	}
	// content_block_delta payload must carry the text fragment
	wantDelta := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`
	if datas[1] != wantDelta {
		t.Errorf("content_block_delta payload: got %q, want %q", datas[1], wantDelta)
	}
}

func TestApplyChunk_ThinkingDeltaPayloadShape(t *testing.T) {
	defer goleak.VerifyNone(t)
	cf := newCountingFlusher()
	e := newEmitter(cf)

	if err := e.applyChunk(canonical.Chunk{
		Kind:    canonical.ChunkKindThought,
		Thought: &canonical.ThoughtChunk{Content: "reasoning"},
	}); err != nil {
		t.Fatalf("applyChunk: %v", err)
	}
	datas := sseDataLines(cf.Body())
	if len(datas) != 2 {
		t.Fatalf("data lines: got %d, want 2; body=%s", len(datas), cf.Body())
	}
	wantStart := `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`
	if datas[0] != wantStart {
		t.Errorf("content_block_start payload: got %q, want %q", datas[0], wantStart)
	}
	wantDelta := `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"reasoning"}}`
	if datas[1] != wantDelta {
		t.Errorf("content_block_delta payload: got %q, want %q", datas[1], wantDelta)
	}
}

// ----------------------------------------------------------------------------
// applyChunk — tool_use placeholder/populated discipline
//
// Regression coverage for the partial_json concat bug: kiro emits a
// `tool_call` notification with Args=nil (block-header announcement),
// followed by a `tool_call_chunk` with Args populated. Both translate
// to canonical.ChunkKindToolCall with the same ID. The buggy renderer
// emitted TWO `input_json_delta` deltas (`{}` then `{...}`), the SDK
// parser concatenated them, and parsing `{}{...}` failed at position 2.
//
// Contract: at most ONE input_json_delta per tool_use content block,
// carrying the FINAL populated args. Placeholder chunks (nil/empty
// args) open the block without emitting a delta. If a block closes
// without ever receiving populated args (zero-arg tool corner case),
// a single `partial_json:"{}"` flush is emitted at close time so the
// SDK parser has exactly one well-formed JSON value to parse.
// ----------------------------------------------------------------------------

// TestApplyChunk_ToolCall_PlaceholderThenPopulated reproduces the
// exact failure mode reported against pi-ai's anthropic-shared.ts
// parser: "Unexpected non-whitespace character after JSON at position
// 2 (line 1 column 3)". Drives the placeholder + populated pair and
// asserts the wire carries EXACTLY ONE input_json_delta with the
// final args; no `{}{...}` shape on partial_json.
func TestApplyChunk_ToolCall_PlaceholderThenPopulated(t *testing.T) {
	defer goleak.VerifyNone(t)
	cf := newCountingFlusher()
	e := newEmitter(cf)

	// Two ChunkKindToolCall with the same toolCallId — first is the
	// kiro-emitted `tool_call` announcement (Args=nil), second is the
	// `tool_call_chunk` carrying the actual args atomically.
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindToolCall, ToolCall: &canonical.ToolCallChunk{
			ID:   "toolu_01",
			Name: "read",
			Args: nil,
		}},
		{Kind: canonical.ChunkKindToolCall, ToolCall: &canonical.ToolCallChunk{
			ID:   "toolu_01",
			Name: "read",
			Args: map[string]any{"filePath": "CLAUDE.md"},
		}},
	}
	for _, c := range chunks {
		if err := e.applyChunk(c); err != nil {
			t.Fatalf("applyChunk: %v", err)
		}
	}

	body := cf.Body()

	// Event sequence: exactly content_block_start + ONE content_block_delta.
	// Two deltas would mean we're emitting both the placeholder and the
	// populated args separately — the bug.
	events := sseEventLines(body)
	want := []string{
		"content_block_start",
		"content_block_delta",
	}
	if !equalSlice(events, want) {
		t.Errorf("events: got %v, want %v (exactly one input_json_delta per tool_use block)", events, want)
	}

	// Wire-shape guards: partial_json must contain the FINAL populated
	// args, NEVER the placeholder `{}` followed by a second delta.
	if !strings.Contains(body, `"partial_json":"{\"filePath\":\"CLAUDE.md\"}"`) {
		t.Errorf("wire missing expected partial_json with populated args; body:\n%s", body)
	}
	// Pi-AI / Anthropic SDK parsers concatenate partial_json deltas
	// and parse the result. `{}` followed by anything is invalid JSON.
	if strings.Contains(body, `"partial_json":"{}"`) {
		t.Errorf("wire carries forbidden `\"partial_json\":\"{}\"` for placeholder chunk (concat bug — Anthropic SDK rejects `{}{...}`); body:\n%s", body)
	}

	// State invariants: block stays open, toolUseEmitted is true (so
	// the stop_reason finalizer override fires), block index unbumped.
	if !e.blockOpen {
		t.Error("blockOpen: got false, want true (block stays open through same-kind chunks)")
	}
	if !e.toolUseEmitted {
		t.Error("toolUseEmitted: got false, want true (tool_use block opened, finalizer override required)")
	}
	if e.blockIndex != 0 {
		t.Errorf("blockIndex: got %d, want 0 (same-kind chunks must not bump)", e.blockIndex)
	}
}

// TestApplyChunk_ToolCall_PlaceholderOnly_FlushesEmptyObject covers
// the zero-arg-tool corner case: a tool that takes no arguments.
// Kiro emits one `tool_call` with Args=nil and no follow-up chunk.
// Without a flush at close time, the SDK parser would see empty
// partial_json and fail to parse. The fix emits a single `{}` delta
// at content_block_stop time so the parser gets a valid empty object.
func TestApplyChunk_ToolCall_PlaceholderOnly_FlushesEmptyObject(t *testing.T) {
	defer goleak.VerifyNone(t)
	cf := newCountingFlusher()
	e := newEmitter(cf)

	// Single placeholder chunk, then a kind transition to text to
	// force a content_block_stop (the flush path).
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindToolCall, ToolCall: &canonical.ToolCallChunk{
			ID:   "toolu_zero",
			Name: "now",
			Args: nil,
		}},
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "done"}},
	}
	for _, c := range chunks {
		if err := e.applyChunk(c); err != nil {
			t.Fatalf("applyChunk: %v", err)
		}
	}

	body := cf.Body()

	// Sequence: content_block_start(tool_use), content_block_delta(`{}`
	// flushed at close), content_block_stop, content_block_start(text),
	// content_block_delta(text).
	events := sseEventLines(body)
	want := []string{
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"content_block_start",
		"content_block_delta",
	}
	if !equalSlice(events, want) {
		t.Errorf("events: got %v, want %v (zero-arg flush at close emits `{}`)", events, want)
	}

	// The flush delta carries `partial_json:"{}"` exactly once.
	if !strings.Contains(body, `"partial_json":"{}"`) {
		t.Errorf("wire missing zero-arg flush `\"partial_json\":\"{}\"`; body:\n%s", body)
	}

	if e.blockIndex != 1 {
		t.Errorf("blockIndex: got %d, want 1 (one kind transition)", e.blockIndex)
	}
}

// TestApplyChunk_ToolCall_TwoPopulatedSameBlock_SecondDropped guards
// against D-06 atomicity violations: kiro is contracted to emit args
// atomically. If a misbehaving stream sends TWO populated chunks for
// the same block, the SECOND must be dropped — emitting it would
// recreate the same concat-broken JSON the placeholder fix prevents.
func TestApplyChunk_ToolCall_TwoPopulatedSameBlock_SecondDropped(t *testing.T) {
	defer goleak.VerifyNone(t)
	cf := newCountingFlusher()
	e := newEmitter(cf)

	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindToolCall, ToolCall: &canonical.ToolCallChunk{
			ID:   "toolu_dup",
			Name: "read",
			Args: map[string]any{"filePath": "a.md"},
		}},
		{Kind: canonical.ChunkKindToolCall, ToolCall: &canonical.ToolCallChunk{
			ID:   "toolu_dup",
			Name: "read",
			Args: map[string]any{"filePath": "b.md"}, // different value — first wins
		}},
	}
	for _, c := range chunks {
		if err := e.applyChunk(c); err != nil {
			t.Fatalf("applyChunk: %v", err)
		}
	}

	body := cf.Body()

	events := sseEventLines(body)
	want := []string{
		"content_block_start",
		"content_block_delta",
	}
	if !equalSlice(events, want) {
		t.Errorf("events: got %v, want %v (D-06 atomicity — second populated chunk dropped)", events, want)
	}
	// First populated chunk wins on the wire.
	if !strings.Contains(body, `"partial_json":"{\"filePath\":\"a.md\"}"`) {
		t.Errorf("wire missing first-wins partial_json `a.md`; body:\n%s", body)
	}
	if strings.Contains(body, `"filePath\":\"b.md\"`) {
		t.Errorf("wire leaked second populated chunk `b.md` (atomicity violation); body:\n%s", body)
	}
}

// ----------------------------------------------------------------------------
// runSSEEmitterLoop — ping interleave, ctx cancel, finalize
// ----------------------------------------------------------------------------

func TestRunSSEEmitterLoop_PingInterleave(t *testing.T) {
	defer goleak.VerifyNone(t)
	cf := newCountingFlusher()
	e := newEmitter(cf)

	// Manual ticker so we can fire pings on demand without waiting 15s.
	tickerC := make(chan time.Time, 1)
	chunks := make(chan canonical.Chunk, 2)
	runHandle := &fakeRunHandle{
		stream: &fakeStream{
			chunks: chunks,
			final:  &canonical.FinalResult{StopReason: canonical.StopEndTurn},
		},
		sessionID: "ping-test",
	}

	// Fire ONE ping BEFORE any chunks land — the loop should write a
	// `event: ping` frame; THEN send chunks and close.
	tickerC <- time.Now()
	// Drive the loop in a goroutine so we can sequence the inputs.
	done := make(chan error, 1)
	go func() {
		_, err := runSSEEmitterLoop(context.Background(), e, runHandle, tickerC, 0)
		done <- err
	}()

	// Give the goroutine a brief moment to process the ping. Polling
	// via the recorded body length avoids a fixed sleep racing with
	// the goroutine.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(cf.Body(), "event: ping") {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !strings.Contains(cf.Body(), "event: ping") {
		t.Fatalf("expected ping frame in body within deadline; body=%q", cf.Body())
	}

	// Now feed a chunk and close — the loop should emit content_block_*
	// frames and then finalize.
	chunks <- canonical.Chunk{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "hi"}}
	close(chunks)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("loop error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not return within 2s after closing chunks")
	}

	// Expected event sequence: ping, content_block_start, content_block_delta,
	// content_block_stop, message_delta, message_stop.
	events := sseEventLines(cf.Body())
	want := []string{
		"ping",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	}
	if !equalSlice(events, want) {
		t.Errorf("events: got %v, want %v", events, want)
	}
}

func TestRunSSEEmitterLoop_CtxCancelTerminatesCleanly(t *testing.T) {
	defer goleak.VerifyNone(t)
	cf := newCountingFlusher()
	e := newEmitter(cf)

	chunks := make(chan canonical.Chunk, 1)
	runHandle := &fakeRunHandle{
		stream: &fakeStream{
			chunks: chunks,
			final:  &canonical.FinalResult{StopReason: canonical.StopEndTurn},
		},
		sessionID: "cancel-test",
	}

	ctx, cancel := context.WithCancel(context.Background())
	tickerC := make(chan time.Time)

	// Push one chunk so the loop emits content_block_start + delta.
	chunks <- canonical.Chunk{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "hi"}}

	done := make(chan error, 1)
	go func() {
		_, err := runSSEEmitterLoop(ctx, e, runHandle, tickerC, 0)
		done <- err
	}()

	// Wait for the chunk to be processed (block_start + delta visible).
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(cf.Body(), "content_block_delta") {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Cancel the ctx — the loop should return ctx.Err() within ~100ms.
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("loop error: got %v, want context.Canceled", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("loop did not return within 100ms after ctx cancel")
	}

	// No message_delta or message_stop should be emitted after cancel —
	// the stream was torn down before the natural end.
	if strings.Contains(cf.Body(), "event: message_delta") {
		t.Error("body contains message_delta after ctx cancel — should NOT")
	}
	if strings.Contains(cf.Body(), "event: message_stop") {
		t.Error("body contains message_stop after ctx cancel — should NOT")
	}

	// Drain the still-open chunks channel so the goroutine that owns it
	// (if any) doesn't leak. In this test we own it directly — close it.
	close(chunks)
}

// TestRunSSEEmitterLoop_ResultError emits `event: error` instead of
// message_delta/message_stop when run.Stream().Result() returns a
// non-nil error.
func TestRunSSEEmitterLoop_ResultError(t *testing.T) {
	defer goleak.VerifyNone(t)
	cf := newCountingFlusher()
	e := newEmitter(cf)

	chunks := make(chan canonical.Chunk)
	close(chunks)
	runHandle := &fakeRunHandle{
		stream: &fakeStream{
			chunks: chunks,
			final:  nil,
			err:    errors.New("upstream blew up"),
		},
		sessionID: "result-error-test",
	}
	tickerC := make(chan time.Time)

	_, err := runSSEEmitterLoop(context.Background(), e, runHandle, tickerC, 0)
	if err == nil || !strings.Contains(err.Error(), "upstream blew up") {
		t.Errorf("error: got %v, want wraps 'upstream blew up'", err)
	}

	events := sseEventLines(cf.Body())
	if len(events) == 0 {
		t.Fatalf("body has no events; body=%q", cf.Body())
	}
	// Final frame MUST be `event: error`. No message_delta / message_stop.
	if events[len(events)-1] != "error" {
		t.Errorf("final event: got %q, want %q; events=%v", events[len(events)-1], "error", events)
	}
	for _, ev := range events {
		if ev == "message_delta" || ev == "message_stop" {
			t.Errorf("found %q event AFTER error frame — SDK treats error as terminal", ev)
		}
	}
}

// TestRunSSEEmitterLoop_ResultError_BlockOpen_StopFirst verifies that
// when a block is open at error time, content_block_stop is emitted
// (best-effort) BEFORE the error frame.
func TestRunSSEEmitterLoop_ResultError_BlockOpen_StopFirst(t *testing.T) {
	defer goleak.VerifyNone(t)
	cf := newCountingFlusher()
	e := newEmitter(cf)

	chunks := make(chan canonical.Chunk, 1)
	chunks <- canonical.Chunk{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "partial"}}
	close(chunks)
	runHandle := &fakeRunHandle{
		stream: &fakeStream{
			chunks: chunks,
			err:    errors.New("late blow"),
		},
		sessionID: "stop-then-error",
	}
	tickerC := make(chan time.Time)

	_, _ = runSSEEmitterLoop(context.Background(), e, runHandle, tickerC, 0)
	events := sseEventLines(cf.Body())

	// Expect: content_block_start, content_block_delta, content_block_stop, error.
	want := []string{"content_block_start", "content_block_delta", "content_block_stop", "error"}
	if !equalSlice(events, want) {
		t.Errorf("events: got %v, want %v", events, want)
	}
}

// ----------------------------------------------------------------------------
// runSSEEmitter — headers + Flusher missing
// ----------------------------------------------------------------------------

// TestRunSSEEmitter_NoFlusherError verifies the type-assertion failure
// returns an error and writes NOTHING (so the caller can render a
// clean JSON 500 envelope).
func TestRunSSEEmitter_NoFlusherError(t *testing.T) {
	defer goleak.VerifyNone(t)
	nfw := &nonFlusherWriter{headerMap: http.Header{}}
	runHandle := newRunHandle()

	_, err := runSSEEmitter(context.Background(), nfw, runHandle, nil, "auto", 0, nullLogger())
	if err == nil || !strings.Contains(err.Error(), "response writer is not flusher") {
		t.Errorf("error: got %v, want 'response writer is not flusher'", err)
	}
	if len(nfw.body) != 0 {
		t.Errorf("body: got %d bytes, want 0 (must not write before flusher check)", len(nfw.body))
	}
	if nfw.status != 0 {
		t.Errorf("status: got %d, want 0 (must not WriteHeader before flusher check)", nfw.status)
	}

	// Drain the runHandle so goleak doesn't catch the chunk goroutine.
	// Our scriptedChunks helper produces a pre-filled-and-closed
	// channel so there's nothing to drain — but call Result() anyway
	// to honor the contract. Counter satisfies revive's empty-block rule.
	_, _ = runHandle.Stream().Result()
	drained := 0
	for range runHandle.Stream().Chunks() {
		drained++
	}
	_ = drained
}

// TestRunSSEEmitter_EndToEnd_Headers exercises the real runSSEEmitter
// (with the production 15s ticker — which won't fire during a fast
// stream) on an httptest.ResponseRecorder which implements Flusher.
// Asserts the headers and the first/last event frame names.
func TestRunSSEEmitter_EndToEnd_Headers(t *testing.T) {
	defer goleak.VerifyNone(t)
	rec := httptest.NewRecorder()
	runHandle := newRunHandle(
		canonical.Chunk{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "hi"}},
	)

	_, err := runSSEEmitter(context.Background(), rec, runHandle, nil, "auto", 0, nullLogger())
	if err != nil {
		t.Fatalf("runSSEEmitter: %v", err)
	}

	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type: got %q, want text/event-stream", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control: got %q, want no-cache", cc)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rec.Code)
	}

	events := sseEventLines(rec.Body.String())
	if len(events) == 0 {
		t.Fatalf("no events emitted; body=%q", rec.Body.String())
	}
	if events[0] != "message_start" {
		t.Errorf("first event: got %q, want message_start", events[0])
	}
	if events[len(events)-1] != "message_stop" {
		t.Errorf("last event: got %q, want message_stop", events[len(events)-1])
	}
}

// ----------------------------------------------------------------------------
// Flush count — every writeEvent must Flush exactly once
// ----------------------------------------------------------------------------

func TestSSEEmitter_FlushCountEqualsWriteEvents(t *testing.T) {
	defer goleak.VerifyNone(t)
	cf := newCountingFlusher()
	e := newEmitter(cf)

	// Drive runSSEEmitterLoop directly so we can count emit-and-flush
	// pairs deterministically.
	chunks := make(chan canonical.Chunk, 3)
	chunks <- canonical.Chunk{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "a"}}
	chunks <- canonical.Chunk{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "b"}}
	chunks <- canonical.Chunk{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "c"}}
	close(chunks)
	runHandle := &fakeRunHandle{
		stream: &fakeStream{
			chunks: chunks,
			final:  &canonical.FinalResult{StopReason: canonical.StopEndTurn},
		},
		sessionID: "flush-count",
	}
	tickerC := make(chan time.Time)

	if _, err := runSSEEmitterLoop(context.Background(), e, runHandle, tickerC, 0); err != nil {
		t.Fatalf("loop: %v", err)
	}

	// 6 events expected: content_block_start, 3x content_block_delta,
	// content_block_stop, message_delta, message_stop = 7 frames.
	// (No message_start because runSSEEmitterLoop assumes the caller
	// emitted it — which it does in runSSEEmitter; this test exercises
	// the loop in isolation.)
	events := sseEventLines(cf.Body())
	if cf.Flushes() != len(events) {
		t.Errorf("flushes: got %d, want %d (Pitfall 2 — flush after every writeEvent); events=%v", cf.Flushes(), len(events), events)
	}
}

// ----------------------------------------------------------------------------
// JSON shape pins — finalize stream payloads
// ----------------------------------------------------------------------------

// TestFinalize_MessageDeltaPayload checks that the message_delta
// payload has the documented shape with cumulative output_tokens:0
// (D-12) and stop_reason mapped per A5.
func TestFinalize_MessageDeltaPayload(t *testing.T) {
	defer goleak.VerifyNone(t)
	cf := newCountingFlusher()
	e := newEmitter(cf)

	chunks := make(chan canonical.Chunk)
	close(chunks)
	runHandle := &fakeRunHandle{
		stream: &fakeStream{
			chunks: chunks,
			final:  &canonical.FinalResult{StopReason: canonical.StopMaxTokens},
		},
		sessionID: "finalize-test",
	}
	tickerC := make(chan time.Time)

	if _, err := runSSEEmitterLoop(context.Background(), e, runHandle, tickerC, 0); err != nil {
		t.Fatalf("loop: %v", err)
	}

	// Find the message_delta data frame.
	var deltaJSON string
	for i, ev := range sseEventLines(cf.Body()) {
		if ev == "message_delta" {
			deltaJSON = sseDataLines(cf.Body())[i]
			break
		}
	}
	if deltaJSON == "" {
		t.Fatalf("no message_delta frame in body; body=%q", cf.Body())
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(deltaJSON), &parsed); err != nil {
		t.Fatalf("unmarshal message_delta: %v", err)
	}
	delta, _ := parsed["delta"].(map[string]any)
	if got, _ := delta["stop_reason"].(string); got != "max_tokens" {
		t.Errorf("stop_reason: got %q, want max_tokens", got)
	}
	if _, present := delta["stop_sequence"]; !present {
		t.Errorf("stop_sequence: field absent — must render as null (Anthropic spec)")
	}
	usage, _ := parsed["usage"].(map[string]any)
	if ot, _ := usage["output_tokens"].(float64); ot != 0 {
		t.Errorf("usage.output_tokens: got %v, want 0 (D-12)", ot)
	}
}

// TestFinalize_NilFinalResult_FallsBackToStopUnknown ensures Result()
// returning a nil *FinalResult doesn't crash and renders stop_reason
// as null (StopUnknown).
func TestFinalize_NilFinalResult_FallsBackToStopUnknown(t *testing.T) {
	defer goleak.VerifyNone(t)
	cf := newCountingFlusher()
	e := newEmitter(cf)

	chunks := make(chan canonical.Chunk)
	close(chunks)
	runHandle := &fakeRunHandle{
		stream: &fakeStream{
			chunks: chunks,
			final:  nil,
			err:    nil,
		},
		sessionID: "nil-final",
	}
	tickerC := make(chan time.Time)

	if _, err := runSSEEmitterLoop(context.Background(), e, runHandle, tickerC, 0); err != nil {
		t.Fatalf("loop: %v", err)
	}
	// Find the message_delta data frame.
	events := sseEventLines(cf.Body())
	deltaIdx := -1
	for i, ev := range events {
		if ev == "message_delta" {
			deltaIdx = i
			break
		}
	}
	if deltaIdx < 0 {
		t.Fatalf("no message_delta frame; body=%q", cf.Body())
	}
	deltaJSON := sseDataLines(cf.Body())[deltaIdx]
	var parsed map[string]any
	if err := json.Unmarshal([]byte(deltaJSON), &parsed); err != nil {
		t.Fatalf("unmarshal message_delta: %v", err)
	}
	delta, _ := parsed["delta"].(map[string]any)
	if sr, present := delta["stop_reason"]; !present || sr != nil {
		t.Errorf("stop_reason: got %v (present=%v), want JSON null (StopUnknown maps to nil)", sr, present)
	}
}

// ----------------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------------

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ----------------------------------------------------------------------------
// Quick 260531-ruv — idle-timeout watchdog
// ----------------------------------------------------------------------------

// TestSSE_IdleTimeout_EmitsErrorFrame exercises the stream-idle watchdog
// added in quick 260531-ruv. A fake Stream whose Chunks() channel never
// produces is passed to runSSEEmitterLoop with streamIdle=100ms; the
// loop must emit an `event: error` frame and return an error that
// errors.Is(canonical.ErrStreamIdleTimeout).
func TestSSE_IdleTimeout_EmitsErrorFrame(t *testing.T) {
	cf := newCountingFlusher()
	e := newEmitter(cf)

	// fakeStream with a chunks channel that never emits.
	chunks := make(chan canonical.Chunk)
	t.Cleanup(func() {
		defer func() { _ = recover() }()
		close(chunks)
	})
	runHandle := &fakeRunHandle{
		stream:    &fakeStream{chunks: chunks, final: &canonical.FinalResult{StopReason: canonical.StopUnknown}},
		sessionID: "idle-test",
	}
	tickerC := make(chan time.Time) // never ticks

	start := time.Now()
	resp, err := runSSEEmitterLoop(context.Background(), e, runHandle, tickerC, 100*time.Millisecond)
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("loop took too long to fire: %v", elapsed)
	}
	if !errors.Is(err, canonical.ErrStreamIdleTimeout) {
		t.Fatalf("expected ErrStreamIdleTimeout, got %v", err)
	}
	if resp == nil {
		t.Errorf("aggregated response should be non-nil so PostHooks observe forensics")
	}
	if !strings.Contains(cf.Body(), "event: error") {
		t.Errorf("expected event: error frame, body=%q", cf.Body())
	}
	if !strings.Contains(cf.Body(), "upstream stream idle") {
		t.Errorf("expected idle message in error frame, body=%q", cf.Body())
	}
}

// TestSSE_IdleTimeout_Disabled verifies that streamIdle=0 disables the
// watchdog. A never-producing stream with a 50ms ctx deadline returns a
// ctx-error (not idle-timeout).
func TestSSE_IdleTimeout_Disabled(t *testing.T) {
	cf := newCountingFlusher()
	e := newEmitter(cf)

	chunks := make(chan canonical.Chunk)
	t.Cleanup(func() {
		defer func() { _ = recover() }()
		close(chunks)
	})
	runHandle := &fakeRunHandle{
		stream:    &fakeStream{chunks: chunks, final: &canonical.FinalResult{StopReason: canonical.StopUnknown}},
		sessionID: "idle-disabled-test",
	}
	tickerC := make(chan time.Time)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := runSSEEmitterLoop(ctx, e, runHandle, tickerC, 0)
	if errors.Is(err, canonical.ErrStreamIdleTimeout) {
		t.Fatalf("idle=0 should disable watchdog; got idle-timeout: %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded wrap, got %v", err)
	}
}
