---
phase: 06-tool-call-path
plan: 02
subsystem: adapter-ollama
tags: [tool-call, ollama, render, ndjson, coerce, streaming-coerce, sawKiroNativeToolCall, two-path-rule, wire-shape-canary]

# Dependency graph
requires:
  - phase: 06-tool-call-path/01
    provides: "canonical.ToolCallChunk.ID (D-08); internal/acp/translate.go promotes tool_call notifications to real ChunkKindToolCall; internal/engine/collect.go aggregates kiro-native tool_calls as [tool: <name>]\\n narration for non-streaming Ollama/OpenAI; internal/engine/coerce.go implements the locked D-09 9-step algorithm; engine.buildBlocks emits the JSON tool catalog inside [Available tools]"
  - phase: 02-ollama-end-to-end
    provides: "ollamaToolSpec → canonical.ToolSpec forward-seam decoder (wire.go:321-330); ollamaMessage.ToolCalls field on the request side (wire.go:40); ollamaToolCall/ollamaToolCallFunction wire types"
  - phase: 04-streaming
    provides: "runNDJSONEmitter / emitNDJSONChunk / finalizeNDJSON skeleton; D-05 single-goroutine invariant for w + flusher + emitter state; D-06 watchdog teardown discipline; Pitfall 2 Flusher-assert-before-write order"

provides:
  - "ollamaChatResponseMessage.ToolCalls — new field, []ollamaToolCall with omitempty (Phase 6 D-04/D-15 wire-type gap closed; symmetric with the request-side ollamaMessage.ToolCalls)"
  - "internal/adapter/ollama/render.go::chatResponseToWire populates Message.ToolCalls from resp.Message.ToolCalls (verbatim — Arguments stays map[string]any, NO json.Marshal). Per-surface contract comment documents that this field is populated ONLY by engine.CoerceToolCall on the Ollama surface"
  - "internal/adapter/ollama/handlers.go invokes engine.CoerceToolCall on the non-streaming branch between engine.Collect and chatResponseToWire (D-01 hook-in) with REVIEW LOW #7 defensive length-guard"
  - "internal/adapter/ollama/handlers.go threads *canonical.ChatRequest through to runNDJSONEmitter so the streaming branch can run end-of-stream coerce with req.Tools available (REVIEW HIGH #1 + iteration-3 sawKiroNativeToolCall)"
  - "internal/adapter/ollama/ndjson.go: new emitterState (textBuffer, buffering, deferredTextLines, sawKiroNativeToolCall) carries the per-stream accumulators; new ChunkKindToolCall case emits `[tool: <name>]\\n` narration AND sets sawKiroNativeToolCall=true; emitTextChunk buffers JSON-shaped text deltas when req.Tools is non-empty; finalizeNDJSON implements the iteration-3 skip-or-coerce-or-flush logic"
  - "Five new ndjson tests + one defensive nil-name fallback test lock the streaming behavior end-to-end (StreamingCoerce_BareJSON, StreamingCoerce_NotJSON_PassThrough, KiroNative_ThoughtTextOnly, Stream_NativeToolCall_ThenJSONText_NoCoerce, Stream_NativeToolCall_Only_NoCoerce, KiroNative_DefensiveNilName)"
  - "Two new handlers tests lock the non-streaming D-01 hook-in (TestHandleChat_NonStreaming_CoerceFires + TestHandleChat_NonStreaming_KiroNativeNarration_NoCoerce) plus one routing test (TestHandlers_DefaultStreamOmitted_GoesToStreaming) with three sub-cases (omitted/true/false stream field)"
  - "Two new render tests lock the wire-shape divergence canary (TestChatResponseToWire_ToolCalls with four subtests including the iteration-3 KiroNativeNarration_NoToolCalls case) + the Phase 2 forward-seam decoder regression test (TestWireToChatRequest_Tools_Phase2Regression)"

affects:
  - 06-03-openai-tool-call (parallel slice — same canonical→render pattern, but Arguments marshaled to JSON-string per OpenAI spec)
  - 06-04-anthropic-tool-call (parallel slice — D-07 exception: adapter-local Collect populates Message.ToolCalls from kiro-native chunks)
  - 06-05-tool-call-e2e (e2e harness will exercise this slice's three load-bearing paths: non-streaming coerce, streaming coerce, streaming kiro-native)
  - phase-08-hooks (Phase 8 hook seam will observe both kiro-native ChunkKindToolCall AND coerce-synthesized Message.ToolCalls — Phase 6 documents the per-surface contract those hooks must respect)

# Tech tracking
tech-stack:
  added: []  # zero new external dependencies
  patterns:
    - "Per-surface Message.ToolCalls population contract (Phase 6 D-03/D-05/D-07) enforced at every code site: render.go has a doc-comment block, ndjson.go's ChunkKindToolCall case has an inline comment, handlers.go's CoerceToolCall site has its own inline comment"
    - "Streaming-coerce buffering pattern (REVIEW HIGH #1): per-chunk emitter state machine — `buffering` flag flips once on the first JSON-shaped text delta (`{` or triple-backtick prefix) and stays true for the rest of the stream so the wire output is consistent (never half-flush half-buffer). Decision point lives in the same goroutine that writes — D-05 single-goroutine invariant preserved"
    - "Iteration-3 sawKiroNativeToolCall flag (REVIEW HIGH #2 iteration-2 regression fix): once a kiro-native ChunkKindToolCall fires during a stream, end-of-stream coerce is suppressed regardless of any buffered JSON-shaped text. Prevents double-fire where a model emits both a kiro-native tool_call AND subsequent JSON text"
    - "Wire-shape divergence canary (Ollama plain-object Arguments vs OpenAI JSON-string Arguments): tests assert `\"arguments\":{\"location\":\"NYC\"}` as a positive byte-level match AND `\"arguments\":\"` as a NEGATIVE match to catch any future regression toward the OpenAI shape"
    - "Defensive length-guard pattern (REVIEW LOW #7): wherever a code path touches `resp.Message.ToolCalls[0]` after a `CoerceToolCall == true` return, the `len(...) > 0` check makes the code robust to future algorithm changes (and is mandatory for any nil-safe code path)"
    - "Engine import in production source (handlers.go + ndjson.go): the Engine interface in adapter.go preserves the TRST-04 boundary for the canonical orchestration surface, but engine.CoerceToolCall is a free function explicitly designed to be called from per-surface adapters. The handlers/ndjson imports of internal/engine are scoped to that one entry point"

key-files:
  created:
    - internal/adapter/ollama/render_test.go (249 lines)
  modified:
    - internal/adapter/ollama/wire.go (+15 lines: ToolCalls field on ollamaChatResponseMessage + per-surface contract doc)
    - internal/adapter/ollama/render.go (+30 lines: chatResponseToWire ToolCalls populate loop + per-surface contract block comment)
    - internal/adapter/ollama/handlers.go (+18 lines: engine import; engine.CoerceToolCall call site with defensive length-guard; comment block on the streaming branch citing REVIEW HIGH #1 + iteration-3; req threaded through runNDJSONEmitter on both chat and generate streaming branches)
    - internal/adapter/ollama/ndjson.go (~260 net new lines: emitterState struct; emitNDJSONChunk extended with ChunkKindToolCall + threading of state/req; new emitTextChunk helper with the buffering branch; new marshalAndWrite + releaseBufferedLines helpers; finalizeNDJSON extended with iteration-3 skip-or-coerce-or-flush logic; engine import; strings import)
    - internal/adapter/ollama/ndjson_test.go (+307 lines: 6 new tests covering all Phase 6 D-03/D-05/HIGH #1/HIGH #2 iteration-3 paths through the streaming emitter; 6 existing call-site signature updates for the new req parameter)
    - internal/adapter/ollama/handlers_test.go (+154 lines: 3 new tests covering non-streaming coerce hit, non-streaming kiro-native narration pass-through, and default-omitted-stream routing)

key-decisions:
  - "REVIEW LOW #7 defensive length-guard applied at both coerce sites (handlers.go non-streaming AND ndjson.go streaming) — `len(resp.Message.ToolCalls) > 0` before `[0].Name` access. Even though `CoerceToolCall == true` implies the slice is non-empty by Step 8 of the locked D-09 algorithm, the explicit guard keeps the debug-log site safe against future algorithm refactors and matches the established defensive discipline from translate.go's firstNonEmpty fallback."
  - "Streaming-coerce buffering is implemented with a `buffering bool` latch rather than per-chunk re-evaluation. Once buffering starts, ALL subsequent text chunks within that stream buffer — even if a later chunk happens to be non-JSON-shaped. This keeps the wire output consistent (never half-flush-half-buffer) and matches the intuition that a stream that LOOKS like it might coerce should commit to that path until proven wrong at stream close."
  - "Iteration-3 sawKiroNativeToolCall SKIPS coerce entirely rather than running it and discarding the result. This is a strict tightening of the iteration-2 semantics — even if buffered JSON-shaped text happens to map onto a tool's properties, we honor the two-path rule's promise that kiro-native is the SOLE source of intent when it fires during a stream. The buffered text is FLUSHED as plain text lines (not discarded) because the user-visible behavior should preserve any incidental JSON-shaped content that wasn't a coerce target."
  - "/api/generate path passes req through to runNDJSONEmitter even though generate has no tools[]. This keeps the function signature uniform between the chat and generate streaming branches. The buffering heuristic naturally short-circuits when `req == nil || len(req.Tools) == 0`, so generate is a no-op for the streaming-coerce machinery in practice."
  - "ChunkKindToolCall + isChat=false drops silently in emitNDJSONChunk. /api/generate has no content-block / tool_calls envelope (the wire-shape is `{response: string}` only) so kiro-native tool_calls cannot meaningfully surface there. This is parity with the existing ChunkKindThought + isChat=false drop and the D-04 spec."
  - "TestNDJSON_KiroNative_DefensiveNilName added as a defensive bonus (not in the plan's task list but a direct application of the same nil-name discipline 06-01 Task 1 established). Locks the [tool: unknown] fallback at the Ollama emitter layer."

patterns-established:
  - "Pattern: per-stream emitterState struct (`textBuffer strings.Builder`, `buffering bool`, `deferredTextLines [][]byte`, `sawKiroNativeToolCall bool`) lives on the select-loop goroutine stack. No allocation per chunk for the bookkeeping itself — strings.Builder grows amortized. Mutex-free by construction; D-05 single-goroutine invariant is the gate."
  - "Pattern: streaming-coerce decision heuristic — `strings.HasPrefix(strings.TrimSpace(buf+chunk), \"{\") || strings.HasPrefix(strings.TrimSpace(buf+chunk), \"```\")`. Same heuristic CoerceToolCall's stripFences will recognize on the buffered text at stream end. Mirrors the algorithm: if it looks parseable, give coerce a chance; otherwise stream-through."
  - "Pattern: skip-or-coerce-or-flush three-way branch at stream close. The three-way distinction (skip vs coerce-hit vs coerce-miss) is the iteration-3 contract — the iteration-2 form collapsed skip and coerce-hit into the same path, which is what produced the HIGH #2 regression. Document the three branches in plain-English comments so future maintainers don't re-collapse them."
  - "Pattern: wire-shape canary positive + negative byte-level assertion. Every test that touches the tool_calls wire-shape divergence axis (Ollama plain-object vs OpenAI JSON-string) makes BOTH the positive match (`\"arguments\":{...}`) AND the negative match (no `\"arguments\":\"`). Reduces the risk of a regression accidentally satisfying the positive check while drifting into the OpenAI form."

requirements-completed: [TOOL-01, TOOL-02, TOOL-03]

# Metrics
duration: 50min
completed: 2026-05-27
---

# Phase 6 Plan 02: Ollama Tool-Call Vertical Slice Summary

**Closed the Ollama side of TOOL-01/02/03 end-to-end: non-streaming AND streaming `/api/chat` with `tools[]` now return `message.tool_calls[]` with plain-object `arguments` (Phase 6 D-04 wire-shape divergence canary vs OpenAI) when `engine.CoerceToolCall` fires; kiro-native `ChunkKindToolCall` chunks render as `[tool: <name>]\n` narration in `message.content` and do NOT contribute to the done line's `tool_calls` (REVIEW HIGH #2 two-path rule); iteration-3 `sawKiroNativeToolCall` flag prevents end-of-stream coerce double-fire after a kiro-native chunk during the stream.**

## Tasks Completed

| # | Task | Commit | Files |
|---|------|--------|-------|
| 1 RED   | Add failing tests for ToolCalls wire-type + render pass-through + Phase 2 decoder regression | `6014c5f` | render_test.go (NEW) |
| 1 GREEN | ollamaChatResponseMessage.ToolCalls field + chatResponseToWire populate loop + per-surface contract doc | `fd94150` | wire.go, render.go |
| 2 RED   | Add failing tests for non-streaming coerce hit + kiro-native narration + default-omitted stream routing | `7920a4b` | handlers_test.go |
| 2 GREEN | engine.CoerceToolCall hook-in on non-streaming branch with defensive length-guard; *canonical.ChatRequest threaded through runNDJSONEmitter | `1a4385e` | handlers.go, ndjson.go, ndjson_test.go |
| 3 RED   | Add failing tests for streaming coerce hit/miss + kiro-native narration + iteration-3 sawKiroNativeToolCall skip-or-coerce-or-flush + nil-name defensive | `eee6b75` | ndjson_test.go |
| 3 GREEN | emitterState + ChunkKindToolCall narration emit + emitTextChunk buffering + finalizeNDJSON iteration-3 skip-or-coerce-or-flush + helpers | `c73976d` | ndjson.go |

All six commits land on the `worktree-agent-ad95696427c440f68` branch in order with passing tests at each GREEN gate. The Plan 06-02 frontmatter's required gates are present (test → feat alternation per RED/GREEN).

## What Was Built

### 1. ollamaChatResponseMessage.ToolCalls (wire-type gap from Phase 2)

`ollamaChatResponseMessage` previously carried only `Role` + `Content` + `Thinking`. The request-side `ollamaMessage` already had `ToolCalls []ollamaToolCall` for assistant-turn echoes, but the response-side struct was missing the symmetric field. Added with `omitempty` so pre-Phase-6 text-only responses serialize identically (backward compatibility verified by the nil-ToolCalls subtest of `TestChatResponseToWire_ToolCalls`).

The doc-comment block on the new field captures the per-surface contract: "populated ONLY by engine.CoerceToolCall (the coerce-from-text path) for the Ollama surface. Kiro-native tool_call chunks render as `[tool: <name>]\n` narration text in Content."

### 2. render.go::chatResponseToWire ToolCalls populate loop

Extends the existing `out := &ollamaChatResponse{...}` assembly with a per-canonical-ToolCall loop that builds `[]ollamaToolCall` with `Function: ollamaToolCallFunction{Name: tc.Name, Arguments: tc.Arguments}`. The canonical `Arguments map[string]any` is assigned verbatim to the wire `Arguments map[string]any` field — **no `json.Marshal` to a string anywhere on this path**. The OpenAI surface (Slice 3) is the one that wraps `arguments` in a JSON-encoded string per spec; that lives under `internal/adapter/openai/`, never here. This is the load-bearing distinction the byte-level wire-shape canary tests guard.

The doc-comment block above the loop spells out the per-surface contract for grep-ability and future maintainers.

### 3. handlers.go non-streaming D-01 hook-in

Inserted between `eng.Collect(...)` and `writeJSON(w, chatResponseToWire(...))`:

```go
if engine.CoerceToolCall(req, resp) {
    var firstName string
    if len(resp.Message.ToolCalls) > 0 {
        firstName = resp.Message.ToolCalls[0].Name
    }
    a.cfg.Logger.Debug("ollama: coerce fired", "tool", firstName)
}
```

The `len(...) > 0` guard is the REVIEW LOW #7 defensive pattern — even though `CoerceToolCall == true` implies the slice was just populated by Step 8 of the locked D-09 algorithm, the explicit check makes the debug-log site robust to future refactors.

`resp` is passed directly (pointer) per Pitfall 6 — the mutation must propagate out to the subsequent `chatResponseToWire(resp, ...)` call. Pre-copying (`respCopy := *resp`) would discard the `Message.ToolCalls` slice append because slice append on the copy would not be visible at the original pointer.

### 4. handlers.go streaming branch — req threaded through

The streaming branch does NOT call `engine.CoerceToolCall` inline. Instead, the canonical `*canonical.ChatRequest` is passed into `runNDJSONEmitter` so the emitter can call it at stream close on the buffered text. Block comment at the call site cites REVIEW HIGH #1 + iteration-3 rationale.

`/api/generate` also threads `req` through for signature uniformity even though it has no `tools[]` — the buffering heuristic short-circuits on empty `req.Tools` so it's a no-op for generate in practice.

### 5. ndjson.go — emitterState + ChunkKindToolCall + buffering + finalizeNDJSON

The most substantive change in this plan. Net structure:

- **emitterState struct** (new): `textBuffer strings.Builder`, `buffering bool`, `deferredTextLines [][]byte`, `sawKiroNativeToolCall bool`. Lives on the select-loop goroutine stack. No mutex.
- **emitNDJSONChunk** (extended): adds `state *emitterState` and `req *canonical.ChatRequest` parameters. Dispatches the new `ChunkKindToolCall` case for kiro-native narration emit + sawKiroNativeToolCall flip.
- **emitTextChunk** (new helper extracted from the previous inline text case): implements the buffering branch. When `len(req.Tools) > 0` AND the accumulated trimmed text starts with `{` or triple-backtick fence, buffer (don't flush). Else stream-through (Phase 4 behavior).
- **finalizeNDJSON** (extended): adds `state *emitterState` and `req *canonical.ChatRequest` parameters. Implements the iteration-3 three-way branch at stream close:
  - **sawKiroNativeToolCall == true:** SKIP coerce. Release buffered text as plain NDJSON lines. Done:true with no tool_calls.
  - **buffering == false:** Emit done:true normally (Phase 4 behavior).
  - **buffering == true && sawKiroNativeToolCall == false:** Build synthetic `*canonical.ChatResponse` from `textBuffer.String()`, call `engine.CoerceToolCall(req, syntheticResp)`. On hit: DISCARD deferredTextLines, compose done:true via `chatResponseToWire(syntheticResp, ...)` so `Message.ToolCalls` populates. On miss: RELEASE deferredTextLines + emit done:true normally.
- **releaseBufferedLines** (new helper): shared flush logic for the skip and coerce-miss branches.
- **marshalAndWrite** (new helper): extracted the json.Marshal + fmt.Fprintf + flusher.Flush + cancelFn-on-error sequence so the per-chunk emit cases stay readable.

The single-goroutine invariant (D-05) is preserved by construction — all emitterState mutations happen inside the select-loop goroutine that calls emitNDJSONChunk → emitTextChunk. The watchdog goroutine (context.AfterFunc in engine.go) never touches it (Pitfall 8 still satisfied).

## Test Coverage

| Test | File | Type | Purpose |
|------|------|------|---------|
| `TestChatResponseToWire_ToolCalls/nil_no_tool_calls_field` | render_test.go | sub | omitempty drops the wire key when input is nil |
| `TestChatResponseToWire_ToolCalls/single_coerce_synthesized_plain_object_args` | render_test.go | sub | Wire-shape canary: positive `"arguments":{...}` + negative `"arguments":"`; round-trip into map[string]any |
| `TestChatResponseToWire_ToolCalls/multi_tool_order_preserved` | render_test.go | sub | Order preservation across the populate loop |
| `TestChatResponseToWire_ToolCalls/KiroNativeNarration_NoToolCalls` | render_test.go | sub | iteration-3 — narration text passes through without re-synthesizing tool_calls |
| `TestWireToChatRequest_Tools_Phase2Regression` | render_test.go | functional | Phase 2 forward-seam decoder regression-locked + nil-Function defensive skip |
| `TestHandlers_DefaultStreamOmitted_GoesToStreaming/omitted_defaults_to_streaming` | handlers_test.go | sub | REVIEW HIGH #1 — Ollama defaults to streaming when stream field absent |
| `TestHandlers_DefaultStreamOmitted_GoesToStreaming/explicit_true_streams` | handlers_test.go | sub | Explicit stream:true routes to NDJSON |
| `TestHandlers_DefaultStreamOmitted_GoesToStreaming/explicit_false_non_streaming` | handlers_test.go | sub | Explicit stream:false routes to JSON |
| `TestHandleChat_NonStreaming_CoerceFires` | handlers_test.go | functional | D-01 hook-in produces plain-object arguments on non-streaming |
| `TestHandleChat_NonStreaming_KiroNativeNarration_NoCoerce` | handlers_test.go | functional | iteration-3 — narration text passes through, no tool_calls fabricated |
| `TestNDJSON_StreamingCoerce_BareJSON` | ndjson_test.go | functional | REVIEW HIGH #1 — JSON-shaped text deltas buffer, coerce fires at stream end, done line carries plain-object arguments, buffered deltas discarded |
| `TestNDJSON_StreamingCoerce_NotJSON_PassThrough` | ndjson_test.go | functional | Non-JSON text never buffers (Phase 4 preserved) |
| `TestNDJSON_KiroNative_ThoughtTextOnly` | ndjson_test.go | functional | REVIEW HIGH #2 two-path rule — kiro-native renders as narration only, done line carries no tool_calls |
| `TestStream_NativeToolCall_ThenJSONText_NoCoerce` | ndjson_test.go | functional | iteration-3 — sawKiroNativeToolCall suppresses coerce; buffered text flushed plain |
| `TestStream_NativeToolCall_Only_NoCoerce` | ndjson_test.go | functional | iteration-3 minimal case — narration + done, no buffering, no tool_calls |
| `TestNDJSON_KiroNative_DefensiveNilName` | ndjson_test.go | defensive | Nil ToolCall → [tool: unknown] fallback (06-01 discipline parity) |

All tests pass under `go test ./internal/adapter/ollama/... -race -count=1` AND under the full module test sweep `go test ./... -race -count=1`. `go vet ./internal/adapter/ollama/...` is clean.

## Per-Surface Contract Wording (HIGH #3 iteration-3 — Ollama side)

This plan extends the Phase 6 per-surface contract documentation to the Ollama-specific code sites for grep-ability:

- **`internal/adapter/ollama/wire.go`** (new ToolCalls field doc-comment): "this field is populated ONLY by engine.CoerceToolCall (the coerce-from-text path) for the Ollama surface."
- **`internal/adapter/ollama/render.go`** (new doc-comment block above the populate loop): cites D-03/D-05/D-07 explicitly and notes the Phase 6 wire-shape divergence canary (plain-object vs JSON-string arguments).
- **`internal/adapter/ollama/handlers.go`** (inline comment at the CoerceToolCall site): "Phase 6 D-01: invoke CoerceToolCall on the non-streaming path AFTER Collect, BEFORE render. The function mutates resp in place (Pitfall 6: pass the pointer directly)."
- **`internal/adapter/ollama/handlers.go`** (streaming-branch block comment): "Coerce for the streaming path lives in ndjson.go, NOT here. The streaming emitter buffers JSON-shaped assistant text, tracks whether any kiro-native ChunkKindToolCall fired during the stream, and at stream end either skips coerce (sawKiroNativeToolCall == true) or runs coerce on the buffered text."
- **`internal/adapter/ollama/ndjson.go`** (ChunkKindToolCall case inline comment): "REVIEW HIGH #2 + iteration-3 fix to HIGH #2: kiro-native tool_call emits a `[tool: <name>]\n` thought-text narration line and sets sawKiroNativeToolCall=true on the emitter state (suppresses the end-of-stream coerce). Does NOT accumulate into any tool_calls slice — the two-path rule isolates kiro-native (narration only) from coerce-synthesized (done line only)."
- **`internal/adapter/ollama/ndjson.go`** (finalizeNDJSON doc): the three-way branch (skip / no-buffer / coerce) documented as 3.a/3.b/3.c so future maintainers don't accidentally collapse them.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking issue] Added `nil` argument for the new `req *canonical.ChatRequest` parameter at all six existing `runNDJSONEmitter` call sites in ndjson_test.go.**

- **Found during:** Task 2 GREEN — adding the `req *canonical.ChatRequest` parameter to the `runNDJSONEmitter` signature in ndjson.go broke compilation of ndjson_test.go.
- **Issue:** The plan called out the signature change in ndjson.go but did not explicitly list the test-file callers that would need updating. Pre-existing tests (TestNDJSON_Chat_TextChunks, TestNDJSON_Chat_ThoughtChunk, TestNDJSON_Generate_ThoughtDropped, TestNDJSON_FlusherAssertionFails, TestNDJSON_WriteError_CancelsCtx, TestNDJSON_StreamResultError) all call runNDJSONEmitter directly.
- **Fix:** Replaced `nilLogger())` with `nilLogger(), nil)` across all six call sites via a single replace_all Edit. The nil value is correct for these pre-Phase-6 tests because they do not exercise the tool-call paths — their `req` would be irrelevant.
- **Files modified:** `internal/adapter/ollama/ndjson_test.go`
- **Commit:** `1a4385e` (folded into Task 2 GREEN — same compilation unit).
- **Rule rationale:** Mechanical signature-update fan-out; no semantic change to the pre-existing tests; required for ndjson.go to compile after the signature change. Same compilation unit as the production source change.

**2. [Rule 3 - Blocking issue] Added `req *canonical.ChatRequest` to `runNDJSONEmitter` call in `/api/generate` streaming branch in handlers.go.**

- **Found during:** Task 2 GREEN — symmetrical to the chat-branch call site. The signature change is uniform across both chat and generate streaming paths.
- **Issue:** The plan specified the chat-branch call site change but did not call out the symmetrical change in the generate handler. Leaving the generate call site unmodified would have broken compilation.
- **Fix:** Generate handler also passes `req` to `runNDJSONEmitter`. The buffering heuristic short-circuits when `req == nil || len(req.Tools) == 0` so the streaming-coerce machinery is a no-op for generate in practice. Added a comment explaining the signature uniformity.
- **Files modified:** `internal/adapter/ollama/handlers.go`
- **Commit:** `1a4385e` (folded into Task 2 GREEN).
- **Rule rationale:** Signature uniformity across handler call sites is the cleanest approach; the alternative (different signatures for chat vs generate) would have required ad-hoc branching at the emitter entry point or a wrapper function. The plan's instruction "Modify `internal/adapter/ollama/handlers.go` streaming branch" was specific to the chat path but the wider signature change naturally requires updating both call sites.

**3. [Rule 2 - Missing critical functionality] Added `TestNDJSON_KiroNative_DefensiveNilName` to lock the defensive nil-name fallback in the new ChunkKindToolCall emit path.**

- **Found during:** Task 3 GREEN — while implementing the `name := "unknown"` fallback in the new ChunkKindToolCall case, I noticed there was no test locking the behavior.
- **Issue:** The plan documents the defensive fallback in the behavior block ("with defensive `name := "unknown"` if `chunk.ToolCall == nil || chunk.ToolCall.Name == ""`") but did not list a test for it. Without a test, a future refactor could remove the guard and the nil-deref crash on `c.ToolCall.Name` access would only surface in production.
- **Fix:** Added a 13-line test that passes a `canonical.Chunk{Kind: ChunkKindToolCall, ToolCall: nil}` and asserts the output contains `[tool: unknown]`.
- **Files modified:** `internal/adapter/ollama/ndjson_test.go`
- **Commit:** `eee6b75` (folded into Task 3 RED).
- **Rule rationale:** Direct application of the 06-01 Task 1 discipline (firstNonEmpty fallback for empty titles) extended into the per-surface emitter. Locks correctness against future refactors. Zero plan-scope expansion — the fallback was already specified; the test just locks it.

### Decisions / Notes (not bugs)

**4. The plan section §verification line 4 expectation "returns exactly two matches" is satisfied at the production-source level.**

`grep -n "engine.CoerceToolCall" internal/adapter/ollama/handlers.go internal/adapter/ollama/ndjson.go` returns 4 lines total: 2 actual call sites (`handlers.go:90` non-streaming, `ndjson.go:407` streaming) + 2 doc-comment hits (`handlers.go:109` and `ndjson.go:352`). The "exactly two matches" criterion is satisfied if interpreted as "two actual call sites" (which is the load-bearing requirement). The doc-comment hits are part of the per-surface contract wording the plan explicitly requires (HIGH #3 iteration-3 grep-ability) — removing them to satisfy a literal "exactly 2" reading of the grep would defeat the documentation discipline.

No code change needed for this — flagging as a documentation interpretation note.

## Authentication Gates

None encountered. All work was local code + test changes.

## Known Stubs

None. All code paths are wired end-to-end through the test matrix. No placeholder data flows to UI rendering; no "coming soon" / "TODO" text added in this plan.

## Wave 2 Readiness Confirmation

The plan's success criteria are met:

- [x] TOOL-01 (Ollama side): both non-streaming AND streaming `/api/chat` with tools[] return `message.tool_calls[].function.arguments` as plain JSON object when coerce fires. Verified by `TestHandleChat_NonStreaming_CoerceFires` + `TestNDJSON_StreamingCoerce_BareJSON` byte-level canary assertions.
- [x] TOOL-02 (Ollama side): BOTH non-streaming AND streaming `/api/chat` with model-emitted JSON-as-text invokes `engine.CoerceToolCall` and returns a synthetic tool_calls entry (REVIEW HIGH #1 streaming-coerce gap closed).
- [x] TOOL-03 (Ollama side): Phase 2 forward-seam decoder regression-tested by `TestWireToChatRequest_Tools_Phase2Regression` including the nil-Function defensive skip.
- [x] REVIEW HIGH #2 two-path rule honored: kiro-native ChunkKindToolCall renders as `[tool: <name>]\n` thought-text NDJSON lines and contributes NOTHING to the done:true line's `tool_calls`. Locked by `TestNDJSON_KiroNative_ThoughtTextOnly`.
- [x] iteration-3 fix to HIGH #2 honored: `sawKiroNativeToolCall` flag prevents end-of-stream coerce from firing after a kiro-native tool_call passed through during the stream. Locked by `TestStream_NativeToolCall_ThenJSONText_NoCoerce` and `TestStream_NativeToolCall_Only_NoCoerce`.
- [x] Default-omitted `stream` field routes through the streaming path. Locked by `TestHandlers_DefaultStreamOmitted_GoesToStreaming` (three subtests).
- [x] Non-streaming kiro-native rendering inherits from 06-01 Task 2 narration aggregator. Verified by `TestHandleChat_NonStreaming_KiroNativeNarration_NoCoerce` end-to-end + the unit-level `TestChatResponseToWire_ToolCalls/KiroNativeNarration_NoToolCalls`.
- [x] Race detector clean: `go test ./internal/adapter/ollama/... -race -count=1` passes.
- [x] Zero new external dependencies.
- [x] Full module test sweep green under `go test ./... -race -count=1`.
- [x] `go vet ./internal/adapter/ollama/...` clean.

The Ollama slice for Phase 6 is complete. Slices 3 (OpenAI) and 4 (Anthropic) may proceed in parallel — no file-level overlap with this plan.

## Self-Check: PASSED

Verified all files mentioned in this SUMMARY exist on disk and all commits are reachable from HEAD:

- `internal/adapter/ollama/render_test.go` — FOUND (created in 6014c5f)
- `internal/adapter/ollama/wire.go` — FOUND (modified in fd94150)
- `internal/adapter/ollama/render.go` — FOUND (modified in fd94150)
- `internal/adapter/ollama/handlers.go` — FOUND (modified in 1a4385e)
- `internal/adapter/ollama/ndjson.go` — FOUND (modified in 1a4385e + c73976d)
- `internal/adapter/ollama/handlers_test.go` — FOUND (modified in 7920a4b)
- `internal/adapter/ollama/ndjson_test.go` — FOUND (modified in 1a4385e + eee6b75)

Commits (all FOUND in `git log`):

- `6014c5f` — test(06-02): add failing tests for Task 1
- `fd94150` — feat(06-02): Task 1 GREEN
- `7920a4b` — test(06-02): add failing tests for Task 2
- `1a4385e` — feat(06-02): Task 2 GREEN
- `eee6b75` — test(06-02): add failing tests for Task 3
- `c73976d` — feat(06-02): Task 3 GREEN
