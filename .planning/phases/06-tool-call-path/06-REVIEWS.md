---
phase: 6
reviewers: [codex]
reviewed_at: 2026-05-26T18:30:00-04:00
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

# Cross-AI Plan Review — Phase 6

## Codex Review

## Overall Summary

The plan set is unusually thorough and mostly well-structured: Wave 1 establishes the canonical pivot, Wave 2 fans out by adapter, and Wave 3 validates cross-surface behavior. The biggest risk is not lack of coverage, but internal semantic drift: several plans mix two incompatible interpretations of the tool-call path, especially whether kiro-native `ChunkKindToolCall` should become native `tool_calls`/`tool_use` wire output during streaming or only `[tool: <name>]` narration. Resolve that before implementation, or the agents will build contradictory slices.

---

## plan-00 — Wave 1 Foundation (06-01-PLAN.md)

### Summary

Strong foundation plan. It identifies the right canonical seams and tests the core algorithm heavily. The main risk is that it bakes in behavior that later plans interpret differently, especially `engine.Collect` populating `Message.ToolCalls` from kiro-native chunks.

### Strengths

- Correctly centralizes `CoerceToolCall` in `internal/engine`.
- Good property-test coverage for panic safety, idempotency, tie-breaking, and no-match behavior.
- Adds `ToolCallChunk.ID`, which is necessary for all three wire surfaces.
- Good attention to JSON fence handling and deterministic scoring.
- Keeps new dependencies at zero.

### Concerns

- **HIGH:** The plan says kiro-native `ChunkKindToolCall` is aggregated into `Message.ToolCalls` and `ContentKindToolUse`. That may conflict with the highlighted two-path invariant: kiro-native tool calls are narration, coerce-synthesized calls are native tool calls. If non-streaming kiro-native output is supposed to return native `tool_calls`, document that as an explicit exception.
- **MEDIUM:** Deferring Node byte-fidelity to Wave 3 is late. If the Node `coerceToolCall` behavior differs, Wave 2 tests and adapters may be built around the wrong algorithm.
- **LOW:** `json.Marshal(req.Tools)` fallback to header-only on marshal error is safe, but silent fallback could make tools mysteriously disappear in tests using non-JSON-ish maps.

### Suggestions

- Add a short "kiro-native aggregation contract" note to plan-00: whether `Message.ToolCalls` from ACP is for non-streaming native output, hooks only, or both.
- Move the Node byte-fidelity checkpoint to immediately after plan-00, before Wave 2 starts.
- Add a focused unit test proving inline fenced JSON inside prose is not coerced.

### Risk Assessment

**MEDIUM.** The implementation scope is reasonable, but the semantic ambiguity around native ACP tool calls could propagate into every adapter.

---

## plan-01 — Ollama Slice (06-02-PLAN.md)

### Summary

Good Ollama-specific coverage for object-shaped arguments and NDJSON final-line aggregation. The main issue is that coerce is only hooked into non-streaming despite Ollama defaulting to streaming.

### Strengths

- Correctly locks Ollama `arguments` as a plain object.
- Preserves `[tool: <name>]\n` narration for streaming chunks.
- Tests that `tool_calls` appear only on the final `done:true` line.
- Regressions for existing Ollama tool spec decoding are valuable.

### Concerns

- **HIGH:** `engine.CoerceToolCall` is non-streaming only. Ollama defaults to `stream:true`, and LangFlow/LangChain compatibility is the load-bearing reason for coerce. If a streaming model emits bare JSON, this plan will not synthesize a tool call.
- **MEDIUM:** The plan emits `[tool: <name>]` on intermediate stream lines and also adds `tool_calls` on the final line for kiro-native tool calls. That may violate the stated two-path rule unless explicitly intended for Ollama.
- **LOW:** The debug log uses `resp.Message.ToolCalls[0]` after coerce; safe if coerce is correct, but still brittle. Prefer capture from return path or guard length defensively.

### Suggestions

- Decide whether streaming coerce is required for Ollama. If yes, add final-buffer coerce before the done line.
- Add a test for default omitted `stream` behavior, not just explicit `stream:false` / `true`.
- Make the final-line kiro-native `tool_calls` behavior an explicit contract if keeping it.

### Risk Assessment

**HIGH** until streaming coerce is resolved. Without it, the most important LangChain/LangFlow path may remain broken.

---

## plan-02 — OpenAI Slice (06-03-PLAN.md)

### Summary

The OpenAI plan is detailed and mostly accurate on JSON-string arguments and typed `tools` decode. Its largest issue is the same streaming-coerce gap, plus a direct conflict with the two-path rule if kiro-native streaming chunks emit OpenAI `delta.tool_calls`.

### Strengths

- Correctly replaces `Tools json.RawMessage` with typed tool specs.
- Correctly locks OpenAI `function.arguments` as a JSON string.
- Handles `finish_reason:"tool_calls"` for non-streaming and streaming.
- Multi-frame SSE shape is SDK-friendly.
- Tool choice coverage is practical.

### Concerns

- **HIGH:** Streaming `ChunkKindToolCall` emits real OpenAI `delta.tool_calls`. The review prompt explicitly calls out the invariant "kiro-native tool_call → `[tool: <name>]` thought-text narration; coerce-synthesized → real tool_calls." This plan appears to violate that.
- **HIGH:** Coerce is non-streaming only, but OpenAI paths default to streaming per requirements.
- **MEDIUM:** Tool choice mapping accepts unknown shapes as nil, which is fine, but tests should verify malformed `tools` entries are ignored without dropping valid siblings.
- **LOW:** The multi-frame SSE plan is good, but it should include a test that role emission still happens exactly once when tool call is the first chunk.

### Suggestions

- Resolve the streaming native tool-call rule before coding `sse.go`: either emit `[tool: ...]` text, or update the phase decision docs to say OpenAI streaming is intentionally native `tool_calls`.
- Add streaming coerce if OpenAI default-stream clients need LangChain JSON-as-text compatibility.
- Add tests for mixed valid/invalid tools and tool-call-first streams.

### Risk Assessment

**HIGH.** The wire shape details are good, but the plan may implement the wrong semantic path for kiro-native streaming.

---

## plan-03 — Anthropic Slice (06-04-PLAN.md)

### Summary

Strong focus on Anthropic-specific hazards: `input:{}` preservation, block indexes, and no-coerce. The biggest gap is non-streaming `stop_reason:"tool_use"`: the plan asserts it in E2E, but does not clearly modify the non-streaming render path to produce it.

### Strengths

- Correctly protects the CR-01 pointer-to-empty-map behavior.
- Good block-index golden test requirements.
- Explicitly locks "no coerce on Anthropic," which is important.
- Correctly decodes `tools[].input_schema` into canonical parameters.
- Preserving `tool_choice:"any"` losslessly is defensible.

### Concerns

- **HIGH:** Non-streaming Anthropic responses with tool use need `stop_reason:"tool_use"`, but the plan only updates SSE finalization. `render.go` is not modified, so E2E scenario 1 may still emit `end_turn`.
- **MEDIUM:** Static source test for absence of `engine.CoerceToolCall` is unusual but acceptable. It can be brittle if imports or wrappers change. Behavioral no-coerce tests are more valuable.
- **MEDIUM:** Tool choice `"any"` is preserved verbatim, while OpenAI `"required"` is used elsewhere. That may be fine, but engine semantics should explicitly accept both.
- **LOW:** The plan says `handlers.go` is modified for a comment, but the source-file context section under "Source files this plan modifies" omits it.

### Suggestions

- Add `internal/adapter/anthropic/render.go` to the modified files and explicitly override non-streaming stop reason to `"tool_use"` when content includes a tool_use block.
- Prefer the behavioral no-coerce test as required, not optional.
- Add one unit test for `tool_choice:any` proving downstream canonical consumers do not misinterpret it.

### Risk Assessment

**MEDIUM-HIGH.** The streaming plan is solid, but non-streaming Anthropic stop reason is under-specified and likely to fail later E2E.

---

## plan-04 — Cross-Surface E2E (06-05-PLAN.md)

### Summary

The validation ambition is excellent, but this plan is oversized and has harness-design risks. The fake-kiro infrastructure is doing a lot, and the Node fidelity checkpoint is too late.

### Strengths

- Covers the right scenario matrix, including no-coerce Anthropic and mid-stream cancel.
- Cross-surface equivalence helper is exactly the right validation layer.
- Good attention to wire-shape divergence across surfaces.
- Scenario 12 usefully ties Phase 4 cancellation to Phase 5 pool survival.
- Human UAT for `@anthropic-ai/sdk` is justified.

### Concerns

- **HIGH:** `FakeKiroScript(t, notifications) string` returns only `KIRO_CMD`, but the proposed compiled fake binary needs a notifications file env var. The API and implementation do not line up unless the returned command is a wrapper that embeds the file path.
- **HIGH:** The fake kiro only lists `initialize`, `session/new`, `session/prompt`, and `session/cancel`. The gateway may also call `session/set_model`, `ping`, and possibly other ACP methods. Missing these will cause E2E flakes or deadlocks.
- **HIGH:** Node byte-fidelity checkpoint should not wait until after all E2E work. If drift is found, much of the matrix may need to be rewritten.
- **MEDIUM:** Creating and compiling a fake CLI inside E2E is heavy. It is still reasonable, but it should be a small, explicit test utility with stable env contracts.
- **MEDIUM:** `goleak.VerifyNone` in process-level E2E can be noisy because the gateway is a child process plus HTTP clients. Make sure ignores and cleanup are proven before making it a hard gate everywhere.
- **LOW:** `tests/e2e/cmd/fake-kiro-cli/main.go` is required but not included in `files_modified`.

### Suggestions

- Change the fixture API to return both command path and env overlay, e.g. `FakeKiro(t, script) (cmd string, env map[string]string)`.
- Add fake support for every ACP method the gateway can issue during a normal request: `initialize`, `session/new`, `session/set_model`, `session/prompt`, `session/cancel`, `ping`, and EOF cleanup.
- Move Node byte-fidelity checkpoint to between plan-00 and Wave 2.
- Split plan-04 into two plans if possible: fake harness + matrix first, cancel/UAT/checkpoints second.
- Include `tests/e2e/cmd/fake-kiro-cli/main.go` in `files_modified`.

### Risk Assessment

**HIGH.** The validation target is right, but the harness is complex enough to become its own project, and one checkpoint arrives too late.

---

## Cross-Plan Issues To Resolve First

- **HIGH:** The two-path tool-call rule is inconsistent across plans. Some slices emit native `tool_calls` for kiro-native streaming; others emit `[tool: ...]` narration. Pick one rule per surface and per streaming/non-streaming mode before implementation.
- **HIGH:** Streaming coerce is not implemented for Ollama/OpenAI despite both defaulting to streaming and coerce being the LangChain compatibility feature.
- **HIGH:** Move Assumption A1 Node fidelity check earlier. It is not safe as a Wave 3 checkpoint if Wave 2 depends on exact behavior.
- **MEDIUM:** Anthropic non-streaming `stop_reason:"tool_use"` needs an explicit implementation plan.
- **MEDIUM:** Fake-kiro E2E needs a stable contract and complete ACP method support.

## Final Risk Assessment

**Overall risk: MEDIUM-HIGH.** The plans are comprehensive and mostly aligned with the phase goals, but the unresolved semantic conflict around native tool-call rendering and the missing streaming-coerce path are load-bearing. Fix those before execution; otherwise the implementation may be well-tested against the wrong contract.

*Tokens used: 95,087*

---

## Consensus Summary

> Only one external reviewer was available (codex). No cross-reviewer consensus could be computed — every concern below comes from codex alone and should be weighed accordingly. Claude was skipped because the orchestrator is running inside Claude Code (SELF_CLI=claude); gemini/coderabbit/opencode/qwen/cursor and the three local-server reviewers (ollama/lm_studio/llama_cpp) are not installed/running on this machine.

### Strengths (from codex)
- Wave structure is sound: foundation → parallel adapters → cross-surface validation.
- Property-test coverage of `CoerceToolCall` is thorough (panic safety, idempotency, tie-breaking, no-match).
- CR-01 pointer-to-empty-map preservation on Anthropic is correctly protected.
- Wire-shape divergence is captured in the right places (OpenAI JSON-string args, Ollama object args, Anthropic tool_use input object).
- Zero new dependencies — uses stdlib `testing/quick` + already-imported `goleak`.

### High-Priority Concerns (load-bearing)

1. **Streaming coerce gap** — `engine.CoerceToolCall` runs post-`engine.Collect` in handlers, which only fires on non-streaming requests. Ollama defaults to `stream:true` and OpenAI clients commonly stream. LangChain JSON-as-text compatibility — the load-bearing reason `coerceToolCall` exists at all — is therefore broken on the most common request shape. Need a streaming-side aggregator that buffers assistant text until end-of-stream (or first non-text chunk), runs coerce, then emits the final native tool_calls frame instead of the buffered text deltas. Touches Ollama NDJSON and OpenAI SSE emitters.

2. **Two-path tool-call rule ambiguity** — CONTEXT.md D-03/D-05 says kiro-native tool_call renders as `[tool: <name>]` thought-text narration, but the plans also wire kiro-native `ChunkKindToolCall` into `Message.ToolCalls` and emit them as native wire `tool_calls[]` on the final frame (Ollama done line, OpenAI delta+finish_reason=tool_calls, Anthropic content_block). For non-streaming this might be intentional; for streaming it appears to violate the narration rule. Need an explicit per-surface × streaming/non-streaming decision matrix in CONTEXT before adapter code lands.

3. **Assumption A1 timing** — Node byte-fidelity checkpoint is in Wave 3 (plan 06-05 task 4), but Wave 2 builds adapters around the coerce algorithm. If Node behavior diverges from the narrative reference, Wave 2 work is wrong. Move the checkpoint to between Wave 1 and Wave 2.

### Medium-Priority Concerns

4. **Anthropic non-streaming `stop_reason:"tool_use"`** — plan 06-04 updates SSE finalization but not `render.go`. E2E scenario 1 (non-streaming native tool_call) will likely emit `stop_reason:"end_turn"` instead. Add render.go to plan 06-04's `files_modified` and override stop reason when content includes a tool_use block.

5. **Fake-kiro harness scope** — plan 06-05's `FakeKiroScript(t, notifications) string` returns only `KIRO_CMD` but the implementation needs an env-overlay or wrapper to point the binary at the notifications file. Fake also only lists `initialize`, `session/new`, `session/prompt`, `session/cancel` — gateway also calls `session/set_model` and `ping`. Missing methods will cause flakes/deadlocks.

### Low-Priority Concerns

6. Silent header-only fallback in `[Available tools]` JSON catalog if `json.Marshal(req.Tools)` fails.
7. Debug log `resp.Message.ToolCalls[0]` post-coerce is brittle (guard length defensively).
8. OpenAI multi-frame SSE missing a test that role emission happens exactly once when tool_call is the first chunk.
9. Static-source absence-of-call test in 06-04 is brittle (prefer behavioral test).
10. `tests/e2e/cmd/fake-kiro-cli/main.go` missing from 06-05's `files_modified`.

### Divergent Views

N/A — only one reviewer.

---

## To Incorporate Feedback

Re-plan with review feedback:

```
/gsd-plan-phase 6 --reviews
```

This will load `06-REVIEWS.md` and replan the affected slices. Expect plan 06-01 (kiro-native aggregation contract clarification + Node-checkpoint move), 06-02 + 06-03 (streaming coerce + two-path decision), and 06-04 (render.go non-streaming stop_reason) to materially change. Plan 06-05 likely splits into two plans (fake-harness + matrix; cancel + UAT + checkpoints) per the codex recommendation.
