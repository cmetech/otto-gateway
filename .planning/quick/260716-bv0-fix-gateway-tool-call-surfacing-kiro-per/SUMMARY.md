---
quick_id: 260716-bv0
slug: fix-gateway-tool-call-surfacing-kiro-per
date: 2026-07-16
title: "Gateway tool-call surfacing + kiro persona bleed"
status: complete
branch: quick/gateway-toolcall-surfacing
commits: [019c7e4, 1f4fcd3, e6334d2, 116df0a, 8d1c1e7, fd9c8ce, 39d5320]
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

## Defect 1 — REDESIGNED: alias-primary native tool-call surfacing (commit 39d5320)

The first fix surfaced whatever native name kiro emitted **verbatim**. A real
`kiro-cli` capture (offered `run_shell`, prompted to run a shell command,
`ACP_CAPTURE=true`) proved that is wrong:

- kiro ignores the offered tool and emits a native ACP `tool_call` for its OWN
  built-in shell tool: `kind:"execute"`, `_meta.kiro.toolName:"shell"`,
  `rawInput:{"command":"…"}`. `translate.go` maps `kind` → `ToolCallChunk.Name`,
  so the native name is `execute` — a name the host never offered and cannot
  route.
- The gateway **denies** kiro's built-in whenever the caller offered tools
  (`acp.WithDenyBuiltinTools`, engaged by `engine.go` when `len(req.Tools) > 0`).
  kiro **retries with a fresh id** after each denial, then falls back to emitting
  the host-tool JSON as prose (flaky).
- Surfacing `execute` verbatim gave the host an unroutable call AND suppressed the
  correct coerced `run_shell` (idempotency guard) → would fail the parity check.

**Redesign — resolve, alias, drop, dedup:**

- **`KIRO_TOOL_ALIASES`** config (`internal/config/config.go`): comma-split
  `from:to` pairs (e.g. `execute:run_shell,shell:run_shell`). Empty default —
  aliases are deployment-specific.
- **`internal/engine/toolcall_resolve.go`** — shared primitives:
  - `ResolveNativeToolName(name, tools, aliases)` — no tools offered → surface
    as-is; native name (or its alias) matches an offered tool → surface under the
    **offered** name; otherwise **drop** (host can't route it; leaves the
    coerce/wrapper fallback unclobbered).
  - `DedupToolCalls(calls)` — merge kiro's `tool_call_chunk`+`tool_call` by id,
    then collapse identical `(name,args)` denial-retries to the first.
- **Wired into every surface**: `engine/collect.go` (OpenAI+Ollama non-stream),
  `adapter/openai/sse.go` (streaming; `sawKiroNativeToolCall` set only when a call
  actually SURFACES so coerce still runs on drops), `adapter/ollama/ndjson.go`
  (streaming done:true line), `adapter/anthropic/collect.go` (non-stream).
  `ToolAliases` threaded through `engine.Config` + all three adapter configs +
  `cmd/otto-gateway/main.go`.
- **Encrypt-mode replay:** `runSyntheticSSEFromResponse` (OpenAI) now emits
  tool_calls + `finish_reason:"tool_calls"`; Ollama's synthetic replay already
  carried them via `chatResponseToWire`.

This resolves the Defect-1 scope question below: the gateway now maps kiro's
built-in back to the host tool the caller offered, instead of leaving it to the
model to reformat.

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
- **Alias-primary unit tests:** `internal/config/tool_aliases_test.go`
  (`parseToolAliases`), `internal/engine/toolcall_resolve_test.go`
  (`ResolveNativeToolName` surface/alias/drop + `DedupToolCalls` chunk-merge +
  denial-retry collapse).
- **Alias-primary e2e** (`tests/e2e/tools_alias_test.go`, native `kind`+`rawInput`
  fixtures via new `NotifToolCallNative`/`NotifToolCallChunkNative` helpers):
  native `execute` + `KIRO_TOOL_ALIASES=execute:run_shell` + offered `run_shell`
  → structured `run_shell` on OpenAI (stream + non-stream), Ollama non-stream, and
  Anthropic non-stream `tool_use`; an unaliased built-in (`fs_write`, no alias) is
  dropped (no tool_calls, no `finish_reason:"tool_calls"`); and a
  chunk+full+denial-retry sequence dedups to exactly one call.
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
- **Alias-primary proven against REAL kiro-cli (v2.12.1, 2026-07-16).** Offered
  `run_shell`, prompted to run `python3 -c "print(2+2)"`, with
  `KIRO_TOOL_ALIASES=execute:run_shell,shell:run_shell`. The gateway returned
  exactly one structured `tool_calls` entry `{name:"run_shell",
  arguments:"{\"command\":\"python3 -c \\\"print(2+2)\\\"\"}"}` +
  `finish_reason:"tool_calls"`, NO `execute`, NO `[tool:`. The `ACP_CAPTURE`
  ring confirmed the mechanism: kiro emitted native `kind:"execute"`
  (`_meta.kiro.toolName:"shell"`), was denied, retried with a fresh id, and the
  gateway resolved `execute` → `run_shell` via the alias, surfaced it, and
  deduped the retry. Admin docs (`/admin/docs`) now list `KIRO_TOOL_ALIASES` with
  its live value.

## Out of scope (unchanged)

- Anthropic model normalization — an unknown model id still forwards to kiro and
  returns HTTP 500; intended contract, no HTTP-level validation added.

## Notes / follow-ups

- **Defect-1 scope question (RESOLVED by the alias-primary redesign, 39d5320):**
  the live `[tool: execute]` repro showed kiro reaching for its *own* built-in
  `execute`/`shell` tool rather than the host tool. Rather than relying on the
  model to reformat (flaky), the gateway now aliases the native name to the
  caller-offered tool (`KIRO_TOOL_ALIASES`) and surfaces it structurally.
- **Anthropic STREAMING SSE still surfaces native `tool_use` verbatim** (deferred,
  NOT a regression — its pre-existing behavior). The resolver is applied on the
  Anthropic non-stream path and both OpenAI/Ollama stream + non-stream paths, but
  the Anthropic streaming content-block state machine (`adapter/anthropic/sse.go`)
  was left unchanged for context budget + state-machine risk. To make it
  consistent: apply `engine.ResolveNativeToolName` at the block-open
  (`toolUseBlockHeader`, gate when `!surface`) + at `aggToolCalls`, thread
  `aliases` onto the emitter, add a per-turn denial-retry dedup guard; test hard
  against real kiro. HANDOFF.md §4 has the line-level pointers.
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
