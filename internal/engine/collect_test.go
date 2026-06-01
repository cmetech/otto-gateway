// Package engine — Collect text aggregation tests + chunk-kind drop
// tests + stop-reason propagation tests (D-01).
package engine

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
)

// TestCollect_AggregatesText asserts that Collect concatenates multiple
// ChunkKindText chunks into a single response Content.Text field.
func TestCollect_AggregatesText(t *testing.T) {
	ack := &fakeACP{
		newSessionID: "sid-aggregate",
		chunksToEmit: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "hello "}},
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "world"}},
		},
	}
	e := newTestEngine(t, ack)
	resp, err := e.Collect(context.Background(), simpleUserReq("greet", ""))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(resp.Message.Content) != 1 {
		t.Fatalf("expected exactly one content part; got %d", len(resp.Message.Content))
	}
	if resp.Message.Content[0].Text != "hello world" {
		t.Errorf("aggregated text: got %q, want 'hello world'", resp.Message.Content[0].Text)
	}
}

// TestCollect_AggregatesThoughtsAsThinkingPart (Phase 3.1 D-02) asserts
// that ChunkKindThought chunks now contribute to a SECOND content part
// of Kind == ContentKindThinking on the assembled Message.Content. Phase
// 2's "intentionally drop" behaviour is replaced — the dormant
// ContentKindThinking seam goes live so the Anthropic adapter can render
// thinking content blocks (ANTH-07 foundation).
//
// Text part comes first (zero index for Anthropic block sequencing per
// D-03/D-04); thinking part comes second when non-empty.
func TestCollect_AggregatesThoughtsAsThinkingPart(t *testing.T) {
	ack := &fakeACP{
		newSessionID: "sid-thought-aggregate",
		chunksToEmit: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "Hello "}},
			{Kind: canonical.ChunkKindThought, Thought: &canonical.ThoughtChunk{Content: "reasoning "}},
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "world"}},
		},
	}
	e := newTestEngine(t, ack)
	resp, err := e.Collect(context.Background(), simpleUserReq("q", ""))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(resp.Message.Content) != 2 {
		t.Fatalf("Content parts: got %d, want 2 (text + thinking)", len(resp.Message.Content))
	}
	if resp.Message.Content[0].Kind != canonical.ContentKindText {
		t.Errorf("Content[0].Kind: got %v, want ContentKindText", resp.Message.Content[0].Kind)
	}
	if resp.Message.Content[0].Text != "Hello world" {
		t.Errorf("Content[0].Text: got %q, want 'Hello world'", resp.Message.Content[0].Text)
	}
	if resp.Message.Content[1].Kind != canonical.ContentKindThinking {
		t.Errorf("Content[1].Kind: got %v, want ContentKindThinking", resp.Message.Content[1].Kind)
	}
	if resp.Message.Content[1].Text != "reasoning " {
		t.Errorf("Content[1].Text: got %q, want 'reasoning '", resp.Message.Content[1].Text)
	}
}

// TestCollect_TextOnly_NoThinkingPart_Appended asserts that the
// thinking part is appended ONLY when at least one ChunkKindThought
// chunk arrives — a text-only stream keeps Content len == 1 so the
// Phase 2 Ollama shape (which expects len(Content) == 1 for plain
// text responses) is preserved as a regression guard.
func TestCollect_TextOnly_NoThinkingPart_Appended(t *testing.T) {
	ack := &fakeACP{
		newSessionID: "sid-text-only",
		chunksToEmit: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "answer"}},
		},
	}
	e := newTestEngine(t, ack)
	resp, err := e.Collect(context.Background(), simpleUserReq("q", ""))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(resp.Message.Content) != 1 {
		t.Fatalf("Content parts (no thoughts emitted): got %d, want 1", len(resp.Message.Content))
	}
	if resp.Message.Content[0].Text != "answer" {
		t.Errorf("Content[0].Text: got %q, want 'answer'", resp.Message.Content[0].Text)
	}
}

// TestCollect_ThoughtOnly_StillEmitsEmptyTextPart — a stream that
// emits ONLY ChunkKindThought (no text at all) still produces a
// stable two-part shape: the leading text part is preserved as an
// empty-string ContentKindText (defensive — keeps Phase 2 Ollama's
// joinTextContent path returning ""; the Anthropic adapter renders
// only the thinking content block in that case).
func TestCollect_ThoughtOnly_StillEmitsEmptyTextPart(t *testing.T) {
	ack := &fakeACP{
		newSessionID: "sid-thought-only",
		chunksToEmit: []canonical.Chunk{
			{Kind: canonical.ChunkKindThought, Thought: &canonical.ThoughtChunk{Content: "pure reasoning"}},
		},
	}
	e := newTestEngine(t, ack)
	resp, err := e.Collect(context.Background(), simpleUserReq("q", ""))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(resp.Message.Content) != 2 {
		t.Fatalf("Content parts: got %d, want 2 (empty text + thinking)", len(resp.Message.Content))
	}
	if resp.Message.Content[0].Kind != canonical.ContentKindText {
		t.Errorf("Content[0].Kind: got %v, want ContentKindText", resp.Message.Content[0].Kind)
	}
	if resp.Message.Content[0].Text != "" {
		t.Errorf("Content[0].Text: got %q, want empty (text builder produced no chars)", resp.Message.Content[0].Text)
	}
	if resp.Message.Content[1].Kind != canonical.ContentKindThinking {
		t.Errorf("Content[1].Kind: got %v, want ContentKindThinking", resp.Message.Content[1].Kind)
	}
	if resp.Message.Content[1].Text != "pure reasoning" {
		t.Errorf("Content[1].Text: got %q, want 'pure reasoning'", resp.Message.Content[1].Text)
	}
}

// TestCollect_AggregatesKiroNativeToolCallAsNarration (Phase 6 D-03
// iteration-3 fix to HIGH #1): engine.Collect MUST aggregate
// ChunkKindToolCall into the response's assistant text as
// `[tool: <name>]\n` narration. Non-streaming Ollama/OpenAI receive
// only *canonical.ChatResponse from Collect — without this aggregation,
// kiro-native tool calls would disappear entirely from non-streaming
// responses. Message.ToolCalls remains untouched (per-surface contract:
// Ollama/OpenAI populate via engine.CoerceToolCall; Anthropic populates
// via adapter-local Collect).
func TestCollect_AggregatesKiroNativeToolCallAsNarration(t *testing.T) {
	ack := &fakeACP{
		newSessionID: "sid-toolcall-narration",
		chunksToEmit: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "checking weather "}},
			{Kind: canonical.ChunkKindToolCall, ToolCall: &canonical.ToolCallChunk{
				ID:   "tc_1",
				Name: "get_weather",
				Args: map[string]any{"location": "NYC"},
			}},
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: " done"}},
		},
	}
	e := newTestEngine(t, ack)
	resp, err := e.Collect(context.Background(), simpleUserReq("q", ""))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	// (1) Message.ToolCalls is empty — Collect MUST NOT populate it for
	// any chunk source; that is a per-surface concern (Phase 6 D-03/D-05/D-07).
	if len(resp.Message.ToolCalls) != 0 {
		t.Errorf("Message.ToolCalls: got %d entries, want 0 (Collect must not populate ToolCalls)", len(resp.Message.ToolCalls))
	}
	// (2) Exactly one ContentKindText part with all three substrings in
	// emission order.
	if len(resp.Message.Content) != 1 {
		t.Fatalf("Content parts: got %d, want 1 (text only — no thinking, no tool-use)", len(resp.Message.Content))
	}
	if resp.Message.Content[0].Kind != canonical.ContentKindText {
		t.Errorf("Content[0].Kind: got %v, want ContentKindText", resp.Message.Content[0].Kind)
	}
	text := resp.Message.Content[0].Text
	for _, sub := range []string{"checking weather ", "[tool: get_weather]\n", " done"} {
		if !strings.Contains(text, sub) {
			t.Errorf("Content[0].Text missing %q in:\n%q", sub, text)
		}
	}
	// Ordering: checking < tool-narration < done.
	idxChecking := strings.Index(text, "checking weather ")
	idxNarration := strings.Index(text, "[tool: get_weather]\n")
	idxDone := strings.Index(text, " done")
	if !(idxChecking < idxNarration && idxNarration < idxDone) {
		t.Errorf("expected order checking < narration < done; got indices %d, %d, %d in %q",
			idxChecking, idxNarration, idxDone, text)
	}
	// (3) No ContentKindToolUse parts appended.
	for i, part := range resp.Message.Content {
		if part.Kind == canonical.ContentKindToolUse {
			t.Errorf("Content[%d].Kind: ContentKindToolUse must not be appended by Collect", i)
		}
	}
}

// TestCollect_KiroNativeToolCall_OnlyChunk: a stream containing only a
// tool_call chunk (no surrounding text) yields a single ContentKindText
// part whose Text is exactly `[tool: <name>]\n`. Message.ToolCalls stays
// empty.
func TestCollect_KiroNativeToolCall_OnlyChunk(t *testing.T) {
	ack := &fakeACP{
		newSessionID: "sid-toolcall-only",
		chunksToEmit: []canonical.Chunk{
			{Kind: canonical.ChunkKindToolCall, ToolCall: &canonical.ToolCallChunk{
				Name: "search_web",
				Args: map[string]any{"q": "go"},
			}},
		},
	}
	e := newTestEngine(t, ack)
	resp, err := e.Collect(context.Background(), simpleUserReq("q", ""))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(resp.Message.ToolCalls) != 0 {
		t.Errorf("Message.ToolCalls: got %d entries, want 0", len(resp.Message.ToolCalls))
	}
	if len(resp.Message.Content) != 1 {
		t.Fatalf("Content parts: got %d, want 1", len(resp.Message.Content))
	}
	if got, want := resp.Message.Content[0].Text, "[tool: search_web]\n"; got != want {
		t.Errorf("Content[0].Text: got %q, want %q", got, want)
	}
}

// TestCollect_KiroNativeToolCall_NilName_Fallback: defensive — if the
// ToolCall pointer is nil or Name is empty (should not happen given
// translate.go's firstNonEmpty fallback, but lock the discipline),
// Collect appends `[tool: unknown]\n` rather than panicking or emitting
// a malformed narration.
func TestCollect_KiroNativeToolCall_NilName_Fallback(t *testing.T) {
	ack := &fakeACP{
		newSessionID: "sid-toolcall-nilname",
		chunksToEmit: []canonical.Chunk{
			{Kind: canonical.ChunkKindToolCall, ToolCall: &canonical.ToolCallChunk{
				ID:   "tc_x",
				Name: "",
				Args: nil,
			}},
		},
	}
	e := newTestEngine(t, ack)
	resp, err := e.Collect(context.Background(), simpleUserReq("q", ""))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(resp.Message.Content) != 1 {
		t.Fatalf("Content parts: got %d, want 1", len(resp.Message.Content))
	}
	if got, want := resp.Message.Content[0].Text, "[tool: unknown]\n"; got != want {
		t.Errorf("Content[0].Text: got %q, want %q", got, want)
	}
}

// --- T-5b CollectFromRun tests ---
//
// CollectFromRun is the aggregation half of Collect extracted so adapter
// handlers can re-route a streaming request through the aggregated path
// AFTER eng.Run has returned (e.g. when the PII encrypt Pre hook flipped
// req.Stream=false). The behavior MUST match the pre-T-5b Collect body
// verbatim — these tests pin direct-call, PreHook short-circuit, idle
// timeout, and PostHook error propagation paths.

// TestCollectFromRun_AggregatesText asserts that calling CollectFromRun
// directly on a *Run handle (produced by Engine.Run) drives the same
// chunk aggregation as Collect.
func TestCollectFromRun_AggregatesText(t *testing.T) {
	ack := &fakeACP{
		newSessionID: "sid-from-run-text",
		chunksToEmit: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "hello "}},
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "world"}},
		},
	}
	e := newTestEngine(t, ack)
	req := simpleUserReq("greet", "")
	run, err := e.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	resp, err := e.CollectFromRun(context.Background(), run, req)
	if err != nil {
		t.Fatalf("CollectFromRun: %v", err)
	}
	if len(resp.Message.Content) != 1 {
		t.Fatalf("expected exactly one content part; got %d", len(resp.Message.Content))
	}
	if resp.Message.Content[0].Text != "hello world" {
		t.Errorf("aggregated text: got %q, want 'hello world'", resp.Message.Content[0].Text)
	}
}

// TestCollectFromRun_PreHookShortCircuit asserts that CollectFromRun
// returns the PreHook's response verbatim WITHOUT touching the empty
// stream, and that PostHooks still run on the short-circuited response.
func TestCollectFromRun_PreHookShortCircuit(t *testing.T) {
	hookResp := &canonical.ChatResponse{
		Message: canonical.Message{
			Role: canonical.RoleAssistant,
			Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "from hook"},
			},
		},
		StopReason: canonical.StopEndTurn,
	}
	pre := &fakePreHook{resp: hookResp}
	post := &fakePostHook{
		mutate: func(resp *canonical.ChatResponse) {
			if len(resp.Message.Content) > 0 {
				resp.Message.Content[0].Text = "wrapped: " + resp.Message.Content[0].Text
			}
		},
	}
	ack := &fakeACP{
		newSessionID: "should-not-be-used",
		chunksToEmit: []canonical.Chunk{
			// If the chunk stream were ranged, the response would be "leaked".
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "leaked"}},
		},
	}
	e := newTestEngine(t, ack, withPreHooks(pre), withPostHooks(post))

	req := simpleUserReq("hi", "anything")
	run, err := e.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.ShortCircuitResponse() != hookResp {
		t.Fatal("Run did not preserve the short-circuit response on the handle")
	}
	resp, err := e.CollectFromRun(context.Background(), run, req)
	if err != nil {
		t.Fatalf("CollectFromRun: %v", err)
	}
	if !post.called {
		t.Error("PostHook.After was not called on short-circuit (H-5 contract)")
	}
	if resp.Message.Content[0].Text != "wrapped: from hook" {
		t.Errorf("short-circuit body not wrapped by PostHook; got %q, want 'wrapped: from hook'", resp.Message.Content[0].Text)
	}
	if len(ack.newSessionCalls) != 0 {
		t.Errorf("ACP touched despite short-circuit; got %v", ack.newSessionCalls)
	}
}

// TestCollectFromRun_NilRun asserts the defensive contract: a nil *Run
// handle returns a wrapped error rather than nil-derefing.
func TestCollectFromRun_NilRun(t *testing.T) {
	ack := &fakeACP{}
	e := newTestEngine(t, ack)
	resp, err := e.CollectFromRun(context.Background(), nil, simpleUserReq("x", ""))
	if err == nil {
		t.Fatal("CollectFromRun(nil): expected error, got nil")
	}
	if resp != nil {
		t.Errorf("CollectFromRun(nil): expected nil resp, got %+v", resp)
	}
	if !strings.Contains(err.Error(), "run is nil") {
		t.Errorf("error message: got %q, want substring 'run is nil'", err.Error())
	}
}

// TestCollectFromRun_PostHookError asserts that a PostHook returning a
// non-nil error aborts CollectFromRun with "engine: posthook: ..." wrap
// — same shape as Collect and RunPostHooks.
func TestCollectFromRun_PostHookError(t *testing.T) {
	post := &fakePostHook{err: errors.New("posthook failed")}
	ack := &fakeACP{
		newSessionID: "sid-from-run-posthook-err",
		chunksToEmit: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "x"}},
		},
	}
	e := newTestEngine(t, ack, withPostHooks(post))
	req := simpleUserReq("hi", "auto")
	run, err := e.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	_, err = e.CollectFromRun(context.Background(), run, req)
	if err == nil {
		t.Fatal("CollectFromRun: expected posthook error, got nil")
	}
	if !strings.Contains(err.Error(), "engine: posthook:") {
		t.Errorf("error wrap prefix: got %q, want substring 'engine: posthook:'", err.Error())
	}
	if !strings.Contains(err.Error(), "posthook failed") {
		t.Errorf("error wrap inner: got %q, want substring 'posthook failed'", err.Error())
	}
}

// TestCollectFromRun_IdleTimeout asserts that a stalled stream returns
// (wrapped) canonical.ErrStreamIdleTimeout so adapter handlers can
// errors.Is-check it on the re-route path.
func TestCollectFromRun_IdleTimeout(t *testing.T) {
	// fakeACP that opens a never-closing chunk channel; goleak gating
	// in testmain_test.go would fail any leak — but the test cleans up
	// via the closed Done channel after CollectFromRun returns the
	// timeout error.
	openCh := make(chan canonical.Chunk)
	ack := &idleAckShim{ch: openCh, sessionID: "sid-idle"}
	e := New(Config{
		Logger:            testLogger(t),
		ACP:               ack,
		StreamIdleTimeout: 25 * time.Millisecond,
	})
	req := simpleUserReq("hi", "auto")
	run, err := e.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	_, err = e.CollectFromRun(context.Background(), run, req)
	if err == nil {
		t.Fatal("CollectFromRun: expected idle timeout error, got nil")
	}
	if !errors.Is(err, canonical.ErrStreamIdleTimeout) {
		t.Errorf("error: got %q, want errors.Is(canonical.ErrStreamIdleTimeout)", err.Error())
	}
	// Allow the open channel to drain so goleak does not flag a
	// dangling goroutine reading from it (the AfterFunc-canceled
	// session/cancel is in-flight already; closing the channel lets
	// any reader exit cleanly).
	close(openCh)
}

// testLogger is a per-test discard logger that does not allocate per
// log call. Matches the discipline of newTestEngine but inlined for the
// one TestCollectFromRun_IdleTimeout site that constructs its own
// Config.
func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{}))
}

// idleAckShim is a minimal ACPClient whose Prompt returns a stream
// whose Chunks channel never delivers — drives the idle-timeout path in
// CollectFromRun without bringing the full fakeACP machinery online.
type idleAckShim struct {
	ch        chan canonical.Chunk
	sessionID string
}

func (s *idleAckShim) NewSession(_ context.Context, _ string) (string, error) {
	return s.sessionID, nil
}
func (s *idleAckShim) SetModel(_ context.Context, _, _ string) error { return nil }
func (s *idleAckShim) Prompt(_ context.Context, _ string, _ []canonical.Block) (Stream, error) {
	return &idleStream{ch: s.ch}, nil
}
func (s *idleAckShim) Cancel(_ string) {}

type idleStream struct {
	ch chan canonical.Chunk
}

func (s *idleStream) Chunks() <-chan canonical.Chunk { return s.ch }
func (s *idleStream) Result() (*canonical.FinalResult, error) {
	return &canonical.FinalResult{StopReason: canonical.StopUnknown}, nil
}

// TestCollect_UnchangedBehaviorAfterRefactor — pin test that the public
// Collect surface still works end-to-end after the T-5b refactor split
// it into Run + CollectFromRun. Belt-and-suspenders alongside the
// existing TestCollect_* suite.
func TestCollect_UnchangedBehaviorAfterRefactor(t *testing.T) {
	ack := &fakeACP{
		newSessionID: "sid-refactor-parity",
		chunksToEmit: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "a"}},
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "b"}},
		},
		finalResult: &canonical.FinalResult{StopReason: canonical.StopEndTurn},
	}
	e := newTestEngine(t, ack)
	resp, err := e.Collect(context.Background(), simpleUserReq("hi", "auto"))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(resp.Message.Content) != 1 || resp.Message.Content[0].Text != "ab" {
		t.Errorf("text aggregation: got %v, want 'ab'", resp.Message.Content)
	}
	if resp.StopReason != canonical.StopEndTurn {
		t.Errorf("StopReason: got %v, want StopEndTurn", resp.StopReason)
	}
}

// TestCollect_PropagatesStopReason asserts that final.StopReason from
// the stream flows into the assembled ChatResponse.StopReason.
func TestCollect_PropagatesStopReason(t *testing.T) {
	ack := &fakeACP{
		newSessionID: "sid-stop",
		chunksToEmit: []canonical.Chunk{
			{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "truncated"}},
		},
		finalResult: &canonical.FinalResult{
			SessionID:  "sid-stop",
			ChunkCount: 1,
			StopReason: canonical.StopMaxTokens,
		},
	}
	e := newTestEngine(t, ack)
	resp, err := e.Collect(context.Background(), simpleUserReq("q", ""))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if resp.StopReason != canonical.StopMaxTokens {
		t.Errorf("StopReason: got %v, want StopMaxTokens", resp.StopReason)
	}
}
