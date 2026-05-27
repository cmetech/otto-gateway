package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"otto-gateway/internal/canonical"
)

// TestChatResponseToCompletion_ToolCalls locks the OpenAI non-streaming
// tool_calls render path per D-07 + iteration-3 KiroNativeNarration:
//   - resp.Message.ToolCalls nil → no tool_calls key on the wire (omitempty)
//   - coerce-synthesized tool_calls → choices[0].message.tool_calls with
//     Arguments serialized as a JSON-STRING (NOT object — wire-shape
//     divergence canary opposite of Ollama's Slice 2 lock)
//   - non-empty ToolCalls → finish_reason override "tool_calls"
//   - multi-tool order preserved
//   - KiroNativeNarration: when Content carries "[tool: <name>]\n" but
//     ToolCalls is nil, render outputs the narration as message.content
//     and emits NO tool_calls field; finish_reason is NOT "tool_calls"
//     (iteration-3 lock — depends on 06-01 Task 2 narration aggregator).
func TestChatResponseToCompletion_ToolCalls(t *testing.T) {
	t.Run("NilToolCalls_NoToolCallsKey", func(t *testing.T) {
		resp := &canonical.ChatResponse{
			StopReason: canonical.StopEndTurn,
			Message: canonical.Message{
				Role: canonical.RoleAssistant,
				Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: "hello"},
				},
			},
		}
		out := chatResponseToCompletion(resp, "auto")
		raw, err := json.Marshal(out)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(raw), `"tool_calls"`) {
			t.Errorf("nil ToolCalls must omit tool_calls field; got JSON: %s", raw)
		}
	})

	t.Run("CoerceSynthesizedToolCalls_ArgumentsIsJSONString", func(t *testing.T) {
		resp := &canonical.ChatResponse{
			StopReason: canonical.StopEndTurn,
			Message: canonical.Message{
				Role: canonical.RoleAssistant,
				// Content text was cleared by CoerceToolCall in Step 8.
				Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: ""},
				},
				ToolCalls: []canonical.ToolCall{
					{
						ID:   "call_123",
						Name: "get_weather",
						Arguments: map[string]any{
							"location": "NYC",
						},
					},
				},
			},
		}
		out := chatResponseToCompletion(resp, "auto")

		if len(out.Choices[0].Message.ToolCalls) != 1 {
			t.Fatalf("ToolCalls: got %d, want 1", len(out.Choices[0].Message.ToolCalls))
		}
		tc := out.Choices[0].Message.ToolCalls[0]
		if tc.ID != "call_123" {
			t.Errorf("ID: got %q, want call_123", tc.ID)
		}
		if tc.Type != "function" {
			t.Errorf("Type: got %q, want function", tc.Type)
		}
		if tc.Function.Name != "get_weather" {
			t.Errorf("Function.Name: got %q, want get_weather", tc.Function.Name)
		}
		// Arguments is a JSON-encoded STRING (wire-shape divergence canary
		// opposite of Ollama's object literal).
		if tc.Function.Arguments != `{"location":"NYC"}` {
			t.Errorf("Function.Arguments: got %q, want %q", tc.Function.Arguments, `{"location":"NYC"}`)
		}

		// finish_reason post-fixup override.
		if out.Choices[0].FinishReason != "tool_calls" {
			t.Errorf("finish_reason: got %q, want tool_calls", out.Choices[0].FinishReason)
		}

		// Byte-level wire-shape assertion: arguments value in JSON output
		// is a STRING (escape-quoted), NOT an object literal.
		raw, err := json.Marshal(out)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		// Look for arguments-as-string: the value starts with a literal
		// backslash-quote (escaped within the wire JSON output).
		if !strings.Contains(string(raw), `"arguments":"{\"location\":\"NYC\"}"`) {
			t.Errorf("expected JSON-string arguments shape (escaped quotes); got: %s", raw)
		}
		if strings.Contains(string(raw), `"arguments":{`) {
			t.Errorf("arguments must be JSON-STRING, not object literal; got: %s", raw)
		}
	})

	t.Run("MultiToolOrderPreserved", func(t *testing.T) {
		resp := &canonical.ChatResponse{
			StopReason: canonical.StopEndTurn,
			Message: canonical.Message{
				Role: canonical.RoleAssistant,
				ToolCalls: []canonical.ToolCall{
					{ID: "call_a", Name: "alpha", Arguments: map[string]any{"a": 1.0}},
					{ID: "call_b", Name: "beta", Arguments: map[string]any{"b": 2.0}},
				},
			},
		}
		out := chatResponseToCompletion(resp, "auto")
		if len(out.Choices[0].Message.ToolCalls) != 2 {
			t.Fatalf("ToolCalls: got %d, want 2", len(out.Choices[0].Message.ToolCalls))
		}
		if out.Choices[0].Message.ToolCalls[0].Function.Name != "alpha" {
			t.Errorf("tc[0].Name: got %q, want alpha", out.Choices[0].Message.ToolCalls[0].Function.Name)
		}
		if out.Choices[0].Message.ToolCalls[1].Function.Name != "beta" {
			t.Errorf("tc[1].Name: got %q, want beta", out.Choices[0].Message.ToolCalls[1].Function.Name)
		}
	})

	t.Run("KiroNativeNarration_NoToolCalls", func(t *testing.T) {
		// Iteration-3 lock: non-streaming kiro-native scenario where
		// 06-01 Task 2's narration aggregator populated Content with
		// "[tool: <name>]\n" text. CoerceToolCall did NOT fire (the
		// narration text fails JSON parse), so Message.ToolCalls stays
		// nil. The wire output must carry the narration text in
		// message.content with NO tool_calls field; finish_reason MUST
		// NOT be "tool_calls".
		resp := &canonical.ChatResponse{
			StopReason: canonical.StopEndTurn,
			Message: canonical.Message{
				Role: canonical.RoleAssistant,
				Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: "[tool: get_weather]\n"},
				},
				ToolCalls: nil,
			},
		}
		out := chatResponseToCompletion(resp, "auto")

		if out.Choices[0].Message.Content != "[tool: get_weather]\n" {
			t.Errorf("content: got %q, want %q", out.Choices[0].Message.Content, "[tool: get_weather]\n")
		}
		if len(out.Choices[0].Message.ToolCalls) != 0 {
			t.Errorf("ToolCalls: got %d, want 0", len(out.Choices[0].Message.ToolCalls))
		}
		if out.Choices[0].FinishReason == "tool_calls" {
			t.Errorf("finish_reason: got %q, must NOT be tool_calls for kiro-native narration path", out.Choices[0].FinishReason)
		}

		raw, err := json.Marshal(out)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(raw), `"tool_calls"`) {
			t.Errorf("kiro-native narration path must omit tool_calls field; got: %s", raw)
		}
	})
}
