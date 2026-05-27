---
phase: 06-tool-call-path
verified: 2026-05-27T13:46:21Z
status: human_needed
score: 5/5 must-haves verified
overrides_applied: 0
human_verification:
  - test: "loop24-client messages.stream() SDK conformance against live binary"
    expected: "@anthropic-ai/sdk MessageStream emits content_block_start -> content_block_delta -> content_block_stop events with a complete tool_use block carrying object input AND stop_reason:'tool_use' in BOTH streaming and non-streaming paths"
    why_human: "SDK parser conformance — only the real @anthropic-ai/sdk client can verify that the streamed bytes are parsed into a structurally-correct tool_use block at the application layer. The byte-level golden test TestSSE_Golden_ToolUse locks the wire shape, but the SDK parser's interpretation requires the live loop24-client smoke run per VALIDATION.md 'Manual-Only Verifications'. This is the Task 4 checkpoint from plan 06-05 that the planner deliberately deferred from a workflow.human_verify_mode=end-of-phase checkpoint."
    instructions: |
      1. Set OTTO_E2E=1 and start the gateway: `go run ./cmd/otto-gateway`
      2. From loop24-client repo: `ANTHROPIC_BASE_URL=http://localhost:11434 npm run smoke:tool-use`
      3. Assert SDK emits content_block_start -> content_block_delta -> content_block_stop events
      4. Assert final message.content includes a complete tool_use block with object input (NOT null, NOT JSON string)
      5. Assert stop_reason is "tool_use" (NOT "end_turn")
      6. Run in both streaming and non-streaming modes (messages.create vs messages.stream)
---

# Phase 6: Tool-Call Path Verification Report

**Phase Goal:** Tool calls flow correctly in both directions, in both surfaces' native shapes, including the `coerceToolCall` fallback for models that emit plain JSON (or markdown-fenced JSON) as text. This is the load-bearing LangChain-compat behavior from the Node reference.

**Verified:** 2026-05-27T13:46:21Z
**Status:** human_needed
**Re-verification:** No — initial verification

## Goal Achievement

### Observable Truths

| # | Truth | Status | Evidence |
|---|-------|--------|----------|
| 1 | SC #1: `/v1/chat/completions` with `tools[]` yielding a tool-call returns `choices[].message.tool_calls[].function.arguments` as a JSON **string** (OpenAI convention) | VERIFIED | `internal/adapter/openai/render.go:120` `Arguments string` json:"arguments"; line 181 `json.Marshal(tc.Arguments)` then `string(argsJSON)` at line 194. `TestChatResponseToCompletion_ToolCalls/CoerceSynthesizedToolCalls_ArgumentsIsJSONString` PASSES, asserting `"arguments":"{\"location\":\"NYC\"}"` (JSON-encoded string). Streaming counterpart `TestSSE_Golden_StreamingCoerce_BareJSON` PASSES |
| 2 | SC #2: `/api/chat` with `tools[]` yielding a tool-call returns `message.tool_calls[].function.arguments` as a plain **object** (Ollama convention) | VERIFIED | `internal/adapter/ollama/wire.go:66` `Arguments map[string]any` json:"arguments"; `render.go:96` assigns `Arguments: tc.Arguments` verbatim (no marshal). `TestChatResponseToWire_ToolCalls/single_coerce_synthesized_plain_object_args` PASSES, asserting `"arguments":{"location":"NYC"}` (object literal). Streaming counterpart `TestNDJSON_StreamingCoerce_BareJSON` PASSES |
| 3 | SC #3: When `tools` provided and model returns bare JSON or markdown-fenced JSON as text, `coerceToolCall` converts it to synthetic `tool_calls` entry — best tool selected via property-overlap scoring — and surface adapter renders in native shape | VERIFIED | `internal/engine/coerce.go:92` `CoerceToolCall(req, resp) bool` implements the locked D-09 9-step algorithm. `pickBestTool` at line 187 uses slice-index iteration for deterministic first-declared tie-break (D-10). `stripFences` at line 251 handles both fenced (```json) and bare (```) wrapping. Invoked from `ollama/handlers.go:90`, `openai/handlers.go:122`, `ollama/ndjson.go:407`, `openai/sse.go:510`. NOT invoked from anthropic (verified via grep — only doc comments). `TestCoerceToolCall_AlgorithmCases` PASSES (14 subtests including bare-JSON, fenced-JSON, bare-fence, inline-fenced-in-prose no-coerce, kiro-native-narration no-coerce, tie-breaker). `TestCoerceToolCall_TieBreaker` PASSES |
| 4 | SC #4: Tool definitions from both request shapes (OpenAI `tools[].function`, Ollama tool spec) normalized into one canonical tool spec consumed by engine | VERIFIED | Three independent decoders all populate `canonical.ToolSpec`: `openai/wire.go:186` per-entry json.RawMessage tolerant decoder; `anthropic/wire.go:255` with input_schema mapping; `ollama/wire.go:340` Phase 2 forward-seam. `internal/engine/build_acp.go` emits the full JSON tool catalog inside `[Available tools]` consumed by kiro-cli. `TestWireToChatRequest_Tools` PASSES for both OpenAI and Anthropic; Ollama covered by `TestWireToChatRequest_Tools_Phase2Regression` |
| 5 | SC #5: Property tests (`pgregory.net/rapid` or `testing/quick`) cover `coerceToolCall` round-trip + never-panic invariants and canonical-tool-spec translator for both surfaces | VERIFIED | `internal/engine/coerce_test.go` uses `testing/quick` with `MaxCount: 1000` for `TestCoerceToolCall_NeverPanics` (line 89), `TestCoerceToolCall_Idempotent` (line 137). Also `TestCoerceToolCall_NoMatchNoMutation`, `TestCoerceToolCall_TieBreaker` (deterministic across 1000 iterations), `TestCoerceToolCall_AlgorithmCases` (14 subtests). Translator tests across all three surfaces: `TestWireToChatRequest_Tools` (OpenAI), `TestWireToChatRequest_Tools_Anthropic`, `TestWireToChatRequest_Tools_Phase2Regression` (Ollama). All PASS |

**Score:** 5/5 truths verified

### Required Artifacts

| Artifact | Expected | Status | Details |
|----------|----------|--------|---------|
| `internal/canonical/chunk.go` | `ToolCallChunk.ID` field added (D-08) | VERIFIED | Line 57: `ID string` field exists; line 47 doc comment cites two-source nature |
| `internal/engine/coerce.go` | CoerceToolCall + pickBestTool + stripFences implementations | VERIFIED | 264 lines, all three functions present at lines 92, 187, 251. Package-level doc cites D-01 + per-surface contract |
| `internal/engine/coerce_test.go` | Property tests + table + Example | VERIFIED | 432 lines. `testing/quick.Config{MaxCount: 1000}` used in NeverPanics, Idempotent, TieBreaker. ExampleCoerceToolCall is present and PASSES |
| `internal/engine/collect.go` | Aggregates ChunkKindToolCall as `[tool: <name>]\n` narration | VERIFIED | Lines 67-95: ChunkKindToolCall case appends `[tool: <name>]\n` to `sb` text accumulator. Does NOT touch Message.ToolCalls (verified via grep). Per-surface contract comment block present |
| `internal/engine/build_acp.go` | JSON tool catalog inside `[Available tools]` | VERIFIED | `availableToolWire` private struct with lowercase JSON tags preserves canonical Phase 2 D-11 invariant. Marshal-failure debug-log fallback per REVIEW LOW #6 |
| `internal/acp/translate.go` | tool_call/tool_call_chunk → ChunkKindToolCall | VERIFIED | Lines 234-256: case branch returns real `canonical.Chunk{Kind: ChunkKindToolCall, ToolCall: &ToolCallChunk{ID, Name, Args}}` (firstNonEmpty for empty title) |
| `internal/adapter/ollama/wire.go` + render.go | ollamaChatResponseMessage.ToolCalls + plain-object Arguments | VERIFIED | wire.go has `Arguments map[string]any`; render.go assigns verbatim (no json.Marshal) |
| `internal/adapter/ollama/handlers.go` + ndjson.go | engine.CoerceToolCall invocation (non-streaming + streaming end-of-stream with sawKiroNativeToolCall) | VERIFIED | handlers.go:90 non-streaming; ndjson.go:407 streaming end-of-stream; sawKiroNativeToolCall flag at line 67 |
| `internal/adapter/openai/wire.go` + render.go + sse.go | tools[] per-entry RawMessage decoder + JSON-string args + multi-frame SSE | VERIFIED | wire.go:43 `Tools []json.RawMessage`; render.go:120 `Arguments string`; sse.go: chunkDelta.ToolCalls, sawKiroNativeToolCall, finalizeSSE skip-or-coerce-or-flush |
| `internal/adapter/anthropic/collect.go` (NEW) | CollectAnthropicChat — D-07 exception aggregator | VERIFIED | 164 lines. Wraps eng.Run; aggregates ChunkKindToolCall into ContentKindToolUse parts + Message.ToolCalls (D-07 exception) |
| `internal/adapter/anthropic/wire.go` + sse.go + render.go + handlers.go | tools[].input_schema decoder + native tool_use SSE + stop_reason override + NO-coerce | VERIFIED | wire.go:255 canonical.ToolSpec assembly; sse.go: toolUseBlockHeader with `Input *map[string]any` (CR-01 pointer-to-empty-map per Pitfall 1); render.go: stop_reason override; handlers.go: PHASE 6 INVARIANT block — calls CollectAnthropicChat NOT eng.Collect, NOT engine.CoerceToolCall |
| `tests/e2e/cmd/fake-kiro-cli/main.go` (NEW) | Controllable fake-kiro handling all ACP methods | VERIFIED | 223 lines; pure Go; supports initialize/session/new/session/set_model/session/prompt/session/cancel/ping + EOF cleanup |
| `tests/e2e/tools_*.go` + tools_fixtures_test.go + tools_testmain_test.go | E2E matrix across three surfaces + cancel scenario | VERIFIED | tools_ollama_test.go (466 lines), tools_openai_test.go (374 lines), tools_anthropic_test.go (295 lines), tools_cancel_test.go (215 lines), tools_fixtures_test.go (576 lines), tools_testmain_test.go (80 lines). `go vet -tags e2e ./tests/e2e/...` clean |

### Key Link Verification

| From | To | Via | Status | Details |
|------|-----|-----|--------|---------|
| ACP translate (case tool_call/tool_call_chunk) | canonical.ToolCallChunk{ID, Name, Args} | body.ToolCallID, body.Title, body.Args | WIRED | translate.go:234-256 returns real ChunkKindToolCall |
| engine.CoerceToolCall | resp.Message.ToolCalls (Ollama/OpenAI sole producer) | pickBestTool top-level-key property overlap | WIRED | coerce.go:149 picks best; lines 159-169 append synthetic ToolCall with call_<unix-nano> ID |
| engine.Collect (ChunkKindToolCall branch) | resp.Message.Content[textIdx].Text | Per-chunk text-append; Message.ToolCalls untouched | WIRED | collect.go:67-95 appends `[tool: <name>]\n` to sb; never touches Message.ToolCalls |
| engine.buildBlocks | ACP `[Available tools]` block content | json.Marshal(availableToolWire) inside fenced ```json``` block | WIRED | build_acp.go emits the catalog with debug-log on marshal failure |
| ollama/handlers.go (non-streaming) | engine.CoerceToolCall | Direct call between Collect and render | WIRED | handlers.go:90 with REVIEW LOW #7 defensive length-guard |
| ollama/ndjson.go (streaming end-of-stream) | engine.CoerceToolCall on buffered text — ONLY when !sawKiroNativeToolCall | Stream-end aggregator builds synthetic resp | WIRED | ndjson.go:407 inside conditional; iteration-3 skip-or-coerce-or-flush |
| openai/handlers.go (non-streaming) | engine.CoerceToolCall | Direct call between Collect and render | WIRED | handlers.go:122 with defensive length-guard |
| openai/sse.go (end-of-stream) | engine.CoerceToolCall on buffered text — ONLY when !sawKiroNativeToolCall | Synthetic resp + multi-frame SSE emit on hit | WIRED | sse.go:510 inside tryStreamingCoerce |
| anthropic/handlers.go (non-streaming) | CollectAnthropicChat | Direct call (NOT eng.Collect, NOT engine.CoerceToolCall) | WIRED | handlers.go:161 — D-07 exception path |
| anthropic/sse.go (applyChunk ChunkKindToolCall) | content_block_start{tool_use, input:{}} + delta + stop | CR-01 pointer-to-empty-map (Pitfall 1) | WIRED | sse.go: applyChunk renders native tool_use sequence; toolUseEmitted finalizer overrides stop_reason to "tool_use" |
| anthropic/render.go (non-streaming) | stop_reason:"tool_use" override | post-fixup on content containing tool_use block | WIRED | render.go override logic; TestRender_ToolUse_StopReasonOverride PASSES |

### Data-Flow Trace (Level 4)

| Artifact | Data Variable | Source | Produces Real Data | Status |
|----------|---------------|--------|--------------------|--------|
| `internal/engine/coerce.go::CoerceToolCall` | `resp.Message.ToolCalls` (append) | Synthesized from parsed JSON in assistant text + matching tool spec from `req.Tools` | YES — synthetic `ToolCall{ID, Name, Arguments}` with real parsed args map | FLOWING |
| `internal/adapter/ollama/render.go::chatResponseToWire` | `out.Message.ToolCalls` | Loop over `resp.Message.ToolCalls` (populated by CoerceToolCall) | YES — Arguments passed as plain `map[string]any` to wire | FLOWING |
| `internal/adapter/openai/render.go::chatResponseToCompletion` | `out.Choices[0].Message.ToolCalls` | Loop over `resp.Message.ToolCalls` (populated by CoerceToolCall) | YES — Arguments marshaled to JSON-string via `json.Marshal` | FLOWING |
| `internal/adapter/anthropic/collect.go::CollectAnthropicChat` | `Message.ToolCalls` + `Message.Content` (ContentKindToolUse parts) | Direct from kiro-native `ChunkKindToolCall` chunks (D-07 exception) | YES — real tool_call data from ACP stream | FLOWING |
| `internal/adapter/anthropic/sse.go::applyChunk` (ChunkKindToolCall) | `content_block_start{tool_use, input:*emptyMap}` SSE frame | Direct from chunk.ToolCall.{ID, Name, Args} | YES — CR-01 pointer-to-empty-map preserves `"input":{}` on wire | FLOWING |
| `internal/engine/build_acp.go` | `[Available tools]` ACP block | `req.Tools` (populated by adapter wire decoders) | YES — full JSON catalog with lowercase wire tags | FLOWING |

### Behavioral Spot-Checks

| Behavior | Command | Result | Status |
|----------|---------|--------|--------|
| Full build | `go build ./...` | Exit 0, no output | PASS |
| go vet (full module) | `go vet ./...` | Exit 0, no output | PASS |
| go vet (e2e tag) | `go vet -tags e2e ./tests/e2e/...` | Exit 0, no output | PASS |
| Engine + ACP unit tests | `go test ./internal/engine/... ./internal/acp/... -count=1` | All PASS | PASS |
| Ollama adapter tests | `go test ./internal/adapter/ollama/... -count=1` | All PASS | PASS |
| OpenAI adapter tests | `go test ./internal/adapter/openai/... -count=1` | All PASS | PASS |
| Anthropic adapter tests | `go test ./internal/adapter/anthropic/... -count=1` | All PASS | PASS |
| CoerceToolCall property tests (MaxCount=1000) | `go test ./internal/engine -run TestCoerceToolCall -count=1` | All PASS (NeverPanics, Idempotent, NoMatchNoMutation, TieBreaker, AlgorithmCases — 14 subtests) | PASS |
| OpenAI JSON-string args canary | `go test ./internal/adapter/openai -run TestChatResponseToCompletion_ToolCalls/CoerceSynthesizedToolCalls_ArgumentsIsJSONString -count=1` | PASS — asserts `"arguments":"{\"location\":\"NYC\"}"` (escaped JSON string) | PASS |
| Ollama plain-object args canary | `go test ./internal/adapter/ollama -run TestChatResponseToWire_ToolCalls/single_coerce_synthesized_plain_object_args -count=1` | PASS — asserts `"arguments":{"location":"NYC"}` (object literal) | PASS |
| Anthropic D-07 native tool_use | `go test ./internal/adapter/anthropic -run TestSSE_Golden_ToolUse -count=1` | PASS — input:{} literal present, input:null absent, stop_reason:tool_use, block-index 0,0,0,1,1,1 | PASS |
| Anthropic NO-coerce asymmetry | `go test ./internal/adapter/anthropic -run TestAnthropic_NoCoerce_Behavioral -count=1` | PASS — bare JSON text preserved, no tool_use synthesized, stop_reason stays end_turn | PASS |
| Static-source no-coerce guard | `go test ./internal/adapter/anthropic -run TestAnthropic_DoesNotCallCoerceToolCall -count=1` | PASS — handlers.go has no engine.CoerceToolCall code reference (doc comments stripped by stripGoComments helper) | PASS |
| ExampleCoerceToolCall godoc | `go test ./internal/engine -run ExampleCoerceToolCall -count=1` | PASS | PASS |

### Requirements Coverage

| Requirement | Source Plan | Description | Status | Evidence |
|-------------|------------|-------------|--------|----------|
| TOOL-01 | 06-01, 06-02, 06-03, 06-04, 06-05 | Tool-call output rendered in surface's native shape — OpenAI JSON-string args, Ollama plain-object args | SATISFIED | OpenAI: render.go:120 `Arguments string`, TestChatResponseToCompletion_ToolCalls PASSES. Ollama: wire.go:66 `Arguments map[string]any`, TestChatResponseToWire_ToolCalls PASSES. Anthropic: native tool_use blocks via CollectAnthropicChat + sse.go, TestSSE_Golden_ToolUse PASSES |
| TOOL-02 | 06-01, 06-02, 06-03, 06-04, 06-05 | coerceToolCall behavior: tools provided + model returns bare JSON / fenced JSON as text → synthetic tool_calls entry; best tool via property-overlap | SATISFIED | engine/coerce.go implements D-09 9-step algorithm; pickBestTool uses property-overlap scoring with deterministic first-declared tie-break (D-10). TestCoerceToolCall_AlgorithmCases covers bare/fenced/bare-fence/inline-fenced-prose/kiro-native-narration. Anthropic NO-coerce asymmetry verified by TestAnthropic_NoCoerce_Behavioral + static-source guard |
| TOOL-03 | 06-01, 06-02, 06-03, 06-04, 06-05 | Tool definitions in OpenAI + Ollama (and Anthropic) request shapes normalized to canonical.ToolSpec | SATISFIED | All three adapters populate canonical.ToolSpec: openai/wire.go:186 (per-entry RawMessage tolerant), anthropic/wire.go:255 (input_schema mapping), ollama/wire.go:340 (Phase 2 forward-seam). engine.buildBlocks emits unified JSON tool catalog for kiro |

### Anti-Patterns Found

| File | Line | Pattern | Severity | Impact |
|------|------|---------|----------|--------|
| (none) | — | — | — | No TBD/FIXME/XXX debt markers found in files modified by this phase. Doc-comment mentions of CoerceToolCall in Anthropic adapter are intentional invariant documentation (verified via stripGoComments-aware static test) |

Grep confirms zero unresolved debt markers:
- `grep -n "TBD\|FIXME\|XXX" internal/engine/coerce.go internal/engine/coerce_test.go internal/canonical/chunk.go internal/engine/collect.go internal/engine/build_acp.go internal/acp/translate.go internal/adapter/ollama/render.go internal/adapter/ollama/ndjson.go internal/adapter/openai/sse.go internal/adapter/anthropic/collect.go` returns 0 lines.

### Human Verification Required

#### 1. loop24-client messages.stream() SDK conformance against live binary

**Test:** Run loop24-client `@anthropic-ai/sdk` against the running otto-gateway binary using a tool-use scenario.

**Expected:**
- SDK emits `content_block_start` → `content_block_delta` → `content_block_stop` events in correct order
- Final `message.content` includes a complete `tool_use` block with object `input` (NOT null, NOT JSON string)
- `stop_reason` is `"tool_use"` (NOT `"end_turn"`)
- Behavior is consistent across BOTH streaming (`messages.stream()`) AND non-streaming (`messages.create()`) modes
- REVIEW MEDIUM #4 verified at the SDK layer: non-streaming path returns `stop_reason:"tool_use"` (not `"end_turn"`) when content contains a tool_use block

**Why human:** SDK parser conformance is fundamentally a manual UAT — only the real `@anthropic-ai/sdk` client running in a loop24-client process can verify that the streamed bytes are parsed into a structurally-correct `tool_use` block at the application layer. The byte-level golden test (`TestSSE_Golden_ToolUse`) and the wire-level E2E test (`tools_anthropic_test.go::NativeToolCall_Streaming`) lock the wire shape, but the SDK parser's interpretation of those bytes requires the live `npm run smoke:tool-use` from the loop24-client repository. This is the Task 4 checkpoint from plan 06-05, deliberately deferred via `workflow.human_verify_mode = end-of-phase`, and the only outstanding item per `VALIDATION.md` "Manual-Only Verifications".

**Instructions:**
1. From otto-gateway repo root: `OTTO_E2E=1 go run ./cmd/otto-gateway` (gateway listens on port 11434)
2. From loop24-client repo: `ANTHROPIC_BASE_URL=http://localhost:11434 npm run smoke:tool-use`
3. Inspect the SDK event log — assert the three event types appear in order
4. Inspect the final response — assert `content[].tool_use.input` is an object (e.g. `{location:"NYC"}`) NOT null and NOT a string `"{\"location\":\"NYC\"}"`
5. Assert `stop_reason === "tool_use"`
6. Re-run with `messages.create()` (non-streaming) — assert same response shape

### Gaps Summary

No gaps. All five Success Criteria are verified by passing unit, golden, property, and E2E-compile tests. The phase has exactly one outstanding item: the HUMAN-UAT checkpoint for loop24-client SDK conformance, which is by design (per `VALIDATION.md` "Manual-Only Verifications") and per the planner's `workflow.human_verify_mode = end-of-phase` directive that surfaced Task 4 of plan 06-05 to this verification stage.

All three TOOL-* requirements are SATISFIED at the code+test level. The Node byte-fidelity checkpoint (originally task 4 of 06-01) was accepted via Path C ("post-ship LangChain integration smoke is the verification") — that decision is documented in 06-01-SUMMARY.md as an explicit risk acceptance and does not block phase completion.

---

_Verified: 2026-05-27T13:46:21Z_
_Verifier: Claude (gsd-verifier)_
