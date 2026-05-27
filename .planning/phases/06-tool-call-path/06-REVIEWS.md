---
phase: 6
iteration: 2
reviewers: [codex]
reviewed_at: 2026-05-27T02:07:03Z
plans_reviewed:
  - 06-01-PLAN.md
  - 06-02-PLAN.md
  - 06-03-PLAN.md
  - 06-04-PLAN.md
  - 06-05-PLAN.md
skipped_reviewers:
  - claude (running inside Claude Code — skipped for independence per SELF_CLI=claude rule)
unavailable_reviewers:
  - gemini, coderabbit, opencode, qwen, cursor, ollama, lm_studio, llama_cpp (not installed / not running locally)
---

# Cross-AI Plan Review — Phase 6 (Iteration 2)

> This is the SECOND review iteration. The first review (preserved in git history at commit 540c1d4's parent) drove the iteration-1 replan via `/gsd-plan-phase 6 --reviews`. This iteration-2 review evaluates whether the revisions resolved the prior concerns and surfaces any NEW concerns introduced.

## Codex Review

## Overall Assessment

The revisions resolve several prior review concerns on paper: Node byte-fidelity moved to Wave 1, Anthropic non-streaming `stop_reason:"tool_use"` is now explicitly planned, fake-kiro scope is much better, and streaming coerce is no longer ignored. However, the new plans introduce a more serious consistency problem: the revised “two-path rule” is now over-corrected in 06-01/02/03 and leaves non-streaming kiro-native tool calls for Ollama/OpenAI with no viable rendering path. There is also a streaming edge case where kiro-native tool calls no longer populate `Message.ToolCalls`, so streaming coerce cannot know to skip when a native tool call already happened.

Overall risk is still **MEDIUM-HIGH**, mostly because the semantic model is clearer but not yet fully implementable across streaming and non-streaming modes.

---

## 06-01 — Foundation

### Summary

The plan is much improved around the prior concerns: it moves Node byte-fidelity before Wave 2, documents the two-path rule, adds `ToolCallChunk.ID`, and narrows `engine.CoerceToolCall` as the canonical coerce implementation. The main issue is that it now states `CoerceToolCall` is the “SOLE producer of `Message.ToolCalls`,” but later 06-04 intentionally violates that for Anthropic. More importantly, by making `engine.Collect` drop `ChunkKindToolCall`, the plan removes any path for non-streaming Ollama/OpenAI kiro-native tool-call narration unless the adapters add local aggregation.

### Strengths

- Moves the Node fidelity checkpoint to the correct dependency point before Wave 2.
- Strong property-test plan for `CoerceToolCall`, including inline fenced JSON in prose.
- Good decision to use simple string fence checks rather than regex.
- Clearer separation between ACP translation and adapter rendering.
- Adds marshal-error logging for `[Available tools]`, addressing the prior silent fallback concern.

### Concerns

- **HIGH:** `engine.Collect` dropping `ChunkKindToolCall` means non-streaming Ollama/OpenAI kiro-native tool calls disappear entirely. Wave 2 emitters only see live chunks on streaming paths; non-streaming adapters receive only `ChatResponse`.
- **HIGH:** “`CoerceToolCall` is the SOLE producer of `Message.ToolCalls`” conflicts with 06-04, where Anthropic-local aggregation populates `Message.ToolCalls` from kiro-native chunks.
- **MEDIUM:** The phrase “ChunkKindToolCall chunks pass through `engine.Collect` unchanged” is misleading. `Collect` consumes the chunk stream; if it does not aggregate them, they do not pass through to non-streaming renderers.
- **LOW:** The Node byte-fidelity checkpoint is human-only and can approve Path C with risk accepted. That is acceptable, but Wave 2 should record the chosen path in summaries so later implementers know whether Node parity was truly verified.

### Suggestions

- Replace “SOLE producer of `Message.ToolCalls`” with “sole generic engine producer; Anthropic has an adapter-local exception.”
- Add an explicit non-streaming decision matrix:
  - Ollama/OpenAI kiro-native non-streaming: render `[tool: name]\n` as text, or drop intentionally?
  - Anthropic kiro-native non-streaming: native `tool_use`.
- If Ollama/OpenAI non-streaming should preserve narration, `engine.Collect` needs to append `[tool: name]\n` to text/thought content or the adapters need local collectors like Anthropic.

### Risk Assessment

**HIGH.** The foundation resolves prior timing concerns, but its revised collect contract breaks or underspecifies non-streaming kiro-native behavior for two surfaces.

---

## 06-02 — Ollama Slice

### Summary

The revised Ollama plan directly addresses the streaming-coerce gap and clarifies that kiro-native streaming tool calls render as `[tool: name]\n` narration only. The buffering strategy is workable for JSON-shaped output, but the plan still has a major hole for non-streaming kiro-native tool calls because `engine.Collect` drops them. It also risks incorrectly coercing JSON text after a kiro-native tool call in streaming scenario 9, since the streaming synthetic response does not know a native tool call already occurred.

### Strengths

- Correctly locks Ollama `arguments` as a plain JSON object.
- Adds default-omitted `stream` coverage, which is important for Ollama compatibility.
- Streaming coerce plan avoids emitting partial JSON text before knowing whether coerce fires.
- Kiro-native streaming behavior is now aligned with the revised two-path rule.
- Defensive log guard addresses prior low-severity concern.

### Concerns

- **HIGH:** Non-streaming kiro-native tool calls are lost. The plan says non-streaming kiro-native renders inline as `[tool: name]\n`, but only streaming `ndjson.go` handles `ChunkKindToolCall`; `engine.Collect` drops it.
- **HIGH:** Streaming scenario 9 is likely wrong. If a kiro-native `ChunkKindToolCall` occurs and later JSON-shaped text is buffered, the synthetic response passed to `CoerceToolCall` has no existing `Message.ToolCalls`, so coerce may synthesize a second client tool call. This violates “existing tool_calls — coerce skipped.”
- **MEDIUM:** The JSON-shape buffering heuristic can delay arbitrary JSON-looking but non-tool responses until stream end. That is probably acceptable, but it should be acknowledged as a compatibility tradeoff.
- **LOW:** Buffering based on `strings.TrimSpace(accumulated)` should handle whitespace, but tests should include whitespace before `{` and fenced JSON split across chunks.

### Suggestions

- Add `sawKiroNativeToolCall bool` in the streaming emitter. If true, disable end-of-stream coerce or seed the synthetic response with a dummy existing tool call so `CoerceToolCall` no-ops.
- Define and implement non-streaming kiro-native behavior explicitly. If desired behavior is narration, aggregate `[tool: name]\n` into the non-streaming response text.
- Add streaming tests for whitespace-prefixed JSON and fenced JSON split across chunks.

### Risk Assessment

**HIGH.** Streaming coerce is addressed, but the revised implementation still fails important native-tool-call cases.

---

## 06-03 — OpenAI Slice

### Summary

The OpenAI plan mirrors the Ollama corrections well: JSON-string arguments are explicit, streaming coerce is added, and kiro-native streaming no longer emits native `delta.tool_calls`. The same two core risks remain: non-streaming kiro-native tool calls are dropped, and streaming coerce can fire after a kiro-native tool call because the emitter does not preserve “existing tool call” state for the coerce check. The typed tools decoder also may not actually tolerate malformed mixed tool arrays if malformed entries fail request-level JSON unmarshal.

### Strengths

- Correctly renders OpenAI `function.arguments` as a JSON string.
- Good `finish_reason:"tool_calls"` post-fixup for coerce-synthesized calls.
- Adds role-emitted-once coverage for tool-call-first streaming.
- Tool choice decoding coverage is practical.
- Removes the previous incorrect kiro-native `delta.tool_calls` streaming path.

### Concerns

- **HIGH:** Non-streaming kiro-native tool calls disappear for the same reason as Ollama: `engine.Collect` drops `ChunkKindToolCall`, and OpenAI non-streaming only renders `resp.Message.ToolCalls`.
- **HIGH:** Streaming coerce may incorrectly synthesize a tool call after a kiro-native tool call because no `sawKiroNativeToolCall` state blocks coerce.
- **MEDIUM:** Mixed valid/invalid tools tolerance is underspecified. If `Tools []openAIToolSpec` is decoded directly, an entry like `"function": "bad"` can fail unmarshalling the whole request before the per-tool skip loop runs. To truly preserve valid siblings, decode as `[]json.RawMessage` and unmarshal per entry.
- **LOW:** The plan says `delta.tool_calls` frames are used only by coerce, but the wire types live in `chunkDelta`; tests should assert no accidental native use in the `ChunkKindToolCall` branch.

### Suggestions

- Add `sawKiroNativeToolCall` and skip streaming coerce after native tool chunks.
- Add a non-streaming kiro-native render path or explicitly remove native non-streaming scenarios from 06-05 for Ollama/OpenAI.
- Change OpenAI tools decode to per-entry `json.RawMessage` if mixed malformed entries are a real requirement.
- Add malformed-type tools test, not just missing/empty-name entries.

### Risk Assessment

**HIGH.** The wire-shape plan is strong, but native tool-call handling is still inconsistent across streaming and non-streaming paths.

---

## 06-04 — Anthropic Slice

### Summary

The Anthropic plan is much more explicit than before. It correctly adds `render.go` for non-streaming `stop_reason:"tool_use"` and promotes behavioral no-coerce testing. The Option A1 local aggregator is a reasonable way to isolate the Anthropic exception, but it creates duplication risk and directly contradicts 06-01’s “sole producer” wording. The plan also needs to be careful not to bypass engine behavior, hooks, cancellation semantics, or response assembly details when duplicating `engine.Collect`.

### Strengths

- Explicitly resolves prior `stop_reason:"tool_use"` gap in non-streaming render.
- Behavioral no-coerce test is correctly made primary.
- Preserves Anthropic `tool_choice:"any"` losslessly with a test.
- CR-01 `input:{}` preservation is well covered.
- Block-index discipline is explicitly tested with golden output.
- Option A1 is now locked rather than left for execution-time decision.

### Concerns

- **HIGH:** Option A1 duplicates `engine.Collect` behavior in an adapter. If `engine.Collect` handles errors, stop reasons, thoughts, stats, cancellation, post-hooks, or response assembly subtleties, the Anthropic local collector can drift.
- **HIGH:** 06-04 populates `Message.ToolCalls` from kiro-native chunks, contradicting 06-01’s global “CoerceToolCall is the SOLE producer of `Message.ToolCalls`” rule.
- **MEDIUM:** The plan says `CollectAnthropicChat` wraps `engine.Run`, but handler flow must be verified carefully. If handlers already call `Run` before `Collect`, this can accidentally double-run or bypass existing handler responsibilities.
- **MEDIUM:** Static-source no-coerce test is still brittle. Keeping it as belt-and-suspenders is fine, but it should not block harmless refactors like aliasing imports or moving code.
- **LOW:** If Anthropic non-streaming includes both text and tool_use content, tests should verify ordering is preserved exactly as chunks arrived.

### Suggestions

- Rename the 06-01 invariant to allow “surface-local exceptions” and cite Anthropic explicitly.
- In 06-04, require tests proving `CollectAnthropicChat` matches `engine.Collect` for text-only, thinking-only, mixed text/thinking, stop reason, and error propagation.
- Confirm handler call structure before implementing Option A1; the plan should say whether it replaces a `Run+Collect` pair or a single `Collect` call.
- Make static no-coerce test less brittle if possible, or mark it as allowed to be updated during refactors as long as behavioral test remains.

### Risk Assessment

**MEDIUM-HIGH.** The Anthropic behavior is now specified well, but adapter-local collection is a meaningful maintenance and correctness risk.

---

## 06-05 — Cross-Surface E2E

### Summary

The E2E plan is substantially better: fake-kiro has a stable `(cmd, env)` contract, method coverage is complete, and Node byte-fidelity is no longer delayed. Per-subtest goleak is also an improvement. The remaining risks are harness complexity, a likely `sync.Once`/`t.TempDir` lifetime bug for the compiled fake binary, and E2E scenarios that may encode currently contradictory expectations from earlier plans.

### Strengths

- Fake-kiro now covers `session/set_model` and `ping`, addressing the prior harness gap.
- `(cmd, env)` fixture API is the right shape.
- Frame logging for `session/cancel` is a good concrete assertion.
- Per-subtest goleak attribution is better than parent-level cleanup.
- E2E coverage maps well to the D-17 matrix.
- loop24-client UAT remains as the correct Anthropic SDK conformance gate.

### Concerns

- **HIGH:** `FakeKiro` compiling once into a `t.TempDir()` path is unsafe. The first test’s temp dir is cleaned up after that test, leaving later tests with a cached path to a deleted binary.
- **HIGH:** E2E scenario expectations conflict with 06-01/02/03. Native non-streaming Ollama/OpenAI scenarios are listed, but the plans do not produce native tool_calls or narration for non-streaming kiro-native chunks.
- **MEDIUM:** `go vet ./tests/e2e/...` without `-tags e2e` will not fully vet e2e-only files. The verification should include `go vet -tags e2e ./tests/e2e/...`.
- **MEDIUM:** `goleak.VerifyNone` in process-level E2E can be noisy with HTTP transports and command wait goroutines. The ignore list needs to be empirically validated before becoming a hard gate across 25+ subtests.
- **MEDIUM:** Fake-kiro emits notifications “verbatim” during `session/prompt`, then sends the response. Tests must ensure notifications include the exact session id the gateway expects or translation/stateful paths may behave unrealistically.
- **LOW:** The cross-surface “same canonical tool call” helper is useful, but because Ollama/OpenAI native kiro calls are now text narration while Anthropic is native tool_use, equivalence must be scoped to coerce-synthesized paths or carefully normalized.

### Suggestions

- Build fake-kiro into a package-level temp dir not tied to an individual test, or compile per test without `sync.Once`.
- Change verification to `go vet -tags e2e ./tests/e2e/...`.
- Add a small harness self-test that starts fake-kiro directly and exercises all six ACP methods before using it in gateway E2E.
- Align D-17 scenario 1 expectations with the final two-path matrix before implementing tests.
- Validate `GoleakVerifyAtEnd` on a single smoke E2E before applying it to the full matrix.

### Risk Assessment

**MEDIUM-HIGH.** The harness design is much improved, but it is still complex and currently depends on unresolved semantic expectations from Waves 1-2.

---

## Cross-Plan Findings

### HIGH: Non-Streaming Kiro-Native Tool Calls Are Lost for Ollama/OpenAI

06-01 says `engine.Collect` drops `ChunkKindToolCall`; 06-02/03 only render kiro-native tool calls in streaming emitters. Non-streaming Ollama/OpenAI handlers only receive `ChatResponse`, so there is no source left for `[tool: name]\n` narration or native `tool_calls`.

**Fix:** Add an explicit non-streaming path. Either aggregate kiro-native tool chunks into text narration in `engine.Collect`, or add Ollama/OpenAI local collectors. Then update 06-05 scenario expectations accordingly.

### HIGH: Streaming Coerce Does Not Respect “Existing Tool Calls” After Kiro-Native Chunks

The original coerce invariant skips when `resp.Message.ToolCalls` is non-empty. In streaming Ollama/OpenAI, kiro-native tool calls no longer populate `Message.ToolCalls`, so buffered JSON after a native tool call can be coerced incorrectly.

**Fix:** Track `sawKiroNativeToolCall` in SSE/NDJSON emitters. If true, skip streaming coerce or seed the synthetic response with an existing tool call before calling `CoerceToolCall`.

### HIGH: “Sole Producer of Message.ToolCalls” Needs Scoped Wording

06-01 says only `CoerceToolCall` produces `Message.ToolCalls`; 06-04 intentionally produces `Message.ToolCalls` from kiro-native chunks for Anthropic.

**Fix:** Reword the invariant: “Generic engine collection does not produce `Message.ToolCalls`; OpenAI/Ollama populate them only via coerce; Anthropic has an adapter-local D-07 exception.”

### MEDIUM: OpenAI Mixed Invalid Tool Tolerance Is Not Fully Achieved

Directly decoding `Tools []openAIToolSpec` only tolerates structurally valid but semantically invalid entries. Type-invalid entries can fail the whole request decode.

**Fix:** Decode `tools` as `[]json.RawMessage` and unmarshal each entry independently if sibling preservation is required.

### MEDIUM: Anthropic Option A1 Needs Parity Tests Against Engine Collect

Adapter-local collection is acceptable, but it must be proven equivalent to `engine.Collect` for all non-tool behavior.

**Fix:** Add text/thinking/mixed/error/stop-reason parity tests for `CollectAnthropicChat`.

---

## Prior Concern Resolution Check

- **Streaming coerce gap:** Partially resolved. Plans add streaming coerce, but miss the native-tool-call skip edge case.
- **Two-path rule consistency:** Improved but overcorrected. Streaming semantics are clearer; non-streaming Ollama/OpenAI is now broken/undefined; Anthropic exception conflicts with 06-01 wording.
- **Node byte-fidelity timing:** Resolved. Moving it to 06-01 is sound.
- **Anthropic non-streaming stop_reason:** Resolved in plan. `render.go` modification and tests are appropriate.
- **Fake-kiro harness scope:** Mostly resolved. Method coverage and `(cmd, env)` API are good; fake binary lifetime and `go vet -tags e2e` need correction.

---

## Final Risk Assessment

**Overall: MEDIUM-HIGH.**

The revised plans are much closer to executable, but two load-bearing semantic problems remain: non-streaming kiro-native tool calls for Ollama/OpenAI have no rendering path, and streaming coerce can incorrectly fire after kiro-native tool calls. Fixing those requires plan edits before implementation, not just tests. Once those are corrected, the phase risk drops to **MEDIUM**, mainly from Anthropic local aggregation and E2E harness complexity.

---

## Consensus Summary

> Only one external reviewer was available (codex). No cross-reviewer consensus could be computed. Claude was skipped because the orchestrator runs inside Claude Code (SELF_CLI=claude). All other reviewers (gemini/coderabbit/opencode/qwen/cursor + local servers ollama/lm_studio/llama_cpp) are not installed/running on this machine.

### Prior Concern Resolution (iteration 1)

| Prior Concern | Codex Verdict |
|---|---|
| Streaming coerce gap | **Partially resolved** — streaming coerce added but native-tool-call skip edge case missed |
| Two-path rule consistency | **Improved but overcorrected** — non-streaming Ollama/OpenAI is now broken/undefined; Anthropic exception conflicts with 06-01 wording |
| Node byte-fidelity timing | **Resolved** — move to 06-01 is sound |
| Anthropic non-streaming stop_reason | **Resolved** — render.go modification appropriate |
| Fake-kiro harness scope | **Mostly resolved** — method coverage + (cmd, env) API good; fake binary lifetime + `go vet -tags e2e` need correction |

### New HIGH-Priority Concerns (introduced by iteration-1 replan)

1. **Non-streaming kiro-native tool calls are LOST for Ollama/OpenAI.** 06-01 says `engine.Collect` drops `ChunkKindToolCall`; 06-02/03 only render kiro-native in streaming emitters. Non-streaming handlers only receive `ChatResponse` — there is no source left for `[tool: name]
` narration. **Fix:** either aggregate kiro-native chunks into text narration in `engine.Collect`, or add Ollama/OpenAI local collectors (mirror the Anthropic D-07 exception).

2. **Streaming coerce does not respect "existing tool calls" after kiro-native chunks.** Coerce invariant skips when `Message.ToolCalls` is non-empty. Now that kiro-native chunks don't populate it, buffered JSON after a native tool call can be coerced incorrectly (double-fire). **Fix:** track `sawKiroNativeToolCall` in SSE/NDJSON emitters; skip streaming coerce or seed synthetic response with existing tool call when true.

3. **"Sole producer of Message.ToolCalls" wording is too absolute.** 06-01 invariant conflicts with 06-04 Anthropic adapter-local aggregation. **Fix:** reword to "generic engine collection does not produce Message.ToolCalls; OpenAI/Ollama populate via coerce; Anthropic has an adapter-local D-07 exception."

### New MEDIUM Concerns

4. **OpenAI mixed-valid/invalid tools tolerance is partial.** `[]openAIToolSpec` decode fails the whole request on type-invalid sibling entries. **Fix:** decode as `[]json.RawMessage` and unmarshal each entry independently.

5. **Anthropic Option A1 needs parity tests vs engine.Collect.** Adapter-local collection must be proven equivalent for text/thinking/mixed/stop-reason/error propagation.

6. **Fake-kiro `sync.Once` + `t.TempDir()` lifetime bug.** First test's temp dir is cleaned up after that test, leaving later tests with a cached path to a deleted binary. **Fix:** compile into a package-level temp dir, or compile per test without `sync.Once`.

7. **`go vet ./tests/e2e/...` without `-tags e2e` won't vet e2e-only files.** **Fix:** `go vet -tags e2e ./tests/e2e/...` in 06-05 Task 1 verify.

### Divergent Views

N/A — only one reviewer.

---

## To Incorporate Feedback

Re-plan with iteration-2 review feedback:

```
/gsd-plan-phase 6 --reviews
```

Iteration-3 replan will need to touch 06-01 (Collect contract: don't drop ChunkKindToolCall — aggregate into text narration for non-streaming; reword "sole producer" with Anthropic exception), 06-02 + 06-03 (add `sawKiroNativeToolCall` flag to streaming emitters; consider non-streaming `[tool: name]
` narration), 06-03 (OpenAI `tools` as `[]json.RawMessage` for per-entry tolerance), 06-04 (parity tests for adapter-local Collect vs engine.Collect), and 06-05 (fake-kiro binary lifetime fix; `-tags e2e` on vet).
