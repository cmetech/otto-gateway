package anthropic

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"loop24-gateway/internal/canonical"
)

// ----------------------------------------------------------------------------
// chatResponseToMessage — non-streaming render
// ----------------------------------------------------------------------------

func TestChatResponseToMessage_TextOnly(t *testing.T) {
	resp := &canonical.ChatResponse{
		Model: "auto",
		Message: canonical.Message{
			Role:    canonical.RoleAssistant,
			Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "Hello!"}},
		},
		StopReason: canonical.StopEndTurn,
	}
	out := chatResponseToMessage(resp, "claude-sonnet-4")

	if out.Type != "message" {
		t.Errorf("Type: got %q, want %q", out.Type, "message")
	}
	if out.Role != "assistant" {
		t.Errorf("Role: got %q, want %q", out.Role, "assistant")
	}
	if out.Model != "claude-sonnet-4" {
		t.Errorf("Model: got %q, want %q (requestedModel echo)", out.Model, "claude-sonnet-4")
	}
	if !strings.HasPrefix(out.ID, "msg_01") {
		t.Errorf("ID: got %q, want prefix msg_01", out.ID)
	}
	if len(out.Content) != 1 || out.Content[0].Type != "text" || out.Content[0].Text != "Hello!" {
		t.Errorf("Content: got %+v, want one text block", out.Content)
	}
	if out.StopReason == nil || *out.StopReason != "end_turn" {
		t.Errorf("StopReason: got %v, want pointer to 'end_turn'", out.StopReason)
	}
	if out.StopSequence != nil {
		t.Errorf("StopSequence: got %v, want nil (renders as null)", *out.StopSequence)
	}
	if out.Usage.InputTokens != 0 || out.Usage.OutputTokens != 0 {
		t.Errorf("Usage: got %+v, want honest zeros (D-12)", out.Usage)
	}
	if out.Usage.CacheCreationInputTokens != nil || out.Usage.CacheReadInputTokens != nil {
		t.Errorf("Usage cache fields: got %v/%v, want nil (omitted)",
			out.Usage.CacheCreationInputTokens, out.Usage.CacheReadInputTokens)
	}
}

func TestChatResponseToMessage_TextThenThinking(t *testing.T) {
	resp := &canonical.ChatResponse{
		Model: "auto",
		Message: canonical.Message{
			Role: canonical.RoleAssistant,
			Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "answer"},
				{Kind: canonical.ContentKindThinking, Text: "reasoning"},
			},
		},
		StopReason: canonical.StopEndTurn,
	}
	out := chatResponseToMessage(resp, "auto")

	if len(out.Content) != 2 {
		t.Fatalf("Content: got %d blocks, want 2", len(out.Content))
	}
	if out.Content[0].Type != "text" || out.Content[0].Text != "answer" {
		t.Errorf("Content[0]: got %+v, want text=answer", out.Content[0])
	}
	if out.Content[1].Type != "thinking" || out.Content[1].Thinking != "reasoning" {
		t.Errorf("Content[1]: got %+v, want thinking=reasoning", out.Content[1])
	}
}

// TestChatResponseToMessage_ToolUseInputIsObject is the load-bearing
// ANTH-03 guarantee: tool_use.input MUST be a JSON OBJECT, not a JSON
// string. Round-trips through json.Marshal + Unmarshal as
// map[string]any and asserts the type.
func TestChatResponseToMessage_ToolUseInputIsObject(t *testing.T) {
	resp := &canonical.ChatResponse{
		Model: "auto",
		Message: canonical.Message{
			Role: canonical.RoleAssistant,
			Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindToolUse, ToolUse: &canonical.ToolUsePart{
					ID: "toolu_99", Name: "search",
					Input: map[string]any{"q": "ducks", "limit": 5},
				}},
			},
		},
		StopReason: canonical.StopEndTurn,
	}
	out := chatResponseToMessage(resp, "auto")

	// Marshal and re-parse as generic map to verify wire shape.
	wire, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(wire, &generic); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	content, ok := generic["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content: got %T %v, want []any with 1 entry", generic["content"], generic["content"])
	}
	block, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("content[0]: got %T, want map", content[0])
	}
	input, ok := block["input"]
	if !ok {
		t.Fatal("content[0].input: missing")
	}
	if _, isObject := input.(map[string]any); !isObject {
		t.Errorf("content[0].input: got %T (%v), want map[string]any (JSON OBJECT not string)", input, input)
	}
	// Also confirm fields round-trip.
	if v, ok := input.(map[string]any)["q"].(string); !ok || v != "ducks" {
		t.Errorf("input.q: got %v, want 'ducks'", input.(map[string]any)["q"])
	}
}

// TestChatResponseToMessage_ToolUseEmptyInput_RendersEmptyObject is the
// CR-01 regression test (VERIFICATION.md gap 1). The Anthropic Messages
// spec requires tool_use.input to ALWAYS be present as a JSON object,
// even when the tool was invoked with no arguments — the
// @anthropic-ai/sdk Zod parser in loop24-client throws if the field is
// missing. Both nil canonical.ToolUsePart.Input and an explicit empty
// map MUST render as `"input":{}` on the wire (NOT `"input":null`, and
// the field MUST NOT be dropped by omitempty).
func TestChatResponseToMessage_ToolUseEmptyInput_RendersEmptyObject(t *testing.T) {
	cases := []struct {
		name  string
		input map[string]any
	}{
		{name: "NilInput", input: nil},
		{name: "EmptyMap", input: map[string]any{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := &canonical.ChatResponse{
				Model: "auto",
				Message: canonical.Message{
					Role: canonical.RoleAssistant,
					Content: []canonical.ContentPart{
						{Kind: canonical.ContentKindToolUse, ToolUse: &canonical.ToolUsePart{
							ID: "toolu_1", Name: "ping", Input: c.input,
						}},
					},
				},
				StopReason: canonical.StopEndTurn,
			}
			out := chatResponseToMessage(resp, "auto")
			wire, err := json.Marshal(out)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}

			// Positive: the wire MUST carry "input":{} for the tool_use block.
			if !bytes.Contains(wire, []byte(`"input":{}`)) {
				t.Errorf("wire missing required `\"input\":{}` for nil/empty tool_use input; got: %s", wire)
			}
			// Negative: the wire MUST NOT carry "input":null.
			if bytes.Contains(wire, []byte(`"input":null`)) {
				t.Errorf("wire carries forbidden `\"input\":null` (spec demands empty object, not null); got: %s", wire)
			}

			// Structural cross-check: round-trip through generic decode to
			// confirm the input field is present and is a JSON object
			// (defends against any future encoder change that might satisfy
			// the byte-substring assertions accidentally via overlap with
			// another field).
			var generic map[string]any
			if err := json.Unmarshal(wire, &generic); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			content, ok := generic["content"].([]any)
			if !ok || len(content) != 1 {
				t.Fatalf("content: got %T %v, want []any with 1 entry", generic["content"], generic["content"])
			}
			block, ok := content[0].(map[string]any)
			if !ok {
				t.Fatalf("content[0]: got %T, want map", content[0])
			}
			inputField, present := block["input"]
			if !present {
				t.Fatalf("content[0].input: field absent (omitempty dropped it); wire=%s", wire)
			}
			obj, isObject := inputField.(map[string]any)
			if !isObject {
				t.Fatalf("content[0].input: got %T (%v), want map[string]any (JSON OBJECT, not null/string)", inputField, inputField)
			}
			if len(obj) != 0 {
				t.Errorf("content[0].input: got %v, want empty object {} for no-arg tool call", obj)
			}
		})
	}
}

func TestChatResponseToMessage_EmptyContent_DefensiveTextBlock(t *testing.T) {
	resp := &canonical.ChatResponse{
		Model: "auto",
		Message: canonical.Message{
			Role:    canonical.RoleAssistant,
			Content: nil,
		},
		StopReason: canonical.StopEndTurn,
	}
	out := chatResponseToMessage(resp, "auto")
	if len(out.Content) != 1 || out.Content[0].Type != "text" || out.Content[0].Text != "" {
		t.Errorf("Content: got %+v, want one empty text block (defensive)", out.Content)
	}
}

func TestChatResponseToMessage_ModelFallbackToRespModel(t *testing.T) {
	resp := &canonical.ChatResponse{
		Model: "claude-3-haiku",
		Message: canonical.Message{
			Role:    canonical.RoleAssistant,
			Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "ok"}},
		},
	}
	out := chatResponseToMessage(resp, "")
	if out.Model != "claude-3-haiku" {
		t.Errorf("Model: got %q, want %q (fallback to resp.Model when requestedModel empty)", out.Model, "claude-3-haiku")
	}
}

func TestChatResponseToMessage_NilResp_DoesNotPanic(t *testing.T) {
	out := chatResponseToMessage(nil, "auto")
	if out == nil {
		t.Fatal("out: nil")
	}
	if len(out.Content) != 1 || out.Content[0].Type != "text" {
		t.Errorf("Content: got %+v, want defensive empty text block", out.Content)
	}
	if out.Model != "auto" {
		t.Errorf("Model: got %q, want %q (requestedModel honored)", out.Model, "auto")
	}
	if out.StopReason != nil {
		t.Errorf("StopReason: got %v, want nil (no resp.StopReason to map)", out.StopReason)
	}
}

// ----------------------------------------------------------------------------
// mapStopReason (per CONTEXT.md A5 / RESEARCH.md §Code Examples)
// ----------------------------------------------------------------------------

func TestMapStopReason(t *testing.T) {
	cases := []struct {
		in   canonical.StopReason
		want *string
	}{
		{canonical.StopEndTurn, strPtr("end_turn")},
		{canonical.StopMaxTokens, strPtr("max_tokens")},
		{canonical.StopMaxTurnRequests, strPtr("max_tokens")},
		{canonical.StopRefusal, strPtr("refusal")},
		{canonical.StopCancelled, strPtr("end_turn")},
		{canonical.StopUnknown, nil},
	}
	for _, c := range cases {
		got := mapStopReason(c.in)
		if (got == nil) != (c.want == nil) {
			t.Errorf("mapStopReason(%v): got %v, want %v", c.in, got, c.want)
			continue
		}
		if got != nil && c.want != nil && *got != *c.want {
			t.Errorf("mapStopReason(%v): got %q, want %q", c.in, *got, *c.want)
		}
	}
}

func strPtr(s string) *string { return &s }

// ----------------------------------------------------------------------------
// genMessageID
// ----------------------------------------------------------------------------

func TestGenMessageID_PrefixAndLength(t *testing.T) {
	id := genMessageID()
	if !strings.HasPrefix(id, "msg_01") {
		t.Errorf("id: got %q, want prefix 'msg_01'", id)
	}
	// "msg_01" (6) + 24 hex chars = 30
	if len(id) != 30 {
		t.Errorf("id length: got %d, want 30", len(id))
	}
}

func TestGenMessageID_Unique(t *testing.T) {
	ids := make(map[string]struct{}, 100)
	for i := 0; i < 100; i++ {
		ids[genMessageID()] = struct{}{}
	}
	if len(ids) != 100 {
		t.Errorf("collisions: got %d unique IDs, want 100", len(ids))
	}
}

// TestChatResponseToMessage_GoldenWireShape pins the full wire JSON shape
// against the Anthropic SDK type definitions. Note: id is normalized
// because it's random; everything else is deterministic.
func TestChatResponseToMessage_GoldenWireShape(t *testing.T) {
	resp := &canonical.ChatResponse{
		Model: "auto",
		Message: canonical.Message{
			Role:    canonical.RoleAssistant,
			Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "Hello!"}},
		},
		StopReason: canonical.StopEndTurn,
	}
	out := chatResponseToMessage(resp, "claude-sonnet-4")
	out.ID = "msg_01_NORMALIZED" // pin for golden compare

	wire, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"id":"msg_01_NORMALIZED","type":"message","role":"assistant","model":"claude-sonnet-4","content":[{"type":"text","text":"Hello!"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}`
	if string(wire) != want {
		t.Errorf("wire mismatch:\n got:  %s\n want: %s", wire, want)
	}
}
