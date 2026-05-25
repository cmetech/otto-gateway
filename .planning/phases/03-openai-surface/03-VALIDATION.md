---
phase: 3
slug: openai-surface
status: draft
nyquist_compliant: false
wave_0_complete: false
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

> Task IDs (`{N}-PP-TT`) are assigned by the planner. Requirement → test-type → command
> mapping below is locked from research; the planner maps each row to concrete task IDs and
> embeds the `<automated>` command per task.

| Behavior | Requirement | Test Type | Automated Command | File Exists |
|----------|-------------|-----------|-------------------|-------------|
| `POST /v1/chat/completions` `stream:false` → `chat.completion` JSON envelope (id/object/choices/usage) | SURF-04 | unit | `go test ./internal/adapter/openai -run TestChatCompletions_NonStream` | ❌ W0 |
| `stream:true` → `data: {chunk}\n\n` frames, role-first delta, finish_reason chunk, `data: [DONE]\n\n` | SURF-04 / STRM-02 | golden | `go test ./internal/adapter/openai -run TestSSEGolden` | ❌ W0 |
| `GET /v1/models` → `{object:"list",data:[{id,object:"model",created,owned_by}]}`; "auto" present; matches `/api/tags` set | SURF-04 | unit | `go test ./internal/adapter/openai -run TestModels` | ❌ W0 |
| `POST /v1/completions` → `{object:"text_completion",choices[].text,usage zeros}`; advanced params ignored | SURF-04 | unit | `go test ./internal/adapter/openai -run TestCompletions` | ❌ W0 |
| Error envelope `{"error":{message,type,param,code}}` + status map (400/401/404/413/500) | SURF-04 | unit | `go test ./internal/adapter/openai -run TestErrors` | ❌ W0 |
| content string-or-array decode; system/developer role hoist; `model:"auto"` skips SetModel | SURF-04 | unit / property | `go test ./internal/adapter/openai -run TestWire` | ❌ W0 |
| `ENABLED_SURFACES` default includes openai; `validateEnabledSurfaces` accepts "openai"; unknown still fails fast | SURF-02 | unit | `go test ./internal/config -run 'EnabledSurfaces'` | ⚠️ extend existing |
| Both `/v1` surfaces mount without panic; `/v1/messages` AND `/v1/chat/completions` both routable | SURF-02 / D-01 | unit | `go test ./internal/server -run TestSurfaceMount` | ❌ W0 |
| SSE handler leak-free; ctx-cancel returns cleanly | SURF-06 | goleak | `go test ./internal/adapter/openai` (TestMain goleak gate) | ❌ W0 |
| `internal/adapter/openai` imports only canonical (+plugin); none import engine | TRST-04 | arch | `make arch-lint` | ⚠️ add `adapter_openai` |
| All concurrency race-clean | TRST-03 | race | `go test -race ./internal/adapter/openai/...` | ❌ W0 |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

> Note: the `ENABLED_SURFACES` row uses `-run 'EnabledSurfaces'` (matches the real
> `TestLoad_EnabledSurfaces_*` functions). `-run TestEnabledSurfaces` matches ZERO
> tests and exits 0 vacuously — do not use it.

---

## Wave 0 Requirements

- [ ] `internal/adapter/openai/testmain_test.go` — `goleak.VerifyTestMain` (copy `anthropic/testmain_test.go`) — covers SURF-06 leak gate
- [ ] `internal/adapter/openai/testdata/*.golden` — SSE byte-exact fixtures (text-only stream; finish_reason chunk; `[DONE]` terminator) — covers STRM-02 front-run
- [ ] `internal/adapter/openai/sse_golden_test.go` — `compareGolden` + `driveGolden` harness with `chatcmpl-…` id normalization (copy + adapt `anthropic/sse_golden_test.go`)
- [ ] `internal/adapter/openai/integration_test.go` — full `r.Route`/httptest round-trip incl. `Content-Type: text/event-stream` assertion + `bufio.Scanner` over frames
- [ ] `internal/server/server_test.go` — SurfaceMount-grouping test proving two `/v1` surfaces co-mount without panic (NEW — D-01 regression pin)
- [ ] `.go-arch-lint.yml` — add `adapter_openai` component + `mayDependOn: [canonical]` (mirror `adapter_anthropic`)
- [ ] Extend `internal/config/config_test.go` — assert default slice includes "openai" and allow-list accepts it (D-05); also update the existing `TestLoad_EnabledSurfaces_Default` expectation to `["ollama","anthropic","openai"]`
- [ ] Framework install: none — all tooling present.

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

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 90s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
</content>
