// Whitebox unit tests for translateUpdate (package acp).
// D-18: whitebox package gives access to unexported types.
package acp

import (
	"encoding/json"
	"strings"
	"testing"

	"loop24-gateway/internal/canonical"
)

func TestTranslateText(t *testing.T) {
	t.Parallel()
	u := sessionUpdateParams{Type: "text", Content: "hello world"}
	ch := translateUpdate(u)
	if ch.Kind != canonical.ChunkKindText {
		t.Fatalf("kind: got %v, want ChunkKindText", ch.Kind)
	}
	if ch.Text == nil || ch.Text.Content != "hello world" {
		t.Errorf("Text.Content: got %v, want hello world", ch.Text)
	}
}

func TestTranslateThought(t *testing.T) {
	t.Parallel()
	u := sessionUpdateParams{Type: "thought", Content: "reasoning step"}
	ch := translateUpdate(u)
	if ch.Kind != canonical.ChunkKindThought {
		t.Fatalf("kind: got %v, want ChunkKindThought", ch.Kind)
	}
	if ch.Thought == nil || ch.Thought.Content != "reasoning step" {
		t.Errorf("Thought.Content: got %v, want reasoning step", ch.Thought)
	}
}

func TestTranslateToolCall(t *testing.T) {
	t.Parallel()
	u := sessionUpdateParams{
		Type:     "tool_call",
		ToolName: "bash",
		Args:     map[string]any{"cmd": "ls"},
	}
	ch := translateUpdate(u)
	if ch.Kind != canonical.ChunkKindToolCall {
		t.Fatalf("kind: got %v, want ChunkKindToolCall", ch.Kind)
	}
	if ch.ToolCall == nil {
		t.Fatal("ToolCall is nil")
	}
	if ch.ToolCall.Name != "bash" {
		t.Errorf("ToolCall.Name: got %q, want bash", ch.ToolCall.Name)
	}
	if ch.ToolCall.Args["cmd"] != "ls" {
		t.Errorf("ToolCall.Args[cmd]: got %v, want ls", ch.ToolCall.Args["cmd"])
	}
}

func TestTranslatePlan(t *testing.T) {
	t.Parallel()
	u := sessionUpdateParams{Type: "plan", Content: "plan content"}
	ch := translateUpdate(u)
	if ch.Kind != canonical.ChunkKindPlan {
		t.Fatalf("kind: got %v, want ChunkKindPlan", ch.Kind)
	}
	if ch.Plan == nil || ch.Plan.Content != "plan content" {
		t.Errorf("Plan.Content: got %v, want plan content", ch.Plan)
	}
}

func TestTranslateUnknown(t *testing.T) {
	t.Parallel()
	u := sessionUpdateParams{Type: "unknown_type", Content: "data"}
	ch := translateUpdate(u)
	// Unknown types fall back to text to avoid data loss.
	if ch.Kind != canonical.ChunkKindText {
		t.Errorf("kind: got %v, want ChunkKindText (fallback for unknown)", ch.Kind)
	}
	if ch.Text == nil || ch.Text.Content != "data" {
		t.Errorf("Text.Content fallback: got %v, want data", ch.Text)
	}
}

// TestParseStopReason_MappingTable locks the wire-string → canonical.StopReason
// mapping per D-02. Unknown/empty values map to StopUnknown (forward-compat).
func TestParseStopReason_MappingTable(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name string
		in   string
		want canonical.StopReason
	}{
		{"end_turn", "end_turn", canonical.StopEndTurn},
		{"max_tokens", "max_tokens", canonical.StopMaxTokens},
		{"max_turn_requests", "max_turn_requests", canonical.StopMaxTurnRequests},
		{"refusal", "refusal", canonical.StopRefusal},
		{"cancelled", "cancelled", canonical.StopCancelled},
		{"empty_string_maps_to_unknown", "", canonical.StopUnknown},
		{"arbitrary_unknown_maps_to_unknown", "banana", canonical.StopUnknown},
	}
	for _, r := range rows {
		r := r
		t.Run(r.name, func(t *testing.T) {
			t.Parallel()
			got := parseStopReason(r.in)
			if got != r.want {
				t.Errorf("parseStopReason(%q): got %v, want %v", r.in, got, r.want)
			}
		})
	}
}

// TestTranslateBlock_TextUsesTextField confirms D-14: the text-variant wire
// frame uses field "text" (not the Phase 1 "content"). Marshals the produced
// wireBlock and checks the rendered JSON contains "text":"hello" — protects
// against accidental field-name regressions even if a future struct change
// keeps the Go field name but swaps the json tag.
func TestTranslateBlock_TextUsesTextField(t *testing.T) {
	t.Parallel()
	block := canonical.Block{
		Kind: canonical.BlockKindText,
		Text: &canonical.TextBlock{Content: "hello"},
	}
	wb := translateBlock(block)
	if wb.Type != "text" {
		t.Errorf("Type: got %q, want %q", wb.Type, "text")
	}
	if wb.Text != "hello" {
		t.Errorf("Text: got %q, want %q", wb.Text, "hello")
	}
	data, err := json.Marshal(wb)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !strings.Contains(string(data), `"text":"hello"`) {
		t.Errorf("marshaled wire frame missing \"text\":\"hello\": %s", string(data))
	}
}

// TestTranslateBlock_ResourceLinkNameFallback locks the D-04 contract: when
// canonical.ResourceLinkBlock.Name is empty, translateBlock derives a non-empty
// wire Name via path.Base on the URI's parsed Path. Unparseable URIs stay empty
// without panic.
func TestTranslateBlock_ResourceLinkNameFallback(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name string
		in   canonical.Block
		want string
	}{
		{
			name: "explicit_name_is_passed_through",
			in: canonical.Block{
				Kind: canonical.BlockKindResourceLink,
				ResourceLink: &canonical.ResourceLinkBlock{
					URI:  "file:///foo/bar.txt",
					Name: "explicit.txt",
				},
			},
			want: "explicit.txt",
		},
		{
			name: "empty_name_with_file_uri_uses_path_base",
			in: canonical.Block{
				Kind: canonical.BlockKindResourceLink,
				ResourceLink: &canonical.ResourceLinkBlock{
					URI: "file:///foo/bar.txt",
				},
			},
			want: "bar.txt",
		},
		{
			name: "empty_name_with_unparseable_uri_stays_empty",
			in: canonical.Block{
				Kind: canonical.BlockKindResourceLink,
				ResourceLink: &canonical.ResourceLinkBlock{
					URI: "://not a url::",
				},
			},
			want: "",
		},
	}
	for _, r := range rows {
		r := r
		t.Run(r.name, func(t *testing.T) {
			t.Parallel()
			wb := translateBlock(r.in)
			if wb.Type != "resource_link" {
				t.Errorf("Type: got %q, want %q", wb.Type, "resource_link")
			}
			if wb.Name != r.want {
				t.Errorf("Name: got %q, want %q", wb.Name, r.want)
			}
		})
	}
}
