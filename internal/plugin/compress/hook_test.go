// internal/plugin/compress/hook_test.go
package compress

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/engine"
)

// Compile-time interface satisfaction (mirrors jsonformat's assertion).
var _ engine.PreHook = (*Hook)(nil)

func newTestHook() *Hook {
	return &Hook{
		Enabled:       true,
		TriggerTokens: 100, // low trigger so small test transcripts compress
		BudgetTokens:  50,
		ProtectTail:   2,
		ToolKeep:      100,
		Logger:        slog.Default(),
	}
}

// bigTranscript builds a transcript comfortably over 100 est tokens with
// two fat tool results outside the protected tail — in the REAL
// OpenAI/Ollama canonical shape (RoleTool + Text part + ToolCallID;
// that is what the adapters produce and what the role-aware estimator
// counts). Distinct ToolCallIDs, so they shrink via stage-1 newline
// cleanup + stage-2 truncation — dupKey correctly does NOT treat them
// as duplicates.
func bigTranscript() *canonical.ChatRequest {
	fat := strings.Repeat("tool output line\n\n\n\n", 100)
	toolMsg := func(id string) canonical.Message {
		m := textMsg(canonical.RoleTool, fat)
		m.ToolCallID = id
		return m
	}
	return &canonical.ChatRequest{
		Model:  "auto",
		System: "SYSTEM PROMPT — MUST SURVIVE VERBATIM",
		Messages: []canonical.Message{
			textMsg(canonical.RoleUser, "please run the tool"),
			toolMsg("t1"),
			toolMsg("t2"),
			textMsg(canonical.RoleUser, "tail question — protected"),
			textMsg(canonical.RoleAssistant, "tail answer — protected"),
		},
	}
}

func TestBefore_CompressesWhenEnabled(t *testing.T) {
	h := newTestHook()
	req := bigTranscript()
	before := estMessagesTokens(req.Messages)

	resp, err := h.Before(context.Background(), req)
	if err != nil || resp != nil {
		t.Fatalf("Before = (%v, %v), want (nil, nil)", resp, err)
	}
	after := estMessagesTokens(req.Messages)
	if after >= before {
		t.Errorf("no shrinkage: before=%d after=%d", before, after)
	}
	if req.System != "SYSTEM PROMPT — MUST SURVIVE VERBATIM" {
		t.Error("System mutated")
	}
	runs, saved := h.Stats()
	if runs != 1 || saved <= 0 {
		t.Errorf("Stats = (%d, %d), want (1, >0)", runs, saved)
	}
}

func TestBefore_DisabledByDefault(t *testing.T) {
	h := newTestHook()
	h.Enabled = false
	req := bigTranscript()
	before := estMessagesTokens(req.Messages)
	_, _ = h.Before(context.Background(), req)
	if estMessagesTokens(req.Messages) != before {
		t.Error("disabled hook mutated the transcript")
	}
}

func TestBefore_MetadataDirectiveOverridesEnv(t *testing.T) {
	h := newTestHook()
	h.Enabled = false // env default off
	req := bigTranscript()
	req.Metadata = map[string]any{MetadataKey: true} // +compress suffix
	before := estMessagesTokens(req.Messages)
	_, _ = h.Before(context.Background(), req)
	if estMessagesTokens(req.Messages) >= before {
		t.Error("metadata directive true did not enable compression")
	}
}

func TestBefore_HeaderOverridesMetadata(t *testing.T) {
	h := newTestHook() // env on
	req := bigTranscript()
	req.Metadata = map[string]any{MetadataKey: true}        // suffix says on
	ctx := WithHeaderDirective(context.Background(), false) // header says OFF — wins
	before := estMessagesTokens(req.Messages)
	_, _ = h.Before(ctx, req)
	if estMessagesTokens(req.Messages) != before {
		t.Error("X-Compression: 0 header did not win over the suffix directive")
	}
}

func TestBefore_UnderTriggerIsNoop(t *testing.T) {
	h := newTestHook()
	h.TriggerTokens = 1_000_000
	req := bigTranscript()
	before := estMessagesTokens(req.Messages)
	_, _ = h.Before(context.Background(), req)
	if estMessagesTokens(req.Messages) != before {
		t.Error("under-trigger transcript was mutated")
	}
	if runs, _ := h.Stats(); runs != 0 {
		t.Error("no-op counted as a run")
	}
}

func TestBefore_AtBudgetIsNoop(t *testing.T) {
	// Boundary (review MINOR-18): config allows budget == trigger. A
	// transcript already at/under budget must not be lossily mutated even
	// when it meets the trigger.
	h := newTestHook()
	req := bigTranscript()
	size := estMessagesTokens(req.Messages)
	h.TriggerTokens = size // trigger met exactly
	h.BudgetTokens = size  // ... but already within budget
	_, _ = h.Before(context.Background(), req)
	if estMessagesTokens(req.Messages) != size {
		t.Error("transcript at budget was mutated")
	}
	if runs, _ := h.Stats(); runs != 0 {
		t.Error("at-budget no-op counted as a run")
	}
}

func TestBefore_NilAndEmptySafe(t *testing.T) {
	h := newTestHook()
	if resp, err := h.Before(context.Background(), nil); resp != nil || err != nil {
		t.Error("nil req must be a no-op")
	}
	if resp, err := h.Before(context.Background(), &canonical.ChatRequest{}); resp != nil || err != nil {
		t.Error("empty req must be a no-op")
	}
}

func TestBefore_ProtectedTailUntouched(t *testing.T) {
	h := newTestHook()
	req := bigTranscript()
	_, _ = h.Before(context.Background(), req)
	n := len(req.Messages)
	if flattenText(req.Messages[n-2]) != "tail question — protected" ||
		flattenText(req.Messages[n-1]) != "tail answer — protected" {
		t.Error("protected tail mutated")
	}
}

func TestBefore_AnchorImmuneToAllStages_ZeroTail(t *testing.T) {
	// Review 2 MAJOR-3: with PROTECT_TAIL=0 nothing but the anchor rule
	// protects the current question. A current question that exactly
	// repeats an older message must NOT be collapsed to a duplicate stub
	// (and must not be whitespace-normalized or truncated either).
	question := strings.Repeat("please analyze this payload\n\n\n\n", 20) // stage-1-compressible on purpose
	req := &canonical.ChatRequest{
		Messages: []canonical.Message{
			textMsg(canonical.RoleUser, question), // old duplicate — fair game
			textMsg(canonical.RoleAssistant, strings.Repeat("filler answer ", 40)),
			textMsg(canonical.RoleUser, question), // ANCHOR — must survive byte-for-byte
		},
	}
	h := newTestHook()
	h.ProtectTail = 0
	_, _ = h.Before(context.Background(), req)
	if flattenText(req.Messages[2]) != question {
		t.Error("anchor mutated with ProtectTail=0 — model would not see the actual request")
	}
}

func TestBefore_CurrentToolResultTurnImmutable_ZeroTail(t *testing.T) {
	// Third-pass MAJOR-1 (OpenAI/Ollama shape): a follow-up transcript
	// ENDS in a RoleTool result. With ProtectTail=0 only the current-turn
	// pin protects it — stages 2-4 must not truncate or elide the result
	// the model is about to consume. Run for both the RoleTool shape and
	// the Anthropic shape (tool_result inside a RoleUser message).
	fatResult := strings.Repeat("tool output the model needs ", 60)
	roleToolShape := textMsg(canonical.RoleTool, fatResult) // real OpenAI/Ollama shape: Text part
	roleToolShape.ToolCallID = "t9"
	shapes := map[string]canonical.Message{
		"openai-ollama-roletool": roleToolShape,
		"anthropic-user-toolresult": {
			Role: canonical.RoleUser,
			Content: []canonical.ContentPart{{
				Kind:       canonical.ContentKindToolResult,
				ToolResult: &canonical.ToolResultPart{ToolUseID: "t9", Content: fatResult},
			}},
		},
	}
	for name, current := range shapes {
		t.Run(name, func(t *testing.T) {
			req := &canonical.ChatRequest{Messages: []canonical.Message{
				textMsg(canonical.RoleUser, "run the tool "+strings.Repeat("context ", 40)),
				textMsg(canonical.RoleAssistant, strings.Repeat("working on it ", 40)),
				current, // CURRENT TURN — must survive byte-for-byte
			}}
			snapshot := mustJSON(t, req.Messages[2])
			h := newTestHook()
			h.ProtectTail = 0
			h.ToolKeep = 10 // aggressive — WOULD truncate if not pinned
			h.BudgetTokens = 1
			h.TriggerTokens = 1
			_, _ = h.Before(context.Background(), req)
			if got := mustJSON(t, req.Messages[2]); got != snapshot {
				t.Errorf("current tool-result turn mutated:\n got %s\nwant %s", got, snapshot)
			}
		})
	}
}

func TestBefore_UserQuestionPinnedWhenNotLast(t *testing.T) {
	// Third-pass MAJOR-1: when the transcript ends in tool output, the
	// user's question is an EARLIER message — it must still be pinned
	// (it is the stage-4 query; compressing it would rank history
	// against a mutated question).
	question := "please analyze the flux readings " + strings.Repeat("q", 300)
	req := &canonical.ChatRequest{Messages: []canonical.Message{
		textMsg(canonical.RoleUser, strings.Repeat("old context ", 50)),
		textMsg(canonical.RoleUser, question), // QUERY pin — not the last message
		{Role: canonical.RoleTool, Content: []canonical.ContentPart{{
			Kind:       canonical.ContentKindToolResult,
			ToolResult: &canonical.ToolResultPart{ToolUseID: "t1", Content: strings.Repeat("flux readings data ", 60)},
		}}}, // CURRENT TURN pin
	}}
	h := newTestHook()
	h.ProtectTail = 0
	h.BudgetTokens = 1
	h.TriggerTokens = 1
	_, _ = h.Before(context.Background(), req)
	if flattenText(req.Messages[1]) != question {
		t.Error("user question mutated even though it is the stage-4 query")
	}
}

func TestBefore_PIIOnlyCurrentQuestion_NeverUsesStaleEvidence(t *testing.T) {
	// Revision-5 MAJOR, full-pipeline regression: the current question
	// is entirely redacted PII. Selection must pick IT (raw text has
	// tokens), its sanitized query must come up empty, and stage 4 must
	// elide NOTHING — never fall back to the older question and prune
	// history against that stale evidence.
	oldQuestion := "diagnose the database connection timeout"
	overlapping := strings.Repeat("the database connection timeout came from the pool ", 8)
	req := &canonical.ChatRequest{Messages: []canonical.Message{
		textMsg(canonical.RoleUser, oldQuestion),
		textMsg(canonical.RoleAssistant, overlapping),             // would score high vs the OLD question
		textMsg(canonical.RoleUser, "[PII:Email:AAAAaaaa1111_-]"), // current question — PII only
	}}
	h := newTestHook()
	h.ProtectTail = 0
	h.BudgetTokens = 1
	h.TriggerTokens = 1
	_, _ = h.Before(context.Background(), req)
	for i := range req.Messages {
		if strings.Contains(flattenText(req.Messages[i]), "elided as low-relevance") {
			t.Fatalf("msg %d elided using stale evidence from an older question", i)
		}
	}
}

func TestBefore_PIIOnlyCurrentQuestion_NERHashToken_NeverUsesStaleEvidence(t *testing.T) {
	// Same defect class as TestBefore_PIIOnlyCurrentQuestion_NeverUsesStaleEvidence,
	// but for NER-emitted (PERSON/LOCATION) hash tokens rather than a
	// regex-recognizer token: with PII_NER_ENABLED + hash mode, a
	// PERSON-only current question must not be treated as carrying
	// evidence just because its hash token wasn't stripped — the
	// tokenizer would otherwise emit shared synthetic terms
	// ("person"/"h") that give stage 4 false positive lexical overlap
	// with an unrelated history message, bypassing the zero-evidence
	// safety stop.
	unrelated := strings.Repeat("the quarterly infrastructure budget review notes ", 8)
	// Padded past minCandidateLen (200 bytes) so it is an eligible stage-4
	// candidate — a bare "[PERSON:h-bbbbbbbb]" is too short to reach the
	// scorer at all, which would mask the defect rather than reproduce it.
	withNERHash := strings.Repeat("internal engineering standup topics covered today ", 5) + "[PERSON:h-bbbbbbbb]"
	req := &canonical.ChatRequest{Messages: []canonical.Message{
		textMsg(canonical.RoleUser, unrelated),
		textMsg(canonical.RoleAssistant, withNERHash),
		textMsg(canonical.RoleUser, "[PERSON:h-aaaaaaaa]"), // current question — NER hash token only
	}}
	h := newTestHook()
	h.ProtectTail = 0
	h.BudgetTokens = 1
	h.TriggerTokens = 1
	_, _ = h.Before(context.Background(), req)
	for i := range req.Messages {
		if strings.Contains(flattenText(req.Messages[i]), "elided as low-relevance") {
			t.Fatalf("msg %d elided using NER-hash-token evidence from a PII-only question", i)
		}
	}
}

func TestBefore_LaterStagesSkippedOnceBudgetMet(t *testing.T) {
	// Review 2 MAJOR-1: if stage 1 (blank-line cleanup) alone reaches the
	// budget, stages 2-3 must not run — the fat tool result stays
	// untruncated and the duplicate pair stays uncollapsed.
	fluffy := strings.Repeat("line\n\n\n\n\n", 200) // shrinks ~40% under stage 1
	toolPayload := strings.Repeat("x", 1200)        // > 2*ToolKeep+64 → WOULD truncate
	// TrimSuffix: strings.Repeat("dup payload ", 30) ends in a trailing
	// space that stage 1's UNCONDITIONAL (budget-independent)
	// trailingWSRe cleanup always strips, regardless of whether stage 3
	// runs — trimming it here keeps the "!= dup" assertion below a
	// faithful probe of stage 3 specifically, not a false positive from
	// stage 1's legitimate, always-on normalization.
	dup := strings.TrimSuffix(strings.Repeat("dup payload ", 30), " ") // > minDupLen → WOULD collapse
	req := &canonical.ChatRequest{
		Messages: []canonical.Message{
			textMsg(canonical.RoleUser, fluffy),
			{Role: canonical.RoleTool, Content: []canonical.ContentPart{{
				Kind:       canonical.ContentKindToolResult,
				ToolResult: &canonical.ToolResultPart{ToolUseID: "t1", Content: toolPayload}}}},
			textMsg(canonical.RoleAssistant, dup),
			textMsg(canonical.RoleAssistant, dup),
			textMsg(canonical.RoleUser, "current question"),
		},
	}
	h := newTestHook()
	h.ProtectTail = 0
	h.ToolKeep = 100
	h.TriggerTokens = estMessagesTokens(req.Messages) // trigger met exactly
	// Budget chosen so stage 1's newline collapse alone satisfies it:
	// everything except the fluffy message, plus the fluffy message's
	// post-stage-1 size, plus slack.
	h.BudgetTokens = estMessagesTokens(req.Messages) - estimateTokens(fluffy) + estimateTokens(normalizeWhitespace(fluffy)) + 10

	_, _ = h.Before(context.Background(), req)

	if got := req.Messages[1].Content[0].ToolResult.Content; got != toolPayload {
		t.Error("stage 2 ran after stage 1 already met the budget")
	}
	if flattenText(req.Messages[3]) != dup {
		t.Error("stage 3 ran after stage 1 already met the budget")
	}
}

func TestDescribe(t *testing.T) {
	h := newTestHook()
	kind, cfg := h.Describe()
	if kind != "Pre" {
		t.Errorf("kind = %q, want Pre", kind)
	}
	for _, k := range []string{
		"enabled", "trigger_tokens", "budget_tokens", "protect_tail", "tool_keep",
		"runs", "tokens_saved_est", "budget_unmet",
	} {
		if _, ok := cfg[k]; !ok {
			t.Errorf("Describe config missing %q", k)
		}
	}
}
