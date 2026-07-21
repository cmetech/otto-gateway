// internal/plugin/compress/invariants_test.go
package compress

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"otto-gateway/internal/canonical"
)

// mustJSON serializes a value for snapshot comparison. canonical types
// are plain exported-field structs, so JSON is a faithful deep-equality
// proxy (and its diffs read well on failure).
func mustJSON(t interface{ Fatalf(string, ...any) }, v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("snapshot marshal: %v", err)
	}
	return string(b)
}

// genMessage draws a deep multipart message: 0-4 parts across EVERY
// ContentKind (text, thinking, image, tool_use, tool_result), optional
// message-level ToolCalls, optional ToolCallID. Single-part text-only
// generation would let structural regressions pass unnoticed (review
// MAJOR-13).
func genMessage(t *rapid.T) canonical.Message {
	m := canonical.Message{Role: canonical.MessageRole(rapid.IntRange(0, 3).Draw(t, "role"))}
	nParts := rapid.IntRange(0, 4).Draw(t, "nParts")
	for p := 0; p < nParts; p++ {
		switch rapid.IntRange(0, 4).Draw(t, "kind") {
		case 0:
			m.Content = append(m.Content, canonical.ContentPart{
				Kind: canonical.ContentKindText, Text: rapid.StringN(0, 1000, 2000).Draw(t, "text")})
		case 1:
			m.Content = append(m.Content, canonical.ContentPart{
				Kind: canonical.ContentKindThinking, Text: rapid.StringN(0, 1000, 2000).Draw(t, "think")})
		case 2:
			m.Content = append(m.Content, canonical.ContentPart{
				Kind:  canonical.ContentKindImage,
				Image: &canonical.ImagePart{MIME: "image/png", DataBase64: rapid.StringN(0, 64, 64).Draw(t, "img")}})
		case 3:
			m.Content = append(m.Content, canonical.ContentPart{
				Kind: canonical.ContentKindToolUse,
				ToolUse: &canonical.ToolUsePart{
					ID:    rapid.StringN(1, 8, 8).Draw(t, "tuid"),
					Name:  rapid.StringN(1, 12, 12).Draw(t, "tuname"),
					Input: map[string]any{"arg": rapid.StringN(0, 500, 1000).Draw(t, "tuarg")}}})
		case 4:
			m.Content = append(m.Content, canonical.ContentPart{
				Kind: canonical.ContentKindToolResult,
				ToolResult: &canonical.ToolResultPart{
					ToolUseID: rapid.StringN(1, 8, 8).Draw(t, "trid"),
					IsError:   rapid.Bool().Draw(t, "trerr"),
					Content:   rapid.StringN(0, 1000, 2000).Draw(t, "trtext")}})
		}
	}
	if rapid.Bool().Draw(t, "hasToolCalls") {
		m.ToolCalls = []canonical.ToolCall{{
			ID:        rapid.StringN(1, 8, 8).Draw(t, "tcid2"),
			Name:      rapid.StringN(1, 12, 12).Draw(t, "tcname"),
			Arguments: map[string]any{"q": rapid.StringN(0, 200, 400).Draw(t, "tcarg")}}}
	}
	if rapid.Bool().Draw(t, "hasToolCallID") {
		m.ToolCallID = rapid.StringN(1, 8, 8).Draw(t, "tcallid")
	}
	return m
}

// structuralKey captures everything Before must NEVER change on a MUTABLE
// message: Role (it selects the ACP serialization branch — review 2
// MINOR-1), ToolCallID, full ToolCalls, and the identity/order of every
// structural part (images byte-for-byte; ToolUse id/name/input;
// ToolResult id/is_error — content is compressible, identity is not).
// Prose parts (text/thinking) are deliberately NOT keyed: replaceText
// legitimately merges them into one stub, so their count/kind may change
// on mutable messages; protected messages are covered by the full-JSON
// snapshot instead.
func structuralKey(tt interface{ Fatalf(string, ...any) }, m canonical.Message) string {
	type partID struct {
		Kind    int
		Image   *canonical.ImagePart
		ToolUse *canonical.ToolUsePart
		TRID    string
		TRErr   bool
	}
	var parts []partID
	for _, p := range m.Content {
		switch p.Kind {
		case canonical.ContentKindImage:
			parts = append(parts, partID{Kind: int(p.Kind), Image: p.Image})
		case canonical.ContentKindToolUse:
			parts = append(parts, partID{Kind: int(p.Kind), ToolUse: p.ToolUse})
		case canonical.ContentKindToolResult:
			if p.ToolResult != nil {
				parts = append(parts, partID{Kind: int(p.Kind), TRID: p.ToolResult.ToolUseID, TRErr: p.ToolResult.IsError})
			}
		}
	}
	return mustJSON(tt, struct {
		Role       int
		ToolCallID string
		ToolCalls  []canonical.ToolCall
		Parts      []partID
	}{int(m.Role), m.ToolCallID, m.ToolCalls, parts})
}

// TestInvariants_Property: for ANY generated transcript and config, Before
// (a) never returns a non-nil error or response, (b) never mutates System
// or Tools, (c) never mutates RoleSystem messages or the protected tail
// (FULL deep equality, not flattened text), (d) never drops or reorders a
// message, (e) never removes ToolCallID / ToolCalls / ToolUse parts /
// image parts / ToolResult identity from ANY message.
func TestInvariants_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		nMsgs := rapid.IntRange(0, 20).Draw(t, "nMsgs")
		msgs := make([]canonical.Message, 0, nMsgs)
		for i := 0; i < nMsgs; i++ {
			msgs = append(msgs, genMessage(t))
		}

		h := &Hook{
			Enabled:       true,
			TriggerTokens: rapid.IntRange(1, 5000).Draw(t, "trigger"),
			BudgetTokens:  rapid.IntRange(1, 5000).Draw(t, "budget"),
			ProtectTail:   rapid.IntRange(0, 25).Draw(t, "tail"),
			ToolKeep:      rapid.IntRange(1, 2000).Draw(t, "keep"),
			Logger:        slog.Default(),
		}
		// Stage 4 is local BM25 and always AVAILABLE, but random
		// triggers/budgets/messages frequently no-op before it runs —
		// this broad property covers "never corrupts under any config";
		// TestInvariants_Stage4ForcedProperty below GUARANTEES pruning
		// executes (revision-4 MINOR: the two claims are different).

		tools := []canonical.ToolSpec{{Name: "grep"}}
		req := &canonical.ChatRequest{System: "SYS", Tools: tools, Messages: msgs}

		// Snapshots: full-message JSON for protected indices; structural
		// key for every message.
		nProtected := h.ProtectTail
		if nProtected > len(msgs) {
			nProtected = len(msgs)
		}
		frozen := map[int]string{} // index → full-message snapshot
		for i := len(msgs) - nProtected; i < len(msgs); i++ {
			frozen[i] = mustJSON(t, msgs[i])
		}
		for i := range msgs {
			if msgs[i].Role == canonical.RoleSystem {
				frozen[i] = mustJSON(t, msgs[i])
			}
		}
		// BOTH pinned indices get the full-JSON snapshot too (third-pass
		// MINOR: images/ToolUse/ToolResult flags on a pinned message must
		// be deep-equal, not just flattened-text-equal).
		lastIdx, queryIdx := findPinned(msgs)
		if lastIdx >= 0 {
			frozen[lastIdx] = mustJSON(t, msgs[lastIdx])
		}
		if queryIdx >= 0 {
			frozen[queryIdx] = mustJSON(t, msgs[queryIdx])
		}
		structural := make([]string, len(msgs))
		for i := range msgs {
			structural[i] = structuralKey(t, msgs[i])
		}
		toolsSnap := mustJSON(t, tools)
		// Reviewer-mandated invariant: compression cannot INCREASE any
		// individual message's estMessageTokens (message count/order are
		// already asserted fixed above/below).
		tokensBefore := make([]int, len(msgs))
		for i := range msgs {
			tokensBefore[i] = estMessageTokens(msgs[i])
		}

		resp, err := h.Before(context.Background(), req)
		if resp != nil || err != nil {
			t.Fatalf("Before returned (%v, %v) — must always be (nil, nil)", resp, err)
		}
		if req.System != "SYS" {
			t.Fatal("System mutated")
		}
		if mustJSON(t, req.Tools) != toolsSnap {
			t.Fatal("Tools mutated")
		}
		if len(req.Messages) != nMsgs {
			t.Fatalf("message count changed: %d → %d", nMsgs, len(req.Messages))
		}
		for i, want := range frozen {
			if got := mustJSON(t, req.Messages[i]); got != want {
				t.Fatalf("protected/system msg %d mutated:\n got %s\nwant %s", i, got, want)
			}
		}
		for i := range req.Messages {
			if got := structuralKey(t, req.Messages[i]); got != structural[i] {
				t.Fatalf("structural fields changed on msg %d:\n got %s\nwant %s", i, got, structural[i])
			}
		}
		for i := range req.Messages {
			if got := estMessageTokens(req.Messages[i]); got > tokensBefore[i] {
				t.Fatalf("estMessageTokens INCREASED on msg %d: %d -> %d (compression must never grow a message)", i, tokensBefore[i], got)
			}
		}
	})
}

// TestInvariants_Stage4ForcedProperty GUARANTEES pruning executes
// (revision-4 MINOR): a tokenizable final user question, 2-6 mutable
// multipart candidates that each share a query term, and
// Trigger=1/Budget=1/ProtectTail=0 force stage 4 to elide — then every
// structural invariant must still hold and the pinned question must be
// deep-equal untouched.
func TestInvariants_Stage4ForcedProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		mkCandidate := func(i int) canonical.Message {
			m := canonical.Message{Role: canonical.RoleUser}
			m.Content = append(m.Content, canonical.ContentPart{
				Kind: canonical.ContentKindText,
				// "alpha" guarantees positive BM25 overlap with the query;
				// the index + random filler keep candidates distinct so
				// stage 3 cannot collapse them first.
				Text: fmt.Sprintf("alpha candidate %d ", i) + rapid.StringN(200, 800, 1600).Draw(t, "filler"),
			})
			if rapid.Bool().Draw(t, "withImage") {
				m.Content = append(m.Content, canonical.ContentPart{
					Kind:  canonical.ContentKindImage,
					Image: &canonical.ImagePart{MIME: "image/png", DataBase64: rapid.StringN(0, 64, 64).Draw(t, "img")},
				})
			}
			if rapid.Bool().Draw(t, "withToolResult") {
				m.Content = append(m.Content, canonical.ContentPart{
					Kind: canonical.ContentKindToolResult,
					ToolResult: &canonical.ToolResultPart{
						ToolUseID: rapid.StringN(1, 8, 8).Draw(t, "trid"),
						Content:   rapid.StringN(0, 500, 1000).Draw(t, "trc"),
					},
				})
			}
			return m
		}
		nCands := rapid.IntRange(2, 6).Draw(t, "nCands")
		msgs := make([]canonical.Message, 0, nCands+1)
		for i := 0; i < nCands; i++ {
			msgs = append(msgs, mkCandidate(i))
		}
		msgs = append(msgs, textMsg(canonical.RoleUser, "alpha question"))

		structural := make([]string, len(msgs))
		for i := range msgs {
			structural[i] = structuralKey(t, msgs[i])
		}
		querySnap := mustJSON(t, msgs[len(msgs)-1])
		tokensBefore := make([]int, len(msgs))
		for i := range msgs {
			tokensBefore[i] = estMessageTokens(msgs[i])
		}

		h := &Hook{Enabled: true, TriggerTokens: 1, BudgetTokens: 1, ProtectTail: 0, ToolKeep: 1, Logger: slog.Default()}
		req := &canonical.ChatRequest{Messages: msgs}
		if resp, err := h.Before(context.Background(), req); resp != nil || err != nil {
			t.Fatalf("Before returned (%v, %v) — must always be (nil, nil)", resp, err)
		}

		elided := false
		for i := range req.Messages {
			if strings.Contains(flattenText(req.Messages[i]), "elided as low-relevance") {
				elided = true
			}
			if got := structuralKey(t, req.Messages[i]); got != structural[i] {
				t.Fatalf("structural fields changed on msg %d under forced pruning", i)
			}
		}
		if !elided {
			t.Fatal("stage 4 elided nothing despite forced overlap and impossible budget")
		}
		if got := mustJSON(t, req.Messages[len(req.Messages)-1]); got != querySnap {
			t.Fatal("pinned question mutated under forced pruning")
		}
		for i := range req.Messages {
			if got := estMessageTokens(req.Messages[i]); got > tokensBefore[i] {
				t.Fatalf("estMessageTokens INCREASED on msg %d under forced pruning: %d -> %d", i, tokensBefore[i], got)
			}
		}
	})
}

// TestInvariants_ThinkingPlusToolUseCrossesTrigger_StructurallyUnchanged is
// a targeted (non-generated) regression pinning review 2 MAJOR-10's
// corrected oracle: a message that is 100% thinking prose plus a fat
// ToolUse.Input and ZERO plain text must still CROSS THE TRIGGER — the
// role-aware estimator (estMessageTokens, RoleAssistant branch) counts
// both the Thinking carrier and the ToolUse carrier — while remaining
// STRUCTURALLY UNCHANGED. ToolUse.Input is never touched by any stage,
// and carriesToolCall excludes the message from stage 4, so the only
// permitted mutation anywhere in the pipeline is stage 1's blank-line
// cleanup of the thinking prose. (The original oracle asserted the
// message must SHRINK, which is unsatisfiable once the huge ToolUse.Input
// dominates the size and cannot be compressed — this test asserts
// structural invariance instead, never shrinkage.)
func TestInvariants_ThinkingPlusToolUseCrossesTrigger_StructurallyUnchanged(t *testing.T) {
	thinking := "deep reasoning about the payload\n\n\n\n" + strings.Repeat("more reasoning here\n\n\n\n", 20)
	fatArg := strings.Repeat("x", 20_000)
	target := canonical.Message{
		Role: canonical.RoleAssistant,
		Content: []canonical.ContentPart{
			{Kind: canonical.ContentKindThinking, Text: thinking},
			{Kind: canonical.ContentKindToolUse, ToolUse: &canonical.ToolUsePart{
				ID: "t1", Name: "grep", Input: map[string]any{"arg": fatArg}}},
		},
	}
	req := &canonical.ChatRequest{Messages: []canonical.Message{
		target,
		textMsg(canonical.RoleUser, "final question"), // pins lastIdx AND queryIdx away from target
	}}
	h := newTestHook()
	h.ProtectTail = 0

	before := estMessagesTokens(req.Messages)
	if before < h.TriggerTokens {
		t.Fatalf("test setup: transcript (%d est tokens) does not cross TriggerTokens (%d)", before, h.TriggerTokens)
	}
	structKeyBefore := structuralKey(t, req.Messages[0])

	if resp, err := h.Before(context.Background(), req); resp != nil || err != nil {
		t.Fatalf("Before returned (%v, %v) — must always be (nil, nil)", resp, err)
	}

	if got := structuralKey(t, req.Messages[0]); got != structKeyBefore {
		t.Fatalf("structural fields changed on thinking+ToolUse msg:\n got %s\nwant %s", got, structKeyBefore)
	}
	gotArg, _ := req.Messages[0].Content[1].ToolUse.Input["arg"].(string)
	if gotArg != fatArg {
		t.Error("ToolUse.Input mutated — no stage may ever touch it")
	}
	if got := req.Messages[0].Content[0].Text; got != normalizeWhitespace(thinking) {
		t.Errorf("thinking text changed by something other than stage-1 normalization:\n got %q\nwant %q",
			got, normalizeWhitespace(thinking))
	}
}

// TestInvariants_ToolResultInToolCallsMessage_TruncateNotElide is a
// targeted (non-generated) regression: an over-budget transcript whose
// only fat lives in a ToolResult part that rides a message which ALSO
// carries message-level ToolCalls. Stage 2 (truncation) is free to shrink
// the ToolResult content, but stage 4 must NEVER elide the message —
// carriesToolCall excludes it because eliding would break tool_use/
// tool_result pairing on the Anthropic surface.
func TestInvariants_ToolResultInToolCallsMessage_TruncateNotElide(t *testing.T) {
	fat := strings.Repeat("stale tool output ", 200)
	msg := canonical.Message{
		Role: canonical.RoleAssistant,
		Content: []canonical.ContentPart{{
			Kind:       canonical.ContentKindToolResult,
			ToolResult: &canonical.ToolResultPart{ToolUseID: "t1", Content: fat},
		}},
		ToolCalls: []canonical.ToolCall{{ID: "t1", Name: "grep", Arguments: map[string]any{"q": "x"}}},
	}
	req := &canonical.ChatRequest{Messages: []canonical.Message{
		msg,
		textMsg(canonical.RoleUser, "final question "+strings.Repeat("q", 300)),
	}}
	h := newTestHook()
	h.ProtectTail = 0
	h.ToolKeep = 20 // aggressive — would truncate heavily
	h.TriggerTokens = 1
	h.BudgetTokens = 1 // unattainable — forces stage 4 to be attempted

	if resp, err := h.Before(context.Background(), req); resp != nil || err != nil {
		t.Fatalf("Before returned (%v, %v) — must always be (nil, nil)", resp, err)
	}

	got := req.Messages[0]
	if len(got.Content) != 1 || got.Content[0].Kind != canonical.ContentKindToolResult {
		t.Fatalf("message elided or restructured — stage 4 must never elide a ToolCalls-carrying message: %+v", got)
	}
	if got.Content[0].ToolResult == nil || got.Content[0].ToolResult.ToolUseID != "t1" {
		t.Fatal("ToolResult identity (ToolUseID) lost")
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].ID != "t1" {
		t.Fatal("ToolCalls lost")
	}
	if strings.Contains(flattenText(got), "elided as low-relevance") {
		t.Fatal("ToolCalls-carrying message was elided by stage 4")
	}
	// Review follow-up: assert stage 2 actually SHRANK the ToolResult
	// content (not just left it structurally intact) — pins the
	// regression where stage 2 becomes a no-op for ToolCalls-carrying
	// messages instead of truncating them.
	content := got.Content[0].ToolResult.Content
	if len(content) >= len(fat) {
		t.Fatalf("ToolResult.Content was not shrunk by stage 2: got %d bytes, original %d bytes", len(content), len(fat))
	}
	if !strings.Contains(content, "chars omitted") {
		t.Fatalf("ToolResult.Content missing the middleTruncate elision marker: %q", content)
	}
}

// TestInvariants_BothPinsSurviveByteForByte_ZeroTailUnattainableBudget is a
// targeted (non-generated) regression: ProtectTail=0 with an unattainable
// budget. Only the two findPinned indices (the query pin and the
// current-turn pin, deliberately distinct here) protect the transcript —
// both must survive BYTE-FOR-BYTE (full-JSON snapshot, not merely
// flattened-text equality) even though both are, individually, exactly
// the kind of content stage 1/2/3 would otherwise mutate.
func TestInvariants_BothPinsSurviveByteForByte_ZeroTailUnattainableBudget(t *testing.T) {
	question := "please analyze the flux readings " + strings.Repeat("q\n\n\n\n", 60) // stage-1-compressible
	toolResultMsg := canonical.Message{
		Role: canonical.RoleTool,
		Content: []canonical.ContentPart{{
			Kind: canonical.ContentKindText,
			Text: strings.Repeat("flux readings data\n\n\n\n", 60), // stage-1 + stage-2 compressible
		}},
		ToolCallID: "t1",
	}
	req := &canonical.ChatRequest{Messages: []canonical.Message{
		textMsg(canonical.RoleUser, strings.Repeat("old context ", 50)),
		textMsg(canonical.RoleUser, question), // QUERY pin
		toolResultMsg,                         // CURRENT TURN pin
	}}
	lastIdx, queryIdx := findPinned(req.Messages)
	if lastIdx != 2 || queryIdx != 1 || lastIdx == queryIdx {
		t.Fatalf("test setup: want distinct pins (lastIdx=2, queryIdx=1), got lastIdx=%d queryIdx=%d", lastIdx, queryIdx)
	}
	querySnap := mustJSON(t, req.Messages[queryIdx])
	turnSnap := mustJSON(t, req.Messages[lastIdx])

	h := newTestHook()
	h.ProtectTail = 0
	h.ToolKeep = 1 // maximally aggressive — would gut the tool result if not pinned
	h.TriggerTokens = 1
	h.BudgetTokens = 1 // unattainable

	if resp, err := h.Before(context.Background(), req); resp != nil || err != nil {
		t.Fatalf("Before returned (%v, %v) — must always be (nil, nil)", resp, err)
	}

	if got := mustJSON(t, req.Messages[queryIdx]); got != querySnap {
		t.Errorf("query pin mutated:\n got %s\nwant %s", got, querySnap)
	}
	if got := mustJSON(t, req.Messages[lastIdx]); got != turnSnap {
		t.Errorf("current-turn pin mutated:\n got %s\nwant %s", got, turnSnap)
	}
}
