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
	"bytes"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"otto-gateway/internal/canonical"
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
			// Phase 6 D-03 part 1: tool_call notifications now promote to
			// real ChunkKindToolCall with populated ToolCall.ID/Name/Args
			// (was ChunkKindThought + [tool: <title>]\n text in Phase 1.1).
			// The [tool: <name>]\n narration is now produced by
			// engine.Collect (non-streaming) and per-surface emitters
			// (streaming) — not by the translator.
			name:       "tool_call_wrapped_snake_with_title_and_args",
			paramsJSON: `{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"tc_1","title":"read_file","args":{"path":"/etc/hosts"}}}`,
			want: canonical.Chunk{
				Kind: canonical.ChunkKindToolCall,
				ToolCall: &canonical.ToolCallChunk{
					ID:   "tc_1",
					Name: "read_file",
					Args: map[string]any{"path": "/etc/hosts"},
				},
			},
		},
		{
			// Phase 6 D-03 part 1: tool_call_chunk discriminator routes
			// identically to tool_call (Pitfall 8 — kiro emits atomically,
			// no aggregation needed).
			name:       "tool_call_chunk_wrapped_snake_with_title_and_args",
			paramsJSON: `{"sessionId":"s1","update":{"sessionUpdate":"tool_call_chunk","toolCallId":"tc_2","title":"write","args":{"file":"out.txt","data":"hi"}}}`,
			want: canonical.Chunk{
				Kind: canonical.ChunkKindToolCall,
				ToolCall: &canonical.ToolCallChunk{
					ID:   "tc_2",
					Name: "write",
					Args: map[string]any{"file": "out.txt", "data": "hi"},
				},
			},
		},
		{
			// Phase 6 D-03 part 1: empty title still falls back to "unknown"
			// per the firstNonEmpty discipline preserved from Phase 1.1.
			// Args may be nil — passes through as nil map.
			name:       "tool_call_without_title_falls_back_to_unknown_with_nil_args",
			paramsJSON: `{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"tc_empty"}}`,
			want: canonical.Chunk{
				Kind: canonical.ChunkKindToolCall,
				ToolCall: &canonical.ToolCallChunk{
					ID:   "tc_empty",
					Name: "unknown",
					Args: nil,
				},
			},
		},
		{
			// Phase 6 D-03 part 1: tool_call_chunk with empty title and nil
			// args also routes through the firstNonEmpty fallback.
			name:       "tool_call_chunk_empty_title_nil_args",
			paramsJSON: `{"sessionId":"s1","update":{"sessionUpdate":"tool_call_chunk"}}`,
			want: canonical.Chunk{
				Kind: canonical.ChunkKindToolCall,
				ToolCall: &canonical.ToolCallChunk{
					ID:   "",
					Name: "unknown",
					Args: nil,
				},
			},
		},
		{
			// Real kiro wire shape: emits args under `rawInput` (not the
			// spec field `args`) and the canonical tool name under `kind`
			// (the spec field `title` is repurposed as a user-facing
			// status string that mutates per chunk — see the next case).
			// The translator MUST prefer kind/rawInput when present so
			// the canonical chunk carries the real args + stable name.
			name:       "tool_call_kiro_wire_kind_and_rawInput",
			paramsJSON: `{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"tooluse_abc","title":"Reading CLAUDE.md:1","kind":"read","locations":[{"path":"/tmp/CLAUDE.md"}],"rawInput":{"operations":[{"mode":"Line","path":"/tmp/CLAUDE.md"}],"__tool_use_purpose":"Read CLAUDE.md to find # Project"}}}`,
			want: canonical.Chunk{
				Kind: canonical.ChunkKindToolCall,
				ToolCall: &canonical.ToolCallChunk{
					ID:   "tooluse_abc",
					Name: "read", // kind wins over title
					Args: map[string]any{
						"operations": []any{
							map[string]any{"mode": "Line", "path": "/tmp/CLAUDE.md"},
						},
						"__tool_use_purpose": "Read CLAUDE.md to find # Project",
					},
				},
			},
		},
		{
			// Spec compliance: when ONLY the spec field `args` is set
			// (no kiro `rawInput`), the translator reads from `args`.
			// Equivalent to the Node-reference fixture above; this case
			// pins the fallback path explicitly so a future refactor
			// can't silently invert the priority.
			name:       "tool_call_spec_args_only_fallback",
			paramsJSON: `{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"tc_spec","title":"read_file","args":{"path":"/etc/hosts"}}}`,
			want: canonical.Chunk{
				Kind: canonical.ChunkKindToolCall,
				ToolCall: &canonical.ToolCallChunk{
					ID:   "tc_spec",
					Name: "read_file", // title used since no kind
					Args: map[string]any{"path": "/etc/hosts"},
				},
			},
		},
		{
			// Precedence: when BOTH `args` and `rawInput` are present
			// the spec field `args` wins (more explicit, documented).
			// Defensive: if a future kiro version emits both,
			// the translator stays deterministic on the spec field.
			name:       "tool_call_args_wins_over_rawInput_when_both_set",
			paramsJSON: `{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"tc_both","kind":"read","title":"read","args":{"path":"/from-args"},"rawInput":{"path":"/from-rawInput"}}}`,
			want: canonical.Chunk{
				Kind: canonical.ChunkKindToolCall,
				ToolCall: &canonical.ToolCallChunk{
					ID:   "tc_both",
					Name: "read",
					Args: map[string]any{"path": "/from-args"},
				},
			},
		},
		{
			// Placeholder-then-populated chunk pattern: kiro emits an
			// announcement (tool_call_chunk, no rawInput) followed by
			// the payload (tool_call, populated rawInput). The two
			// translate calls produce two ChunkKindToolCall chunks with
			// the same ID — the first with nil Args, the second with
			// populated Args. The anthropic SSE adapter discriminates
			// these via pendingToolUseFlush (sse.go) — translator's
			// responsibility is to faithfully report what's on the wire.
			// Verifies the announcement chunk does NOT synthesize fake
			// args; it correctly leaves Args nil.
			name:       "tool_call_chunk_kiro_announcement_no_args",
			paramsJSON: `{"sessionId":"s1","update":{"sessionUpdate":"tool_call_chunk","toolCallId":"tooluse_xyz","title":"read","kind":"read"}}`,
			want: canonical.Chunk{
				Kind: canonical.ChunkKindToolCall,
				ToolCall: &canonical.ToolCallChunk{
					ID:   "tooluse_xyz",
					Name: "read",
					Args: nil,
				},
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
			// Phase 6 D-03 part 1 (replaces Phase 1.1 thought-text): tool_call
			// with no title/id/args still produces a real ChunkKindToolCall
			// with Name == "unknown" (firstNonEmpty fallback preserved).
			name:       "tool_call_without_title_renders_unknown_name_on_real_toolcall_chunk",
			paramsJSON: `{"sessionId":"s1","update":{"sessionUpdate":"tool_call"}}`,
			want: canonical.Chunk{
				Kind: canonical.ChunkKindToolCall,
				ToolCall: &canonical.ToolCallChunk{
					ID:   "",
					Name: "unknown",
					Args: nil,
				},
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
			got, ok := translateUpdate(nil, parsed)
			if !ok {
				t.Fatalf("translateUpdate returned ok=false; want a valid chunk for params: %s", r.paramsJSON)
			}
			if !reflect.DeepEqual(got, r.want) {
				t.Errorf("translateUpdate mismatch\n  got:  %+v (Text=%+v, Thought=%+v, Plan=%+v)\n  want: %+v (Text=%+v, Thought=%+v, Plan=%+v)",
					got, got.Text, got.Thought, got.Plan,
					r.want, r.want.Text, r.want.Thought, r.want.Plan)
			}
		})
	}
}

// TestTranslateUpdate_InnerUnmarshalFailure_DropsNotification locks the WR-05
// contract: when the wrapped form arrives but params.update is malformed JSON
// (e.g., a server bug or wire corruption), translateUpdate returns ok=false
// so the caller drops the notification rather than pushing a phantom empty
// chunk. The pre-fix code silently swallowed the inner-unmarshal error and
// emitted ChunkKindText with empty content, which is indistinguishable from
// a real empty message — invisible failure mode.
func TestTranslateUpdate_InnerUnmarshalFailure_DropsNotification(t *testing.T) {
	t.Parallel()
	// Outer params parse cleanly because Update is json.RawMessage. The
	// inner re-unmarshal into sessionUpdateBody fails because the JSON
	// is not an object.
	parsed := sessionUpdateParams{
		SessionID: "s1",
		Update:    json.RawMessage(`"not an object"`),
	}
	chunk, ok := translateUpdate(nil, parsed)
	if ok {
		t.Errorf("translateUpdate(malformed inner update): ok=true, want false (got chunk %+v)", chunk)
	}
}

// TestTranslateUpdate_UnknownDiscriminator_LogsDebug locks the observability
// fix: a non-empty discriminator the switch does not recognize is surfaced as
// text (data-loss avoidance) AND logged at Debug, so a new kiro chunk type
// (e.g. a future reasoning discriminator) leaves a breadcrumb instead of
// silently rendering as answer content. An empty discriminator (a body.text-only
// notification) must NOT log — that is the expected text-carrying shape.
func TestTranslateUpdate_UnknownDiscriminator_LogsDebug(t *testing.T) {
	t.Parallel()

	newLogger := func(buf *bytes.Buffer) *slog.Logger {
		return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}

	t.Run("unknown_discriminator_logs", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		parsed := sessionUpdateParams{
			Update: json.RawMessage(`{"sessionUpdate":"agent_reasoning_chunk","content":{"type":"text","text":"thinking..."}}`),
		}
		got, ok := translateUpdate(newLogger(&buf), parsed)
		if !ok || got.Kind != canonical.ChunkKindText {
			t.Fatalf("want ok text chunk, got ok=%v kind=%v", ok, got.Kind)
		}
		if got.Text == nil || got.Text.Content != "thinking..." {
			t.Errorf("content lost on fallback: %+v", got.Text)
		}
		out := buf.String()
		if !strings.Contains(out, "unrecognized session/update discriminator") ||
			!strings.Contains(out, "agent_reasoning_chunk") {
			t.Errorf("expected Debug log naming the discriminator; got: %q", out)
		}
	})

	t.Run("empty_discriminator_does_not_log", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		parsed := sessionUpdateParams{
			Update: json.RawMessage(`{"text":"bare text"}`),
		}
		got, ok := translateUpdate(newLogger(&buf), parsed)
		if !ok || got.Text == nil || got.Text.Content != "bare text" {
			t.Fatalf("want bare text surfaced; got ok=%v text=%+v", ok, got.Text)
		}
		if buf.Len() != 0 {
			t.Errorf("empty discriminator should not log; got: %q", buf.String())
		}
	})
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
