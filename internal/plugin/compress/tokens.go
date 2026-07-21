// internal/plugin/compress/tokens.go

// Package compress implements CompressionHook, a canonical-layer PreHook
// that shrinks the transcript actually sent to kiro. Pipeline (cheap →
// expensive; later stages run only while still over budget):
//
//  1. blank-line/trailing-space cleanup  (low-loss normalization)
//  2. stale tool-result truncation       (head+tail, elision marker)
//  3. exact-duplicate collapse           (agent loops repeat themselves)
//  4. local BM25 relevance pruning       (lexical overlap vs. the user's
//     question; lowest score first; elides NOTHING on zero overlap)
//
// The budget is re-checked between stages: once the estimate is at or
// under BudgetTokens, no further (lossier) stage runs.
//
// Hard invariants (never violated regardless of budget): req.System,
// req.Tools, RoleSystem messages, the last ProtectTail messages, AND
// both pinned indices — the current inbound turn and the latest
// user-text question (findPinned) — pass through verbatim; ToolCallID /
// ToolCalls / ContentKindToolUse parts are never removed.
//
// Compression is an optimization and must never be able to break a
// request: Before always returns (nil, nil). A failure or panic forwards
// the request with whatever stages had already completed applied (stages
// mutate in place; there is no rollback) — in particular a stage-4
// failure forwards the stages 1-3 result. This is the same wording as
// docs/operating.md; keep them in sync.
//
// Port of the Node ACP gateway's v3 compressMessages() (acp_server/
// acp-server-ollama.js) onto the otto-gateway canonical types.
package compress

import (
	"encoding/json"
	"strings"

	"otto-gateway/internal/canonical"
)

// estimateTokens is the bytes/4 heuristic: UTF-8 byte length, NOT
// characters (diverges from Node's UTF-16-code-unit count by up to ~3×
// on CJK). Intentionally crude — it gates "is compression worth running"
// and "are we under budget yet", not billing.
func estimateTokens(text string) int {
	return (len(text) + 3) / 4
}

// flattenText concatenates the prose-bearing content of a message:
// ContentKindText and ContentKindThinking parts (both serialized to the
// ACP wire — [User]/[Assistant] and [Reasoning] sections) plus
// ToolResultPart.Content. Images, ToolUse parts, and ToolCalls are
// excluded (structured, counted separately by estMessagesTokens).
func flattenText(m canonical.Message) string {
	var b strings.Builder
	for _, p := range m.Content {
		switch p.Kind {
		case canonical.ContentKindText, canonical.ContentKindThinking:
			b.WriteString(p.Text)
		case canonical.ContentKindToolResult:
			if p.ToolResult != nil {
				b.WriteString(p.ToolResult.Content)
			}
		}
	}
	return b.String()
}

// estMessageTokens estimates one message's byte-based token footprint AS
// SERIALIZED by build_acp — which is ROLE-DEPENDENT (build_acp.go:171-214;
// revision-4 fix: a role-blind walker counted carriers ACP ignores):
//
//   - RoleAssistant: Text ([Assistant]) + Thinking ([Reasoning]) + the
//     tool-call carrier ACP renders — ToolUse parts PREFERRED, falling
//     back to message-level ToolCalls only when no ToolUse part exists
//     (mirrors appendAssistantToolCalls, so both-carrier messages are
//     not double-counted).
//   - RoleTool: Text parts only ([Tool result] via joinTextParts —
//     ToolResult PARTS on a RoleTool message are not serialized).
//   - RoleUser (default branch): ToolResult parts + Text parts.
//     Thinking/ToolUse/ToolCalls on a user message are never rendered.
//   - RoleSystem: 0 (skipped by the transcript loop; see
//     estMessagesTokens).
func estMessageTokens(m canonical.Message) int {
	sum := 0
	switch m.Role {
	case canonical.RoleSystem:
		return 0
	case canonical.RoleAssistant:
		renderedToolUse := false
		for _, p := range m.Content {
			switch p.Kind {
			case canonical.ContentKindText, canonical.ContentKindThinking:
				sum += estimateTokens(p.Text)
			case canonical.ContentKindToolUse:
				if p.ToolUse != nil {
					argsJSON, err := json.Marshal(p.ToolUse.Input)
					if err != nil {
						argsJSON = nil // estimation only — never fail on odd args
					}
					sum += estimateTokens(p.ToolUse.Name) + estimateTokens(string(argsJSON))
					renderedToolUse = true
				}
			}
		}
		if !renderedToolUse {
			for _, tc := range m.ToolCalls {
				argsJSON, err := json.Marshal(tc.Arguments)
				if err != nil {
					argsJSON = nil
				}
				sum += estimateTokens(tc.Name) + estimateTokens(string(argsJSON))
			}
		}
	case canonical.RoleTool:
		for _, p := range m.Content {
			if p.Kind == canonical.ContentKindText {
				sum += estimateTokens(p.Text)
			}
		}
	default: // RoleUser
		for _, p := range m.Content {
			switch p.Kind {
			case canonical.ContentKindText:
				sum += estimateTokens(p.Text)
			case canonical.ContentKindToolResult:
				if p.ToolResult != nil {
					sum += estimateTokens(p.ToolResult.Content)
				}
			}
		}
	}
	return sum
}

// estMessagesTokens sums estMessageTokens over the transcript, SKIPPING
// RoleSystem messages — build_acp.go:173-174 never serializes them (the
// system prompt rides req.System), and the Ollama adapter retains
// RoleSystem entries in Messages after hoisting (ollama/wire.go:333-338)
// while OpenAI/Anthropic remove them. Counting them would make the same
// logical prompt cross the trigger on one surface and not another. One
// estimator feeds the trigger gate, the budget loop, and the saved-token
// metric so they can never disagree.
func estMessagesTokens(msgs []canonical.Message) int {
	sum := 0
	for i := range msgs {
		if msgs[i].Role == canonical.RoleSystem {
			continue
		}
		sum += estMessageTokens(msgs[i])
	}
	return sum
}
