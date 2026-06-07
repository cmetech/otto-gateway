---
phase: 6
slug: tool-call-path
status: complete
nyquist_compliant: true
wave_0_complete: true
created: 2026-05-26
approved: 2026-06-07
---

# Phase 6 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.
> Operationalized from RESEARCH.md §Validation Architecture.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | Go stdlib `testing` + `testing/quick` (property tests) + `go.uber.org/goleak` v1.3.0 (leak detection) |
| **Config file** | None (Go convention); test files are `*_test.go` and `tests/e2e/*_test.go` with `//go:build e2e` |
| **Quick run command** | `go test ./internal/engine/... ./internal/acp/... ./internal/adapter/...` |
| **Full suite command** | `make ci` then `OTTO_E2E=1 go test -tags e2e ./tests/e2e/...` |
| **Estimated runtime** | ~10s quick, ~30s `make ci`, +~60s E2E with real-kiro |

---

## Sampling Rate

- **After every task commit:** Run `go test ./internal/engine/... ./internal/acp/... ./internal/adapter/...` (≤10s; covers unit + property + golden)
- **After every plan wave:** Run `make ci` (lint + race + govulncheck + all in-process tests; ~30s)
- **Before `/gsd-verify-work`:** Full suite green + `OTTO_E2E=1 go test -tags e2e ./tests/e2e/...` (real-kiro, ~60s) + HUMAN-UAT with real loop24-client `messages.stream()` for D-17 Anthropic conformance
- **Max feedback latency:** 10 seconds (quick path)

---

## Per-Task Verification Map

> Filled by nyquist-auditor (plan 13-05) on 2026-06-07. One row per V-ID from the original planner map; Plan/Wave columns now reflect the actual execution plan assignments from the 5 Phase-06 PLAN.md files. Adapter-side rows (V-01/V-02/V-03/V-10/V-11/V-12/V-14/V-15/V-16/V-17) reference existing adapter test files — no new tests written by 13-05 (owned by 13-03 Ollama, 13-04 Anthropic per scope_guard; 13-03 OpenAI adapter). Engine+jsonformat rows (V-04/V-05/V-07/V-08/V-09/V-18/V-20) all covered by existing `internal/engine/*_test.go`. No `internal/jsonformat/` package exists in this codebase; the tool-call encoding lives in `internal/adapter/*` and `internal/engine/`.

| Row | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|-----|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| V-01 | 06-02 | 2 | TOOL-01 (Ollama object args) | — | Non-streaming Ollama `tool_calls[].function.arguments` is plain object | unit | `go test ./internal/adapter/ollama/... -run TestChatResponseToWire_ToolCalls` | ✅ internal/adapter/ollama/render_test.go | ✅ green |
| V-02 | 06-03 | 2 | TOOL-01 (OpenAI JSON-string args) | — | Non-streaming OpenAI `tool_calls[].function.arguments` is JSON-encoded string | unit | `go test ./internal/adapter/openai/... -run TestChatResponseToCompletion_ToolCalls` | ✅ internal/adapter/openai/render_test.go | ✅ green |
| V-03 | 06-04 | 2 | TOOL-01 (Anthropic tool_use) | — | Non-streaming Anthropic `content[]` includes `tool_use` block with object `input` | unit | `go test ./internal/adapter/anthropic/... -run TestRender_ToolUse` | ✅ internal/adapter/anthropic/render_test.go | ✅ green |
| V-04 | 06-01 | 1 | TOOL-02 (bare JSON coerce) | T-V5-01 | Ollama+OpenAI: bare JSON in text → synthetic `tool_calls` | unit + property | `go test ./internal/engine -run TestCoerceToolCall` | ✅ internal/engine/coerce_test.go | ✅ green |
| V-05 | 06-01 | 1 | TOOL-02 (fenced JSON coerce) | T-V5-01 | Markdown-fenced JSON → synthetic `tool_calls` (fence stripped) | unit | `go test ./internal/engine -run TestCoerceToolCall_AlgorithmCases` | ✅ internal/engine/coerce_test.go (AlgorithmCases/fenced_json + bare_fence) | ✅ green |
| V-06 | 06-04 | 2 | TOOL-02 (no-coerce on Anthropic) | T-V5-02 | Anthropic surface preserves bare JSON text verbatim | unit + e2e | `go test ./internal/adapter/anthropic/... -run TestAnthropic_NoCoerce_Behavioral` | ✅ internal/adapter/anthropic/handlers_test.go | ✅ green |
| V-07 | 06-01 | 1 | TOOL-02 (pickBestTool tie-break) | — | First-declared tool wins on equal property-overlap scores | unit | `go test ./internal/engine -run TestCoerceToolCall_TieBreaker` | ✅ internal/engine/coerce_test.go | ✅ green |
| V-08 | 06-01 | 1 | TOOL-02 (never-panic) | T-V5-01 | Any `(req, resp)` shape including nil pointers — no panic | property (testing/quick, MaxCount=1000) | `go test ./internal/engine -run TestCoerceToolCall_NeverPanics` | ✅ internal/engine/coerce_test.go | ✅ green |
| V-09 | 06-01 | 1 | TOOL-02 (idempotent) | — | `Coerce(Coerce(x)) == Coerce(x)` | property | `go test ./internal/engine -run TestCoerceToolCall_Idempotent` | ✅ internal/engine/coerce_test.go | ✅ green |
| V-10 | 06-03 | 2 | TOOL-03 (OpenAI tools normalization) | — | OpenAI `tools[].function` → `canonical.ToolSpec` | unit | `go test ./internal/adapter/openai -run TestWireToChatRequest_Tools` | ✅ internal/adapter/openai/wire_test.go | ✅ green |
| V-11 | 06-04 | 2 | TOOL-03 (Anthropic tools normalization) | — | Anthropic `tools[].input_schema` → `canonical.ToolSpec` | unit | `go test ./internal/adapter/anthropic -run TestWireToChatRequest_Tools_Anthropic` | ✅ internal/adapter/anthropic/wire_test.go | ✅ green |
| V-12 | 06-02 | 2 | TOOL-03 (Ollama regression) | — | Phase 2 forward seam unchanged | unit | `go test ./internal/adapter/ollama -run TestWireToChatRequest_Tools_Phase2Regression` | ✅ internal/adapter/ollama/render_test.go | ✅ green |
| V-13 | 06-05 | 3 | All scenarios (goleak) | — | No goroutine leaks under tool_call streaming + cancel | leak | `goleak.VerifyTestMain` in testmain_test.go (engine pkg) + `OTTO_E2E=1 go test -tags e2e ./tests/e2e/... -run TestE2E_Tools` (per-subtest goleak) | ✅ internal/engine/testmain_test.go + tests/e2e/ | ✅ green |
| V-14 | 06-03 | 2 | D-07 OpenAI streaming wire shape | — | SSE byte sequence: frame1(id+name) + frame2(args) + terminal(`finish_reason:"tool_calls"`) | golden fixture | `go test ./internal/adapter/openai -run TestSSE_Golden_StreamingCoerce_BareJSON` | ✅ internal/adapter/openai/sse_golden_test.go | ✅ green |
| V-15 | 06-04 | 2 | D-07 Anthropic streaming wire shape | — | SSE byte sequence: `content_block_start{input:{}}` + `content_block_delta{input_json_delta}` + `content_block_stop` | golden fixture | `go test ./internal/adapter/anthropic -run TestSSE_Golden_ToolUse` | ✅ internal/adapter/anthropic/sse_golden_test.go | ✅ green |
| V-16 | 06-02 | 2 | D-07 Ollama streaming wire shape | — | NDJSON final `done:true` line carries `message.tool_calls[]` as plain object | golden | `go test ./internal/adapter/ollama -run TestNDJSON_StreamingCoerce_BareJSON` | ✅ internal/adapter/ollama/ndjson_test.go | ✅ green |
| V-17 | 06-02,06-03,06-04 | 2 | D-12 round-trip property | — | synth tool_call → adapter render → re-decode preserves name + args | property | `go test ./internal/adapter/ollama -run TestChatResponseToWire_ToolCalls` (round-trip subcase); OpenAI+Anthropic analogues | ✅ render_test.go (Ollama) + openai/render_test.go + anthropic/render_test.go | ✅ green |
| V-18 | 06-01 | 1 | D-16 `[Available tools]` catalog | T-V8-01 | `engine.buildBlocks` emits full JSON tool catalog when `req.Tools` non-empty | unit | `go test ./internal/engine -run TestBuildBlocks_AvailableTools_JSONCatalog` | ✅ internal/engine/build_acp_test.go | ✅ green |
| V-19 | 06-05 | 3 | D-17 scenario 12 (mid-stream cancel) | — | Client disconnect during tool_call → `session/cancel`; slot survives; no leak | e2e + leak | `OTTO_E2E=1 go test -tags e2e ./tests/e2e/... -run TestE2E_Tools_Cancel` | ✅ tests/e2e/tools_cancel_test.go | ✅ green (OTTO_E2E=1) |
| V-20 | 06-01 | 1 | TRST-07 Example function | — | `ExampleCoerceToolCall` runnable godoc | example | `go test ./internal/engine -run ExampleCoerceToolCall` | ✅ internal/engine/coerce_test.go | ✅ green |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

**Adapter-side rows note (V-01/V-02/V-03/V-06/V-10/V-11/V-12/V-14/V-15/V-16/V-17):** Tests for these rows live in `internal/adapter/{ollama,openai,anthropic}/*_test.go`. Per 13-05 scope_guard, those files are owned by sibling nyquist plans (13-03 for OpenAI, 13-04 for Ollama). Existing tests from Phase 06 execution cover these rows — 13-05 does not introduce new adapter-package test files.

---

## Wave 0 Requirements

- [x] `internal/engine/coerce.go` — EXISTS (264 lines; created by 06-01 Task 3)
- [x] `internal/engine/coerce_test.go` — EXISTS (432 lines; created by 06-01 Task 3)
- [x] `tests/e2e/tools_ollama_test.go` — EXISTS (14 subtests; created by 06-05 Task 2)
- [x] `tests/e2e/tools_openai_test.go` — EXISTS (15 subtests; created by 06-05 Task 2)
- [x] `tests/e2e/tools_anthropic_test.go` — EXISTS (4 subtests; created by 06-05 Task 2)
- [x] `tests/e2e/tools_cancel_test.go` — EXISTS (scenario 12 per-surface; created by 06-05 Task 3)
- [x] `tests/e2e/tools_fixtures_test.go` — EXISTS (3-tool catalog + FakeKiro API; created by 06-05 Task 1)
- [x] Golden fixtures for tool-call wire shapes: Anthropic `sse_golden_test.go` extended (V-15); OpenAI `sse_golden_test.go` extended (V-14); Ollama `ndjson_test.go` extended (V-16)
- [x] Framework install: NONE required — `testing/quick` is stdlib, `goleak` already in `go.mod`

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions | Status |
|----------|-------------|------------|-------------------|--------|
| `@anthropic-ai/sdk` MessageStream parses tool_use streaming correctly against the live binary | TOOL-01 (Anthropic) | SDK parser conformance — requires the real loop24-client to consume the stream and verify the SDK surfaces a complete `tool_use` block to the application layer | 1. `OTTO_E2E=1 go run ./cmd/otto-gateway` 2. From loop24-client repo: `ANTHROPIC_BASE_URL=http://localhost:11434 npm run smoke:tool-use` 3. Assert SDK emits `content_block_start` → `content_block_delta` → `content_block_stop` events and final `message.content` includes a complete `tool_use` block with object `input` | pending-UAT (scheduled before phase sign-off per original plan) |
| Assumption A1 — Node source byte-level fidelity check on `coerceToolCall` + `pickBestTool` | TOOL-02 | Node source `acp-ollama-server.js` is NOT in this checkout; RESEARCH.md confirmed algorithm against narrative reference only. Per CONTEXT.md line 471 ("Node code wins"), Phase 6 must verify byte-level fidelity before sign-off. | Path C accepted (06-01 Task 4 checkpoint): Node source inaccessible on this machine; algorithm verified against narrative reference + property tests prove the 4 load-bearing invariants across 1000 random inputs each. Post-ship LangChain smoke is the verification gate. | accepted-path-C |

---

## Validation Sign-Off

- [x] All tasks have `<automated>` verify or Wave 0 dependencies
- [x] Sampling continuity: no 3 consecutive tasks without automated verify
- [x] Wave 0 covers all MISSING references (all W0 fixtures now EXIST)
- [x] No watch-mode flags
- [x] Feedback latency < 10s
- [x] Manual-only verifications scheduled before phase sign-off (loop24-client UAT pending; Node byte-fidelity accepted via Path C)

**Approval:** 2026-06-07 (nyquist-auditor plan 13-05)
