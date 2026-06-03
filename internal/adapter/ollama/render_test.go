package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
)

// TestChatResponseToWire_ToolCalls covers the Phase 6 D-04/D-15 wire-shape
// divergence canary: Ollama emits message.tool_calls[].function.arguments
// as a plain JSON OBJECT (map[string]any), NOT as a JSON-encoded string.
// This is the load-bearing distinction between the Ollama and OpenAI wire
// surfaces (OpenAI marshals arguments to a string per spec).
//
// Per the per-surface contract (Phase 6 D-03/D-05/D-07), the source of
// truth for Message.ToolCalls in Ollama is engine.CoerceToolCall — NOT
// kiro-native ChunkKindToolCall chunks. The render layer here just passes
// through whatever Message.ToolCalls contains; the "kiro-native narration"
// sub-case proves that when ToolCalls is nil but Content carries the
// [tool: <name>]\n narration (sourced from engine.Collect's iteration-3
// aggregator in 06-01), the wire output has narration text in content AND
// NO tool_calls key.
func TestChatResponseToWire_ToolCalls(t *testing.T) {
	start := time.Now().Add(-10 * time.Millisecond)

	t.Run("nil_no_tool_calls_field", func(t *testing.T) {
		resp := &canonical.ChatResponse{
			Model: "auto",
			Message: canonical.Message{
				Role:    canonical.RoleAssistant,
				Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "Hello"}},
			},
			StopReason: canonical.StopEndTurn,
		}
		got := chatResponseToWire(resp, start, "auto")
		if len(got.Message.ToolCalls) != 0 {
			t.Errorf("ToolCalls: got %d entries, want 0 (nil input)", len(got.Message.ToolCalls))
		}
		// Serialize and confirm omitempty drops the key entirely.
		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(raw), `"tool_calls"`) {
			t.Errorf("serialized output contains tool_calls key for nil-ToolCalls input: %s", raw)
		}
	})

	t.Run("single_coerce_synthesized_plain_object_args", func(t *testing.T) {
		resp := &canonical.ChatResponse{
			Model: "auto",
			Message: canonical.Message{
				Role: canonical.RoleAssistant,
				Content: []canonical.ContentPart{
					// Coerce zeroes the text and appends the synthesized ToolCall.
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
			StopReason: canonical.StopEndTurn,
		}
		got := chatResponseToWire(resp, start, "auto")
		if len(got.Message.ToolCalls) != 1 {
			t.Fatalf("ToolCalls len: got %d, want 1", len(got.Message.ToolCalls))
		}
		if got.Message.ToolCalls[0].Function.Name != "get_weather" {
			t.Errorf("Function.Name: got %q, want get_weather", got.Message.ToolCalls[0].Function.Name)
		}
		if got.Message.ToolCalls[0].Function.Arguments["location"] != "NYC" {
			t.Errorf("Function.Arguments[location]: got %v, want NYC", got.Message.ToolCalls[0].Function.Arguments["location"])
		}

		// Byte-level wire-shape canary: arguments must serialize as a JSON
		// OBJECT, not as a JSON-encoded STRING. The Ollama wire form is
		// `"arguments":{"location":"NYC"}` (no escaped quotes inside the
		// value). This is the divergence axis vs OpenAI (which uses
		// `"arguments":"{\"location\":\"NYC\"}"`).
		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !strings.Contains(string(raw), `"arguments":{"location":"NYC"}`) {
			t.Errorf("wire shape canary FAILED — expected plain-object arguments; got: %s", raw)
		}
		// Negative assertion: must NOT contain an escaped-string form.
		if strings.Contains(string(raw), `"arguments":"`) {
			t.Errorf("wire shape canary FAILED — found JSON-string arguments form (OpenAI shape); got: %s", raw)
		}

		// Round-trip back into a generic map and assert arguments is
		// map[string]any (D-12 byte-level lock).
		var roundTrip map[string]any
		if err := json.Unmarshal(raw, &roundTrip); err != nil {
			t.Fatalf("round-trip unmarshal: %v", err)
		}
		msg, ok := roundTrip["message"].(map[string]any)
		if !ok {
			t.Fatalf("round-trip message not a map: %T", roundTrip["message"])
		}
		toolCalls, ok := msg["tool_calls"].([]any)
		if !ok {
			t.Fatalf("round-trip tool_calls not a slice: %T", msg["tool_calls"])
		}
		if len(toolCalls) != 1 {
			t.Fatalf("round-trip tool_calls len: got %d, want 1", len(toolCalls))
		}
		entry := toolCalls[0].(map[string]any)
		fn := entry["function"].(map[string]any)
		args := fn["arguments"]
		if _, isMap := args.(map[string]any); !isMap {
			t.Errorf("round-trip arguments type: got %T, want map[string]any (Ollama plain-object canary)", args)
		}
	})

	t.Run("multi_tool_order_preserved", func(t *testing.T) {
		resp := &canonical.ChatResponse{
			Model: "auto",
			Message: canonical.Message{
				Role:    canonical.RoleAssistant,
				Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: ""}},
				ToolCalls: []canonical.ToolCall{
					{ID: "call_1", Name: "first", Arguments: map[string]any{"x": float64(1)}},
					{ID: "call_2", Name: "second", Arguments: map[string]any{"y": float64(2)}},
				},
			},
		}
		got := chatResponseToWire(resp, start, "auto")
		if len(got.Message.ToolCalls) != 2 {
			t.Fatalf("ToolCalls len: got %d, want 2", len(got.Message.ToolCalls))
		}
		if got.Message.ToolCalls[0].Function.Name != "first" {
			t.Errorf("ToolCalls[0]: got %q, want first", got.Message.ToolCalls[0].Function.Name)
		}
		if got.Message.ToolCalls[1].Function.Name != "second" {
			t.Errorf("ToolCalls[1]: got %q, want second", got.Message.ToolCalls[1].Function.Name)
		}
	})

	t.Run("KiroNativeNarration_NoToolCalls", func(t *testing.T) {
		// iteration-3: kiro-native ChunkKindToolCall now lands as narration
		// in Message.Content via engine.Collect's aggregator (06-01 Task 2).
		// Message.ToolCalls stays nil. The Ollama render layer must pass
		// the narration through to message.content without re-synthesizing
		// any tool_calls entries.
		resp := &canonical.ChatResponse{
			Model: "auto",
			Message: canonical.Message{
				Role: canonical.RoleAssistant,
				Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: "[tool: get_weather]\n"},
				},
				// ToolCalls intentionally nil.
			},
			StopReason: canonical.StopEndTurn,
		}
		got := chatResponseToWire(resp, start, "auto")
		if got.Message.Content != "[tool: get_weather]\n" {
			t.Errorf("Message.Content: got %q, want narration pass-through", got.Message.Content)
		}
		if len(got.Message.ToolCalls) != 0 {
			t.Errorf("ToolCalls: got %d entries, want 0 (kiro-native narration must not re-synthesize)", len(got.Message.ToolCalls))
		}
		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(raw), `"tool_calls"`) {
			t.Errorf("serialized output contains tool_calls key for kiro-native narration: %s", raw)
		}
		if !strings.Contains(string(raw), `[tool: get_weather]`) {
			t.Errorf("serialized output missing narration content: %s", raw)
		}
	})
}

// TestWireToChatRequest_Tools_Phase2Regression locks the Phase 2 forward-seam
// decoder behavior (wire.go lines 321-330) — a request with tools[] decodes
// into req.Tools with the right shape. This protects against accidental
// regression when later Wave 2 work (06-03 OpenAI, 06-04 Anthropic) might
// be tempted to refactor cross-adapter "for consistency."
func TestWireToChatRequest_Tools_Phase2Regression(t *testing.T) {
	body := ollamaChatRequest{
		Model: "auto",
		Messages: []ollamaMessage{
			{Role: "user", Content: "what's the weather?"},
		},
		Tools: []ollamaToolSpec{
			{
				Type: "function",
				Function: &ollamaToolSpecFunction{
					Name:        "get_weather",
					Description: "Get the current weather",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"location": map[string]any{"type": "string"},
						},
					},
				},
			},
		},
	}
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/chat", nil)
	got, err := wireToChatRequest(&body, r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(got.Tools) != 1 {
		t.Fatalf("Tools len: got %d, want 1", len(got.Tools))
	}
	if got.Tools[0].Name != "get_weather" {
		t.Errorf("Tools[0].Name: got %q, want get_weather", got.Tools[0].Name)
	}
	if got.Tools[0].Description != "Get the current weather" {
		t.Errorf("Tools[0].Description: got %q, want %q", got.Tools[0].Description, "Get the current weather")
	}
	if got.Tools[0].Parameters == nil {
		t.Fatal("Tools[0].Parameters: nil, want non-nil")
	}
	if got.Tools[0].Parameters["type"] != "object" {
		t.Errorf("Tools[0].Parameters[type]: got %v, want object", got.Tools[0].Parameters["type"])
	}
	props, ok := got.Tools[0].Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("Tools[0].Parameters[properties] not a map: %T", got.Tools[0].Parameters["properties"])
	}
	if _, hasLoc := props["location"]; !hasLoc {
		t.Errorf("Tools[0].Parameters[properties].location missing; got %v", props)
	}

	// Defensive: Function == nil entries must be silently skipped.
	body.Tools = append(body.Tools, ollamaToolSpec{Type: "function", Function: nil})
	got2, err2 := wireToChatRequest(&body, r)
	if err2 != nil {
		t.Fatalf("unexpected error on nil-Function test: %v", err2)
	}
	if len(got2.Tools) != 1 {
		t.Errorf("Tools len after appending nil-Function entry: got %d, want 1 (nil-Function must be dropped)", len(got2.Tools))
	}
}
