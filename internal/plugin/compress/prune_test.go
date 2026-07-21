// internal/plugin/compress/prune_test.go
package compress

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"otto-gateway/internal/canonical"
)

func toolResultMsg(role canonical.MessageRole, id, content string) canonical.Message {
	return canonical.Message{
		Role: role,
		Content: []canonical.ContentPart{{
			Kind:       canonical.ContentKindToolResult,
			ToolResult: &canonical.ToolResultPart{ToolUseID: id, Content: content},
		}},
	}
}

func TestFindPinned_SeparatesTurnFromQuery(t *testing.T) {
	// OpenAI/Ollama shape: transcript ends in a RoleTool result. The
	// current turn is the tool result (index 2); the query is the user
	// question (index 1).
	msgs := []canonical.Message{
		textMsg(canonical.RoleSystem, "sys"),
		textMsg(canonical.RoleUser, "run the tool please"),
		toolResultMsg(canonical.RoleTool, "t1", "tool output"),
	}
	last, query := findPinned(msgs)
	if last != 2 || query != 1 {
		t.Errorf("findPinned = (%d, %d), want (2, 1)", last, query)
	}
}

func TestFindPinned_AnthropicPureToolResultIsNotQuery(t *testing.T) {
	// Anthropic shape: tool_result rides a RoleUser message with NO Text
	// part. It is the current turn (pinned) but NOT the query — the
	// query is the earlier real question.
	msgs := []canonical.Message{
		textMsg(canonical.RoleUser, "what does the log say?"),
		textMsg(canonical.RoleAssistant, "let me check"),
		toolResultMsg(canonical.RoleUser, "t1", "log contents here"),
	}
	last, query := findPinned(msgs)
	if last != 2 || query != 0 {
		t.Errorf("findPinned = (%d, %d), want (2, 0)", last, query)
	}
}

func TestFindPinned_MixedToolResultPlusTextIsQuery(t *testing.T) {
	// A mixed Anthropic turn (tool_result + user text) IS the query —
	// but queryText must use only its Text parts.
	mixed := canonical.Message{
		Role: canonical.RoleUser,
		Content: []canonical.ContentPart{
			{Kind: canonical.ContentKindToolResult, ToolResult: &canonical.ToolResultPart{ToolUseID: "t1", Content: "zebra zebra zebra"}},
			{Kind: canonical.ContentKindText, Text: "now summarize the alpha findings"},
		},
	}
	msgs := []canonical.Message{textMsg(canonical.RoleUser, "old"), mixed}
	last, query := findPinned(msgs)
	if last != 1 || query != 1 {
		t.Errorf("findPinned = (%d, %d), want (1, 1)", last, query)
	}
	q := queryText(mixed)
	if strings.Contains(q, "zebra") {
		t.Error("queryText leaked ToolResult content into the query")
	}
	if !strings.Contains(q, "alpha") {
		t.Error("queryText missing the Text part")
	}
}

func TestFindPinned_NoQuery(t *testing.T) {
	msgs := []canonical.Message{
		textMsg(canonical.RoleAssistant, "assistant only"),
	}
	last, query := findPinned(msgs)
	if last != 0 || query != -1 {
		t.Errorf("findPinned = (%d, %d), want (0, -1)", last, query)
	}
}

// pad makes s a valid >= minCandidateLen candidate without adding
// query-overlapping tokens (padding token is unique per call site).
func pad(s, filler string) string {
	return s + " " + strings.Repeat(filler+" ", 200/(len(filler)+1)+1)
}

func TestPrune_ElidesZeroOverlapFirst_StopsAtBudget(t *testing.T) {
	question := "diagnose the database connection timeout"
	msgs := []canonical.Message{
		textMsg(canonical.RoleUser, pad("weather and cooking chatter", "fillerx")),                             // zero overlap → elide first
		textMsg(canonical.RoleAssistant, pad("the database connection timeout came from the pool", "fillery")), // overlaps → keep
		textMsg(canonical.RoleUser, question),
	}
	// Budget such that ONE elision suffices.
	budget := estMessagesTokens(msgs) - 40
	mutable := func(i int) bool { return i < 2 }
	pruneByRelevance(context.Background(), msgs, mutable, 2, budget)

	if !strings.Contains(flattenText(msgs[0]), "elided as low-relevance") {
		t.Error("zero-overlap message not elided first")
	}
	if !strings.Contains(flattenText(msgs[1]), "database connection timeout") {
		t.Error("overlapping message elided — ranking or budget stop broken")
	}
}

func TestPrune_AllZeroOverlap_NoMutation(t *testing.T) {
	// SAFETY STOP: no candidate shares a single token with the question
	// → stage 4 must elide NOTHING, even under an impossible budget.
	msgs := []canonical.Message{
		textMsg(canonical.RoleUser, pad("weather chatter", "fillerx")),
		textMsg(canonical.RoleAssistant, pad("cooking recipes", "fillery")),
		textMsg(canonical.RoleUser, "quantum flux capacitor calibration"),
	}
	snapshot := []string{flattenText(msgs[0]), flattenText(msgs[1])}
	pruneByRelevance(context.Background(), msgs, func(i int) bool { return i < 2 }, 2, 1)
	if flattenText(msgs[0]) != snapshot[0] || flattenText(msgs[1]) != snapshot[1] {
		t.Error("zero-evidence transcript was pruned — safety stop violated")
	}
}

func TestPrune_EqualScoresOldestFirst(t *testing.T) {
	shared := pad("alpha beta shared terms", "fillerz")
	msgs := []canonical.Message{
		textMsg(canonical.RoleUser, shared), // identical docs → identical scores
		textMsg(canonical.RoleUser, shared),
		textMsg(canonical.RoleUser, "alpha beta question"),
	}
	budget := estMessagesTokens(msgs) - 40 // one elision suffices
	pruneByRelevance(context.Background(), msgs, func(i int) bool { return i < 2 }, 2, budget)
	if !strings.Contains(flattenText(msgs[0]), "elided") {
		t.Error("tie-break must elide the OLDEST message first")
	}
	if flattenText(msgs[1]) != shared {
		t.Error("newer equal-score message elided out of order")
	}
}

func TestPrune_PIITokensAreNeverLexicalEvidence(t *testing.T) {
	// Revision-4 MAJOR: encrypted PII tokens are STRIPPED from ranking
	// text. Different ciphertexts of the same entity must contribute
	// ZERO evidence — a question that is only Alice's encrypted email
	// must not authorize eliding unrelated context just because history
	// contains Bob's encrypted email.
	msgs := []canonical.Message{
		textMsg(canonical.RoleUser, pad("important unrelated context", "fillerp")),
		textMsg(canonical.RoleUser, pad("note about", "fillerq")+" [PII:Email:BBBBbbbb2222_-]"), // Bob's email
		textMsg(canonical.RoleUser, "[PII:Email:AAAAaaaa1111_-]"),                               // question = Alice's email only
	}
	snap := []string{flattenText(msgs[0]), flattenText(msgs[1])}
	pruneByRelevance(context.Background(), msgs, func(i int) bool { return i < 2 }, 2, 1)
	if flattenText(msgs[0]) != snap[0] || flattenText(msgs[1]) != snap[1] {
		t.Error("PII placeholders acted as lexical evidence — zero-overlap stop bypassed")
	}
	// The PII-only question IS a textual user turn (raw selection,
	// revision-5 MAJOR) — but its SANITIZED query must be empty.
	if !hasQueryTerms(msgs[2]) {
		t.Error("PII-only current question must still be SELECTED as the query turn")
	}
	if qi := newQueryIndex(queryText(msgs[2])); qi.n != 0 {
		t.Errorf("sanitized PII-only question produced %d query terms, want 0", qi.n)
	}

	// Candidate-side stripping: the user literally typing "email" must
	// not lexically match a candidate whose only "email" is inside a
	// [PII:Email:...] wire token.
	msgs2 := []canonical.Message{
		textMsg(canonical.RoleUser, pad("archive discussion", "fillerz")+" [PII:Email:CCCCcccc3333_-]"),
		textMsg(canonical.RoleUser, "check that email thread"),
	}
	snap2 := flattenText(msgs2[0])
	pruneByRelevance(context.Background(), msgs2, func(i int) bool { return i < 1 }, 1, 1)
	if flattenText(msgs2[0]) != snap2 {
		t.Error(`query word "email" matched a PII wire token — candidate-side stripping failed`)
	}
}

func TestFindPinned_WhitespaceOnlyTextFallsBackToRealQuestion(t *testing.T) {
	// Revision-4 MINOR: a mixed Anthropic turn whose Text block is
	// whitespace/punctuation-only is NOT the query — selection falls
	// back to the earlier real question.
	blank := canonical.Message{
		Role: canonical.RoleUser,
		Content: []canonical.ContentPart{
			{Kind: canonical.ContentKindToolResult, ToolResult: &canonical.ToolResultPart{ToolUseID: "t1", Content: "output"}},
			{Kind: canonical.ContentKindText, Text: "   \n\t"},
		},
	}
	punct := textMsg(canonical.RoleUser, "?!... ---")
	msgs := []canonical.Message{
		textMsg(canonical.RoleUser, "what does the log say?"),
		punct,
		blank,
	}
	last, query := findPinned(msgs)
	if last != 2 || query != 0 {
		t.Errorf("findPinned = (%d, %d), want (2, 0) — token-empty Text must not be the query", last, query)
	}
}

func TestPrune_HashModePIITokensAreNeverLexicalEvidence(t *testing.T) {
	// Revision-5 MAJOR: hash mode emits [ENTITY:h-xxxxxxxx]
	// (pii.ApplyMode). Alice's and Bob's DIFFERENT hashed emails share
	// the synthetic tokens "email"/"h" — those must never authorize
	// pruning. Covers global hash mode and per-entity hash actions
	// (different entities, same grammar).
	msgs := []canonical.Message{
		textMsg(canonical.RoleUser, pad("important unrelated context", "fillerh")),
		textMsg(canonical.RoleUser, pad("note about", "fillerk")+" [EMAIL:h-bbbbbbbb] [PHONE:h-cccccccc]"),
		textMsg(canonical.RoleUser, "[EMAIL:h-aaaaaaaa]"), // question = Alice's hashed email only
	}
	snap := []string{flattenText(msgs[0]), flattenText(msgs[1])}
	pruneByRelevance(context.Background(), msgs, func(i int) bool { return i < 2 }, 2, 1)
	if flattenText(msgs[0]) != snap[0] || flattenText(msgs[1]) != snap[1] {
		t.Error("hash-mode tokens acted as lexical evidence — zero-overlap stop bypassed")
	}
	if qi := newQueryIndex(queryText(msgs[2])); qi.n != 0 {
		t.Errorf("sanitized hash-only question produced %d query terms, want 0", qi.n)
	}
	// Countered replace tokens are stripped too; bare [EMAIL] is the
	// documented residual and is NOT (single weak term).
	if got := stripPII("see [EMAIL_2] and [EMAIL]"); strings.Contains(got, "EMAIL_2") || !strings.Contains(got, "[EMAIL]") {
		t.Errorf("replace-shape stripping wrong: %q", got)
	}
}

func TestPrune_OverCapQueryFailsClosed(t *testing.T) {
	// Revision-5 MAJOR: a >maxQueryTerms question must make stage 4 a
	// no-op — never rank on the first-4096-unique-terms prefix. The
	// candidate overlaps a term from EARLY in the prefix, which is
	// exactly the attacker-controlled evidence that must not count.
	var qb strings.Builder
	qb.WriteString("incidental ")
	for i := 0; i < maxQueryTerms+100; i++ {
		fmt.Fprintf(&qb, "w%05d ", i)
	}
	qb.WriteString("why did payment reconciliation fail")
	msgs := []canonical.Message{
		textMsg(canonical.RoleUser, pad("incidental overlap here", "fillerv")),
		textMsg(canonical.RoleUser, qb.String()),
	}
	snap := flattenText(msgs[0])
	pruneByRelevance(context.Background(), msgs, func(i int) bool { return i < 1 }, 1, 1)
	if flattenText(msgs[0]) != snap {
		t.Error("over-cap query ranked on its prefix and elided — must fail closed")
	}
}

func TestPrune_PreCancelledContextIsNoop(t *testing.T) {
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	msgs := []canonical.Message{
		textMsg(canonical.RoleUser, pad("alpha history", "fillerc")),
		textMsg(canonical.RoleUser, "alpha question"),
	}
	snap := flattenText(msgs[0])
	pruneByRelevance(cctx, msgs, func(i int) bool { return i < 1 }, 1, 1)
	if flattenText(msgs[0]) != snap {
		t.Error("cancelled ctx still pruned")
	}
}

func TestPrune_ToolCallCarriersIneligible(t *testing.T) {
	// Both canonical carriers: Message.ToolCalls (OpenAI/Ollama) and
	// ContentKindToolUse parts (Anthropic).
	filler := pad("query terms overlap here", "fillerq")
	withToolCalls := canonical.Message{
		Role:      canonical.RoleAssistant,
		Content:   []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: filler}},
		ToolCalls: []canonical.ToolCall{{ID: "c1", Name: "grep"}},
	}
	withToolUse := canonical.Message{
		Role: canonical.RoleAssistant,
		Content: []canonical.ContentPart{
			{Kind: canonical.ContentKindText, Text: filler},
			{Kind: canonical.ContentKindToolUse, ToolUse: &canonical.ToolUsePart{ID: "t1", Name: "grep"}},
		},
	}
	msgs := []canonical.Message{withToolCalls, withToolUse, textMsg(canonical.RoleUser, "query terms overlap")}
	pruneByRelevance(context.Background(), msgs, func(i int) bool { return i < 2 }, 2, 1)
	if !strings.Contains(flattenText(msgs[0]), filler[:30]) {
		t.Error("ToolCalls carrier elided")
	}
	if !strings.Contains(flattenText(msgs[1]), filler[:30]) {
		t.Error("ToolUse-part carrier elided")
	}
}

func TestPrune_NegativeQueryIdxIsNoop(t *testing.T) {
	msgs := []canonical.Message{textMsg(canonical.RoleAssistant, pad("text", "fillern"))}
	before := flattenText(msgs[0])
	pruneByRelevance(context.Background(), msgs, func(int) bool { return true }, -1, 1)
	if flattenText(msgs[0]) != before {
		t.Error("queryIdx=-1 must be a complete no-op")
	}
}

// BenchmarkPruneManyMessages locks the running-delta complexity fix
// (second-pass MAJOR-2): stage 4 over thousands of messages must be
// roughly linear — compare ns/op when message count doubles.
func BenchmarkPruneManyMessages(b *testing.B) {
	const n = 4000
	base := make([]canonical.Message, 0, n+1)
	for i := 0; i < n; i++ {
		base = append(base, textMsg(canonical.RoleUser, pad("history message about topics", "fillerb")))
	}
	base = append(base, textMsg(canonical.RoleUser, "question about topics"))
	mutable := func(i int) bool { return i < n }
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		msgs := make([]canonical.Message, len(base))
		copy(msgs, base)
		pruneByRelevance(context.Background(), msgs, mutable, n, 100)
	}
}
