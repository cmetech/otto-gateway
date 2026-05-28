// Package engine — property tests for buildBlocks and CoerceToolCall.
// TRST-06 closure (Phase 9): every non-trivial canonical-to-wire
// translation has at least one round-trip property test and one
// adversarial never-panic test backing the golden cases.
//
// Why property tests beyond the goldens: goldens lock specific
// (input, output) pairs but miss the SHAPE invariants — "every
// non-empty user message produces a [User] section", "image emission
// never happens without an ImageBlock input", "CoerceToolCall is
// idempotent". A failing property test points at a class of bugs
// rather than a single regression.

package engine

import (
	"strings"
	"testing"

	"pgregory.net/rapid"

	"otto-gateway/internal/canonical"
)

// ─── Generators ─────────────────────────────────────────────────────────────

// genRole picks a uniform-random canonical role. Iota-defined; range
// is closed [RoleUser, RoleTool].
func genRole() *rapid.Generator[canonical.MessageRole] {
	return rapid.Custom(func(t *rapid.T) canonical.MessageRole {
		return canonical.MessageRole(rapid.IntRange(int(canonical.RoleUser), int(canonical.RoleTool)).Draw(t, "role"))
	})
}

// genTextPart yields a ContentPart with Kind=Text and a small random
// body. Empty strings are valid input — the production code must
// handle them without emitting a phantom section.
func genTextPart() *rapid.Generator[canonical.ContentPart] {
	return rapid.Custom(func(t *rapid.T) canonical.ContentPart {
		return canonical.ContentPart{
			Kind: canonical.ContentKindText,
			Text: rapid.StringN(0, 64, -1).Draw(t, "text"),
		}
	})
}

// genMessage yields a Message with a random role and 1-3 text parts.
// No image parts here — image generation is opt-in via the
// IncludeImages test to keep the no-image-blocks invariant testable.
func genMessage() *rapid.Generator[canonical.Message] {
	return rapid.Custom(func(t *rapid.T) canonical.Message {
		role := genRole().Draw(t, "role")
		parts := rapid.SliceOfN(genTextPart(), 1, 3).Draw(t, "parts")
		return canonical.Message{Role: role, Content: parts}
	})
}

// genRequest yields a ChatRequest with 0–6 messages, an optional
// System string, and Think/Format/Tools at their zero values. Tools
// are tested separately by TestBuildBlocks_ToolsCatalogShape — keeping
// them out here isolates the message-flattening invariants from the
// JSON-marshal side path.
func genRequest() *rapid.Generator[*canonical.ChatRequest] {
	return rapid.Custom(func(t *rapid.T) *canonical.ChatRequest {
		return &canonical.ChatRequest{
			System:   rapid.StringN(0, 32, -1).Draw(t, "system"),
			Messages: rapid.SliceOfN(genMessage(), 0, 6).Draw(t, "messages"),
		}
	})
}

// ─── buildBlocks invariants ─────────────────────────────────────────────────

// PropertyBuildBlocks_AlwaysReturnsAtLeastOneTextBlock locks the
// load-bearing invariant that adapters depend on: the FIRST element
// of the output slice is always a text block. Adapter code at
// internal/adapter/* indexes `blocks[0].Text` without a length check
// because buildBlocks owns the contract.
func TestProperty_BuildBlocks_AlwaysReturnsAtLeastOneTextBlock(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		req := genRequest().Draw(t, "req")
		blocks := buildBlocks(req)
		if len(blocks) < 1 {
			t.Fatalf("expected ≥1 block; got 0 for req=%+v", req)
		}
		if blocks[0].Kind != canonical.BlockKindText {
			t.Fatalf("expected first block to be Text; got %v for req=%+v", blocks[0].Kind, req)
		}
		if blocks[0].Text == nil {
			t.Fatalf("Text payload must be non-nil; got nil for req=%+v", req)
		}
	})
}

// PropertyBuildBlocks_NoImageBlocksWithoutImageInput locks the SC1
// invariant that image emission cannot drift — every BlockKindImage in
// the output must trace back to a ContentKindImage in the input.
// Generator deliberately omits image parts; output should be image-free.
func TestProperty_BuildBlocks_NoImageBlocksWithoutImageInput(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		req := genRequest().Draw(t, "req")
		blocks := buildBlocks(req)
		for i, b := range blocks {
			if b.Kind == canonical.BlockKindImage {
				t.Fatalf("blocks[%d] is Image but req had no ContentKindImage parts; req=%+v", i, req)
			}
		}
	})
}

// PropertyBuildBlocks_AllKindsRecognized verifies the output enum
// stays within the canonical-declared set. Catches the case where a
// future block kind is added to canonical but buildBlocks emits an
// unhandled discriminator.
func TestProperty_BuildBlocks_AllKindsRecognized(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		req := genRequest().Draw(t, "req")
		for i, b := range buildBlocks(req) {
			switch b.Kind {
			case canonical.BlockKindText, canonical.BlockKindImage:
				// ok
			default:
				t.Fatalf("blocks[%d] has unrecognized Kind %v", i, b.Kind)
			}
		}
	})
}

// PropertyBuildBlocks_Idempotent runs buildBlocks twice on the same
// input and confirms the outputs are structurally identical. Catches
// hidden state (package-level mutables, random tie-breakers) — none
// exist today, and this property test pins it.
func TestProperty_BuildBlocks_Idempotent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		req := genRequest().Draw(t, "req")
		first := buildBlocks(req)
		second := buildBlocks(req)
		if len(first) != len(second) {
			t.Fatalf("length differs: first=%d second=%d req=%+v", len(first), len(second), req)
		}
		for i := range first {
			if first[i].Kind != second[i].Kind {
				t.Fatalf("Kind differs at [%d]: first=%v second=%v", i, first[i].Kind, second[i].Kind)
			}
			if (first[i].Text == nil) != (second[i].Text == nil) {
				t.Fatalf("Text-nil-ness differs at [%d]", i)
			}
			if first[i].Text != nil && first[i].Text.Content != second[i].Text.Content {
				t.Fatalf("Text content differs at [%d]: first=%q second=%q", i, first[i].Text.Content, second[i].Text.Content)
			}
		}
	})
}

// PropertyBuildBlocks_NeverPanicsOnZeroValues covers the adversarial
// edge of the invariant matrix the rapid generator doesn't exercise —
// explicit nil request, empty-everything request, oversized strings.
// Each must return a valid non-nil slice without a panic.
func TestProperty_BuildBlocks_NeverPanicsOnZeroValues(t *testing.T) {
	cases := []*canonical.ChatRequest{
		nil,
		{},
		{System: ""},
		{Messages: nil},
		{Messages: []canonical.Message{}},
		// Empty content within a message:
		{Messages: []canonical.Message{{Role: canonical.RoleUser, Content: nil}}},
		// Oversized text — 100KB of 'x':
		{Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: strings.Repeat("x", 100*1024)},
			}},
		}},
		// All four roles present, each with empty text:
		{Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: ""}}},
			{Role: canonical.RoleSystem, Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: ""}}},
			{Role: canonical.RoleAssistant, Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: ""}}},
			{Role: canonical.RoleTool, Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: ""}}},
		}},
	}
	for i, req := range cases {
		i, req := i, req
		t.Run("", func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("case[%d] panicked: %v", i, r)
				}
			}()
			out := buildBlocks(req)
			if out == nil {
				t.Fatalf("case[%d]: got nil slice, want non-nil", i)
			}
			if len(out) < 1 {
				t.Fatalf("case[%d]: got 0-length slice, want ≥1 (text block contract)", i)
			}
		})
	}
}

// ─── CoerceToolCall invariants ──────────────────────────────────────────────

// genToolSpec yields a ToolSpec with a non-empty Name and an
// arbitrary Parameters map (small to keep iterations fast).
func genToolSpec() *rapid.Generator[canonical.ToolSpec] {
	return rapid.Custom(func(t *rapid.T) canonical.ToolSpec {
		return canonical.ToolSpec{
			Name:        rapid.StringN(1, 16, -1).Draw(t, "name"),
			Description: rapid.StringN(0, 32, -1).Draw(t, "desc"),
			Parameters:  map[string]any{"type": "object"},
		}
	})
}

// genResponseWithText yields a ChatResponse whose Message contains a
// single text content part. Coerce mutates this; the property test
// uses it as the not-yet-coerced baseline.
func genResponseWithText() *rapid.Generator[*canonical.ChatResponse] {
	return rapid.Custom(func(t *rapid.T) *canonical.ChatResponse {
		return &canonical.ChatResponse{
			Message: canonical.Message{
				Role: canonical.RoleAssistant,
				Content: []canonical.ContentPart{{
					Kind: canonical.ContentKindText,
					Text: rapid.StringN(0, 128, -1).Draw(t, "text"),
				}},
			},
		}
	})
}

// PropertyCoerceToolCall_Idempotent: the function mutates resp on a
// successful coercion (populates resp.Message.ToolCalls). A second
// call on the now-coerced resp must short-circuit (the early-return
// guard checks len(resp.Message.ToolCalls) > 0) and return false.
// This pins the "no double-coercion" invariant that the engine relies
// on at internal/engine/collect.go.
func TestProperty_CoerceToolCall_Idempotent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		req := &canonical.ChatRequest{
			Tools: rapid.SliceOfN(genToolSpec(), 1, 4).Draw(t, "tools"),
		}
		resp := genResponseWithText().Draw(t, "resp")

		// First call: outcome depends on whether the text happens to
		// parse as a tool-call JSON payload. Either way, the second
		// call must short-circuit if the first succeeded.
		_ = CoerceToolCall(req, resp)
		// If the first call populated ToolCalls, the second call must
		// return false (idempotency via the guard).
		if len(resp.Message.ToolCalls) > 0 {
			if CoerceToolCall(req, resp) {
				t.Fatalf("second CoerceToolCall returned true on already-coerced resp")
			}
		}
	})
}

// PropertyCoerceToolCall_NeverPanics covers the never-panic
// invariant across the full input matrix including nil pointers and
// pathological values. CoerceToolCall is invoked from a high-traffic
// path (every adapter response); a panic here is a denial-of-service
// surface for any client that hits it.
func TestProperty_CoerceToolCall_NeverPanics(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// 5 input branches the generator exercises:
		switch rapid.IntRange(0, 4).Draw(t, "branch") {
		case 0:
			// Both nil — must return false, must not panic.
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("nil-nil panicked: %v", r)
				}
			}()
			if CoerceToolCall(nil, nil) {
				t.Fatalf("nil-nil returned true")
			}
		case 1:
			// nil req, real resp.
			resp := genResponseWithText().Draw(t, "resp")
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("nil-req panicked: %v", r)
				}
			}()
			if CoerceToolCall(nil, resp) {
				t.Fatalf("nil-req returned true")
			}
		case 2:
			// real req, nil resp.
			req := &canonical.ChatRequest{Tools: []canonical.ToolSpec{{Name: "x"}}}
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("nil-resp panicked: %v", r)
				}
			}()
			if CoerceToolCall(req, nil) {
				t.Fatalf("nil-resp returned true")
			}
		case 3:
			// Empty tools + real resp.
			req := &canonical.ChatRequest{}
			resp := genResponseWithText().Draw(t, "resp")
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("empty-tools panicked: %v", r)
				}
			}()
			if CoerceToolCall(req, resp) {
				t.Fatalf("empty-tools returned true (len(Tools)==0 short-circuit broken)")
			}
		case 4:
			// Oversized response text — exercises the JSON-parse path
			// on a payload that cannot possibly be valid JSON.
			req := &canonical.ChatRequest{Tools: []canonical.ToolSpec{{Name: "x"}}}
			resp := &canonical.ChatResponse{
				Message: canonical.Message{
					Role: canonical.RoleAssistant,
					Content: []canonical.ContentPart{{
						Kind: canonical.ContentKindText,
						Text: strings.Repeat("x", 64*1024),
					}},
				},
			}
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("oversized-text panicked: %v", r)
				}
			}()
			// Outcome is "false" (not valid JSON) but the load-bearing
			// assertion is "no panic on a 64KB response body."
			_ = CoerceToolCall(req, resp)
		}
	})
}
