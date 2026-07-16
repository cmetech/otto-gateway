---
quick_id: 260716-bv0
slug: fix-gateway-tool-call-surfacing-kiro-per
date: 2026-07-16
title: "Gateway tool-call surfacing + kiro persona bleed"
status: complete
---

# Quick Task: Gateway tool-call surfacing + kiro persona bleed

Fix two production defects reproduced live in a loop24 Hermes desktop client
talking to the gateway's OpenAI surface. Plan verified against source this
session (see `docs/2026-07-16-gateway-toolcall-surfacing-fix-prompt.md`).

## Root causes (grounded in source)

**Defect 1 — kiro-native tool calls leak as `[tool: <name>]` text.**
`canonical.ToolCallChunk` (`internal/canonical/chunk.go:47-62`) carries `ID`,
`Name`, **and `Args`**. Anthropic surfaces all three as structured `tool_use`
(`internal/adapter/anthropic/collect.go:148-167`). OpenAI/Ollama deliberately
(Phase-6 "two-path rule") render kiro-native `ChunkKindToolCall` as
`[tool: <name>]\n` narration and **discard `Args`** at three sites:
- `internal/engine/collect.go:137-147` — non-stream aggregator (shared by
  OpenAI + Ollama non-stream; Anthropic uses its own aggregator).
- `internal/adapter/openai/sse.go:331-357` — `applyToolCallChunk` (streaming).
- `internal/adapter/ollama/ndjson.go:178-216` — `emitNDJSONChunk` (streaming).

The wire renderers already map `Message.ToolCalls` correctly
(`openai/render.go:178-208` args-as-JSON-string + `finish_reason:"tool_calls"`;
`ollama/render.go:87-95` args-as-object). They are simply never fed from
native chunks.

**Defect 2 — kiro persona bleed.** No gateway-side persona injection exists to
remove; `ACPClient.NewSession` takes only `cwd` (`acp_adapter.go:37`). The
caller's system prompt is forwarded as a `[System]` text section
(`build_acp.go:58-59`) which kiro's baked-in "Kiro CLI/AWS" persona out-weighs.
Only lever is the composed prompt text.

## Changes

1. **Non-stream (Defect 1a):** `engine/collect.go` — accumulate native tool
   calls into `Message.ToolCalls` (id/name/args); stop writing `[tool:]` text.
   `CoerceToolCall` idempotency guard makes the handler's later coerce a no-op.
2. **OpenAI stream (Defect 1b):** `openai/sse.go` `applyToolCallChunk` — emit
   native `delta.tool_calls` frames (id+name, then args JSON-string), terminal
   `finish_reason:"tool_calls"`; keep `sawKiroNativeToolCall` to skip coerce.
3. **Ollama stream (Defect 1c):** `ollama/ndjson.go` — accumulate native tool
   calls in `emitterState`, emit on `done:true` line via `chatResponseToWire`.
4. **Persona (Defect 2):** `build_acp.go` — brand-neutral negative-instruction
   identity clause (no OTTO/LOOP24 hardcode).
5. Update Phase-6 contract comments; verify `be95326` PII decrypt covers
   native-populated `Message.ToolCalls`.

## Tests (load-bearing — existing tests assert the OLD contract, REWRITE them)

- Rewrite: `ollama/render_test.go`, `ollama/ndjson_test.go`,
  `ollama/handlers_test.go`, `ollama/ndjson_posthook_test.go`, OpenAI
  `sse_test.go` + `sse_golden_test.go` (golden fixtures) + `render_test.go`,
  `engine/collect_test.go`.
- Add per-surface native-tool_call cases asserting structured `tool_calls` and
  **zero** `[tool:` in content.
- Keep `anthropic/collect_test.go` green (regression fence).
- Add deterministic prompt-assembly test for the Defect-2 clause.

## Out of scope
- Model normalization on Anthropic (unknown ids → 500 is intended contract).

## Verification
- `gofmt -l`, `go vet ./...`, `go test ./...` all green.
- Grep: no `[tool:` written into content/delta on any surface render path.
