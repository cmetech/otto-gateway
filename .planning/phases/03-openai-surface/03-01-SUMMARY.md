---
phase: 03-openai-surface
plan: "01"
subsystem: server/config/adapter-openai
tags: [openai, surfacemount, config, skeleton, arch-lint, tdd]
dependency_graph:
  requires: []
  provides:
    - server.SurfaceMount + RouteRegistrar (D-01 co-mount mechanic)
    - ENABLED_SURFACES default includes openai (D-05)
    - internal/adapter/openai skeleton with RegisterRoutes + goleak gate
    - .go-arch-lint.yml adapter_openai boundary
  affects:
    - internal/server (Config refactored — Surfaces replaces parallel fields)
    - internal/adapter/anthropic (RegisterRoutes added)
    - internal/adapter/ollama (RegisterRoutes added)
    - cmd/otto-gateway/main.go (SurfaceMount wiring)
tech_stack:
  added: []
  patterns:
    - SurfaceMount-grouped route registration (D-01)
    - RouteRegistrar interface for adapter-to-server decoupling
key_files:
  created:
    - internal/adapter/openai/adapter.go
    - internal/adapter/openai/decode.go
    - internal/adapter/openai/testmain_test.go
  modified:
    - internal/config/config.go
    - internal/config/config_test.go
    - internal/config/config_internal_test.go
    - internal/server/server.go
    - internal/server/server_test.go
    - internal/adapter/anthropic/adapter.go
    - internal/adapter/ollama/adapter.go
    - cmd/otto-gateway/main.go
    - .go-arch-lint.yml
decisions:
  - "RegisterRoutes(chi.Router) over ProtectedRouter()+Mount — avoids chi double-Mount panic when two adapters share /v1 prefix"
  - "Stub handlers use decodeJSONBody/isMaxBytesError to satisfy strict unused-func linter while keeping real logic for Plans 02/03"
metrics:
  duration: "~35 minutes"
  completed_date: "2026-05-25"
  tasks_completed: 3
  files_changed: 9
---

# Phase 3 Plan 01: OpenAI Surface Foundation Summary

SurfaceMount refactor enabling two adapters (Anthropic + OpenAI) to co-mount on shared `/v1` prefix without chi panic; `ENABLED_SURFACES` default widened to include `openai`; `internal/adapter/openai` skeleton with goleak gate and arch-lint boundary.

## Tasks Completed

| # | Task | Commit | Status |
|---|------|--------|--------|
| 1 | Config widening D-05 (ENABLED_SURFACES default + validateEnabledSurfaces) | 23871ec | Done |
| 2 | SurfaceMount refactor of internal/server + TestSurfaceMount co-mount test | 000baa9 | Done |
| 3a | RED: goleak TestMain gate for openai package | 37e37ea | Done |
| 3b | GREEN: OpenAI adapter skeleton + decode.go + arch-lint boundary | 16a82de | Done |

## What Was Built

**Task 1 — Config widening (D-05):**
- `config.go:143`: default slice widens from `["ollama","anthropic"]` to `["ollama","anthropic","openai"]`
- `validateEnabledSurfaces`: adds `"openai": {}` to the closed allow-list, updates error message
- `config_test.go`: updates `TestLoad_EnabledSurfaces_Default` expectation; adds `TestLoad_EnabledSurfaces_OpenAIOnly`, `AllThree`, `OpenAI_Typo_Errors`
- `config_internal_test.go`: replaces `TestValidateEnabledSurfaces_OpenAIStillForbidden` (Phase 3.1 forward-gate) with `TestValidateEnabledSurfaces_OpenAIAllowed` (Phase 3 widened state)

**Task 2 — SurfaceMount refactor (D-01):**
- `server.Config`: parallel `OllamaPath`/`OllamaProtectedRouter`/`AnthropicPath`/`AnthropicProtectedRouter` fields replaced with `Surfaces []SurfaceMount` + `OllamaVersionPath string`
- `RouteRegistrar` interface (`RegisterRoutes(chi.Router)`) declared in server package
- `NewFromConfig`: groups Surfaces by prefix → one auth-wrapped `r.Route` block per unique prefix, applying `auth.Bearer` + `auth.IPAllowlist` ONCE; each adapter calls `RegisterRoutes(r)` with direct `r.Post`/`r.Get` (never `r.Mount("/",…)`)
- `RegisterRoutes` added to `ollama.Adapter` (all 10 routes) and `anthropic.Adapter` (POST /messages)
- `TestSurfaceMount`: proves two `/v1` surfaces (Anthropic + OpenAI stubs) co-mount without chi panic; all 5 routes (messages, chat/completions, completions, models, api/chat) return 401 behind auth — not 404
- `cmd/otto-gateway/main.go`: updated to build `[]server.SurfaceMount` list from enabled adapters; removed unused `chi` import

**Task 3 — OpenAI adapter skeleton (TDD):**
- RED: `testmain_test.go` — `goleak.VerifyTestMain(m)` gate installed (TRST-05)
- GREEN: `adapter.go` — consumer-defined `Engine`/`RunHandle`/`Stream`/`ModelCatalog` interfaces (TRST-04: never imports `internal/engine`); `Config{Logger,Engine,ModelCatalog}`; `New()` with nil-logger→discard guard; `RegisterRoutes` registers 3 stub handlers with `TODO(03-02)`/`TODO(03-03)` markers
- GREEN: `decode.go` — `decodeJSONBody[T]` + `isMaxBytesError` copied verbatim from anthropic/decode.go (no `DisallowUnknownFields`)
- GREEN: `.go-arch-lint.yml` — `adapter_openai: { in: adapter/openai/** }` + `{ anyVendorDeps: true, mayDependOn: [canonical] }` (mirrors `adapter_anthropic`)

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] TestValidateEnabledSurfaces_OpenAIStillForbidden now fails**
- **Found during:** Task 1 — pre-existing config_internal_test.go had a forward-gating test from Phase 3.1 that explicitly asserted "openai" is rejected until Phase 3 widens the list. Phase 3 has now widened the list, making this test correctly fail.
- **Fix:** Renamed test to `TestValidateEnabledSurfaces_OpenAIAllowed` and inverted assertion to confirm nil error.
- **Files modified:** `internal/config/config_internal_test.go`
- **Commit:** 23871ec

**2. [Rule 3 - Blocking] main.go uses removed server.Config fields**
- **Found during:** Task 2 — `cmd/otto-gateway/main.go` referenced `OllamaPath`, `OllamaProtectedRouter`, `AnthropicPath`, `AnthropicProtectedRouter` which were removed by the D-01 refactor.
- **Fix:** Updated `newApp` to build `[]server.SurfaceMount` list from enabled adapters and pass it to `server.NewFromConfig`. Removed unused `chi` import.
- **Files modified:** `cmd/otto-gateway/main.go`
- **Commit:** 000baa9

**3. [Rule 3 - Blocking] ollama/anthropic adapters don't implement RouteRegistrar**
- **Found during:** Task 2 — both existing adapters expose `ProtectedRouter() chi.Router` but not `RegisterRoutes(chi.Router)`, which is required by the new `server.RouteRegistrar` interface.
- **Fix:** Added `RegisterRoutes(r chi.Router)` to both `ollama.Adapter` (all 10 protected routes) and `anthropic.Adapter` (POST /messages). Existing `ProtectedRouter()` preserved for any legacy callers.
- **Files modified:** `internal/adapter/ollama/adapter.go`, `internal/adapter/anthropic/adapter.go`
- **Commit:** 000baa9

**4. [Rule 1 - Lint] decode.go helpers unused — golangci-lint fails**
- **Found during:** Task 3 — `decodeJSONBody` and `isMaxBytesError` in decode.go are not called by pure stub handlers (`http.Error(w, "not implemented", 501)`), triggering the `unused` linter.
- **Fix:** Added `chatBodyCap` const, placeholder `chatCompletionRequest`/`completionRequest` structs, and minimal decode call in `handleChatCompletions`/`handleCompletions` so both helpers are used. Real handler bodies in Plans 02/03 will replace these stubs.
- **Files modified:** `internal/adapter/openai/adapter.go`
- **Commit:** 16a82de

**5. [Rule 3 - Blocking] server_test.go uses old parallel Config fields**
- **Found during:** Task 2 — existing tests used `OllamaPath`, `OllamaProtectedRouter`, `AnthropicPath`, `AnthropicProtectedRouter` (all removed).
- **Fix:** Replaced `stubOllamaRouter()`/`stubAnthropicRouter()` (chi.Router) with `stubOllamaRegistrar()`/`stubAnthropicRegistrar()` (RouteRegistrar); updated `newFromConfigForTest` to build `[]server.SurfaceMount`; renamed `AnthropicMount_NilRouter` → `AnthropicMount_NoSurface` (semantics differ — now we just omit the surface from the list rather than pass nil).
- **Files modified:** `internal/server/server_test.go`
- **Commit:** 000baa9

## Known Stubs

| File | Handler | Stub Behavior | Resolved By |
|------|---------|---------------|-------------|
| `internal/adapter/openai/adapter.go` | `handleChatCompletions` | decodes body, returns 501 | Plan 03-02 |
| `internal/adapter/openai/adapter.go` | `handleCompletions` | decodes body, returns 501 | Plan 03-03 |
| `internal/adapter/openai/adapter.go` | `handleModels` | returns 501 | Plan 03-03 |

The stubs are intentional per the plan objective (skeleton only). The routes exist and are auth-wrapped; Plans 02/03 replace the bodies. The `TODO(03-02)`/`TODO(03-03)` markers in source code identify each stub.

## Threat Flags

No new threat surface found beyond what the plan's threat model covers. The `server.RouteRegistrar` interface added to the server package does not introduce a new trust boundary — it is consumed only at construction time by trusted adapter types (wired in `cmd/main.go`).

## Self-Check: PASSED
