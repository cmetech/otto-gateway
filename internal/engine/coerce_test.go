// Package engine — CoerceToolCall property tests (TRST-06 + D-12) +
// table-driven D-09/D-10 algorithm cases + Example function (TRST-07).
//
// CoerceToolCall is the Ollama/OpenAI producer of Message.ToolCalls
// (per-surface contract — see Phase 6 D-03/D-05/D-07 + collect.go's
// commentary). Anthropic does NOT call it; Anthropic populates
// Message.ToolCalls via its adapter-local Collect (06-04 Option A1).
package engine

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"testing/quick"

	"otto-gateway/internal/canonical"
)

// makeResp builds a *canonical.ChatResponse with a single ContentKindText
// part holding `text`. Helper for the table-driven cases.
func makeResp(text string) *canonical.ChatResponse {
	return &canonical.ChatResponse{
		Message: canonical.Message{
			Role:    canonical.RoleAssistant,
			Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: text}},
		},
	}
}

// makeReq builds a *canonical.ChatRequest with the supplied tools.
func makeReq(tools ...canonical.ToolSpec) *canonical.ChatRequest {
	return &canonical.ChatRequest{Tools: tools}
}

// weatherTool is a small reusable tool spec for table tests.
func weatherTool() canonical.ToolSpec {
	return canonical.ToolSpec{
		Name:        "get_weather",
		Description: "Get current weather",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"location": map[string]any{"type": "string"},
				"units":    map[string]any{"type": "string"},
			},
			"required": []any{"location"},
		},
	}
}

func fileTool() canonical.ToolSpec {
	return canonical.ToolSpec{
		Name:        "read_file",
		Description: "Read a file",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
			"required": []any{"path"},
		},
	}
}

// TestCoerceToolCall_NeverPanics (TRST-06 + D-12) — CoerceToolCall MUST
// NOT panic for any input shape, including nil pointers, malformed JSON,
// and pathological tool specs. testing/quick generates 1000 random
// (text, toolNames) pairs.
func TestCoerceToolCall_NeverPanics(t *testing.T) {
	property := func(text string, toolNames []string, extraJunk string) bool {
		tools := make([]canonical.ToolSpec, 0, len(toolNames))
		for _, n := range toolNames {
			tools = append(tools, canonical.ToolSpec{
				Name: n,
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"x": map[string]any{"type": "string"},
					},
				},
			})
		}
		req := &canonical.ChatRequest{Tools: tools}
		resp := makeResp(text + extraJunk)
		_ = CoerceToolCall(req, resp)
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 1000}); err != nil {
		t.Errorf("CoerceToolCall property check failed: %v", err)
	}

	// Defensive nil-input guard.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("CoerceToolCall panicked on nil inputs: %v", r)
		}
	}()
	_ = CoerceToolCall(nil, nil)
	_ = CoerceToolCall(nil, makeResp("text"))
	_ = CoerceToolCall(makeReq(), nil)
}

// TestCoerceToolCall_Idempotent (D-02 + D-12) — running CoerceToolCall
// twice on the same response is the same as running it once. The first
// call may mutate; the second is a no-op (Message.ToolCalls is now
// non-empty, which short-circuits per D-09 Step 1).
func TestCoerceToolCall_Idempotent(t *testing.T) {
	property := func(text string) bool {
		req := makeReq(weatherTool(), fileTool())
		resp := makeResp(text)
		_ = CoerceToolCall(req, resp) // first call may mutate; we snapshot after.
		snapshotContent := append([]canonical.ContentPart(nil), resp.Message.Content...)
		snapshotToolCalls := append([]canonical.ToolCall(nil), resp.Message.ToolCalls...)
		// Second invocation: must be a no-op (returns false because
		// either nothing was coerced the first time, or ToolCalls is now
		// non-empty).
		if second := CoerceToolCall(req, resp); second && len(snapshotToolCalls) > 0 {
			// Idempotency violation: ToolCalls were already populated
			// (first call fired) and second still returned true.
			t.Logf("idempotency violated for text %q: first fired then second also fired", text)
			return false
		}
		// Whether or not coerce fired, the response state must be
		// identical to the snapshot taken immediately after the first
		// call.
		if !reflect.DeepEqual(resp.Message.Content, snapshotContent) {
			t.Logf("Content mutated by second call for text %q", text)
			return false
		}
		if !reflect.DeepEqual(resp.Message.ToolCalls, snapshotToolCalls) {
			t.Logf("ToolCalls mutated by second call for text %q", text)
			return false
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 1000}); err != nil {
		t.Errorf("CoerceToolCall idempotency check failed: %v", err)
	}
}

// TestCoerceToolCall_NoMatchNoMutation (D-12) — when pickBestTool scores
// zero (parsed JSON has no overlap with any tool's properties), the
// response is bit-identical to the input snapshot. Compare both
// Content and ToolCalls.
func TestCoerceToolCall_NoMatchNoMutation(t *testing.T) {
	// JSON whose keys do not appear in any tool's properties.
	cases := []string{
		`{"foo":"bar"}`,
		`{"completely":"unrelated","keys":"here"}`,
		"```json\n{\"alpha\":\"beta\"}\n```",
	}
	for _, text := range cases {
		text := text
		t.Run(text, func(t *testing.T) {
			req := makeReq(weatherTool(), fileTool())
			resp := makeResp(text)
			snapshotContent := append([]canonical.ContentPart(nil), resp.Message.Content...)
			snapshotToolCalls := append([]canonical.ToolCall(nil), resp.Message.ToolCalls...)
			fired := CoerceToolCall(req, resp)
			if fired {
				t.Errorf("CoerceToolCall fired on no-match text %q (got Message.ToolCalls=%+v)", text, resp.Message.ToolCalls)
			}
			if !reflect.DeepEqual(resp.Message.Content, snapshotContent) {
				t.Errorf("Content mutated despite no-match for %q\n got: %+v\nwant: %+v",
					text, resp.Message.Content, snapshotContent)
			}
			if !reflect.DeepEqual(resp.Message.ToolCalls, snapshotToolCalls) {
				t.Errorf("ToolCalls mutated despite no-match for %q\n got: %+v\nwant: %+v",
					text, resp.Message.ToolCalls, snapshotToolCalls)
			}
		})
	}
}

// TestCoerceToolCall_TieBreaker (D-10) — two tools with identical
// property keys, parsed JSON matches both equally: first-declared in
// req.Tools order wins. Deterministic across 1000 runs.
func TestCoerceToolCall_TieBreaker(t *testing.T) {
	first := canonical.ToolSpec{
		Name: "first_tool",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"location": map[string]any{"type": "string"},
			},
		},
	}
	second := canonical.ToolSpec{
		Name: "second_tool",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"location": map[string]any{"type": "string"},
			},
		},
	}
	for i := 0; i < 1000; i++ {
		req := makeReq(first, second)
		resp := makeResp(`{"location":"Boston"}`)
		if !CoerceToolCall(req, resp) {
			t.Fatalf("iteration %d: expected coerce to fire", i)
		}
		if len(resp.Message.ToolCalls) != 1 {
			t.Fatalf("iteration %d: expected exactly 1 tool_call, got %d", i, len(resp.Message.ToolCalls))
		}
		if resp.Message.ToolCalls[0].Name != "first_tool" {
			t.Fatalf("iteration %d: tie-break violated; got %q, want %q",
				i, resp.Message.ToolCalls[0].Name, "first_tool")
		}
	}
}

// TestCoerceToolCall_AlgorithmCases walks the D-09 9-step matrix plus
// the D-10 edge cases plus the iteration-3 kiro-native narration case
// and the REVIEW LOW inline-fenced-JSON-in-prose case.
func TestCoerceToolCall_AlgorithmCases(t *testing.T) {
	type row struct {
		name             string
		text             string
		tools            []canonical.ToolSpec
		preExistingCalls []canonical.ToolCall
		wantFired        bool
		wantToolName     string // when wantFired==true, the synthesized ToolCall name
	}
	rows := []row{
		// D-09 Step 3 — raw JSON parse success.
		{
			name:         "raw_json_matches_get_weather",
			text:         `{"location":"Boston"}`,
			tools:        []canonical.ToolSpec{weatherTool()},
			wantFired:    true,
			wantToolName: "get_weather",
		},
		// D-09 Step 4 — fenced ```json``` parse success.
		{
			name:         "fenced_json_matches_get_weather",
			text:         "```json\n{\"location\":\"NYC\"}\n```",
			tools:        []canonical.ToolSpec{weatherTool()},
			wantFired:    true,
			wantToolName: "get_weather",
		},
		// D-09 Step 4 — bare ``` ``` fence parse success.
		{
			name:         "bare_fence_matches_get_weather",
			text:         "```\n{\"location\":\"LA\"}\n```",
			tools:        []canonical.ToolSpec{weatherTool()},
			wantFired:    true,
			wantToolName: "get_weather",
		},
		// REVIEW LOW addition — inline fenced JSON inside prose MUST NOT
		// coerce. The fence is not at start AND end of trimmed text;
		// Pitfall 3 "entire text" requirement.
		{
			name:      "inline_fenced_json_in_prose_no_coerce",
			text:      "Here's the result: ```json\n{\"location\":\"Paris\"}\n```\nDone.",
			tools:     []canonical.ToolSpec{weatherTool()},
			wantFired: false,
		},
		// Iteration-3 addition — kiro-native narration text from Task 2's
		// engine.Collect aggregator MUST NOT coerce (not JSON-shaped,
		// no fence). Locks the kiro-native + Ollama/OpenAI non-streaming
		// interaction.
		{
			name:      "kiro_native_narration_text_no_coerce",
			text:      "[tool: get_weather]\n",
			tools:     []canonical.ToolSpec{weatherTool()},
			wantFired: false,
		},
		// D-09 Step 5 — parse failure (no JSON, no fence) → no-coerce.
		{
			name:      "plain_prose_no_coerce",
			text:      "I will check the weather for you.",
			tools:     []canonical.ToolSpec{weatherTool()},
			wantFired: false,
		},
		// D-09 Step 5 — truncated/invalid JSON → no-coerce.
		{
			name:      "truncated_json_no_coerce",
			text:      `{"location":"Bos`,
			tools:     []canonical.ToolSpec{weatherTool()},
			wantFired: false,
		},
		// D-10 — parsed value is an array → no-coerce.
		{
			name:      "json_array_no_coerce",
			text:      `["Boston","NYC"]`,
			tools:     []canonical.ToolSpec{weatherTool()},
			wantFired: false,
		},
		// D-10 — parsed value is a scalar → no-coerce.
		{
			name:      "json_scalar_no_coerce",
			text:      `"just a string"`,
			tools:     []canonical.ToolSpec{weatherTool()},
			wantFired: false,
		},
		// D-10 — parsed value is null → no-coerce.
		{
			name:      "json_null_no_coerce",
			text:      `null`,
			tools:     []canonical.ToolSpec{weatherTool()},
			wantFired: false,
		},
		// D-10 — empty {} parses to zero overlap → no-coerce.
		{
			name:      "empty_object_no_coerce",
			text:      `{}`,
			tools:     []canonical.ToolSpec{weatherTool()},
			wantFired: false,
		},
		// D-10 — tool with empty/missing parameters.properties skipped.
		{
			name: "tool_with_no_properties_skipped",
			text: `{"location":"Boston"}`,
			tools: []canonical.ToolSpec{
				{Name: "empty_spec_tool"}, // no Parameters at all
				weatherTool(),              // this one matches
			},
			wantFired:    true,
			wantToolName: "get_weather",
		},
		// D-09 Step 1 — empty tools[] → no-coerce.
		{
			name:      "empty_tools_no_coerce",
			text:      `{"location":"Boston"}`,
			tools:     nil,
			wantFired: false,
		},
		// D-09 Step 1 / D-02 — resp.Message.ToolCalls already populated → no-coerce (idempotency).
		{
			name:             "existing_tool_calls_no_coerce",
			text:             `{"location":"Boston"}`,
			tools:            []canonical.ToolSpec{weatherTool()},
			preExistingCalls: []canonical.ToolCall{{ID: "tc_pre", Name: "preexisting", Arguments: map[string]any{}}},
			wantFired:        false,
		},
	}

	for _, r := range rows {
		r := r
		t.Run(r.name, func(t *testing.T) {
			req := &canonical.ChatRequest{Tools: r.tools}
			resp := makeResp(r.text)
			if r.preExistingCalls != nil {
				resp.Message.ToolCalls = r.preExistingCalls
			}
			gotFired := CoerceToolCall(req, resp)
			if gotFired != r.wantFired {
				t.Errorf("fired: got %v, want %v (text=%q tools=%v)",
					gotFired, r.wantFired, r.text, toolNames(r.tools))
			}
			if r.wantFired {
				if len(resp.Message.ToolCalls) != 1 {
					t.Fatalf("expected 1 tool_call, got %d (text=%q)", len(resp.Message.ToolCalls), r.text)
				}
				if resp.Message.ToolCalls[0].Name != r.wantToolName {
					t.Errorf("synthesized tool name: got %q, want %q", resp.Message.ToolCalls[0].Name, r.wantToolName)
				}
				if !strings.HasPrefix(resp.Message.ToolCalls[0].ID, "call_") {
					t.Errorf("synthesized ID format: got %q, want prefix 'call_' (D-11)", resp.Message.ToolCalls[0].ID)
				}
				if resp.Message.Content[0].Text != "" {
					t.Errorf("on coerce, Content[0].Text must be cleared; got %q", resp.Message.Content[0].Text)
				}
			} else {
				// no mutation: when no pre-existing calls were seeded,
				// no calls should appear post-no-coerce. When pre-existing
				// calls WERE seeded, they must still be the only entries
				// (Step 1 short-circuit preserves prior state verbatim).
				if r.preExistingCalls == nil && len(resp.Message.ToolCalls) != 0 {
					t.Errorf("no-coerce path leaked tool_calls: %+v", resp.Message.ToolCalls)
				}
				if r.preExistingCalls != nil && !reflect.DeepEqual(resp.Message.ToolCalls, r.preExistingCalls) {
					t.Errorf("no-coerce path mutated pre-existing tool_calls:\n got: %+v\nwant: %+v",
						resp.Message.ToolCalls, r.preExistingCalls)
				}
				if resp.Message.Content[0].Text != r.text {
					t.Errorf("no-coerce path mutated text: got %q, want %q", resp.Message.Content[0].Text, r.text)
				}
			}
		})
	}
}

func toolNames(tools []canonical.ToolSpec) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}

// ExampleCoerceToolCall is a runnable godoc example (TRST-07). The
// Output block is validated by `go test -run Example`. No suffix
// (CoerceToolCall is exported, so the Go test framework expects the
// function name to be ExampleCoerceToolCall — underscore suffix is
// reserved for examples on unexported names).
func ExampleCoerceToolCall() {
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
	}
	resp := &canonical.ChatResponse{
		Message: canonical.Message{
			Role: canonical.RoleAssistant,
			Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: `{"location":"Boston"}`},
			},
		},
	}
	fired := CoerceToolCall(req, resp)
	fmt.Println("fired:", fired)
	fmt.Println("name:", resp.Message.ToolCalls[0].Name)
	fmt.Println("location:", resp.Message.ToolCalls[0].Arguments["location"])
	fmt.Println("text:", resp.Message.Content[0].Text)
	// Output:
	// fired: true
	// name: get_weather
	// location: Boston
	// text:
}
