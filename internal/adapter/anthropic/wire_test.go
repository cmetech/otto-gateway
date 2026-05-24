package anthropic

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"otto-gateway/internal/canonical"
)

// captureLogger returns a logger whose output is captured in the
// returned *bytes.Buffer. Used by tests that assert debug-log emission
// (D-09 image-drop, D-10 unknown-block, D-13 redacted_thinking-drop).
func captureLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h), &buf
}

// newTestRequest constructs an *http.Request with optional X-Working-Dir
// header. The body is empty — wire-decode tests build the
// anthropicMessagesRequest struct directly rather than round-tripping
// JSON through http.
func newTestRequest(t *testing.T, workingDir string) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/messages", strings.NewReader(""))
	if workingDir != "" {
		r.Header.Set("X-Working-Dir", workingDir)
	}
	return r
}

// ----------------------------------------------------------------------------
// System normalization (D-08)
// ----------------------------------------------------------------------------

func TestWire_SystemString(t *testing.T) {
	logger, _ := captureLogger()
	wire := &anthropicMessagesRequest{
		Model:     "auto",
		MaxTokens: 256,
		System:    json.RawMessage(`"you are helpful"`),
		Messages: []anthropicWireMessage{
			{Role: "user", Content: json.RawMessage(`"hi"`)},
		},
	}
	req := wireToChatRequest(wire, newTestRequest(t, ""), logger)
	if req.System != "you are helpful" {
		t.Errorf("System: got %q, want %q", req.System, "you are helpful")
	}
}

func TestWire_SystemArrayJoinedWithDoubleNewline(t *testing.T) {
	logger, _ := captureLogger()
	wire := &anthropicMessagesRequest{
		Model:     "auto",
		MaxTokens: 256,
		System:    json.RawMessage(`[{"type":"text","text":"part one"},{"type":"text","text":"part two"}]`),
		Messages: []anthropicWireMessage{
			{Role: "user", Content: json.RawMessage(`"hi"`)},
		},
	}
	req := wireToChatRequest(wire, newTestRequest(t, ""), logger)
	if req.System != "part one\n\npart two" {
		t.Errorf("System: got %q, want %q", req.System, "part one\n\npart two")
	}
}

func TestWire_SystemArrayDropsNonText(t *testing.T) {
	logger, _ := captureLogger()
	wire := &anthropicMessagesRequest{
		Model:     "auto",
		MaxTokens: 256,
		System:    json.RawMessage(`[{"type":"text","text":"keep","cache_control":{"type":"ephemeral"}},{"type":"image","source":{"type":"base64"}}]`),
		Messages: []anthropicWireMessage{
			{Role: "user", Content: json.RawMessage(`"hi"`)},
		},
	}
	req := wireToChatRequest(wire, newTestRequest(t, ""), logger)
	if req.System != "keep" {
		t.Errorf("System: got %q, want %q (cache_control ignored, image dropped)", req.System, "keep")
	}
}

// ----------------------------------------------------------------------------
// Message content normalization
// ----------------------------------------------------------------------------

func TestWire_ContentString(t *testing.T) {
	logger, _ := captureLogger()
	wire := &anthropicMessagesRequest{
		Model: "auto", MaxTokens: 256,
		Messages: []anthropicWireMessage{
			{Role: "user", Content: json.RawMessage(`"hi"`)},
		},
	}
	req := wireToChatRequest(wire, newTestRequest(t, ""), logger)
	if len(req.Messages) != 1 {
		t.Fatalf("messages: got %d, want 1", len(req.Messages))
	}
	if req.Messages[0].Role != canonical.RoleUser {
		t.Errorf("role: got %v, want RoleUser", req.Messages[0].Role)
	}
	if len(req.Messages[0].Content) != 1 || req.Messages[0].Content[0].Kind != canonical.ContentKindText {
		t.Errorf("content parts: got %+v, want one ContentKindText", req.Messages[0].Content)
	}
	if req.Messages[0].Content[0].Text != "hi" {
		t.Errorf("text: got %q, want %q", req.Messages[0].Content[0].Text, "hi")
	}
}

func TestWire_ContentArrayText(t *testing.T) {
	logger, _ := captureLogger()
	wire := &anthropicMessagesRequest{
		Model: "auto", MaxTokens: 256,
		Messages: []anthropicWireMessage{
			{Role: "user", Content: json.RawMessage(`[{"type":"text","text":"hi"}]`)},
		},
	}
	req := wireToChatRequest(wire, newTestRequest(t, ""), logger)
	if len(req.Messages) != 1 || len(req.Messages[0].Content) != 1 {
		t.Fatalf("messages/content: got %+v", req.Messages)
	}
	if req.Messages[0].Content[0].Kind != canonical.ContentKindText || req.Messages[0].Content[0].Text != "hi" {
		t.Errorf("content[0]: got %+v, want text=hi", req.Messages[0].Content[0])
	}
}

func TestWire_ContentArrayToolUse(t *testing.T) {
	logger, _ := captureLogger()
	wire := &anthropicMessagesRequest{
		Model: "auto", MaxTokens: 256,
		Messages: []anthropicWireMessage{
			{Role: "assistant", Content: json.RawMessage(`[{"type":"tool_use","id":"toolu_01","name":"foo","input":{"x":1}}]`)},
		},
	}
	req := wireToChatRequest(wire, newTestRequest(t, ""), logger)
	if len(req.Messages) != 1 || len(req.Messages[0].Content) != 1 {
		t.Fatalf("messages/content: got %+v", req.Messages)
	}
	p := req.Messages[0].Content[0]
	if p.Kind != canonical.ContentKindToolUse {
		t.Fatalf("kind: got %v, want ContentKindToolUse", p.Kind)
	}
	if p.ToolUse == nil {
		t.Fatal("ToolUse: nil")
	}
	if p.ToolUse.ID != "toolu_01" || p.ToolUse.Name != "foo" {
		t.Errorf("ToolUse id/name: got %q/%q, want toolu_01/foo", p.ToolUse.ID, p.ToolUse.Name)
	}
	// JSON numbers unmarshal as float64.
	if got, ok := p.ToolUse.Input["x"].(float64); !ok || got != 1.0 {
		t.Errorf("ToolUse.Input[x]: got %v (%T), want 1.0 (float64)", p.ToolUse.Input["x"], p.ToolUse.Input["x"])
	}
}

// ----------------------------------------------------------------------------
// tool_result (D-09)
// ----------------------------------------------------------------------------

func TestWire_ToolResultStringContent(t *testing.T) {
	logger, _ := captureLogger()
	wire := &anthropicMessagesRequest{
		Model: "auto", MaxTokens: 256,
		Messages: []anthropicWireMessage{
			{Role: "user", Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"toolu_01","content":"result text"}]`)},
		},
	}
	req := wireToChatRequest(wire, newTestRequest(t, ""), logger)
	p := req.Messages[0].Content[0]
	if p.Kind != canonical.ContentKindToolResult || p.ToolResult == nil {
		t.Fatalf("part: got %+v", p)
	}
	if p.ToolResult.ToolUseID != "toolu_01" || p.ToolResult.Content != "result text" {
		t.Errorf("ToolResult: got id=%q content=%q, want toolu_01/result text",
			p.ToolResult.ToolUseID, p.ToolResult.Content)
	}
}

func TestWire_ToolResultArrayContent_JoinedAndImagesDropped(t *testing.T) {
	logger, buf := captureLogger()
	wire := &anthropicMessagesRequest{
		Model: "auto", MaxTokens: 256,
		Messages: []anthropicWireMessage{
			{Role: "user", Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"toolu_01","content":[{"type":"text","text":"part1"},{"type":"text","text":"part2"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"xx"}}]}]`)},
		},
	}
	req := wireToChatRequest(wire, newTestRequest(t, ""), logger)
	p := req.Messages[0].Content[0]
	if p.ToolResult.Content != "part1\npart2" {
		t.Errorf("ToolResult.Content: got %q, want %q (text joined with \\n; image dropped)",
			p.ToolResult.Content, "part1\npart2")
	}
	// D-09: image drop must emit a debug log.
	if !strings.Contains(buf.String(), "dropping image inside tool_result.content") {
		t.Errorf("debug log: missing image-drop entry; buf=%s", buf.String())
	}
}

// ----------------------------------------------------------------------------
// thinking + redacted_thinking (D-11, D-13)
// ----------------------------------------------------------------------------

func TestWire_ThinkingBlockPreserved(t *testing.T) {
	logger, _ := captureLogger()
	wire := &anthropicMessagesRequest{
		Model: "auto", MaxTokens: 256,
		Messages: []anthropicWireMessage{
			{Role: "assistant", Content: json.RawMessage(`[{"type":"thinking","thinking":"reasoning"}]`)},
		},
	}
	req := wireToChatRequest(wire, newTestRequest(t, ""), logger)
	p := req.Messages[0].Content[0]
	if p.Kind != canonical.ContentKindThinking || p.Text != "reasoning" {
		t.Errorf("part: got %+v, want ContentKindThinking text=reasoning", p)
	}
}

// TestWire_RedactedThinking_Dropped covers W5 part 1 + D-13: redacted
// thinking blocks are dropped at wire decode with a debug log.
func TestWire_RedactedThinking_Dropped(t *testing.T) {
	logger, buf := captureLogger()
	wire := &anthropicMessagesRequest{
		Model: "auto", MaxTokens: 256,
		Messages: []anthropicWireMessage{
			{Role: "assistant", Content: json.RawMessage(`[{"type":"redacted_thinking","data":"<opaque-blob>"}]`)},
		},
	}
	req := wireToChatRequest(wire, newTestRequest(t, ""), logger)
	// (a) Zero ContentParts emitted — message is skipped entirely (empty
	//     parts → continue).
	if len(req.Messages) != 0 {
		t.Errorf("messages: got %d, want 0 (redacted_thinking-only message skipped)", len(req.Messages))
	}
	// (b) Debug log fired naming the dropped block type.
	if !strings.Contains(buf.String(), "redacted_thinking") {
		t.Errorf("debug log: missing redacted_thinking drop entry; buf=%s", buf.String())
	}
}

// ----------------------------------------------------------------------------
// resource_link (D-14)
// ----------------------------------------------------------------------------

func TestWire_ResourceLink_PopulatesResourceLinks(t *testing.T) {
	logger, _ := captureLogger()
	wire := &anthropicMessagesRequest{
		Model: "auto", MaxTokens: 256,
		Messages: []anthropicWireMessage{
			{Role: "user", Content: json.RawMessage(`[{"type":"resource_link","uri":"file:///tmp/x","name":"x"}]`)},
		},
	}
	req := wireToChatRequest(wire, newTestRequest(t, ""), logger)
	if len(req.ResourceLinks) != 1 {
		t.Fatalf("ResourceLinks: got %d, want 1", len(req.ResourceLinks))
	}
	if req.ResourceLinks[0].URI != "file:///tmp/x" || req.ResourceLinks[0].Name != "x" {
		t.Errorf("ResourceLinks[0]: got %+v, want {URI:file:///tmp/x Name:x}", req.ResourceLinks[0])
	}
	// No ContentPart was emitted for resource_link — it lives only in
	// ResourceLinks.
	if len(req.Messages) != 0 {
		t.Errorf("messages: got %d, want 0 (resource_link does not produce ContentPart)", len(req.Messages))
	}
}

// ----------------------------------------------------------------------------
// image blocks
// ----------------------------------------------------------------------------

func TestWire_Image_WellFormed(t *testing.T) {
	logger, _ := captureLogger()
	// 1x1 PNG (well-formed base64 — content doesn't have to be a real
	// PNG; decode just needs to succeed).
	data := base64.StdEncoding.EncodeToString([]byte{0x89, 0x50, 0x4e, 0x47})
	wire := &anthropicMessagesRequest{
		Model: "auto", MaxTokens: 256,
		Messages: []anthropicWireMessage{
			{Role: "user", Content: json.RawMessage(`[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + data + `"}}]`)},
		},
	}
	req := wireToChatRequest(wire, newTestRequest(t, ""), logger)
	p := req.Messages[0].Content[0]
	if p.Kind != canonical.ContentKindImage || p.Image == nil {
		t.Fatalf("part: got %+v", p)
	}
	if p.Image.MIME != "image/png" || p.Image.DataBase64 != data {
		t.Errorf("Image: got MIME=%q DataBase64=%q, want image/png/%s", p.Image.MIME, p.Image.DataBase64, data)
	}
}

func TestWire_Image_MalformedBase64_Dropped(t *testing.T) {
	logger, buf := captureLogger()
	wire := &anthropicMessagesRequest{
		Model: "auto", MaxTokens: 256,
		Messages: []anthropicWireMessage{
			{Role: "user", Content: json.RawMessage(`[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"!!!not-b64!!!"}}]`)},
		},
	}
	req := wireToChatRequest(wire, newTestRequest(t, ""), logger)
	// Message has no parts (image dropped) → skipped.
	if len(req.Messages) != 0 {
		t.Errorf("messages: got %d, want 0 (malformed image → empty parts → skipped)", len(req.Messages))
	}
	if !strings.Contains(buf.String(), "malformed base64") {
		t.Errorf("debug log: missing malformed-image entry; buf=%s", buf.String())
	}
}

// ----------------------------------------------------------------------------
// X-Working-Dir header (Phase 2 pattern carry-forward)
// ----------------------------------------------------------------------------

func TestWire_WorkingDirOverride(t *testing.T) {
	logger, _ := captureLogger()
	wire := &anthropicMessagesRequest{
		Model: "auto", MaxTokens: 256,
		Messages: []anthropicWireMessage{
			{Role: "user", Content: json.RawMessage(`"hi"`)},
		},
	}
	req := wireToChatRequest(wire, newTestRequest(t, "/tmp/work"), logger)
	if req.WorkingDirOverride != "/tmp/work" {
		t.Errorf("WorkingDirOverride: got %q, want %q", req.WorkingDirOverride, "/tmp/work")
	}
}

// ----------------------------------------------------------------------------
// Role mapping
// ----------------------------------------------------------------------------

func TestWire_MapAnthropicRole(t *testing.T) {
	cases := []struct {
		in   string
		want canonical.MessageRole
	}{
		{"user", canonical.RoleUser},
		{"assistant", canonical.RoleAssistant},
		{"system", canonical.RoleUser}, // Anthropic has no system role at message level
		{"tool", canonical.RoleUser},   // no tool role either — tool_result is a content block
		{"unknown", canonical.RoleUser},
		{"", canonical.RoleUser},
	}
	for _, c := range cases {
		if got := mapAnthropicRole(c.in); got != c.want {
			t.Errorf("mapAnthropicRole(%q): got %v, want %v", c.in, got, c.want)
		}
	}
}

// TestWire_StreamAndMaxTokensCopied verifies forward-design carry-through.
func TestWire_StreamAndMaxTokensCopied(t *testing.T) {
	logger, _ := captureLogger()
	wire := &anthropicMessagesRequest{
		Model:     "auto",
		MaxTokens: 512,
		Stream:    true,
		Messages: []anthropicWireMessage{
			{Role: "user", Content: json.RawMessage(`"hi"`)},
		},
	}
	req := wireToChatRequest(wire, newTestRequest(t, ""), logger)
	if !req.Stream {
		t.Errorf("Stream: got false, want true (copied from wire)")
	}
	if req.MaxTokens != 512 {
		t.Errorf("MaxTokens: got %d, want 512", req.MaxTokens)
	}
}
