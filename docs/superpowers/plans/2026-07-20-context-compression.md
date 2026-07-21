# Context Compression (CompressionHook, self-contained BM25 stage 4) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an opt-in transcript-compression PreHook to otto-gateway that shrinks the canonical request sent to kiro (blank-line cleanup → stale-tool-result truncation → duplicate collapse → local BM25 lexical relevance pruning), toggleable per process (env), per request (`X-Compression` header), and per model (`+compress`/`-compress` model-name suffix). The feature is entirely self-contained: the deployed topology is otto-gateway + kiro-cli only, so no stage may depend on a network endpoint, external model, or sidecar.

> **Revision note (2026-07-20):** reworked after the adversarial review in `docs/2026-07-20-context-compression-REVIEW-FINDINGS.md` (verdict REWORK, 13 MAJOR / 5 MINOR — all findings verified against the repo and accepted). Key deltas: stage 1 no longer collapses horizontal whitespace runs (code-indentation corruption); the token estimator and duplicate-identity keys are structure-aware (Thinking + ToolUse input included); `/v1/completions` is a fifth directive site; stage 4 excludes its own anchor; the existing five-hook chain-order regression test is updated, not left to break. (This pass's embeddings-endpoint deltas — env-var graduation, batching caps, URL redaction — were later superseded by revision 3's BM25 replacement.)
>
> **Revision note 2 (2026-07-20, second review pass — 10 MAJOR / 8 MINOR, all verified and accepted):** the anchor (last user message) is now immutable across ALL stages, not just stage 4; the budget is re-checked between stages so later lossy stages never run once it is met; stage 4 uses running per-message token deltas instead of quadratic full-transcript rescans; the text sent to `EMBEDDINGS_URL` is an explicit **embedding projection** that excludes Thinking (PII skips Thinking — `internal/plugin/pii/pii.go:535` — so it must never leave the box) and normalizes `[PII:entity:payload]` ciphertext tokens to stable `[PII:entity]` placeholders (fresh AES-GCM nonces would otherwise defeat duplicate collapse and the embed cache); `dupKey` fields are length-prefixed (delimiter-injection-proof); embed errors identify the endpoint by origin only and never echo response bodies; a failure cooldown + 3s call timeout bounds the latency a dead endpoint can add, with failure counters in `Describe`; the estimator skips `RoleSystem` messages (ACP never serializes them; Ollama retains them in `Messages`) and counts only the tool-call carrier ACP actually renders. This pass also hardened the then-current HTTP-embeddings stage 4 (Thinking-free projection, credential-free errors, batching, timeout+cooldown) — all **superseded by revision 3's BM25 replacement** and no longer prescribed anywhere in this plan.
>
> **Revision note 3 (2026-07-20, third review pass — 2 MAJOR / 3 MINOR / 1 NIT, plus an architecture correction):** the external-embeddings assumption was wrong — the deployed topology is otto-gateway + kiro-cli only. Stage 4 is now a **deterministic in-process BM25 lexical scorer** (stdlib `strings`/`unicode`/`math` only) with a hard safety stop: if every candidate has zero lexical overlap with the user's question, nothing is elided. The planned `internal/embed` package, `EMBEDDINGS_URL`, the `EMBEDDING_MODEL_DEFAULT` graduation (REL-CFG-03 warn-and-ignore stays intact, as does `CLAUDE.md`'s reserved annotation), the LRU cache, timeouts, cooldowns, and embed failure counters are all removed. MAJOR fixes: current-turn protection and relevance-query selection are SEPARATE pins (`findPinned` — tool-result turns are `RoleTool` on OpenAI/Ollama but `RoleUser` on Anthropic, so "last RoleUser" conflated the two concepts); `dupKey` uses **exact ciphertext** for PII tokens (entity-only normalization falsely collapsed messages differing only in encrypted values — `stablePII` now applies to BM25 ranking text only). MINOR fixes: pinned indices join the property test's full-snapshot set; the response `model` echo contract is specified (echo the caller's original directive-bearing string on every branch) and tested; stage 1 is described honestly as low-loss, not lossless. NIT: the header parser no longer claims vocabulary parity with `getEnvBool`.
>
> **Revision note 4 (2026-07-20, revision-3 BM25-focused review — 1 CRITICAL / 3 MAJOR / 7 MINOR / 1 NIT, all verified and accepted):** CRITICAL — the naive scorer's `O(unique query terms × candidates)` map scans were remotely amplifiable inside a valid 4-MiB request; stage 4 is now a **sparse single-pass scorer**: query terms get stable first-seen IDs (capped at `maxQueryTerms`), each document is scanned exactly once via a zero-allocation streaming tokenizer, `df`/`tf` accumulate sparsely, and scoring iterates each doc's matched IDs in ascending order — which also fixes the MAJOR nondeterministic map-iteration float accumulation. `pruneByRelevance` is now ctx-aware (cancellation between documents → stage-4 no-op), with a large-unique-query benchmark (`ReportAllocs`) scaling both query vocabulary and candidate count. MAJOR — encrypted PII tokens are now **stripped** from ranking text entirely (revision 3's `[PII:entity]` placeholders manufactured shared `pii`/`email` tokens — synthetic evidence that bypassed the zero-overlap safety stop; a PII-only query now makes stage 4 a no-op). MAJOR — `dupKey` now includes message-level `ToolCallID` (byte-identical results for different tool invocations no longer collapse). MINORs — query selection requires at least one actual tokenizer term (whitespace/punctuation-only Text falls back to the prior real question); a second, stage-4-forced rapid property guarantees pruning is actually exercised with multipart candidates; the estimator is **role-aware**, mirroring build_acp's per-role branches exactly; the global failure guarantee now uses the precise partial-application wording; the Task 9 test matrix reflects `/v1/completions`' JSON-only stream-downgrade contract; the smoke command sets a valid budget; unsegmented CJK inertness is documented. NIT — `CLAUDE.md` removed from the final `git add`.
>
> **Revision note 5 (2026-07-20, sparse-BM25 review — 3 MAJOR / 2 MINOR, verdict SHIP WITH FIXES, all accepted):** three ways incomplete/synthetic/stale evidence could still authorize pruning are closed. (1) Query selection now tests the RAW text for tokenizer terms — a PII-only current question IS the current question (its sanitized query comes up empty → stage 4 no-ops) instead of silently falling back to an older question whose stale evidence would rank history. (2) Query-index overflow **fails closed**: more than `maxQueryTerms` unique terms sets `overflow` and stage 4 no-ops — it never ranks on an attacker-choosable 4,096-term prefix. (3) `stripPII` covers every machine-generated PII token grammar — encrypt `[PII:Entity:…]`, hash `[ENTITY:h-…]` (`pii.ApplyMode`, modes.go:152-184), and countered replace `[ENTITY_N]` — with a documented residual: bare `[ENTITY]` replace tokens and masked values are indistinguishable from ordinary text and are not stripped. MINORs: the adversarial benchmark's `shared` term moved to the FRONT of the query (the 10k/20k rows previously hit the cap before indexing it and never exercised the match path — rows now scale within the cap, plus a separate over-cap fail-closed row), and cancellation is checked at prune entry, per candidate projection, and periodically inside large document scans. Final narrow pass confirmed all fixes; one residual explicitly ACCEPTED: cancellation checks are token-granular, so query projection or a multi-megabyte single token can complete after disconnect — bounded by the 4-MiB cap, no safety/correctness impact, and a byte-level check would tax the hot loop of every live request. **Verdict: SHIP WITH FIXES, all fixes incorporated — this revision is the implementation baseline.**

**Architecture:** A new `internal/plugin/compress` package implements `engine.PreHook` (mutating `req` in place, same discipline as `pii.PIIRedactionHook`), inserted in the chain after `piiHook` and before `loggingHook`. The package is fully self-contained: stages 1–3 are deterministic text transforms and stage 4 is a local BM25 lexical scorer — no network, no external process, no model weights, no new dependencies. (`internal/embed` keeps its existing `.gitkeep` placeholder untouched; this feature does not use it.) Compression is an optimization and must NEVER be able to break a request: the hook always returns `(nil, nil)` and recovers its own panics.

**Tech Stack:** Go 1.26.5, stdlib only for production code (no new module dependencies; `CGO_ENABLED=0` and all four GOOS/GOARCH cross-builds plus the binary-size gate remain valid — stage 4 adds only `strings`/`unicode`/`math`). Tests use the already-present `pgregory.net/rapid` (property tests), `go.uber.org/goleak` (TestMain gate), and stdlib `net/http/httptest`.

## Global Constraints

- **No new entries in `go.mod`.** Production code: stdlib only. Tests: `rapid`, `goleak` (already direct deps).
- **Never abort a request.** `Hook.Before` must always return `(nil, nil)`. A failure or panic forwards the request with whatever stages had already completed applied (stages mutate in place; there is no rollback) — a failure before stage 1 forwards the transcript untouched. `engine.callPreHookSafe` converts hook panics into request-aborting errors, so the hook installs its own `defer/recover`. (Same wording as the package doc and `docs/operating.md` — keep all three in sync.)
- **Fully self-contained stage 4.** No network call, external process, model download, or CGO for relevance scoring — `CGO_ENABLED=0`, all four GOOS/GOARCH cross-builds, and the binary-size gate remain valid. Nothing in this feature ever sends transcript content anywhere except to kiro-cli.
- **Hard invariants (never violated regardless of config):** `req.System` untouched; `req.Tools` untouched; `RoleSystem` messages untouched; the last `ProtectTail` messages untouched; `Message.ToolCallID`, `ToolCalls`, and `ContentKindToolUse` parts never removed (tool_use/tool_result pairing must survive for the Anthropic surface); BOTH pinned indices — the current inbound turn (latest non-system message, any role) and the latest user-text question — are untouched by **every** stage even when `ProtectTail=0`; stage 4 elides nothing when every candidate has zero lexical overlap with the question.
- **Toggle precedence:** `X-Compression` header > model-name suffix > `COMPRESSION_ENABLED` env default. `ENABLED_HOOKS` remains the hard kill switch (two-knob model, Phase 8 D-02).
- **Env names (Node ACP gateway parity):** `COMPRESSION_ENABLED` (default `false`), `COMPRESS_TRIGGER_TOKENS` (default `6000`), `COMPRESS_BUDGET_TOKENS` (default `4000`), `COMPRESS_PROTECT_TAIL` (default `4`), `COMPRESS_TOOL_KEEP` (default `1200`). That is the complete set — stage 4 needs no configuration. `EMBEDDING_MODEL_DEFAULT` stays **reserved** exactly as today (REL-CFG-03 warn-and-ignore untouched, `CLAUDE.md` annotation untouched); this feature does not consume it.
- **Estimation unit honesty:** the token heuristic is UTF-8 **bytes**/4, not characters (Go `len(string)`). This diverges from Node's UTF-16-code-unit count by up to ~3× on CJK text; acceptable because it gates "is compression worth running", not billing — but every name, comment, and log label must say bytes, never chars.
- **Coverage:** ACP-serialized content the estimator/walkers must count: `ContentKindText`, `ContentKindThinking` (serialized as `[Reasoning]` — `internal/engine/build_acp.go:188-191`), `ContentKindToolResult`, `ContentKindToolUse.Input` (serialized as `[Assistant tool call]` — `build_acp.go:259-275`), and message-level `ToolCalls`.
- **Commits:** conventional style (`feat(compress): …`), NO `Co-Authored-By` trailer of any kind.
- **Every new test package gets a goleak `TestMain`** (mirrors `internal/plugin/testmain_test.go`).
- Run `gofmt` on every file; verify with `make vet` and `make test` at final task.
- Repo root for all paths below: `/Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway`.

---

### Task 1: compress package skeleton — token estimation + text walkers

**Files:**
- Create: `internal/plugin/compress/tokens.go`
- Create: `internal/plugin/compress/tokens_test.go`
- Create: `internal/plugin/compress/testmain_test.go`

**Interfaces:**
- Consumes: `otto-gateway/internal/canonical` (`Message`, `ContentPart`, `ContentKindText`, `ContentKindToolResult`).
- Produces (used by Tasks 4, 5, 8): `estimateTokens(text string) int`, `flattenText(m canonical.Message) string`, `estMessageTokens(m canonical.Message) int`, `estMessagesTokens(msgs []canonical.Message) int` (all unexported, same package). `estMessageTokens` is the per-message unit Task 8's running-delta budget loop depends on.

- [x] **Step 1: Write goleak TestMain**

```go
// internal/plugin/compress/testmain_test.go
// Package compress — whitebox test file.
//
// TestMain installs the goleak goroutine-leak gate for the entire compress
// package test suite (mirrors internal/plugin/testmain_test.go).
package compress

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
```

- [x] **Step 2: Write the failing tests**

```go
// internal/plugin/compress/tokens_test.go
package compress

import (
	"strings"
	"testing"

	"otto-gateway/internal/canonical"
)

func TestEstimateTokens(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abcd", 1},
		{"abcde", 2}, // 5 chars → ceil(5/4) = 2
		{"12345678", 2},
	}
	for _, c := range cases {
		if got := estimateTokens(c.in); got != c.want {
			t.Errorf("estimateTokens(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func textMsg(role canonical.MessageRole, text string) canonical.Message {
	return canonical.Message{
		Role:    role,
		Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: text}},
	}
}

func TestFlattenText_TextThinkingAndToolResult(t *testing.T) {
	// Thinking is serialized to the ACP wire ([Reasoning] section,
	// build_acp.go:188-191) so it MUST count toward size and identity.
	m := canonical.Message{
		Role: canonical.RoleAssistant,
		Content: []canonical.ContentPart{
			{Kind: canonical.ContentKindText, Text: "head"},
			{Kind: canonical.ContentKindThinking, Text: "+think"},
			{Kind: canonical.ContentKindToolResult, ToolResult: &canonical.ToolResultPart{Content: "-tail"}},
			{Kind: canonical.ContentKindImage, Image: &canonical.ImagePart{DataBase64: "ignored"}},
		},
	}
	if got := flattenText(m); got != "head+think-tail" {
		t.Errorf("flattenText = %q, want %q", got, "head+think-tail")
	}
}

func TestEstMessagesTokens_IncludesToolCalls(t *testing.T) {
	msgs := []canonical.Message{
		textMsg(canonical.RoleUser, "12345678"), // 2 tokens
		{
			Role: canonical.RoleAssistant,
			ToolCalls: []canonical.ToolCall{
				{Name: "grep", Arguments: map[string]any{"q": "x"}},
			},
		},
	}
	got := estMessagesTokens(msgs)
	// 2 for the user text + at least 1 for the tool-call name/args JSON.
	if got < 3 {
		t.Errorf("estMessagesTokens = %d, want >= 3 (text + tool-call overhead)", got)
	}
}

func TestEstMessagesTokens_IncludesToolUseInput(t *testing.T) {
	// Anthropic-surface tool calls ride ContentKindToolUse parts, not
	// Message.ToolCalls — build_acp serializes their Input as an
	// [Assistant tool call] section, so a 20 KB Input must move the
	// estimate even with near-zero plain text.
	fatArg := strings.Repeat("x", 20_000)
	msgs := []canonical.Message{{
		Role: canonical.RoleAssistant,
		Content: []canonical.ContentPart{{
			Kind:    canonical.ContentKindToolUse,
			ToolUse: &canonical.ToolUsePart{ID: "t1", Name: "grep", Input: map[string]any{"q": fatArg}},
		}},
	}}
	if got := estMessagesTokens(msgs); got < 5000 {
		t.Errorf("estMessagesTokens = %d, want >= 5000 (ToolUse.Input JSON counted)", got)
	}
}

func TestEstMessagesTokens_SkipsRoleSystem(t *testing.T) {
	// build_acp never serializes RoleSystem transcript messages (the
	// system prompt rides req.System), and Ollama retains them in
	// Messages after hoisting — counting them would let the same logical
	// prompt cross the trigger on Ollama but not OpenAI/Anthropic.
	msgs := []canonical.Message{
		textMsg(canonical.RoleSystem, strings.Repeat("s", 8000)),
		textMsg(canonical.RoleUser, "12345678"), // 2 tokens
	}
	if got := estMessagesTokens(msgs); got != 2 {
		t.Errorf("estMessagesTokens = %d, want 2 (RoleSystem must not count)", got)
	}
}

func TestEstMessageTokens_PrefersToolUseCarrier(t *testing.T) {
	// appendAssistantToolCalls renders ToolUse parts and SKIPS ToolCalls
	// when any ToolUse part rendered — a message carrying both must not
	// be double-counted.
	arg := strings.Repeat("x", 4000)
	both := canonical.Message{
		Role: canonical.RoleAssistant,
		Content: []canonical.ContentPart{{
			Kind:    canonical.ContentKindToolUse,
			ToolUse: &canonical.ToolUsePart{ID: "t1", Name: "grep", Input: map[string]any{"q": arg}},
		}},
		ToolCalls: []canonical.ToolCall{{ID: "t1", Name: "grep", Arguments: map[string]any{"q": arg}}},
	}
	only := both
	only.ToolCalls = nil
	if estMessageTokens(both) != estMessageTokens(only) {
		t.Errorf("both-carriers message double-counted: %d vs %d",
			estMessageTokens(both), estMessageTokens(only))
	}
}

func TestEstMessageTokens_RoleKindMatrix(t *testing.T) {
	// Revision-4: the estimator mirrors build_acp's per-role branches —
	// carriers ACP never serializes for a role must count ZERO there.
	big := strings.Repeat("x", 4000) // 1000 tokens
	thinking := canonical.ContentPart{Kind: canonical.ContentKindThinking, Text: big}
	toolResult := canonical.ContentPart{Kind: canonical.ContentKindToolResult,
		ToolResult: &canonical.ToolResultPart{ToolUseID: "t", Content: big}}
	toolUse := canonical.ContentPart{Kind: canonical.ContentKindToolUse,
		ToolUse: &canonical.ToolUsePart{ID: "t", Name: "grep", Input: map[string]any{"q": big}}}
	cases := []struct {
		name    string
		m       canonical.Message
		counted bool
	}{
		{"assistant-thinking", canonical.Message{Role: canonical.RoleAssistant, Content: []canonical.ContentPart{thinking}}, true},
		{"assistant-tooluse", canonical.Message{Role: canonical.RoleAssistant, Content: []canonical.ContentPart{toolUse}}, true},
		{"user-thinking-ignored", canonical.Message{Role: canonical.RoleUser, Content: []canonical.ContentPart{thinking}}, false},
		{"user-toolcalls-ignored", canonical.Message{Role: canonical.RoleUser, ToolCalls: []canonical.ToolCall{{Name: "grep", Arguments: map[string]any{"q": big}}}}, false},
		{"user-toolresult", canonical.Message{Role: canonical.RoleUser, Content: []canonical.ContentPart{toolResult}}, true},
		{"tool-text", textMsg(canonical.RoleTool, big), true},
		{"tool-toolresult-part-ignored", canonical.Message{Role: canonical.RoleTool, Content: []canonical.ContentPart{toolResult}}, false},
		{"system-anything", textMsg(canonical.RoleSystem, big), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := estMessageTokens(c.m)
			if c.counted && got < 500 {
				t.Errorf("estMessageTokens = %d, want large (ACP serializes this carrier for this role)", got)
			}
			if !c.counted && got != 0 {
				t.Errorf("estMessageTokens = %d, want 0 (ACP ignores this carrier for this role)", got)
			}
		})
	}
}
```

- [x] **Step 3: Run tests to verify they fail**

Run: `cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && go test ./internal/plugin/compress/ -v`
Expected: FAIL — `undefined: estimateTokens`, `undefined: flattenText`, `undefined: estMessagesTokens` (compile errors).

- [x] **Step 4: Write the implementation**

```go
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
```

- [x] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/plugin/compress/ -v`
Expected: PASS (7 tests).

- [x] **Step 6: Commit**

```bash
git add internal/plugin/compress/
git commit -m "feat(compress): token estimation and text walkers for compression hook"
```

---

### Task 2: model-suffix directive parsing

**Files:**
- Create: `internal/plugin/compress/directive.go`
- Create: `internal/plugin/compress/directive_test.go`

**Interfaces:**
- Produces (used by Task 9 adapters and Task 5 hook): `SplitCompressDirective(model string) (base string, directive *bool)` (exported), `MetadataKey = "compress"` (exported const — the `canonical.ChatRequest.Metadata` key carrying the parsed suffix directive as a `bool`).

- [x] **Step 1: Write the failing tests**

```go
// internal/plugin/compress/directive_test.go
package compress

import "testing"

func TestSplitCompressDirective(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }
	cases := []struct {
		in       string
		wantBase string
		wantDir  *bool
	}{
		{"qwen-2.5+compress", "qwen-2.5", boolPtr(true)},
		{"qwen-2.5-compress", "qwen-2.5", boolPtr(false)},
		{"claude-sonnet-4-6+compress", "claude-sonnet-4-6", boolPtr(true)},
		{"qwen-2.5+COMPRESS", "qwen-2.5", boolPtr(true)}, // case-insensitive
		{"qwen-2.5", "qwen-2.5", nil},
		{"auto", "auto", nil},
		{"", "", nil},
		{"compress", "compress", nil},            // no +/- separator → not a directive
		{"model+compression", "model+compression", nil}, // suffix must be exactly "compress"
		{"+compress", "+compress", nil},          // empty base → literal model name, NOT a directive
		{"-compress", "-compress", nil},          // empty base → literal model name, NOT a directive
		// KNOWN COLLISION (documented, Node-syntax parity): a real model
		// whose id happens to end in "-compress" is indistinguishable from
		// a disable directive and gets stripped. There is no escape syntax;
		// docs/operating.md carries the caveat.
		{"vendor/model-compress", "vendor/model", boolPtr(false)},
	}
	for _, c := range cases {
		base, dir := SplitCompressDirective(c.in)
		if base != c.wantBase {
			t.Errorf("SplitCompressDirective(%q) base = %q, want %q", c.in, base, c.wantBase)
		}
		switch {
		case dir == nil && c.wantDir != nil:
			t.Errorf("SplitCompressDirective(%q) dir = nil, want %v", c.in, *c.wantDir)
		case dir != nil && c.wantDir == nil:
			t.Errorf("SplitCompressDirective(%q) dir = %v, want nil", c.in, *dir)
		case dir != nil && c.wantDir != nil && *dir != *c.wantDir:
			t.Errorf("SplitCompressDirective(%q) dir = %v, want %v", c.in, *dir, *c.wantDir)
		}
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/plugin/compress/ -run TestSplitCompressDirective -v`
Expected: FAIL — `undefined: SplitCompressDirective`.

- [x] **Step 3: Write the implementation**

```go
// internal/plugin/compress/directive.go
package compress

import "regexp"

// MetadataKey is the canonical.ChatRequest.Metadata key under which the
// adapters store the parsed model-suffix directive (a bool: true for
// "+compress", false for "-compress"). Metadata is the documented
// free-form per-request seam (canonical/chat.go) — the suffix originates
// in the request BODY (the model name), so it rides the request, not ctx.
const MetadataKey = "compress"

// directiveRe matches a trailing "+compress" / "-compress" directive on a
// model name (case-insensitive). Mirrors the Node gateway's
// /([+-])(skills|compress)$/i — only the compress directive exists here.
var directiveRe = regexp.MustCompile(`(?i)([+-])compress$`)

// SplitCompressDirective strips a trailing +compress/-compress directive
// from a model name. Returns the base model name and a nil directive when
// no suffix is present. LangFlow can select a model name but cannot send
// HTTP headers, so the suffix is its only per-request compression lever.
//
// A directive that would leave an EMPTY base ("+compress" alone) is not a
// directive — the input is returned verbatim as a model name. Stripping
// to "" would slip past surface nonempty-model validation (anthropic
// validates wire.Model BEFORE conversion, handlers.go:113-117) and the
// engine treats "" as do-not-SetModel (engine.go:256-263), silently
// changing semantics instead of erroring.
//
// KNOWN COLLISION (accepted, Node-syntax parity — /([+-])compress$/i): a
// real model id ending in "-compress" is parsed as a disable directive.
// No escape syntax exists; documented in docs/operating.md.
//
// Adapters MUST call this before any surface-specific model normalization
// (e.g. anthropic's normalizeClaudeModelID) — the anthropic hyphen-version
// regex is $-anchored and would not fire on a suffixed name.
func SplitCompressDirective(model string) (string, *bool) {
	m := directiveRe.FindStringSubmatch(model)
	if m == nil {
		return model, nil
	}
	base := model[:len(model)-len(m[0])]
	if base == "" {
		return model, nil
	}
	on := m[1] == "+"
	return base, &on
}
```

- [x] **Step 4: Run test to verify it passes**

Run: `go test ./internal/plugin/compress/ -run TestSplitCompressDirective -v`
Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add internal/plugin/compress/directive.go internal/plugin/compress/directive_test.go
git commit -m "feat(compress): +compress/-compress model-suffix directive parsing"
```

---

### Task 3: per-request header ctx helpers

**Files:**
- Create: `internal/plugin/compress/ctx.go`
- Create: `internal/plugin/compress/ctx_test.go`

**Interfaces:**
- Produces (used by Task 5 hook and Task 9 adapters): `WithHeaderDirective(ctx context.Context, on bool) context.Context`, `HeaderDirectiveFromContext(ctx context.Context) (on bool, ok bool)`, `ParseHeaderValue(v string) (on bool, ok bool)`. Follows the `pii.WithSummary` precedent: ctx helpers live in the hook's own package; adapters stamp, the hook reads. Key-collision safety via unexported struct key (same argument as `plugin/surface.go`).

- [x] **Step 1: Write the failing tests**

```go
// internal/plugin/compress/ctx_test.go
package compress

import (
	"context"
	"testing"
)

func TestHeaderDirective_RoundTrip(t *testing.T) {
	ctx := context.Background()

	if _, ok := HeaderDirectiveFromContext(ctx); ok {
		t.Fatal("unstamped ctx: ok = true, want false")
	}

	on, ok := HeaderDirectiveFromContext(WithHeaderDirective(ctx, true))
	if !ok || !on {
		t.Errorf("stamped true: got (%v, %v), want (true, true)", on, ok)
	}

	on, ok = HeaderDirectiveFromContext(WithHeaderDirective(ctx, false))
	if !ok || on {
		t.Errorf("stamped false: got (%v, %v), want (false, true)", on, ok)
	}
}

func TestParseHeaderValue(t *testing.T) {
	cases := []struct {
		in     string
		wantOn bool
		wantOK bool
	}{
		{"1", true, true},
		{"true", true, true},
		{"on", true, true},
		{"TRUE", true, true},
		{" 1 ", true, true}, // whitespace trimmed
		{"0", false, true},
		{"false", false, true},
		{"off", false, true},
		{" 0 ", false, true},
		// Invalid values are IGNORED (ok=false → fall through to
		// suffix/env), never treated as enable — "00", "no", garbage must
		// not switch destructive compression on.
		{"00", false, false},
		{"no", false, false},
		{"yes", false, false},
		{"garbage", false, false},
		{"", false, false},
	}
	for _, c := range cases {
		on, ok := ParseHeaderValue(c.in)
		if on != c.wantOn || ok != c.wantOK {
			t.Errorf("ParseHeaderValue(%q) = (%v, %v), want (%v, %v)", c.in, on, ok, c.wantOn, c.wantOK)
		}
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/plugin/compress/ -run TestHeaderDirective -v`
Expected: FAIL — `undefined: HeaderDirectiveFromContext`.

- [x] **Step 3: Write the implementation**

```go
// internal/plugin/compress/ctx.go
// WithHeaderDirective / HeaderDirectiveFromContext — the X-Compression
// header seam. The per-surface adapters stamp the header value onto ctx in
// stampPluginCtx (mirroring plugin.WithRequestID / pii.WithSummary);
// CompressionHook.Before reads it. Header wins over the model-suffix
// directive and over the process-wide COMPRESSION_ENABLED default.
//
// Key-collision safety: unexported struct key — an external package cannot
// construct an equal key value (same argument as plugin/surface.go).
package compress

import (
	"context"
	"strings"
)

type ctxKey struct{ name string }

var headerDirectiveKey = ctxKey{name: "compress-header"}

// WithHeaderDirective returns a child ctx carrying the X-Compression
// header decision. Adapters call it only when the header is PRESENT —
// absence must fall through to the suffix/env default (tri-state).
func WithHeaderDirective(ctx context.Context, on bool) context.Context {
	return context.WithValue(ctx, headerDirectiveKey, on)
}

// HeaderDirectiveFromContext returns the stamped header decision.
// ok=false means the header was absent (comma-ok idiom).
func HeaderDirectiveFromContext(ctx context.Context) (bool, bool) {
	v, ok := ctx.Value(headerDirectiveKey).(bool)
	return v, ok
}

// ParseHeaderValue interprets an X-Compression header value as a strict
// tri-state: "1"/"true"/"on" → enable, "0"/"false"/"off" → disable
// (case-insensitive, whitespace-trimmed), anything else → ok=false,
// meaning IGNORE the header and fall through to the suffix/env default.
// NOTE: this vocabulary is deliberately WIDER than config getEnvBool
// (which accepts only 1/true/0/false) — "on"/"off" work in the header
// but NOT in COMPRESSION_ENABLED. Anything-nonzero-means-on would let
// "false"/"off"/"00" silently enable destructive compression.
func ParseHeaderValue(v string) (on bool, ok bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "on":
		return true, true
	case "0", "false", "off":
		return false, true
	}
	return false, false
}
```

- [x] **Step 4: Run test to verify it passes**

Run: `go test ./internal/plugin/compress/ -run TestHeaderDirective -v`
Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add internal/plugin/compress/ctx.go internal/plugin/compress/ctx_test.go
git commit -m "feat(compress): X-Compression header ctx stamp helpers"
```

---

### Task 4: deterministic stages 1–3 (whitespace, truncation, duplicate collapse)

**Files:**
- Create: `internal/plugin/compress/stages.go`
- Create: `internal/plugin/compress/stages_test.go`

**Interfaces:**
- Consumes: `flattenText` (Task 1).
- Produces (used by Task 5 hook and Task 8 pruning): `normalizeMessageWhitespace(m *canonical.Message)`, `truncateToolResults(m *canonical.Message, keep int)`, `collapseDuplicates(msgs []canonical.Message, mutable func(int) bool)`, `dupKey(m canonical.Message) string`, `replaceText(m *canonical.Message, stub string)`, `runeSafeCut(s string, n int) string`, `middleTruncate(text string, keep int) string`, `const minDupLen = 200` (all unexported). (PII-token handling for ranking lives in Task 8's `stripPII` — nothing PII-related remains in this task.)

**Design decisions (second review, amended by third-pass MAJOR-2 and revision-4):** (a) `dupKey` fields are **length-prefixed** — raw 0x1E/0x1F delimiters can legally appear inside canonical text, so a crafted message could otherwise forge a colliding key. (b) `dupKey` uses **exact text, including exact PII ciphertext**, and includes the message-level `ToolCallID` (revision-4 MAJOR: byte-identical tool output satisfying different invocations must stay distinct — ACP renders the ID into the `[Tool result (id: …)]` section). Entity-only PII normalization inside identity keys is forbidden — two messages identical except for *different encrypted values* would collapse as "exact duplicates", and the surviving token decrypts to the wrong plaintext. Encrypt-mode duplicates of genuinely identical plaintext therefore rarely collapse (fresh nonces) — a missed optimization, accepted. For stage-4 RANKING, PII tokens are handled in Task 8 by **stripping them entirely** (`stripPII`) — revision 3's `[PII:entity]` placeholders manufactured shared tokens that counted as lexical evidence. (c) Stage 1 also cleans Thinking parts — they are prose serialized as `[Reasoning]`.

**Design decision (review MAJOR-1, wording tightened per third-pass MINOR):** stage 1 does NOT collapse horizontal whitespace runs. The Node gateway's `[ \t]{2,}` → `" "` pass destroys Python/YAML/Makefile indentation in any older code snippet, and build_acp forwards that text verbatim to the model — semantic corruption, not compression. Stage 1 is limited to stripping trailing whitespace at line ends and collapsing 3+ consecutive newlines to 2. Call this **low-loss normalization, not lossless**: it DOES alter exact bytes, and content whose meaning depends on them (Markdown two-trailing-space hard breaks, exact-output fixtures, patches, triple-quoted strings with trailing spaces or 3+ blank lines) is changed. That boundary is accepted, deliberate, and pinned by regression fixtures — never describe stage 1 as lossless in code or docs. This is a deliberate divergence from Node.

- [x] **Step 1: Write the failing tests**

```go
// internal/plugin/compress/stages_test.go
package compress

import (
	"strings"
	"testing"
	"unicode/utf8"

	"otto-gateway/internal/canonical"
)

func TestNormalizeWhitespace(t *testing.T) {
	// Trailing space/tab stripped at line ends; 5 newlines → 2. INTERIOR
	// horizontal whitespace is untouched — no [ \t]{2,} collapse (that
	// would corrupt indented code; deliberate divergence from Node).
	in := "line1   \nline2\n\n\n\n\nline3\ta  \t b"
	want := "line1\nline2\n\nline3\ta  \t b"
	if got := normalizeWhitespace(in); got != want {
		t.Errorf("normalizeWhitespace = %q, want %q", got, want)
	}
}

func TestNormalizeWhitespace_PreservesCodeIndentation(t *testing.T) {
	// Regression lock for review MAJOR-1: indentation-significant code in
	// old transcript messages must survive stage 1 byte-for-byte.
	code := "def f():\n    if x:\n        return {\n            \"k\": 1,\n        }\nkey:\n  nested: true\n\ttab-indented"
	if got := normalizeWhitespace(code); got != code {
		t.Errorf("indentation mutated:\ngot  %q\nwant %q", got, code)
	}
}

func TestNormalizeWhitespace_AcceptedLossBoundary(t *testing.T) {
	// Third-pass MINOR: stage 1 is LOW-LOSS, not lossless. These fixtures
	// pin the exact accepted mutation boundary so it can never silently
	// widen (and so the docs' honesty claims stay checkable).
	cases := []struct{ name, in, want string }{
		// ACCEPTED loss: Markdown hard break (two trailing spaces) → soft break.
		{"markdown-hard-break", "line one  \nline two", "line one\nline two"},
		// ACCEPTED loss: 3+ blank-line runs collapse, even inside what a
		// client meant as an exact fixture.
		{"blank-run", "a\n\n\n\nb", "a\n\nb"},
		// PRESERVED: single and double newlines, interior runs, tabs.
		{"exact-double", "a\n\nb", "a\n\nb"},
		{"interior-runs", "col1    col2\tcol3", "col1    col2\tcol3"},
	}
	for _, c := range cases {
		if got := normalizeWhitespace(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestMiddleTruncate(t *testing.T) {
	short := strings.Repeat("a", 100)
	if got := middleTruncate(short, 50); got != short {
		t.Errorf("short text mutated: %q", got)
	}

	long := strings.Repeat("a", 1000) + strings.Repeat("b", 1000)
	got := middleTruncate(long, 100)
	if !strings.HasPrefix(got, strings.Repeat("a", 100)) {
		t.Error("head not preserved")
	}
	if !strings.HasSuffix(got, strings.Repeat("b", 100)) {
		t.Error("tail not preserved")
	}
	if !strings.Contains(got, "chars omitted") {
		t.Error("elision marker missing")
	}
	if len(got) >= len(long) {
		t.Error("no shrinkage")
	}
}

func TestMiddleTruncate_RuneSafe(t *testing.T) {
	// Multibyte runes positioned to straddle the cut points.
	long := strings.Repeat("é", 2000) // 2 bytes each
	got := middleTruncate(long, 101)  // 101 lands mid-rune
	if !utf8.ValidString(got) {
		t.Error("middleTruncate produced invalid UTF-8")
	}
}

func TestTruncateToolResults(t *testing.T) {
	big := strings.Repeat("x", 5000)
	m := canonical.Message{
		Role: canonical.RoleTool,
		Content: []canonical.ContentPart{
			{Kind: canonical.ContentKindText, Text: big},
			{Kind: canonical.ContentKindToolResult, ToolResult: &canonical.ToolResultPart{ToolUseID: "t1", Content: big}},
		},
	}
	truncateToolResults(&m, 100)
	if len(m.Content[0].Text) >= 5000 {
		t.Error("RoleTool text part not truncated")
	}
	if len(m.Content[1].ToolResult.Content) >= 5000 {
		t.Error("ToolResult content not truncated")
	}
	if m.Content[1].ToolResult.ToolUseID != "t1" {
		t.Error("ToolUseID lost — tool pairing broken")
	}
}

func TestTruncateToolResults_NonToolTextUntouched(t *testing.T) {
	big := strings.Repeat("x", 5000)
	m := textMsg(canonical.RoleAssistant, big)
	truncateToolResults(&m, 100)
	if m.Content[0].Text != big {
		t.Error("assistant text part truncated — stage 2 must only touch tool output")
	}
}

func TestCollapseDuplicates(t *testing.T) {
	big := strings.Repeat("payload ", 50) // > minDupLen
	msgs := []canonical.Message{
		textMsg(canonical.RoleUser, big),
		textMsg(canonical.RoleAssistant, "short"), // < minDupLen — never collapsed
		textMsg(canonical.RoleUser, big),          // duplicate of #0
		textMsg(canonical.RoleAssistant, big),     // same text, different role — NOT a duplicate
	}
	all := func(int) bool { return true }
	collapseDuplicates(msgs, all)

	if flattenText(msgs[0]) != big {
		t.Error("first occurrence must survive")
	}
	if got := flattenText(msgs[2]); !strings.Contains(got, "duplicate of earlier message #1") {
		t.Errorf("duplicate not collapsed: %q", got)
	}
	if flattenText(msgs[3]) != big {
		t.Error("different role collapsed — role must be part of the identity key")
	}
}

func TestCollapseDuplicates_RespectsMutable(t *testing.T) {
	big := strings.Repeat("payload ", 50)
	msgs := []canonical.Message{
		textMsg(canonical.RoleUser, big),
		textMsg(canonical.RoleUser, big),
	}
	collapseDuplicates(msgs, func(i int) bool { return false })
	if flattenText(msgs[1]) != big {
		t.Error("immutable message collapsed")
	}
}

func TestCollapseDuplicates_DelimiterInjectionSafe(t *testing.T) {
	// Review 2 MAJOR-6: canonical text may legally contain 0x1E/0x1F.
	// A single text part "A<RS>k<US>B" must NOT collide with separate
	// text "A" + thinking "B" parts — length prefixes make the encoding
	// injection-proof.
	a := strings.Repeat("A", 150)
	b := strings.Repeat("B", 150)
	forged := textMsg(canonical.RoleUser, a+"\x1ek\x1f"+b)
	genuine := canonical.Message{
		Role: canonical.RoleUser,
		Content: []canonical.ContentPart{
			{Kind: canonical.ContentKindText, Text: a},
			{Kind: canonical.ContentKindThinking, Text: b},
		},
	}
	if dupKey(forged) == dupKey(genuine) {
		t.Error("dupKey forged via embedded delimiters")
	}
}

func TestCollapseDuplicates_DifferentCiphertextIsNotDuplicate(t *testing.T) {
	// Third-pass MAJOR-2: different [PII:Email:...] payloads can encode
	// DIFFERENT email addresses. They must NEVER collapse as "exact
	// duplicates" — dupKey uses exact ciphertext, not entity-only
	// normalization.
	pre := strings.Repeat("same message body ", 15)
	m1 := textMsg(canonical.RoleUser, pre+"[PII:Email:AAAAaaaa1111_-]")
	m2 := textMsg(canonical.RoleUser, pre+"[PII:Email:BBBBbbbb2222_-]")
	msgs := []canonical.Message{m1, m2}
	full := flattenText(msgs[1])
	collapseDuplicates(msgs, func(int) bool { return true })
	if flattenText(msgs[1]) != full {
		t.Error("messages differing only in PII ciphertext were collapsed — the model could echo the wrong decryptable token")
	}
	// IDENTICAL ciphertext (kiro echoing the same token) still collapses.
	m3 := textMsg(canonical.RoleUser, pre+"[PII:Email:AAAAaaaa1111_-]")
	msgs2 := []canonical.Message{m1, m3}
	collapseDuplicates(msgs2, func(int) bool { return true })
	if !strings.Contains(flattenText(msgs2[1]), "duplicate of earlier message #1") {
		t.Error("byte-identical messages failed to collapse")
	}
}

func TestCollapseDuplicates_DifferentToolCallIDIsNotDuplicate(t *testing.T) {
	// Revision-4 MAJOR: byte-identical tool output satisfying DIFFERENT
	// invocations (call_A vs call_B) is not a duplicate — ACP renders
	// the ToolCallID into the [Tool result (id:…)] section.
	out := strings.Repeat("identical tool output ", 15)
	mk := func(id string) canonical.Message {
		m := textMsg(canonical.RoleTool, out)
		m.ToolCallID = id
		return m
	}
	msgs := []canonical.Message{mk("call_A"), mk("call_B")}
	collapseDuplicates(msgs, func(int) bool { return true })
	if flattenText(msgs[1]) != out {
		t.Error("results for different tool invocations collapsed")
	}
	// Same ID (a true resend) still collapses.
	msgs2 := []canonical.Message{mk("call_A"), mk("call_A")}
	collapseDuplicates(msgs2, func(int) bool { return true })
	if !strings.Contains(flattenText(msgs2[1]), "duplicate of earlier message #1") {
		t.Error("identical-ID duplicate failed to collapse")
	}
}

func TestCollapseDuplicates_MultipartNotConfusedWithFlatText(t *testing.T) {
	// Review MAJOR-5: a message {text "A", tool-result "B"} and a plain
	// text message "AB" flatten to the same string but are structurally
	// different (ACP serializes a [Tool result] section for one and not
	// the other). dupKey must keep them distinct.
	a := strings.Repeat("A", 150)
	b := strings.Repeat("B", 150)
	msgs := []canonical.Message{
		{
			Role: canonical.RoleUser,
			Content: []canonical.ContentPart{
				{Kind: canonical.ContentKindText, Text: a},
				{Kind: canonical.ContentKindToolResult, ToolResult: &canonical.ToolResultPart{ToolUseID: "call-1", Content: b}},
			},
		},
		textMsg(canonical.RoleUser, a+b), // same flattened bytes, different structure
	}
	collapseDuplicates(msgs, func(int) bool { return true })
	if flattenText(msgs[1]) != a+b {
		t.Error("structurally different message collapsed as duplicate")
	}
}

func TestReplaceText_PreservesStructure(t *testing.T) {
	m := canonical.Message{
		Role:       canonical.RoleTool,
		ToolCallID: "call-9",
		Content: []canonical.ContentPart{
			{Kind: canonical.ContentKindText, Text: "old text"},
			{Kind: canonical.ContentKindThinking, Text: "old thinking"}, // prose — dropped, not structural
			{Kind: canonical.ContentKindImage, Image: &canonical.ImagePart{DataBase64: "imgdata"}},
			{Kind: canonical.ContentKindToolResult, ToolResult: &canonical.ToolResultPart{ToolUseID: "t2", Content: "old result"}},
		},
	}
	replaceText(&m, "[stub]")
	if m.ToolCallID != "call-9" {
		t.Error("ToolCallID lost")
	}
	if flattenText(m) != "[stub]" {
		t.Errorf("flattened = %q, want only the stub once", flattenText(m))
	}
	foundImage := false
	for _, p := range m.Content {
		if p.Kind == canonical.ContentKindImage && p.Image != nil && p.Image.DataBase64 == "imgdata" {
			foundImage = true
		}
	}
	if !foundImage {
		t.Error("image part dropped")
	}
	for _, p := range m.Content {
		if p.Kind == canonical.ContentKindToolResult && p.ToolResult.ToolUseID != "t2" {
			t.Error("ToolResult.ToolUseID lost")
		}
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/plugin/compress/ -run 'TestNormalize|TestMiddle|TestTruncate|TestCollapse|TestReplace' -v`
Expected: FAIL — undefined functions.

- [x] **Step 3: Write the implementation**

```go
// internal/plugin/compress/stages.go
package compress

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"otto-gateway/internal/canonical"
)

// minDupLen: messages shorter than this are never collapsed as duplicates
// (short acks like "ok" legitimately repeat). Node-parity value.
const minDupLen = 200

var (
	trailingWSRe = regexp.MustCompile(`(?m)[ \t]+$`)
	tripleNLRe   = regexp.MustCompile(`\n{3,}`)
)

// normalizeWhitespace is stage 1: strip trailing whitespace at line ends
// and collapse 3+ consecutive newlines to 2. That is ALL — interior
// horizontal whitespace is never touched. The Node gateway additionally
// collapses [ \t]{2,} runs to one space; that pass rewrites the
// indentation of any Python/YAML/Makefile snippet in older messages into
// semantically different code (review MAJOR-1), so it is deliberately
// omitted here.
func normalizeWhitespace(text string) string {
	text = trailingWSRe.ReplaceAllString(text, "")
	return tripleNLRe.ReplaceAllString(text, "\n\n")
}

// normalizeMessageWhitespace applies stage 1 to every prose-bearing part
// (Text, Thinking — both serialized as prose sections — and ToolResult
// content).
func normalizeMessageWhitespace(m *canonical.Message) {
	for j := range m.Content {
		p := &m.Content[j]
		switch p.Kind {
		case canonical.ContentKindText, canonical.ContentKindThinking:
			p.Text = normalizeWhitespace(p.Text)
		case canonical.ContentKindToolResult:
			if p.ToolResult != nil {
				tr := *p.ToolResult // copy-on-write: alias-proof
				tr.Content = normalizeWhitespace(tr.Content)
				p.ToolResult = &tr
			}
		}
	}
}

// runeSafeCut returns s truncated to at most n bytes without splitting a
// UTF-8 rune (backs off to the previous rune start).
func runeSafeCut(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// runeSafeTail returns the last (at most) n bytes of s without splitting
// a UTF-8 rune (advances past a partial leading rune).
func runeSafeTail(s string, n int) string {
	if n >= len(s) {
		return s
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

// middleTruncate is stage 2's cut: keep the head and tail (the signal in
// tool output lives at the edges), elide the middle with a marker. The
// +64 slack means near-limit text is left alone rather than "truncated"
// into something the same size. keep is in BYTES (rune-safe cuts); the
// marker's omitted count is in runes so the label "chars" is honest.
// The keep > len/2 guard both short-circuits pointless truncation AND
// makes the keep*2 below overflow-safe for absurd (but representable)
// keep values.
func middleTruncate(text string, keep int) string {
	if keep > len(text)/2 {
		return text // head+tail would cover (nearly) everything anyway
	}
	if len(text) <= keep*2+64 {
		return text
	}
	head := runeSafeCut(text, keep)
	tail := runeSafeTail(text, keep)
	omitted := utf8.RuneCountInString(text) - utf8.RuneCountInString(head) - utf8.RuneCountInString(tail)
	return fmt.Sprintf("%s\n…[compressed: %d chars omitted]…\n%s", head, omitted, tail)
}

// truncateToolResults is stage 2: middle-truncate stale tool output.
// Applies to ToolResultPart.Content in any message and to text parts of
// RoleTool messages. Assistant/user prose is NOT touched by this stage.
func truncateToolResults(m *canonical.Message, keep int) {
	for j := range m.Content {
		p := &m.Content[j]
		switch p.Kind {
		case canonical.ContentKindToolResult:
			if p.ToolResult != nil {
				tr := *p.ToolResult
				tr.Content = middleTruncate(tr.Content, keep)
				p.ToolResult = &tr
			}
		case canonical.ContentKindText:
			if m.Role == canonical.RoleTool {
				p.Text = middleTruncate(p.Text, keep)
			}
		}
	}
}

// dupKey builds a STRUCTURAL identity for duplicate detection: role plus
// every content part with an explicit kind discriminator, plus tool-call
// identities. Every variable-length field is LENGTH-PREFIXED
// ("<len>:<bytes>") — canonical text is an unrestricted string, so bare
// separator bytes could be forged by message content; length prefixes
// make the encoding injection-proof (review 2 MAJOR-6).
//
// All text is EXACT — including PII ciphertext. Never normalize PII
// tokens here: entity-only equivalence would collapse messages that
// differ only in their encrypted values (third-pass MAJOR-2).
// Encrypt-mode duplicates therefore rarely collapse (fresh nonces);
// that missed optimization is the accepted price of never producing a
// false "exact duplicate". The message-level ToolCallID participates
// too (revision-4 MAJOR): ACP renders it into the [Tool result (id:…)]
// section, so byte-identical output for DIFFERENT invocations is not a
// duplicate.
func dupKey(m canonical.Message) string {
	var b strings.Builder
	field := func(tag string, s string) {
		fmt.Fprintf(&b, "%s%d:%s", tag, len(s), s)
	}
	fmt.Fprintf(&b, "r%d", m.Role)
	field("I", m.ToolCallID)
	for _, p := range m.Content {
		switch p.Kind {
		case canonical.ContentKindText:
			field("t", p.Text)
		case canonical.ContentKindThinking:
			field("k", p.Text)
		case canonical.ContentKindToolResult:
			if p.ToolResult != nil {
				field("rI", p.ToolResult.ToolUseID)
				fmt.Fprintf(&b, "e%t", p.ToolResult.IsError)
				field("rC", p.ToolResult.Content)
			}
		case canonical.ContentKindToolUse:
			if p.ToolUse != nil {
				inputJSON, _ := json.Marshal(p.ToolUse.Input) // best-effort identity
				field("uI", p.ToolUse.ID)
				field("uN", p.ToolUse.Name)
				field("uA", string(inputJSON))
			}
		case canonical.ContentKindImage:
			if p.Image != nil {
				field("iM", p.Image.MIME)
				field("iD", p.Image.DataBase64)
			}
		}
	}
	for _, tc := range m.ToolCalls {
		argsJSON, _ := json.Marshal(tc.Arguments)
		field("cI", tc.ID)
		field("cN", tc.Name)
		field("cA", string(argsJSON))
	}
	return b.String()
}

// collapseDuplicates is stage 3: replace exact structural repeats (same
// dupKey, flattened length >= minDupLen) with a short stub pointing at
// the first occurrence. Agent loops re-send identical blobs turn after
// turn — this is where the big wins usually are.
func collapseDuplicates(msgs []canonical.Message, mutable func(int) bool) {
	seen := make(map[string]int)
	for i := range msgs {
		key := dupKey(msgs[i])
		if !mutable(i) || len(flattenText(msgs[i])) < minDupLen {
			if _, ok := seen[key]; !ok {
				seen[key] = i
			}
			continue
		}
		if first, ok := seen[key]; ok {
			replaceText(&msgs[i], fmt.Sprintf("[duplicate of earlier message #%d omitted]", first+1))
		} else {
			seen[key] = i
		}
	}
}

// replaceText swaps a message's prose content for a stub while preserving
// everything structural: ToolCallID, ToolCalls, image parts, ToolUse
// parts, and ToolResult part identity (ToolUseID / IsError). Thinking
// parts are DROPPED — they are prose ([Reasoning] on the wire), and
// leaving them would defeat the elision (flattenText counts them). For a
// message with a ToolResult part the stub lands INSIDE ToolResult.Content
// so the anthropic adapter still emits a well-formed tool_result block.
func replaceText(m *canonical.Message, stub string) {
	replaced := false
	out := make([]canonical.ContentPart, 0, len(m.Content))
	for _, p := range m.Content {
		switch p.Kind {
		case canonical.ContentKindText, canonical.ContentKindThinking:
			if !replaced {
				out = append(out, canonical.ContentPart{Kind: canonical.ContentKindText, Text: stub})
				replaced = true
			}
			// subsequent text/thinking parts drop — the stub stands in
		case canonical.ContentKindToolResult:
			if p.ToolResult != nil {
				tr := *p.ToolResult
				if !replaced {
					tr.Content = stub
					replaced = true
				} else {
					tr.Content = ""
				}
				p.ToolResult = &tr
			}
			out = append(out, p)
		default:
			out = append(out, p) // images, ToolUse: structural, pass through
		}
	}
	if !replaced {
		out = append(out, canonical.ContentPart{Kind: canonical.ContentKindText, Text: stub})
	}
	m.Content = out
}
```

- [x] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/plugin/compress/ -v`
Expected: PASS (all tests so far).

- [x] **Step 5: Commit**

```bash
git add internal/plugin/compress/stages.go internal/plugin/compress/stages_test.go
git commit -m "feat(compress): deterministic stages — whitespace, tool-result truncation, duplicate collapse"
```

---

### Task 5: the Hook — enablement resolution, pipeline, invariants

**Files:**
- Create: `internal/plugin/compress/hook.go`
- Create: `internal/plugin/compress/hook_test.go`
- Create: `internal/plugin/compress/invariants_test.go` (rapid property test)

**Interfaces:**
- Consumes: everything from Tasks 1–4; `findPinned`/`pruneByRelevance` (Tasks 7–8); `engine.PreHook` (`internal/engine/hooks.go:30`).
- Produces (used by Tasks 10, 11): exported type `Hook` with fields `Enabled bool`, `TriggerTokens, BudgetTokens, ProtectTail, ToolKeep int`, `Logger *slog.Logger`; methods `Name() string` (returns `"CompressionHook"`), `Describe() (string, map[string]any)`, `Before(ctx, *canonical.ChatRequest) (*canonical.ChatResponse, error)`, `Stats() (runs, savedTokens int64)`.

**Ordering note:** this task calls `tokenize`/`findPinned`/`pruneByRelevance`, which are FREE functions delivered by Tasks 7–8 — execute in order **1, 2, 3, 4, 7, 8, 5, 6, 9, 10, 11** and every task compiles green with no temporary stubs.

- [x] **Step 1: Write the failing tests**

```go
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
	req.Metadata = map[string]any{MetadataKey: true}      // suffix says on
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
		textMsg(canonical.RoleAssistant, overlapping), // would score high vs the OLD question
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

func TestBefore_LaterStagesSkippedOnceBudgetMet(t *testing.T) {
	// Review 2 MAJOR-1: if stage 1 (blank-line cleanup) alone reaches the
	// budget, stages 2-3 must not run — the fat tool result stays
	// untruncated and the duplicate pair stays uncollapsed.
	fluffy := strings.Repeat("line\n\n\n\n\n", 200) // shrinks ~40% under stage 1
	toolPayload := strings.Repeat("x", 1200)        // > 2*ToolKeep+64 → WOULD truncate
	dup := strings.Repeat("dup payload ", 30)       // > minDupLen → WOULD collapse
	req := &canonical.ChatRequest{
		Messages: []canonical.Message{
			textMsg(canonical.RoleUser, fluffy),
			{Role: canonical.RoleTool, Content: []canonical.ContentPart{{
				Kind: canonical.ContentKindToolResult,
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
```

```go
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
	})
}
```

**Plus targeted (non-generated) regression cases** in the same file — rapid generation alone can miss rare shapes, so pin the exact review scenarios: (1) a message that is 100% thinking + fat ToolUse Input and zero plain text must CROSS THE TRIGGER (the estimator counts both) while remaining **structurally unchanged** — ToolUse input is never compressed and such a message is excluded from stages 2-4, so the only permitted change is blank-line cleanup of the thinking prose (review 2 MAJOR-10 corrected this oracle: the original "must shrink" was unsatisfiable); (2) an over-budget transcript whose only fat lives in a ToolResult inside a ToolCalls-carrying message (stage 2 may truncate the result; stage 4 must not elide the message); (3) `ProtectTail=0` with an unattainable budget (both pinned messages must survive byte-for-byte — pairs with the Task 5 pin tests).

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/plugin/compress/ -run 'TestBefore|TestDescribe|TestInvariants' -v`
Expected: FAIL — `undefined: Hook`.

- [x] **Step 3: Write the implementation**

```go
// internal/plugin/compress/hook.go
package compress

import (
	"context"
	"log/slog"
	"sync/atomic"

	"otto-gateway/internal/canonical"
)

// Hook is the CompressionHook PreHook. Chain position: after
// PIIRedactionHook (compress redacted text; never resurface raw PII into
// stubs or logs), before LoggingHook (log what is actually sent).
//
// Enabled is the process-wide DEFAULT, not a hard gate — a per-request
// X-Compression header or a +compress/-compress model suffix overrides it
// in either direction. ENABLED_HOOKS remains the hard kill switch.
//
// Safe for concurrent Before calls: config fields are set once at
// construction; runtime state is atomic counters only.
type Hook struct {
	Enabled       bool
	TriggerTokens int
	BudgetTokens  int
	ProtectTail   int
	ToolKeep      int
	Logger        *slog.Logger

	runs     atomic.Int64
	savedTok atomic.Int64

	// budgetUnmet counts runs that ended still over BudgetTokens — the
	// budget is best-effort (pinned/protected/tool-carrying messages are
	// never elided, and stage 4's zero-evidence stop can leave the
	// transcript over budget by design).
	budgetUnmet atomic.Int64
}

// Name reports the filter-discovery name for chain.Filter (Pattern A —
// explicit Name() over reflect for stable API).
func (h *Hook) Name() string { return "CompressionHook" }

// Describe publishes config + lifetime counters for /health/hooks
// (OBSV-04). Everything here is static config or an atomic counter —
// nothing sensitive (stage 4 is local; there is no endpoint to leak).
func (h *Hook) Describe() (string, map[string]any) {
	return "Pre", map[string]any{
		"enabled":          h.Enabled,
		"trigger_tokens":   h.TriggerTokens,
		"budget_tokens":    h.BudgetTokens,
		"protect_tail":     h.ProtectTail,
		"tool_keep":        h.ToolKeep,
		"runs":             h.runs.Load(),
		"tokens_saved_est": h.savedTok.Load(),
		"budget_unmet":     h.budgetUnmet.Load(),
	}
}

// Stats returns the lifetime counters (Prometheus CounterFunc seam —
// see metrics.RegisterCompression).
func (h *Hook) Stats() (runs, savedTokens int64) {
	return h.runs.Load(), h.savedTok.Load()
}

func (h *Hook) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

// Before is the PreHook entry point.
//
// CONTRACT: always returns (nil, nil). engine.callPreHookSafe converts a
// hook panic into a request-ABORTING error, so Before installs its own
// recover — compression must never be able to break a request.
func (h *Hook) Before(ctx context.Context, req *canonical.ChatRequest) (resp *canonical.ChatResponse, err error) {
	defer func() {
		if r := recover(); r != nil {
			h.logger().ErrorContext(ctx, "compress.panic_recovered", "panic", r)
			resp, err = nil, nil
		}
	}()

	if req == nil || len(req.Messages) == 0 {
		return nil, nil
	}

	// Effective enablement: header > model-suffix directive > env default.
	on := h.Enabled
	if v, ok := req.Metadata[MetadataKey].(bool); ok { // nil-map read is safe
		on = v
	}
	if v, ok := HeaderDirectiveFromContext(ctx); ok {
		on = v
	}
	if !on {
		return nil, nil
	}

	h.compress(ctx, req)
	return nil, nil
}

// compress runs the 4-stage pipeline in place. The budget is re-checked
// BETWEEN stages: once the estimate is at or under BudgetTokens no
// further (lossier) stage runs — stage 1 alone reaching the budget must
// not be followed by truncation or collapse (review 2 MAJOR-1).
func (h *Hook) compress(ctx context.Context, req *canonical.ChatRequest) {
	msgs := req.Messages
	before := estMessagesTokens(msgs)
	if before < h.TriggerTokens {
		return // not worth the work
	}
	if before <= h.BudgetTokens {
		return // already within budget (possible when budget == trigger) —
		// never lossily mutate a transcript that already fits
	}

	tailStart := len(msgs) - h.ProtectTail
	if tailStart < 0 {
		tailStart = 0
	}
	// Two pins, immutable across ALL stages (second-pass MAJOR-3 +
	// third-pass MAJOR-1):
	// lastIdx — the current inbound turn (on OpenAI/Ollama a follow-up
	// can END in a RoleTool result the model must consume); queryIdx —
	// the latest user-text question (stage 4's relevance query). With
	// PROTECT_TAIL=0 nothing else protects either one.
	lastIdx, queryIdx := findPinned(msgs)
	mutable := func(i int) bool {
		return i < tailStart && i != lastIdx && i != queryIdx && msgs[i].Role != canonical.RoleSystem
	}
	overBudget := func() bool { return estMessagesTokens(msgs) > h.BudgetTokens }

	// Stage 1: blank-line/trailing-space cleanup (low-loss normalization).
	for i := range msgs {
		if mutable(i) {
			normalizeMessageWhitespace(&msgs[i])
		}
	}
	// Stage 2: stale tool-result truncation.
	if overBudget() {
		for i := range msgs {
			if mutable(i) {
				truncateToolResults(&msgs[i], h.ToolKeep)
			}
		}
	}
	// Stage 3: exact-duplicate collapse.
	if overBudget() {
		collapseDuplicates(msgs, mutable)
	}
	// Stage 4: local BM25 relevance pruning — fully in-process (no
	// network, no external model), so it runs whenever still over
	// budget. Elides nothing when no candidate shares a token with the
	// question (zero-evidence stop) or when there is no user question.
	if overBudget() {
		pruneByRelevance(ctx, msgs, mutable, queryIdx, h.BudgetTokens)
	}

	after := estMessagesTokens(msgs)
	if after > h.BudgetTokens {
		// Best-effort budget: pinned/protected/tool-carrying messages are
		// never elided and zero-evidence stops pruning, so the budget can
		// legitimately go unmet.
		h.budgetUnmet.Add(1)
	}
	if saved := before - after; saved > 0 {
		h.runs.Add(1)
		h.savedTok.Add(int64(saved))
		h.logger().DebugContext(ctx, "compress.done",
			"before_est_tokens", before, "after_est_tokens", after, "saved_est_tokens", saved)
	}
}
```

(No stub needed: `pruneByRelevance` and `findPinned` are free functions from Tasks 7–8, which execute before this task.)

- [x] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/plugin/compress/ -v`
Expected: PASS, including the rapid property test.

- [x] **Step 5: Commit**

```bash
git add internal/plugin/compress/hook.go internal/plugin/compress/hook_test.go internal/plugin/compress/invariants_test.go
git commit -m "feat(compress): CompressionHook PreHook with 3-level toggle and hard invariants"
```

---

### Task 6: config — env knobs + boot validation

**Files:**
- Modify: `internal/config/config.go` (struct fields after `JSONFormatSteeringEnabled` at :290; parse block near the JSON_FORMAT_STEERING_ENABLED parse; literal at :836)
- Test: `internal/config/config_test.go` (append; follow existing table style in that file)

**Interfaces:**
- Produces (used by Task 11 main.go): `Config.CompressionEnabled bool`, `Config.CompressTriggerTokens int`, `Config.CompressBudgetTokens int`, `Config.CompressProtectTail int`, `Config.CompressToolKeep int`. That is the complete set — stage 4 (local BM25) needs no configuration.

**Scope note (third-pass rework):** this task does NOT touch the embedding surface. `EMBEDDING_MODEL_DEFAULT` remains reserved exactly as today — the REL-CFG-03 warn-and-ignore block in `config.go` and its regression test (`regression_rel_cfg_03_test.go`) stay byte-for-byte untouched, as does `CLAUDE.md`'s "(reserved, not yet implemented)" annotation. No `EMBEDDINGS_URL` exists.

- [x] **Step 1: Write the failing tests** (append to `internal/config/config_test.go`; use the same `t.Setenv` + `Load()`/`LoadArgs` helpers the existing tests in that file use — read two neighboring tests first and mirror their setup exactly)

```go
func TestLoad_CompressionDefaults(t *testing.T) {
	// (mirror the env-reset preamble used by neighboring tests)
	cfg, err := LoadArgs(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CompressionEnabled {
		t.Error("CompressionEnabled default must be false")
	}
	if cfg.CompressTriggerTokens != 6000 || cfg.CompressBudgetTokens != 4000 ||
		cfg.CompressProtectTail != 4 || cfg.CompressToolKeep != 1200 {
		t.Errorf("compress defaults wrong: %+v", cfg)
	}
}

func TestLoad_CompressionValidation(t *testing.T) {
	cases := []struct{ key, val, wantSub string }{
		{"COMPRESS_TRIGGER_TOKENS", "0", "COMPRESS_TRIGGER_TOKENS"},
		{"COMPRESS_BUDGET_TOKENS", "-1", "COMPRESS_BUDGET_TOKENS"},
		{"COMPRESS_PROTECT_TAIL", "-2", "COMPRESS_PROTECT_TAIL"},
		{"COMPRESS_TOOL_KEEP", "0", "COMPRESS_TOOL_KEEP"},
		{"COMPRESS_TOOL_KEEP", "9223372036854775807", "COMPRESS_TOOL_KEEP"}, // upper bound (overflow guard)
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			t.Setenv(c.key, c.val)
			if _, err := LoadArgs(nil); err == nil || !strings.Contains(err.Error(), c.wantSub) {
				t.Errorf("want boot error naming %s, got %v", c.wantSub, err)
			}
		})
	}
}

func TestLoad_CompressBudgetOverTriggerIsBootError(t *testing.T) {
	t.Setenv("COMPRESS_TRIGGER_TOKENS", "1000")
	t.Setenv("COMPRESS_BUDGET_TOKENS", "2000")
	if _, err := LoadArgs(nil); err == nil || !strings.Contains(err.Error(), "COMPRESS_BUDGET_TOKENS") {
		t.Errorf("budget > trigger must be a boot error, got %v", err)
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/ -run TestLoad_Compress -v`
Expected: FAIL — `cfg.CompressionEnabled undefined`.

- [x] **Step 3: Add struct fields** (insert after `JSONFormatSteeringEnabled bool` at config.go:290)

```go
	// CompressionEnabled is the process-wide DEFAULT for CompressionHook
	// (two-knob model: ENABLED_HOOKS controls chain membership; this
	// controls default work-doing). Unlike the PII knob this is a
	// default, not a gate — a per-request X-Compression header or a
	// +compress/-compress model suffix overrides it per request in
	// either direction. Default false. Loaded from COMPRESSION_ENABLED.
	CompressionEnabled bool

	// CompressTriggerTokens: below this estimated transcript size
	// (UTF-8 bytes/4 heuristic) compression is a no-op — not worth the work.
	// Node-parity default 6000. Loaded from COMPRESS_TRIGGER_TOKENS.
	CompressTriggerTokens int

	// CompressBudgetTokens: stage 4 (local BM25 relevance pruning) elides
	// lowest-relevance messages until the estimate is at or under this.
	// Must be <= CompressTriggerTokens (boot error otherwise).
	// Node-parity default 4000. Loaded from COMPRESS_BUDGET_TOKENS.
	CompressBudgetTokens int

	// CompressProtectTail: the last N messages pass through verbatim —
	// the most recent context is where the action is. Node-parity
	// default 4. Loaded from COMPRESS_PROTECT_TAIL.
	CompressProtectTail int

	// CompressToolKeep: stale tool results keep this many bytes of head
	// AND tail around an elision marker. Node-parity default 1200.
	// Loaded from COMPRESS_TOOL_KEEP.
	CompressToolKeep int

```

(No embedding-related fields. Stage 4 is local BM25 — nothing to configure.)

- [x] **Step 4: Add the parse + validation block** (place after the `jsonFormatSteeringEnabled` parse block, before the `return Config{` at :836; follow the `errs = append(errs, ...)` accumulation pattern used throughout)

```go
	// Context compression (CompressionHook) knobs. Fail-fast posture
	// matches POOL_SIZE / STREAM_IDLE_TIMEOUT_SEC: nonsensical values are
	// boot errors, not silent coercions.
	compressionEnabled, err := getEnvBool("COMPRESSION_ENABLED", false)
	if err != nil {
		errs = append(errs, err)
	}
	compressTrigger, err := getEnvInt("COMPRESS_TRIGGER_TOKENS", 6000)
	if err != nil {
		errs = append(errs, err)
	}
	if compressTrigger <= 0 {
		errs = append(errs, fmt.Errorf("COMPRESS_TRIGGER_TOKENS: must be > 0, got %d", compressTrigger))
	}
	compressBudget, err := getEnvInt("COMPRESS_BUDGET_TOKENS", 4000)
	if err != nil {
		errs = append(errs, err)
	}
	if compressBudget <= 0 {
		errs = append(errs, fmt.Errorf("COMPRESS_BUDGET_TOKENS: must be > 0, got %d", compressBudget))
	}
	if compressBudget > 0 && compressTrigger > 0 && compressBudget > compressTrigger {
		errs = append(errs, fmt.Errorf("COMPRESS_BUDGET_TOKENS: must be <= COMPRESS_TRIGGER_TOKENS (%d), got %d", compressTrigger, compressBudget))
	}
	compressProtectTail, err := getEnvInt("COMPRESS_PROTECT_TAIL", 4)
	if err != nil {
		errs = append(errs, err)
	}
	if compressProtectTail < 0 {
		errs = append(errs, fmt.Errorf("COMPRESS_PROTECT_TAIL: must be >= 0, got %d", compressProtectTail))
	}
	compressToolKeep, err := getEnvInt("COMPRESS_TOOL_KEEP", 1200)
	if err != nil {
		errs = append(errs, err)
	}
	// Upper bound: request bodies cap at 4 MiB, so any keep beyond that
	// is nonsensical — and unbounded values would make keep*2 arithmetic
	// overflow-prone downstream (review 2 MINOR-2).
	if compressToolKeep <= 0 || compressToolKeep > 4<<20 {
		errs = append(errs, fmt.Errorf("COMPRESS_TOOL_KEEP: must be in 1..%d, got %d", 4<<20, compressToolKeep))
	}
```

Leave the REL-CFG-03 warn block at config.go:757-773 and `regression_rel_cfg_03_test.go` completely untouched — `EMBEDDING_MODEL_DEFAULT` stays reserved.

- [x] **Step 5: Add fields to the `return Config{` literal** (after `JSONFormatSteeringEnabled: jsonFormatSteeringEnabled,` at :870)

```go
		CompressionEnabled:    compressionEnabled,
		CompressTriggerTokens: compressTrigger,
		CompressBudgetTokens:  compressBudget,
		CompressProtectTail:   compressProtectTail,
		CompressToolKeep:      compressToolKeep,
```

- [x] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/config/ -v -run TestLoad_Compress`
Expected: PASS. Also run `go test ./internal/config/` in full — no existing test may change or break (including the untouched REL-CFG-03 regression).

- [x] **Step 7: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): compression env knobs with fail-fast validation"
```

---

### Task 7: stage-4 scoring — deterministic BM25 (stdlib only)

**Architecture note (third-pass rework):** the deployed topology is otto-gateway + kiro-cli, nothing else — no Node gateway, no Ollama endpoint, no sidecar, no hosted embeddings API, no downloaded model, no native inference runtime. Stage 4 is therefore a small **in-process BM25 lexical relevance scorer** written with the Go standard library. This preserves the gateway's release properties verbatim: no network call or external process for scoring, no model weights, no CGO or shared libraries, no new `go.mod` entries, deterministic output, and `CGO_ENABLED=0` cross-compilation for all four GOOS/GOARCH targets plus the existing binary-size gate stay valid. No scorer interface, cache, worker goroutine, or config knob is introduced — BM25 recognizes exact lexical overlap (identifiers, error strings, names), not synonyms or paraphrases, and that is the accepted trade.

**Files:**
- Create: `internal/plugin/compress/bm25.go`
- Create: `internal/plugin/compress/bm25_test.go`

**Interfaces:**
- Produces (used by Task 8): `forEachToken(s string, fn func(tok string) bool)`, `tokenize(s string) []string` (test/query convenience over forEachToken), `newQueryIndex(query string) *queryIndex`, `bm25Rank(ctx context.Context, qi *queryIndex, docs []string) []float64`, `const bm25K1 = 1.2`, `const bm25B = 0.75`, `const maxQueryTerms = 4096` (all unexported, same package). Pure functions/types — no Hook dependency, so this task compiles and tests standalone before the hook exists.

**Design (revision-4 CRITICAL + MAJOR):** the scorer is **sparse and single-pass**. The naive shape — materialize every token, build a full term-frequency map per document, then scan every document map once per unique query term — is `O(uniqueQueryTerms × candidates)` map operations, and a valid 4-MiB request with a megabyte-scale question can force billions of them synchronously inside `Before`. Instead: unique query terms get **stable integer IDs in first-seen order**, bounded by `maxQueryTerms` — and exceeding the bound **fails closed** (`overflow` → stage 4 no-op; revision-5 MAJOR: ranking on a truncated prefix would let an attacker-chosen preamble authorize pruning while the real question past the cap is discarded); each document is scanned exactly ONCE with a zero-allocation streaming tokenizer (tokens are substrings of one lowered copy), accumulating only query-term matches sparsely; `df` updates once per matched term per doc; scoring then iterates each doc's matched IDs in **ascending order**, which makes floating-point accumulation order-deterministic (the naive map-range accumulation could flip near-tied scores between runs). Total cost: `O(queryTokens + totalDocTokens + matches·log(matches))`. `bm25Rank` takes `ctx` and checks cancellation between documents and every `cancelCheckEvery` tokens within one — on cancellation it returns nil and stage 4 becomes a no-op.

- [x] **Step 1: Write the failing tests**

```go
// internal/plugin/compress/bm25_test.go
package compress

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestTokenize(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"Hello, World!", []string{"hello", "world"}},
		{"snake_case_ident stays whole", []string{"snake_case_ident", "stays", "whole"}},
		{"HTTP2 400 err_code=EOF", []string{"http2", "400", "err_code", "eof"}},
		{"Grüße 世界 42", []string{"grüße", "世界", "42"}}, // Unicode letters/digits kept
		{"---   \t\n---", nil},                            // separators only → no tokens
	}
	for _, c := range cases {
		if got := tokenize(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("tokenize(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	// Determinism: same input, same output, every time.
	for i := 0; i < 3; i++ {
		if got := tokenize("Hello, World!"); !reflect.DeepEqual(got, []string{"hello", "world"}) {
			t.Fatal("tokenize is not deterministic")
		}
	}
}

func TestBM25Rank_SharedTermsRankHigher(t *testing.T) {
	ctx := context.Background()
	qi := newQueryIndex("database connection timeout in pool_manager")
	docs := []string{
		"the pool_manager raised a database connection timeout again", // overlaps
		"completely unrelated prose about weather and cooking",        // no overlap
	}
	scores := bm25Rank(ctx, qi, docs)
	if scores[0] <= scores[1] {
		t.Errorf("overlapping doc must outscore unrelated doc: %v", scores)
	}
	if scores[1] != 0 {
		t.Errorf("zero-overlap doc must score exactly 0, got %v", scores[1])
	}
}

func TestBM25Rank_Guards(t *testing.T) {
	ctx := context.Background()
	// Empty query, empty corpus, all-empty docs — all zeros, no NaN, no panic.
	for name, c := range map[string]struct {
		query string
		docs  []string
	}{
		"empty-query":  {"...!!!", []string{"a doc"}},
		"no-docs":      {"a", nil},
		"empty-corpus": {"a", []string{"", "  ...  "}}, // avgLen 0
	} {
		for _, s := range bm25Rank(ctx, newQueryIndex(c.query), c.docs) {
			if s != 0 {
				t.Errorf("%s: scored %v, want 0", name, s)
			}
		}
	}
}

func TestBM25Rank_DeterministicManyNearTiedTerms(t *testing.T) {
	// Revision-4 MAJOR: accumulation order must be fixed, not map-range.
	// Hundreds of terms with near-tied contributions is exactly where
	// order-dependent float addition would flip last bits between runs.
	ctx := context.Background()
	var qb, d1, d2 strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&qb, "term%03d ", i)
		if i%2 == 0 {
			fmt.Fprintf(&d1, "term%03d filler ", i)
		} else {
			fmt.Fprintf(&d2, "term%03d filler ", i)
		}
	}
	qi := newQueryIndex(qb.String())
	docs := []string{d1.String(), d2.String(), "nothing shared here"}
	first := bm25Rank(ctx, qi, docs)
	for i := 0; i < 5; i++ {
		if !reflect.DeepEqual(bm25Rank(ctx, newQueryIndex(qb.String()), docs), first) {
			t.Fatal("bm25Rank scores differ between identical runs")
		}
	}
	if first[2] != 0 {
		t.Errorf("zero-overlap doc scored %v, want exactly 0", first[2])
	}
}

func TestBM25Rank_CancelledContextReturnsNil(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := bm25Rank(ctx, newQueryIndex("alpha"), []string{"alpha beta"}); got != nil {
		t.Errorf("cancelled ctx: got %v, want nil (stage-4 no-op signal)", got)
	}
}

func TestNewQueryIndex_OverflowFailsClosed(t *testing.T) {
	// Revision-5 MAJOR: exceeding the cap must NOT rank on the prefix —
	// overflow marks the index unusable and bm25Rank returns all zeros.
	var qb strings.Builder
	for i := 0; i < maxQueryTerms+500; i++ {
		fmt.Fprintf(&qb, "u%05d ", i)
	}
	qi := newQueryIndex(qb.String())
	if !qi.overflow {
		t.Fatal("over-cap query did not set overflow")
	}
	scores := bm25Rank(context.Background(), qi, []string{"u00000 overlaps the prefix"})
	for _, s := range scores {
		if s != 0 {
			t.Errorf("overflowed index still ranked: %v", s)
		}
	}

	// EXACTLY at the cap: complete index, no overflow, ranking works.
	var qb2 strings.Builder
	for i := 0; i < maxQueryTerms; i++ {
		fmt.Fprintf(&qb2, "u%05d ", i)
	}
	qi2 := newQueryIndex(qb2.String())
	if qi2.overflow || qi2.n != maxQueryTerms {
		t.Fatalf("at-cap query: overflow=%v n=%d, want false/%d", qi2.overflow, qi2.n, maxQueryTerms)
	}
	if s := bm25Rank(context.Background(), qi2, []string{"u00000 u00001"}); s[0] <= 0 {
		t.Error("at-cap (complete) index must still rank")
	}
	// Repeats of already-indexed terms past the cap are NOT overflow.
	qi3 := newQueryIndex(qb2.String() + " u00000 u00001")
	if qi3.overflow {
		t.Error("repeated known terms wrongly flagged as overflow")
	}
}

// BenchmarkBM25RankAdversarial locks the sparse single-pass complexity
// (revision-4 CRITICAL): scale query vocabulary and candidate count
// independently — ns/op and allocs must grow roughly linearly in
// (queryTokens + totalDocTokens), NOT as their product. "shared" leads
// the query (revision-5 MINOR: appended after the cap it was never
// indexed, so the big rows scored all-zero and skipped the match/sort/
// scoring path entirely). In-cap rows genuinely scale the indexed
// vocabulary; the over-cap row measures the fail-closed path (must be
// near-instant: overflow detected during query indexing, no doc scans).
func BenchmarkBM25RankAdversarial(b *testing.B) {
	mkQuery := func(nTerms int) string {
		var sb strings.Builder
		sb.WriteString("shared ") // FIRST — always indexed → real matches in every in-cap row
		for i := 0; i < nTerms-1; i++ {
			fmt.Fprintf(&sb, "q%06d ", i)
		}
		return sb.String()
	}
	mkDocs := func(n int) []string {
		docs := make([]string, n)
		for i := range docs {
			docs[i] = "shared " + strings.Repeat("dfiller ", 24) // ~200 bytes, overlaps query
		}
		return docs
	}
	for _, nq := range []int{1000, 2000, 4000} { // all within maxQueryTerms
		for _, nd := range []int{500, 1000} {
			b.Run(fmt.Sprintf("qterms=%d/docs=%d", nq, nd), func(b *testing.B) {
				query, docs := mkQuery(nq), mkDocs(nd)
				// Sanity: the match path is actually exercised.
				if s := bm25Rank(context.Background(), newQueryIndex(query), docs[:1]); s[0] <= 0 {
					b.Fatal("benchmark setup broken: shared term not matching")
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					qi := newQueryIndex(query)
					_ = bm25Rank(context.Background(), qi, docs)
				}
			})
		}
	}
	b.Run("overcap-failclosed/qterms=20000/docs=1000", func(b *testing.B) {
		query, docs := mkQuery(20_000), mkDocs(1000)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			qi := newQueryIndex(query) // overflow → bm25Rank returns zeros without scanning docs
			_ = bm25Rank(context.Background(), qi, docs)
		}
	})
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/plugin/compress/ -run 'TestTokenize|TestBM25|TestNewQueryIndex' -v`
Expected: FAIL — `undefined: tokenize`, `undefined: newQueryIndex`, `undefined: bm25Rank`.

- [x] **Step 3: Write the implementation**

```go
// internal/plugin/compress/bm25.go
// Stage 4's relevance scorer: Okapi BM25 over a deterministic,
// stdlib-only tokenization, implemented as a SPARSE SINGLE PASS. Local
// by design — the deployed topology is otto-gateway + kiro-cli only, so
// relevance scoring must not require a network endpoint, model weights,
// or CGO. BM25 measures exact lexical overlap (identifiers, error
// strings, names) — not synonyms or paraphrases; note that no-space CJK
// text tokenizes into sentence-sized runs, making stage 4 largely inert
// for it (documented operator limitation).
//
// Cost discipline (revision-4 CRITICAL): Before runs synchronously on
// the request path and its inputs are client-controlled up to the 4 MiB
// body cap, so this file must never do O(uniqueQueryTerms × candidates)
// work. Everything here is O(queryTokens + totalDocTokens +
// matches·log(matches)), with per-token allocations avoided (tokens are
// substrings of a single ToLower'd copy) and ctx cancellation honored
// between documents.
package compress

import (
	"context"
	"math"
	"sort"
	"strings"
	"unicode"
)

const (
	bm25K1 = 1.2
	bm25B  = 0.75
	// maxQueryTerms bounds how many UNIQUE query terms stage 4 will
	// index (df/idf arrays, per-doc match lists) against megabyte-scale
	// adversarial questions. Exceeding it FAILS CLOSED: queryIndex sets
	// overflow and stage 4 no-ops (revision-5 MAJOR — ranking on the
	// first-4096-terms PREFIX would let an attacker-chosen preamble
	// authorize pruning while the real question past the cap is
	// ignored). 4096 unique terms is far beyond any real user question.
	maxQueryTerms = 4096
	// cancelCheckEvery: token interval for ctx checks inside a single
	// large document scan (a lone near-4-MiB candidate must not run to
	// completion after the client disconnects).
	//
	// ACCEPTED RESIDUAL (final review sign-off): checks are per-token,
	// so the query projection and a pathological multi-megabyte single
	// token can still finish after disconnect. That is one linear pass
	// bounded by the 4-MiB body cap (~ms of CPU); a byte-level check
	// would put a branch in the zero-allocation hot loop and tax every
	// LIVE request to save milliseconds on dead ones. Do not "fix" this.
	cancelCheckEvery = 4096
)

// forEachToken lowercases s once and streams its tokens to fn — a token
// is a maximal run of Unicode letters, Unicode digits, or '_'; all
// other runes separate; empty tokens are discarded. Each token is a
// substring of the single lowered copy, so the scan allocates nothing
// per token. fn returning false stops the scan early. No stemming,
// stop-words, synonyms, or language detection: deterministic by
// construction.
func forEachToken(s string, fn func(tok string) bool) {
	s = strings.ToLower(s)
	start := -1
	for i, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			if !fn(s[start:i]) {
				return
			}
			start = -1
		}
	}
	if start >= 0 {
		fn(s[start:])
	}
}

// tokenize materializes forEachToken's stream — used for tests and the
// (small) query; documents are streamed, never materialized.
func tokenize(s string) []string {
	var toks []string
	forEachToken(s, func(tok string) bool {
		toks = append(toks, tok)
		return true
	})
	return toks
}

// queryIndex assigns stable integer IDs to the query's unique terms in
// FIRST-SEEN order. Stable IDs are what make downstream accumulation
// order-deterministic. overflow means the query had MORE than
// maxQueryTerms unique terms — the index is incomplete and MUST NOT be
// used for ranking (fail closed; revision-5 MAJOR).
type queryIndex struct {
	ids      map[string]int
	n        int
	overflow bool
}

// newQueryIndex tokenizes the query text and indexes its unique terms.
// n == 0 or overflow == true mean stage 4 must no-op.
func newQueryIndex(query string) *queryIndex {
	qi := &queryIndex{ids: make(map[string]int)}
	forEachToken(query, func(tok string) bool {
		if _, ok := qi.ids[tok]; ok {
			return true
		}
		if qi.n >= maxQueryTerms {
			qi.overflow = true // incomplete index — caller must no-op
			return false
		}
		qi.ids[tok] = qi.n
		qi.n++
		return true
	})
	return qi
}

// bm25Rank scores every doc against the indexed query with Okapi BM25:
//
//	idf(t)     = ln(1 + (N - df(t) + 0.5) / (df(t) + 0.5))
//	score(doc) = Σ over matched query terms of
//	             idf(t) · tf(t,doc)·(k1+1) / (tf(t,doc) + k1·(1 − b + b·|doc|/avg))
//
// Single pass per document: one streaming token scan accumulates the
// doc length and sparse per-query-term tf counts; df updates once per
// matched term per doc. Scoring iterates each doc's matched IDs in
// ASCENDING order — floating-point accumulation order is therefore
// identical run-to-run (map-range accumulation could flip near-tied
// scores). A doc sharing no query term scores exactly 0 — the caller's
// zero-evidence stop depends on that exactness.
//
// Guards: nil/empty/overflowed query index, no docs, or an all-empty
// corpus return all zeros (overflow means the index is an incomplete
// prefix — never rank on it); no NaN or division by zero is possible.
// ctx is checked between documents AND every cancelCheckEvery tokens
// inside a document (one candidate can approach the 4-MiB body cap);
// on cancellation bm25Rank returns nil and the caller must treat
// stage 4 as a no-op.
func bm25Rank(ctx context.Context, qi *queryIndex, docs []string) []float64 {
	scores := make([]float64, len(docs))
	if qi == nil || qi.n == 0 || qi.overflow || len(docs) == 0 {
		return scores
	}
	type match struct{ qid, tf int }
	df := make([]int, qi.n)
	docLens := make([]int, len(docs))
	matches := make([][]match, len(docs))
	tfBuf := make([]int, qi.n)
	touched := make([]int, 0, 16)
	totalLen := 0

	for d, text := range docs {
		if ctx.Err() != nil {
			return nil // cancelled — caller treats stage 4 as a no-op
		}
		docLen := 0
		cancelled := false
		forEachToken(text, func(tok string) bool {
			docLen++
			if docLen%cancelCheckEvery == 0 && ctx.Err() != nil {
				cancelled = true
				return false
			}
			if qid, ok := qi.ids[tok]; ok {
				if tfBuf[qid] == 0 {
					touched = append(touched, qid)
				}
				tfBuf[qid]++
			}
			return true
		})
		if cancelled {
			return nil
		}
		docLens[d] = docLen
		totalLen += docLen
		if len(touched) > 0 {
			sort.Ints(touched) // ascending qid — deterministic accumulation order
			ms := make([]match, len(touched))
			for k, qid := range touched {
				ms[k] = match{qid: qid, tf: tfBuf[qid]}
				df[qid]++
				tfBuf[qid] = 0
			}
			matches[d] = ms
			touched = touched[:0]
		}
	}
	if totalLen == 0 {
		return scores // all-empty corpus — no evidence, all zeros
	}
	avgLen := float64(totalLen) / float64(len(docs))

	n := float64(len(docs))
	idf := make([]float64, qi.n)
	for qid := range idf {
		if df[qid] > 0 {
			idf[qid] = math.Log(1 + (n-float64(df[qid])+0.5)/(float64(df[qid])+0.5))
		}
	}
	for d := range docs {
		docLen := float64(docLens[d])
		for _, m := range matches[d] {
			f := float64(m.tf)
			scores[d] += idf[m.qid] * (f * (bm25K1 + 1)) / (f + bm25K1*(1-bm25B+bm25B*docLen/avgLen))
		}
	}
	return scores
}
```

- [x] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/plugin/compress/ -run 'TestTokenize|TestBM25|TestNewQueryIndex' -v`
Expected: PASS. Also run `go test ./internal/plugin/compress/ -bench BenchmarkBM25RankAdversarial -benchtime 1x` once and eyeball that doubling qterms at fixed docs does NOT double-times-docs the ns/op.

- [x] **Step 5: Commit**

```bash
git add internal/plugin/compress/bm25.go internal/plugin/compress/bm25_test.go
git commit -m "feat(compress): deterministic stdlib BM25 scorer for stage-4 relevance"
```

---

### Task 8: stage 4 — pin discovery + BM25 relevance pruning

**Files:**
- Create: `internal/plugin/compress/prune.go`
- Create: `internal/plugin/compress/prune_test.go`

**Interfaces:**
- Consumes: `forEachToken`/`newQueryIndex`/`bm25Rank` (Task 7), `flattenText`/`estMessageTokens`/`estMessagesTokens` (Task 1), `replaceText` (Task 4).
- Produces (used by Task 5 hook): `findPinned(msgs []canonical.Message) (lastIdx, queryIdx int)`, `hasQueryTerms(m canonical.Message) bool`, `queryText(m canonical.Message) string`, `relevanceText(m canonical.Message) string`, `stripPII(text string) string`, `pruneByRelevance(ctx context.Context, msgs []canonical.Message, mutable func(int) bool, queryIdx, budgetTokens int)`, `carriesToolCall(m canonical.Message) bool`, `const minCandidateLen = 200` (all unexported). Everything here is a free function — no Hook receiver — so this task compiles and tests before Task 5 exists (no stub, no cross-task compile break).

**Design decisions (third-pass MAJOR-1, amended by revision-4):** current-turn protection and relevance-query selection are SEPARATE concepts, because tool-result turns use different canonical roles per surface — OpenAI/Ollama map wire `role:"tool"` to `RoleTool` (openai/wire.go:188-206, ollama/wire.go:382-393), while Anthropic carries `tool_result` blocks inside a `RoleUser` message (anthropic/wire.go:346-356). `findPinned` therefore returns two indices: the latest non-system message (the current inbound turn — pinned so a current `RoleTool` result is never compressed even at `ProtectTail=0`), and the latest `RoleUser` message whose **RAW** Text parts yield at least one tokenizer term (revision-4 MINOR: `p.Text != ""` accepted whitespace/punctuation-only Text; revision-5 MAJOR: selection must be raw, not sanitized — a PII-only current question must be SELECTED, then produce an empty sanitized query so stage 4 no-ops, rather than being skipped in favor of an older question whose stale evidence would authorize pruning). Both are pinned across every stage. **Every machine-generated PII token grammar is STRIPPED from ranking text** — encrypt `[PII:Entity:…]`, hash `[ENTITY:h-…]`, countered replace `[ENTITY_N]` (revision-4/-5 MAJORs): placeholder/hash shapes tokenize into shared `pii`/`email`/`h` terms — synthetic evidence that could bypass the zero-overlap safety stop and elide genuinely relevant context. Documented residual: bare `[ENTITY]` replace tokens and mask-mode output are indistinguishable from ordinary text and are not stripped.

- [x] **Step 1: Write the failing tests**

```go
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
		textMsg(canonical.RoleUser, pad("weather and cooking chatter", "fillerx")),                  // zero overlap → elide first
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
		textMsg(canonical.RoleUser, "[PII:Email:AAAAaaaa1111_-]"), // question = Alice's email only
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
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/plugin/compress/ -run 'TestFindPinned|TestPrune' -v`
Expected: FAIL — `undefined: findPinned`, `undefined: pruneByRelevance`.

- [x] **Step 3: Write the implementation**

```go
// internal/plugin/compress/prune.go
package compress

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"otto-gateway/internal/canonical"
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
// Documented residual (accepted): bare replace tokens "[EMAIL]" and
// mask-mode output are indistinguishable from ordinary bracketed text /
// prose and are NOT stripped — a bare entity token is a single weak,
// idf-discounted term; drop mode emits nothing to strip.
var piiRankingTokenRe = regexp.MustCompile(
	`\[PII:[A-Za-z0-9_]+:[A-Za-z0-9_-]+\]` + // encrypt wire token
		`|\[[A-Z][A-Z0-9_]*:h-[A-Za-z0-9_-]+\]` + // hash-mode token
		`|\[[A-Z][A-Z0-9_]*_[0-9]+\]`) // countered replace token

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

// queryText builds the BM25 query from a message's Text parts ONLY —
// ToolResult content is excluded so history is ranked against the
// user's question, not against tool output (a mixed Anthropic
// tool_result+Text turn contributes only its Text). Encrypted PII
// tokens are stripped so they can never form query terms.
func queryText(m canonical.Message) string {
	var b strings.Builder
	for _, p := range m.Content {
		if p.Kind == canonical.ContentKindText {
			b.WriteString(p.Text)
			b.WriteByte('\n')
		}
	}
	return stripPII(b.String())
}

// relevanceText builds a candidate's scoring document: Text, Thinking,
// and ToolResult prose (flattenText). Everything stays in-process —
// stage 4 is local BM25, nothing leaves the gateway — so thinking may
// participate in ranking even though the PII hook never redacts it.
// Encrypted PII tokens are stripped: they must be neither noise nor
// evidence (revision-4 MAJOR).
func relevanceText(m canonical.Message) string {
	return stripPII(flattenText(m))
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
```

- [x] **Step 4: Run the package suite**

Run: `go test ./internal/plugin/compress/ -v`
Expected: PASS (bm25 + prune + all earlier-task tests).

- [x] **Step 5: Commit**

```bash
git add internal/plugin/compress/prune.go internal/plugin/compress/prune_test.go
git commit -m "feat(compress): stage-4 BM25 relevance pruning with pin separation and zero-evidence stop"
```

---

### Task 9: adapter wiring — model suffix (5 sites) + X-Compression header (3 sites)

**Files:**
- Modify: `internal/adapter/anthropic/wire.go` (the `Model: normalizeClaudeModelID(w.Model)` assignment in `wireToChatRequest`, ~:170)
- Modify: `internal/adapter/anthropic/handlers.go` (`stampPluginCtx`, ~:22)
- Modify: `internal/adapter/openai/wire.go` (the `Model:` assignment in `wireToChatRequest`, ~:136-146)
- Modify: `internal/adapter/openai/handlers.go` (`stampPluginCtx`, ~:38; AND the inline `canonical.ChatRequest` literal in `handleCompletions`, ~:389-393 — the FIFTH builder site, missed by the original plan per review MAJOR-3: `/v1/completions` builds its canonical request directly, so `model+compress` there would otherwise leak a suffixed name into `SetModel`)
- Modify: `internal/adapter/ollama/wire.go` (BOTH canonical-request builders, ~:326 and ~:432 — /api/chat and /api/generate)
- Modify: `internal/adapter/ollama/handlers.go` (`stampPluginCtx`, ~:45)
- Test: `internal/adapter/anthropic/wire_test.go`, `internal/adapter/openai/wire_test.go`, `internal/adapter/openai/handlers_test.go` (completions), `internal/adapter/ollama/wire_test.go` (append; mirror each file's existing table style)

**Interfaces:**
- Consumes: `compress.SplitCompressDirective`, `compress.MetadataKey`, `compress.WithHeaderDirective`, `compress.ParseHeaderValue` (Tasks 2–3).
- Produces: every canonical-request builder — anthropic wireToChatRequest, openai wireToChatRequest, openai handleCompletions, ollama chat + generate — delivers (a) `req.Model` with the directive stripped, (b) `req.Metadata[compress.MetadataKey] bool` when a suffix was present; and every `stampPluginCtx` delivers (c) ctx stamped via `compress.WithHeaderDirective` when the `X-Compression` header parses as a valid tri-state value.

**Response model-echo contract (third-pass MINOR):** every response — streaming and non-streaming, on all five endpoints — echoes the caller's ORIGINAL directive-bearing model string (`qwen-2.5+compress`), never the stripped base. This is what the current renderers already do (they pass `wire.Model`: openai handlers.go:246-251/:344/:446, anthropic handlers.go:298-303/:405, ollama handlers.go:219/:364-369/:529/:636-644) — the contract exists to FREEZE it: clients correlate and cache by response model, so a later change that echoes `req.Model` on one branch but `wire.Model` on another would give the same request two identities. Implementation rule: renderers keep using the wire model; `req.Model` (stripped) is engine-internal only. Tested per surface, per branch, in Step 1's handler-level tables.

- [x] **Step 1: Write the failing tests** (one per adapter; the anthropic one is the critical interaction test)

```go
// append to internal/adapter/anthropic/wire_test.go
func TestWireToChatRequest_CompressSuffixThenNormalize(t *testing.T) {
	// The +compress suffix must be stripped BEFORE hyphen-version
	// normalization: "claude-sonnet-4-6+compress" → base
	// "claude-sonnet-4-6" → canonical "claude-sonnet-4.6".
	// (Build the minimal wire request the way neighboring tests in this
	// file do; the essential assertions:)
	//   req.Model == "claude-sonnet-4.6"
	//   req.Metadata[compress.MetadataKey] == true
	// And for a "-compress" suffix: Metadata value false.
	// And for no suffix: req.Metadata has no compress key.
}
```

```go
// append to internal/adapter/openai/wire_test.go and ollama/wire_test.go
// (same three assertions, using each adapter's existing request-builder
// test helpers; ollama covers BOTH the chat and generate builders):
//   "qwen-2.5+compress"  → Model "qwen-2.5",  Metadata[compress.MetadataKey] == true
//   "qwen-2.5-compress"  → Model "qwen-2.5",  Metadata[compress.MetadataKey] == false
//   "qwen-2.5"           → Model "qwen-2.5",  no Metadata key
```

```go
// append to internal/adapter/openai/handlers_test.go — the /v1/completions
// builder is INLINE in handleCompletions, so cover it at the handler level
// using the file's existing fake-engine harness (read a neighboring
// handleCompletions test first and mirror its setup): POST /v1/completions
// with {"model": "qwen-2.5+compress", "prompt": "hi"} → the canonical
// request captured by the fake engine has Model "qwen-2.5" and
// Metadata[compress.MetadataKey] == true (plus the -compress and
// no-suffix rows).
```

```go
// ALSO append handler-level X-Compression tests to each adapter's
// handlers_test.go (all three surfaces — review 2 MINOR-8: the suffix
// tests alone cannot catch a stamp that misses a branch). Table over:
//   header "1" / "true"  → ctx observed by the engine/hook has directive true
//   header "0" / "off"   → directive false
//   header "garbage"     → NO directive stamped (falls through)
//   header "0" + model "m+compress" → header WINS (directive false)
// Branch matrix (revision-4 MINOR — /v1/completions is deliberately
// JSON-only and force-downgrades stream:true, openai
// handlers.go:379-381; do NOT expect SSE from it):
//   /v1/chat/completions, /v1/messages, /api/chat, /api/generate:
//       streaming AND non-streaming rows.
//   /v1/completions: non-streaming row PLUS a stream:true row that
//       asserts the downgrade (JSON response) while still verifying
//       header precedence and model echo.
// The stamp lives in stampPluginCtx which all branches share (openai
// handlers.go:108, anthropic handlers.go:149, ollama handlers.go:119) —
// these tests lock that sharing in place. Observe the stamped ctx via
// each file's existing fake-engine harness (the hook reads
// compress.HeaderDirectiveFromContext(ctx) — a tiny fake PreHook or the
// captured canonical request's ctx, whichever the harness exposes).
//
// In the SAME handler-level tables, assert the response model echo
// contract: with request model "qwen-2.5+compress", the response's
// model field is "qwen-2.5+compress" (the caller's original string) on
// every row above — streaming SSE/NDJSON events and non-streaming JSON —
// while the captured canonical request carries the stripped "qwen-2.5".
```

Write these as real table tests mirroring each file's existing tests — read the neighboring test first, reuse its fixture builder, add the cases.

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/adapter/... -run Compress -v`
Expected: FAIL — Model keeps its suffix, Metadata empty.

- [x] **Step 3: Implement the suffix split at all 5 builder sites**

Anthropic (`wire.go` ~:170) — replace:

```go
		Model: normalizeClaudeModelID(w.Model),
```

with (hoisting the split above the `canonical.ChatRequest` literal):

```go
	// +compress/-compress must be stripped BEFORE hyphen-version
	// normalization — claudeModelHyphenVersionRe is $-anchored and would
	// not fire on a suffixed name, leaking the hyphen form to SetModel.
	baseModel, compressDir := compress.SplitCompressDirective(w.Model)
```

```go
		Model: normalizeClaudeModelID(baseModel),
```

and after the literal is built (before `return`):

```go
	if compressDir != nil {
		if req.Metadata == nil {
			req.Metadata = make(map[string]any, 1)
		}
		req.Metadata[compress.MetadataKey] = *compressDir
	}
```

OpenAI (`wire.go` `wireToChatRequest`) and Ollama (both builders): the same pattern with `Model: baseModel` (no normalization on those surfaces). Add the import `"otto-gateway/internal/plugin/compress"` to each wire.go.

OpenAI `handleCompletions` (handlers.go ~:389) — the fifth site; apply the identical pattern to its inline literal:

```go
	baseModel, compressDir := compress.SplitCompressDirective(wire.Model)
	req := &canonical.ChatRequest{
		Model:              baseModel,
		Messages:           msgs,
		WorkingDirOverride: r.Header.Get("X-Working-Dir"),
	}
	if compressDir != nil {
		req.Metadata = map[string]any{compress.MetadataKey: *compressDir}
	}
```

- [x] **Step 4: Implement the header stamp in all 3 `stampPluginCtx` helpers** (append before `return ctx` in each; identical code, matching the existing mirrored-helper convention):

```go
	// X-Compression: strict tri-state ("1"/"true"/"on" enable,
	// "0"/"false"/"off" disable); invalid values are ignored and absence
	// falls through to the model-suffix directive / COMPRESSION_ENABLED
	// default. Never treat unrecognized text as enable.
	if on, ok := compress.ParseHeaderValue(r.Header.Get("X-Compression")); ok {
		ctx = compress.WithHeaderDirective(ctx, on)
	}
```

Add the `compress` import to each handlers.go.

- [x] **Step 5: Run the adapter suites**

Run: `go test ./internal/adapter/... -v -run 'Compress|WireToChatRequest'`
Expected: PASS, plus zero regressions in each adapter's full suite: `go test ./internal/adapter/...`

- [x] **Step 6: Commit**

```bash
git add internal/adapter/
git commit -m "feat(adapters): compress model-suffix split and X-Compression header stamp on all three surfaces"
```

---

### Task 10: metrics — compression counters seam

**Files:**
- Modify: `internal/metrics/metrics.go` (struct + `New` at ~:236-320)
- Test: `internal/metrics/metrics_test.go` (append)

**Interfaces:**
- Consumes: nothing new.
- Produces (used by Task 11): `(m *Metrics) RegisterCompression(stats func() (runs, savedTokens int64))` — registers `gw_compress_runs_total` and `gw_compress_tokens_saved_estimate_total` CounterFuncs that pull from the hook's atomics at scrape time (no background goroutine — same pull posture as `newPoolCollector`).

- [x] **Step 1: Write the failing test** (append to `internal/metrics/metrics_test.go` — note it is the EXTERNAL `package metrics_test`; reuse its `testMetrics` + `scrape` helpers. Two constraints verified against the repo, review MAJOR-10: the pull collector invokes the pool/session closures unconditionally at scrape time, so nil closures panic — `testMetrics` supplies non-nil ones; and `New` wraps every series with the constant `gateway_id` label, so assertions must use the labeled form)

```go
// TestRegisterCompression_SeriesExposed: the compression counters attach
// post-New via the retained wrapped registerer and read the hook's stats
// closure at scrape time, carrying the gateway_id constant label like
// every other series.
func TestRegisterCompression_SeriesExposed(t *testing.T) {
	m := testMetrics(metrics.PoolStats{}, metrics.SessionStats{})
	m.RegisterCompression(func() (int64, int64) { return 7, 4242 })

	body := scrape(t, m)
	if !strings.Contains(body, `gw_compress_runs_total{gateway_id="gw-test-123"} 7`) {
		t.Errorf("runs counter missing/wrong:\n%s", body)
	}
	if !strings.Contains(body, `gw_compress_tokens_saved_estimate_total{gateway_id="gw-test-123"} 4242`) {
		t.Errorf("saved-tokens counter missing/wrong:\n%s", body)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/metrics/ -run TestRegisterCompression -v`
Expected: FAIL — `m.RegisterCompression undefined`.

- [x] **Step 3: Implement**

In the `Metrics` struct add a field:

```go
	// hookReg is the gateway_id-wrapped registerer retained so optional
	// feature series (RegisterCompression) can attach after New.
	hookReg prometheus.Registerer
```

In `New`, after `reggw` is created, set it in the constructed literal (`hookReg: reggw,` alongside `reg: reg,`). Then add:

```go
// RegisterCompression exposes the CompressionHook counters as pull-style
// CounterFuncs (read at scrape time from the hook's atomics — no
// background goroutine, matching the pool collector posture). Call at
// most once, after New, when the compression feature is wired.
func (m *Metrics) RegisterCompression(stats func() (runs, savedTokens int64)) {
	m.hookReg.MustRegister(
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "gw_compress_runs_total",
			Help: "Requests where CompressionHook reduced the transcript.",
		}, func() float64 { r, _ := stats(); return float64(r) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "gw_compress_tokens_saved_estimate_total",
			Help: "Estimated tokens removed from transcripts (UTF-8 bytes/4 heuristic).",
		}, func() float64 { _, s := stats(); return float64(s) }),
	)
}
```

- [x] **Step 4: Run tests**

Run: `go test ./internal/metrics/ -v`
Expected: PASS (new + existing).

- [x] **Step 5: Commit**

```bash
git add internal/metrics/
git commit -m "feat(metrics): gw_compress_* counters via pull-style RegisterCompression seam"
```

---

### Task 11: main.go wiring, docs, full verification

**Files:**
- Modify: `cmd/otto-gateway/main.go` (chain literal ~:274-289; after `gwMetrics := metrics.New(...)` ~:364)
- Modify: `cmd/otto-gateway/main_test.go` (`TestApp_DefaultHookChain_AllFiveHooksPresent` at :216-274 asserts the EXACT five-hook registration order with `LoggingHook` at index 4 — inserting CompressionHook necessarily breaks it, review MAJOR-12; update it in the same commit)
- Modify: `docs/operating.md` (env-var reference)
(`CLAUDE.md` is deliberately NOT modified — `EMBEDDING_MODEL_DEFAULT` stays reserved and its annotation stays accurate; third-pass rework reverted the second-pass graduation.)

**Interfaces:**
- Consumes: `compress.Hook` (Task 5), `Config.Compress*` (Task 6), `Metrics.RegisterCompression` (Task 10).

- [ ] **Step 1: Construct the hook and insert it in the chain** (in `newApp`, after the `jsonFormatHook := ...` line; import: `"otto-gateway/internal/plugin/compress"`)

```go
	// Context compression (CompressionHook). Chain position: after
	// piiHook (compress redacted text — never resurface raw PII into
	// stubs), before loggingHook (log what is actually sent).
	// COMPRESSION_ENABLED is the default; X-Compression header and
	// +compress/-compress model suffixes override per request.
	compressHook := &compress.Hook{
		Enabled:       cfg.CompressionEnabled,
		TriggerTokens: cfg.CompressTriggerTokens,
		BudgetTokens:  cfg.CompressBudgetTokens,
		ProtectTail:   cfg.CompressProtectTail,
		ToolKeep:      cfg.CompressToolKeep,
		Logger:        logger,
	}
	// Known interaction: middle-truncation can clip AES-GCM ciphertext
	// tokens inside stale tool results, disabling their round-trip
	// decryption (the request itself is unharmed). Gate: PII must be
	// actually DOING encryption (PIIRedactionEnabled — the hook's real
	// work gate, pii.go:428) — mode alone defaults to "encrypt" even when
	// PII is off, and warning then would be noise (review 2 MINOR-7).
	// NOT gated on cfg.CompressionEnabled: the header and +compress
	// suffix enable compression per request even when the env default is
	// off, and the boot warning must cover that advertised use case.
	if cfg.PIIRedactionEnabled && (cfg.PIIRedactionMode == "encrypt" || hasEncryptAction(cfg.PIIEntityActions)) {
		logger.Warn("PII encrypt mode active with CompressionHook available; when compression is enabled (env, X-Compression header, or +compress model suffix), truncated stale tool results may clip ciphertext tokens (see docs/operating.md)")
	}
```

(Residual accepted gap, documented rather than coded: if `ENABLED_HOOKS` filters one of the two hooks out, the warning may still fire — chain filtering happens downstream of this construction site. The warning text says "when compression is enabled", which stays truthful.)

Chain literal — insert between `piiHook` and `loggingHook`:

```go
		Pre: []engine.PreHook{
			&plugin.RequestIDHook{Logger: logger},
			&plugin.AuthHook{Tokens: cfg.AuthToken},
			jsonFormatHook,
			piiHook,
			// Compression runs after PII (operates on redacted text) and
			// before logging (log the transcript actually sent).
			compressHook,
			loggingHook,
		},
```

- [ ] **Step 2: Register metrics** (immediately after the `gwMetrics := metrics.New(...)` call completes, ~:364)

```go
	gwMetrics.RegisterCompression(compressHook.Stats)
```

(If `compressHook` is out of scope at that point in `newApp`, hoist its declaration — both sites are inside `newApp`.)

- [ ] **Step 3: Update the chain-order regression test**

`cmd/otto-gateway/main_test.go` `TestApp_DefaultHookChain_AllFiveHooksPresent` (:216-274) asserts the exact five-hook order with `LoggingHook` at index 4 — it MUST be updated in this commit or the gate fails. Rename to `TestApp_DefaultHookChain_AllSixHooksPresent`, update the doc comment (the v1.8.2 ENABLED_HOOKS regression story still applies — extend it to note CompressionHook joined in this feature), and change `wantOrder` to:

```go
	wantOrder := []string{
		"RequestIDHook",
		"AuthHook",
		"JSONFormatSteeringHook",
		"PIIRedactionHook",
		"CompressionHook",
		"LoggingHook",
	}
```

Also add an explicit-allowlist case (the very bug that test guards): `ENABLED_HOOKS` set to the five legacy names must yield a chain WITHOUT CompressionHook (two-knob model — chain membership is the hard kill switch), mirroring how the existing test constructs cfg.

- [ ] **Step 4: Build and run the full gate**

Run: `go build ./... && make vet && make test`
Expected: clean build, vet clean, all tests pass. The ONLY pre-existing test intentionally modified by this plan is the chain-order test (this task); any other failure — including the untouched REL-CFG-03 regression — is a regression to fix, not to accommodate.

- [ ] **Step 5: Manual smoke check**

Start the gateway with `COMPRESSION_ENABLED=1 COMPRESS_TRIGGER_TOKENS=100 COMPRESS_BUDGET_TOKENS=50` (both knobs — the boot validation requires budget <= trigger, so trigger alone with the default 4000 budget refuses to start), then:
1. `GET /health/hooks` → a `CompressionHook` row with kind `Pre` and the config map from `Describe()`.
2. Send an `/api/chat` request with a fat repeated transcript → response OK; `gw_compress_runs_total` on `/metrics` increments; a `compress.done` DEBUG log line appears.
3. Same request with header `X-Compression: 0` → `gw_compress_runs_total` does NOT increment.
4. Model `auto+compress` with `COMPRESSION_ENABLED=0` (unset) → counter increments (suffix forced it on).
5. Restart with `COMPRESS_BUDGET_TOKENS=10 COMPRESS_TRIGGER_TOKENS=100`, send a transcript whose history shares no words with the final question → response OK, history messages NOT elided (zero-evidence stop), and `/health/hooks` shows `budget_unmet` > 0.

- [ ] **Step 6: Document** (append to the env-var reference in `docs/operating.md`)

```markdown
### Context compression (CompressionHook)

| Env | Default | Meaning |
|---|---|---|
| `COMPRESSION_ENABLED` | `false` | Process-wide default for CompressionHook. Per-request overrides: `X-Compression` header (wins; accepts `1`/`true`/`on` and `0`/`false`/`off`, other values ignored), or a `+compress`/`-compress` model-name suffix (e.g. `qwen-2.5+compress` — for callers like LangFlow that cannot send headers). Caveat: a real model id ending in `-compress` is parsed as a disable directive (no escape syntax). `ENABLED_HOOKS` remains the hard kill switch; explicit allowlists must include `CompressionHook`. |
| `COMPRESS_TRIGGER_TOKENS` | `6000` | Below this estimated transcript size (UTF-8 bytes/4) compression is a no-op. |
| `COMPRESS_BUDGET_TOKENS` | `4000` | Target size. Re-checked between stages: once met, no further (lossier) stage runs; a transcript already at/under budget is never modified. **Best-effort**, not guaranteed: protected-tail/pinned messages and tool-call carriers are never elided, and stage 4 elides nothing when no message shares a token with the question — so a run can end still over budget (counted in `/health/hooks` as `budget_unmet`). Must be <= trigger. |
| `COMPRESS_PROTECT_TAIL` | `4` | The last N messages are never modified. Regardless of this value — even at `0` — the following are never modified: system prompt, tool schemas, tool-call pairing, the current inbound turn (including a trailing tool result on any surface), and the most recent user question. |
| `COMPRESS_TOOL_KEEP` | `1200` | Head+tail bytes kept when middle-truncating stale tool results. Bounded 1..4194304. |

Pipeline: blank-line/trailing-space cleanup → stale tool-result truncation → exact-duplicate collapse → local BM25 lexical relevance pruning against the user's most recent question — with the budget re-checked between stages, so later (lossier) stages are skipped the moment the target is met. Everything runs in-process: no network call, external model, or additional configuration. Stage 1 is **low-loss normalization**, not lossless — it strips trailing whitespace and collapses 3+ blank lines (so exact-output fixtures relying on those bytes are altered), but never rewrites interior whitespace, so code indentation in old messages survives byte-for-byte.

Stage 4 ranks by **exact lexical overlap** (identifiers, error strings, names — not synonyms or paraphrases) and has a hard safety rule: if no eligible message shares a single token with the user's question, nothing is elided — the transcript proceeds over budget rather than pruning blind, and `/health/hooks` counts it under `budget_unmet`. Further limitations to know: machine-generated PII tokens — encrypted `[PII:…]`, hashed `[ENTITY:h-…]`, and numbered `[ENTITY_N]` replacements — are stripped before ranking (they are neither noise nor evidence; a question consisting only of redacted values disables stage 4 for that request, as does a question with more than 4,096 unique terms — stage 4 never ranks on a truncated query). Bare `[ENTITY]` replacements and masked values are not stripped (indistinguishable from ordinary text). Unsegmented CJK text tokenizes into sentence-sized runs (no word segmentation), so stage 4 is largely inert for such history — stages 1–3 still apply.

Failure posture — precise guarantee: compression never fails a request. Stage 4 performs no I/O, so there is no endpoint-failure path; the hook's panic recovery still guarantees `(nil, nil)`, and an internal panic forwards the request with whatever stages had already completed applied (stages run in place). Observability: `/health/hooks` (config + lifetime counters, including `budget_unmet`), `gw_compress_runs_total` and `gw_compress_tokens_saved_estimate_total` on `/metrics`.

Known interaction: with `PII_REDACTION_MODE=encrypt` (or any per-entity encrypt action), middle-truncation of a stale tool result can clip an embedded ciphertext token, which disables round-trip decryption of that token in the response (the request still succeeds). A boot-time warning is logged whenever encrypt mode is active — even if `COMPRESSION_ENABLED=false` — because the header and model-suffix toggles can enable compression per request.
```

- [ ] **Step 7: Commit**

```bash
git add cmd/otto-gateway/main.go cmd/otto-gateway/main_test.go docs/operating.md
git commit -m "feat(gateway): wire CompressionHook into the chain with metrics and operator docs"
```

---

## Execution order note

Execution order: **1 → 2 → 3 → 4 → 7 → 8 → 5 → 6 → 9 → 10 → 11**. Tasks 7 (BM25 scorer) and 8 (pin discovery + pruning) are free functions with no Hook dependency, so they compile and test standalone before Task 5's Hook wires them together — no temporary stubs, and every task leaves the tree compiling green. Config (6) anytime before main-wiring (11); adapters/metrics (9, 10) after the hook exists.

## Out of scope (deliberate, for a later plan)

- Semantic (dense-embedding) relevance pruning. Rejected for this plan, not merely deferred: the deployed topology is otto-gateway + kiro-cli only, so any embedding model would require a network endpoint, a downloaded model, or CGO — each of which breaks a release property this gateway guarantees. BM25's exact-lexical-overlap ranking plus the zero-evidence stop is the accepted operating point.
- Admin runtime toggle (`POST /admin/api/compression`, `capture.Controller` atomic-gate pattern).
- Advertising an `auto+compress` virtual model in Ollama `/api/tags`.
- Content-aware skip of ciphertext tokens in stage 2 when PII encrypt mode is active (documented limitation + boot warning instead).
- A keyed deterministic PII fingerprint (provided by the PII hook) that would restore duplicate collapse across encrypt-mode nonces without entity-only false positives.
