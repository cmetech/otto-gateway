# Phase 6: Tool-Call Path - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-05-26
**Phase:** 6-Tool-Call Path
**Areas discussed:** coerceToolCall placement, kiro tool-execution surfacing, Tool-call streaming scope, coerceToolCall algorithm fidelity, E2E test coverage

**Discussion mode:** User asked Claude to provide its best recommendations rather than walking through multiple choice questions. Claude synthesized prior-phase context (Phase 2/3/3.1/4/5 CONTEXT.md files) + codebase scout + Node reference docs + go_port_brief.md to produce recommendations across all four selected areas; user approved with one additional requirement (comprehensive E2E test matrix for all surfaces × success/failure).

---

## A. coerceToolCall placement

| Option | Description | Selected |
|--------|-------------|----------|
| Engine, post-stream | Inside `engine.Collect`. Runs once for all surfaces including Anthropic. Most consistent with "one place to enforce policy" core value, but forces the rewrite on Anthropic where it doesn't belong. | |
| Adapter, opt-in | Per-surface helpers in each adapter package. Anthropic opts out. Duplicates the non-trivial scoring algorithm across two surfaces — drift risk. | |
| **Engine helper, adapter-invoked** | Shared canonical-typed helper in `internal/engine/coerce.go`; Ollama + OpenAI handlers call it; Anthropic intentionally does NOT. One algorithm, per-surface invocation. | ✓ |

**Selected:** Engine helper, adapter-invoked (Claude's recommendation, user accepted).
**Notes:** The LangChain JSON-as-text pitfall is genuinely surface-specific — Anthropic-native clients (`@anthropic-ai/sdk` / loop24-client) don't emit raw JSON in assistant text; running coerce on Anthropic would silently rewrite the response into `tool_use` blocks, surprising the client. Matches Node version's per-handler invocation structure; preserves "one place to enforce policy" at the algorithm level while respecting the per-surface streaming-day-one precedent set in Phases 3 and 3.1.

---

## B. kiro tool-execution surfacing

| Option | Description | Selected |
|--------|-------------|----------|
| **Preserve Node "narrative thought" + populate canonical fully** | kiro's `tool_call` ACP notif → real `canonical.ToolCallChunk` (closes Phase 1.1 placeholder TODO) → rendered per-surface as `[tool: <name>]\n` thought text. Separately, `coerceToolCall` synthesis → real `Message.ToolCalls[]` entry rendered in surface-native shape. Two paths, intentionally different render. | ✓ |
| Emit structured tool_use blocks; skip tool_result | kiro's tool_call becomes a real tool_use block on the wire; tool_result is dropped (kiro consumed it). Half-formed for clients expecting the agent loop. | |
| Emit BOTH tool_use AND synthetic tool_result + user turn | Closes the agent loop visibly. Synthesizes turns the client never sent — wire-shape violation. | |

**Selected:** Preserve Node "narrative thought" behavior; populate canonical type fully (Claude's recommendation, user accepted).
**Notes:** kiro-cli auto-grants permissions and runs tools internally — by the time the gateway sees the result, the agent loop has already happened. Clients can't respond to kiro's tool_result. Synthesizing a tool_result + user-turn fakes wire shape the client never sent. Node has shipped this narrative-thought behavior to LangFlow + Pi-SDK + LangChain agents for a year. The subtle but correct split: kiro-native → thought-text narration (kiro already ran it); coerce-from-text → real tool_calls entry (client must run it). Same canonical type, two different sources, two different render decisions. This is the most counter-intuitive Phase 6 decision and is called out in CONTEXT.md `<specifics>` for the PR description and post-execution PATTERNS.md entry.

---

## C. Tool-call streaming scope

| Option | Description | Selected |
|--------|-------------|----------|
| Full incremental streaming | Char-by-char `input_json_delta`, JSON-string char-deltas. No actual incremental source from kiro — would be theatre. | |
| **Atomic event sequences per surface (SDK-correct)** | kiro emits complete args atomically; gateway renders the full SDK-expected event sequence in one logical burst. Anthropic: content_block_start → one input_json_delta with full JSON → content_block_stop. OpenAI: single delta.tool_calls frame + finish_reason:"tool_calls". Ollama: tool_calls on the final done:true line. | ✓ |
| Defer streaming; non-streaming only | `@anthropic-ai/sdk` MessageStream wouldn't surface tool_use blocks at all in streaming mode → regression vs expected loop24-client behavior. | |

**Selected:** Atomic emission per surface (Claude's recommendation, user accepted).
**Notes:** kiro-cli does not emit partial tool args — each `tool_call` ACP notification is atomic (`toolCallId` + `title` + complete `args`). Synthesizing char-deltas adds code complexity for no behavioral gain. But SDK parsers EXPECT the full event sequence (the `@anthropic-ai/sdk` MessageStream specifically handles tool_use via `content_block_start` → `input_json_delta`+ → `content_block_stop`) — emitting the sequence in atomic form (one delta carries everything) is wire-correct and parser-safe.

---

## D. coerceToolCall algorithm fidelity

| Option | Description | Selected |
|--------|-------------|----------|
| Smarter than Node (specific-schema preference, recursion into nested properties) | Risk of behavioral drift; Node's algorithm has commit-log receipts (`0ead935`, `7569745`, `995c569`). | |
| **Strict Node parity with explicit edge-case locks** | Mirror Node's order-of-checks; first-declared tie-breaker; top-level-only schema overlap; both ```json and bare ``` fences supported; zero-overlap → preserve text (no synthesis); empty/non-object parsed → no synthesis; idempotent. | ✓ |
| Strip-down (no fence handling) | Misses the load-bearing LangChain failure mode. Would silently break clients. | |

**Selected:** Strict Node parity with explicit edge-case locks (Claude's recommendation, user accepted).
**Notes:** The brief is explicit (`docs/briefs/go_port_brief.md` § 1.4 lines 133-138, § "Things that must survive the port"): *"This must be preserved in the Go port. It exists because LangChain agents rely on JSON-as-tool-call. Removing it silently breaks real users."* Edge cases locked in CONTEXT.md D-09 / D-10. Synthetic IDs use `call_<unix-nano>` (mirrors Phase 2 chatcmpl pattern + matches OpenAI convention). Property tests via `testing/quick` per `pickcwd_test.go` precedent (TRST-06).

---

## E. E2E test coverage (USER-ADDED REQUIREMENT)

| Option | Description | Selected |
|--------|-------------|----------|
| Unit + property tests only | Insufficient confidence per user requirement. | |
| **Full E2E matrix: all surfaces × success + failure × streaming + non-streaming** | 12-scenario matrix per CONTEXT.md D-17. Real-binary boot with controllable fake-kiro-cli via `KIRO_CMD`. Cross-surface assertions on canonical-response equivalence. goleak gate on all E2E tests. | ✓ |
| Smoke tests only | Insufficient regression coverage on the load-bearing LangChain compat behavior. | |

**Selected:** Full E2E matrix (user-requested in follow-up after recommendations).
**Notes:** User specifically asked: *"please ensure we have proper e2e tests for success/failure that exercise all api endpoints (openai, anthropic, ollama), so we are confident that tool calling handles properly for failure and success cases across all clients."* CONTEXT.md D-17 ships the matrix; D-18 reuses the existing `tests/e2e/` harness from quick-tasks 260524-pee + 260524-pyd; D-19 splits files by surface (`tools_ollama_test.go`, `tools_openai_test.go`, `tools_anthropic_test.go`, `tools_cancel_test.go`); D-21 enforces goleak. The Anthropic-skips-coerce verification (scenario 5) is explicit in the matrix — proves D-01's intentional asymmetry and prevents accidental regression if a future refactor unifies the per-surface handlers.

---

## Claude's Discretion

The planner/researcher have latitude on (per CONTEXT.md `<decisions>` § "Claude's Discretion"):

- Exact file split: `internal/engine/coerce.go` vs private helper inside `collect.go`. Recommended: separate file.
- Synthetic-coerced ID prefix: `call_<unix-nano>` (recommended) vs `call_<random-hex>` vs `toolu_<hex>`. `call_` preferred to match OpenAI convention.
- `pickBestTool` secondary tie-breaker if planner finds Node uses one (e.g., total property count) — match Node exactly if found.
- OpenAI streaming tool_call frame composition: single combined frame vs split (id+name first, args second). Either works with `@openai-sdk`; pick cleaner Go path.
- Anthropic `content_block_start` initial `input` value: `{}` (matches non-streaming render) vs `null`. Either valid with `@anthropic-ai/sdk`.
- Test data tool names (`get_weather` / `read_file` / `search_web` etc.) — stay consistent across the three surface test files.
- Whether kiro `[tool: <name>]\n` rendering grows to `[tool: <name>(args)]\n` for operator-DX. D-03 locks wire shape; richer form is a planner judgement call if zero-cost.

## Deferred Ideas

Captured in CONTEXT.md `<deferred>`:

- Full agent-loop transparency (synthetic tool_result + user turn for kiro's `tool_call_update`)
- True incremental tool_call streaming (char-by-char `input_json_delta`)
- `/api/generate` tools[] support
- Per-tool budget / audit hooks observing `ChunkKindToolCall` — Phase 8
- Tool-call cancellation mid-execution beyond `session/cancel`
- Per-tool rate limiting / quotas — Phase 8
- OpenAI `parallel_tool_calls` field
- OpenAI `response_format.json_schema` strict-output mode
- Anthropic multi-delta `input_json_delta` streaming
- `tool_choice: required` gateway-side enforcement
- Visual debug renderer for tool-call wire matrix (Phase 9 distribution)

### Reviewed Todos (not folded)

- `perf-baseline-vs-node.md` (score 0.6, weak keyword match) — milestone-deferral perf gate from Phase 5 Accepted-with-Notes; not Phase 6 work.
