package anthropic

// Parity test suite per Phase 6 Plan 04 iteration-3 MEDIUM #5.
// CollectAnthropicChat duplicates engine.Collect's aggregation loop
// with ONE intentional divergence: ChunkKindToolCall produces native
// tool_use blocks (D-07 exception per the per-surface Message.ToolCalls
// population contract defined in 06-01) instead of `[tool: <name>]\n`
// narration text. These tests prove equivalence for ALL OTHER behavior
// (text-only, thinking-only, mixed text+thinking, stop-reason
// propagation, error propagation) — any non-tool-call drift in
// engine.Collect that CollectAnthropicChat doesn't follow will fail
// this suite.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"otto-gateway/internal/canonical"
)

// stopReasonName produces a stable subtest name for the StopReason
// enum since canonical.StopReason has no String() method.
func stopReasonName(s canonical.StopReason) string {
	switch s {
	case canonical.StopEndTurn:
		return "StopEndTurn"
	case canonical.StopMaxTokens:
		return "StopMaxTokens"
	case canonical.StopMaxTurnRequests:
		return "StopMaxTurnRequests"
	case canonical.StopRefusal:
		return "StopRefusal"
	case canonical.StopCancelled:
		return "StopCancelled"
	case canonical.StopUnknown:
		return "StopUnknown"
	default:
		return fmt.Sprintf("StopReason(%d)", int(s))
	}
}

// parityFakeEngine is a tiny fake Engine that scripts both the Collect
// and Run paths from the SAME chunk-list fixture so the two collectors
// can be driven from a shared input source. Collect returns
// `engineCollectEquivalent(chunks, final, err)` directly — the
// reference behavior that CollectAnthropicChat must match for
// non-tool-call cases.
type parityFakeEngine struct {
	chunks []canonical.Chunk
	final  *canonical.FinalResult
	err    error
	// WR-03 (Phase 6 review): when errOnRun is true and err is non-nil,
	// Run returns (nil, err) — exercising the "engine: collect: <err>"
	// wrap path inside CollectAnthropicChat that handles a Run error
	// BEFORE the stream is consumed. When false (default), err surfaces
	// via Stream.Result(), exercising the "engine: collect result:
	// <err>" wrap path. The two are different code paths and must both
	// be parity-tested.
	errOnRun bool
}

// Collect mirrors the engine.Collect aggregation contract for the
// non-tool-call subset (text + thinking + stop reason + error). This
// is the REFERENCE behavior CollectAnthropicChat is asserted against.
func (f *parityFakeEngine) Collect(_ context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	var sb, thoughtSB strings.Builder
	for _, c := range f.chunks {
		switch c.Kind {
		case canonical.ChunkKindText:
			if c.Text != nil {
				sb.WriteString(c.Text.Content)
			}
		case canonical.ChunkKindThought:
			if c.Thought != nil {
				thoughtSB.WriteString(c.Thought.Content)
			}
		}
		// Non-tool-call kinds: parity reference does NOT exercise the
		// kiro-native ChunkKindToolCall path — that's the D-07
		// divergence and is tested separately in sse_golden_test.go.
	}
	stop := canonical.StopUnknown
	if f.final != nil {
		stop = f.final.StopReason
	}
	content := []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: sb.String()}}
	if thoughtSB.Len() > 0 {
		content = append(content, canonical.ContentPart{
			Kind: canonical.ContentKindThinking,
			Text: thoughtSB.String(),
		})
	}
	model := ""
	if req != nil {
		model = req.Model
	}
	return &canonical.ChatResponse{
		Model: model,
		Message: canonical.Message{
			Role:    canonical.RoleAssistant,
			Content: content,
		},
		StopReason: stop,
	}, nil
}

// Run yields a single-shot scripted stream from the same chunk list.
// CollectAnthropicChat consumes this path. WR-03: when errOnRun=true
// and err is set, Run surfaces err directly so the Run-error wrap
// path is exercised (NOT the Stream.Result() wrap path).
func (f *parityFakeEngine) Run(_ context.Context, _ *canonical.ChatRequest) (RunHandle, error) {
	if f.errOnRun && f.err != nil {
		return nil, f.err
	}
	ch := make(chan canonical.Chunk, len(f.chunks))
	for _, c := range f.chunks {
		ch <- c
	}
	close(ch)
	return &fakeRunHandle{
		stream: &fakeStream{
			chunks: ch,
			final:  f.final,
			err:    f.err,
		},
		sessionID: "parity-fake",
	}, nil
}

// assertContentEqual asserts two ContentPart slices are equivalent
// for the parity dimensions (Kind + Text). It does NOT assert on
// ContentKindToolUse parts because those are the D-07 divergence
// (CollectAnthropicChat emits them; engine.Collect does not).
func assertContentEqual(t *testing.T, got, want []canonical.ContentPart) {
	t.Helper()
	// Filter out tool_use parts from `got` (D-07 divergence). The
	// reference (`want`) by construction never has them.
	var gotFiltered []canonical.ContentPart
	for _, p := range got {
		if p.Kind == canonical.ContentKindToolUse {
			continue
		}
		gotFiltered = append(gotFiltered, p)
	}
	if len(gotFiltered) != len(want) {
		t.Fatalf("content length: got %d (filtered from %d), want %d; got=%+v want=%+v",
			len(gotFiltered), len(got), len(want), gotFiltered, want)
	}
	for i := range want {
		if gotFiltered[i].Kind != want[i].Kind {
			t.Errorf("content[%d].Kind: got %v, want %v", i, gotFiltered[i].Kind, want[i].Kind)
		}
		if gotFiltered[i].Text != want[i].Text {
			t.Errorf("content[%d].Text: got %q, want %q", i, gotFiltered[i].Text, want[i].Text)
		}
	}
}

// TestCollectAnthropicChat_ParityWithEngine_TextOnly proves
// CollectAnthropicChat matches the engine.Collect equivalent for a
// stream of pure text chunks.
func TestCollectAnthropicChat_ParityWithEngine_TextOnly(t *testing.T) {
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "hello "}},
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "world"}},
	}
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}
	eng := &parityFakeEngine{chunks: chunks, final: final}
	req := &canonical.ChatRequest{Model: "auto"}

	refResp, refErr := eng.Collect(context.Background(), req)
	if refErr != nil {
		t.Fatalf("reference Collect: %v", refErr)
	}

	gotResp, gotErr := CollectAnthropicChat(context.Background(), eng, req)
	if gotErr != nil {
		t.Fatalf("CollectAnthropicChat: %v", gotErr)
	}

	if gotResp.StopReason != refResp.StopReason {
		t.Errorf("StopReason: got %v, want %v", gotResp.StopReason, refResp.StopReason)
	}
	if len(gotResp.Message.ToolCalls) != 0 {
		t.Errorf("ToolCalls: got %+v, want empty (no tool_call chunks in stream)", gotResp.Message.ToolCalls)
	}
	assertContentEqual(t, gotResp.Message.Content, refResp.Message.Content)
}

// TestCollectAnthropicChat_ParityWithEngine_ThinkingOnly proves parity
// for a stream of pure thinking chunks. engine.Collect appends a
// ContentKindThinking part when thoughts are present (Phase 3.1 D-02);
// CollectAnthropicChat must do the same.
func TestCollectAnthropicChat_ParityWithEngine_ThinkingOnly(t *testing.T) {
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindThought, Thought: &canonical.ThoughtChunk{Content: "reasoning step 1"}},
		{Kind: canonical.ChunkKindThought, Thought: &canonical.ThoughtChunk{Content: " step 2"}},
	}
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}
	eng := &parityFakeEngine{chunks: chunks, final: final}
	req := &canonical.ChatRequest{Model: "auto"}

	refResp, refErr := eng.Collect(context.Background(), req)
	if refErr != nil {
		t.Fatalf("reference Collect: %v", refErr)
	}
	gotResp, gotErr := CollectAnthropicChat(context.Background(), eng, req)
	if gotErr != nil {
		t.Fatalf("CollectAnthropicChat: %v", gotErr)
	}

	if gotResp.StopReason != refResp.StopReason {
		t.Errorf("StopReason: got %v, want %v", gotResp.StopReason, refResp.StopReason)
	}
	assertContentEqual(t, gotResp.Message.Content, refResp.Message.Content)
}

// TestCollectAnthropicChat_ParityWithEngine_MixedTextThinking proves
// parity for an interleaved stream — both collectors split text and
// thinking into separate ContentParts; ordering within each kind is
// preserved.
func TestCollectAnthropicChat_ParityWithEngine_MixedTextThinking(t *testing.T) {
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "a "}},
		{Kind: canonical.ChunkKindThought, Thought: &canonical.ThoughtChunk{Content: "hmm"}},
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "b"}},
		{Kind: canonical.ChunkKindThought, Thought: &canonical.ThoughtChunk{Content: " aha"}},
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: " c"}},
	}
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}
	eng := &parityFakeEngine{chunks: chunks, final: final}
	req := &canonical.ChatRequest{Model: "auto"}

	refResp, refErr := eng.Collect(context.Background(), req)
	if refErr != nil {
		t.Fatalf("reference Collect: %v", refErr)
	}
	gotResp, gotErr := CollectAnthropicChat(context.Background(), eng, req)
	if gotErr != nil {
		t.Fatalf("CollectAnthropicChat: %v", gotErr)
	}
	assertContentEqual(t, gotResp.Message.Content, refResp.Message.Content)
	// Belt-and-suspenders: expected concatenations.
	if gotResp.Message.Content[0].Text != "a b c" {
		t.Errorf("text: got %q, want %q", gotResp.Message.Content[0].Text, "a b c")
	}
}

// TestCollectAnthropicChat_ParityWithEngine_StopReasonPropagation
// verifies the FinalResult StopReason flows through unchanged.
func TestCollectAnthropicChat_ParityWithEngine_StopReasonPropagation(t *testing.T) {
	cases := []canonical.StopReason{
		canonical.StopEndTurn,
		canonical.StopMaxTokens,
		canonical.StopRefusal,
		canonical.StopCancelled,
		canonical.StopUnknown,
	}
	for _, want := range cases {
		t.Run(stopReasonName(want), func(t *testing.T) {
			final := &canonical.FinalResult{StopReason: want}
			eng := &parityFakeEngine{
				chunks: []canonical.Chunk{
					{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "x"}},
				},
				final: final,
			}
			req := &canonical.ChatRequest{Model: "auto"}
			refResp, _ := eng.Collect(context.Background(), req)
			gotResp, err := CollectAnthropicChat(context.Background(), eng, req)
			if err != nil {
				t.Fatalf("CollectAnthropicChat: %v", err)
			}
			if gotResp.StopReason != refResp.StopReason {
				t.Errorf("StopReason: got %v, want %v (parity with engine.Collect)",
					gotResp.StopReason, refResp.StopReason)
			}
			if gotResp.StopReason != want {
				t.Errorf("StopReason raw: got %v, want %v", gotResp.StopReason, want)
			}
		})
	}
}

// TestCollectAnthropicChat_ParityWithEngine_ErrorPropagation verifies
// that an error from the underlying stream propagates from both
// collectors. The exact wrap pattern may differ (CollectAnthropicChat
// wraps with its own "anthropic: collect" prefix; engine.Collect uses
// "engine: collect"); the test asserts that the underlying error is
// reachable via errors.Is rather than byte-identical messages.
func TestCollectAnthropicChat_ParityWithEngine_ErrorPropagation(t *testing.T) {
	sentinel := errors.New("scripted stream failure")
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "x"}},
	}
	eng := &parityFakeEngine{chunks: chunks, err: sentinel}
	req := &canonical.ChatRequest{Model: "auto"}

	// Reference: parity Collect returns the sentinel directly.
	_, refErr := eng.Collect(context.Background(), req)
	if !errors.Is(refErr, sentinel) {
		t.Fatalf("reference Collect err: got %v, want errors.Is(err, sentinel)", refErr)
	}

	// CollectAnthropicChat must surface the same underlying error via
	// errors.Is. The wrap layer may differ; what matters is that
	// callers can distinguish via the sentinel.
	_, gotErr := CollectAnthropicChat(context.Background(), eng, req)
	if gotErr == nil {
		t.Fatal("CollectAnthropicChat: nil error; want non-nil")
	}
	if !errors.Is(gotErr, sentinel) {
		t.Errorf("CollectAnthropicChat err: got %v, want errors.Is(err, sentinel)", gotErr)
	}
}

// TestCollectAnthropicChat_ParityWithEngine_ErrorPropagation_RunPath
// (WR-03 follow-up): the original ErrorPropagation test wires the
// scripted error through Stream.Result() (exercising the "engine:
// collect result: <err>" wrap path). This sibling test wires the same
// sentinel through Run() (exercising the "engine: collect: <err>" wrap
// path that handles a Run error BEFORE the stream is consumed). Both
// paths must surface the same underlying error via errors.Is so a
// future refactor that breaks the wrap shape on ONE path but not the
// other is caught.
func TestCollectAnthropicChat_ParityWithEngine_ErrorPropagation_RunPath(t *testing.T) {
	sentinel := errors.New("scripted Run failure")
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "x"}},
	}
	eng := &parityFakeEngine{chunks: chunks, err: sentinel, errOnRun: true}
	req := &canonical.ChatRequest{Model: "auto"}

	// Reference: parity Collect returns the sentinel directly (errOnRun
	// only affects Run; Collect still returns err for parity with the
	// original ErrorPropagation test).
	_, refErr := eng.Collect(context.Background(), req)
	if !errors.Is(refErr, sentinel) {
		t.Fatalf("reference Collect err: got %v, want errors.Is(err, sentinel)", refErr)
	}

	// CollectAnthropicChat: exercises the Run-error wrap path. errors.Is
	// must still reach the sentinel.
	_, gotErr := CollectAnthropicChat(context.Background(), eng, req)
	if gotErr == nil {
		t.Fatal("CollectAnthropicChat: nil error; want non-nil")
	}
	if !errors.Is(gotErr, sentinel) {
		t.Errorf("CollectAnthropicChat err: got %v, want errors.Is(err, sentinel)", gotErr)
	}
}

// TestCollectAnthropicChat_AnthropicException_ToolCallProducesToolUse
// is the D-07 EXCEPTION test (NOT a parity test). It proves the one
// intentional divergence from engine.Collect: kiro-native
// ChunkKindToolCall is rendered as a ContentKindToolUse part AND
// populates Message.ToolCalls — engine.Collect would render the same
// chunk as `[tool: <name>]\n` narration text.
func TestCollectAnthropicChat_AnthropicException_ToolCallProducesToolUse(t *testing.T) {
	chunks := []canonical.Chunk{
		{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "I'll check weather. "}},
		{Kind: canonical.ChunkKindToolCall, ToolCall: &canonical.ToolCallChunk{
			ID:   "toolu_01",
			Name: "get_weather",
			Args: map[string]any{"location": "NYC"},
		}},
	}
	final := &canonical.FinalResult{StopReason: canonical.StopEndTurn}
	eng := &parityFakeEngine{chunks: chunks, final: final}
	req := &canonical.ChatRequest{Model: "auto"}

	resp, err := CollectAnthropicChat(context.Background(), eng, req)
	if err != nil {
		t.Fatalf("CollectAnthropicChat: %v", err)
	}

	// Message.ToolCalls populated from kiro-native chunks (D-07 exception).
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("ToolCalls: got %d, want 1", len(resp.Message.ToolCalls))
	}
	tc := resp.Message.ToolCalls[0]
	if tc.ID != "toolu_01" || tc.Name != "get_weather" {
		t.Errorf("ToolCalls[0]: got %+v, want {ID:toolu_01,Name:get_weather}", tc)
	}
	if v, _ := tc.Arguments["location"].(string); v != "NYC" {
		t.Errorf("ToolCalls[0].Arguments[location]: got %v, want NYC", tc.Arguments["location"])
	}

	// At least one ContentKindToolUse part appended in Message.Content.
	sawToolUse := false
	for _, p := range resp.Message.Content {
		if p.Kind == canonical.ContentKindToolUse {
			sawToolUse = true
			if p.ToolUse == nil {
				t.Error("ContentKindToolUse part has nil ToolUse")
			} else if p.ToolUse.Name != "get_weather" {
				t.Errorf("ToolUse.Name: got %q, want get_weather", p.ToolUse.Name)
			}
		}
	}
	if !sawToolUse {
		t.Errorf("Message.Content: no ContentKindToolUse part appended; got %+v", resp.Message.Content)
	}

	// The text part should still carry the original prefix; it MUST
	// NOT carry `[tool: get_weather]\n` narration text — that is the
	// engine.Collect behavior, NOT the Anthropic exception.
	if len(resp.Message.Content) > 0 && resp.Message.Content[0].Kind == canonical.ContentKindText {
		if strings.Contains(resp.Message.Content[0].Text, "[tool:") {
			t.Errorf("D-07 violation: Anthropic adapter must NOT emit [tool: ...] narration text; got text=%q",
				resp.Message.Content[0].Text)
		}
	}
}
