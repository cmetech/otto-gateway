package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"otto-gateway/internal/canonical"
)

// TestWire covers wireToChatRequest: content polymorphism, role mapping,
// system/developer hoist, accept-and-ignore extras.
func TestWire(t *testing.T) {
	t.Run("content_string", func(t *testing.T) {
		wire := &chatCompletionRequest{
			Model: "auto",
			Messages: []chatMessage{
				{Role: "user", Content: jsonRaw(`"hello world"`)},
			},
		}
		req := wireToChatRequest(wire, fakeHTTPRequest(t))
		if len(req.Messages) != 1 {
			t.Fatalf("messages: got %d, want 1", len(req.Messages))
		}
		if len(req.Messages[0].Content) != 1 {
			t.Fatalf("content parts: got %d, want 1", len(req.Messages[0].Content))
		}
		if req.Messages[0].Content[0].Kind != canonical.ContentKindText {
			t.Errorf("kind: got %v, want ContentKindText", req.Messages[0].Content[0].Kind)
		}
		if req.Messages[0].Content[0].Text != "hello world" {
			t.Errorf("text: got %q, want %q", req.Messages[0].Content[0].Text, "hello world")
		}
	})

	t.Run("content_array", func(t *testing.T) {
		wire := &chatCompletionRequest{
			Model: "auto",
			Messages: []chatMessage{
				{Role: "user", Content: jsonRaw(`[{"type":"text","text":"hello"},{"type":"text","text":" world"}]`)},
			},
		}
		req := wireToChatRequest(wire, fakeHTTPRequest(t))
		if len(req.Messages) != 1 {
			t.Fatalf("messages: got %d, want 1", len(req.Messages))
		}
		// Joined into a single ContentKindText part
		if len(req.Messages[0].Content) != 1 {
			t.Fatalf("content parts: got %d, want 1 (joined)", len(req.Messages[0].Content))
		}
		if req.Messages[0].Content[0].Text != "hello world" {
			t.Errorf("joined text: got %q, want %q", req.Messages[0].Content[0].Text, "hello world")
		}
	})

	t.Run("system_role_hoisted", func(t *testing.T) {
		wire := &chatCompletionRequest{
			Model: "auto",
			Messages: []chatMessage{
				{Role: "system", Content: jsonRaw(`"you are helpful"`)},
				{Role: "user", Content: jsonRaw(`"hello"`)},
			},
		}
		req := wireToChatRequest(wire, fakeHTTPRequest(t))
		if req.System != "you are helpful" {
			t.Errorf("system: got %q, want %q", req.System, "you are helpful")
		}
		// system message must not appear in Messages
		for _, m := range req.Messages {
			if m.Role == canonical.RoleSystem {
				t.Error("system role must not appear in canonical Messages[] (should be hoisted to System field)")
			}
		}
		if len(req.Messages) != 1 {
			t.Errorf("messages: got %d, want 1 (only user message)", len(req.Messages))
		}
	})

	t.Run("developer_role_hoisted", func(t *testing.T) {
		wire := &chatCompletionRequest{
			Model: "auto",
			Messages: []chatMessage{
				{Role: "developer", Content: jsonRaw(`"system instructions"`)},
				{Role: "user", Content: jsonRaw(`"query"`)},
			},
		}
		req := wireToChatRequest(wire, fakeHTTPRequest(t))
		if req.System != "system instructions" {
			t.Errorf("developer hoist: got %q, want %q", req.System, "system instructions")
		}
		for _, m := range req.Messages {
			if m.Role == canonical.RoleSystem {
				t.Error("developer role must not appear in canonical Messages[]")
			}
		}
	})

	t.Run("role_mapping", func(t *testing.T) {
		cases := []struct {
			wireRole string
			wantRole canonical.MessageRole
		}{
			{"user", canonical.RoleUser},
			{"assistant", canonical.RoleAssistant},
			{"tool", canonical.RoleTool},
			{"unknown_future_role", canonical.RoleUser}, // default → RoleUser
		}
		for _, tc := range cases {
			tc := tc
			t.Run(tc.wireRole, func(t *testing.T) {
				wire := &chatCompletionRequest{
					Model: "auto",
					Messages: []chatMessage{
						{Role: tc.wireRole, Content: jsonRaw(`"text"`)},
					},
				}
				req := wireToChatRequest(wire, fakeHTTPRequest(t))
				if len(req.Messages) == 0 {
					t.Fatalf("expected 1 message for role %q", tc.wireRole)
				}
				if req.Messages[0].Role != tc.wantRole {
					t.Errorf("role %q: got canonical %v, want %v",
						tc.wireRole, req.Messages[0].Role, tc.wantRole)
				}
			})
		}
	})

	t.Run("model_set_unconditionally", func(t *testing.T) {
		for _, model := range []string{"auto", "", "gpt-4"} {
			wire := &chatCompletionRequest{
				Model: model,
				Messages: []chatMessage{
					{Role: "user", Content: jsonRaw(`"hi"`)},
				},
			}
			req := wireToChatRequest(wire, fakeHTTPRequest(t))
			if req.Model != model {
				t.Errorf("model %q: got %q, want %q", model, req.Model, model)
			}
		}
	})

	t.Run("accept_and_ignore_extras", func(t *testing.T) {
		// stream_options, max_completion_tokens, logprobs should not 400
		body := `{
			"model":"auto",
			"messages":[{"role":"user","content":"hi"}],
			"stream_options":{"include_usage":true},
			"max_completion_tokens":1024,
			"logprobs":true,
			"unknown_future_field":"ignored"
		}`
		var wire chatCompletionRequest
		if err := json.Unmarshal([]byte(body), &wire); err != nil {
			t.Fatalf("unmarshal with extras: %v", err)
		}
		req := wireToChatRequest(&wire, fakeHTTPRequest(t))
		if len(req.Messages) == 0 {
			t.Error("messages should decode despite extras")
		}
	})

	t.Run("working_dir_header", func(t *testing.T) {
		wire := &chatCompletionRequest{
			Model: "auto",
			Messages: []chatMessage{
				{Role: "user", Content: jsonRaw(`"hi"`)},
			},
		}
		r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(""))
		r.Header.Set("X-Working-Dir", "/home/user/project")
		req := wireToChatRequest(wire, r)
		if req.WorkingDirOverride != "/home/user/project" {
			t.Errorf("working dir: got %q, want /home/user/project", req.WorkingDirOverride)
		}
	})
}

// TestWireToChatRequest_Tools verifies the per-entry-tolerant decoder for
// the OpenAI `tools[]` field per D-13 + iteration-3 MEDIUM #4. Tools is
// decoded as []json.RawMessage on the wire so type-invalid sibling entries
// can be skipped per-entry without failing the whole request decode.
func TestWireToChatRequest_Tools(t *testing.T) {
	t.Run("single_tool", func(t *testing.T) {
		body := `{
			"model":"auto",
			"messages":[{"role":"user","content":"hi"}],
			"tools":[{"type":"function","function":{"name":"get_weather","description":"Get current weather","parameters":{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}}}]
		}`
		var wire chatCompletionRequest
		if err := json.Unmarshal([]byte(body), &wire); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		req := wireToChatRequest(&wire, fakeHTTPRequest(t))
		if len(req.Tools) != 1 {
			t.Fatalf("tools: got %d, want 1", len(req.Tools))
		}
		if req.Tools[0].Name != "get_weather" {
			t.Errorf("name: got %q, want get_weather", req.Tools[0].Name)
		}
		if req.Tools[0].Description != "Get current weather" {
			t.Errorf("description: got %q, want Get current weather", req.Tools[0].Description)
		}
		if req.Tools[0].Parameters == nil {
			t.Fatal("parameters: nil; want populated map")
		}
		if got := req.Tools[0].Parameters["type"]; got != "object" {
			t.Errorf("parameters.type: got %v, want object", got)
		}
	})

	t.Run("multi_tool_order_preserved", func(t *testing.T) {
		body := `{
			"model":"auto",
			"messages":[{"role":"user","content":"hi"}],
			"tools":[
				{"type":"function","function":{"name":"alpha","parameters":{}}},
				{"type":"function","function":{"name":"beta","parameters":{}}},
				{"type":"function","function":{"name":"gamma","parameters":{}}}
			]
		}`
		var wire chatCompletionRequest
		if err := json.Unmarshal([]byte(body), &wire); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		req := wireToChatRequest(&wire, fakeHTTPRequest(t))
		if len(req.Tools) != 3 {
			t.Fatalf("tools: got %d, want 3", len(req.Tools))
		}
		names := []string{req.Tools[0].Name, req.Tools[1].Name, req.Tools[2].Name}
		want := []string{"alpha", "beta", "gamma"}
		for i := range want {
			if names[i] != want[i] {
				t.Errorf("tools[%d].Name: got %q, want %q", i, names[i], want[i])
			}
		}
	})

	t.Run("no_tools_field", func(t *testing.T) {
		body := `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`
		var wire chatCompletionRequest
		if err := json.Unmarshal([]byte(body), &wire); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		req := wireToChatRequest(&wire, fakeHTTPRequest(t))
		if req.Tools != nil {
			t.Errorf("tools: got %v, want nil", req.Tools)
		}
	})
}

// TestWireToChatRequest_Tools_MixedValidInvalid locks the iteration-3
// MEDIUM #4 fix: type-invalid sibling entries (function:42) are skipped
// via per-entry unmarshal failure; decoded-but-semantically-invalid
// entries (empty name) are skipped via post-unmarshal validation; valid
// siblings are preserved in declaration order. The outer request decode
// MUST NOT fail on the type-invalid sibling.
func TestWireToChatRequest_Tools_MixedValidInvalid(t *testing.T) {
	body := `{
		"model":"auto",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[
			{"type":"function","function":{"name":"valid_a","parameters":{}}},
			{"type":"function","function":42},
			{"type":"function","function":{"name":""}},
			{"type":"function","function":{"name":"valid_b","parameters":{}}}
		]
	}`
	var wire chatCompletionRequest
	if err := json.Unmarshal([]byte(body), &wire); err != nil {
		t.Fatalf("outer request decode must NOT fail on type-invalid sibling; got: %v", err)
	}
	req := wireToChatRequest(&wire, fakeHTTPRequest(t))
	if len(req.Tools) != 2 {
		t.Fatalf("tools: got %d, want 2 (valid_a + valid_b)", len(req.Tools))
	}
	if req.Tools[0].Name != "valid_a" {
		t.Errorf("tools[0].Name: got %q, want valid_a", req.Tools[0].Name)
	}
	if req.Tools[1].Name != "valid_b" {
		t.Errorf("tools[1].Name: got %q, want valid_b", req.Tools[1].Name)
	}
}

// TestWireToChatRequest_ToolChoice verifies polymorphic tool_choice decode
// per D-13: "auto" / "required" / "none" string forms + {type:"function",
// function:{name:...}} object form + unknown-shape accept-and-ignore.
func TestWireToChatRequest_ToolChoice(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantType string
		wantName string
		wantNil  bool
	}{
		{
			name:     "auto",
			body:     `{"model":"auto","messages":[{"role":"user","content":"hi"}],"tool_choice":"auto"}`,
			wantType: "auto",
		},
		{
			name:     "required",
			body:     `{"model":"auto","messages":[{"role":"user","content":"hi"}],"tool_choice":"required"}`,
			wantType: "required",
		},
		{
			name:     "none",
			body:     `{"model":"auto","messages":[{"role":"user","content":"hi"}],"tool_choice":"none"}`,
			wantType: "none",
		},
		{
			name:     "function",
			body:     `{"model":"auto","messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"function","function":{"name":"get_weather"}}}`,
			wantType: "function",
			wantName: "get_weather",
		},
		{
			name:    "unknown_numeric_accepts_and_ignores",
			body:    `{"model":"auto","messages":[{"role":"user","content":"hi"}],"tool_choice":42}`,
			wantNil: true,
		},
		{
			name:    "absent",
			body:    `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`,
			wantNil: true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var wire chatCompletionRequest
			if err := json.Unmarshal([]byte(tc.body), &wire); err != nil {
				t.Fatalf("outer decode: %v", err)
			}
			req := wireToChatRequest(&wire, fakeHTTPRequest(t))
			if tc.wantNil {
				if req.ToolChoice != nil {
					t.Errorf("tool_choice: got %+v, want nil", req.ToolChoice)
				}
				return
			}
			if req.ToolChoice == nil {
				t.Fatalf("tool_choice: nil; want type=%q name=%q", tc.wantType, tc.wantName)
			}
			if req.ToolChoice.Type != tc.wantType {
				t.Errorf("tool_choice.Type: got %q, want %q", req.ToolChoice.Type, tc.wantType)
			}
			if req.ToolChoice.Name != tc.wantName {
				t.Errorf("tool_choice.Name: got %q, want %q", req.ToolChoice.Name, tc.wantName)
			}
		})
	}
}

// TestErrors covers writeError: envelope shape + status codes + content-type ordering.
func TestErrors(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		errType    string
		message    string
		wantStatus int
		wantType   string
	}{
		{"bad_request", http.StatusBadRequest, errInvalidRequest, "bad input", 400, "invalid_request_error"},
		{"not_found", http.StatusNotFound, errNotFound, "not found", 404, "not_found_error"},
		{"too_large", http.StatusRequestEntityTooLarge, errRequestTooLarge, "too large", 413, "invalid_request_error"},
		{"internal_error", http.StatusInternalServerError, errAPI, "internal error", 500, "api_error"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeError(rec, tc.status, tc.errType, tc.message)

			if rec.Code != tc.wantStatus {
				t.Errorf("status: got %d, want %d", rec.Code, tc.wantStatus)
			}

			ct := rec.Header().Get("Content-Type")
			if !strings.HasPrefix(ct, "application/json") {
				t.Errorf("Content-Type: got %q, want application/json prefix", ct)
			}

			// Decode the envelope.
			var env errorEnvelope
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("unmarshal error envelope: %v", err)
			}

			// OpenAI shape: {"error":{...}} — NOT {"type":"error","error":{...}}
			if env.Error.Type != tc.wantType {
				t.Errorf("error.type: got %q, want %q", env.Error.Type, tc.wantType)
			}
			if env.Error.Message != tc.message {
				t.Errorf("error.message: got %q, want %q", env.Error.Message, tc.message)
			}
			if env.Error.Param != nil {
				t.Errorf("error.param: got %v, want null", env.Error.Param)
			}
			if env.Error.Code != nil {
				t.Errorf("error.code: got %v, want null", env.Error.Code)
			}
		})
	}

	t.Run("not_anthropic_outer_type", func(t *testing.T) {
		// Verify the raw JSON does NOT contain an outer "type":"error" field
		rec := httptest.NewRecorder()
		writeError(rec, 400, errInvalidRequest, "test")
		raw := rec.Body.String()
		if strings.Contains(raw, `"type":"error"`) {
			t.Errorf("OpenAI envelope must NOT have outer type:error field; got: %s", raw)
		}
	})
}

// TestChatCompletions_NonStream covers the render half:
// chatResponseToCompletion produces correct shape with joined text,
// non-null finish_reason, zero usage, and chatcmpl- id prefix.
func TestChatCompletions_NonStream(t *testing.T) {
	t.Run("basic_text_response", func(t *testing.T) {
		resp := &canonical.ChatResponse{
			StopReason: canonical.StopEndTurn,
			Message: canonical.Message{
				Role: canonical.RoleAssistant,
				Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: "Hello, "},
					{Kind: canonical.ContentKindText, Text: "world!"},
				},
			},
		}
		out := chatResponseToCompletion(resp, "auto")

		if out.Object != "chat.completion" {
			t.Errorf("object: got %q, want chat.completion", out.Object)
		}
		if !strings.HasPrefix(out.ID, "chatcmpl-") {
			t.Errorf("id: got %q, want chatcmpl- prefix", out.ID)
		}
		if len(out.Choices) != 1 {
			t.Fatalf("choices: got %d, want 1", len(out.Choices))
		}
		if out.Choices[0].Message.Role != "assistant" {
			t.Errorf("message.role: got %q, want assistant", out.Choices[0].Message.Role)
		}
		if out.Choices[0].Message.Content != "Hello, world!" {
			t.Errorf("message.content: got %q, want Hello, world!", out.Choices[0].Message.Content)
		}
		if out.Choices[0].FinishReason != "stop" {
			t.Errorf("finish_reason: got %q, want stop", out.Choices[0].FinishReason)
		}
		if out.Usage.PromptTokens != 0 || out.Usage.CompletionTokens != 0 || out.Usage.TotalTokens != 0 {
			t.Errorf("usage: got %+v, want all zeros", out.Usage)
		}
	})

	t.Run("max_tokens_finish_reason", func(t *testing.T) {
		resp := &canonical.ChatResponse{
			StopReason: canonical.StopMaxTokens,
			Message: canonical.Message{
				Role:    canonical.RoleAssistant,
				Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "partial"}},
			},
		}
		out := chatResponseToCompletion(resp, "gpt-4")
		if out.Choices[0].FinishReason != "length" {
			t.Errorf("finish_reason: got %q, want length", out.Choices[0].FinishReason)
		}
	})

	t.Run("empty_content_defensive", func(t *testing.T) {
		resp := &canonical.ChatResponse{
			StopReason: canonical.StopEndTurn,
			Message: canonical.Message{
				Role:    canonical.RoleAssistant,
				Content: []canonical.ContentPart{},
			},
		}
		out := chatResponseToCompletion(resp, "auto")
		if out.Choices[0].Message.Content != "" {
			t.Errorf("empty content: got %q, want empty string", out.Choices[0].Message.Content)
		}
	})

	t.Run("model_echo", func(t *testing.T) {
		resp := &canonical.ChatResponse{
			StopReason: canonical.StopEndTurn,
			Message:    canonical.Message{Role: canonical.RoleAssistant},
		}
		out := chatResponseToCompletion(resp, "gpt-4o")
		if out.Model != "gpt-4o" {
			t.Errorf("model: got %q, want gpt-4o", out.Model)
		}
	})

	t.Run("finish_reason_mapping", func(t *testing.T) {
		cases := []struct {
			stop canonical.StopReason
			want string
		}{
			{canonical.StopEndTurn, "stop"},
			{canonical.StopMaxTokens, "length"},
			{canonical.StopRefusal, "content_filter"},
			{canonical.StopCancelled, "stop"},
			{canonical.StopUnknown, "stop"},
		}
		for _, tc := range cases {
			got := mapFinishReason(tc.stop)
			if got != tc.want {
				t.Errorf("mapFinishReason(%v): got %q, want %q", tc.stop, got, tc.want)
			}
		}
	})
}

// ---- helpers ----------------------------------------------------------------

// jsonRaw returns a json.RawMessage from a raw JSON string literal.
// TestWire_ToolCallRoundTrip covers the multi-turn tool-calling mapping:
// an assistant turn with tool_calls and null content must SURVIVE (it was
// previously dropped as empty), its tool_calls map onto
// canonical.Message.ToolCalls with arguments parsed from the JSON string,
// and a role:"tool" message maps onto RoleTool with ToolCallID + content.
func TestWire_ToolCallRoundTrip(t *testing.T) {
	body := `{
	  "model":"auto",
	  "messages":[
	    {"role":"user","content":"Weather in Paris?"},
	    {"role":"assistant","content":null,"tool_calls":[
	      {"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}
	    ]},
	    {"role":"tool","tool_call_id":"call_1","content":"18C sunny"}
	  ]
	}`
	var wire chatCompletionRequest
	if err := json.Unmarshal([]byte(body), &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	req := wireToChatRequest(&wire, fakeHTTPRequest(t))
	if len(req.Messages) != 3 {
		t.Fatalf("messages: got %d, want 3 (assistant tool-call turn must survive)", len(req.Messages))
	}
	asst := req.Messages[1]
	if asst.Role != canonical.RoleAssistant || len(asst.ToolCalls) != 1 {
		t.Fatalf("assistant tool_calls not mapped: %+v", asst)
	}
	tc := asst.ToolCalls[0]
	if tc.ID != "call_1" || tc.Name != "get_weather" || tc.Arguments["city"] != "Paris" {
		t.Errorf("tool call mapping: got id=%q name=%q args=%v", tc.ID, tc.Name, tc.Arguments)
	}
	tool := req.Messages[2]
	if tool.Role != canonical.RoleTool || tool.ToolCallID != "call_1" {
		t.Errorf("tool role/id: got role=%v id=%q", tool.Role, tool.ToolCallID)
	}
	if len(tool.Content) != 1 || tool.Content[0].Text != "18C sunny" {
		t.Errorf("tool content: %+v", tool.Content)
	}
}

func jsonRaw(s string) json.RawMessage {
	return json.RawMessage(s)
}

// fakeHTTPRequest returns a minimal *http.Request for wireToChatRequest tests
// that don't need special headers.
func fakeHTTPRequest(t *testing.T) *http.Request {
	t.Helper()
	return httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
}
