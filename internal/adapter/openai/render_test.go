package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"otto-gateway/internal/canonical"
)

// TestChatResponseToCompletion_ToolCalls locks the OpenAI non-streaming
// tool_calls render path per D-07:
//   - resp.Message.ToolCalls nil → no tool_calls key on the wire (omitempty)
//   - coerce-synthesized OR kiro-native tool_calls → choices[0].message.
//     tool_calls with Arguments serialized as a JSON-STRING (NOT object —
//     wire-shape divergence canary opposite of Ollama's plain-object lock)
//   - non-empty ToolCalls → finish_reason override "tool_calls"
//   - multi-tool order preserved
//   - KiroNativeToolCall_StructuredArgsString: kiro-native tool calls now
//     reach the renderer as structured Message.ToolCalls (Defect 1a) and
//     surface as tool_calls with NO `[tool:` marker in content.
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

	t.Run("KiroNativeToolCall_StructuredArgsString", func(t *testing.T) {
		// Defect 1a (2026-07-16): non-streaming kiro-native scenario. The
		// engine.Collect aggregator now surfaces a kiro-native tool call as
		// a STRUCTURED Message.ToolCalls entry (empty assistant text). The
		// OpenAI wire output must carry tool_calls[].function.arguments as a
		// JSON-encoded STRING, finish_reason:"tool_calls", and NO `[tool:`
		// marker in message.content.
		resp := &canonical.ChatResponse{
			StopReason: canonical.StopEndTurn,
			Message: canonical.Message{
				Role:    canonical.RoleAssistant,
				Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: ""}},
				ToolCalls: []canonical.ToolCall{
					{ID: "tc_1", Name: "get_weather", Arguments: map[string]any{"location": "NYC"}},
				},
			},
		}
		out := chatResponseToCompletion(resp, "auto")

		if strings.Contains(out.Choices[0].Message.Content, "[tool:") {
			t.Errorf("content must not contain a [tool: marker; got %q", out.Choices[0].Message.Content)
		}
		if len(out.Choices[0].Message.ToolCalls) != 1 {
			t.Fatalf("ToolCalls: got %d, want 1", len(out.Choices[0].Message.ToolCalls))
		}
		if got := out.Choices[0].Message.ToolCalls[0].Function.Name; got != "get_weather" {
			t.Errorf("tc.Name: got %q, want get_weather", got)
		}
		if got := out.Choices[0].Message.ToolCalls[0].Function.Arguments; got != `{"location":"NYC"}` {
			t.Errorf("tc.Arguments: got %q, want JSON-string {\"location\":\"NYC\"}", got)
		}
		if out.Choices[0].FinishReason != "tool_calls" {
			t.Errorf("finish_reason: got %q, want tool_calls", out.Choices[0].FinishReason)
		}

		raw, err := json.Marshal(out)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(raw), "[tool:") {
			t.Errorf("serialized output must not contain a [tool: marker; got: %s", raw)
		}
	})
}

// TestChatResponseToTextCompletion locks the /v1/completions (text_completion)
// wire shape (WR-07 Phase 6 review). The function exists and is wired into
// handleCompletions but had no direct test coverage before this lock. The
// shape must match the legacy OpenAI text_completion endpoint contract that
// the D-03 forward-compat shim accepts and ignores.
func TestChatResponseToTextCompletion(t *testing.T) {
	t.Run("ShapeAndFields", func(t *testing.T) {
		resp := &canonical.ChatResponse{
			StopReason: canonical.StopEndTurn,
			Model:      "auto",
			Message: canonical.Message{
				Role: canonical.RoleAssistant,
				Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: "Hello, "},
					{Kind: canonical.ContentKindText, Text: "world!"},
				},
			},
		}
		out := chatResponseToTextCompletion(resp, "auto")

		// object literal canary
		if out.Object != "text_completion" {
			t.Errorf("object: got %q, want %q", out.Object, "text_completion")
		}
		// ID must use the "cmpl-" prefix (NOT the chat-completion "chatcmpl-" prefix).
		if !strings.HasPrefix(out.ID, "cmpl-") {
			t.Errorf("id: got %q, want cmpl- prefix", out.ID)
		}
		if strings.HasPrefix(out.ID, "chatcmpl-") {
			t.Errorf("id: must NOT use chatcmpl- prefix on text_completion path; got %q", out.ID)
		}
		// Exactly one choice
		if len(out.Choices) != 1 {
			t.Fatalf("choices len: got %d, want 1", len(out.Choices))
		}
		// Joined text appears in the text field (concatenation of all
		// ContentKindText parts).
		if out.Choices[0].Text != "Hello, world!" {
			t.Errorf("choices[0].text: got %q, want %q", out.Choices[0].Text, "Hello, world!")
		}
		// FinishReason is the mapped stop string ("stop" for StopEndTurn).
		if out.Choices[0].FinishReason != "stop" {
			t.Errorf("choices[0].finish_reason: got %q, want %q", out.Choices[0].FinishReason, "stop")
		}
		// Logprobs must render as JSON null. Logprobs field is *struct{};
		// nil pointer marshals to null with no `omitempty` tag.
		raw, err := json.Marshal(out)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !strings.Contains(string(raw), `"logprobs":null`) {
			t.Errorf("logprobs must render as JSON null; got: %s", raw)
		}
		// Usage envelope present with honest zeros (D-12).
		if !strings.Contains(string(raw), `"usage"`) {
			t.Errorf("usage field missing from text_completion shape; got: %s", raw)
		}
	})

	t.Run("NilResp_DefensiveDefaults", func(t *testing.T) {
		// Nil resp path: empty text, finish_reason defaults via mapFinishReason
		// on StopUnknown, model echoes the requested value.
		out := chatResponseToTextCompletion(nil, "auto")
		if out.Object != "text_completion" {
			t.Errorf("object: got %q, want %q", out.Object, "text_completion")
		}
		if len(out.Choices) != 1 {
			t.Fatalf("choices len: got %d, want 1", len(out.Choices))
		}
		if out.Choices[0].Text != "" {
			t.Errorf("choices[0].text: got %q, want empty", out.Choices[0].Text)
		}
		if out.Model != "auto" {
			t.Errorf("model: got %q, want %q (echoed from requestedModel)", out.Model, "auto")
		}
	})
}
