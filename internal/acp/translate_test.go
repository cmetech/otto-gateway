// Whitebox unit tests for translate.go (package acp).
// D-25: whitebox layout unchanged from Phase 1.
//
// Phase 1.1 Plan 04 rewrites the legacy per-variant TestTranslate* tests into
// a table-driven TestTranslateUpdate_VarianceMatrix (D-22) plus the new
// TestNormalizeUpdateType (D-19). The Plan 03 TestTranslateBlock_* and
// TestParseStopReason_MappingTable tests are preserved verbatim — they exercise
// translateBlock and parseStopReason, not translateUpdate.
package acp

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"loop24-gateway/internal/canonical"
)

// TestNormalizeUpdateType locks the D-19 contract: CamelCase → snake_case,
// already-snake → lowercased, mixed-Camel-with-underscores → lowercased,
// empty → empty.
func TestNormalizeUpdateType(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"snake_already", "agent_message_chunk", "agent_message_chunk"},
		{"camel_two_word", "AgentMessageChunk", "agent_message_chunk"},
		{"camel_three_word", "AgentThoughtChunk", "agent_thought_chunk"},
		{"snake_with_two_words", "tool_call", "tool_call"},
		{"camel_three_word_alt", "ToolCallUpdate", "tool_call_update"},
		{"single_camel_word", "Plan", "plan"},
		{"mixed_camel_and_underscore", "Agent_Message_Chunk", "agent_message_chunk"},
		{"already_lower_single_word", "plan", "plan"},
	}
	for _, r := range rows {
		r := r
		t.Run(r.name, func(t *testing.T) {
			t.Parallel()
			got := normalizeUpdateType(r.in)
			if got != r.want {
				t.Errorf("normalizeUpdateType(%q): got %q, want %q", r.in, got, r.want)
			}
		})
	}
}

// TestTranslateUpdate_VarianceMatrix walks a representative sample of the
// D-22 cross-product: (body wrap) x (discriminator field) x (discriminator
// casing) x (content shape) x (update type). Each row supplies a JSON
// payload representing the `params` of a session/update notification AS IT
// ARRIVES from the wire, then unmarshals it into sessionUpdateParams and
// passes the result through translateUpdate. The expected canonical.Chunk
// is compared via reflect.DeepEqual.
//
// Method-name dispatch (D-16) is exercised end-to-end in Task 4's
// TestIntegration_FakeACP_E2E_MixedVariants — translateUpdate sees the
// already-parsed body, so the method-name axis collapses here.
func TestTranslateUpdate_VarianceMatrix(t *testing.T) {
	t.Parallel()

	rows := []struct {
		name       string
		paramsJSON string
		want       canonical.Chunk
	}{
		{
			name:       "agent_message_chunk_wrapped_snake_content_obj_text",
			paramsJSON: `{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hello"}}}`,
			want: canonical.Chunk{
				Kind: canonical.ChunkKindText,
				Text: &canonical.TextChunk{Content: "hello"},
			},
		},
		{
			name:       "agent_message_chunk_flat_snake_content_string",
			paramsJSON: `{"sessionId":"s1","sessionUpdate":"agent_message_chunk","content":"hello"}`,
			want: canonical.Chunk{
				Kind: canonical.ChunkKindText,
				Text: &canonical.TextChunk{Content: "hello"},
			},
		},
		{
			name:       "agent_message_chunk_flat_type_field_body_text",
			paramsJSON: `{"sessionId":"s1","type":"agent_message_chunk","text":"hello"}`,
			want: canonical.Chunk{
				Kind: canonical.ChunkKindText,
				Text: &canonical.TextChunk{Content: "hello"},
			},
		},
		{
			name:       "agent_message_chunk_wrapped_camel_content_obj_text",
			paramsJSON: `{"sessionId":"s1","update":{"sessionUpdate":"AgentMessageChunk","content":{"type":"text","text":"hi"}}}`,
			want: canonical.Chunk{
				Kind: canonical.ChunkKindText,
				Text: &canonical.TextChunk{Content: "hi"},
			},
		},
		{
			name:       "agent_thought_chunk_wrapped_snake_content_obj_text",
			paramsJSON: `{"sessionId":"s1","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"thinking"}}}`,
			want: canonical.Chunk{
				Kind:    canonical.ChunkKindThought,
				Thought: &canonical.ThoughtChunk{Content: "thinking"},
			},
		},
		{
			name:       "agent_thought_chunk_camel_alias",
			paramsJSON: `{"sessionId":"s1","update":{"sessionUpdate":"AgentThoughtChunk","content":{"type":"text","text":"thinking"}}}`,
			want: canonical.Chunk{
				Kind:    canonical.ChunkKindThought,
				Thought: &canonical.ThoughtChunk{Content: "thinking"},
			},
		},
		{
			name:       "tool_call_wrapped_snake_with_title",
			paramsJSON: `{"sessionId":"s1","update":{"sessionUpdate":"tool_call","title":"read_file"}}`,
			want: canonical.Chunk{
				Kind:    canonical.ChunkKindThought,
				Thought: &canonical.ThoughtChunk{Content: "[tool: read_file]\n"},
			},
		},
		{
			name:       "tool_call_chunk_wrapped_snake_with_title",
			paramsJSON: `{"sessionId":"s1","update":{"sessionUpdate":"tool_call_chunk","title":"write"}}`,
			want: canonical.Chunk{
				Kind:    canonical.ChunkKindThought,
				Thought: &canonical.ThoughtChunk{Content: "[tool: write]\n"},
			},
		},
		{
			name:       "tool_call_update_wrapped_snake_output_wins",
			paramsJSON: `{"sessionId":"s1","update":{"sessionUpdate":"tool_call_update","output":"result data","content":{"type":"text","text":"alt result"}}}`,
			want: canonical.Chunk{
				Kind:    canonical.ChunkKindThought,
				Thought: &canonical.ThoughtChunk{Content: "result data"},
			},
		},
		{
			name:       "tool_call_update_wrapped_snake_no_output_uses_content_text",
			paramsJSON: `{"sessionId":"s1","update":{"sessionUpdate":"tool_call_update","content":{"type":"text","text":"alt result"}}}`,
			want: canonical.Chunk{
				Kind:    canonical.ChunkKindThought,
				Thought: &canonical.ThoughtChunk{Content: "alt result"},
			},
		},
		{
			name:       "plan_wrapped_snake_with_entries",
			paramsJSON: `{"sessionId":"s1","update":{"sessionUpdate":"plan","entries":[{"content":"Step 1"},{"content":"Step 2"}]}}`,
			want: canonical.Chunk{
				Kind: canonical.ChunkKindPlan,
				Plan: &canonical.PlanChunk{Content: "Step 1\nStep 2"},
			},
		},
		{
			name:       "plan_wrapped_snake_empty_entries",
			paramsJSON: `{"sessionId":"s1","update":{"sessionUpdate":"plan","entries":[]}}`,
			want: canonical.Chunk{
				Kind: canonical.ChunkKindPlan,
				Plan: &canonical.PlanChunk{Content: ""},
			},
		},
		{
			name:       "unknown_discriminator_falls_back_to_text_via_body_text",
			paramsJSON: `{"sessionId":"s1","sessionUpdate":"banana_chunk","text":"fallback text"}`,
			want: canonical.Chunk{
				Kind: canonical.ChunkKindText,
				Text: &canonical.TextChunk{Content: "fallback text"},
			},
		},
		{
			name:       "tool_call_without_title_renders_unknown",
			paramsJSON: `{"sessionId":"s1","update":{"sessionUpdate":"tool_call"}}`,
			want: canonical.Chunk{
				Kind:    canonical.ChunkKindThought,
				Thought: &canonical.ThoughtChunk{Content: "[tool: unknown]\n"},
			},
		},
		{
			// WR-04: when content is an object that explicitly carries
			// text:"" (empty), that's the load-bearing value — must NOT
			// fall through to body.Text. The pre-fix probe (Text string)
			// could not distinguish absent from present-but-empty, so the
			// "leaked" body.text from this row would have replaced the
			// (correct) empty content.text.
			name:       "empty_content_text_does_not_leak_body_text",
			paramsJSON: `{"sessionId":"s1","sessionUpdate":"agent_message_chunk","content":{"type":"text","text":""},"text":"leaked"}`,
			want: canonical.Chunk{
				Kind: canonical.ChunkKindText,
				Text: &canonical.TextChunk{Content: ""},
			},
		},
	}

	for _, r := range rows {
		r := r
		t.Run(r.name, func(t *testing.T) {
			t.Parallel()
			var parsed sessionUpdateParams
			if err := json.Unmarshal([]byte(r.paramsJSON), &parsed); err != nil {
				t.Fatalf("unmarshal params: %v\nJSON: %s", err, r.paramsJSON)
			}
			got := translateUpdate(parsed)
			if !reflect.DeepEqual(got, r.want) {
				t.Errorf("translateUpdate mismatch\n  got:  %+v (Text=%+v, Thought=%+v, Plan=%+v)\n  want: %+v (Text=%+v, Thought=%+v, Plan=%+v)",
					got, got.Text, got.Thought, got.Plan,
					r.want, r.want.Text, r.want.Thought, r.want.Plan)
			}
		})
	}
}

// TestParseStopReason_MappingTable locks the wire-string → canonical.StopReason
// mapping per D-02. Unknown/empty values map to StopUnknown (forward-compat).
// Preserved from Plan 03 verbatim — exercises parseStopReason, not translateUpdate.
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
// Preserved from Plan 03.
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
// Preserved from Plan 03.
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
