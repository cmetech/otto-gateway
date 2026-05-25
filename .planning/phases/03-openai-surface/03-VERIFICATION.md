---
phase: 03-openai-surface
verified: 2026-05-24T21:50:00Z
status: passed
score: 5/5 must-haves verified
overrides_applied: 0
---

# Phase 3: OpenAI Surface — Verification Report

**Phase Goal:** Bring a third adapter online on the same port, sharing the same canonical engine — Pi SDK with `base_url=http://localhost:11434/v1` completes an end-to-end chat against `kiro-cli` and gets back an OpenAI-compatible response. Validates that the adapter-over-canonical layout cleanly supports three surfaces (Ollama + Anthropic + OpenAI).
**Verified:** 2026-05-24T21:50:00Z
**Status:** PASSED
**Re-verification:** No — initial verification

---

## Goal Achievement

### Observable Truths

| # | Truth | Status | Evidence |
|---|-------|--------|----------|
| 1 | `POST /v1/chat/completions` (stream:false) returns an OpenAI-compatible JSON response from the same canonical engine | VERIFIED | `internal/adapter/openai/handlers.go` handleChatCompletions non-streaming path calls `engine.Collect`; `chatResponseToCompletion` returns `{id,object:"chat.completion",choices[{message{role,content},finish_reason}],usage}`; `TestIntegration_FakeEngine_NonStream` PASS; `TestChatCompletions_NonStream` (5 subtests) PASS |
| 2 | Pi-SDK round-trip (stream:false JSON and stream:true SSE) works without SDK modification | VERIFIED | `internal/adapter/openai/sse.go` flat SSE emitter: `data: <chunk>\n\n` frames, role-first delta, finish_reason frame, `data: [DONE]`; `TestIntegration_FakeEngine_Streaming` PASS; `TestSSEGolden` PASS; `TestSSE_RoleFirstDelta`, `TestSSE_DoneTerminator`, `TestSSE_NoEventLines` all PASS; E2E: `TestE2E_OpenAI/ChatCompletions_Streaming` + `TestE2E_OpenAI_SDK_RoundTrip` present in `tests/e2e/openai_e2e_test.go` and `tests/e2e/sdk/openai_roundtrip.mjs`; confirmed passed against live kiro-cli on 2026-05-24 per SUMMARY |
| 3 | `GET /v1/models` and `POST /v1/completions` return OpenAI-compatible shapes; `/v1/models` and `/api/tags` reflect the same model set | VERIFIED | `handleModels` sources from same `*pool.Pool` ModelCatalog as `/api/tags`; `catalogToModelList` always prepends "auto" entry; `TestModels` (2 subtests), `TestModels_ModelCatalogSourcing`, `TestModelListRender` all PASS; `handleCompletions` returns `{object:"text_completion",choices[{text,finish_reason,logprobs:null}],usage}`; `TestCompletions` (8 subtests) PASS; `TestE2E_OpenAI/ModelsMatchTags` E2E verifies same-set identity |
| 4 | `ENABLED_SURFACES` accepts `openai`; default is `ollama,anthropic,openai`; subset omitting `openai` disables the surface; `OPENAI_PATH_PREFIX`/`OLLAMA_PATH_PREFIX` overridable | VERIFIED | `config.go:143` default `[]string{"ollama", "anthropic", "openai"}`; `validateEnabledSurfaces` allow-list includes `"openai"`, error message says "allowed: ollama, anthropic, openai"; `TestLoad_EnabledSurfaces_Default`, `OpenAIOnly`, `AllThree`, `OpenAI_Typo_Errors` all PASS; `main.go` wraps openai adapter with `slices.Contains(cfg.EnabledSurfaces, "openai")` gate; `TestE2E_SurfaceGating_OpenAINotMounted` proves 404 when `ENABLED_SURFACES=ollama`; `OPENAI_PATH_PREFIX` and `OLLAMA_PATH_PREFIX` both read from env via `getEnvStr` and have CLI flag overrides |
| 5 | Architectural boundary check: `adapter/openai`, `adapter/ollama`, `adapter/anthropic` import ONLY `internal/canonical + internal/plugin`; none import `internal/engine` | VERIFIED | `make arch-lint` EXIT 0, "OK - No warnings found"; `.go-arch-lint.yml` registers `adapter_openai: { in: adapter/openai/**, mayDependOn: [canonical] }`; `grep -rn '"otto-gateway/internal/engine"' internal/adapter/ --include="*.go" | grep -v "_test.go"` returns no output; `adapter.go` package doc explicitly states "MUST NOT import internal/engine" |

**Score:** 5/5 truths verified

---

### Required Artifacts

| Artifact | Expected | Status | Details |
|----------|----------|--------|---------|
| `internal/adapter/openai/adapter.go` | Consumer-defined interfaces + RegisterRoutes + Config/New | VERIFIED | Substantive: Engine, RunHandle, Stream, ModelCatalog interfaces; RegisterRoutes registers 3 real handlers; no 501 stubs remaining |
| `internal/adapter/openai/handlers.go` | handleChatCompletions, handleCompletions, handleModels | VERIFIED | All 3 handlers fully implemented; nil-engine guards; body-cap + 413/400 guards; T-02-33 engine error pattern |
| `internal/adapter/openai/wire.go` | wireToChatRequest decode, content poly, system role hoist | VERIFIED | Polymorphic content string/array; developer+system → RoleSystem hoist; MapOpenAIRole; promptToMessages |
| `internal/adapter/openai/render.go` | chatCompletion, catalogToModelList, textCompletion shapes | VERIFIED | All three render shapes substantive; mapFinishReason; genMessageID; joinTextContent |
| `internal/adapter/openai/sse.go` | sseEmitter, runSSEEmitter, finalizeSSE | VERIFIED | flat data-only framing; role-first delta; finish_reason frame; [DONE] terminator; ctx.Done path |
| `internal/adapter/openai/errors.go` | OpenAI error envelope {error:{message,type,param,code}} | VERIFIED | writeError, writeJSON; error type constants; Content-Type before WriteHeader |
| `internal/adapter/openai/testmain_test.go` | goleak.VerifyTestMain gate | VERIFIED | `goleak.VerifyTestMain(m)` installed |
| `internal/server/server.go` | SurfaceMount + RouteRegistrar + NewFromConfig grouped prefix | VERIFIED | SurfaceMount struct; RouteRegistrar interface; NewFromConfig groups by prefix, one auth-wrapped r.Route per prefix |
| `internal/config/config.go` | ENABLED_SURFACES default = [ollama,anthropic,openai]; openai in allow-list | VERIFIED | Line 143 default; validateEnabledSurfaces includes "openai" key |
| `cmd/otto-gateway/main.go` | openaiEngineAdapter + SurfaceMount wiring + ENABLED_SURFACES gating | VERIFIED | openaiEngineAdapter + openaiRunHandleAdapter; slices.Contains gate; openai_mounted boot log |
| `.go-arch-lint.yml` | adapter_openai component + mayDependOn: [canonical] | VERIFIED | Lines 33-34,66-69 define adapter_openai component and boundary |
| `tests/e2e/openai_e2e_test.go` | E2E covering SC1+SC2+SC3+SC4 with real binary | VERIFIED | TestE2E_OpenAI with 6 subtests + TestE2E_SurfaceGating_OpenAINotMounted |
| `tests/e2e/sdk/openai_roundtrip.mjs` | Official openai npm SDK round-trip harness | VERIFIED | Drives non-stream + stream against gateway; exits 0 on success |

---

### Key Link Verification

| From | To | Via | Status | Details |
|------|----|-----|--------|---------|
| `openai.handleChatCompletions` | canonical engine | `engine.Run` (stream) / `engine.Collect` (non-stream) | WIRED | handlers.go:51-79 and handlers.go:71-79; `a.cfg.Engine.Run` / `a.cfg.Engine.Collect` called on every request |
| `openai.handleModels` | pool ModelCatalog | `a.cfg.ModelCatalog.Models()` | WIRED | handlers.go:156-159; same `*pool.Pool` as ollama's `/api/tags` |
| `cmd/main.go openaiEngineAdapter` | `*engine.Engine` | `engine.Collect` + `engine.Run` pass-through | WIRED | main.go:397-410; wraps concrete *engine.Engine returns to openai.Engine interface |
| `server.NewFromConfig` | OpenAI + Anthropic co-mount on `/v1` | `SurfaceMount` grouped prefix | WIRED | server.go:180-208; sorts prefixes, groups by prefix, opens one r.Route block |
| `ENABLED_SURFACES=ollama` | OpenAI routes 404 | `slices.Contains` gate in main.go | WIRED | main.go:208; if gate fails, openaiAdapter stays nil, not appended to surfaces list |
| `internal/adapter/openai` | `internal/canonical` only | consumer-defined local interfaces | WIRED | arch-lint passes; no engine import in non-test files |

---

### Data-Flow Trace (Level 4)

| Artifact | Data Variable | Source | Produces Real Data | Status |
|----------|---------------|--------|--------------------|--------|
| `handleChatCompletions` (non-stream) | `resp *canonical.ChatResponse` | `engine.Collect` → `*engine.Engine.Collect` → `pool.Acquire` → ACP `session/prompt` | Yes — ACP yields real kiro-cli chunks collected into response | FLOWING |
| `handleChatCompletions` (stream) | `<-chan canonical.Chunk` via `run.Stream().Chunks()` | `engine.Run` → ACP streaming `session/update` notifications | Yes — channel receives live kiro-cli chunks | FLOWING |
| `handleModels` | `[]canonical.ModelInfo` | `pool.Models()` → captured from kiro-cli `session/new` response `availableModels` | Yes — real models from kiro-cli | FLOWING |
| `handleCompletions` | `resp *canonical.ChatResponse` | `engine.Collect` (same as chat completions non-stream path) | Yes — same engine path | FLOWING |

---

### Behavioral Spot-Checks

| Behavior | Command | Result | Status |
|----------|---------|--------|--------|
| `go build ./...` compiles cleanly | `go build ./...` | Exit 0, no output | PASS |
| Unit + goleak tests pass in openai adapter | `go test -race -count=1 ./internal/adapter/openai/...` | `ok otto-gateway/internal/adapter/openai 2.703s` | PASS |
| TestSurfaceMount co-mount (D-01) | `go test -v -run TestSurfaceMount ./internal/server/...` | PASS — 5 routes returned 401 (not 404) behind auth | PASS |
| ENABLED_SURFACES config tests | `go test -v -run TestLoad_EnabledSurfaces ./internal/config/...` | 7/7 subtests PASS | PASS |
| make ci (lint+race+arch-lint+govulncheck) | `make ci` | Exit 0; "0 issues", "OK - No warnings found", "No vulnerabilities found" | PASS |

---

### Probe Execution

No probe scripts declared for this phase. `tests/e2e/openai_e2e_test.go` is the automated E2E contract but is gated on `OTTO_E2E=1` (requires live kiro-cli). The VALIDATION.md records it as passing on 2026-05-24. Unit and integration tests (fake engine) exercise all code paths verifiably without the live subprocess.

---

### Requirements Coverage

| Requirement | Source Plan | Description | Status | Evidence |
|-------------|-------------|-------------|--------|----------|
| SURF-02 | 03-01, 03-04 | `ENABLED_SURFACES` enables/disables surfaces at deploy time; path prefixes overridable | SATISFIED | Default includes openai; validate allow-list; slices.Contains gate in main.go; prefix env vars |
| SURF-04 | 03-02, 03-03 | `POST /v1/chat/completions`, `POST /v1/completions`, `GET /v1/models` with OpenAI-compatible shapes | SATISFIED | All 3 handlers implemented and tested; shapes verified by unit tests and E2E |
| SURF-06 | 03-02, 03-04 | Pi-SDK chat CLI with `base_url=…/v1` works end-to-end | SATISFIED | SSE emitter per D-02; `TestE2E_OpenAI/ChatCompletions_Streaming` + `TestE2E_OpenAI_SDK_RoundTrip`; official openai npm SDK harness in `tests/e2e/sdk/openai_roundtrip.mjs` |

---

### Anti-Patterns Found

| File | Line | Pattern | Severity | Impact |
|------|------|---------|----------|--------|
| `internal/adapter/openai/adapter.go` | 16 | Doc comment referencing old stub behavior | Info | Historical doc comment only; no active 501 returns; `grep -n "501\|http.StatusNotImplemented" handlers.go adapter.go` confirms only in comment |

No blockers. No TODO/FIXME/XXX debt markers in any Phase 3 source file. No unreferenced stubs.

---

### Human Verification Required

None. All Phase 3 success criteria are verified by automated means:

- SC1 (non-streaming JSON) — unit tests + E2E `TestE2E_OpenAI/ChatCompletions_NonStreaming`
- SC2 (Pi-SDK round-trip, both modes) — `TestE2E_OpenAI/ChatCompletions_Streaming` + `TestE2E_OpenAI_SDK_RoundTrip` via official npm `openai` SDK
- SC3 (models/completions shapes) — unit tests + `TestE2E_OpenAI/ModelsMatchTags`
- SC4 (ENABLED_SURFACES gating) — config unit tests + `TestE2E_SurfaceGating_OpenAINotMounted`
- SC5 (arch boundary) — `make arch-lint` EXIT 0

The original Pi-SDK HUMAN-UAT (SC2/SURF-06) was explicitly converted to automated E2E coverage in Plan 04, confirmed passed against live kiro-cli on 2026-05-24.

---

### Gaps Summary

No gaps. All 5 ROADMAP success criteria are satisfied by substantive, wired, data-flowing implementations with passing test coverage.

One note on SC5 wording: the ROADMAP says adapters "import ONLY `internal/canonical` + `internal/plugin`." The `internal/plugin` package is an empty `.gitkeep` seam at this phase — no adapter imports it because it has no symbols yet. This is not a failure; it is the designed forward-seam from Phase 8. The arch-lint rule correctly constrains adapters to `mayDependOn: [canonical]`; plugin will be added to the `mayDependOn` list when Phase 8 populates it. No gap.

---

_Verified: 2026-05-24T21:50:00Z_
_Verifier: Claude (gsd-verifier)_
