// Track 3b defect fixes — Anthropic SSE tool-call-wrapper coercion.
//
// H2: the one-shot buffering decision must be deferred past leading
// whitespace-only text chunks (a kiro "\n" token before the wrapper must
// not permanently disable coercion).
//
// H3(b): a non-text chunk (thinking / native tool_use) that interleaves
// while buffered wrapper text is pending must resolve the buffer FIRST so
// wire order stays canonical rather than [non-text, buffered-text].
//
// Mirrors the sse_coerce_test.go / sse_test.go harness (newEmitter +
// fakeRunHandle + runSSEEmitterLoop).
package anthropic

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"otto-gateway/internal/canonical"
)

// driveChunks feeds arbitrary canonical chunks through runSSEEmitterLoop
// against the supplied emitter, closing the channel so finalizeStream
// runs. Returns the recorded wire body.
func driveChunks(t *testing.T, e *sseEmitter, cf *countingFlusher, stop canonical.StopReason, chunks ...canonical.Chunk) string {
	t.Helper()
	ch := make(chan canonical.Chunk, len(chunks))
	for _, c := range chunks {
		ch <- c
	}
	close(ch)
	runHandle := &fakeRunHandle{
		stream: &fakeStream{
			chunks: ch,
			final:  &canonical.FinalResult{StopReason: stop},
		},
		sessionID: "coerce-order-test",
	}
	tickerC := make(chan time.Time)
	if _, err := runSSEEmitterLoop(context.Background(), e, runHandle, tickerC, 0); err != nil {
		t.Fatalf("runSSEEmitterLoop: %v", err)
	}
	return cf.Body()
}

// firstIndexOf returns the emission index (in data-line order) of the
// first data frame whose payload contains needle, or -1.
func firstIndexOf(body, needle string) int {
	for i, data := range sseDataLines(body) {
		if strings.Contains(data, needle) {
			return i
		}
	}
	return -1
}

// contentBlockStartsByIndex maps each content_block_start's `index` to
// its content_block `type` in emission order.
func contentBlockStartsByIndex(t *testing.T, body string) []struct {
	Index int
	Type  string
} {
	t.Helper()
	events := sseEventLines(body)
	datas := sseDataLines(body)
	var out []struct {
		Index int
		Type  string
	}
	for i, ev := range events {
		if ev != "content_block_start" || i >= len(datas) {
			continue
		}
		var parsed struct {
			Index        int `json:"index"`
			ContentBlock struct {
				Type string `json:"type"`
			} `json:"content_block"`
		}
		if err := json.Unmarshal([]byte(datas[i]), &parsed); err != nil {
			t.Fatalf("unmarshal content_block_start %q: %v", datas[i], err)
		}
		out = append(out, struct {
			Index int
			Type  string
		}{parsed.Index, parsed.ContentBlock.Type})
	}
	return out
}

func textChunk(s string) canonical.Chunk {
	return canonical.Chunk{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: s}}
}

// TestSSE_H2_LeadingWhitespaceThenWrapper_StillCoerces — H2 fix. A
// whitespace-only chunk BEFORE the fenced wrapper must not disable
// coercion: the wrapper still resolves to native tool_use frames at
// end-of-stream, the raw JSON / leading whitespace never leaks as text.
func TestSSE_H2_LeadingWhitespaceThenWrapper_StillCoerces(t *testing.T) {
	defer goleak.VerifyNone(t)
	cf := newCountingFlusher()
	e := newEmitter(cf)
	e.tools = []canonical.ToolSpec{{Name: "get_weather"}}

	body := driveChunks(
		t, e, cf, canonical.StopEndTurn,
		textChunk("\n "),
		textChunk("```json\n"),
		textChunk(`{"tool_call":{"name":"get_weather",`),
		textChunk(`"arguments":{"city":"Paris"}}}`),
		textChunk("\n```"),
	)

	// No text_delta at all — neither the raw wrapper nor the leading
	// whitespace may reach the wire as text.
	if strings.Contains(body, `"type":"text_delta"`) {
		t.Errorf("text leaked as a text_delta frame (whitespace disabled coercion?); body:\n%s", body)
	}
	if !strings.Contains(body, `"type":"tool_use"`) || !strings.Contains(body, `"name":"get_weather"`) {
		t.Errorf("missing coerced tool_use for get_weather; body:\n%s", body)
	}
	payloads := inputJSONDeltaPayloads(t, body)
	if len(payloads) != 1 {
		t.Fatalf("input_json_delta count: got %d, want 1; body:\n%s", len(payloads), body)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(payloads[0]), &args); err != nil {
		t.Fatalf("partial_json not valid JSON %q: %v", payloads[0], err)
	}
	if city, _ := args["city"].(string); city != "Paris" {
		t.Errorf("args city: got %q, want Paris; partial_json=%q", city, payloads[0])
	}
	if sr := messageDeltaStopReason(t, body); sr != "tool_use" {
		t.Errorf("stop_reason: got %q, want tool_use", sr)
	}
	if !e.toolUseEmitted {
		t.Error("toolUseEmitted: got false, want true")
	}
}

// TestSSE_H2_LeadingWhitespaceThenProse_ReplaysWhitespace — H2 no-buffer
// path. Whitespace then prose (tools declared) streams the whitespace
// then the prose as text_delta(s) exactly, byte-identical to the
// tool-less baseline, with no tool_use and a non-tool_use stop_reason.
func TestSSE_H2_LeadingWhitespaceThenProse_ReplaysWhitespace(t *testing.T) {
	defer goleak.VerifyNone(t)

	// With tools declared and a leading whitespace chunk.
	cfTools := newCountingFlusher()
	eTools := newEmitter(cfTools)
	eTools.tools = []canonical.ToolSpec{{Name: "get_weather"}}
	bodyTools := driveChunks(t, eTools, cfTools, canonical.StopEndTurn,
		textChunk("\n "), textChunk("Hello world"))

	// Baseline: no tools, same chunks — the passthrough reference.
	cfBare := newCountingFlusher()
	eBare := newEmitter(cfBare)
	bodyBare := driveChunks(t, eBare, cfBare, canonical.StopEndTurn,
		textChunk("\n "), textChunk("Hello world"))

	if bodyTools != bodyBare {
		t.Errorf("whitespace+prose diverged with tools declared (H2 replay regression)\nwith tools:\n%s\nbaseline:\n%s", bodyTools, bodyBare)
	}
	if eTools.buffering {
		t.Error("buffering engaged on whitespace+prose (must not)")
	}
	// Two text_delta frames: the whitespace then the prose, in order.
	if n := strings.Count(bodyTools, `"type":"text_delta"`); n != 2 {
		t.Errorf("text_delta count: got %d, want 2 (whitespace + prose); body:\n%s", n, bodyTools)
	}
	wsIdx := firstIndexOf(bodyTools, `"text":"\n "`)
	proseIdx := firstIndexOf(bodyTools, `Hello world`)
	if wsIdx < 0 || proseIdx < 0 || wsIdx >= proseIdx {
		t.Errorf("whitespace must stream as its own delta BEFORE the prose; wsIdx=%d proseIdx=%d body:\n%s", wsIdx, proseIdx, bodyTools)
	}
	if strings.Contains(bodyTools, `"type":"tool_use"`) {
		t.Errorf("whitespace+prose produced a tool_use block; body:\n%s", bodyTools)
	}
	if sr := messageDeltaStopReason(t, bodyTools); sr == "tool_use" {
		t.Errorf("stop_reason: got tool_use, want non-tool_use")
	}
}

// TestSSE_H2_ProseFirst_Passthrough_ByteIdentical — passthrough
// regression. A prose-first stream (no leading whitespace) with tools
// declared is byte-identical to the tool-less baseline.
func TestSSE_H2_ProseFirst_Passthrough_ByteIdentical(t *testing.T) {
	defer goleak.VerifyNone(t)

	cfTools := newCountingFlusher()
	eTools := newEmitter(cfTools)
	eTools.tools = []canonical.ToolSpec{{Name: "get_weather"}}
	bodyTools := driveChunks(t, eTools, cfTools, canonical.StopEndTurn,
		textChunk("The answer "), textChunk("is 42."))

	cfBare := newCountingFlusher()
	eBare := newEmitter(cfBare)
	bodyBare := driveChunks(t, eBare, cfBare, canonical.StopEndTurn,
		textChunk("The answer "), textChunk("is 42."))

	if bodyTools != bodyBare {
		t.Errorf("prose-first stream diverged with tools declared (passthrough regression)\nwith tools:\n%s\nbaseline:\n%s", bodyTools, bodyBare)
	}
	if eTools.buffering {
		t.Error("buffering engaged on prose-first stream (must not)")
	}
}

// TestSSE_H3b_ThoughtInterleave_PreservesOrder — H3(b) fix. With tools
// declared, a fenced opener triggers buffering; a thinking chunk then
// interleaves; more text completes a NON-wrapper. The buffered text must
// resolve (flush as a text block) BEFORE the thinking block on the wire.
func TestSSE_H3b_ThoughtInterleave_PreservesOrder(t *testing.T) {
	defer goleak.VerifyNone(t)
	cf := newCountingFlusher()
	e := newEmitter(cf)
	e.tools = []canonical.ToolSpec{{Name: "get_weather"}}

	body := driveChunks(
		t, e, cf, canonical.StopEndTurn,
		textChunk("```json\n"), // buffering triggers, no complete wrapper
		canonical.Chunk{Kind: canonical.ChunkKindThought, Thought: &canonical.ThoughtChunk{Content: "hmm"}},
		textChunk("tail prose"),
	)

	// No coercion — the buffer was an incomplete (non-wrapper) fence.
	if strings.Contains(body, `"type":"tool_use"`) {
		t.Errorf("incomplete fence forged a tool_use block; body:\n%s", body)
	}
	// The buffered text ("```json\n") must be flushed as text and appear
	// BEFORE the thinking_delta — order preserved, not reordered.
	bufIdx := firstIndexOf(body, `json`)
	thinkIdx := firstIndexOf(body, `"type":"thinking_delta"`)
	if bufIdx < 0 {
		t.Fatalf("buffered fence text not flushed as text_delta; body:\n%s", body)
	}
	if thinkIdx < 0 {
		t.Fatalf("thinking_delta missing; body:\n%s", body)
	}
	if bufIdx >= thinkIdx {
		t.Errorf("buffered text must precede the thinking block (order preserved); bufIdx=%d thinkIdx=%d body:\n%s", bufIdx, thinkIdx, body)
	}
	// Blocks: text (0), thinking (1), text (2) — clean index walk.
	starts := contentBlockStartsByIndex(t, body)
	if len(starts) != 3 ||
		starts[0].Type != "text" || starts[0].Index != 0 ||
		starts[1].Type != "thinking" || starts[1].Index != 1 ||
		starts[2].Type != "text" || starts[2].Index != 2 {
		t.Errorf("block sequence/indices wrong: %+v; body:\n%s", starts, body)
	}
}

// TestSSE_H3b_NativeToolCallInterleave_NoDoubleToolUse — H3(b) fix,
// native-tool_call variant. Buffering active (incomplete fence), then a
// NATIVE tool_use chunk arrives. The buffered text must flush as a text
// block FIRST (index 0); the native tool_use block follows (index 1).
// Exactly one tool_use block on the wire — no double emission.
func TestSSE_H3b_NativeToolCallInterleave_NoDoubleToolUse(t *testing.T) {
	defer goleak.VerifyNone(t)
	cf := newCountingFlusher()
	e := newEmitter(cf)
	e.tools = []canonical.ToolSpec{{Name: "search"}}

	body := driveChunks(
		t, e, cf, canonical.StopEndTurn,
		textChunk("```json\n"), // buffering triggers, incomplete wrapper
		canonical.Chunk{Kind: canonical.ChunkKindToolCall, ToolCall: &canonical.ToolCallChunk{
			ID:   "toolu_native",
			Name: "search",
			Args: map[string]any{"q": "hi"},
		}},
	)

	// Exactly one tool_use block — the native one; the incomplete fence
	// must NOT also coerce.
	if n := strings.Count(body, `"type":"tool_use"`); n != 1 {
		t.Errorf("tool_use block count: got %d, want 1 (no double emission); body:\n%s", n, body)
	}
	if !strings.Contains(body, `"name":"search"`) {
		t.Errorf("missing native tool_use name search; body:\n%s", body)
	}
	// Buffered fence text flushed BEFORE the native tool_use args.
	bufIdx := firstIndexOf(body, `json`)
	argsIdx := firstIndexOf(body, `"type":"input_json_delta"`)
	if bufIdx < 0 || argsIdx < 0 || bufIdx >= argsIdx {
		t.Errorf("buffered text must precede the native tool_use; bufIdx=%d argsIdx=%d body:\n%s", bufIdx, argsIdx, body)
	}
	// Blocks: text (0) then tool_use (1) — clean index walk, no double.
	starts := contentBlockStartsByIndex(t, body)
	if len(starts) != 2 ||
		starts[0].Type != "text" || starts[0].Index != 0 ||
		starts[1].Type != "tool_use" || starts[1].Index != 1 {
		t.Errorf("block sequence/indices wrong: %+v; body:\n%s", starts, body)
	}
	if sr := messageDeltaStopReason(t, body); sr != "tool_use" {
		t.Errorf("stop_reason: got %q, want tool_use (native tool_use emitted)", sr)
	}
}
