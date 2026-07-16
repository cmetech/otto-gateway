// Package engine — buildBlocks golden bracketed-section tests +
// image-block emission tests. D-02 + D-09 footnote + Codex M-1.
package engine

import (
	"encoding/base64"
	"reflect"
	"testing"

	"otto-gateway/internal/canonical"
)

// TestBuildBlocks_GoldenSystemUserAssistant verifies the bracketed-
// section text output for a standard system/user/assistant transcript.
// The text block must be element [0] of the returned slice (image
// blocks, when present, follow).
func TestBuildBlocks_GoldenSystemUserAssistant(t *testing.T) {
	req := &canonical.ChatRequest{
		System: "You are helpful.",
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "Hello!"},
			}},
			{Role: canonical.RoleAssistant, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "Hi there."},
			}},
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "How are you?"},
			}},
		},
	}
	got := buildBlocks(req)
	if len(got) == 0 {
		t.Fatal("buildBlocks returned empty slice; expected at least the text block")
	}
	if got[0].Kind != canonical.BlockKindText {
		t.Fatalf("first block kind: got %v, want BlockKindText", got[0].Kind)
	}
	if got[0].Text == nil {
		t.Fatal("first block Text is nil")
	}
	// Defect 2 (2026-07-16): the [System] section now pairs the caller's
	// identity with the brand-neutral identityGuardClause.
	want := "[System]\nYou are helpful.\n\n" + identityGuardClause + "\n\n[User]\nHello!\n\n[Assistant]\nHi there.\n\n[User]\nHow are you?"
	if got[0].Text.Content != want {
		t.Errorf("bracketed text mismatch.\n got: %q\nwant: %q", got[0].Text.Content, want)
	}
}

// TestBuildBlocks_IdentityGuard_AlwaysPresent (Defect 2): the persona guard
// clause is emitted on every request — including a bare "who are you?" turn
// with NO caller system prompt — so kiro-cli's built-in persona cannot leak.
// The guard is brand-neutral (no OTTO/LOOP24), names Kiro/AWS only to forbid
// them, and uses no angle-bracket markers.
func TestBuildBlocks_IdentityGuard_AlwaysPresent(t *testing.T) {
	t.Run("no_system_prompt", func(t *testing.T) {
		req := &canonical.ChatRequest{
			Messages: []canonical.Message{
				{Role: canonical.RoleUser, Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: "who are you?"},
				}},
			},
		}
		got := buildBlocks(req)
		content := got[0].Text.Content
		if !contains(content, "[System]") {
			t.Errorf("expected a [System] section even without a caller system prompt; got %q", content)
		}
		if !contains(content, identityGuardClause) {
			t.Errorf("expected identity guard clause in output; got %q", content)
		}
		// Guard must forbid the kiro/AWS persona and cross-agent deferral.
		for _, sub := range []string{`"Kiro CLI"`, "AWS", "requires, a different agent"} {
			if !contains(content, sub) {
				t.Errorf("guard missing expected phrase %q; got %q", sub, content)
			}
		}
		// No brand hardcode, no angle-bracket markers.
		for _, banned := range []string{"OTTO", "LOOP24", "<", ">"} {
			if contains(content, banned) {
				t.Errorf("guard must not contain %q; got %q", banned, content)
			}
		}
	})

	t.Run("with_system_prompt_caller_identity_precedes_guard", func(t *testing.T) {
		req := &canonical.ChatRequest{
			System: "You are Aria, the host assistant.",
			Messages: []canonical.Message{
				{Role: canonical.RoleUser, Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: "hi"},
				}},
			},
		}
		got := buildBlocks(req)
		content := got[0].Text.Content
		idxCaller := indexOfSection(content, "You are Aria, the host assistant.")
		idxGuard := indexOfSection(content, identityGuardClause)
		if idxCaller < 0 || idxGuard < 0 {
			t.Fatalf("expected both caller identity and guard; got %q", content)
		}
		if idxCaller >= idxGuard {
			t.Errorf("caller identity must precede the guard; got %q", content)
		}
	})
}

// TestBuildBlocks_ThinkBlock verifies the [Reasoning] section emits when
// req.Think is true.
func TestBuildBlocks_ThinkBlock(t *testing.T) {
	req := &canonical.ChatRequest{
		Think: true,
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "Help me debug."},
			}},
		},
	}
	got := buildBlocks(req)
	if got[0].Text == nil {
		t.Fatal("expected text block")
	}
	if !contains(got[0].Text.Content, "[Reasoning]") {
		t.Errorf("expected [Reasoning] block in output; got %q", got[0].Text.Content)
	}
	if !contains(got[0].Text.Content, "[User]") {
		t.Errorf("expected [User] block in output; got %q", got[0].Text.Content)
	}
}

// TestBuildBlocks_FormatBlock verifies the [Output format] section
// emits when req.Format is non-nil.
func TestBuildBlocks_FormatBlock(t *testing.T) {
	req := &canonical.ChatRequest{
		Format: &canonical.Format{Type: "json"},
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "Give me JSON."},
			}},
		},
	}
	got := buildBlocks(req)
	if got[0].Text == nil {
		t.Fatal("expected text block")
	}
	if !contains(got[0].Text.Content, "[Output format]") {
		t.Errorf("expected [Output format] block; got %q", got[0].Text.Content)
	}
}

// TestBuildBlocks_DropsSystemMessage verifies that RoleSystem messages
// do NOT appear in the transcript body (System field is the canonical
// source and already extracted into the [System] header).
func TestBuildBlocks_DropsSystemMessage(t *testing.T) {
	req := &canonical.ChatRequest{
		System: "Already extracted.",
		Messages: []canonical.Message{
			// This RoleSystem message body must NOT appear in transcript.
			{Role: canonical.RoleSystem, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "SHOULD-NOT-APPEAR"},
			}},
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "Hello."},
			}},
		},
	}
	got := buildBlocks(req)
	if got[0].Text == nil {
		t.Fatal("expected text block")
	}
	if contains(got[0].Text.Content, "SHOULD-NOT-APPEAR") {
		t.Errorf("system message body leaked into transcript: %q", got[0].Text.Content)
	}
	if !contains(got[0].Text.Content, "[System]\nAlready extracted.") {
		t.Errorf("expected [System] header with the System field value; got %q", got[0].Text.Content)
	}
}

// TestBuildBlocks_EmitsImageBlock_ForContentKindImage (Codex M-1 / D-09
// footnote) — proves ContentKindImage parts produce BlockKindImage
// blocks, not silently dropped.
func TestBuildBlocks_EmitsImageBlock_ForContentKindImage(t *testing.T) {
	pngBytes := []byte{0x89, 0x50, 0x4e, 0x47}
	dataB64 := base64.StdEncoding.EncodeToString(pngBytes)
	req := &canonical.ChatRequest{
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "describe this"},
				{Kind: canonical.ContentKindImage, Image: &canonical.ImagePart{
					MIME:       "image/png",
					DataBase64: dataB64,
				}},
			}},
		},
	}
	got := buildBlocks(req)
	if len(got) != 2 {
		t.Fatalf("expected 2 blocks (text + image), got %d", len(got))
	}
	if got[0].Kind != canonical.BlockKindText {
		t.Errorf("block[0] kind: got %v, want BlockKindText", got[0].Kind)
	}
	if got[1].Kind != canonical.BlockKindImage {
		t.Errorf("block[1] kind: got %v, want BlockKindImage", got[1].Kind)
	}
	if got[1].Image == nil {
		t.Fatal("block[1].Image is nil")
	}
	if got[1].Image.MIMEType != "image/png" {
		t.Errorf("block[1].Image.MIMEType: got %q, want image/png", got[1].Image.MIMEType)
	}
	if !reflect.DeepEqual(got[1].Image.Data, pngBytes) {
		t.Errorf("block[1].Image.Data: got %v, want %v", got[1].Image.Data, pngBytes)
	}
}

// TestBuildBlocks_SkipsMalformedBase64 (defensive — Codex M-1) — a
// single corrupt base64 image must NOT abort buildBlocks; the text
// block survives and no image block is emitted.
func TestBuildBlocks_SkipsMalformedBase64(t *testing.T) {
	req := &canonical.ChatRequest{
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "look"},
				{Kind: canonical.ContentKindImage, Image: &canonical.ImagePart{
					MIME:       "image/png",
					DataBase64: "not-valid-base64-!@#$",
				}},
			}},
		},
	}
	got := buildBlocks(req)
	if len(got) != 1 {
		t.Fatalf("expected 1 block (text only — malformed image skipped), got %d", len(got))
	}
	if got[0].Kind != canonical.BlockKindText {
		t.Errorf("block[0] kind: got %v, want BlockKindText", got[0].Kind)
	}
	if got[0].Text == nil {
		t.Fatal("text block has nil Text")
	}
	if !contains(got[0].Text.Content, "look") {
		t.Errorf("text content lost: got %q", got[0].Text.Content)
	}
}

// TestBuildBlocks_MultipleImages_PreservesOrder — two ContentKindImage
// parts in the same message produce three blocks (text + 2 images)
// with the images in message order.
func TestBuildBlocks_MultipleImages_PreservesOrder(t *testing.T) {
	img1 := []byte{0x01, 0x02}
	img2 := []byte{0x03, 0x04}
	b1 := base64.StdEncoding.EncodeToString(img1)
	b2 := base64.StdEncoding.EncodeToString(img2)
	req := &canonical.ChatRequest{
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "two images"},
				{Kind: canonical.ContentKindImage, Image: &canonical.ImagePart{MIME: "image/png", DataBase64: b1}},
				{Kind: canonical.ContentKindImage, Image: &canonical.ImagePart{MIME: "image/jpeg", DataBase64: b2}},
			}},
		},
	}
	got := buildBlocks(req)
	if len(got) != 3 {
		t.Fatalf("expected 3 blocks (text + 2 images), got %d", len(got))
	}
	if !reflect.DeepEqual(got[1].Image.Data, img1) {
		t.Errorf("block[1] data: got %v, want %v", got[1].Image.Data, img1)
	}
	if got[1].Image.MIMEType != "image/png" {
		t.Errorf("block[1] MIME: got %q, want image/png", got[1].Image.MIMEType)
	}
	if !reflect.DeepEqual(got[2].Image.Data, img2) {
		t.Errorf("block[2] data: got %v, want %v", got[2].Image.Data, img2)
	}
	if got[2].Image.MIMEType != "image/jpeg" {
		t.Errorf("block[2] MIME: got %q, want image/jpeg", got[2].Image.MIMEType)
	}
}

// TestBuildBlocks_AssistantWithThinking (Phase 3.1 D-11, option a):
// when an assistant message carries BOTH a text part and a thinking
// part, the transcript emits a [Reasoning] bracketed section AFTER
// the [Assistant] section. Keeps [Assistant] semantically pure for
// response text and gives kiro-cli the thinking content as a
// distinct section in the prompt — matches the [System] / [User] /
// [Assistant] convention from the Node reference.
func TestBuildBlocks_AssistantWithThinking(t *testing.T) {
	req := &canonical.ChatRequest{
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "Why?"},
			}},
			{Role: canonical.RoleAssistant, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "Because reasons."},
				{Kind: canonical.ContentKindThinking, Text: "Step 1: identify cause. Step 2: explain."},
			}},
		},
	}
	got := buildBlocks(req)
	if got[0].Text == nil {
		t.Fatal("expected text block")
	}
	content := got[0].Text.Content

	// The [Reasoning] section is forward-design seam from Phase 2 and
	// also appears via req.Think=true, so the assertion specifically
	// checks for the THINKING content text after [Reasoning] (not just
	// the header) and the ordering: Assistant text before Reasoning.
	assistantIdx := indexOfSection(content, "[Assistant]\nBecause reasons.")
	reasoningIdx := indexOfSection(content, "[Reasoning]\nStep 1: identify cause. Step 2: explain.")
	if assistantIdx < 0 {
		t.Errorf("expected [Assistant] section with text; got %q", content)
	}
	if reasoningIdx < 0 {
		t.Errorf("expected [Reasoning] section with thinking text; got %q", content)
	}
	if assistantIdx > reasoningIdx {
		t.Errorf("ordering: [Assistant] must appear BEFORE [Reasoning] in the transcript; got %q", content)
	}
}

// TestBuildBlocks_AssistantTextOnly_NoReasoningSection (Phase 3.1 D-11
// regression guard): an assistant message with only a text part MUST
// NOT emit a [Reasoning] section. This guards against an accidental
// double-section emit when the new joinThinkingParts helper is added
// alongside the existing joinTextParts.
func TestBuildBlocks_AssistantTextOnly_NoReasoningSection(t *testing.T) {
	req := &canonical.ChatRequest{
		Messages: []canonical.Message{
			{Role: canonical.RoleAssistant, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "Hi there."},
			}},
		},
	}
	got := buildBlocks(req)
	if got[0].Text == nil {
		t.Fatal("expected text block")
	}
	content := got[0].Text.Content
	if contains(content, "[Reasoning]") {
		t.Errorf("text-only assistant must NOT emit [Reasoning]; got %q", content)
	}
	if !contains(content, "[Assistant]\nHi there.") {
		t.Errorf("expected [Assistant] section with text; got %q", content)
	}
}

// indexOfSection returns the byte index of `needle` in `haystack` or
// -1 when absent. Tiny helper so the ordering assertion above stays
// readable.
func indexOfSection(haystack, needle string) int {
	if len(needle) > len(haystack) {
		return -1
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// TestBuildBlocks_AvailableTools_JSONCatalog (Phase 6 D-16 + REVIEW LOW #6):
// when req.Tools is non-empty, buildBlocks emits the full JSON tool catalog
// inside the [Available tools] bracketed section. When req.Tools is nil, no
// [Available tools] section appears. When json.Marshal fails on a pathological
// tool spec, the section header is still present (defensive degrade — no panic,
// no error propagated) and a debug log is emitted via slog.Default().
func TestBuildBlocks_AvailableTools_JSONCatalog(t *testing.T) {
	t.Run("nil_tools_no_section", func(t *testing.T) {
		req := &canonical.ChatRequest{
			Messages: []canonical.Message{
				{Role: canonical.RoleUser, Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: "hi"},
				}},
			},
		}
		got := buildBlocks(req)
		if got[0].Text == nil {
			t.Fatal("expected text block")
		}
		if contains(got[0].Text.Content, "[Available tools]") {
			t.Errorf("nil Tools must NOT emit [Available tools] section; got %q", got[0].Text.Content)
		}
	})

	t.Run("single_tool_emits_json_catalog", func(t *testing.T) {
		req := &canonical.ChatRequest{
			Tools: []canonical.ToolSpec{
				{
					Name:        "get_weather",
					Description: "Get current weather",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"location": map[string]any{"type": "string"},
						},
						"required": []any{"location"},
					},
				},
			},
			Messages: []canonical.Message{
				{Role: canonical.RoleUser, Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: "weather?"},
				}},
			},
		}
		got := buildBlocks(req)
		if got[0].Text == nil {
			t.Fatal("expected text block")
		}
		content := got[0].Text.Content
		assertions := []string{
			"[Available tools]",
			"```json",
			`"name":"get_weather"`,
			`"description":"Get current weather"`,
			`"properties"`,
			`"location"`,
		}
		for _, sub := range assertions {
			if !contains(content, sub) {
				t.Errorf("[Available tools] catalog missing %q in:\n%s", sub, content)
			}
		}
	})

	t.Run("multi_tool_preserves_declaration_order", func(t *testing.T) {
		req := &canonical.ChatRequest{
			Tools: []canonical.ToolSpec{
				{Name: "alpha_tool", Description: "first", Parameters: map[string]any{"type": "object"}},
				{Name: "beta_tool", Description: "second", Parameters: map[string]any{"type": "object"}},
			},
			Messages: []canonical.Message{
				{Role: canonical.RoleUser, Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: "x"},
				}},
			},
		}
		got := buildBlocks(req)
		if got[0].Text == nil {
			t.Fatal("expected text block")
		}
		content := got[0].Text.Content
		alphaIdx := indexOfSection(content, `"name":"alpha_tool"`)
		betaIdx := indexOfSection(content, `"name":"beta_tool"`)
		if alphaIdx < 0 || betaIdx < 0 {
			t.Fatalf("expected both tool names in catalog; got %q", content)
		}
		if alphaIdx > betaIdx {
			t.Errorf("tool declaration order not preserved: alpha (idx %d) appears after beta (idx %d) in:\n%s", alphaIdx, betaIdx, content)
		}
	})

	t.Run("marshal_failure_falls_back_to_header_only", func(t *testing.T) {
		// REVIEW LOW #6: json.Marshal cannot serialize channels — inject
		// one into Parameters as the marshal-error trigger. Expectation:
		// (a) no panic, (b) section header still present, (c) buildBlocks
		// returns successfully (does not propagate the marshal error).
		req := &canonical.ChatRequest{
			Tools: []canonical.ToolSpec{
				{
					Name:        "bad_tool",
					Description: "has unmarshal-hostile params",
					Parameters: map[string]any{
						"bad": make(chan int),
					},
				},
			},
			Messages: []canonical.Message{
				{Role: canonical.RoleUser, Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: "x"},
				}},
			},
		}
		// Defer-recover so an accidental panic surfaces as a test failure
		// rather than a process crash.
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("buildBlocks panicked on marshal-failure path: %v", r)
			}
		}()
		got := buildBlocks(req)
		if len(got) == 0 || got[0].Text == nil {
			t.Fatal("expected at least the text block on marshal-failure path")
		}
		content := got[0].Text.Content
		if !contains(content, "[Available tools]") {
			t.Errorf("marshal-failure fallback must still emit [Available tools] header; got %q", content)
		}
	})
}

// Example_buildBlocks is a runnable godoc example (TRST-07). The
// Output: block is validated by `go test -run Example`. Lowercase
// suffix style because buildBlocks is unexported.
// TestBuildBlocks_SystemThenUser pins the canonical single-turn transcript
// shape: [System] (caller identity + Defect-2 guard) then [User]. Replaces
// the former Example_buildBlocks, whose exact-stdout match became brittle
// once the always-on identityGuardClause landed in the [System] section.
func TestBuildBlocks_SystemThenUser(t *testing.T) {
	req := &canonical.ChatRequest{
		System: "Be brief.",
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "Hi."},
			}},
		},
	}
	got := buildBlocks(req)
	want := "[System]\nBe brief.\n\n" + identityGuardClause + "\n\n[User]\nHi."
	if got[0].Text.Content != want {
		t.Errorf("transcript mismatch.\n got: %q\nwant: %q", got[0].Text.Content, want)
	}
}

// TestBuildBlocks_StrictToolPrompt verifies that the strict function-calling
// prompt is emitted when tools are present. The prompt must include markers
// indicating the expected JSON structure ("tool_call", "arguments") and must
// NOT contain the old weak prompt text. The tool name from the catalog must
// be present in the emitted prompt.
func TestBuildBlocks_StrictToolPrompt(t *testing.T) {
	req := &canonical.ChatRequest{
		Tools: []canonical.ToolSpec{
			{
				Name:        "get_weather",
				Description: "Get current weather",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"location": map[string]any{"type": "string"},
					},
				},
			},
		},
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "What's the weather?"},
			}},
		},
	}
	got := buildBlocks(req)
	if len(got) == 0 || got[0].Text == nil {
		t.Fatal("expected text block with strict tool prompt")
	}
	content := got[0].Text.Content

	// Assert key markers of strict function-calling prompt are present
	requiredStrings := []string{
		"\"tool_call\"",
		"\"arguments\"",
		"must NOT use your own built-in tools",
		"get_weather",
	}
	for _, required := range requiredStrings {
		if !contains(content, required) {
			t.Errorf("strict prompt missing required marker %q; content:\n%s", required, content)
		}
	}

	// Assert old weak prompt is NOT present
	if contains(content, "Emit a tool_call ACP notification") {
		t.Errorf("old weak prompt still present; should use strict prompt. Content:\n%s", content)
	}
}

// TestBuildBlocks_MultiTurnToolCall_Anthropic verifies the JS-parity
// multi-turn tool-calling sections for the Anthropic canonical shape:
// an assistant ContentKindToolUse renders as [Assistant tool call], and a
// following user turn's ContentKindToolResult renders as [Tool result …]
// BEFORE that turn's own [User] text (JS reference 830 + 1798-1810).
// Without these sections kiro never sees its prior call or the result and
// re-invokes the tool (the phase-2 re-call bug).
func TestBuildBlocks_MultiTurnToolCall_Anthropic(t *testing.T) {
	req := &canonical.ChatRequest{
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "Weather in Paris?"},
			}},
			{Role: canonical.RoleAssistant, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindToolUse, ToolUse: &canonical.ToolUsePart{
					ID: "call_1", Name: "get_weather", Input: map[string]any{"city": "Paris"},
				}},
			}},
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindToolResult, ToolResult: &canonical.ToolResultPart{
					ToolUseID: "call_1", Content: "18C sunny",
				}},
				{Kind: canonical.ContentKindText, Text: "thanks"},
			}},
		},
	}
	got := buildBlocks(req)[0].Text.Content
	if !contains(got, "[Assistant tool call: get_weather]\n{\n  \"city\": \"Paris\"\n}") {
		t.Errorf("missing/mis-rendered [Assistant tool call] section in:\n%s", got)
	}
	// Contiguous block proves BOTH the [Tool result] rendering AND that it
	// precedes the same turn's [User] text.
	if !contains(got, "[Tool result (id: call_1)]\n18C sunny\n\n[User]\nthanks") {
		t.Errorf("tool result must render before [User] text in:\n%s", got)
	}
}

// TestBuildBlocks_MultiTurnToolCall_ToolCallsAndRoleTool verifies the same
// sections for the OpenAI/Ollama canonical shape: assistant tool calls on
// Message.ToolCalls, and the tool result carried as a RoleTool message
// with ToolCallID (JS reference 830 + 836-838). Also asserts the
// is_error prefix and the empty-id ([Tool result], no "(id: …)") form.
func TestBuildBlocks_MultiTurnToolCall_ToolCallsAndRoleTool(t *testing.T) {
	req := &canonical.ChatRequest{
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "Weather in Paris?"},
			}},
			{Role: canonical.RoleAssistant, ToolCalls: []canonical.ToolCall{
				{ID: "call_1", Name: "get_weather", Arguments: map[string]any{"city": "Paris"}},
			}},
			{Role: canonical.RoleTool, ToolCallID: "call_1", Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "18C sunny"},
			}},
		},
	}
	got := buildBlocks(req)[0].Text.Content
	if !contains(got, "[Assistant tool call: get_weather]\n{\n  \"city\": \"Paris\"\n}") {
		t.Errorf("missing/mis-rendered [Assistant tool call] section in:\n%s", got)
	}
	if !contains(got, "[Tool result (id: call_1)]\n18C sunny") {
		t.Errorf("missing [Tool result (id: …)] section in:\n%s", got)
	}

	// Error tool result with no call id → "[TOOL ERROR]" prefix, bare header.
	req2 := &canonical.ChatRequest{
		Messages: []canonical.Message{
			{Role: canonical.RoleTool, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: "boom"},
			}},
		},
	}
	// IsError lives on ToolResultPart (Anthropic path); exercise it there.
	req3 := &canonical.ChatRequest{
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindToolResult, ToolResult: &canonical.ToolResultPart{
					Content: "boom", IsError: true,
				}},
			}},
		},
	}
	if got2 := buildBlocks(req2)[0].Text.Content; !contains(got2, "[Tool result]\nboom") {
		t.Errorf("empty-id tool result should render bare [Tool result] header; got:\n%s", got2)
	}
	if got3 := buildBlocks(req3)[0].Text.Content; !contains(got3, "[Tool result]\n[TOOL ERROR] boom") {
		t.Errorf("error tool result should carry [TOOL ERROR] prefix; got:\n%s", got3)
	}
}

// contains is a tiny helper to keep test-string assertions readable
// without importing strings in every test file.
func contains(s, sub string) bool {
	if sub == "" {
		return true
	}
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
