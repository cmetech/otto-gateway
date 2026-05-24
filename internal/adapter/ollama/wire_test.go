package ollama

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
)

// onePixelPNGBase64 is a 1×1 transparent PNG encoded as base64 (66 bytes
// decoded). Used to verify Codex M-1 image translation end-to-end.
const onePixelPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkAAIAAAoAAv/lxKUAAAAASUVORK5CYII="

// TestWireToChatRequest covers the canonical translation surface in
// table-driven form. Three rows: text-only message, system+user split,
// and X-Working-Dir header passthrough.
func TestWireToChatRequest(t *testing.T) {
	tests := []struct {
		name     string
		body     ollamaChatRequest
		wdHeader string
		assert   func(t *testing.T, got *canonical.ChatRequest)
	}{
		{
			name: "text only",
			body: ollamaChatRequest{
				Model: "auto",
				Messages: []ollamaMessage{
					{Role: "user", Content: "hi"},
				},
			},
			assert: func(t *testing.T, got *canonical.ChatRequest) {
				if got.Model != "auto" {
					t.Errorf("Model: got %q, want auto", got.Model)
				}
				if len(got.Messages) != 1 {
					t.Fatalf("Messages len: got %d, want 1", len(got.Messages))
				}
				if got.Messages[0].Role != canonical.RoleUser {
					t.Errorf("Role: got %v, want RoleUser", got.Messages[0].Role)
				}
				if len(got.Messages[0].Content) != 1 {
					t.Fatalf("Content len: got %d, want 1", len(got.Messages[0].Content))
				}
				if got.Messages[0].Content[0].Kind != canonical.ContentKindText {
					t.Errorf("Content[0].Kind: got %v, want Text", got.Messages[0].Content[0].Kind)
				}
				if got.Messages[0].Content[0].Text != "hi" {
					t.Errorf("Content[0].Text: got %q, want hi", got.Messages[0].Content[0].Text)
				}
			},
		},
		{
			name: "system message extracted",
			body: ollamaChatRequest{
				Messages: []ollamaMessage{
					{Role: "system", Content: "you are X"},
					{Role: "user", Content: "hi"},
				},
			},
			assert: func(t *testing.T, got *canonical.ChatRequest) {
				if got.System != "you are X" {
					t.Errorf("System: got %q, want %q", got.System, "you are X")
				}
				// All messages still flow through (system included for
				// round-trip; buildBlocks skips it via req.System path).
				if len(got.Messages) != 2 {
					t.Fatalf("Messages len: got %d, want 2", len(got.Messages))
				}
			},
		},
		{
			name: "X-Working-Dir header passthrough",
			body: ollamaChatRequest{
				Messages: []ollamaMessage{
					{Role: "user", Content: "hi"},
				},
			},
			wdHeader: "/tmp/worktree",
			assert: func(t *testing.T, got *canonical.ChatRequest) {
				if got.WorkingDirOverride != "/tmp/worktree" {
					t.Errorf("WorkingDirOverride: got %q, want /tmp/worktree", got.WorkingDirOverride)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/chat", nil)
			if tc.wdHeader != "" {
				r.Header.Set("X-Working-Dir", tc.wdHeader)
			}
			got := wireToChatRequest(&tc.body, r)
			tc.assert(t, got)
		})
	}
}

// TestWireToChatRequest_Images is the Codex M-1 acceptance test: one
// message with text + a base64 PNG → canonical Content with [text,
// image] parts; the image part carries detectMIME's "image/png" plus
// the original base64 string.
func TestWireToChatRequest_Images(t *testing.T) {
	body := ollamaChatRequest{
		Messages: []ollamaMessage{
			{
				Role:    "user",
				Content: "what is this?",
				Images:  []string{onePixelPNGBase64},
			},
		},
	}
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/chat", nil)
	got := wireToChatRequest(&body, r)
	if len(got.Messages) != 1 {
		t.Fatalf("Messages len: got %d, want 1", len(got.Messages))
	}
	parts := got.Messages[0].Content
	if len(parts) != 2 {
		t.Fatalf("Content len: got %d, want 2 (text + image)", len(parts))
	}
	if parts[0].Kind != canonical.ContentKindText || parts[0].Text != "what is this?" {
		t.Errorf("Content[0]: got %+v, want text 'what is this?'", parts[0])
	}
	if parts[1].Kind != canonical.ContentKindImage {
		t.Fatalf("Content[1].Kind: got %v, want ContentKindImage (Codex M-1 image translation broken)", parts[1].Kind)
	}
	if parts[1].Image == nil {
		t.Fatal("Content[1].Image is nil — wire.go did not populate the ImagePart")
	}
	if parts[1].Image.MIME != "image/png" {
		t.Errorf("Image.MIME: got %q, want image/png (detectMIME broken)", parts[1].Image.MIME)
	}
	if parts[1].Image.DataBase64 != onePixelPNGBase64 {
		t.Errorf("Image.DataBase64 round-trip broken")
	}
	// Also verify detectMIME would re-classify the same bytes.
	decoded, _ := base64.StdEncoding.DecodeString(onePixelPNGBase64)
	if got := detectMIME(decoded); got != "image/png" {
		t.Errorf("detectMIME of decoded PNG: got %q, want image/png", got)
	}
}

// TestWireToChatRequest_Images_MalformedBase64 proves a corrupt
// images[] entry is silently skipped (Codex M-1 defensive path) — the
// surviving message has only the text part.
func TestWireToChatRequest_Images_MalformedBase64(t *testing.T) {
	body := ollamaChatRequest{
		Messages: []ollamaMessage{
			{
				Role:    "user",
				Content: "hi",
				Images:  []string{"not-valid-base64-!!"},
			},
		},
	}
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/chat", nil)
	got := wireToChatRequest(&body, r)
	if len(got.Messages) != 1 {
		t.Fatalf("Messages len: got %d, want 1", len(got.Messages))
	}
	if len(got.Messages[0].Content) != 1 {
		t.Errorf("Content len: got %d, want 1 (malformed image must be dropped silently)", len(got.Messages[0].Content))
	}
}

// TestChatResponseToWire validates the duration split, RFC3339Nano
// timestamp formatting, mapStopReason values, and estimateTokens
// behaviour in one consolidated test.
func TestChatResponseToWire(t *testing.T) {
	start := time.Now().Add(-100 * time.Millisecond)
	resp := &canonical.ChatResponse{
		Model: "auto",
		Message: canonical.Message{
			Role:    canonical.RoleAssistant,
			Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "Hello there!"}},
		},
		StopReason: canonical.StopEndTurn,
	}
	got := chatResponseToWire(resp, start, "auto")
	if got.Model != "auto" {
		t.Errorf("Model: got %q, want auto", got.Model)
	}
	if got.Message.Role != "assistant" || got.Message.Content != "Hello there!" {
		t.Errorf("Message: got %+v", got.Message)
	}
	if got.DoneReason != "stop" {
		t.Errorf("DoneReason (StopEndTurn): got %q, want stop", got.DoneReason)
	}
	if !got.Done {
		t.Error("Done: got false, want true")
	}
	if got.TotalDuration == 0 {
		t.Error("TotalDuration: got 0; expected >0 (100ms elapsed)")
	}
	// 15/85 duration split.
	if got.PromptEvalDuration+got.EvalDuration > got.TotalDuration+10 {
		t.Errorf("duration split exceeds total: prompt=%d eval=%d total=%d", got.PromptEvalDuration, got.EvalDuration, got.TotalDuration)
	}
	if got.PromptEvalDuration > got.EvalDuration {
		t.Errorf("PromptEvalDuration should be smaller than EvalDuration (15/85 split): prompt=%d eval=%d", got.PromptEvalDuration, got.EvalDuration)
	}
	// estimateTokens of "Hello there!" (12 chars) = ceil(12/4) = 3.
	if got.EvalCount != 3 {
		t.Errorf("EvalCount: got %d, want 3 (estimateTokens('Hello there!'))", got.EvalCount)
	}
	// RFC3339Nano format — should parse back round-trip.
	if _, err := time.Parse(time.RFC3339Nano, got.CreatedAt); err != nil {
		t.Errorf("CreatedAt not parseable as RFC3339Nano: %q (%v)", got.CreatedAt, err)
	}
}

// TestMapStopReason exercises every canonical StopReason value.
func TestMapStopReason(t *testing.T) {
	cases := map[canonical.StopReason]string{
		canonical.StopEndTurn:         "stop",
		canonical.StopMaxTokens:       "length",
		canonical.StopMaxTurnRequests: "stop",
		canonical.StopRefusal:         "stop",
		canonical.StopCancelled:       "stop",
		canonical.StopUnknown:         "stop",
	}
	for in, want := range cases {
		if got := mapStopReason(in); got != want {
			t.Errorf("mapStopReason(%v): got %q, want %q", in, got, want)
		}
	}
}

// TestEstimateTokens covers the ceil(len/4) Node-parity behaviour.
func TestEstimateTokens(t *testing.T) {
	cases := []struct {
		s    string
		want int
	}{
		{"", 0},
		{"a", 1},
		{"abcd", 1},
		{"abcde", 2},
		{strings.Repeat("x", 12), 3},
		{strings.Repeat("x", 13), 4},
	}
	for _, c := range cases {
		if got := estimateTokens(c.s); got != c.want {
			t.Errorf("estimateTokens(%q): got %d, want %d", c.s, got, c.want)
		}
	}
}

// TestDetectMIME covers the four magic-byte cases.
func TestDetectMIME(t *testing.T) {
	cases := []struct {
		name string
		head []byte
		want string
	}{
		{"png", []byte{0x89, 0x50, 0x4e, 0x47, 0x0d}, "image/png"},
		{"jpeg", []byte{0xff, 0xd8, 0xff, 0xe0}, "image/jpeg"},
		{"gif87a", []byte{0x47, 0x49, 0x46, 0x38, 0x37, 0x61}, "image/gif"},
		{"unknown", []byte{0x00, 0x01, 0x02, 0x03}, "application/octet-stream"},
		{"too short", []byte{0x89}, "application/octet-stream"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := detectMIME(c.head); got != c.want {
				t.Errorf("detectMIME(%x): got %q, want %q", c.head, got, c.want)
			}
		})
	}
}
