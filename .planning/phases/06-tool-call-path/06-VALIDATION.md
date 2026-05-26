---
phase: 6
slug: tool-call-path
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-05-26
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

> Filled by planner during PLAN.md generation. The 19-row map below is the canonical Phase 6 requirements-to-test matrix; planner translates each row into one or more `<task>` entries with `<automated>` blocks.

| Row | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|-----|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| V-01 | TBD | TBD | TOOL-01 (Ollama object args) | — | Non-streaming Ollama `tool_calls[].function.arguments` is plain object | unit + e2e | `go test ./internal/adapter/ollama/... -run TestChatResponseToWire_ToolCalls` + `OTTO_E2E=1 go test -tags e2e ./tests/e2e/... -run TestE2E_Tools_Ollama/NativeToolCall_NonStreaming` | ❌ W0 | ⬜ pending |
| V-02 | TBD | TBD | TOOL-01 (OpenAI JSON-string args) | — | Non-streaming OpenAI `tool_calls[].function.arguments` is JSON-encoded string | unit + e2e | `go test ./internal/adapter/openai/... -run TestChatResponseToCompletion_ToolCalls` + e2e | ❌ W0 | ⬜ pending |
| V-03 | TBD | TBD | TOOL-01 (Anthropic tool_use) | — | Non-streaming Anthropic `content[]` includes `tool_use` block with object `input` | unit + e2e | `go test ./internal/adapter/anthropic/... -run TestRender_ToolUse` + e2e | ❌ W0 | ⬜ pending |
| V-04 | TBD | TBD | TOOL-02 (bare JSON coerce) | T-V5-01 | Ollama+OpenAI: bare JSON in text → synthetic `tool_calls` | unit + property + e2e | `go test ./internal/engine -run TestCoerceToolCall` + e2e | ❌ W0 | ⬜ pending |
| V-05 | TBD | TBD | TOOL-02 (fenced JSON coerce) | T-V5-01 | Markdown-fenced JSON → synthetic `tool_calls` (fence stripped) | unit + e2e | `go test ./internal/engine -run TestCoerceToolCall_Fenced` + e2e | ❌ W0 | ⬜ pending |
| V-06 | TBD | TBD | TOOL-02 (no-coerce on Anthropic) | T-V5-02 | Anthropic surface preserves bare JSON text verbatim | e2e | `OTTO_E2E=1 go test -tags e2e ./tests/e2e/... -run TestE2E_Tools_Anthropic/NoCoerce` | ❌ W0 | ⬜ pending |
| V-07 | TBD | TBD | TOOL-02 (pickBestTool tie-break) | — | First-declared tool wins on equal property-overlap scores | property | `go test ./internal/engine -run TestCoerceToolCall_TieBreaker` | ❌ W0 | ⬜ pending |
| V-08 | TBD | TBD | TOOL-02 (never-panic) | T-V5-01 | Any `(req, resp)` shape including nil pointers — no panic | property (`testing/quick`, MaxCount=1000) | `go test ./internal/engine -run TestCoerceToolCall_NeverPanics` | ❌ W0 | ⬜ pending |
| V-09 | TBD | TBD | TOOL-02 (idempotent) | — | `Coerce(Coerce(x)) == Coerce(x)` | property | `go test ./internal/engine -run TestCoerceToolCall_Idempotent` | ❌ W0 | ⬜ pending |
| V-10 | TBD | TBD | TOOL-03 (OpenAI tools normalization) | — | OpenAI `tools[].function` → `canonical.ToolSpec` | unit | `go test ./internal/adapter/openai -run TestWireToChatRequest_Tools` | ❌ W0 | ⬜ pending |
| V-11 | TBD | TBD | TOOL-03 (Anthropic tools normalization) | — | Anthropic `tools[].input_schema` → `canonical.ToolSpec` | unit | `go test ./internal/adapter/anthropic -run TestWireToChatRequest_Tools` | ❌ W0 | ⬜ pending |
| V-12 | TBD | TBD | TOOL-03 (Ollama regression) | — | Phase 2 forward seam unchanged | unit | `go test ./internal/adapter/ollama -run TestWireToChatRequest_Tools` | ✅ (Phase 2) | ⬜ pending |
| V-13 | TBD | TBD | All scenarios (goleak) | — | No goroutine leaks under tool_call streaming + cancel | leak | `goleak.VerifyNone(t)` wrapped in each E2E test | ❌ W0 | ⬜ pending |
| V-14 | TBD | TBD | D-07 OpenAI streaming wire shape | — | SSE byte sequence: frame1(id+name) + frame2(args) + terminal(`finish_reason:"tool_calls"`) | golden fixture | `go test ./internal/adapter/openai -run TestSSE_Golden_ToolCall` | ❌ W0 | ⬜ pending |
| V-15 | TBD | TBD | D-07 Anthropic streaming wire shape | — | SSE byte sequence: `content_block_start{input:{}}` + `content_block_delta{input_json_delta}` + `content_block_stop` | golden fixture | `go test ./internal/adapter/anthropic -run TestSSE_Golden_ToolUse` | ❌ W0 (extends `sse_golden_test.go`) | ⬜ pending |
| V-16 | TBD | TBD | D-07 Ollama streaming wire shape | — | NDJSON final `done:true` line carries `message.tool_calls[]` as plain object | golden | `go test ./internal/adapter/ollama -run TestNDJSON_ToolCalls_DoneLine` | ❌ W0 | ⬜ pending |
| V-17 | TBD | TBD | D-12 round-trip property | — | synth tool_call → adapter render → re-decode preserves name + args | property | `go test ./internal/adapter/{ollama,openai,anthropic} -run TestToolCall_RoundTrip` | ❌ W0 | ⬜ pending |
| V-18 | TBD | TBD | D-16 `[Available tools]` catalog | T-V8-01 | `engine.buildBlocks` emits full JSON tool catalog when `req.Tools` non-empty | unit | `go test ./internal/engine -run TestBuildBlocks_AvailableTools_JSONCatalog` | ❌ W0 (extends `build_acp_test.go`) | ⬜ pending |
| V-19 | TBD | TBD | D-17 scenario 12 (mid-stream cancel) | — | Client disconnect during tool_call → `session/cancel`; slot survives; no leak | e2e + leak | `OTTO_E2E=1 go test -tags e2e ./tests/e2e/... -run TestE2E_Tools_Cancel` | ❌ W0 | ⬜ pending |
| V-20 | TBD | TBD | TRST-07 Example function | — | `Example_CoerceToolCall` runnable godoc | example | `go test ./internal/engine -run Example` | ❌ W0 | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] `internal/engine/coerce.go` — NEW (per D-01)
- [ ] `internal/engine/coerce_test.go` — NEW (property tests per D-12, D-20)
- [ ] `tests/e2e/tools_ollama_test.go` — NEW (D-17 scenarios 1-4, 6-11)
- [ ] `tests/e2e/tools_openai_test.go` — NEW
- [ ] `tests/e2e/tools_anthropic_test.go` — NEW (scenarios 1, 2, 5, 9)
- [ ] `tests/e2e/tools_cancel_test.go` — NEW (scenario 12)
- [ ] `tests/e2e/tools_fixtures.go` — NEW (non-test file; shared 3-tool catalog `get_weather` / `read_file` / `search_web` + fake-kiro script template)
- [ ] Golden fixtures for tool-call wire shapes: extend `internal/adapter/anthropic/sse_golden_test.go`; new `internal/adapter/openai/sse_golden_test.go` tool_call subtests; new `internal/adapter/ollama/ndjson_test.go` tool_call subtests
- [ ] Framework install: **NONE required** — `testing/quick` is stdlib, `goleak` already in `go.mod`

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| `@anthropic-ai/sdk` MessageStream parses tool_use streaming correctly against the live binary | TOOL-01 (Anthropic) | SDK parser conformance — requires the real loop24-client to consume the stream and verify the SDK surfaces a complete `tool_use` block to the application layer | 1. `OTTO_E2E=1 go run ./cmd/otto-gateway` 2. From loop24-client repo: `ANTHROPIC_BASE_URL=http://localhost:11434 npm run smoke:tool-use` 3. Assert SDK emits `content_block_start` → `content_block_delta` → `content_block_stop` events and final `message.content` includes a complete `tool_use` block with object `input` |
| Assumption A1 — Node source byte-level fidelity check on `coerceToolCall` + `pickBestTool` | TOOL-02 | Node source `acp-ollama-server.js` is NOT in this checkout; RESEARCH.md confirmed algorithm against narrative reference only. Per CONTEXT.md line 471 ("Node code wins"), Phase 6 must verify byte-level fidelity before sign-off. | Either: (a) clone the Node repo and diff `coerceToolCall` + `pickBestTool` function bodies against `internal/engine/coerce.go`, OR (b) paste the Node function bodies into the verification ticket and have a human cross-check. Flag any drift in fenced-block regex, scoring algorithm, or tie-break behavior. Planner injects this as a `checkpoint:human-verify` task. |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 10s
- [ ] Manual-only verifications scheduled before phase sign-off
- [ ] `nyquist_compliant: true` set in frontmatter after planner fills `Plan` / `Wave` columns + injects checkpoint task

**Approval:** pending
