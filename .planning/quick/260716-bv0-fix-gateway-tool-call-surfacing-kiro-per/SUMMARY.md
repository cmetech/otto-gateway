---
quick_id: 260716-bv0
slug: fix-gateway-tool-call-surfacing-kiro-per
date: 2026-07-16
title: "Gateway tool-call surfacing + kiro persona bleed"
status: complete
branch: quick/gateway-toolcall-surfacing
commits: [019c7e4, 1f4fcd3, e6334d2, 116df0a, 8d1c1e7, fd9c8ce]
---

# Summary — Gateway tool-call surfacing + kiro persona bleed

Fixed two production defects reproduced live in a loop24 Hermes desktop client
talking to the gateway's OpenAI surface.

## Defect 1 — tool calls leaked as `[tool: …]` text instead of structured tool_calls

**Root cause.** `canonical.ToolCallChunk` already carried `ID`, `Name`, and
`Args`, and Anthropic surfaced all three as structured `tool_use`. But the
OpenAI/Ollama paths (a deliberate Phase-6 "two-path rule") rendered kiro-native
`ChunkKindToolCall` as `[tool: <name>]\n` narration and **discarded the args** at
three sites. The host never received a structured tool call, so the tool never
executed, the model retried, and hallucinated a permission error.

**Fix (all three surfaces now surface structurally):**
- `internal/engine/collect.go` — non-stream aggregator (shared by OpenAI +
  Ollama) populates `Message.ToolCalls` (id/name/arguments) from native chunks;
  both renderers already map that to their wire shape. `CoerceToolCall`'s
  idempotency guard makes the handler's post-Collect coerce a no-op, so a native
  call is never double-counted.
- `internal/adapter/openai/sse.go` — `applyToolCallChunk` emits native
  `delta.tool_calls` frames (id+name, then arguments JSON-string) + terminal
  `finish_reason:"tool_calls"`.
- `internal/adapter/ollama/ndjson.go` — accumulates native calls in the emitter
  state and renders them on the `done:true` line's `message.tool_calls`
  (object-shaped args) via the existing `chatResponseToWire` mechanism.
- Synthesize a `call_<n>` id when kiro omits `toolCallId` (OpenAI clients need a
  stable id). **Zero** `[tool:` is written into content/delta on any path.
- PII: verified the `be95326` decrypt `After` hook walks
  `resp.Message.ToolCalls[].Arguments`, so native-populated args are decrypted on
  the response round-trip (non-streaming; encrypt-mode streaming re-routes through
  the same aggregation). No new PII work needed.

## Defect 2 — kiro persona bleed

**Root cause.** No gateway-side persona existed to remove — `NewSession` takes
only `cwd`, and the caller system prompt was forwarded as a weak `[System]` text
section that kiro-cli's baked-in "Kiro CLI"/AWS persona out-weighed.

**Fix.** `internal/engine/build_acp.go` always composes a `[System]` section
pairing the caller identity (authoritative when present) with a brand-neutral
`identityGuardClause` — no OTTO/LOOP24 hardcode, no angle-bracket markers
(kiro mis-parses `<...>` as XML). Emitted even when `req.System` is empty so a
bare "who are you?" turn is covered.

## Tests

- Rewrote every narration-era assertion to the structured contract:
  `engine/collect_test.go`, `openai/{render,sse,sse_golden}_test.go`,
  `ollama/{render,ndjson,ndjson_posthook,handlers}_test.go`.
- Added `build_acp` identity-guard tests (no-system + ordering) and a
  deterministic structured streaming test per surface.
- Rewrote the e2e tool matrix (`tests/e2e/tools_{openai,ollama,anthropic}_test.go`,
  `tools_fixtures_test.go`); `AssertSameCanonicalToolCall` now compares **name AND
  args** across all three surfaces (was name-only via narration parsing).
- Kept `anthropic/collect_test.go` + Anthropic e2e green as the regression fence.
- Refreshed the Phase-6 contract comments in `collect.go`, `coerce.go`,
  `openai/render.go`, `openai/handlers.go`, `ollama/{render,wire,ndjson}.go`, and
  `acp/translate.go` (whose docstring wrongly claimed `tool_call → ChunkKindThought`).

## Verification

- `gofmt -l` clean, `go vet ./...` clean.
- Full unit suite: 20 packages pass.
- Full e2e suite (real otto-gateway binary + fake-kiro): **green** (~263s). Run
  with `PII_REDACTION_MODE=replace GW_E2E=1` — the gateway's default
  `PII_REDACTION_MODE` is `encrypt`, which requires `PII_ENCRYPT_KEY` at boot.
- Confirmed end-to-end over HTTP: the real gateway returns structured
  `tool_calls` + `finish_reason:"tool_calls"` (OpenAI) / object-args
  `message.tool_calls` on the done line (Ollama) with no `[tool:` markers.

## Out of scope (unchanged)

- Anthropic model normalization — an unknown model id still forwards to kiro and
  returns HTTP 500; intended contract, no HTTP-level validation added.

## Notes / follow-ups

- **Defect-2 scope question (unanswered by operator):** the live `[tool: execute]`
  repro shows kiro reaching for its *own* built-in `execute` tool rather than the
  host's `code_execution`. Structured surfacing faithfully surfaces whatever kiro
  emits; making the model prefer the host tool names is a model/prompt concern.
  The `[Available tools]` section already says "You must NOT use your own built-in
  tools" — a stronger "use ONLY the listed tool names" nudge was left out pending
  a decision.
- **Encrypt-mode streaming limitation (pre-existing, not addressed):** in encrypt
  mode a streaming request is re-routed to the aggregated path and re-emitted via
  `runSyntheticSSEFromResponse`/`runSyntheticNDJSONFromResponse`, which drop
  tool calls ("v1 limitation"). Native tool calls ARE populated on the aggregated
  response, but the synthetic re-emit does not surface them. Separate seam.
- Identity/tool-call checks are model-dependent (flaky) — the host's
  `gateway-toolcall-parity` harness against a real `kiro-cli` remains the final
  acceptance gate; run each check a few times.
- Branch `quick/gateway-toolcall-surfacing` off `main`; **not pushed** (origin
  dual-pushes to GitHub + Ericsson GitLab).
