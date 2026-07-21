// internal/plugin/compress/prune.go

package compress

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/plugin/pii"
)

// minCandidateLen: messages with less relevance text than this (UTF-8
// bytes) are never elision candidates — cheap to keep, and short
// messages are often structural.
const minCandidateLen = 200

// piiRankingTokenRe matches EVERY machine-generated PII token grammar
// pii.ApplyMode can emit (modes.go:152-184) plus the encrypt wire token
// (pii.decryptTokenRe, pii.go:388):
//
//	encrypt:            [PII:Entity:base64url]
//	hash:               [ENTITY:h-xxxxxxxx]
//	replace (counter):  [ENTITY_2]
//
// The hash and countered-replace alternatives are built from the REAL
// recognizer entity vocabulary (pii.SourceAuditNames, upper-cased exactly
// as pii.ApplyMode upper-cases entity names) rather than an arbitrary
// [A-Z][A-Z0-9_]* alphabet — review LOW-4: the previous grammar stripped
// ordinary bracketed identifiers ("[ISO_9001]", "[ERROR_404]",
// "[RFC_2616]") that merely LOOK like PII tokens but name no recognizer.
// Fail-closed but lossy; constraining to the actual registry removes the
// loss without weakening the safety property (an entity NOT in the
// registry can never be one of pii's synthetic tokens).
//
// Documented residual (accepted): bare replace tokens "[EMAIL]" and
// mask-mode output are indistinguishable from ordinary bracketed text /
// prose and are NOT stripped — a bare entity token is a single weak,
// idf-discounted term; drop mode emits nothing to strip.
var piiRankingTokenRe = func() *regexp.Regexp {
	names := make([]string, 0, 16)
	for _, n := range pii.SourceAuditNames() {
		names = append(names, regexp.QuoteMeta(strings.ToUpper(n)))
	}
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) }) // longest-first alternation
	alt := strings.Join(names, "|")
	return regexp.MustCompile(
		`\[PII:[A-Za-z0-9_]+:[A-Za-z0-9_-]+\]` + // encrypt wire token (matches pii.decryptTokenRe)
			`|\[(?:` + alt + `):h-[A-Za-z0-9_-]+\]` + // hash-mode token, real entities only
			`|\[(?:` + alt + `)_[0-9]+\]`) // countered replace token, real entities only
}()

// stripPII REMOVES synthetic PII tokens from ranking text (replaced by
// a space so neighbors don't merge into one token). Removal, not
// entity-placeholder substitution: placeholder shapes tokenize into
// shared terms ("pii"/"email"/"h") that two UNRELATED protected values
// would have in common — synthetic evidence that bypasses the
// zero-overlap safety stop authorizing lossy pruning (revision-4 MAJOR;
// hash grammar added by revision-5 MAJOR). RANKING ONLY: dupKey and the
// transcript itself always keep exact tokens.
func stripPII(text string) string {
	return piiRankingTokenRe.ReplaceAllString(text, " ")
}

// findPinned returns the two indices compress() keeps immutable beyond
// the protected tail. They are DISTINCT concepts (third-pass MAJOR-1):
//
//   - lastIdx: the latest non-RoleSystem message — the CURRENT INBOUND
//     TURN. On OpenAI/Ollama a follow-up can end in a RoleTool result
//     the model must consume; on Anthropic the equivalent tool_result
//     rides a RoleUser message. Whatever its role, the newest turn is
//     what the model is being asked to act on — never compressed, even
//     at ProtectTail=0.
//   - queryIdx: the latest RoleUser message whose RAW Text parts
//     produce at least one tokenizer term — the user's actual QUESTION,
//     stage 4's relevance query. Often equal to lastIdx; distinct when
//     the transcript ends in tool output. An Anthropic pure-tool_result
//     turn is RoleUser but has no Text part, so it is never the query;
//     neither is a turn whose Text is whitespace/punctuation-only
//     (revision-4 MINOR — falls back to the prior REAL question).
//     Selection is RAW-text on purpose (revision-5 MAJOR): a question
//     consisting only of redacted PII tokens IS the current question —
//     it must be SELECTED here and then produce an empty sanitized
//     query (stage-4 no-op), never skipped in favor of an OLDER
//     question whose stale evidence would authorize pruning.
//
// Either is -1 when absent.
func findPinned(msgs []canonical.Message) (lastIdx, queryIdx int) {
	lastIdx, queryIdx = -1, -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if lastIdx == -1 && msgs[i].Role != canonical.RoleSystem {
			lastIdx = i
		}
		if queryIdx == -1 && msgs[i].Role == canonical.RoleUser && hasQueryTerms(msgs[i]) {
			queryIdx = i
		}
		if lastIdx != -1 && queryIdx != -1 {
			break
		}
	}
	return lastIdx, queryIdx
}

// hasQueryTerms reports whether m's RAW Text parts yield at least one
// tokenizer term — it answers "is this a textual user turn?", NOT "does
// it carry usable evidence". Deliberately unsanitized (revision-5
// MAJOR): stripping here made a PII-only current question invisible and
// selection fell back to an older question, ranking history against
// STALE evidence. Sanitization happens later in queryText; a PII-only
// question then yields qi.n == 0 and stage 4 no-ops. Early-exits on the
// first term; ToolResult content never counts.
func hasQueryTerms(m canonical.Message) bool {
	for _, p := range m.Content {
		if p.Kind != canonical.ContentKindText {
			continue
		}
		found := false
		forEachToken(p.Text, func(string) bool {
			found = true
			return false // stop at first term
		})
		if found {
			return true
		}
	}
	return false
}

// queryText builds the BM25 query from a message's Text parts ONLY,
// joined DIRECTLY exactly as canonical.JoinTextParts (and therefore
// build_acp's [User] section) does — ToolResult content is excluded so
// history is ranked against the user's question, not against tool output
// (a mixed Anthropic tool_result+Text turn contributes only its Text).
// Encrypted PII tokens are stripped so they can never form query terms.
// A direct join (no inserted separator) can only MISS evidence relative
// to the previous '\n'-joined form, never fabricate it — and it exactly
// mirrors what the wire renders (review HIGH-2).
func queryText(m canonical.Message) string {
	return stripPII(canonical.JoinTextParts(m.Content))
}

// acpProse projects the prose a message actually contributes to the ACP
// prompt, mirroring build_acp's per-role branches and section order
// (build_acp.go:171-215): assistant = joined Text then joined Thinking;
// tool = joined Text; user = each ToolResult part's Content then joined
// Text; system = nothing. Sections are separated by '\n' so tokens can
// never straddle two sections; chunks WITHIN one carrier are joined
// directly, exactly as the wire does (a real merged token is real
// evidence). Carriers ACP ignores for the role contribute nothing —
// content invisible on the wire must be neither ranking evidence nor
// grounds for duplicate collapse (review HIGH-2 / MEDIUM-3).
func acpProse(m canonical.Message) string {
	var sections []string
	switch m.Role {
	case canonical.RoleSystem:
		// Never serialized (req.System carries it) — no evidence.
	case canonical.RoleAssistant:
		if t := canonical.JoinTextParts(m.Content); t != "" {
			sections = append(sections, t)
		}
		if th := canonical.JoinThinkingParts(m.Content); th != "" {
			sections = append(sections, th)
		}
	case canonical.RoleTool:
		if t := canonical.JoinTextParts(m.Content); t != "" {
			sections = append(sections, t)
		}
	default: // RoleUser
		for _, p := range m.Content {
			if p.Kind == canonical.ContentKindToolResult && p.ToolResult != nil && p.ToolResult.Content != "" {
				sections = append(sections, p.ToolResult.Content)
			}
		}
		if t := canonical.JoinTextParts(m.Content); t != "" {
			sections = append(sections, t)
		}
	}
	return strings.Join(sections, "\n")
}

// relevanceText builds a candidate's scoring document from acpProse — the
// ROLE-AWARE, section-ordered, separator-joined prose the message
// actually contributes to the ACP prompt (review HIGH-2). Everything
// stays in-process — stage 4 is local BM25, nothing leaves the gateway —
// so an assistant's Thinking may participate in ranking even though the
// PII hook never redacts it. Encrypted PII tokens are stripped: they must
// be neither noise nor evidence (revision-4 MAJOR).
func relevanceText(m canonical.Message) string {
	return stripPII(acpProse(m))
}

// pruneByRelevance is stage 4: score every eligible candidate against
// the user's question with local BM25 and elide candidates in
// ascending-score order until the budget is met.
//
// Eligibility: mutable (compress() already excludes the protected tail,
// both pinned indices, and RoleSystem), at least minCandidateLen bytes
// of relevance text, and NOT carrying a tool invocation on either
// carrier (Message.ToolCalls or ContentKindToolUse parts — eliding a
// tool_use breaks tool_use/tool_result pairing on the Anthropic
// surface).
//
// SAFETY STOP (zero evidence): if EVERY eligible candidate scores
// exactly 0 — no lexical overlap with the question at all — stage 4
// elides NOTHING. No recency, length, or arbitrary-order fallback:
// without evidence the scorer has no basis to choose what the model
// doesn't need, and budget_unmet records the shortfall.
//
// Determinism: equal scores tie-break by ascending message index
// (oldest first), so output is stable run-to-run.
//
// A free function (not a Hook method): it needs only ctx and the
// budget, which keeps Task ordering stub-free and makes it directly
// testable. ctx cancellation (checked inside bm25Rank between
// documents) turns the whole stage into a no-op.
func pruneByRelevance(ctx context.Context, msgs []canonical.Message, mutable func(int) bool, queryIdx, budgetTokens int) {
	if ctx.Err() != nil || queryIdx < 0 || queryIdx >= len(msgs) {
		return
	}
	qi := newQueryIndex(queryText(msgs[queryIdx]))
	if qi.n == 0 || qi.overflow {
		// No sanitized query terms (e.g. PII-only question), or the
		// question exceeded maxQueryTerms — either way there is no
		// COMPLETE evidence basis; fail closed (budget_unmet records it).
		return
	}

	type candidate struct {
		i   int
		doc string
	}
	var cands []candidate
	for i := range msgs {
		if i%64 == 0 && ctx.Err() != nil {
			return // cancelled mid-projection — stage 4 is a no-op
		}
		if !mutable(i) || carriesToolCall(msgs[i]) {
			continue
		}
		t := relevanceText(msgs[i])
		if len(t) < minCandidateLen {
			continue
		}
		cands = append(cands, candidate{i: i, doc: t})
	}
	if len(cands) == 0 {
		return
	}

	docs := make([]string, len(cands))
	for k := range cands {
		docs[k] = cands[k].doc
	}
	scores := bm25Rank(ctx, qi, docs)
	if scores == nil {
		return // ctx cancelled mid-scan — treat stage 4 as a no-op
	}

	anyPositive := false
	for _, s := range scores {
		if s > 0 {
			anyPositive = true
			break
		}
	}
	if !anyPositive {
		return // zero lexical evidence — never prune blind
	}

	order := make([]int, len(cands))
	for k := range order {
		order[k] = k
	}
	sort.Slice(order, func(a, b int) bool {
		if scores[order[a]] != scores[order[b]] {
			return scores[order[a]] < scores[order[b]]
		}
		return cands[order[a]].i < cands[order[b]].i // deterministic: oldest first
	})

	// Running-delta budget loop (second-pass MAJOR-2 fix retained):
	// estimate the transcript once, re-estimate only the message each
	// elision mutates, stop the moment the budget is met.
	total := estMessagesTokens(msgs)
	for _, k := range order {
		if total <= budgetTokens {
			break
		}
		i := cands[k].i
		before := estMessageTokens(msgs[i])
		replaceText(&msgs[i], fmt.Sprintf("[message #%d elided as low-relevance to the current request]", i+1))
		total += estMessageTokens(msgs[i]) - before
	}
}

// carriesToolCall reports whether a message carries a tool invocation on
// either canonical carrier: message-level ToolCalls (OpenAI/Ollama) or a
// ContentKindToolUse part (Anthropic). Such messages are never elided.
func carriesToolCall(m canonical.Message) bool {
	if len(m.ToolCalls) > 0 {
		return true
	}
	for _, p := range m.Content {
		if p.Kind == canonical.ContentKindToolUse {
			return true
		}
	}
	return false
}
