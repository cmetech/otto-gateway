---
phase: 06-tool-call-path
plan: 01
subsystem: engine
tags: [tool-call, coerce, canonical, acp, langchain-compat]

# Dependency graph
requires:
  - phase: 01.1-acp-wire-alignment
    provides: "sessionUpdateBody already decodes toolCallId/title/args (tolerant decoder); tool_call/tool_call_chunk discriminator routing"
  - phase: 02-ollama-end-to-end
    provides: "canonical.Tools / ToolChoice / Message.ToolCalls / ToolUsePart / ToolSpec / ToolChoice forward-design seams (Phase 2 D-08/D-09/D-10); engine.buildBlocks [Available tools] placeholder header (Phase 2 D-02); engine.Collect text+thinking aggregator pattern (Phase 3.1 D-02)"
  - phase: 04-streaming
    provides: "per-surface SSE/NDJSON emitter independence pattern (D-08) — Phase 6 grows tool_call handling locally per emitter in Wave 2"

provides:
  - "canonical.ToolCallChunk.ID — new exported field, two-source (ACP wire or coerce-synthesized) per Phase 6 D-08"
  - "internal/acp/translate.go promotes tool_call / tool_call_chunk notifications to real ChunkKindToolCall with populated ID/Name/Args (Phase 6 D-03 part 1 — closes the Phase 1.1 placeholder)"
  - "internal/engine/collect.go aggregates kiro-native ChunkKindToolCall into the assistant text as `[tool: <name>]\\n` narration (Phase 6 D-03 iteration-3 fix to HIGH #1 — non-streaming Ollama/OpenAI rendering path restored). Per-surface contract documented: Collect does NOT populate Message.ToolCalls from any chunk source."
  - "internal/engine/coerce.go — new file. CoerceToolCall implements the locked D-09 9-step algorithm. The producer of Message.ToolCalls for Ollama/OpenAI surfaces (per per-surface contract — Anthropic excepted via 06-04 Option A1)."
  - "engine.buildBlocks emits the full JSON tool catalog inside [Available tools] when req.Tools is non-empty (D-16), with a defensive debug-log fallback on json.Marshal failure (REVIEW LOW #6)."
  - "Property tests in coerce_test.go using testing/quick MaxCount=1000 prove NeverPanic + Idempotent + NoMatchNoMutation + deterministic TieBreaker invariants per D-12."

affects:
  - 06-02-ollama-tool-call
  - 06-03-openai-tool-call
  - 06-04-anthropic-tool-call
  - 06-05-tool-call-e2e
  - phase-08-hooks (Phase 8 hook seam will observe ChunkKindToolCall — Phase 6 populates the canonical chunk fully so the hook seam has structured data)

# Tech tracking
tech-stack:
  added: []  # zero new external dependencies — testing/quick and go.uber.org/goleak were already in go.mod
  patterns:
    - "Property tests via testing/quick (TRST-06): MaxCount=1000 + defer-recover nil-input guard + table-driven cases (mirrors internal/engine/pickcwd_test.go)"
    - "Per-surface Message.ToolCalls population contract (Phase 6 D-03/D-05/D-07): generic engine.Collect never populates; Ollama/OpenAI use engine.CoerceToolCall; Anthropic uses adapter-local Collect"
    - "Engine-internal JSON wire shim with explicit `json:` tags (availableToolWire in build_acp.go) — preserves the canonical Phase 2 D-11 invariant that canonical types carry no JSON tags while still emitting lowercase wire-shape names kiro-cli expects"
    - "Defensive marshal-error fallback discipline (REVIEW LOW #6 pattern): log via slog.Default() + emit header-only placeholder; never propagate the marshal error to the caller"

key-files:
  created:
    - internal/engine/coerce.go (264 lines)
    - internal/engine/coerce_test.go (432 lines)
  modified:
    - internal/canonical/chunk.go (+9 lines: ID field on ToolCallChunk)
    - internal/acp/translate.go (+/-26 lines: tool_call/tool_call_chunk branch promotes to real ChunkKindToolCall; removed unused fmt import)
    - internal/acp/translate_test.go (+75 lines: updated tool_call expectations + 4 new subtests covering ID/Args/empty-title fallback variants)
    - internal/acp/integration_test.go (+26 lines: updated TestIntegration_FakeACP_E2E_MixedVariants expectation for variantToolCallWrapped + reflect import)
    - internal/engine/build_acp.go (+64 lines: JSON tool catalog emission with marshal-failure debug-log fallback; availableToolWire wire struct)
    - internal/engine/build_acp_test.go (+131 lines: TestBuildBlocks_AvailableTools_JSONCatalog with 4 subcases — nil, single, multi-order, marshal-failure)
    - internal/engine/collect.go (+33 lines: ChunkKindToolCall narration aggregator + per-surface contract comment block)
    - internal/engine/collect_test.go (+122 lines: 3 new tests — AggregatesKiroNativeToolCallAsNarration, OnlyChunk, NilName_Fallback)

key-decisions:
  - "Path C accepted for Node byte-fidelity checkpoint (Task 4). The Node source repo `../gitlab.rosetta.ericssondevops.com/loop_24/acp_server` does not exist on this machine and `acp-ollama-server.js` could not be located on the filesystem. The Go implementation was verified against the narrative reference (`docs/reference/acp_server_node_reference.md` §\"Load-bearing weirdness: coerceToolCall\" lines 166-195) and the locked D-09 9-step specification + D-10 edge cases. Per the checkpoint's Path C contract, post-ship LangChain integration smoke (Pi-SDK / LangFlow real users hitting the gateway with JSON-as-text) will surface any divergent behavior as bug reports. A follow-up perf/parity verification todo is recommended for post-ship review. Wave 2 (06-02/03/04) unblocks per the checkpoint's Path C semantics."
  - "engine-internal availableToolWire struct (not exported, not in canonical) to preserve the Phase 2 D-11 invariant that canonical types have no JSON tags. The wire shim translates canonical.ToolSpec → {name, description, parameters} with lowercase JSON tags so kiro-cli sees the spec shape the Node reference emits."
  - "engine.Collect aggregates ChunkKindToolCall into the ASSISTANT TEXT accumulator (sb), not into a separate content part. Non-streaming Ollama/OpenAI render the joined text; the narration survives. The interaction with CoerceToolCall is automatic: the narration text doesn't start with `{` or ``` so Step 3 + Step 4 both fail, Step 5 returns false, narration passes through to the wire. No special-case logic in CoerceToolCall needed."
  - "ExampleCoerceToolCall (no underscore) — Go test framework requires the example name to match the exported function name exactly for exported APIs. Underscore suffix is reserved for examples on unexported names (cf. Example_pickCwd and Example_buildBlocks where the underlying symbol is unexported)."

patterns-established:
  - "Pattern: per-surface population contract — same canonical type populated by different code paths depending on the consuming surface. Documented in collect.go's ChunkKindToolCall case comment and coerce.go's package-level doc. Makes the two-path tool-call rule (kiro-ran-it-we-narrate vs model-wants-client-to-run-it-we-surface-it) explicit at every code site."
  - "Pattern: marshal-failure defensive degrade — engine-side serialization that could fail on pathological inputs logs a slog debug line and emits a header-only fallback rather than propagating the error or panicking. The buildBlocks signature stays `(req) []Block` with no error return; callers don't need to reason about catalog-emission failures."
  - "Pattern: stripFences without regex — two HasPrefix/HasSuffix chains with CRLF normalization. Linear-time guaranteed (Pitfall 3 catastrophic-backtracking safety). Fence must wrap ENTIRE trimmed text (Pitfall 3 \"entire text\" requirement) — inline fenced JSON inside prose is locked as a no-coerce case by the test matrix."

requirements-completed: [TOOL-01, TOOL-02, TOOL-03]

# Metrics
duration: 35min
completed: 2026-05-27
---

# Phase 6 Plan 01: Tool-Call Foundation Summary

**Lit up the dormant canonical tool-call seams across canonical → acp → engine. ACP `tool_call` notifications now promote to real `ChunkKindToolCall` with populated `ID/Name/Args`; `engine.Collect` aggregates kiro-native tool_calls as `[tool: <name>]\n` narration into the assistant text for non-streaming Ollama/OpenAI; `engine.CoerceToolCall` implements the load-bearing LangChain-compat coercer per the locked D-09 9-step algorithm; `engine.buildBlocks` emits the full JSON tool catalog inside `[Available tools]`. Foundation for Wave 2.**

## Tasks Completed

| # | Task | Commit | Files |
|---|------|--------|-------|
| 1 RED  | Add failing tests for ToolCallChunk.ID, ACP tool_call promotion, [Available tools] JSON catalog | `7d148ab` | translate_test.go, build_acp_test.go |
| 1 GREEN | canonical.ToolCallChunk.ID + ACP translator promotion + JSON tool catalog with marshal-failure fallback | `803b5a6` | chunk.go, translate.go, integration_test.go, build_acp.go |
| 2 RED  | Add failing tests for Collect narration of kiro-native tool_call chunks | `5223db3` | collect_test.go |
| 2 GREEN | engine.Collect aggregates kiro-native ChunkKindToolCall as `[tool: <name>]\n` narration | `239cd7b` | collect.go |
| 3 RED  | Add failing property + table tests for engine.CoerceToolCall | `e8d2189` | coerce_test.go (NEW) |
| 3 GREEN | Implement engine.CoerceToolCall per locked D-09 9-step algorithm | `312902e` | coerce.go (NEW), coerce_test.go |
| 4      | Node byte-fidelity checkpoint — Path C accepted (see Key Decisions and Deviations below) | (no code commit — gate accepted) | — |

## What Was Built

### 1. canonical.ToolCallChunk.ID (Phase 6 D-08)

New exported `ID string` field on `canonical.ToolCallChunk`. Doc comment cites the two-source nature: populated from `toolCallId` on the ACP wire when source is kiro-native, OR synthesized as `call_<unix-nano>` by `engine.CoerceToolCall` when source is text-not-kiro. No JSON tag (Phase 2 D-11 invariant preserved).

Backward compatibility: existing struct literals using named fields (`canonical.ToolCallChunk{Name: ..., Args: ...}`) still compile.

### 2. internal/acp/translate.go — kiro-native promotion (Phase 6 D-03 part 1)

The `tool_call` / `tool_call_chunk` branch in `translateUpdate` now returns a real `canonical.Chunk{Kind: ChunkKindToolCall, ToolCall: &ToolCallChunk{ID, Name, Args}}` instead of the Phase 1.1 placeholder `ChunkKindThought` + `[tool: <title>]\n` text. The `firstNonEmpty(body.Title, "unknown")` fallback for empty titles is preserved. `body.ToolCallID` and `body.Args` pass through verbatim (nil-safe).

The `[tool: <name>]\n` narration text formerly produced here has moved downstream:
- `engine.Collect` aggregates it for non-streaming Ollama/OpenAI (see §3 below).
- Per-surface streaming emitters render the chunk in their native shape (Anthropic content_block_*, OpenAI delta.tool_calls, Ollama tool_calls on done) — Wave 2 work in 06-02/03/04.

Removed the now-unused `fmt` import.

The existing `TestIntegration_FakeACP_E2E_MixedVariants` was updated to assert `ChunkKindToolCall` with populated `ID/Name/Args` for the `variantToolCallWrapped` notification (was `ChunkKindThought` with `[tool: read_file]\n`). Added `reflect` import for DeepEqual on `Args`.

### 3. internal/engine/collect.go — narration aggregation (Phase 6 D-03 iteration-3 fix to HIGH #1)

Added a `ChunkKindToolCall` case to the stream switch in `Collect` that appends `[tool: <name>]\n` to the same `sb` text accumulator that feeds `Message.Content`'s `ContentKindText` part. Defensive nil/empty-Name fallback to `"unknown"` matches the discipline in `translate.go`.

This is what non-streaming Ollama/OpenAI render — they receive only `*canonical.ChatResponse` from this function and need the kiro-native tool-call information to survive into the final body. Without this aggregation (iteration-2's HIGH #1 regression), kiro-native tool calls disappeared entirely on non-streaming paths.

`assembleChatResponse` signature unchanged. `Message.ToolCalls` stays untouched. No `ContentKindToolUse` parts appended.

The 33-line comment block documents the per-surface population contract (Phase 6 D-03/D-05/D-07):
- Generic `engine.Collect` does NOT populate `Message.ToolCalls` from any chunk source.
- Ollama and OpenAI populate via `engine.CoerceToolCall` (D-05).
- Anthropic populates via its adapter-local `Collect` (06-04 Option A1) — D-07 exception.
`ChunkKindPlan` continues to drop.

### 4. internal/engine/coerce.go — load-bearing LangChain-compat coercer (D-01, D-09, D-10, D-11)

New file. Exports:

`func CoerceToolCall(req *canonical.ChatRequest, resp *canonical.ChatResponse) bool`

Implements the locked D-09 9-step algorithm:

1. Defensive nil guards + skip (req/resp nil; empty tools; non-empty Message.ToolCalls).
2. Locate first `ContentKindText` part; bail on empty text.
3. Try raw `json.Unmarshal`.
4. On failure, run `stripFences` and retry parse.
5. Still fail → return false, text preserved.
6. `pickBestTool` scores tools by top-level key overlap with parsed object.
7. Zero score → return false, text preserved.
8. Clear `Content[textIdx].Text`; append synthetic `ToolCall{ID: "call_<unix-nano>", Name, Arguments: parsedMap}`.
9. Return true.

Private helpers:

- `pickBestTool(parsed, tools) → (*ToolSpec, score)` — deterministic first-declared tie-break via slice-index iteration (NOT map iteration — Pitfall 4). Tools with empty/missing `properties` are skipped.
- `stripFences(text) → (inner, ok)` — detects ` ```json\n...\n``` ` OR bare ` ```\n...\n``` ` fences wrapping the ENTIRE trimmed text (Pitfall 3 "entire text" requirement). CRLF-tolerant. NO regex (linear-time guaranteed; Pitfall 3 catastrophic-backtracking safety + Don't-Hand-Roll guidance).
- `extractProperties` — helper for the property-map extraction.

Package-level doc explains the per-surface contract (Ollama/OpenAI invoke; Anthropic does NOT) and the iteration-3 interaction with `engine.Collect`: kiro-native narration text naturally fails Step 3 + Step 4 parse, falls through to Step 5 no-coerce, narration survives on the wire. No special-case logic needed.

### 5. internal/engine/build_acp.go — [Available tools] JSON catalog (D-16)

Replaced the placeholder header with a header + fenced ` ```json``` ` block carrying the full tool catalog. Uses an engine-internal `availableToolWire` struct with lowercase JSON tags so the wire shape kiro-cli sees is `{"name","description","parameters"}` — preserves the canonical Phase 2 D-11 invariant (canonical types have no JSON tags) while still emitting the wire-shape names the Node reference produces.

REVIEW LOW #6 defensive fallback: if `json.Marshal` fails on pathological `Parameters` (e.g., a map containing a `chan int`), log a debug line via `slog.Default()` and emit the header-only placeholder so kiro degrades gracefully. No panic; no error propagated.

## Test Coverage

| Test | Type | Purpose |
|------|------|---------|
| `TestTranslateUpdate_VarianceMatrix` (updated rows) | table | Lock the ChunkKindToolCall promotion for `tool_call` and `tool_call_chunk` discriminators; cover ID/Name/Args propagation; cover empty-title fallback to "unknown"; cover nil-args pass-through |
| `TestBuildBlocks_AvailableTools_JSONCatalog/{nil_tools,single,multi,marshal_failure}` | sub-tests | Lock catalog emission contract; multi case asserts declaration-order preservation; marshal-failure case proves defensive fallback (no panic, header still present) |
| `TestCollect_AggregatesKiroNativeToolCallAsNarration` | functional | Stream `[text, tool_call, text]` → text contains all three substrings in order; Message.ToolCalls empty; no ContentKindToolUse parts |
| `TestCollect_KiroNativeToolCall_OnlyChunk` | functional | Only-tool_call stream → `Content[0].Text == "[tool: search_web]\n"` exactly |
| `TestCollect_KiroNativeToolCall_NilName_Fallback` | functional | Nil/empty Name → `[tool: unknown]\n` fallback (no panic) |
| `TestCoerceToolCall_NeverPanics` | property (1000) | Random inputs + nil-input recovery guard |
| `TestCoerceToolCall_Idempotent` | property (1000) | Second call no-op when ToolCalls populated; snapshot equality |
| `TestCoerceToolCall_NoMatchNoMutation` | functional | Zero-overlap → bit-identical resp (Content + ToolCalls) |
| `TestCoerceToolCall_TieBreaker` | functional (1000) | Deterministic first-declared wins across 1000 iterations |
| `TestCoerceToolCall_AlgorithmCases` | table (14 rows) | D-09 + D-10 + REVIEW LOW inline-fenced-in-prose + iteration-3 kiro-native-narration no-coerce + raw/fenced parse + truncated/array/scalar/null/empty + empty-tools + pre-existing-calls |
| `ExampleCoerceToolCall` | godoc | TRST-07 runnable example; happy-path bare JSON coerce |
| `TestIntegration_FakeACP_E2E_MixedVariants` (updated) | integration | End-to-end fake-ACP exercises the new ChunkKindToolCall flow through the real client → translator → stream pipeline |

All tests pass under `go test ./... -race -count=1`. The full module test sweep (`internal/canonical`, `internal/acp`, `internal/adapter/{anthropic,ollama,openai}`, `internal/auth`, `internal/config`, `internal/engine`, `internal/pool`, `internal/server`, `internal/session`, `cmd/otto-gateway`) is green.

## Per-Surface Contract Wording (HIGH #3 iteration-3)

The plan reframes the iteration-1 "sole producer" wording as an explicit per-surface contract:

- **Generic `engine.Collect`:** does NOT populate `Message.ToolCalls` from any chunk source. Period. (Documented in `collect.go`'s 33-line comment block.)
- **Ollama and OpenAI:** populate `Message.ToolCalls` ONLY via `engine.CoerceToolCall`. The function is invoked by the adapter handlers AFTER `engine.Collect` returns, between aggregation and per-surface render. (Phase 6 D-05.)
- **Anthropic (D-07 exception):** populates `Message.ToolCalls` via its adapter-local `Collect` (`internal/adapter/anthropic/collect.go`, Option A1 from 06-04) from kiro-native `ChunkKindToolCall` chunks. Anthropic's wire protocol has `tool_use` blocks as native first-class elements, so the adapter-local aggregator mirrors that shape rather than going through the generic engine path.

The wording lives in three places for grep-ability:
1. `internal/engine/collect.go` — full contract paragraph on the `ChunkKindToolCall` case.
2. `internal/engine/coerce.go` — package-level doc on the per-surface invocation.
3. `internal/acp/translate.go` — note in the `tool_call` / `tool_call_chunk` branch that narration moves downstream per the contract.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking issue] Updated `internal/acp/integration_test.go` to match the new Phase 6 contract.**

- **Found during:** Task 1 GREEN — `go test ./internal/acp/... -count=1` failed in `TestIntegration_FakeACP_E2E_MixedVariants` at `chunk[3].Kind: got 2, want 1` because the legacy expectation still asserted `ChunkKindThought` for the `variantToolCallWrapped` notification.
- **Issue:** The integration test was not listed in the plan's modified-files set, but the Phase 6 D-03 promotion (changing the translator to emit `ChunkKindToolCall` instead of `ChunkKindThought`) necessarily invalidates the legacy expectation.
- **Fix:** Updated the `wantChunks` slice entry for the tool_call variant, updated the doc comment at the top of the test, replaced the `ChunkKindToolCall` switch arm to do field-level DeepEqual assertions instead of an unconditional `t.Errorf`, and added the `reflect` import.
- **Files modified:** `internal/acp/integration_test.go`
- **Commit:** `803b5a6` (folded into Task 1 GREEN — same conceptual unit of change).
- **Rule rationale:** Test belonged to the same compilation unit as the production change; leaving it broken would have blocked the rest of Task 1's verification command. No architectural impact (Rule 4 not applicable). The fix is mechanical and asserts the new correct contract.

**2. [Rule 3 - Blocking issue] Wire-shape struct introduced inside `engine/build_acp.go` for the JSON tool catalog.**

- **Found during:** Task 1 GREEN — the initial test failure was `[Available tools] catalog missing "\"name\":\"get_weather\""` because passing `req.Tools` (a `[]canonical.ToolSpec` with no JSON tags) through `json.Marshal` emitted capitalized `"Name"`/`"Description"`/`"Parameters"` keys, which is not the wire shape kiro-cli expects.
- **Issue:** The plan said "emit a fenced JSON block containing `json.Marshal(req.Tools)` output" but `canonical.ToolSpec` deliberately carries no JSON tags (Phase 2 D-11 invariant — canonical stays wire-format-agnostic). Marshaling canonical directly would emit Capitalized keys.
- **Fix:** Added a private `availableToolWire` struct inside `build_acp.go` with explicit lowercase JSON tags (`name`, `description`, `parameters`). The `[Available tools]` emission translates `canonical.ToolSpec` → `availableToolWire` before marshaling. This preserves the canonical invariant AND emits the wire shape kiro-cli expects.
- **Files modified:** `internal/engine/build_acp.go` (function body + new type at the bottom).
- **Commit:** `803b5a6` (folded into Task 1 GREEN).
- **Rule rationale:** This is the only correct way to satisfy both the test's lowercase-key assertion AND the Phase 2 D-11 canonical-stays-tag-free invariant. The wire shim is engine-internal (lowercase `availableToolWire`) so it does not leak into the canonical or adapter packages. Analogous to `wireBlock` in `internal/acp/translate.go`.

**3. [Rule 1 - Bug] Example function naming convention.**

- **Found during:** Task 3 GREEN — `go test` failed with `Example_CoerceToolCall has malformed example suffix: CoerceToolCall`.
- **Issue:** Go test framework expects examples on exported names to use the bare name (`ExampleCoerceToolCall`); underscore-suffix form (`Example_<name>`) is reserved for examples on unexported names (cf. existing `Example_pickCwd` and `Example_buildBlocks` in this codebase, where the underlying symbols are unexported).
- **Fix:** Renamed `Example_CoerceToolCall` → `ExampleCoerceToolCall`. Updated the doc comment.
- **Files modified:** `internal/engine/coerce_test.go`.
- **Commit:** `312902e` (folded into Task 3 GREEN).

### Decisions / Notes (not bugs)

**4. Task 4 — Node byte-fidelity checkpoint accepted via Path C.**

The Node source repo (`../gitlab.rosetta.ericssondevops.com/loop_24/acp_server`) does not exist on this machine; `acp-ollama-server.js` could not be located on the filesystem (verified via `find /Users/coreyellis -maxdepth 4 -name "acp-ollama-server.js"`).

Per the checkpoint's Path C contract, this risk is accepted with the following mitigations:

1. The Go implementation is verified against the narrative reference `docs/reference/acp_server_node_reference.md` §"Load-bearing weirdness: coerceToolCall" lines 166-195 and the locked D-09 9-step + D-10 edge cases.
2. Property tests prove the four load-bearing invariants (never-panic, idempotent, no-match-no-mutation, deterministic tie-break) across 1000 random inputs each.
3. Table-driven tests cover the D-09 happy path + D-10 edge cases (array/scalar/null/empty/no-properties/no-tools/pre-existing) AND the two REVIEW additions (inline-fenced-in-prose no-coerce + kiro-native-narration no-coerce).
4. Post-ship LangChain integration smoke (Pi-SDK / LangFlow real users hitting the gateway with JSON-as-text) will surface any divergent behavior as bug reports. A follow-up parity verification todo is recommended for post-ship review.

Per the checkpoint's Path C semantics ("approved with risk accepted unblocks Wave 2 — the gate enforces a conscious decision, not a fix-everything bar"), **Wave 2 (06-02 Ollama, 06-03 OpenAI, 06-04 Anthropic) is unblocked**.

Because Task 4 was a checkpoint:human-verify gate (no code change), there is no commit for Task 4; the acceptance is documented here.

## Authentication Gates

None encountered. All work was local code + test changes.

## Known Stubs

None. All code paths are wired end-to-end through the test matrix. No placeholder data flows to UI rendering; no "coming soon" / "TODO" text added in this plan. The Phase 1.1 `tool_call` placeholder (which this plan explicitly closes) has been removed from `internal/acp/translate.go`.

## Wave 2 Readiness Confirmation

The plan's success criteria are met:

- [x] `canonical.ToolCallChunk` has `ID string` field with the two-source doc comment (Phase 6 D-08); no JSON tag.
- [x] `internal/acp/translate.go` tool_call / tool_call_chunk branch promotes to `ChunkKindToolCall` with populated `ToolCall.ID/Name/Args` (Phase 6 D-03 part 1).
- [x] `engine.Collect` aggregates kiro-native `ChunkKindToolCall` into the assistant text as `[tool: <name>]\n` narration (iteration-3 fix to HIGH #1); does NOT populate `Message.ToolCalls` from any chunk source.
- [x] Per-surface `Message.ToolCalls` population contract honored (HIGH #3 iteration-3 wording).
- [x] `engine.CoerceToolCall(req, resp)` is exported, canonical-typed, idempotent (D-02), follows the locked D-09 9-step algorithm with D-10 edge cases (INCLUDING the inline-fenced-prose AND the new kiro-native narration no-coerce cases), and uses unix-nano ID per D-11.
- [x] `engine.buildBlocks` emits the full JSON tool catalog inside `[Available tools]` when `req.Tools` is non-empty (D-16) with a debug log on the marshal-fallback path (REVIEW LOW #6).
- [x] Property tests cover NeverPanics + Idempotent + NoMatchNoMutation + TieBreaker invariants per D-12 with MaxCount=1000.
- [x] `ExampleCoerceToolCall` runs clean (TRST-07).
- [x] Node byte-fidelity checkpoint resolved per REVIEW HIGH #3 (Path C accepted) — Wave 2 unblocks.
- [x] Zero new external dependencies; `go.uber.org/goleak` + `testing/quick` were already available.
- [x] Full module test sweep green under `-race -count=1`.

Wave 2 (06-02 / 06-03 / 06-04) may proceed in parallel.

## Self-Check: PASSED

Verified all files mentioned in this SUMMARY exist on disk and all commits are reachable from HEAD:

- `internal/engine/coerce.go` — FOUND (264 lines)
- `internal/engine/coerce_test.go` — FOUND (432 lines)
- `internal/canonical/chunk.go` — FOUND (modified)
- `internal/acp/translate.go` — FOUND (modified)
- `internal/acp/translate_test.go` — FOUND (modified)
- `internal/acp/integration_test.go` — FOUND (modified)
- `internal/engine/build_acp.go` — FOUND (modified)
- `internal/engine/build_acp_test.go` — FOUND (modified)
- `internal/engine/collect.go` — FOUND (modified)
- `internal/engine/collect_test.go` — FOUND (modified)

Commits (all FOUND in `git log`):

- `7d148ab` — test(06-01): add failing tests for Task 1
- `803b5a6` — feat(06-01): Task 1 GREEN
- `5223db3` — test(06-01): add failing tests for Task 2
- `239cd7b` — feat(06-01): Task 2 GREEN
- `e8d2189` — test(06-01): add failing tests for Task 3
- `312902e` — feat(06-01): Task 3 GREEN (CoerceToolCall implementation)
