// Track 3b Task 5 — Anthropic streaming (SSE) tool-call-wrapper coercion.
//
// kiro can emit an explicit {"tool_call":{"name","arguments"}} wrapper as
// assistant TEXT (the JS gateway's coercion apparatus). On the Anthropic
// SSE surface this text must be buffered (not streamed as text_delta) and,
// at end-of-stream, coerced into native tool_use content-block frames via
// engine.ExtractToolCallWrappers (the UNAMBIGUOUS wrapper extractor —
// Anthropic NEVER calls engine.CoerceToolCall, the bare-{args} heuristic).
//
// These tests mirror the sse_test.go harness (newEmitter + fakeRunHandle +
// runSSEEmitterLoop) and the non-streaming collect coercion tests.
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

// driveTextChunks feeds the supplied text fragments as ChunkKindText
// chunks through runSSEEmitterLoop against the supplied emitter, closing
// the channel so finalizeStream runs. Returns the recorded wire body.
func driveTextChunks(t *testing.T, e *sseEmitter, cf *countingFlusher, stop canonical.StopReason, fragments ...string) string { //nolint:unparam // test helper intentionally general over stop reason
	t.Helper()
	ch := make(chan canonical.Chunk, len(fragments))
	for _, f := range fragments {
		ch <- canonical.Chunk{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: f}}
	}
	close(ch)
	runHandle := &fakeRunHandle{
		stream: &fakeStream{
			chunks: ch,
			final:  &canonical.FinalResult{StopReason: stop},
		},
		sessionID: "coerce-sse-test",
	}
	tickerC := make(chan time.Time)
	if _, err := runSSEEmitterLoop(context.Background(), e, runHandle, tickerC, 0); err != nil {
		t.Fatalf("runSSEEmitterLoop: %v", err)
	}
	return cf.Body()
}

// inputJSONDeltaPayloads returns the partial_json strings from every
// input_json_delta content_block_delta frame in emission order.
func inputJSONDeltaPayloads(t *testing.T, body string) []string {
	t.Helper()
	var out []string
	for _, data := range sseDataLines(body) {
		var parsed struct {
			Delta struct {
				Type        string `json:"type"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &parsed); err != nil {
			continue
		}
		if parsed.Delta.Type == "input_json_delta" {
			out = append(out, parsed.Delta.PartialJSON)
		}
	}
	return out
}

// messageDeltaStopReason returns the stop_reason string on the
// message_delta frame (empty if absent/null).
func messageDeltaStopReason(t *testing.T, body string) string {
	t.Helper()
	events := sseEventLines(body)
	datas := sseDataLines(body)
	for i, ev := range events {
		if ev != "message_delta" || i >= len(datas) {
			continue
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(datas[i]), &parsed); err != nil {
			return ""
		}
		delta, _ := parsed["delta"].(map[string]any)
		sr, _ := delta["stop_reason"].(string)
		return sr
	}
	return ""
}

// TestSSE_CoercesToolCallWrapper_EmitsToolUseFrames — POSITIVE. A fenced
// {"tool_call":{name,arguments}} wrapper split across text chunks, with
// the tool declared, must coerce into native tool_use frames at
// end-of-stream. The raw wrapper JSON must NOT reach the wire as a
// text_delta, and message_delta must carry stop_reason:"tool_use".
func TestSSE_CoercesToolCallWrapper_EmitsToolUseFrames(t *testing.T) {
	defer goleak.VerifyNone(t)
	cf := newCountingFlusher()
	e := newEmitter(cf)
	e.tools = []canonical.ToolSpec{{Name: "get_weather"}}

	body := driveTextChunks(
		t, e, cf, canonical.StopEndTurn,
		"```json\n",
		`{"tool_call":{"name":"get_weather",`,
		`"arguments":{"city":"Paris"}}}`,
		"\n```",
	)

	// The raw wrapper JSON must not leak as text_delta.
	if strings.Contains(body, `"type":"text_delta"`) {
		t.Errorf("wrapper JSON leaked as a text_delta frame; body:\n%s", body)
	}
	// A tool_use content_block_start with the declared name.
	if !strings.Contains(body, `"type":"tool_use"`) {
		t.Errorf("missing tool_use content_block_start; body:\n%s", body)
	}
	if !strings.Contains(body, `"name":"get_weather"`) {
		t.Errorf("missing tool name get_weather; body:\n%s", body)
	}
	// Exactly one input_json_delta whose partial_json parses to {"city":"Paris"}.
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
	// content_block_stop present for the tool_use block.
	events := sseEventLines(body)
	wantSeq := []string{"content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	if !equalSlice(events, wantSeq) {
		t.Errorf("event sequence: got %v, want %v", events, wantSeq)
	}
	// stop_reason override.
	if sr := messageDeltaStopReason(t, body); sr != "tool_use" {
		t.Errorf("stop_reason: got %q, want tool_use", sr)
	}
	if !e.toolUseEmitted {
		t.Error("toolUseEmitted: got false, want true")
	}
}

// TestSSE_BareJSON_NotCoerced_FlushedAsText — NEGATIVE (anti-forgery).
// Bare {"location":"NYC"} text (no tool_call key) with tools declared must
// NOT produce tool_use frames; the text is flushed verbatim as a normal
// text block and stop_reason is NOT tool_use.
func TestSSE_BareJSON_NotCoerced_FlushedAsText(t *testing.T) {
	defer goleak.VerifyNone(t)
	cf := newCountingFlusher()
	e := newEmitter(cf)
	e.tools = []canonical.ToolSpec{{
		Name: "get_weather",
		Parameters: map[string]any{
			"properties": map[string]any{"location": map[string]any{"type": "string"}},
		},
	}}

	body := driveTextChunks(t, e, cf, canonical.StopEndTurn, `{"location":"NYC"}`)

	// No tool_use frames — bare JSON must never forge a tool call on Anthropic.
	if strings.Contains(body, `"type":"tool_use"`) {
		t.Errorf("bare JSON forged a tool_use block (anti-forgery violation); body:\n%s", body)
	}
	// The text is flushed verbatim as a text block.
	if !strings.Contains(body, `"type":"text_delta"`) {
		t.Errorf("buffered bare JSON not flushed as a text_delta; body:\n%s", body)
	}
	if !strings.Contains(body, `location`) {
		t.Errorf("flushed text_delta missing the original JSON text; body:\n%s", body)
	}
	events := sseEventLines(body)
	wantSeq := []string{"content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	if !equalSlice(events, wantSeq) {
		t.Errorf("event sequence: got %v, want %v", events, wantSeq)
	}
	if sr := messageDeltaStopReason(t, body); sr == "tool_use" {
		t.Errorf("stop_reason: got tool_use, want a non-tool_use reason (no coercion happened)")
	}
	if e.toolUseEmitted {
		t.Error("toolUseEmitted: got true, want false (no coercion)")
	}
}

// TestSSE_NormalProse_NotBuffered_StreamsImmediately — PASSTHROUGH. A
// normal prose stream, even with tools declared, must NOT be buffered: it
// streams as text_delta frames exactly as it arrives, byte-for-byte
// identical to the tool-less path.
func TestSSE_NormalProse_NotBuffered_StreamsImmediately(t *testing.T) {
	defer goleak.VerifyNone(t)

	// With tools declared.
	cfTools := newCountingFlusher()
	eTools := newEmitter(cfTools)
	eTools.tools = []canonical.ToolSpec{{Name: "get_weather"}}
	bodyTools := driveTextChunks(t, eTools, cfTools, canonical.StopEndTurn, "Hello ", "world")

	// Without tools (baseline current behavior).
	cfBare := newCountingFlusher()
	eBare := newEmitter(cfBare)
	bodyBare := driveTextChunks(t, eBare, cfBare, canonical.StopEndTurn, "Hello ", "world")

	if bodyTools != bodyBare {
		t.Errorf("prose stream diverged with tools declared (regression)\nwith tools:\n%s\nbaseline:\n%s", bodyTools, bodyBare)
	}
	if eTools.buffering {
		t.Error("buffering engaged on normal prose (must not)")
	}
	// Two text_delta frames streamed as they arrived.
	if strings.Count(bodyTools, `"type":"text_delta"`) != 2 {
		t.Errorf("text_delta count: got %d, want 2 (streamed immediately); body:\n%s",
			strings.Count(bodyTools, `"type":"text_delta"`), bodyTools)
	}
	if strings.Contains(bodyTools, `"type":"tool_use"`) {
		t.Errorf("prose produced a tool_use block; body:\n%s", bodyTools)
	}
}

// TestSSE_CoercesZeroArgTool_EmitsSingleEmptyInputDelta — ZERO-ARG. A
// wrapper with empty arguments must emit exactly one input_json_delta
// carrying "{}".
func TestSSE_CoercesZeroArgTool_EmitsSingleEmptyInputDelta(t *testing.T) {
	defer goleak.VerifyNone(t)
	cf := newCountingFlusher()
	e := newEmitter(cf)
	e.tools = []canonical.ToolSpec{{Name: "get_time"}}

	body := driveTextChunks(t, e, cf, canonical.StopEndTurn,
		`{"tool_call":{"name":"get_time","arguments":{}}}`)

	if !strings.Contains(body, `"name":"get_time"`) {
		t.Errorf("missing tool_use name get_time; body:\n%s", body)
	}
	payloads := inputJSONDeltaPayloads(t, body)
	if len(payloads) != 1 {
		t.Fatalf("input_json_delta count: got %d, want exactly 1; body:\n%s", len(payloads), body)
	}
	if payloads[0] != "{}" {
		t.Errorf("zero-arg partial_json: got %q, want {}", payloads[0])
	}
	if sr := messageDeltaStopReason(t, body); sr != "tool_use" {
		t.Errorf("stop_reason: got %q, want tool_use", sr)
	}
}
