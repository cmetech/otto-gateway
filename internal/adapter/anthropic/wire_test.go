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

// ----------------------------------------------------------------------------
// Claude model-ID normalization
// ----------------------------------------------------------------------------
//
// Anthropic API clients (loop24-client, otto-cli, @anthropic-ai/sdk) send
// hyphen-separated version components (`claude-sonnet-4-6`). kiro-cli
// advertises and accepts dot-separated versions (`claude-sonnet-4.6`).
// kiro-cli's session/set_model silently accepts unknown IDs, and the
// subsequent session/prompt then fails with JSON-RPC -32603 ("Internal
// error"). Translating the model ID at the wire boundary fixes this
// surface compatibility without leaking kiro-cli wire shape into the
// canonical layer. The original wire.Model is still echoed back via the
// response renderers — only the canonical ChatRequest.Model is normalised.

func TestNormalizeClaudeModelID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// otto-cli / @anthropic-ai/sdk hyphen forms → kiro dot form.
		{"claude-sonnet-4-6", "claude-sonnet-4.6"},
		{"claude-sonnet-4-5", "claude-sonnet-4.5"},
		{"claude-haiku-4-5", "claude-haiku-4.5"},
		{"claude-opus-4-7", "claude-opus-4.7"},
		{"claude-opus-4-6", "claude-opus-4.6"},

		// Date-pinned form: drop the trailing -YYYYMMDD tag.
		{"claude-sonnet-4-5-20250514", "claude-sonnet-4.5"},
		{"claude-opus-4-7-20260101", "claude-opus-4.7"},

		// Major-only IDs (kiro advertises "claude-sonnet-4"): unchanged.
		{"claude-sonnet-4", "claude-sonnet-4"},

		// "auto" + empty + non-claude IDs: pass through untouched.
		{"auto", "auto"},
		{"", ""},
		{"deepseek-3.2", "deepseek-3.2"},
		{"glm-5", "glm-5"},
		{"qwen3-coder-next", "qwen3-coder-next"},

		// Already-normalised dot form: idempotent.
		{"claude-sonnet-4.6", "claude-sonnet-4.6"},
		{"claude-haiku-4.5", "claude-haiku-4.5"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := normalizeClaudeModelID(c.in)
			if got != c.want {
				t.Errorf("normalizeClaudeModelID(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestWire_ModelNormalizedToKiroForm verifies wireToChatRequest applies
// the Anthropic-→kiro model-ID translation so engine.SetModel hands kiro-cli
// an ID it recognises. End-to-end repro: claude-sonnet-4-6 produced a 500
// against real kiro-cli before this fix (set_model accepted silently,
// session/prompt then returned JSON-RPC -32603).
func TestWire_ModelNormalizedToKiroForm(t *testing.T) {
	logger, _ := captureLogger()
	wire := &anthropicMessagesRequest{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 256,
		Messages: []anthropicWireMessage{
			{Role: "user", Content: json.RawMessage(`"hi"`)},
		},
	}
	req := wireToChatRequest(wire, newTestRequest(t, ""), logger)
	if req.Model != "claude-sonnet-4.6" {
		t.Errorf("req.Model: got %q, want %q (normalize hyphen→dot for kiro-cli set_model)",
			req.Model, "claude-sonnet-4.6")
	}
}

// TestWire_AutoModelPassesThrough guards against over-normalisation breaking
// the "auto" / unset path. The engine special-cases Model=="auto" to skip
// SetModel entirely (engine/engine.go), so the canonical value must be
// preserved verbatim.
func TestWire_AutoModelPassesThrough(t *testing.T) {
	logger, _ := captureLogger()
	wire := &anthropicMessagesRequest{
		Model:     "auto",
		MaxTokens: 256,
		Messages: []anthropicWireMessage{
			{Role: "user", Content: json.RawMessage(`"hi"`)},
		},
	}
	req := wireToChatRequest(wire, newTestRequest(t, ""), logger)
	if req.Model != "auto" {
		t.Errorf("req.Model for auto: got %q, want %q", req.Model, "auto")
	}
}

// ----------------------------------------------------------------------------
// Phase 6 Plan 04 Task 1 — tools[] decode + tool_choice polymorphic decode (D-14)
// Closes the TODO(Phase 6) block inside wireToChatRequest.
// ----------------------------------------------------------------------------

// TestWireToChatRequest_Tools_Anthropic verifies that anthropicToolSpec
// entries are translated into canonical.ToolSpec with Parameters
// populated directly from the wire's input_schema field.
func TestWireToChatRequest_Tools_Anthropic(t *testing.T) {
	logger, _ := captureLogger()
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"location": map[string]any{"type": "string"},
		},
		"required": []any{"location"},
	}
	wire := &anthropicMessagesRequest{
		Model:     "auto",
		MaxTokens: 256,
		Messages: []anthropicWireMessage{
			{Role: "user", Content: json.RawMessage(`"hi"`)},
		},
		Tools: []anthropicToolSpec{
			{Name: "get_weather", Description: "look up weather", InputSchema: schema},
		},
	}
	req := wireToChatRequest(wire, newTestRequest(t, ""), logger)
	if len(req.Tools) != 1 {
		t.Fatalf("req.Tools: got %d, want 1; tools=%+v", len(req.Tools), req.Tools)
	}
	got := req.Tools[0]
	if got.Name != "get_weather" {
		t.Errorf("Tools[0].Name: got %q, want %q", got.Name, "get_weather")
	}
	if got.Description != "look up weather" {
		t.Errorf("Tools[0].Description: got %q, want %q", got.Description, "look up weather")
	}
	if got.Parameters == nil {
		t.Fatal("Tools[0].Parameters: nil (input_schema should map directly)")
	}
	if v, _ := got.Parameters["type"].(string); v != "object" {
		t.Errorf("Tools[0].Parameters[type]: got %v, want object", got.Parameters["type"])
	}
	props, ok := got.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("Tools[0].Parameters[properties]: got %T, want map[string]any", got.Parameters["properties"])
	}
	if _, ok := props["location"]; !ok {
		t.Errorf("Tools[0].Parameters.properties.location: missing")
	}
}

// TestWireToChatRequest_Tools_DropEmptyName guards the defensive
// skip-on-empty-name path. Anthropic spec requires `name`; clients that
// omit it should produce no canonical tool entry rather than a partially-
// populated ToolSpec.
func TestWireToChatRequest_Tools_DropEmptyName(t *testing.T) {
	logger, _ := captureLogger()
	wire := &anthropicMessagesRequest{
		Model:     "auto",
		MaxTokens: 256,
		Messages: []anthropicWireMessage{
			{Role: "user", Content: json.RawMessage(`"hi"`)},
		},
		Tools: []anthropicToolSpec{
			{Name: "", Description: "no name"},
			{Name: "real_tool", InputSchema: map[string]any{"type": "object"}},
		},
	}
	req := wireToChatRequest(wire, newTestRequest(t, ""), logger)
	if len(req.Tools) != 1 {
		t.Fatalf("req.Tools: got %d, want 1 (empty-name dropped); tools=%+v", len(req.Tools), req.Tools)
	}
	if req.Tools[0].Name != "real_tool" {
		t.Errorf("Tools[0].Name: got %q, want %q", req.Tools[0].Name, "real_tool")
	}
}

// TestWireToChatRequest_Tools_Empty_Anthropic verifies that the absence
// of tools[] leaves req.Tools nil — no spurious empty-slice allocation.
func TestWireToChatRequest_Tools_Empty_Anthropic(t *testing.T) {
	logger, _ := captureLogger()
	wire := &anthropicMessagesRequest{
		Model:     "auto",
		MaxTokens: 256,
		Messages: []anthropicWireMessage{
			{Role: "user", Content: json.RawMessage(`"hi"`)},
		},
	}
	req := wireToChatRequest(wire, newTestRequest(t, ""), logger)
	if req.Tools != nil {
		t.Errorf("req.Tools: got %+v, want nil (no tools[] in wire)", req.Tools)
	}
	if req.ToolChoice != nil {
		t.Errorf("req.ToolChoice: got %+v, want nil (no tool_choice in wire)", req.ToolChoice)
	}
}

// TestWireToChatRequest_ToolChoice_Auto verifies `{"type":"auto"}`
// decodes into canonical.ToolChoice{Type:"auto"}.
func TestWireToChatRequest_ToolChoice_Auto(t *testing.T) {
	logger, _ := captureLogger()
	wire := &anthropicMessagesRequest{
		Model:      "auto",
		MaxTokens:  256,
		Messages:   []anthropicWireMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
		ToolChoice: json.RawMessage(`{"type":"auto"}`),
	}
	req := wireToChatRequest(wire, newTestRequest(t, ""), logger)
	if req.ToolChoice == nil {
		t.Fatal("req.ToolChoice: nil; want {Type:auto}")
	}
	if req.ToolChoice.Type != "auto" || req.ToolChoice.Name != "" {
		t.Errorf("req.ToolChoice: got %+v, want {Type:auto,Name:\"\"}", req.ToolChoice)
	}
}

// TestWireToChatRequest_ToolChoice_Any_PreservedVerbatim is the REVIEW
// MEDIUM tool_choice coverage test. Anthropic uses 'any' where OpenAI
// uses 'required'; this gateway preserves both losslessly in canonical
// rather than silently mapping. Future engine/hook code that wants to
// treat them as semantic equivalents must do that translation
// explicitly — the adapter MUST NOT smear the distinction at the wire
// boundary.
func TestWireToChatRequest_ToolChoice_Any_PreservedVerbatim(t *testing.T) {
	logger, _ := captureLogger()
	wire := &anthropicMessagesRequest{
		Model:      "auto",
		MaxTokens:  256,
		Messages:   []anthropicWireMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
		ToolChoice: json.RawMessage(`{"type":"any"}`),
	}
	req := wireToChatRequest(wire, newTestRequest(t, ""), logger)
	if req.ToolChoice == nil {
		t.Fatal("req.ToolChoice: nil; want {Type:any}")
	}
	if req.ToolChoice.Type != "any" {
		t.Errorf("req.ToolChoice.Type: got %q, want \"any\" (NOT silently mapped to \"required\")", req.ToolChoice.Type)
	}
	// Downstream consumers see req.ToolChoice.Type == "any" as-is.
	// Anything else here would be a semantic loss.
	if req.ToolChoice.Type == "required" {
		t.Error("req.ToolChoice.Type: silently normalized to \"required\" — Anthropic 'any' must be preserved verbatim")
	}
}

// TestWireToChatRequest_ToolChoice_NamedTool verifies
// `{"type":"tool","name":"get_weather"}` decodes into
// canonical.ToolChoice{Type:"tool", Name:"get_weather"}.
func TestWireToChatRequest_ToolChoice_NamedTool(t *testing.T) {
	logger, _ := captureLogger()
	wire := &anthropicMessagesRequest{
		Model:      "auto",
		MaxTokens:  256,
		Messages:   []anthropicWireMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
		ToolChoice: json.RawMessage(`{"type":"tool","name":"get_weather"}`),
	}
	req := wireToChatRequest(wire, newTestRequest(t, ""), logger)
	if req.ToolChoice == nil {
		t.Fatal("req.ToolChoice: nil; want {Type:tool,Name:get_weather}")
	}
	if req.ToolChoice.Type != "tool" || req.ToolChoice.Name != "get_weather" {
		t.Errorf("req.ToolChoice: got %+v, want {Type:tool,Name:get_weather}", req.ToolChoice)
	}
}

// TestWireToChatRequest_ToolChoice_Unknown verifies that unknown-shape
// tool_choice values (numeric, etc.) accept-and-ignore — leave
// req.ToolChoice nil rather than reject the whole request.
func TestWireToChatRequest_ToolChoice_Unknown(t *testing.T) {
	logger, _ := captureLogger()
	wire := &anthropicMessagesRequest{
		Model:      "auto",
		MaxTokens:  256,
		Messages:   []anthropicWireMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
		ToolChoice: json.RawMessage(`42`),
	}
	req := wireToChatRequest(wire, newTestRequest(t, ""), logger)
	if req.ToolChoice != nil {
		t.Errorf("req.ToolChoice: got %+v, want nil (unknown shape accept-and-ignore)", req.ToolChoice)
	}
}
