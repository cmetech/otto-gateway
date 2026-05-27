---
phase: 06-tool-call-path
plan: 04
subsystem: adapter/anthropic
tags: [tool-call, anthropic, sse, tool-use, no-coerce, stop-reason, per-surface-contract]

# Dependency graph
requires:
  - phase: 06-01
    provides: "canonical.ToolCallChunk.ID; engine.Run chunk channel + StopWatchdog seam; engine.Collect non-tool-call aggregation contract (the reference behavior CollectAnthropicChat must match for non-tool-call dimensions per the iteration-3 MEDIUM #5 parity tests); engine.CoerceToolCall — the symbol whose ABSENCE on the Anthropic surface is the load-bearing D-01 invariant"

provides:
  - "internal/adapter/anthropic/collect.go::CollectAnthropicChat — the D-07 Anthropic exception to the per-surface Message.ToolCalls population contract (Option A1, locked per WARNING #2). Aggregates kiro-native ChunkKindToolCall into ContentKindToolUse parts + Message.ToolCalls."
  - "internal/adapter/anthropic/wire.go — closes the TODO(Phase 6) block inside wireToChatRequest: tools[].input_schema -> canonical.ToolSpec.Parameters; tool_choice polymorphic decode with lossless 'any' preservation per REVIEW MEDIUM."
  - "internal/adapter/anthropic/sse.go — ChunkKindToolCall native tool_use streaming sequence (content_block_start{tool_use, input:{}} + content_block_delta{input_json_delta} + content_block_stop) with CR-01 pointer-to-empty-map (Pitfall 1) and one-bump-per-kind-transition (Pitfall 7) preservation. toolUseEmitted finalizer override sets stop_reason:'tool_use' on message_delta."
  - "internal/adapter/anthropic/render.go — non-streaming stop_reason override to 'tool_use' when content contains a tool_use block (REVIEW MEDIUM #4 — mirrors the streaming finalizer)."
  - "internal/adapter/anthropic/handlers.go::handleMessages — non-streaming branch now invokes CollectAnthropicChat instead of eng.Collect. PHASE 6 INVARIANT doc comment documents the D-01 no-coerce asymmetry rationale and cites both regression tests."

affects:
  - 06-05-tool-call-e2e (Anthropic non-streaming + streaming tool-call paths now wired end-to-end; ready for E2E scenarios 1+2)

# Tech tracking
tech-stack:
  added: []  # zero new external dependencies
  patterns:
    - "Per-surface Message.ToolCalls population contract (Phase 6 D-07): the Anthropic surface is the documented exception — kiro-native ChunkKindToolCall is aggregated by the adapter-local CollectAnthropicChat (Option A1), NOT engine.CoerceToolCall (Ollama/OpenAI) and NOT engine.Collect (which produces `[tool: <name>]\\n` narration text for the other two surfaces)."
    - "CR-01 pointer-to-empty-map (Pitfall 1): tool_use.input is `*map[string]any` (not `map[string]any`) so encoding/json.omitempty preserves `\"input\":{}` rather than dropping the field (default for nil/empty maps) or emitting `\"input\":null` (default for nil-pointer maps). The pointer indirection is the load-bearing trick; tested via byte-level golden assertion in TestSSE_Golden_ToolUse."
    - "Static-source assertion + behavioral assertion as defense-in-depth (REVIEW LOW #9): a Go-comment-aware static-source guard catches refactor regressions at compile-time, complemented by a behavioral test that exercises the live HTTP handler with bare-JSON assistant text + matching tools[] catalog. The static guard's comment-stripper (stripGoComments) lets PHASE 6 INVARIANT documentation MENTION the absent symbol without false-positive trips."
    - "Lossless cross-surface tool_choice preservation: Anthropic 'any' is preserved verbatim in canonical.ToolChoice.Type rather than silently mapped to OpenAI's 'required'. Cross-surface semantic mapping is engine/hook concern, not adapter — the decode layer's job is to capture the wire shape losslessly."
    - "Fake-engine test-double upgrade pattern: when a handler swap moves the invocation from eng.Collect to eng.Run, existing tests that script the old call still work by synthesizing a single-shot RunHandle from the canned collectResp (replay each ContentPart as a chunk). Keeps the test-blast-radius bounded."

key-files:
  created:
    - internal/adapter/anthropic/collect.go (164 lines — CollectAnthropicChat + assembleAnthropicChatResponse)
    - internal/adapter/anthropic/collect_test.go (332 lines — 5 ParityWithEngine tests + AnthropicException_ToolCallProducesToolUse)
  modified:
    - internal/adapter/anthropic/wire.go (+55 lines: anthropicToolChoiceObject type, closes TODO(Phase 6), decodeAnthropicToolChoice helper)
    - internal/adapter/anthropic/wire_test.go (+184 lines: 7 new wire-decode tests including ToolChoice_Any_PreservedVerbatim)
    - internal/adapter/anthropic/sse.go (+86 lines: toolUseBlockHeader/inputJSONDelta wire types, ChunkKindToolCall branches in applyChunk header+delta switches, toolUseEmitted finalizer override)
    - internal/adapter/anthropic/sse_golden_test.go (+97 lines: TestSSE_Golden_ToolUse with byte-level input:{} + stop_reason:tool_use + block-index sequence assertions)
    - internal/adapter/anthropic/render.go (+15 lines: hasToolUse loop + stop_reason override per REVIEW MEDIUM #4)
    - internal/adapter/anthropic/render_test.go (+85 lines: TestRender_ToolUse_StopReasonOverride with 4 sub-cases)
    - internal/adapter/anthropic/handlers.go (+25 lines: PHASE 6 INVARIANT doc comment + swap eng.Collect -> CollectAnthropicChat)
    - internal/adapter/anthropic/handlers_test.go (+236 lines: TestAnthropic_NoCoerce_Behavioral + TestAnthropic_DoesNotCallCoerceToolCall + stripGoComments helper + fakeEngine.Run auto-synth from collectResp + collectN/runN swap on Happy_NonStreaming)
    - internal/adapter/anthropic/sse_test.go (+/-3 lines: TestApplyChunk_UnsupportedKindDropped_NoIndexBump switched to ChunkKindPlan since ChunkKindToolCall is now supported)

key-decisions:
  - "Lossless 'any' preservation in canonical.ToolChoice (REVIEW MEDIUM). Anthropic's `tool_choice: {type:'any'}` is preserved verbatim as canonical.ToolChoice{Type:'any'}, NOT silently mapped to OpenAI's 'required'. Rationale: cross-surface semantic interpretation is engine/hook concern, not adapter; the decode layer's job is lossless capture. Documented inline above the polymorphic decode + locked by TestWireToChatRequest_ToolChoice_Any_PreservedVerbatim. Future engine code that wants to treat them as equivalents must do that translation explicitly."
  - "Option A1 (adapter-local collect.go wrapping eng.Run) is the LOCKED factoring. Option B (engine-side switch or new canonical flag) was considered and rejected because it leaks adapter concerns into the engine and either expands the canonical type surface or branches engine code on adapter identity — both violate the per-surface contract. The collect.go file-level doc comment captures this trade-off so future readers don't relitigate."
  - "fakeEngine.Run auto-synthesizes a RunHandle from collectResp when runHandle is nil. This is the Rule-3 fix to keep the existing handlers_session_test.go + handlers_test.go suite green after the handler swapped from eng.Collect to CollectAnthropicChat. Replays each ContentPart back as a chunk so the aggregator reproduces the same canonical response. Tests that genuinely care about the Run path (streaming) still set runHandle explicitly."
  - "TestApplyChunk_UnsupportedKindDropped_NoIndexBump migrated from ChunkKindToolCall (now supported per Task 2) to ChunkKindPlan (still the only dormant ChunkKind). The drop-without-state-change contract is still tested; only the dormant kind used as the test vector changed."
  - "The behavioral no-coerce test (TestAnthropic_NoCoerce_Behavioral) is the PRIMARY regression guard per REVIEW LOW #9; the static-source test is belt-and-suspenders defense-in-depth. The static test required a Go-comment-aware stripper (stripGoComments) so the PHASE 6 INVARIANT doc-comment that MENTIONS the absent symbol does not trip a false positive — naive substring search would have."

patterns-established:
  - "Pattern: comment-aware static-source assertion. For tests that lock a symbol's ABSENCE at the byte-level, strip Go line + block comments before substring-searching. The documentation block that explains WHY the symbol is absent is itself a valuable invariant marker; the test should not be hostile to it."
  - "Pattern: fake-engine response synthesis for handler-swap blast-radius containment. When a handler swaps its invocation from one engine method to another, upgrade the fake to auto-route from the legacy canned response to the new invocation path. Existing tests that scripted the old method via legacy field setters keep working transparently."
  - "Pattern: parity test suite as silent-drift regression guard. When an adapter-local helper duplicates an engine-internal function's aggregation loop (Option A1 factoring), a parity test suite proving equivalence on the non-divergent dimensions catches future maintenance drift before it reaches production. The test runs the SAME fake-chunk-stream fixture through BOTH collectors and asserts field-by-field equivalence."

requirements-completed: [TOOL-01, TOOL-02, TOOL-03]

# Metrics
duration: 28min
completed: 2026-05-27
---

# Phase 6 Plan 04: Anthropic Vertical Slice Summary

**Closes the Anthropic side of the tri-surface tool-call path. tools[] decode lit up (canonical.ToolSpec.Parameters + canonical.ToolChoice polymorphic decode with lossless 'any' preservation). New adapter-local collect.go wraps eng.Run with the D-07 exception that populates Message.ToolCalls + ContentKindToolUse parts from kiro-native ChunkKindToolCall (Option A1, locked per WARNING #2; not engine.Collect / not engine.CoerceToolCall). SSE applyChunk grows native tool_use multi-event sequence with CR-01 pointer-to-empty-map and Pitfall 7 block-index discipline; toolUseEmitted finalizer overrides stop_reason to "tool_use" on message_delta. Non-streaming render.go mirrors the same override (REVIEW MEDIUM #4). D-01 / D-17 scenario 5 no-coerce asymmetry locked behaviorally (REVIEW LOW #9 — primary) and via static-source assertion (belt-and-suspenders).**

## Tasks Completed

| # | Task | Commit | Files |
|---|------|--------|-------|
| 1 RED   | Add failing tests for Anthropic tools/tool_choice decode | `dd189c4` | wire_test.go |
| 1 GREEN | Close TODO(Phase 6) — anthropicToolSpec/tool_choice -> canonical | `2c5ed3e` | wire.go |
| 2 RED   | Add failing tests for ChunkKindToolCall path (parity + golden) | `7f6c912` | collect_test.go (NEW), sse_golden_test.go |
| 2 GREEN | Anthropic-local CollectAnthropicChat + SSE tool_use + handler swap | `d640236` | collect.go (NEW), sse.go, handlers.go, handlers_test.go, sse_test.go, sse_golden_test.go |
| 3 RED   | Add failing test for non-streaming stop_reason:tool_use override | `77a5fec` | render_test.go |
| 3 GREEN | render.go stop_reason override on ContentKindToolUse parts (REVIEW MEDIUM #4) | `a6c9b02` | render.go |
| 4       | NO-coerce asymmetry behavioral + static-source guards (D-01 / D-17 scenario 5) | `d315a68` | handlers.go, handlers_test.go |

Task 4 is presented as a single commit (not RED/GREEN) because the regression guards lock a NEGATIVE behavior (no CoerceToolCall reference today) — they're GREEN by construction at commit time. The static-source comment-stripper utility (`stripGoComments`) was added in the same commit so the PHASE 6 INVARIANT doc-block could safely mention the absent symbol without tripping the static guard.

## What Was Built

### 1. wire.go — TODO(Phase 6) closed (D-14)

`wireToChatRequest` now translates `anthropicToolSpec` -> `canonical.ToolSpec` and `tool_choice` -> `canonical.ToolChoice`. Anthropic's `input_schema` maps DIRECTLY to canonical `Parameters` (both are JSON-schema-shaped object maps). Per-tool defensive skip on empty `name` (Anthropic spec requires it).

`decodeAnthropicToolChoice` polymorphically handles the four shapes:
- `{type:"auto"}` -> `&{Type:"auto"}`
- `{type:"any"}` -> `&{Type:"any"}` (LOSSLESS verbatim preservation per REVIEW MEDIUM — NOT mapped to "required")
- `{type:"tool",name:"X"}` -> `&{Type:"tool",Name:"X"}`
- empty / absent / unknown shape (numeric, etc.) -> `nil` (accept-and-ignore per D-10)

Six wire_test cases lock the decode contract; the `Any_PreservedVerbatim` test is the REVIEW MEDIUM coverage point — it asserts the canonical type stays `"any"` and explicitly checks it is NOT `"required"`.

### 2. collect.go (NEW) — Anthropic-local aggregator (D-07 exception)

`CollectAnthropicChat(ctx, eng, req)` wraps `eng.Run(...)` (which returns the local `RunHandle` — the adapter does not import `internal/engine` per TRST-04) and aggregates the chunk stream manually. Mirrors `engine.Collect`'s text + thinking aggregation loop for parity; adds the D-07 exception branch for `ChunkKindToolCall`:

- Appends a `ContentKindToolUse` ContentPart to `Message.Content` (so non-streaming render.go's tool_use block + stop_reason override fire correctly).
- Appends a `canonical.ToolCall{ID, Name, Arguments}` to `Message.ToolCalls`.

The text/thinking builders are NOT touched on tool_call chunks — no `[tool: <name>]\n` narration on Anthropic (that's the engine.Collect behavior for Ollama/OpenAI's non-streaming path).

The file-level doc comment cites:
- CONTEXT D-07 (the locked decision).
- The 06-01 iteration-3 HIGH #3 per-surface contract wording.
- Option A1 (locked per WARNING #2) vs Option B (rejected; rationale captured).
- Parity test suite reference (iteration-3 MEDIUM #5).

`assembleAnthropicChatResponse` preserves the Phase 3.1 D-02 contract: text part is ALWAYS at Content[0] (may be empty string); thinking appends when present; tool_use parts append after. `Message.ToolCalls` is populated separately on the Message struct.

D-06 teardown: `run.StopWatchdog()` is invoked after natural stream completion, mirroring `engine.Collect`'s watchdog discipline.

### 3. collect_test.go (NEW) — parity suite (iteration-3 MEDIUM #5)

`parityFakeEngine` is a tiny test double whose `Collect` method implements the reference engine.Collect non-tool-call behavior, and whose `Run` method yields the same chunk fixture as a single-shot stream. The FIVE parity tests drive the SAME chunk stream through BOTH collectors and assert field-by-field equivalence:

1. `TestCollectAnthropicChat_ParityWithEngine_TextOnly` — text chunks aggregate into a single ContentKindText part with concatenated text.
2. `TestCollectAnthropicChat_ParityWithEngine_ThinkingOnly` — thinking chunks aggregate into a ContentKindThinking part appended after the (empty) text part.
3. `TestCollectAnthropicChat_ParityWithEngine_MixedTextThinking` — interleaved stream produces the same split: text concatenated in its own part, thinking concatenated in its own part.
4. `TestCollectAnthropicChat_ParityWithEngine_StopReasonPropagation` — table-driven 5-case sweep across StopEndTurn / StopMaxTokens / StopRefusal / StopCancelled / StopUnknown; asserts CollectAnthropicChat's StopReason matches the reference exactly.
5. `TestCollectAnthropicChat_ParityWithEngine_ErrorPropagation` — scripted error via `Stream.Result()`; asserts `errors.Is(err, sentinel)` returns true for both collectors (the wrap layer differs — "anthropic: collect" vs "engine: collect" — but the underlying sentinel is reachable through both).

A SIXTH test (`TestCollectAnthropicChat_AnthropicException_ToolCallProducesToolUse`) locks the D-07 exception: a stream containing a kiro-native tool_call chunk produces a ContentKindToolUse part + a populated Message.ToolCalls entry, AND asserts the text part does NOT contain `[tool: ...]` narration (which would be the engine.Collect behavior).

### 4. sse.go — native tool_use multi-event sequence (D-07)

Two new wire types:
- `toolUseBlockHeader{Type, ID, Name, Input *map[string]any}` — `Input` is `*map[string]any` per CR-01 (Pitfall 1). The pointer-to-empty-map preserves `"input":{}` through `encoding/json.omitempty` rather than dropping the field (len==0 map default) or emitting `"input":null` (nil-pointer default). Anthropic's SDK MessageStream parser rejects null input on tool_use blocks.
- `inputJSONDelta{Type, PartialJSON}` — `Type:"input_json_delta"`, `PartialJSON` carries the full serialized args in ONE delta (kiro emits atomically per D-06; no chunking needed).

`applyChunk` grows `case canonical.ChunkKindToolCall:` in both the header switch (step 1) and the delta switch (step 4):
- Step 1 builds `toolUseBlockHeader` using the CR-01 pattern (`emptyMap := map[string]any{}; ...Input: &emptyMap`).
- Step 4 marshals `c.ToolCall.Args` via `json.Marshal` and emits ONE `content_block_delta` event with the `inputJSONDelta` payload. Defensive: nil/empty args coerce to `"{}"` rather than `"null"` on the wire. Marshal failure logs at debug + drops the delta (does not tear down the stream).
- After successful delta emit, sets `e.toolUseEmitted = true`.

The existing step-2 close-then-bump path handles the kind transition (no extra blockIndex bump per Pitfall 7).

`finalizeStream` consults `e.toolUseEmitted` and overrides the `message_delta` `stop_reason` to `"tool_use"` regardless of the engine's mapped StopReason. Anthropic spec mandates this — the SDK keys its tool-use dispatch on `stop_reason:"tool_use"`.

D-05 single-goroutine invariant: `toolUseEmitted` is touched ONLY inside the select-loop goroutine (applyChunk + finalizeStream). No mutex needed.

### 5. sse_golden_test.go — `TestSSE_Golden_ToolUse`

Drives a fake stream `[text("I'll check weather. "), tool_call(id=toolu_01, name=get_weather, args={location:"NYC"})]` through the real `runSSEEmitter` and asserts:

- Byte-level: `"input":{}` LITERAL is present.
- Byte-level: `"input":null` is NOT present anywhere.
- Byte-level: `"stop_reason":"tool_use"` on message_delta.
- Byte-level: `"stop_reason":"end_turn"` is NOT present.
- Discriminator: `"input_json_delta"` present on the tool_use content_block_delta.
- Args carriage: `"partial_json":"{\"location\":\"NYC\"}"` present (encoding/json sorts map keys deterministically for `map[string]any`, so this is a stable literal).
- Event sequence: `message_start, content_block_start, content_block_delta, content_block_stop, content_block_start, content_block_delta, content_block_stop, message_delta, message_stop`.
- Block-index sequence: `0,0,0,1,1,1` (text block's start/delta/stop at index 0; tool_use block's start/delta/stop at index 1; exactly ONE bump per Pitfall 7).

### 6. render.go — non-streaming stop_reason override (REVIEW MEDIUM #4)

`chatResponseToMessage` now tracks a `hasToolUse` flag while walking `resp.Message.Content`. After the existing `mapStopReason` call, if `hasToolUse` is true, the wire `StopReason` is overridden to `"tool_use"`. Mirrors the streaming finalizer in sse.go.

Without this fix, the non-streaming path would have emitted `stop_reason:"end_turn"` AND a populated tool_use content block — a contradictory pair that triggers undefined `@anthropic-ai/sdk` behavior. The four sub-case test (`TestRender_ToolUse_StopReasonOverride`) locks every relevant combination: text+tool_use (override fires), text-only (no override), tool_use-only (override fires), empty content (no override, canonical mapping respected).

### 7. handlers.go — handler swap + PHASE 6 INVARIANT

Non-streaming branch now invokes `CollectAnthropicChat(r.Context(), eng, req)` instead of `eng.Collect(...)`. The streaming branch's `eng.Run` path is unchanged (SSE emitter already operates on the raw chunk channel).

A `PHASE 6 INVARIANT` doc comment block at the top of the file documents:
- The D-01 + D-17 scenario 5 no-coerce decision and the rationale (wire-shape forgery on messages.stream() consumers).
- The per-surface contract reference (Anthropic exception via CollectAnthropicChat).
- Cross-references to both regression tests (`TestAnthropic_NoCoerce_Behavioral` — primary; `TestAnthropic_DoesNotCallCoerceToolCall` — belt-and-suspenders).

### 8. handlers_test.go — NO-coerce regression guards (REVIEW LOW #9 + D-01)

`TestAnthropic_NoCoerce_Behavioral` (primary guard): drives a fake engine that emits `Message.Content[0] = {Kind: ContentKindText, Text: "{\"location\":\"NYC\"}"}` plus a request carrying a matching `tools: [{name:"get_weather", input_schema: {...}}]` catalog. On Ollama/OpenAI this would trigger `engine.CoerceToolCall` to synthesize a tool_use block; on Anthropic it MUST NOT. The test asserts:
- The text content block is preserved verbatim with the original bare JSON.
- NO `tool_use` content block is synthesized.
- `stop_reason` stays `"end_turn"` (NOT `"tool_use"`).

`TestAnthropic_DoesNotCallCoerceToolCall` (belt-and-suspenders): reads `handlers.go` at test time, strips Go line + block comments via `stripGoComments` (so the PHASE 6 INVARIANT doc-block that MENTIONS the absent symbol does not false-positive), and asserts the `engine.CoerceToolCall` substring does NOT appear in actual code. Failure message cites D-01 + D-17 scenario 5 + the per-surface contract + the primary behavioral test.

### 9. handlers_test.go (fakeEngine upgrade — Rule 3 blast-radius containment)

`fakeEngine.Run` auto-synthesizes a single-shot `RunHandle` from `collectResp` when `runHandle` is nil. Each ContentPart is replayed back as the matching chunk kind (text → ChunkKindText, thinking → ChunkKindThought, tool_use → ChunkKindToolCall). Also replays any preexisting `Message.ToolCalls` entries directly as tool_call chunks. This keeps the existing 8+ tests that scripted the old `collectResp:` convention working transparently after the handler swapped to `CollectAnthropicChat`.

`TestHandleMessages_Happy_NonStreaming` updated its invocation-count assertions: `collectN==0` (handler no longer calls eng.Collect) + `runN==1` (CollectAnthropicChat invokes eng.Run). Belt-and-suspenders proof that the handler swap is locked in place.

`TestApplyChunk_UnsupportedKindDropped_NoIndexBump` migrated from `ChunkKindToolCall` (now supported per Task 2) to `ChunkKindPlan` (still the only dormant kind). The drop-without-state-change contract is preserved; only the dormant kind used as the test vector changed.

## Test Coverage

| Test | Type | Purpose |
|------|------|---------|
| `TestWireToChatRequest_Tools_Anthropic` | functional | Lock input_schema -> canonical.ToolSpec.Parameters direct mapping |
| `TestWireToChatRequest_Tools_DropEmptyName` | functional | Defensive empty-name skip (Anthropic spec requires name) |
| `TestWireToChatRequest_Tools_Empty_Anthropic` | functional | Absent tools[] leaves req.Tools nil (no spurious allocation) |
| `TestWireToChatRequest_ToolChoice_Auto` | functional | `{type:"auto"}` decode |
| `TestWireToChatRequest_ToolChoice_Any_PreservedVerbatim` | functional (REVIEW MEDIUM) | LOSSLESS 'any' preservation; NOT mapped to "required" |
| `TestWireToChatRequest_ToolChoice_NamedTool` | functional | `{type:"tool",name:"X"}` decode |
| `TestWireToChatRequest_ToolChoice_Unknown` | functional | Numeric/unknown shape accept-and-ignore |
| `TestCollectAnthropicChat_ParityWithEngine_TextOnly` | parity (MEDIUM #5) | Text-only stream parity |
| `TestCollectAnthropicChat_ParityWithEngine_ThinkingOnly` | parity (MEDIUM #5) | Thinking-only stream parity |
| `TestCollectAnthropicChat_ParityWithEngine_MixedTextThinking` | parity (MEDIUM #5) | Interleaved text+thinking parity |
| `TestCollectAnthropicChat_ParityWithEngine_StopReasonPropagation` | parity (MEDIUM #5, table-5) | StopReason flows through unchanged |
| `TestCollectAnthropicChat_ParityWithEngine_ErrorPropagation` | parity (MEDIUM #5) | Sentinel reachable via errors.Is on both wrap layers |
| `TestCollectAnthropicChat_AnthropicException_ToolCallProducesToolUse` | functional | D-07 divergence: tool_call -> tool_use parts + Message.ToolCalls, NOT narration text |
| `TestSSE_Golden_ToolUse` | golden | Multi-event byte-level lock; input:{} literal, no input:null, stop_reason:tool_use, block-index 0,0,0,1,1,1 |
| `TestRender_ToolUse_StopReasonOverride/{TextAndToolUse,TextOnly,ToolUseOnly,EmptyContent}` | functional (REVIEW MEDIUM #4, 4 sub-cases) | Non-streaming stop_reason override behavior matrix |
| `TestAnthropic_NoCoerce_Behavioral` | functional (REVIEW LOW #9 primary) | D-01 + D-17 scenario 5 — bare JSON text preserved, no tool_use synthesized, stop_reason stays end_turn |
| `TestAnthropic_DoesNotCallCoerceToolCall` | static-source (belt-and-suspenders) | engine.CoerceToolCall not referenced in handlers.go (Go-comment-aware) |

All tests pass under `go test ./internal/adapter/anthropic/... -race -count=1` and the full module sweep `go test ./... -race -count=1` is clean.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 — Blocking issue] fakeEngine.Run synthesis from collectResp.**

- **Found during:** Task 2 GREEN — after swapping handlers.go from `eng.Collect(...)` to `CollectAnthropicChat(ctx, eng, req)`, `TestAnthropicHandleMessages_NoXSessionId_RoutesToPool` panicked with a nil-pointer dereference at `run.Stream()` because the existing `fakeEngine.runHandle` was nil while only `collectResp` was set.
- **Issue:** Many existing handler tests use the legacy "set collectResp, leave runHandle nil" convention from before Phase 6. The handler swap moved the invocation to eng.Run, breaking that convention silently.
- **Fix:** Upgraded `fakeEngine.Run` to auto-synthesize a `RunHandle` from `collectResp` (each ContentPart replayed back as the matching chunk kind; Message.ToolCalls replayed as tool_call chunks). The synthesis preserves the StopReason via FinalResult and the error via Stream.Result. Tests that genuinely care about the Run path (streaming) still set `runHandle` explicitly and bypass the synthesis branch.
- **Files modified:** `internal/adapter/anthropic/handlers_test.go`
- **Commit:** `d640236` (folded into Task 2 GREEN — same compilation unit).
- **Rule rationale:** Test scaffolding is the same compilation unit as the production change; leaving 9+ tests broken would have blocked the rest of the verification commands. No architectural impact (Rule 4 not applicable). The new synthesis is contained to the fake double and is documented inline.

**2. [Rule 3 — Blocking issue] TestApplyChunk_UnsupportedKindDropped_NoIndexBump kind migration.**

- **Found during:** Task 2 GREEN — the existing SSE state-machine test asserted that `ChunkKindToolCall` was dropped without state change. After Task 2, it is no longer dropped (it is the FIRST-CLASS supported kind that produces native tool_use blocks).
- **Issue:** The dormant-kind drop contract is still important to test, but ChunkKindToolCall is no longer the right vector.
- **Fix:** Migrated the test to `ChunkKindPlan` (still the only remaining dormant kind in Phase 6). The drop-without-state-change semantics are unchanged.
- **Files modified:** `internal/adapter/anthropic/sse_test.go`
- **Commit:** `d640236` (folded into Task 2 GREEN).

**3. [Rule 3 — Blocking issue] TestHandleMessages_Happy_NonStreaming invocation-count assertion swap.**

- **Found during:** Task 2 GREEN — the test asserted `eng.collectN != 1` (legacy: handler called eng.Collect once). After the swap, `collectN == 0` is the correct value; `runN` is what should be 1.
- **Issue:** The assertion encoded the legacy invocation path; preserving it would have failed the test under the correct new behavior.
- **Fix:** Updated to `collectN == 0` AND `runN == 1` as belt-and-suspenders proof that the handler swap is locked in place. Both assertions are valuable: collectN==0 catches a future regression that resurrects the eng.Collect call; runN==1 catches a future regression that bypasses CollectAnthropicChat entirely.
- **Files modified:** `internal/adapter/anthropic/handlers_test.go`
- **Commit:** `d640236` (folded into Task 2 GREEN).

**4. [Rule 1 — Bug] TestSSE_Golden_ToolUse block-index sequence count.**

- **Found during:** Task 2 GREEN test run — the test asserted block-index sequence `0,0,0,0,1,1,1` (7 indices) but the actual wire produces 6 indices: `0,0,0,1,1,1` (start/delta/stop for block 0; start/delta/stop for block 1).
- **Issue:** The plan author miscounted — `message_start` has no `index` field; only `content_block_*` frames do. The total is 3+3=6 indices, not 7.
- **Fix:** Updated the `wantIdx` to `[]string{"0","0","0","1","1","1"}`. The Pitfall 7 "exactly one bump per kind transition" contract is unchanged — only the count was wrong in the assertion.
- **Files modified:** `internal/adapter/anthropic/sse_golden_test.go`
- **Commit:** `d640236` (folded into Task 2 GREEN).
- **Rule rationale:** This is a defect IN THE TEST — the production code is correct; the test's expected value was off-by-one. Catching this kind of arithmetic slip is exactly what running RED tests is for.

**5. [Rule 3 — Blocking issue] Static-source NO-coerce test required a Go-comment stripper.**

- **Found during:** Task 4 final test run — after adding the `PHASE 6 INVARIANT` doc comment block that EXPLAINS the absence of `engine.CoerceToolCall`, the naive `strings.Contains` check in `TestAnthropic_DoesNotCallCoerceToolCall` false-positive-tripped on the doc-comment mention.
- **Issue:** The static-source assertion needs to distinguish documentation mentions of a symbol from actual code references. A naive substring search is hostile to invariant documentation.
- **Fix:** Added a `stripGoComments(src)` helper that walks the source removing `// ...` and `/* ... */` comments while preserving string + raw-string literals. The static guard now operates on the stripped source. The doc-block can safely mention the symbol; only real code references trip the check.
- **Files modified:** `internal/adapter/anthropic/handlers_test.go`
- **Commit:** `d315a68` (folded into Task 4 — same compilation unit).
- **Rule rationale:** This is the right shape of static-source guard for a NEGATIVE invariant. Pure substring search is too blunt; a tiny Go-aware tokenizer is the minimum-viable correctness fix. Future symbol-absence assertions in this repo can reuse the helper.

### Decisions / Notes (not bugs)

**6. Task 4 has no RED commit.**

The two regression guards (`TestAnthropic_NoCoerce_Behavioral` + `TestAnthropic_DoesNotCallCoerceToolCall`) lock a NEGATIVE behavior — they're GREEN by construction at commit time because the production code never had a CoerceToolCall reference. The `tdd="true"` mode would have required a RED phase, but RED on a negative invariant is "force the bug to exist temporarily, then revert" — which is anti-pattern. Instead, both guards are committed in a single `test(06-04):` commit alongside the PHASE 6 INVARIANT doc block they reference. The behavioral test would have RED-failed if Anthropic was calling CoerceToolCall; the static-source test would have RED-failed if the symbol was present in handlers.go. Either failure mode is what makes them regression guards, even without a RED prelude.

## Authentication Gates

None encountered. All work was local code + test changes.

## Known Stubs

None. All code paths are wired end-to-end through the test matrix. No placeholder data flows to UI rendering; no "coming soon" / "TODO" text added in this plan. The Phase 3.1 `TODO(Phase 6)` block in `wireToChatRequest` (which this plan explicitly closes) has been removed.

## Per-Surface Contract Wording (iteration-3 HIGH #3) — Anthropic Exception Documented

The Phase 6 D-07 exception to the per-surface Message.ToolCalls population contract is now grep-able in three places for future readers:

1. **`internal/adapter/anthropic/collect.go`** (file-level doc comment) — full contract paragraph with the per-surface enumeration, the Anthropic exception rationale, and the Option A1 / Option B trade-off.
2. **`internal/adapter/anthropic/sse.go`** (applyChunk ChunkKindToolCall case) — short note citing D-07 and the engine.Collect divergence.
3. **`internal/adapter/anthropic/handlers.go`** (PHASE 6 INVARIANT doc block) — handler-level note citing D-01 + D-17 scenario 5 + the per-surface contract + both regression tests.

The 06-01 contract block in `internal/engine/collect.go` is already in place from Wave 1; Wave 2 plans 06-02 / 06-03 / 06-04 each cross-reference it from their adapter-specific contexts.

## Wave 2 Readiness Confirmation

The plan's success criteria are met:

- [x] TOOL-01 Anthropic side: non-streaming `/v1/messages` returns `content[]` with `tool_use` block carrying object input AND `stop_reason:"tool_use"` (REVIEW MEDIUM #4). Reachable via `CollectAnthropicChat` (D-07 Anthropic exception, Option A1 per WARNING #2). Streaming `/v1/messages` emits the SDK-expected `content_block_start(tool_use, input:{})` -> `content_block_delta(input_json_delta)` -> `content_block_stop` sequence with `stop_reason:"tool_use"` on message_delta.
- [x] TOOL-02 Anthropic NO-coerce verification: behavioral test (REVIEW LOW #9 primary) + static-source belt-and-suspenders test both green; bare JSON in assistant text preserved verbatim, no tool_use synthesized, stop_reason stays end_turn.
- [x] TOOL-03 Anthropic side: `TODO(Phase 6)` block inside `wireToChatRequest` closed; `tools[].input_schema` decodes to `canonical.ToolSpec.Parameters`; `tool_choice` polymorphic decode populates `canonical.ToolChoice` (auto / any-preserved-verbatim per REVIEW MEDIUM / named-tool / unknown-graceful).
- [x] iteration-3 MEDIUM #5: FIVE parity tests prove `CollectAnthropicChat` matches `engine.Collect` for text-only, thinking-only, mixed text+thinking, stop-reason propagation, and error propagation.
- [x] CR-01 pointer-to-empty-map preserved on streaming side (Pitfall 1).
- [x] Block-index discipline preserved (Pitfall 7 — exactly one bump per kind transition).
- [x] Race detector + goleak gate clean across the module.
- [x] Zero new dependencies.

Plan 06-05 (E2E across all three surfaces) is now unblocked for the Anthropic dimension.

## Self-Check: PASSED

Verified all files mentioned in this SUMMARY exist on disk and all commits are reachable from HEAD.

Files:
- `internal/adapter/anthropic/collect.go` — FOUND (NEW, 164 lines)
- `internal/adapter/anthropic/collect_test.go` — FOUND (NEW, 332 lines)
- `internal/adapter/anthropic/wire.go` — FOUND (modified)
- `internal/adapter/anthropic/wire_test.go` — FOUND (modified)
- `internal/adapter/anthropic/sse.go` — FOUND (modified)
- `internal/adapter/anthropic/sse_test.go` — FOUND (modified)
- `internal/adapter/anthropic/sse_golden_test.go` — FOUND (modified)
- `internal/adapter/anthropic/render.go` — FOUND (modified)
- `internal/adapter/anthropic/render_test.go` — FOUND (modified)
- `internal/adapter/anthropic/handlers.go` — FOUND (modified)
- `internal/adapter/anthropic/handlers_test.go` — FOUND (modified)

Commits (all FOUND in `git log`):
- `dd189c4` — test(06-04): add failing tests for Anthropic tools/tool_choice decode
- `2c5ed3e` — feat(06-04): close TODO(Phase 6) — Anthropic tools/tool_choice decode
- `7f6c912` — test(06-04): add failing tests for Anthropic ChunkKindToolCall path
- `d640236` — feat(06-04): Anthropic ChunkKindToolCall native tool_use rendering
- `77a5fec` — test(06-04): add failing test for non-streaming stop_reason:tool_use
- `a6c9b02` — feat(06-04): non-streaming stop_reason:tool_use override (REVIEW MEDIUM #4)
- `d315a68` — test(06-04): lock Anthropic NO-coerce asymmetry (D-01 + D-17 scenario 5)
