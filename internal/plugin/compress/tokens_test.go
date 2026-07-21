// internal/plugin/compress/tokens_test.go
package compress

import (
	"strings"
	"testing"

	"otto-gateway/internal/canonical"
)

func TestEstimateTokens(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abcd", 1},
		{"abcde", 2}, // 5 chars → ceil(5/4) = 2
		{"12345678", 2},
	}
	for _, c := range cases {
		if got := estimateTokens(c.in); got != c.want {
			t.Errorf("estimateTokens(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func textMsg(role canonical.MessageRole, text string) canonical.Message {
	return canonical.Message{
		Role:    role,
		Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: text}},
	}
}

func TestFlattenText_TextThinkingAndToolResult(t *testing.T) {
	// Thinking is serialized to the ACP wire ([Reasoning] section,
	// build_acp.go:188-191) so it MUST count toward size and identity.
	m := canonical.Message{
		Role: canonical.RoleAssistant,
		Content: []canonical.ContentPart{
			{Kind: canonical.ContentKindText, Text: "head"},
			{Kind: canonical.ContentKindThinking, Text: "+think"},
			{Kind: canonical.ContentKindToolResult, ToolResult: &canonical.ToolResultPart{Content: "-tail"}},
			{Kind: canonical.ContentKindImage, Image: &canonical.ImagePart{DataBase64: "ignored"}},
		},
	}
	if got := flattenText(m); got != "head+think-tail" {
		t.Errorf("flattenText = %q, want %q", got, "head+think-tail")
	}
}

func TestEstMessagesTokens_IncludesToolCalls(t *testing.T) {
	msgs := []canonical.Message{
		textMsg(canonical.RoleUser, "12345678"), // 2 tokens
		{
			Role: canonical.RoleAssistant,
			ToolCalls: []canonical.ToolCall{
				{Name: "grep", Arguments: map[string]any{"q": "x"}},
			},
		},
	}
	got := estMessagesTokens(msgs)
	// 2 for the user text + at least 1 for the tool-call name/args JSON.
	if got < 3 {
		t.Errorf("estMessagesTokens = %d, want >= 3 (text + tool-call overhead)", got)
	}
}

func TestEstMessagesTokens_IncludesToolUseInput(t *testing.T) {
	// Anthropic-surface tool calls ride ContentKindToolUse parts, not
	// Message.ToolCalls — build_acp serializes their Input as an
	// [Assistant tool call] section, so a 20 KB Input must move the
	// estimate even with near-zero plain text.
	fatArg := strings.Repeat("x", 20_000)
	msgs := []canonical.Message{{
		Role: canonical.RoleAssistant,
		Content: []canonical.ContentPart{{
			Kind:    canonical.ContentKindToolUse,
			ToolUse: &canonical.ToolUsePart{ID: "t1", Name: "grep", Input: map[string]any{"q": fatArg}},
		}},
	}}
	if got := estMessagesTokens(msgs); got < 5000 {
		t.Errorf("estMessagesTokens = %d, want >= 5000 (ToolUse.Input JSON counted)", got)
	}
}

func TestEstMessagesTokens_SkipsRoleSystem(t *testing.T) {
	// build_acp never serializes RoleSystem transcript messages (the
	// system prompt rides req.System), and Ollama retains them in
	// Messages after hoisting — counting them would let the same logical
	// prompt cross the trigger on Ollama but not OpenAI/Anthropic.
	msgs := []canonical.Message{
		textMsg(canonical.RoleSystem, strings.Repeat("s", 8000)),
		textMsg(canonical.RoleUser, "12345678"), // 2 tokens
	}
	if got := estMessagesTokens(msgs); got != 2 {
		t.Errorf("estMessagesTokens = %d, want 2 (RoleSystem must not count)", got)
	}
}

func TestEstMessageTokens_PrefersToolUseCarrier(t *testing.T) {
	// appendAssistantToolCalls renders ToolUse parts and SKIPS ToolCalls
	// when any ToolUse part rendered — a message carrying both must not
	// be double-counted.
	arg := strings.Repeat("x", 4000)
	both := canonical.Message{
		Role: canonical.RoleAssistant,
		Content: []canonical.ContentPart{{
			Kind:    canonical.ContentKindToolUse,
			ToolUse: &canonical.ToolUsePart{ID: "t1", Name: "grep", Input: map[string]any{"q": arg}},
		}},
		ToolCalls: []canonical.ToolCall{{ID: "t1", Name: "grep", Arguments: map[string]any{"q": arg}}},
	}
	only := both
	only.ToolCalls = nil
	if estMessageTokens(both) != estMessageTokens(only) {
		t.Errorf("both-carriers message double-counted: %d vs %d",
			estMessageTokens(both), estMessageTokens(only))
	}
}

func TestEstMessageTokens_RoleKindMatrix(t *testing.T) {
	// Revision-4: the estimator mirrors build_acp's per-role branches —
	// carriers ACP never serializes for a role must count ZERO there.
	big := strings.Repeat("x", 4000) // 1000 tokens
	thinking := canonical.ContentPart{Kind: canonical.ContentKindThinking, Text: big}
	toolResult := canonical.ContentPart{
		Kind:       canonical.ContentKindToolResult,
		ToolResult: &canonical.ToolResultPart{ToolUseID: "t", Content: big},
	}
	toolUse := canonical.ContentPart{
		Kind:    canonical.ContentKindToolUse,
		ToolUse: &canonical.ToolUsePart{ID: "t", Name: "grep", Input: map[string]any{"q": big}},
	}
	cases := []struct {
		name    string
		m       canonical.Message
		counted bool
	}{
		{"assistant-thinking", canonical.Message{Role: canonical.RoleAssistant, Content: []canonical.ContentPart{thinking}}, true},
		{"assistant-tooluse", canonical.Message{Role: canonical.RoleAssistant, Content: []canonical.ContentPart{toolUse}}, true},
		{"user-thinking-ignored", canonical.Message{Role: canonical.RoleUser, Content: []canonical.ContentPart{thinking}}, false},
		{"user-toolcalls-ignored", canonical.Message{Role: canonical.RoleUser, ToolCalls: []canonical.ToolCall{{Name: "grep", Arguments: map[string]any{"q": big}}}}, false},
		{"user-toolresult", canonical.Message{Role: canonical.RoleUser, Content: []canonical.ContentPart{toolResult}}, true},
		{"tool-text", textMsg(canonical.RoleTool, big), true},
		{"tool-toolresult-part-ignored", canonical.Message{Role: canonical.RoleTool, Content: []canonical.ContentPart{toolResult}}, false},
		{"system-anything", textMsg(canonical.RoleSystem, big), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := estMessageTokens(c.m)
			if c.counted && got < 500 {
				t.Errorf("estMessageTokens = %d, want large (ACP serializes this carrier for this role)", got)
			}
			if !c.counted && got != 0 {
				t.Errorf("estMessageTokens = %d, want 0 (ACP ignores this carrier for this role)", got)
			}
		})
	}
}
