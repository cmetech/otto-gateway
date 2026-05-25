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
func jsonRaw(s string) json.RawMessage {
	return json.RawMessage(s)
}

// fakeHTTPRequest returns a minimal *http.Request for wireToChatRequest tests
// that don't need special headers.
func fakeHTTPRequest(t *testing.T) *http.Request {
	t.Helper()
	return httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
}
