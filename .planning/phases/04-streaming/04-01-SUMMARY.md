---
phase: "04-streaming"
plan: "01"
subsystem: "engine + adapters + main.go"
tags: ["watchdog", "streaming", "context-cancel", "spurious-cancel", "interface-extension", "shim"]
dependency_graph:
  requires: []
  provides:
    - "engine.Run AfterFunc watchdog (D-06)"
    - "engine.Run.StopWatchdog() accessor"
    - "engine.Collect natural-completion teardown"
    - "ollama.Engine.Run + RunHandle + Stream interfaces"
    - "openai.RunHandle.StopWatchdog extension"
    - "anthropic.RunHandle.StopWatchdog extension"
    - "main.go ollamaEngineAdapter + ollamaRunHandleAdapter"
    - "finalizeSSE + finalizeStream stop() call sites"
    - "D-07 derived context in OpenAI and Anthropic streaming branches"
  affects:
    - "internal/engine/engine.go"
    - "internal/engine/collect.go"
    - "internal/adapter/ollama/wire.go"
    - "internal/adapter/ollama/adapter.go"
    - "internal/adapter/openai/adapter.go"
    - "internal/adapter/openai/sse.go"
    - "internal/adapter/openai/handlers.go"
    - "internal/adapter/anthropic/adapter.go"
    - "internal/adapter/anthropic/sse.go"
    - "internal/adapter/anthropic/handlers.go"
    - "cmd/otto-gateway/main.go"
tech_stack:
  added: []
  patterns:
    - "context.AfterFunc watchdog pattern (Go 1.21+) for session/cancel on ctx termination"
    - "*bool JSON fields for absent=default (Ollama stream field Node parity)"
    - "cmd-level shim adapters for Go return-type-invariance (ollamaEngineAdapter)"
    - "context.WithCancel derived ctx (D-07) for write-error cancellation signal"
key_files:
  created: []
  modified:
    - "internal/engine/engine.go — stopWatchdog field + StopWatchdog() accessor + AfterFunc watchdog in Run()"
    - "internal/engine/collect.go — stop() call on natural completion path"
    - "internal/adapter/ollama/wire.go — *bool Stream fields + streamEnabled() helper"
    - "internal/adapter/ollama/adapter.go — Engine.Run + RunHandle + Stream interfaces"
    - "internal/adapter/ollama/handlers.go — Rule 3: update silent-downgrade for *bool Stream"
    - "internal/adapter/openai/adapter.go — StopWatchdog() in RunHandle interface"
    - "internal/adapter/openai/sse.go — stop() in finalizeSSE success path"
    - "internal/adapter/openai/handlers.go — D-07 derived ctx in streaming branch"
    - "internal/adapter/anthropic/adapter.go — StopWatchdog() in RunHandle interface"
    - "internal/adapter/anthropic/sse.go — stop() in finalizeStream success path (after rerr block)"
    - "internal/adapter/anthropic/handlers.go — D-07 derived ctx in streaming branch"
    - "cmd/otto-gateway/main.go — ollamaEngineAdapter + ollamaRunHandleAdapter + StopWatchdog on all three *RunHandleAdapter structs"
decisions:
  - "stop() placed AFTER if rerr != nil block in anthropic/sse.go finalizeStream — error path must NOT call stop() so watchdog fires Cancel on stream truncation/error (Anthropic spec mandates event:error but watchdog must still fire)"
  - "streamEnabled(*bool) treats nil as true (absent = stream:true) per Node reference Ollama behavior"
  - "ollama integration_test.go uses local testEngineAdapter shim (Rule 3) since whitebox test cannot use cmd-level ollamaEngineAdapter"
  - "fakeRunHandle.StopWatchdog() returns nil in all test fakes (no watchdog in unit tests — no ACP session)"
metrics:
  duration: "~45 minutes"
  completed_date: "2026-05-25"
  tasks: 2
  files_modified: 14
---

# Phase 04 Plan 01: Streaming Foundation — Watchdog + RunHandle Extensions + Shims Summary

Load-bearing foundation for Phase 4 streaming: AfterFunc watchdog teardown wiring across all three API surfaces and the engine Collect path, with complete main.go shim coverage and interface extensions in one atomic wave.

## What Was Built

### Task 1: Engine watchdog + StopWatchdog + Collect teardown + ollama *bool

**engine.go changes:**
- Added `stopWatchdog func() bool` field to `Run` struct
- Added `StopWatchdog() func() bool` accessor on `*Run` (returns nil for PreHook short-circuit runs)
- Registered `context.AfterFunc` watchdog in `Engine.Run()` after `ACP.Prompt()` succeeds — the closure logs disconnect vs. timeout vs. other context cancellation at Debug level, then calls `ACP.Cancel(sid)`
- Added `errors` import for `errors.Is` in watchdog closure

**collect.go changes:**
- Added `stop()` call on the natural-completion path (`rerr == nil` after `run.stream.Result()`)
- CRITICAL: stop() is NOT called on the error path (`rerr != nil`) — the watchdog must fire Cancel on errors, only suppressed on natural completion (D-06 teardown)

**ollama/wire.go changes:**
- Changed `ollamaChatRequest.Stream` from `bool` to `*bool json:"stream,omitempty"`
- Changed `ollamaGenerateRequest.Stream` from `bool` to `*bool json:"stream,omitempty"`
- Added `streamEnabled(*bool) bool` helper: nil = absent = stream:true (Node parity), explicit false = false
- Updated `wireToChatRequest` and `wireGenerateToChatRequest` to call `streamEnabled(w.Stream)`

**Rule 3 auto-fix: ollama/handlers.go** — the silent-downgrade blocks used `if wire.Stream {` (bool) and `wire.Stream = false` which broke after the *bool change. Updated to use `if streamEnabled(wire.Stream)` and `f := false; wire.Stream = &f`.

### Task 2: RunHandle interface extensions + main.go shims + finalizer stop() + D-07 derived ctx

**ollama/adapter.go:**
- Added `Run(ctx, req) (RunHandle, error)` to `Engine` interface
- Added `RunHandle` interface with `Stream()`, `SessionID()`, `StopWatchdog()`
- Added `Stream` interface mirroring engine.Stream

**ollama/handlers_test.go:**
- Added `fakeEngine.Run` compile stub returning diagnostic error (not nil, nil)

**openai/adapter.go:**
- Added `StopWatchdog() func() bool` to `RunHandle` interface

**openai/sse.go:**
- Added stop() in `finalizeSSE` AFTER `Result()` returns nil, BEFORE finish_reason frame emission

**openai/handlers.go:**
- Added `ctx, cancelFn := context.WithCancel(r.Context()); defer cancelFn()` before `Engine.Run(ctx, req)` in streaming branch (D-07)
- Added `context` import

**anthropic/adapter.go:**
- Added `StopWatchdog() func() bool` to `RunHandle` interface

**anthropic/sse.go:**
- Added stop() in `finalizeStream` AFTER `if rerr != nil` block — success path ONLY
- Error path (rerr != nil) returns without calling stop() so watchdog fires Cancel on truncation

**anthropic/handlers.go:**
- Added `ctx, cancelFn := context.WithCancel(r.Context()); defer cancelFn()` in streaming branch (D-07)
- Added `context` import

**cmd/otto-gateway/main.go:**
- Added `ollamaEngineAdapter` struct with `Collect` and `Run` methods
- Added `ollamaRunHandleAdapter` struct with `Stream`, `SessionID`, `StopWatchdog` methods
- Changed `engineForAdapter = a.engine` to `engineForAdapter = ollamaEngineAdapter{engine: a.engine}` (FACT 2 fix)
- Added `StopWatchdog()` to `anthropicRunHandleAdapter` (FACT 3)
- Added `StopWatchdog()` to `openaiRunHandleAdapter` (FACT 3)

**Rule 3 auto-fixes in test files:**
- Added `StopWatchdog() func() bool { return nil }` to all `fakeRunHandle` types across openai/sse_golden_test.go, openai/integration_test.go, anthropic/handlers_test.go, anthropic/integration_test.go
- Added `realRunHandle.StopWatchdog()` delegating to `h.run.StopWatchdog()` in openai/integration_test.go and anthropic/integration_test.go
- Added `testEngineAdapter` + `testRunHandleAdapter` in ollama/integration_test.go (whitebox test cannot use cmd-level ollamaEngineAdapter; *engine.Engine no longer satisfies ollama.Engine after Run() added)

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] ollama/handlers.go: *bool Stream used as bool**
- **Found during:** Task 1 — changing ollamaChatRequest.Stream and ollamaGenerateRequest.Stream to *bool
- **Issue:** handlers.go used `if wire.Stream {` and `wire.Stream = false` which are invalid when Stream is *bool
- **Fix:** Changed to `if streamEnabled(wire.Stream) { f := false; wire.Stream = &f }` in both handleChat and handleGenerate
- **Files modified:** internal/adapter/ollama/handlers.go
- **Commit:** dea85d8

**2. [Rule 3 - Blocking] All fakeRunHandle types missing StopWatchdog()**
- **Found during:** Task 2 — adding StopWatchdog() to RunHandle interfaces
- **Issue:** 5 test fakeRunHandle types across 4 files did not implement StopWatchdog() breaking compilation
- **Fix:** Added `StopWatchdog() func() bool { return nil }` to all fakeRunHandle + realRunHandle types in test files
- **Files modified:** internal/adapter/openai/sse_golden_test.go, internal/adapter/openai/integration_test.go, internal/adapter/anthropic/handlers_test.go, internal/adapter/anthropic/integration_test.go
- **Commit:** 332e4eb

**3. [Rule 3 - Blocking] ollama/integration_test.go: *engine.Engine no longer satisfies ollama.Engine**
- **Found during:** Task 2 — adding Run() to ollama.Engine interface
- **Issue:** Integration test passed `*engine.Engine` directly as `ollama.Config.Engine`; after Run() added, *engine.Engine.Run returns *engine.Run (not ollama.RunHandle) so it no longer satisfies the interface
- **Fix:** Added `testEngineAdapter` and `testRunHandleAdapter` local types in integration_test.go; changed `Engine: eng` to `Engine: testEngineAdapter{eng: eng}`
- **Files modified:** internal/adapter/ollama/integration_test.go
- **Commit:** 332e4eb

## Verification

```
go build ./...   — exit 0 (full project compiles)
go vet ./...     — exit 0
go test -race ./internal/engine/... ./internal/adapter/... — all PASS
```

All three FACTS from the plan were handled:
- FACT 1: stop() in collect.go on natural completion path only (rerr == nil after Result())
- FACT 2: ollamaEngineAdapter + ollamaRunHandleAdapter shims added; engineForAdapter = ollamaEngineAdapter{engine: a.engine}
- FACT 3: StopWatchdog() added to anthropicRunHandleAdapter and openaiRunHandleAdapter

## Known Stubs

None — all code paths are wired. The fakeEngine.Run stubs in test files are intentional compile stubs (not production stubs); they fail diagnostically if streaming tests accidentally exercise Run before Plan 03 wires the real fake.

## Threat Flags

None — all changes are within the pre-planned threat model. The new AfterFunc watchdog goroutine is the D-06 mitigated surface (T-04-03 goroutine leak prevention via stop() call sites).

## Self-Check: PASSED
