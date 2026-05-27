---
phase: 06-tool-call-path
fixed_at: 2026-05-27T00:00:00Z
review_path: .planning/phases/06-tool-call-path/06-REVIEW.md
iteration: 1
findings_in_scope: 12
fixed: 12
skipped: 0
status: all_fixed
---

# Phase 6: Code Review Fix Report

**Fixed at:** 2026-05-27T00:00:00Z
**Source review:** `.planning/phases/06-tool-call-path/06-REVIEW.md`
**Iteration:** 1

**Summary:**
- Findings in scope: 12 (2 Critical/BLOCKER + 10 Warning)
- Fixed: 12
- Skipped: 0

All in-scope findings were addressed. Each fix was applied in an isolated
git worktree (`gsd-reviewfix/06-82839`), built with `go build ./...`, and
verified against the affected package's test suite. The full project test
suite (`go test ./...`) passes after every fix, and the e2e package
compiles under `-tags e2e` (gate-off run is a clean skip as designed).

## Fixed Issues

### CR-01: Ollama non-streaming error path echoes raw engine error string to client (T-02-33 inconsistency)

**Files modified:** `internal/adapter/ollama/handlers.go`, `internal/adapter/ollama/handlers_test.go`
**Commit:** `af75266`
**Applied fix:** Wrapped both `handleChat` and `handleGenerate` non-streaming `eng.Collect` error branches to log the raw error via `a.cfg.Logger.Error("ollama: engine.Collect error", "err", err)` and return the neutral `"internal error"` body. Mirrors the Anthropic/OpenAI discipline. Updated `TestHandleChat_EngineError_500` to lock the generic message and assert the raw "kiro exploded" string never reaches the wire.

---

### CR-02: `FakeKiro` panics on nil binary path when invoked without TestMain initialisation

**Files modified:** `tests/e2e/e2e_test.go`
**Commit:** `e654b9a`
**Applied fix:** Refactored `TestMain` to delegate to a new `runE2E(m *testing.M) int` helper that returns instead of calling `os.Exit(2)`. The fake-kiro output path is registered for cleanup via `defer` BEFORE the build runs, so a build failure no longer leaks the `otto-e2e-*` temp dir or partial fake-kiro outputs. Every `os.Exit(N)` path is now reachable only through the outermost wrapper, preserving defer execution.

---

### WR-01: Streaming-coerce buffering misses prose that PRECEDES the JSON object

**Files modified:** `internal/adapter/ollama/ndjson.go`, `internal/adapter/ollama/ndjson_test.go`, `internal/adapter/openai/sse.go`, `internal/adapter/openai/sse_golden_test.go`
**Commit:** `52ee196`
**Applied fix:** Added a `textFlushed` flag to both the Ollama `emitterState` and OpenAI `sseEmitter`. `shouldBuffer` / `applyTextChunk` refuse to enter the buffering branch once non-buffered text has been flushed to the wire (locks Pitfall 3 "entire text" invariant). Added prose-then-JSON regression tests on both surfaces that assert (a) no `tool_calls` field/frame, (b) no `finish_reason:"tool_calls"`, (c) both text fragments reach the wire. **Logic-bug class**: requires human verification — the textFlushed semantics are subtle and the regression tests pass, but a developer should confirm the Pitfall 3 interpretation is correct for the iteration-3 sawKiroNativeToolCall interaction.

---

### WR-02: Anthropic `argsJSON == "null"` coerce is conditioned on the wrong predicate

**Files modified:** `internal/adapter/anthropic/sse.go`
**Commit:** `d4de23b`
**Applied fix:** Replaced the post-marshal string-compare on `"null"` with a pre-marshal nil-map normalization (`if args == nil { args = map[string]any{} }`), unifying the defensive shape with the pointer-to-empty-map pattern used by `toolUseBlockHeader.Input`. The wire still emits `partial_json:"{}"` for nil/empty args (preserving the existing behaviour); the @anthropic-ai/sdk MessageStream parser path is now defended with one consistent pattern rather than two.

---

### WR-03: `parityFakeEngine.Run` silently discards the scripted error on Run

**Files modified:** `internal/adapter/anthropic/collect_test.go`
**Commit:** `0874064`
**Applied fix:** Added an `errOnRun bool` flag to `parityFakeEngine`. When `errOnRun=true && err != nil`, `Run()` returns `(nil, err)` directly, exercising the "engine: collect: <err>" wrap path inside `CollectAnthropicChat`. Added a sibling test `TestCollectAnthropicChat_ParityWithEngine_ErrorPropagation_RunPath` that drives the same sentinel through `Run` and asserts `errors.Is` reaches the sentinel — locks both error-wrap surfaces.

---

### WR-04: `synthesizeRunHandleFromCollectResp` masks real ChunkKind semantics

**Files modified:** `internal/adapter/anthropic/handlers_test.go`
**Commit:** `7d5042b`
**Applied fix:** Removed the second loop that synthesized `ChunkKindToolCall` chunks from `resp.Message.ToolCalls`. Added a `panic` on any caller that passes a pre-populated `ToolCalls` slice — the synthesizer contract is one-way (`Content -> chunks`), and pre-populated `ToolCalls` is a fixture mistake. No existing tests pass a `ToolCalls` slice to the synthesizer, so the panic is forward-protection only.

---

### WR-05: `joinThinkingContent` and `joinTextContent` duplicate logic across three adapters

**Files modified:** `internal/canonical/join.go` (new), `internal/adapter/ollama/render.go`, `internal/adapter/openai/render.go`, `internal/engine/build_acp.go`
**Commit:** `aa34a5c`
**Applied fix:** Created `internal/canonical/join.go` exporting `JoinTextParts` and `JoinThinkingParts` (using `strings.Builder` — the engine version's algorithm, NOT the O(n²) adapter version). All three previous implementations now delegate to the canonical helpers. Local function names (`joinTextContent`, `joinThinkingContent`, `joinTextParts`, `joinThinkingParts`) are preserved for grep continuity. TRST-04 layering unchanged: canonical was already a permitted dependency of every consumer.

---

### WR-06: `chatResponseToWire` `promptTokens := estimateTokens("")` is dead code

**Files modified:** `internal/adapter/ollama/render.go`
**Commit:** `7ed766d`
**Applied fix:** Inlined `PromptEvalCount: 0` in the struct literal and dropped the dead `promptTokens` variable + `if resp != nil` branch. Added a comment explaining why this is a hardcoded 0 (Phase 2 does not retain the prompt at render time; a real estimate needs a `canonical.ChatRequest` parameter — tracked as a Phase 8 task). Node parity preserved.

---

### WR-07: `chatResponseToTextCompletion` lacks direct test coverage

**Files modified:** `internal/adapter/openai/render_test.go`
**Commit:** `2810eab`
**Applied fix:** Added `TestChatResponseToTextCompletion` with two subtests:
- `ShapeAndFields` locks `Object == "text_completion"`, ID prefix `"cmpl-"` (not `"chatcmpl-"`), exactly one choice, joined text, mapped finish_reason, `logprobs:null`, usage envelope present.
- `NilResp_DefensiveDefaults` locks the nil-resp path (empty text, model echoed from `requestedModel`).

---

### WR-08: `completionWireRequest.MaxTokens` is `json.RawMessage` (accept-and-ignore) vs `int` on chat path

**Files modified:** `internal/adapter/openai/wire.go`
**Commit:** `529b628`
**Applied fix:** Added an inline comment block explaining the deliberate asymmetry. `/v1/completions` is accept-and-ignore per D-03 (kiro-cli does not expose max_tokens); `/v1/chat/completions` propagates the field into the canonical request. Comment documents the path forward if kiro-cli ever grows a max-tokens lever (hoist to `int`, wire through `promptToMessages`).

---

### WR-09: `tools_cancel_test.go` `time.Sleep(300 * time.Millisecond)` is a flaky timing dependency

**Files modified:** `tests/e2e/tools_cancel_test.go`
**Commit:** `8ef1a99`
**Applied fix:** Added a `waitForSessionCancel(t, framesPath, timeout)` helper that polls `ReadFakeKiroFrames` every 20ms (returns immediately when a `session/cancel` frame appears, with a 5s deadline). Replaced all three `time.Sleep(300 * time.Millisecond)` sites in the Ollama, OpenAI, and Anthropic cancel subtests. Fast path is now bounded by the actual propagation time; slow CI runners get up to 5s of headroom.

---

### WR-10: Anthropic `mapAnthropicRole` silently maps `"tool"` and `"system"` to `RoleUser`

**Files modified:** `internal/adapter/anthropic/wire.go`
**Commit:** `50004b7`
**Applied fix:** Documented the deliberate D-10 permissive-decode policy and the intentional asymmetry with the OpenAI surface (which hoists `role:"system"` into the canonical `System` field because the OpenAI spec allows it at the message level — Anthropic's does not). The behavior is locked by `TestWire_MapAnthropicRole`; the documentation now points future readers to that lock.

## Skipped Issues

None — all in-scope findings were applied.

## Verification Summary

- `go build ./...` — clean
- `go test ./... -count=1` — all packages pass
- `go test -tags e2e ./tests/e2e/... -count=1` — clean skip (gate off, as designed)
- `go vet ./...` — clean
- `go vet -tags e2e ./tests/e2e/...` — clean

Total: 12 fix commits, all atomic (one finding per commit), all on the
isolated `gsd-reviewfix/06-82839` branch.

---

_Fixed: 2026-05-27T00:00:00Z_
_Fixer: Claude (gsd-code-fixer)_
_Iteration: 1_
