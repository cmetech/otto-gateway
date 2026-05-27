---
phase: 6
iteration: 3
reviewers: [codex]
reviewed_at: 2026-05-27T03:02:11Z
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

# Cross-AI Plan Review — Phase 6 (Iteration 3)

> This is the THIRD review iteration. Prior REVIEWS.md files for iteration-1 (commit a3e34fd) and iteration-2 (commit 34ef930) are preserved in git history. This iteration-3 review evaluates the iteration-3 replan that fixed the iteration-2 regression.

## Codex Review

## Overall Assessment

Iteration 3 fixes the main iteration-2 semantic regressions on paper: non-streaming Ollama/OpenAI kiro-native tool calls now survive through `engine.Collect` as `[tool: name]\n` narration; streaming emitters track `sawKiroNativeToolCall`; the `Message.ToolCalls` contract is now per-surface; OpenAI mixed tool decoding uses per-entry `json.RawMessage`; Anthropic local collection has parity tests; fake-kiro lifetime and `go vet -tags e2e` are addressed.

Remaining risk is mostly in edge-case ordering and E2E feasibility. The biggest new issues are: streaming buffering can reorder JSON-shaped text that appears before a kiro-native tool call, cross-surface E2E cannot recover tool-call args from Ollama/OpenAI narration, the fake-kiro cancel test may complete too fast to observe cancellation, and the planned `TestMain` may conflict with the existing E2E `TestMain`.

## Cross-Plan Findings

### HIGH: Buffered Streaming Text Can Reorder Before Kiro-Native Tool Calls

Plans 06-02 and 06-03 buffer JSON-shaped text, but if the stream is:

```text
text("{\"location\":")
tool_call(get_weather)
text("\"NYC\"}")
```

the tool-call narration is emitted immediately while the prior buffered JSON is held until stream end. With `sawKiroNativeToolCall == true`, the buffer is later flushed after the tool narration, reordering the stream.

**Why it matters:** This breaks wire ordering and can confuse clients that display streamed deltas.

**Suggestion:** When a `ChunkKindToolCall` arrives while buffering, flush buffered text before emitting `[tool: name]\n`, then set `sawKiroNativeToolCall = true` and disable further coerce for the stream. Add tests for “JSON-shaped text before native tool_call”.

### HIGH: 06-05 Cross-Surface Canonical Equivalence Is Not Actually Observable

06-05 says `AssertSameCanonicalToolCall` will parse narration text for Ollama/OpenAI and native `tool_use` for Anthropic, then compare name + args. But Ollama/OpenAI kiro-native narration is only `[tool: name]\n`; it intentionally drops args on the wire.

**Why it matters:** The E2E helper cannot prove canonical arg equivalence from response bodies for native kiro calls. This assertion will either be impossible or will silently weaken to name-only.

**Suggestion:** Scope cross-surface arg equivalence to coerce-synthesized paths where args are present on the wire. For kiro-native paths, assert surface-specific behavior separately: Ollama/OpenAI narration name only; Anthropic native `tool_use` with args.

### HIGH: Fake-Kiro Cancel Scenario Needs Scripted Delay or Blocking

06-05 scenario 12 relies on disconnecting mid-stream, but fake-kiro currently emits all scripted notifications and the prompt result immediately. “Trailing text” does not create time if the fake writes frames back-to-back.

**Why it matters:** The gateway may finish before the client cancels, so `session/cancel` is never emitted. The test becomes flaky or false-negative.

**Suggestion:** Add script steps with delays or wait points, e.g. `Script{Steps: []Step{{Notif: ...}, {Sleep: 200ms}, ...}}`, or have fake-kiro block after the tool-call notification until the test cancels.

### HIGH: 06-05 TestMain Plan Conflicts With Existing E2E TestMain

The plan notes `tests/e2e/e2e_test.go` already has `TestMain`, but adds `tools_testmain_test.go` with another `TestMain` unless the existing one is extended. `e2e_test.go` is not in `files_modified`.

**Why it matters:** Two `TestMain` functions in one package will not compile.

**Suggestion:** Add `tests/e2e/e2e_test.go` to `files_modified` and make the plan explicitly extend the existing `TestMain`. Keep `tools_testmain_test.go` for helper/smoke tests only, or rename the compile function and call it from the existing `TestMain`.

---

## 06-01 Foundation

### Summary

This plan soundly resolves the iteration-2 non-streaming regression by aggregating `ChunkKindToolCall` into assistant text narration while keeping `Message.ToolCalls` untouched. The per-surface contract is now much clearer. Main residual risks are around text/thinking ordering and a minor documentation mismatch around synthesized IDs.

### Strengths

- Restores non-streaming Ollama/OpenAI kiro-native visibility via `engine.Collect`.
- Keeps `Message.ToolCalls` ownership explicit by surface.
- Adds focused no-coerce test for kiro-native narration text.
- Keeps Node byte-fidelity as a Wave 1 blocking checkpoint.
- Adds marshal-failure fallback logging for `[Available tools]`.

### Concerns

- **MEDIUM:** Appending tool narration into the text accumulator may lose original ordering if `engine.Collect` currently separates text and thinking streams. Mixed text/thinking/tool-call tests are only partially specified.
- **LOW:** `ToolCallChunk.ID` doc says IDs may be synthesized by `coerceToolCall`, but coerce synthesizes `canonical.ToolCall.ID`, not `ToolCallChunk.ID`.
- **LOW:** The acceptance grep for JSON tags is awkward and may produce misleading results, though the intent is fine.

### Suggestions

- Add a `Collect` test for `text → thinking → tool_call → text` and assert the exact intended content/thinking split.
- Change the `ToolCallChunk.ID` comment to “populated from ACP `toolCallId`”; document coerce synthesis on `canonical.ToolCall.ID` instead.

### Risk Assessment

**MEDIUM.** The prior regression is fixed, but mixed content ordering should be locked before implementation.

---

## 06-02 Ollama

### Summary

The Ollama plan correctly applies the new two-path model: coerce-synthesized calls become native `message.tool_calls`, while kiro-native chunks become narration only. The `sawKiroNativeToolCall` flag fixes the prior double-fire issue for native-then-JSON streams. The remaining concern is buffered-text ordering when JSON-shaped text precedes a native tool call.

### Strengths

- Correctly preserves Ollama object-shaped `arguments`.
- Explicitly tests non-streaming kiro-native narration.
- Adds streaming coerce for default streaming mode.
- Adds `sawKiroNativeToolCall` tests for native-then-JSON and native-only streams.
- Keeps kiro-native tool calls out of final `tool_calls[]`.

### Concerns

- **HIGH:** Buffered JSON-shaped text before a native tool call can be flushed after the tool narration, reordering output.
- **MEDIUM:** Whitespace-prefixed JSON may leak initial whitespace before buffering starts unless the emitter buffers leading whitespace while tools are present.
- **MEDIUM:** The plan says buffered text lines are released “one per chunk, or aggregated”; for NDJSON tests, pick one deterministic behavior to avoid fragile assertions.

### Suggestions

- Flush any existing buffer before emitting a native tool-call narration.
- Add tests for whitespace-prefixed bare JSON and fenced JSON split after leading whitespace.
- Make deferred text release deterministic.

### Risk Assessment

**MEDIUM-HIGH.** The core fix is good, but stream ordering needs one more guard.

---

## 06-03 OpenAI

### Summary

The OpenAI plan addresses iteration-2’s main gaps: per-entry `json.RawMessage` tools decoding, non-streaming coerce, streaming coerce, and native tool-call narration with `sawKiroNativeToolCall`. The same ordering issue from Ollama applies here, and the OpenAI SSE path has slightly higher parser sensitivity.

### Strengths

- Correctly changes `tools` to `[]json.RawMessage` for mixed-validity tolerance.
- Locks JSON-string `function.arguments`.
- Keeps `finish_reason:"tool_calls"` scoped to coerce hits.
- Adds role-emitted-once coverage for tool-call-first streams.
- Adds explicit native-then-JSON no-coerce tests.

### Concerns

- **HIGH:** Buffered JSON-shaped text before native `ChunkKindToolCall` can be emitted after `[tool: name]\n`, reordering SSE deltas.
- **MEDIUM:** Whitespace before JSON can be streamed before the emitter realizes the response is JSON-shaped, causing content + tool_calls inconsistency on coerce hit.
- **LOW:** Tool-choice decode is broad enough, but the plan should specify what happens if `tool_choice:"required"` is present with zero decoded valid tools.

### Suggestions

- Same as Ollama: flush buffered text before native tool-call narration and add ordering tests.
- Add whitespace-prefixed JSON streaming-coerce tests.
- Add a small test for `tool_choice:"required"` plus all-invalid tools to lock graceful behavior.

### Risk Assessment

**MEDIUM-HIGH.** Functionally close, but SSE ordering and buffering rules need tightening.

---

## 06-04 Anthropic

### Summary

The Anthropic plan is much clearer now: it explicitly owns the D-07 exception through `CollectAnthropicChat`, adds parity tests, preserves no-coerce behavior, and fixes non-streaming `stop_reason:"tool_use"`. The main unresolved design risk is whether `stop_reason:"tool_use"` should be set when any tool_use appears, even if later text follows.

### Strengths

- Good explicit documentation of the Anthropic exception.
- Parity tests vs `engine.Collect` address iteration-2 MEDIUM #5.
- Behavioral no-coerce test is the right primary guard.
- CR-01 `input:{}` preservation is explicitly tested.
- Non-streaming `stop_reason:"tool_use"` is now covered.

### Concerns

- **MEDIUM:** `stop_reason:"tool_use"` is triggered when content contains any `tool_use`, not necessarily when the final content block is `tool_use`. If text follows a tool_use block, the response may be semantically contradictory.
- **MEDIUM:** Parity tests cover non-tool behavior, but there is no direct unit test for `CollectAnthropicChat` preserving ordering in `text → tool_call → text`.
- **LOW:** Static-source no-coerce test is brittle, though acceptable as secondary.

### Suggestions

- Decide and document whether `stop_reason:"tool_use"` means “any tool_use exists” or “turn ended on tool_use.” Add tests for `text → tool_use → text`.
- Add a collector unit test for mixed text/tool/text ordering and content block sequence.
- Keep the static no-coerce test non-blocking in spirit; behavioral no-coerce should remain authoritative.

### Risk Assessment

**MEDIUM.** The iteration-3 parity fix is sound, but Anthropic stop-reason semantics need a sharper contract.

---

## 06-05 E2E

### Summary

The E2E plan is ambitious and covers the right scenario matrix, including the iteration-3 fixes. It addresses fake-kiro binary lifetime and e2e vet tagging. However, it has the highest remaining risk because several harness claims conflict with observable wire behavior or Go package mechanics.

### Strengths

- Full ACP method coverage for fake-kiro is now specified.
- Package-level fake binary lifetime fixes the prior `t.TempDir` bug.
- `go vet -tags e2e` resolves the prior vet gap.
- Per-subtest goleak gating is the right attribution model.
- Adds E2E coverage for non-streaming narration and native-then-JSON no-coerce.

### Concerns

- **HIGH:** Existing `TestMain` conflict risk, as described above.
- **HIGH:** Cancel test lacks scripted delay/blocking, so `session/cancel` may never be observable.
- **HIGH:** Cross-surface canonical equivalence cannot compare args from Ollama/OpenAI kiro-native narration.
- **MEDIUM:** `TestFakeKiro_BinaryExistsAfterMultipleSubtests` is good, but it does not prove parallel package test safety. Per-pid path helps, but concurrent `go test` invocations in the same package process are not the concern; parallel subtests sharing one binary path should be explicitly safe.
- **LOW:** The required `grep -c defer GoleakVerifyAtEnd` counts are brittle and may encourage test-shape padding.

### Suggestions

- Modify the existing E2E `TestMain` rather than adding another.
- Add fake-kiro script delays or synchronization steps for cancel tests.
- Change cross-surface equivalence to either coerce-only or name-only for kiro-native narration paths.
- Replace grep-count acceptance criteria with named subtest presence plus actual E2E pass.

### Risk Assessment

**HIGH.** The E2E intent is strong, but the current plan has compile, flake, and assertion-validity risks.

---

## Prior Concern Resolution Check

| Prior Concern | Iteration-3 Verdict |
|---|---|
| Non-streaming kiro-native lost for Ollama/OpenAI | **Resolved on paper** via `engine.Collect` narration aggregation. Needs mixed ordering tests. |
| Streaming coerce double-fire after kiro-native | **Mostly resolved** via `sawKiroNativeToolCall`. New ordering edge remains when buffered text precedes the native call. |
| “Sole producer” wording absolutism | **Resolved** with per-surface contract and Anthropic exception. |
| OpenAI mixed invalid tools | **Resolved** with `[]json.RawMessage` per-entry decode. |
| Anthropic local collect parity | **Resolved on paper** with parity tests. Add mixed tool ordering test. |
| Fake-kiro binary lifetime | **Mostly resolved**, but `TestMain` integration must be corrected. |
| `go vet -tags e2e` | **Resolved**. |

## Final Risk Assessment

**Overall: MEDIUM-HIGH.**

The plan is materially better than iteration 2 and no prior fix appears intentionally undone. The remaining issues are not conceptual disagreements with the phase design; they are execution hazards in buffering order and E2E harness validity. Fixing the four HIGH cross-plan findings above would drop the plan risk to **MEDIUM**.

---

## Consensus Summary

> Only one external reviewer was available (codex). Claude was skipped (SELF_CLI=claude inside Claude Code). All other reviewers (gemini/coderabbit/opencode/qwen/cursor/ollama/lm_studio/llama_cpp) are not installed/running on this machine.

### Iteration-2 Concern Resolution

Codex confirms iteration-3 resolves all 7 iteration-2 concerns on paper (3 HIGH + 4 MEDIUM):

| Iteration-2 Concern | Iteration-3 Verdict |
|---|---|
| HIGH #1 Non-streaming kiro-native lost | Resolved (engine.Collect aggregates as text narration; mixed-ordering tests still needed) |
| HIGH #2 Streaming coerce double-fire | Mostly resolved (sawKiroNativeToolCall flag added; ordering edge remains when text precedes the native call) |
| HIGH #3 "Sole producer" wording absolute | Resolved (per-surface contract + Anthropic D-07 exception) |
| MEDIUM #4 OpenAI mixed-invalid tools | Resolved ([]json.RawMessage per-entry decode) |
| MEDIUM #5 Anthropic CollectAnthropicChat parity | Resolved on paper (5 parity tests added; mixed-ordering test still needed) |
| MEDIUM #6 Fake-kiro binary lifetime | Mostly resolved (TestMain integration must be corrected — see new HIGH #4 below) |
| MEDIUM #7 go vet -tags e2e | Resolved |

**Codex confirms: "no prior fix appears intentionally undone."**

### New HIGH-Priority Concerns (introduced or surfaced by iteration-3 edits)

1. **Buffered streaming text reorders before kiro-native tool calls.** (Affects 06-02 + 06-03.) The new `sawKiroNativeToolCall` flag prevents end-of-stream double-coerce, but the streaming emitter buffers JSON-shaped text BEFORE the native chunk arrives. The native `[tool: name]
` narration is emitted immediately while the prior buffered text is held until stream end — reordering the wire output. **Fix:** when `ChunkKindToolCall` arrives while buffer is non-empty, FLUSH the buffer first (as plain text), THEN emit narration, THEN set `sawKiroNativeToolCall=true` (which disables further coerce). Add streaming tests for "JSON-shaped text BEFORE native tool_call".

2. **06-05 cross-surface canonical-equivalence is not actually observable for kiro-native paths.** Ollama/OpenAI kiro-native narration is just `[tool: name]
` — args are intentionally NOT on the wire. `AssertSameCanonicalToolCall` can't recover args from narration text to compare against Anthropic's `tool_use` block. **Fix:** scope cross-surface arg-equivalence to coerce-synthesized paths only. For kiro-native paths, assert surface-specific behavior: Ollama/OpenAI narration name-only; Anthropic native tool_use with args. Update 06-05 scenario assertions accordingly.

3. **06-05 cancel scenario (scenario 12) may complete too fast for cancellation to be observable.** Fake-kiro emits all scripted notifications + the prompt result back-to-back; there is no time for the client to disconnect mid-stream. `session/cancel` may never be sent, making the test flaky or false-negative. **Fix:** add scripted delays or blocking wait-points to the fake-kiro script DSL (e.g. `Step{Sleep: 200ms}` or `Step{WaitForSignal: ...}`). Or have fake-kiro block after the tool-call notification until the test cancels. Update 06-05 Task 3 + fixture API.

4. **06-05 TestMain conflicts with existing TestMain in tests/e2e/e2e_test.go.** Iteration-3 adds `tests/e2e/tools_testmain_test.go` with a new `TestMain`. The Go package can only have ONE `TestMain` per package. `e2e_test.go` is NOT in 06-05's `files_modified`. **Fix:** add `tests/e2e/e2e_test.go` to 06-05's `files_modified` AND extend the existing `TestMain` to call the fake-kiro compile function; keep `tools_testmain_test.go` for helper/smoke tests only (or rename the file and remove its TestMain).

### New MEDIUM Concerns

5. **Mixed text/thinking/tool ordering in engine.Collect (06-01).** Appending `[tool: name]
` narration into the text accumulator may lose original chunk ordering when text and thinking streams interleave with tool calls. **Fix:** add a `TestCollect_OrderingMixedTextThinkingToolCall` covering `text → thinking → tool_call → text` and assert exact content/thinking split.

6. **Whitespace-prefixed bare JSON in streaming coerce buffer.** Both 06-02 and 06-03 buffer based on whether trimmed accumulated text looks JSON-shaped. Initial whitespace before `{` may leak as a stream frame before the emitter realizes it should buffer. **Fix:** buffer leading whitespace when tools are present; add streaming-coerce tests for whitespace-prefixed JSON and fenced JSON split after leading whitespace.

7. **Anthropic stop_reason:"tool_use" semantics ambiguous when text follows tool_use (06-04).** Current behavior triggers on "any tool_use exists in content"; if text follows the tool_use block, the response is semantically contradictory. **Fix:** decide and document whether the rule is "any tool_use exists" or "turn ended on tool_use". Add tests for `text → tool_use → text` content sequence.

8. **Anthropic CollectAnthropicChat mixed-ordering not tested (06-04).** Parity tests cover non-tool behavior; add a collector unit test for mixed `text → tool_call → text` ordering.

9. **Deterministic buffered-text release rule (06-02 NDJSON).** Plan says lines are released "one per chunk, or aggregated" — pick one rule to avoid fragile test assertions.

10. **OpenAI tool_choice:"required" + all-invalid tools edge case (06-03).** Current plan doesn't specify graceful behavior. Add a small test locking the contract.

### Low-Priority Concerns

- **LOW:** 06-01 `ToolCallChunk.ID` doc comment says IDs may be synthesized by coerceToolCall, but coerce synthesizes `canonical.ToolCall.ID`, not `ToolCallChunk.ID`. Update comment: "populated from ACP toolCallId."
- **LOW:** 06-01 grep-based acceptance criterion for JSON tag absence is awkward; consider using a structural check.
- **LOW:** 06-04 static-source no-coerce test remains brittle; keep behavioral as authoritative (already done — confirming preservation).
- **LOW:** 06-05 `grep -c defer GoleakVerifyAtEnd ≥ N` acceptance criteria encourage test-shape padding; consider replacing with named-subtest-presence checks.

### Divergent Views

N/A — only one reviewer.

---

## Final Risk Assessment

**Codex overall risk: MEDIUM-HIGH.**

> The plan is materially better than iteration 2 and no prior fix appears intentionally undone. The remaining issues are not conceptual disagreements with the phase design; they are execution hazards in buffering order and E2E harness validity. Fixing the four HIGH cross-plan findings above would drop the plan risk to MEDIUM.

---

## To Incorporate Feedback

Re-plan with iteration-3 review feedback:

```
/gsd-plan-phase 6 --reviews
```

Iteration-4 replan should touch:
- **06-02 + 06-03** — flush buffer BEFORE emitting kiro-native narration (HIGH #1); whitespace-prefixed JSON buffering (MEDIUM #6); deterministic release rule (MEDIUM #9); OpenAI tool_choice:required + all-invalid edge case (MEDIUM #10)
- **06-05** — scope cross-surface equivalence to coerce-only (HIGH #2); add fake-kiro script delays for cancel (HIGH #3); fix TestMain conflict (HIGH #4); replace grep-count acceptance with subtest-presence
- **06-01** — mixed text/thinking/tool ordering test (MEDIUM #5); ToolCallChunk.ID comment fix (LOW)
- **06-04** — stop_reason:tool_use semantics decision + tests (MEDIUM #7); CollectAnthropicChat mixed-ordering test (MEDIUM #8)
