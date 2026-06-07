---
phase: 3
slug: openai-surface
status: complete
nyquist_compliant: true
wave_0_complete: true
created: 2026-05-24
---

# Phase 3 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.
> Derived from `03-RESEARCH.md` § Validation Architecture.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | Go stdlib `testing` + `net/http/httptest` + `go.uber.org/goleak` v1.3.0 |
| **Config file** | none — Go convention; `Makefile` targets `test`, `test-race`, `arch-lint`, `ci`, `e2e` |
| **Quick run command** | `go test ./internal/adapter/openai/...` |
| **Full suite command** | `make ci` (lint + `go test -race ./...` + govulncheck + arch-lint) |
| **Estimated runtime** | quick ~3s · full `make ci` ~60–90s |

---

## Sampling Rate

- **After every task commit:** `go test ./internal/adapter/openai/...` (fast; fakes only)
- **After every plan wave:** `go test -race ./internal/adapter/openai/... ./internal/server/... ./internal/config/...`
- **Before `/gsd:verify-work`:** `make ci` green (incl. arch-lint with the new `adapter_openai` boundary), then Pi HUMAN-UAT
- **Max feedback latency:** ~5 seconds (quick) / ~90 seconds (full)

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 03-01-1 | 03-01 | 1 | SURF-02 | T-03-02 | ENABLED_SURFACES default includes openai; validateEnabledSurfaces accepts it; unknown surfaces fail fast at boot | unit | `go test ./internal/config -run 'EnabledSurfaces' -timeout 30s` | ✅ internal/config/config_test.go | ✅ green |
| 03-01-2 | 03-01 | 1 | SURF-02 / D-01 | T-03-01 / T-03-03 | Two /v1 adapters co-mount under one auth-wrapped prefix block without chi panic; all routes resolve (not 404) | unit | `go test ./internal/server -run TestSurfaceMount -timeout 30s` | ✅ internal/server/server_test.go | ✅ green |
| 03-01-3a | 03-01 | 1 | SURF-06 | T-03-12 | goleak.VerifyTestMain leak gate installed for openai package (TDD RED) | goleak | `go test ./internal/adapter/openai/... -timeout 30s` | ✅ internal/adapter/openai/testmain_test.go | ✅ green |
| 03-01-3b | 03-01 | 1 | TRST-04 | T-03-04 | internal/adapter/openai compiles; RegisterRoutes wires 3 routes; arch-lint enforces canonical-only imports (TDD GREEN) | arch | `go build ./internal/adapter/openai/... && make arch-lint` | ✅ internal/adapter/openai/adapter.go + .go-arch-lint.yml | ✅ green |
| 03-02-1 | 03-02 | 2 | SURF-04 | T-03-11 | content string-or-array decode; system AND developer role hoist; error envelope OpenAI shape {error:{message,type,param,code}}; finish_reason non-null mapped string (TDD) | unit | `go test ./internal/adapter/openai -run 'TestWire\|TestErrors\|TestChatCompletions_NonStream' -timeout 30s` | ✅ wire_test.go / render_test.go / errors_test.go | ✅ green |
| 03-02-2 | 03-02 | 2 | SURF-04 / STRM-02 | T-03-12 / T-03-13 | SSE emitter: role delta → content deltas → finish_reason frame → data: [DONE]; no event: lines; no ticker; Flusher before WriteHeader; ctx-cancel returns no [DONE] (TDD) | golden | `go test ./internal/adapter/openai -run 'TestSSEGolden\|TestSSE' -timeout 30s` | ✅ sse.go / sse_golden_test.go / sse_test.go / testdata/sse_text_only.golden | ✅ green |
| 03-02-3 | 03-02 | 2 | SURF-04 / SURF-06 | T-03-10 / T-03-11 / T-03-12 | handleChatCompletions: nil-engine→503; oversize→413; empty-messages→400; SC1 stream:false → 200 + chat.completion; SC2 stream:true → 200 + text/event-stream + [DONE]; leak-free (TDD) | integration | `go test ./internal/adapter/openai/... -timeout 60s && go test -race ./internal/adapter/openai/... -timeout 60s` | ✅ handlers.go / integration_test.go | ✅ green |
| 03-03-1 | 03-03 | 3 | SURF-04 | T-03-22 | GET /v1/models returns {object:"list", data:[{id,object:"model",created,owned_by}]}; "auto" prepended; nil catalog → only "auto"; same source as /api/tags (TDD) | unit | `go test ./internal/adapter/openai -run 'TestModels\|TestModelListRender' -timeout 30s` | ✅ models_test.go / render_test.go | ✅ green |
| 03-03-2 | 03-03 | 3 | SURF-04 | T-03-20 / T-03-21 / T-03-23 | POST /v1/completions: string/array prompt → text_completion; advanced params accepted-and-ignored; stream:true silently downgraded to JSON; nil-engine→503; engine error→generic 500 (never echo err.Error()) (TDD) | unit | `go test ./internal/adapter/openai -run 'TestCompletions\|TestTextCompletionRender\|TestPromptDecode' -timeout 30s` | ✅ completions_test.go / render_test.go | ✅ green |
| 03-04-1 | 03-04 | 4 | SURF-02 / SURF-04 | T-03-30 | main.go: openaiEngineAdapter + SurfaceMount list + slices.Contains gate + openai_mounted boot log; whole binary compiles; existing Ollama/Anthropic integration tests pass through migrated RegisterRoutes path | integration | `go build ./... && go test ./internal/server/... ./internal/adapter/... -timeout 60s` | ✅ cmd/otto-gateway/main.go | ✅ green |
| 03-04-2 | 03-04 | 4 | SURF-06 / TRST-04 | T-03-32 / T-03-33 | Real-kiro integration: stream + non-stream skip cleanly when OTTO_INTEGRATION unset; make ci green (lint 0, race, arch-lint SC5, govulncheck) | race / arch | `go test ./internal/adapter/openai/... -timeout 60s && make arch-lint` | ✅ integration_test.go | ✅ green |
| 03-04-3 | 03-04 | 4 | SURF-06 | T-03-31 | Pi-SDK HUMAN-UAT: streamed round-trip with zero SDK modification (SC2). Automated substitution: TestE2E_OpenAI/ChatCompletions_Streaming in tests/e2e/ verified against live kiro-cli 2026-05-24. | manual-only | Wave 0 fixture: `make e2e RUN=TestE2E_OpenAI` (gated on OTTO_E2E=1); manual-only rationale: requires live Pi-SDK client + kiro-cli subprocess. SC2 subsequently automated via E2E suite — no operator sign-off required for CI. | ✅ tests/e2e/openai_e2e_test.go | ✅ green |
| 03-W0-leak | 03-VALIDATION.md | 1 | SURF-06 | T-03-12 | goleak.VerifyTestMain covers entire openai package goroutine leak gate | goleak | `go test ./internal/adapter/openai/... -timeout 60s` | ✅ testmain_test.go | ✅ green |
| 03-W0-race | 03-VALIDATION.md | 4 | TRST-03 | — | All adapter concurrency race-clean | race | `go test -race ./internal/adapter/openai/... -timeout 60s` | ✅ (all test files) | ✅ green |
| 03-W0-arch | 03-VALIDATION.md | 1 | TRST-04 | T-03-04 | adapter_openai depends only on canonical (+vendor); never imports engine | arch | `make arch-lint` | ✅ .go-arch-lint.yml adapter_openai entry | ✅ green |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [x] `internal/adapter/openai/testmain_test.go` — `goleak.VerifyTestMain` (copied from `anthropic/testmain_test.go`) — covers SURF-06 leak gate (installed in 03-01-3a)
- [x] `internal/adapter/openai/testdata/sse_text_only.golden` — SSE byte-exact fixture (role delta → content deltas → finish_reason chunk → `[DONE]` terminator) — covers STRM-02 front-run (installed in 03-02-2)
- [x] `internal/adapter/openai/sse_golden_test.go` — `compareGolden` + `driveGolden` harness with `chatcmpl-` id + timestamp normalization (installed in 03-02-2)
- [x] `internal/adapter/openai/integration_test.go` — full `r.Route`/httptest round-trip incl. `Content-Type: text/event-stream` assertion + `bufio.Scanner` over frames (installed in 03-02-3; real-kiro path added in 03-04-2)
- [x] `internal/server/server_test.go` — TestSurfaceMount proving two `/v1` surfaces co-mount without panic (D-01 regression pin) (installed in 03-01-2)
- [x] `.go-arch-lint.yml` — `adapter_openai` component + `mayDependOn: [canonical]` (installed in 03-01-3b)
- [x] `internal/config/config_test.go` — TestLoad_EnabledSurfaces_Default updated to expect `["ollama","anthropic","openai"]`; TestLoad_EnabledSurfaces_OpenAIOnly + AllThree + OpenAI_Typo_Errors added (installed in 03-01-1)

---

## Manual-Only Verifications

None — the Pi-SDK round-trip (SURF-06 / SC2) is now covered by automated E2E
tests (see below), so there are no manual-only verifications for this phase.

### Automated coverage of the former HUMAN-UAT (SC2 / SURF-06)

The Pi round-trip was originally scoped as a manual HUMAN-UAT. It is now proven
by two automated layers in `tests/e2e/` (real binary + real `kiro-cli`, gated by
`OTTO_E2E=1`, skips cleanly when `kiro-cli` is unavailable):

| Behavior | Requirement | Test | Command |
|----------|-------------|------|---------|
| `/v1/chat/completions` non-stream → `chat.completion` (real kiro) | SURF-04 / SC1 | `TestE2E_OpenAI/ChatCompletions_NonStreaming` | `make e2e RUN=TestE2E_OpenAI` |
| `/v1/chat/completions` **stream:true** → well-formed `chat.completion.chunk` frames + `[DONE]` (real kiro; the Pi path) | SURF-06 / SC2 | `TestE2E_OpenAI/ChatCompletions_Streaming` | `make e2e RUN=TestE2E_OpenAI` |
| `/v1/models` set == `/api/tags` set | SURF-04 / SC3 | `TestE2E_OpenAI/ModelsMatchTags` | `make e2e RUN=TestE2E_OpenAI` |
| `/v1/completions` → `text_completion`, advanced params ignored | SURF-04 | `TestE2E_OpenAI/Completions_NonStreaming` | `make e2e RUN=TestE2E_OpenAI` |
| OpenAI routes 404 when `ENABLED_SURFACES` omits `openai` | SURF-02 / SC4 | `TestE2E_SurfaceGating_OpenAINotMounted` | `make e2e RUN=TestE2E_SurfaceGating_OpenAINotMounted` |
| **Official `openai` SDK** non-stream + stream round-trip (the exact SDK Pi wraps) | SURF-06 / SC2 | `TestE2E_OpenAI_SDK_RoundTrip` (opt-in; `make e2e-sdk-setup` then `OTTO_E2E_SDK=1`) | `make e2e RUN=TestE2E_OpenAI_SDK_RoundTrip` |

All passed against live `kiro-cli` on 2026-05-24 (streamed reply: "Hi! How can I help you?").

---

## Validation Sign-Off

- [x] All tasks have `<automated>` verify or Wave 0 dependencies
- [x] Sampling continuity: no 3 consecutive tasks without automated verify
- [x] Wave 0 covers all MISSING references
- [x] No watch-mode flags
- [x] Feedback latency < 90s
- [x] `nyquist_compliant: true` set in frontmatter

**Approval:** approved 2026-06-07
