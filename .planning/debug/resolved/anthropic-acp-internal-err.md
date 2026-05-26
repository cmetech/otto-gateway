---
slug: anthropic-acp-internal-err
status: resolved
trigger: "Anthropic /v1/messages returning 500 with acp: prompt: rpc error -32603: Internal error — every request fails with the same JSON-RPC internal error from the kiro-cli ACP worker (real otto client testing)"
created: 2026-05-26T17:10:00-04:00
updated: 2026-05-26T17:30:00-04:00
---

# Debug Session: anthropic-acp-internal-err

## Symptoms

DATA_START
**Expected behavior:** Real otto client sends a chat message via the gateway's Anthropic surface (`POST /v1/messages`); gateway routes through guardrails into the kiro-cli ACP worker pool and streams a successful response back.

**Actual behavior:** Every `POST /v1/messages` returns HTTP 500. Server logs show a consistent error:

```
{"level":"ERROR","msg":"anthropic: engine.Run error","err":"anthropic engine run: engine: prompt: pool: prompt: acp: prompt: rpc error -32603: Internal error"}
```

Request durations are short (108–169 ms), suggesting fast failure from the ACP worker rather than a timeout. Request IDs increment normally (HfSofNW35O-000984 through ...992 shown), so the proxy is accepting requests and getting through to the engine.

**Error messages:**
- `rpc error -32603: Internal error` — this is JSON-RPC 2.0 reserved code `-32603` ("Internal error"), returned by the ACP worker (kiro-cli) in response to a `prompt` JSON-RPC method call.
- Wrap chain: `anthropic engine run → engine: prompt → pool: prompt → acp: prompt → rpc error -32603`. So the error originates from the ACP client speaking to the kiro-cli subprocess on the `prompt` method.

**Timeline:** Reported now during real-client testing. Phase 5 (pool / stateful sessions) just completed. Phase 6 (tool call path) has not started. The Anthropic surface (Phase 3.1) is on `main`. Unclear yet whether this ever worked end-to-end with a real client; e2e suites pass against real kiro per recent commits (e.g. `a57cbf5`, `49fb09e`) but those exercise OpenAI/Ollama surfaces — Anthropic surface e2e may not cover the same kiro round-trip.

**Reproduction:** Send any `POST /v1/messages` from the real otto client → gateway responds with 500 every time. Multiple consecutive request IDs show the same failure, so it is not a one-off; it is a deterministic failure of the prompt-to-kiro path on the Anthropic surface.
DATA_END

## Current Focus

hypothesis: The Anthropic adapter is constructing a `prompt` ACP request that kiro-cli rejects with JSON-RPC -32603 ("Internal error"). Likely shape mismatch in the prompt content blocks the Anthropic adapter forwards into the canonical/engine path versus what the ACP worker expects — most plausible candidates: (a) a content-block discriminator unique to Anthropic (e.g. tool_use/tool_result, image source.type, cache_control) leaking into the canonical request and breaking ACP's expected input; (b) a system prompt or message field handed to the engine in a shape OpenAI/Ollama paths normalize but Anthropic does not; (c) the pool/session layer reusing or initializing a session in a way that works for the other two surfaces but not for the Anthropic path's first turn.
next_action: Reproduce the failure locally with the gateway running in DEBUG mode and capture (1) the exact canonical request handed to engine.Run from the Anthropic adapter, (2) the raw JSON-RPC `prompt` frame sent to kiro-cli, and (3) kiro-cli's full -32603 error payload (data/message). Compare against a known-good prompt frame from an Ollama or OpenAI request to isolate which field differs.
test: Replay the same Anthropic request body the otto client sent (smallest possible — single user text message, no tools, no images, no cache_control) against a fresh gateway and inspect the ACP wire frames.
expecting: Either a successful 200 streamed response (eliminates "Anthropic adapter always wrong" and points to something otto-client-specific), or the same -32603 with a kiro-cli `data` payload describing the precise field/value rejected.

## Evidence

- timestamp: 2026-05-26T17:05:35
  observation: Server returns 500 for `POST /v1/messages` (request_id CoreysMacStudio.home/HfSofNW35O-000984), duration 169 ms.
  source: User-supplied gateway log
- timestamp: 2026-05-26T17:05:35.839
  observation: Error wrap chain `anthropic engine run: engine: prompt: pool: prompt: acp: prompt: rpc error -32603: Internal error` logged at ERROR.
  source: User-supplied gateway log
- timestamp: 2026-05-26T17:05:35..17:05:52
  observation: 9 consecutive requests across ~17 seconds all fail with the identical error and short duration (108–169 ms); deterministic failure of the Anthropic prompt path.
  source: User-supplied gateway log
- timestamp: 2026-05-26T17:15:00
  observation: |
    Exact request body from real otto CLI (user-supplied):
      POST http://127.0.0.1:18080/v1/messages
      Authorization: Bearer otto-gateway
      anthropic-version: 2023-06-01
      anthropic-dangerous-direct-browser-access: true
      {
        "model": "claude-sonnet-4-6",
        "messages": [{"role": "user", "content": "2+2"}],
        "max_tokens": 2730,
        "stream": true,
        "system": [{"type": "text", "text": "..."}],
        "tools": []
      }
    Notable fields:
      - model = "claude-sonnet-4-6" (kiro-cli may not recognize this exact model id)
      - system is an ARRAY of content blocks, not a string
      - tools is present but empty (tools: [])
      - stream: true
  source: User-supplied repro request

## Eliminated

- hypothesis: Empty/malformed prompt blocks (text/system normalization defect in the Anthropic adapter).
  reason: The same payload with `model: "auto"` succeeded (200, correct response). The wire decode for `system` array + `messages[].content` string + empty `tools` is therefore correct. Failure is purely model-ID-driven.
- hypothesis: Pool/session layer regression introduced by Phase 5 (stateful sessions).
  reason: `model: "auto"` and `model: "claude-sonnet-4.6"` (dot-form) both succeed against the same gateway/pool. The pool path is healthy; the failure trigger is set_model with a hyphen-version ID, independent of Phase 5 internals.

## Resolution

root_cause: |
  kiro-cli advertises model IDs with DOT-separated version components (`claude-sonnet-4.6`,
  `claude-haiku-4.5`, `claude-opus-4.7`), but Anthropic-API-compatible clients
  (loop24-client, otto-cli, `@anthropic-ai/sdk`) send HYPHEN-separated IDs by convention
  (`claude-sonnet-4-6`, `claude-haiku-4-5`, `claude-sonnet-4-5-20250514`).
  kiro-cli's `session/set_model` SILENTLY accepts unknown IDs (no JSON-RPC error
  on that frame), and the subsequent `session/prompt` then fails with
  `JSON-RPC -32603: Internal error` because the configured model name does not
  resolve to a real provider.
  Confirmed by curl matrix against the live gateway (PID 52745, real kiro-cli pool):
    - model:"auto"                         → 200 ✓ (engine skips SetModel)
    - model:"claude-sonnet-4.6" (dot)      → 200 ✓
    - model:"claude-haiku-4.5"  (dot)      → 200 ✓
    - model:"claude-sonnet-4-6" (hyphen)   → 500 ✗ (acp: prompt: rpc error -32603)
    - model:"claude-haiku-4-5"  (hyphen)   → 500 ✗ (acp: prompt: rpc error -32603)
    - model:"claude-sonnet-4-5-20250514"   → 500 ✗ (date-pinned form)
  `/v1/models` shows kiro advertises the dot form (`claude-sonnet-4.6` etc.).
  The Anthropic e2e suite uses `model:"auto"`, which skips set_model entirely
  — so the real-kiro round-trip for hyphen-version IDs was never exercised
  before this real-client test.
fix: |
  internal/adapter/anthropic/wire.go — add `normalizeClaudeModelID` (regex
  `^(claude-[a-z]+-\d+)-(\d+)(?:-\d{8})?$` → `<prefix>.<minor>`, optional
  -YYYYMMDD date tag dropped) and apply it inside `wireToChatRequest`
  before storing into `canonical.ChatRequest.Model`. The original
  `wire.Model` continues to flow into the response renderers, so SDK
  clients see back the exact ID they sent.
  Translation table (mechanical, no kiro lookup needed):
    claude-sonnet-4-6              → claude-sonnet-4.6
    claude-haiku-4-5               → claude-haiku-4.5
    claude-opus-4-7                → claude-opus-4.7
    claude-sonnet-4-5-20250514     → claude-sonnet-4.5   (date stripped)
    claude-sonnet-4                → claude-sonnet-4     (major-only; unchanged)
    auto / "" / non-claude IDs     → unchanged
verification: |
  - `go test ./internal/adapter/anthropic/` passes (full suite + new
    TestNormalizeClaudeModelID + TestWire_ModelNormalizedToKiroForm +
    TestWire_AutoModelPassesThrough).
  - `go build ./...` clean.
  - Live curl against rebuilt gateway (PID 58086):
      model:"claude-sonnet-4-6"           → 200 ✓ (echo: claude-sonnet-4-6)
      model:"claude-haiku-4-5"            → 200 ✓ (echo: claude-haiku-4-5)
      model:"claude-sonnet-4-5-20250514"  → 200 ✓ (echo: claude-sonnet-4-5-20250514)
      model:"auto"                        → 200 ✓ (echo: auto)
  - User's exact original request shape (stream:true + system as array +
    tools:[] + max_tokens:2730, model:"claude-sonnet-4-6") streams a
    correct SSE response ending in event:message_stop.
files_changed:
  - internal/adapter/anthropic/wire.go     (added regex + normalize helper; applied in wireToChatRequest)
  - internal/adapter/anthropic/wire_test.go (added TestNormalizeClaudeModelID table + 2 wire-decode tests)
