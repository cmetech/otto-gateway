---
phase: 06-tool-call-path
plan: 03
subsystem: adapter_openai
tags: [tool-call, openai, sse, render, coerce, streaming-coerce]

# Dependency graph
requires:
  - phase: 06-01
    provides: "canonical.ToolCallChunk.ID (two-source); engine.CoerceToolCall (D-01/D-09/D-10/D-11); engine.Collect narration aggregator for kiro-native ChunkKindToolCall → [tool: <name>]\\n text; per-surface population contract"
  - phase: 03-openai-end-to-end
    provides: "OpenAI adapter skeleton — Engine/RunHandle/Stream interfaces (TRST-04 seam); chatCompletionRequest wire struct; chatResponseToCompletion render path; sseEmitter applyChunk + finalizeSSE; Phase 3 json.RawMessage Tools accept-and-ignore placeholder this plan replaces"
  - phase: 06-02
    provides: "Ollama Wave 2 cross-adapter analog — proven sawKiroNativeToolCall skip-or-coerce-or-flush triage pattern + per-entry json.RawMessage tools decoder pattern (this plan mirrors both for the OpenAI surface)"

provides:
  - "OpenAI POST /v1/chat/completions decodes `tools[]` into req.Tools via per-entry json.RawMessage unmarshal (iteration-3 MEDIUM #4 fix to D-13). Type-invalid sibling entries are skipped without dropping valid siblings."
  - "OpenAI POST /v1/chat/completions decodes `tool_choice` polymorphically into req.ToolChoice (auto/required/none/function), accept-and-ignore for unknown shapes."
  - "OpenAI non-streaming `/v1/chat/completions` invokes engine.CoerceToolCall after engine.Collect; LangChain-style JSON-as-text emissions are rescued into synthetic Message.ToolCalls entries."
  - "OpenAI non-streaming render emits `choices[0].message.tool_calls[].function.arguments` as a JSON-encoded STRING (wire-shape canary opposite of Ollama's object literal — Phase 6 D-07)."
  - "OpenAI non-streaming render overrides `choices[0].finish_reason` to `tool_calls` when Message.ToolCalls is non-empty."
  - "OpenAI streaming `/v1/chat/completions` renders kiro-native `ChunkKindToolCall` as text-delta narration `[tool: <name>]\\n` (REVIEW HIGH #2 two-path rule); NO native delta.tool_calls frames from this path."
  - "OpenAI streaming `/v1/chat/completions` end-of-stream coerce path: buffers JSON-shaped text deltas, calls engine.CoerceToolCall at stream close, emits multi-frame native delta.tool_calls + finish_reason:tool_calls SSE shape on hit (REVIEW HIGH #1 streaming-coerce gap fix)."
  - "OpenAI streaming sawKiroNativeToolCall flag suppresses end-of-stream coerce when a kiro-native tool_call fired during the stream (iteration-3 HIGH #2 fix — prevents iteration-2 double-fire regression)."

affects:
  - 06-04-anthropic-tool-call (independent — Anthropic uses its own D-07 native tool_use path)
  - 06-05-tool-call-e2e (the OpenAI vertical slice plumbing is now ready for end-to-end testing)

# Tech tracking
tech-stack:
  added: []  # zero new external dependencies
  patterns:
    - "Per-entry json.RawMessage tolerant decode (iteration-3 MEDIUM #4): outer field is []json.RawMessage; each entry unmarshaled independently into the typed wire struct; per-entry failures debug-logged and skipped; valid siblings preserved in declaration order."
    - "Polymorphic string-or-object decode for tool_choice: typed-object attempt first (richer shape), string fallback, accept-and-ignore for unknown shapes (mirrors decodeMessageContent string-or-array discipline)."
    - "sawKiroNativeToolCall skip-or-coerce-or-flush triage at stream close: kiro-native suppresses coerce; coerce hit discards deferred frames; coerce miss flushes deferred frames in order."
    - "Wire-shape divergence canary: OpenAI Arguments is JSON-STRING, Ollama Arguments is object literal — same canonical ToolCall.Arguments map[string]any serialized differently per surface."

key-files:
  created:
    - internal/adapter/openai/render_test.go (167 lines — TestChatResponseToCompletion_ToolCalls with 4 subcases)
  modified:
    - internal/adapter/openai/wire.go (Tools→[]json.RawMessage; openAIToolSpec / openAIToolSpecFunction / openAIToolChoiceObject wire types; per-entry unmarshal loop; decodeToolChoice polymorphic decoder; log/slog import)
    - internal/adapter/openai/wire_test.go (TestWireToChatRequest_Tools single/multi/absent; TestWireToChatRequest_Tools_MixedValidInvalid type-invalid+empty-name skipped; TestWireToChatRequest_ToolChoice auto/required/none/function/numeric-ignored/absent)
    - internal/adapter/openai/handlers.go (non-streaming CoerceToolCall hook with REVIEW LOW #7 defensive length-guard; streaming branch threads canonical *req to runSSEEmitter; internal/engine import)
    - internal/adapter/openai/render.go (responseMessage.ToolCalls field; openAIToolCall + openAIToolCallFunction wire types — Arguments is JSON-STRING; per-surface contract doc comment; finish_reason post-fixup; encoding/json import)
    - internal/adapter/openai/sse.go (full rewrite — applyChunk dispatch on Kind; applyTextChunk buffering decision; applyToolCallChunk text-delta narration + sawKiroNativeToolCall=true; chunkDelta.ToolCalls field; chunkDeltaToolCall/Function wire types; finalizeSSE skip-or-coerce-or-flush triage; tryStreamingCoerce multi-frame emission; strings + engine imports)
    - internal/adapter/openai/sse_test.go (pass &canonical.ChatRequest{} to existing runSSEEmitter calls — signature change in handlers.go)
    - internal/adapter/openai/sse_golden_test.go (driveGoldenWithReq variant + 5 new tests: StreamingCoerce_BareJSON / KiroNative_TextDelta / ToolCallFirst_RoleEmitOnce / Stream_NativeToolCall_ThenJSONText_NoCoerce / Stream_NativeToolCall_Only_NoCoerce; strings import)
    - .go-arch-lint.yml (adapter_openai + adapter_ollama may now depend on engine — Phase 6 D-05 invokes the stateless engine.CoerceToolCall)

key-decisions:
  - "Tools wire field changed to []json.RawMessage (not []openAIToolSpec) per iteration-3 MEDIUM #4. Decoding the outer slice as []json.RawMessage succeeds as long as the JSON value is an array — regardless of what each entry contains. Per-entry json.Unmarshal into openAIToolSpec runs in the wireToChatRequest loop with skip-on-failure + debug log. This is the only way to tolerate type-invalid sibling entries (e.g. function:42) without failing the outer request decode."
  - "Inline byte-level substring assertions used for the 5 new SSE tests (rather than testdata-file goldens). The wire-shape canaries — `delta.tool_calls` absent for kiro-native, `finish_reason:tool_calls` absent for kiro-native, JSON-string arguments shape for coerce-synthesized — are concise and clear as substring checks. Full goldens would be fragile against the chatcmpl- id + created timestamp normalization machinery and provide little additional signal over the substring patterns."
  - "Inline string contains-checks instead of regex for the SSE assertions. Cheap, exact, no catastrophic-backtracking surface (Pitfall 3 carry-forward)."
  - "Streaming-coerce buffering heuristic mirrors the Ollama 06-02 implementation: start buffering when req.Tools is non-empty AND accumulated text starts with `{` or a triple-backtick fence. Once buffering is true, ALL subsequent text fragments are buffered (they belong to the same JSON candidate sequence)."
  - "writeRaw helper added for releasing pre-marshaled deferred text frames. The deferred frames are stored with the full `data: ...\\n\\n` envelope so the release path is byte-identical to a normal flush (no re-marshal, no race on the buffered text)."
  - ".go-arch-lint.yml updated to allow adapter_openai + adapter_ollama to depend on internal/engine. Phase 6 D-05 makes engine.CoerceToolCall a load-bearing per-surface call; both adapters now directly invoke it. The structural Engine interface in adapter.go remains the TRST-04 seam for Engine.Collect / Engine.Run. CoerceToolCall is a stateless package-level function — no engine state, no driver — so the direct import is acceptable per the Phase 6 per-surface contract."

patterns-established:
  - "Pattern: typed-tolerant decode (per-entry json.RawMessage). Outer field declared []json.RawMessage; each entry unmarshaled independently inside the canonical translator. Per-entry failure → debug log + skip; per-entry semantic-failure (empty name, nil function) → silent skip. Pattern proven in 06-02 Ollama wire.go and replicated here for OpenAI."
  - "Pattern: sawKiroNativeToolCall flag in per-surface streaming emitter. Set true on first kiro-native ChunkKindToolCall; at stream close it gates the coerce path (true → skip coerce; false → buffer-then-coerce). The flag is a single-goroutine boolean touched only inside the select-loop, so no synchronization overhead."
  - "Pattern: skip-or-coerce-or-flush triage. End-of-stream finalization branches on (sawKiroNativeToolCall, buffering). sawKiroNativeToolCall=true → flush deferred + terminal normal. !buffering → terminal normal. buffering=true → tryStreamingCoerce (HIT: discard deferred + multi-frame native + terminal tool_calls. MISS: flush deferred + terminal normal)."
  - "Pattern: writeRaw for deferred frames. The full `data: <json>\\n\\n` envelope is captured at buffering time, NOT re-marshaled at flush time. Keeps the deferred path byte-identical to the normal flush path."

requirements-completed: [TOOL-01, TOOL-02, TOOL-03]

# Metrics
duration: 38min
completed: 2026-05-27
---

# Phase 6 Plan 03: OpenAI Tool-Call Vertical Slice Summary

**OpenAI Wave 2 slice complete: per-entry tolerant `tools[]` decoder + polymorphic `tool_choice` + non-streaming CoerceToolCall hook with JSON-string arguments + streaming end-of-stream coerce with sawKiroNativeToolCall skip + five SSE wire-shape locks. Closes TOOL-01 / TOOL-02 / TOOL-03 on the OpenAI surface, honors REVIEW HIGH #1 (streaming-coerce gap), REVIEW HIGH #2 (two-path rule: kiro-native renders as text-delta narration, NOT delta.tool_calls), REVIEW LOW #8 (role-emit-once with tool-call-first), and the iteration-3 sawKiroNativeToolCall double-fire prevention. Wire-shape divergence canary locked OPPOSITE Ollama: OpenAI Arguments is JSON-STRING, Ollama Arguments is object.**

## Tasks Completed

| # | Task | Commit | Files |
|---|------|--------|-------|
| 1 RED  | Add failing tests for tools[] + tool_choice decoder (iteration-3 per-entry tolerance) | `c83b3bd` | wire_test.go |
| 1 GREEN | Per-entry-tolerant OpenAI tools[] + polymorphic tool_choice decoder | `6ba0eee` | wire.go |
| 2 RED  | Add failing tests for non-streaming tool_calls render (JSON-string args + kiro-native narration) | `7366c8d` | render_test.go (NEW) |
| 2 GREEN | handlers.go coerce hook + render.go JSON-string tool_calls + per-surface contract | `be7e82d` | handlers.go, render.go, sse.go, sse_test.go, sse_golden_test.go, .go-arch-lint.yml |
| 3 RED  | Add failing tests for streaming-coerce + kiro-native-text-delta + sawKiroNativeToolCall | `854fce4` | sse_golden_test.go |
| 3 GREEN | Streaming coerce + kiro-native text-delta + sawKiroNativeToolCall + 5 wire-shape tests | `1fb9bfd` | sse.go |

## What Was Built

### Task 1: Per-entry-tolerant OpenAI tools[] + ToolChoice decoder (D-13 + iteration-3 MEDIUM #4)

**wire.go:**
- `chatCompletionRequest.Tools` changed from `json.RawMessage` (Phase 3 accept-and-ignore) to `[]json.RawMessage` (iteration-3 MEDIUM #4 fix — array of raw entries, decoded individually below).
- New wire types `openAIToolSpec` + `openAIToolSpecFunction` (byte-identical to Ollama's structural counterparts per the public OpenAI/Ollama agreed wire shape — the duplication is intentional per Phase 4 D-08 no-shared-driver).
- New wire types `openAIToolChoiceObject` + `openAIToolChoiceObjectFunction` for the polymorphic tool_choice object form.
- `wireToChatRequest` per-entry loop: each `json.RawMessage` is independently unmarshaled into `openAIToolSpec`. On per-entry unmarshal failure → `slog.Default().Debug(...)` + skip. On decoded-but-semantically-invalid entries (Function nil or Name empty) → silent skip. Valid siblings preserved in declaration order. THIS IS THE ITERATION-3 FIX: type-invalid siblings (e.g. `function:42` where the unmarshal target expects an object) no longer fail the whole request decode.
- New `decodeToolChoice` polymorphic decoder: typed-object attempt first → string fallback (`auto`/`required`/`none`) → accept-and-ignore for unknown shapes (numeric, etc.).
- `log/slog` import added.

### Task 2: Non-streaming CoerceToolCall hook + JSON-string tool_calls render (D-01 + D-07)

**handlers.go:**
- Non-streaming branch: between `eng.Collect` and `writeJSON(chatResponseToCompletion(...))`, invoke `engine.CoerceToolCall(req, resp)`. The function mutates `resp` in place (Pitfall 6 — pass pointer directly, no pre-copy). REVIEW LOW #7 defensive length-guard on the `resp.Message.ToolCalls[0].Name` debug-log read.
- Streaming branch: threads canonical `req` pointer down to `runSSEEmitter` (so end-of-stream coerce in sse.go can read `req.Tools`). Comment block notes the REVIEW HIGH #1 streaming-coerce + iteration-3 sawKiroNativeToolCall rationale.
- `internal/engine` import added (matches Ollama's 06-02 pattern).

**render.go:**
- `responseMessage.ToolCalls []openAIToolCall` field added (omitempty).
- New wire types `openAIToolCall` + `openAIToolCallFunction` — `Arguments string` (NOT `map[string]any`) is the wire-shape divergence canary opposite of Ollama's object literal.
- `chatResponseToCompletion` populates `out.Choices[0].Message.ToolCalls` from `resp.Message.ToolCalls` via `json.Marshal(tc.Arguments)`; on marshal error falls back to `"{}"` defensively.
- Finish-reason post-fixup: when `len(toolCalls) > 0`, override `out.Choices[0].FinishReason = "tool_calls"`.
- Per-surface contract doc comment block at the ToolCalls population site documents Phase 6 D-03/D-05/D-07.

### Task 3: SSE streaming-coerce + kiro-native text-delta + sawKiroNativeToolCall (REVIEW HIGH #1 + HIGH #2 + LOW #8 + iteration-3 HIGH #2)

**sse.go (rewrite):**

Wire types added:
- `chunkDelta.ToolCalls []chunkDeltaToolCall` (omitempty) — populated ONLY by the coerce-synthesized end-of-stream emission.
- `chunkDeltaToolCall { Index, ID, Type, Function }` + `chunkDeltaToolCallFunction { Name, Arguments string }` — Arguments is JSON-STRING (same canary as the non-streaming render).

Emitter state (single-goroutine invariant):
- `req *canonical.ChatRequest` — for end-of-stream coerce.
- `textBuffer strings.Builder` — accumulates JSON-candidate text deltas.
- `buffering bool` — true once we start a JSON-candidate sequence.
- `deferredTextFrames [][]byte` — would-be plain text-delta SSE frames, stored fully marshaled.
- `sawKiroNativeToolCall bool` — set true on first ChunkKindToolCall; gates the end-of-stream coerce path.

`applyChunk` dispatches on `c.Kind`:
- **ChunkKindText:** if `len(req.Tools) > 0` AND (already buffering OR `strings.TrimSpace(textBuffer + frag)` starts with `{` or `` ``` ``), append to textBuffer + marshal would-be frame + append to deferredTextFrames (NO flush). Otherwise flush per Phase 4.
- **ChunkKindToolCall:** ensure role-sent gate (REVIEW LOW #8 — fires even when this is the first chunk), emit single text-delta frame with `"[tool: <name>]\n"` narration content (REVIEW HIGH #2 two-path rule — NOT a delta.tool_calls frame), set `sawKiroNativeToolCall = true` (iteration-3 HIGH #2 fix).
- **Other kinds:** drop silently with debug log.

`finalizeSSE` iteration-3 skip-or-coerce-or-flush triage:
- **sawKiroNativeToolCall=true:** SKIP coerce. Release deferredTextFrames in order via `writeRaw`. Emit terminal frame with finish_reason mapped from canonical StopReason (NOT `"tool_calls"`).
- **buffering=false:** No coerce candidate. Emit terminal frame from StopReason.
- **else (buffering=true AND sawKiroNativeToolCall=false):** `tryStreamingCoerce` builds synthetic `*canonical.ChatResponse` from textBuffer, calls `engine.CoerceToolCall(e.req, syntheticResp)` (Pitfall 6 — pointer direct). On HIT: discard deferredTextFrames; emit frame B (id+name, empty args), frame C (arguments JSON-STRING — single atom per Pitfall 2 no-split-on-escape-boundary), terminal `finish_reason:"tool_calls"`. On MISS: flush deferredTextFrames + terminal frame from StopReason.

`runSSEEmitter` signature now accepts `req *canonical.ChatRequest` so the streaming-coerce path can read `req.Tools` at stream close. Existing tests updated to pass `&canonical.ChatRequest{}` (empty Tools — coerce is a no-op).

### Cross-cutting: .go-arch-lint.yml update

Both `adapter_openai` and `adapter_ollama` now declare `engine` in their `mayDependOn` allow-list. Rationale: Phase 6 D-05 makes `engine.CoerceToolCall` a load-bearing per-surface invocation, and both adapters import `internal/engine` directly to call it. The structural `Engine` interface in the adapter package remains the TRST-04 seam for `Engine.Collect` / `Engine.Run`. `engine.CoerceToolCall` is a stateless package-level function — no engine state, no driver — so the direct import is acceptable per the Phase 6 per-surface contract. Inline comments document the rationale at both adapter dep entries.

## Test Coverage

| Test | Type | Purpose |
|------|------|---------|
| `TestWireToChatRequest_Tools/single_tool` | functional | Single-entry decode into req.Tools with Name/Description/Parameters populated |
| `TestWireToChatRequest_Tools/multi_tool_order_preserved` | functional | Multi-entry preserves declaration order |
| `TestWireToChatRequest_Tools/no_tools_field` | functional | Absent tools field → req.Tools nil (no-op) |
| `TestWireToChatRequest_Tools_MixedValidInvalid` | functional | type-invalid entry (function:42) skipped via per-entry unmarshal failure; empty-name skipped via post-unmarshal validation; valid siblings (valid_a + valid_b) preserved (ITERATION-3 MEDIUM #4 LOCK) |
| `TestWireToChatRequest_ToolChoice/{auto,required,none,function,unknown_numeric_accepts_and_ignores,absent}` | table | Polymorphic decode coverage (string + object + accept-and-ignore) |
| `TestChatResponseToCompletion_ToolCalls/NilToolCalls_NoToolCallsKey` | functional | omitempty discipline — no tool_calls field on the wire when ToolCalls nil |
| `TestChatResponseToCompletion_ToolCalls/CoerceSynthesizedToolCalls_ArgumentsIsJSONString` | functional | Wire-shape canary: arguments is JSON-STRING (escape-quoted), finish_reason override "tool_calls" |
| `TestChatResponseToCompletion_ToolCalls/MultiToolOrderPreserved` | functional | Multi-tool order preserved |
| `TestChatResponseToCompletion_ToolCalls/KiroNativeNarration_NoToolCalls` | functional | Content carries `[tool: <name>]\n` narration; no tool_calls field; finish_reason NOT "tool_calls" (ITERATION-3 LOCK — depends on 06-01 Task 2 narration aggregator) |
| `TestSSE_Golden_StreamingCoerce_BareJSON` | wire-shape | REVIEW HIGH #1 — buffered JSON text → multi-frame native delta.tool_calls + finish_reason:tool_calls; buffered fragments do NOT leak |
| `TestSSE_Golden_KiroNative_TextDelta` | wire-shape | REVIEW HIGH #2 — kiro-native renders `[tool: <name>]\n` as text-delta; NO delta.tool_calls; finish_reason NOT "tool_calls" |
| `TestSSE_Golden_ToolCallFirst_RoleEmitOnce` | wire-shape | REVIEW LOW #8 — role frame emits exactly once even when tool_call is first chunk |
| `TestStream_NativeToolCall_ThenJSONText_NoCoerce` | wire-shape | ITERATION-3 HIGH #2 — kiro-native trips sawKiroNativeToolCall; buffered JSON text is FLUSHED as plain text-delta frames; finish_reason "stop" |
| `TestStream_NativeToolCall_Only_NoCoerce` | wire-shape | ITERATION-3 HIGH #2 — kiro-native only (no surrounding text); narration emitted; finish_reason "stop" |

All tests pass under `go test ./internal/adapter/openai/... -race -count=1`. Full module sweep (`go test ./... -race -count=1`) green.

## Wire-Shape Divergence Canary Cross-Confirm

The Phase 6 D-07 divergence axis is now locked in BOTH directions:

- **OpenAI (this plan):** `TestChatResponseToCompletion_ToolCalls/CoerceSynthesizedToolCalls_ArgumentsIsJSONString` asserts the wire bytes contain `"arguments":"{\"location\":\"NYC\"}"` (escape-quoted JSON STRING). The streaming counterpart (`TestSSE_Golden_StreamingCoerce_BareJSON`) asserts the same shape inside frame C of the streaming multi-frame emission.
- **Ollama (06-02 Slice 2):** `TestChatResponseToWire_ToolCalls` asserts the wire bytes contain `"arguments":{"location":"NYC"}` (object literal).

These two assertions are intentionally OPPOSITE shapes, sourced from the SAME canonical `ToolCall.Arguments map[string]any` value via per-surface serialization discipline. The canary fails immediately if either surface drifts.

## Per-Surface Population Contract Honored

Per Phase 6 D-03/D-05/D-07 (and the per-surface contract documented in 06-01 SUMMARY):
- **Generic `engine.Collect`:** does NOT populate Message.ToolCalls from any chunk source.
- **OpenAI (this plan):** populates Message.ToolCalls ONLY via `engine.CoerceToolCall`. Non-streaming path invokes it in handlers.go between Collect and render. Streaming path invokes it in sse.go's `tryStreamingCoerce` at end-of-stream (when sawKiroNativeToolCall=false AND buffering=true).
- **Anthropic (D-07 exception):** populates via its adapter-local Collect (06-04 Option A1, NOT this plan).

The OpenAI non-streaming kiro-native scenario inherits correctly from 06-01 Task 2's narration aggregator: `engine.Collect` produces `Message.Content[textIdx].Text = "[tool: <name>]\n"`, `CoerceToolCall` then fails parse on the narration text and returns false, the narration flows through `chatResponseToCompletion` into `choices[0].message.content`. No `Message.ToolCalls` entry, no `finish_reason:tool_calls`. `TestChatResponseToCompletion_ToolCalls/KiroNativeNarration_NoToolCalls` locks this path.

## Two-Path Rule Enforcement (REVIEW HIGH #2)

Native `delta.tool_calls[]` frames and the terminal `finish_reason:"tool_calls"` fire ONLY from coerce-synthesized entries (the buffered-text end-of-stream path). Kiro-native `ChunkKindToolCall` renders ONLY as text-delta narration `[tool: <name>]\n`. The five new SSE tests are the locks:
- `TestSSE_Golden_StreamingCoerce_BareJSON` proves coerce-synthesized → delta.tool_calls + finish_reason:tool_calls.
- `TestSSE_Golden_KiroNative_TextDelta` proves kiro-native → text-delta narration, NO delta.tool_calls, NO finish_reason:tool_calls.
- `TestStream_NativeToolCall_ThenJSONText_NoCoerce` proves the iteration-3 fix: kiro-native trip suppresses subsequent coerce even when buffered text looks JSON-shaped.
- `TestStream_NativeToolCall_Only_NoCoerce` proves the same suppression with no surrounding text.
- `TestSSE_Golden_ToolCallFirst_RoleEmitOnce` proves the role-emit-once gate fires correctly when tool_call is the first chunk.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 — Blocking issue] Updated existing `internal/adapter/openai/sse_test.go` and existing `runSSEEmitter` call sites in `sse_golden_test.go` for the signature change.**

- **Found during:** Task 2 GREEN — the streaming branch threads `req` into `runSSEEmitter`, which changed the function signature. Two existing tests called the old 5-arg form and failed to compile.
- **Fix:** Pass `&canonical.ChatRequest{}` (empty Tools — coerce is a no-op) to both call sites. The existing TestSSEGolden / TestSSE_CtxCancel / TestSSE_HeadersSetBeforeBody behavior is unchanged. Added a `driveGoldenWithReq` variant for the new Phase 6 tests that need to attach `req.Tools`.
- **Files modified:** `internal/adapter/openai/sse_test.go`, `internal/adapter/openai/sse_golden_test.go`.
- **Commit:** `be7e82d` (folded into Task 2 GREEN — same conceptual unit).
- **Rule rationale:** Pure mechanical update for an interface change introduced by the plan. No design impact. Without the fix the build would not compile and all later tests would be blocked.

**2. [Rule 3 — Blocking issue] Updated `.go-arch-lint.yml` to allow `adapter_openai` and `adapter_ollama` to depend on `internal/engine`.**

- **Found during:** Task 2 GREEN — adding `import "otto-gateway/internal/engine"` to handlers.go would have violated the existing arch-lint rule (which restricted adapters to `canonical + session` only).
- **Issue:** Phase 6 D-05 makes `engine.CoerceToolCall` a load-bearing per-surface invocation. Both adapter_ollama (already shipping in 06-02 — the rule was already breached there but the binary wasn't installed locally to catch it) and adapter_openai now invoke it directly.
- **Fix:** Added `engine` to the `mayDependOn` allow-list for both `adapter_ollama` and `adapter_openai` in `.go-arch-lint.yml`, with inline comments documenting the Phase 6 D-05 rationale. `adapter_anthropic` is intentionally NOT updated — Anthropic uses its own native tool_use path and does not invoke CoerceToolCall.
- **Files modified:** `.go-arch-lint.yml`.
- **Commit:** `be7e82d` (folded into Task 2 GREEN — same conceptual unit).
- **Rule rationale:** The structural Engine interface in adapter.go remains the TRST-04 seam for stateful Engine.Collect / Engine.Run. CoerceToolCall is a stateless package-level function — no engine state, no driver — and the per-surface contract makes direct invocation the cleanest expression. Mirrors the pattern that 06-02 Ollama already shipped with (the yml had not yet been updated when Wave 2 began; this plan closes that gap for both adapters at once).

### Decisions / Notes (not bugs)

**3. Inline byte-level substring assertions used for the 5 new SSE tests instead of testdata-file goldens.**

The wire-shape canary assertions are conceptually substring checks: "does `delta.tool_calls` appear in this output?", "does `[tool: get_weather]\n` appear as a text-delta?", "does `finish_reason:tool_calls` appear?". Expressing them as substring tests is concise and clear. A full golden file would add the chatcmpl- id + created normalization machinery overhead for little additional signal. The existing `TestSSEGolden` (testdata/sse_text_only.golden) still pins the byte-exact non-tool-call SSE shape.

## Authentication Gates

None encountered. All work was local code + test changes.

## Known Stubs

None. All code paths are wired end-to-end:
- Tools and ToolChoice flow from wire → canonical → render (non-streaming) and emitter (streaming).
- CoerceToolCall hook is real (no placeholder) on both paths.
- Kiro-native narration flow is real (via the 06-01 Task 2 aggregator + this plan's sse.go applyToolCallChunk).
- All five new SSE wire-shape tests pass on the live emitter (not mocked).

## Threat Flags

No new threat surface introduced beyond what is already in `<threat_model>` (T-V5-01, T-06-06, T-06-07, T-06-15, T-V5-03, T-06-05, T-06-19, T-06-SC). All mitigations honored:
- T-06-15 (iteration-3 MEDIUM #4 fix): per-entry json.RawMessage loop with skip-on-failure + debug log. `TestWireToChatRequest_Tools_MixedValidInvalid` locks the behavior.
- T-06-19 (NEW iteration-3): `sawKiroNativeToolCall` flag suppresses end-of-stream coerce after a kiro-native tool_call. `TestStream_NativeToolCall_ThenJSONText_NoCoerce` and `TestStream_NativeToolCall_Only_NoCoerce` lock the behavior.
- T-V5-01: `engine.CoerceToolCall` called with resp/syntheticResp pointer-direct (Pitfall 6 honored in both handlers.go and sse.go).
- T-06-07: polymorphic tool_choice decode falls back to nil on unknown shapes (numeric, etc.).
- T-06-SC: zero new dependencies.

## Wave 2 Slot Completion

Plan 06-03 closes the OpenAI vertical slice for Phase 6. Phase 6 Wave 2 OpenAI side is unblocked for Plan 06-05 end-to-end testing. Slice 4 (Anthropic) runs independently and does not depend on this plan.

The plan's success criteria are met:
- [x] TOOL-01 (OpenAI side, per SC #1): non-streaming + streaming `/v1/chat/completions` returns `choices[].message.tool_calls[].function.arguments` as a JSON-encoded STRING when coerce fires.
- [x] TOOL-02 (OpenAI side, per SC #3): BOTH non-streaming AND streaming `/v1/chat/completions` invoke `engine.CoerceToolCall` (REVIEW HIGH #1 streaming gap closed).
- [x] TOOL-03 (OpenAI side, per SC #4 + D-13 + iteration-3 MEDIUM #4): OpenAI `tools[]` and `tool_choice` decode into canonical; mixed valid/invalid tools entries preserve valid siblings.
- [x] REVIEW HIGH #2 two-path rule honored: kiro-native ChunkKindToolCall renders as text-delta narration; no delta.tool_calls or finish_reason:tool_calls from the kiro-native path.
- [x] Iteration-3 fix to HIGH #2 honored: sawKiroNativeToolCall flag prevents end-of-stream coerce double-fire.
- [x] REVIEW LOW #8 honored: role frame emits exactly once even when tool_call is the first chunk.
- [x] Wire-shape divergence axis canary locked: OpenAI Arguments JSON-STRING vs Ollama Arguments object.
- [x] Non-streaming kiro-native render inherits from 06-01 Task 2 narration aggregator; KiroNativeNarration_NoToolCalls test locks the path.
- [x] Race detector + goleak gate clean (`go test -race -count=1` on full module).
- [x] Zero new dependencies.

## Self-Check: PASSED

Verified all files mentioned in this SUMMARY exist on disk and all commits are reachable from HEAD:

- `internal/adapter/openai/wire.go` — FOUND (modified)
- `internal/adapter/openai/wire_test.go` — FOUND (modified)
- `internal/adapter/openai/handlers.go` — FOUND (modified)
- `internal/adapter/openai/render.go` — FOUND (modified)
- `internal/adapter/openai/render_test.go` — FOUND (created)
- `internal/adapter/openai/sse.go` — FOUND (rewritten)
- `internal/adapter/openai/sse_test.go` — FOUND (modified — signature update)
- `internal/adapter/openai/sse_golden_test.go` — FOUND (modified — 5 new tests + driveGoldenWithReq helper)
- `.go-arch-lint.yml` — FOUND (modified)

Commits (all FOUND in `git log`):

- `c83b3bd` — test(06-03): add failing tests for Task 1
- `6ba0eee` — feat(06-03): Task 1 GREEN — per-entry-tolerant tools/tool_choice decoder
- `7366c8d` — test(06-03): add failing tests for Task 2
- `be7e82d` — feat(06-03): Task 2 GREEN — non-streaming coerce + JSON-string render + arch-lint update
- `854fce4` — test(06-03): add failing tests for Task 3
- `1fb9bfd` — feat(06-03): Task 3 GREEN — streaming coerce + kiro-native text-delta + sawKiroNativeToolCall
