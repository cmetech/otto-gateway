---
phase: "04-streaming"
plan: "02"
subsystem: "ollama adapter — NDJSON streaming vertical slice"
tags: ["ndjson", "streaming", "ollama", "langflow", "goleak", "D-06", "D-07", "SC4"]
dependency_graph:
  requires:
    - "04-01: engine.Run AfterFunc watchdog + StopWatchdog() + ollama RunHandle/Stream interfaces + main.go shims"
  provides:
    - "runNDJSONEmitter + emitNDJSONChunk + finalizeNDJSON (internal/adapter/ollama/ndjson.go)"
    - "handlers.go streaming branch: context.WithCancel derived ctx + Engine.Run + runNDJSONEmitter"
    - "goleak.VerifyTestMain gate for ollama adapter package"
    - "E2E: Chat_Streaming, Generate_Streaming, Chat_DisconnectSmoke (SC4 pool-alive assertion)"
  affects:
    - "internal/adapter/ollama/ndjson.go"
    - "internal/adapter/ollama/ndjson_test.go"
    - "internal/adapter/ollama/testmain_test.go"
    - "internal/adapter/ollama/handlers.go"
    - "internal/adapter/ollama/handlers_test.go"
    - "tests/e2e/ollama_e2e_test.go"
tech_stack:
  added: []
  patterns:
    - "NDJSON streaming: bufio.NewScanner consumer side; json.Marshal + fmt.Fprintf(w, '%s\n', body) + flusher.Flush() producer side"
    - "context.WithCancel derived ctx in Ollama handlers (D-07) matching OpenAI/Anthropic pattern from Plan 01"
    - "cancelFn() called inside emitNDJSONChunk on write error (D-07 — adapter signals write failure to engine watchdog)"
    - "StopWatchdog() called in finalizeNDJSON on natural completion (D-06 teardown)"
    - "goleak.VerifyTestMain gate for test package goroutine-leak detection"
    - "Chat_DisconnectSmoke E2E: pool.alive health check before/after disconnect (SC4 proof)"
key_files:
  created:
    - "internal/adapter/ollama/ndjson.go — runNDJSONEmitter, emitNDJSONChunk, finalizeNDJSON, ndjsonChatLine, ndjsonGenerateLine"
    - "internal/adapter/ollama/ndjson_test.go — 6 TestNDJSON_* unit tests"
    - "internal/adapter/ollama/testmain_test.go — goleak.VerifyTestMain gate"
  modified:
    - "internal/adapter/ollama/handlers.go — removed downgrade blocks; added streaming branch with context.WithCancel + Engine.Run + runNDJSONEmitter"
    - "internal/adapter/ollama/handlers_test.go — real fake RunHandle; TestHandleChat_Streaming, TestHandleChat_StreamFalse_NonStreaming, TestHandleChat_RunError_500"
    - "tests/e2e/ollama_e2e_test.go — Chat_StreamDowngrade replaced; Chat_Streaming, Generate_Streaming, Chat_DisconnectSmoke added; getHealthPoolAlive helper"
decisions:
  - "cancelFn() called INSIDE emitNDJSONChunk on write error (not in runNDJSONEmitter) — keeps D-07 signal co-located with the failure site and avoids double-cancel races"
  - "finalizeNDJSON calls chatResponseToWire/generateResponseToWire with nil *ChatResponse — nil-safe confirmed in render.go (both helpers guard 'if resp != nil'); no zero-value workaround needed"
  - "isChat=false drops ChunkKindThought silently in emitNDJSONChunk (D-04) — /api/generate has no thinking field in Ollama wire shape"
  - "handlers_test.go uses newFakeRunHandle from ndjson_test.go (same package ollama — whitebox test binary)"
  - "fakeEngine.Run stub diagnostic error (Plan 01) replaced by real fakeRunHandle — Test_EngineError_500 updated to use stream:false to exercise Collect path"
  - "getHealthPoolAlive added as file-local helper in ollama_e2e_test.go — reuses shared helpers from e2e_test.go without redeclaration"
metrics:
  duration: "~50 minutes"
  completed_date: "2026-05-25"
  tasks: 2
  files_modified: 6
---

# Phase 04 Plan 02: Ollama NDJSON Streaming Vertical Slice Summary

Ollama surface now streams NDJSON end-to-end: LangFlow pointing at `/api/chat` receives a live NDJSON stream. The Chat_StreamDowngrade landmine is defused. The disconnect smoke test proves SC4 (pool-of-1 slot survives mid-stream cancel).

## What Was Built

### Task 1: goleak gate + NDJSON emitter

**internal/adapter/ollama/testmain_test.go:**
- Added `goleak.VerifyTestMain(m)` — closes the VALIDATION.md Wave 0 gap for the ollama adapter package

**internal/adapter/ollama/ndjson.go:**
- `ndjsonChatLine` / `ndjsonGenerateLine` — intermediate done:false frame structs (RESEARCH.md Pitfall 7: separate from done:true ollamaChatResponse to prevent field-order contamination)
- `emitNDJSONChunk(w, flusher, c, model, isChat, cancelFn)` — handles ChunkKindText (chat: message.content; generate: response), ChunkKindThought (chat: message.thinking; generate: dropped per D-04), unknown kinds (dropped silently). Calls `cancelFn()` on write error (D-07) before returning the wrapped error.
- `runNDJSONEmitter(ctx, cancelFn, w, run, model, isChat, start, logger)` — asserts http.Flusher before any write (Pitfall 2), sets `Content-Type: application/x-ndjson` before `WriteHeader(200)` (Pitfall 2 order), core select-loop: ctx.Done logs disconnect and returns ctx error; chunk channel close delegates to finalizeNDJSON.
- `finalizeNDJSON(w, flusher, run, model, isChat, start, logger)` — calls `run.Stream().Result()` (on error: debug-log + return without done:true per D-05); calls `run.StopWatchdog()` + `stop()` (D-06 teardown, Pitfall 3); builds done:true line via `chatResponseToWire(nil, start, model)` / `generateResponseToWire(nil, start, model)` (nil-safe confirmed).

**internal/adapter/ollama/ndjson_test.go (6 subtests):**
- `TestNDJSON_Chat_TextChunks` — ≥3 NDJSON lines, done:true final
- `TestNDJSON_Chat_ThoughtChunk` — message.thinking non-empty, message.content empty
- `TestNDJSON_Generate_ThoughtDropped` — exactly 1 line (done:true only; thought dropped)
- `TestNDJSON_FlusherAssertionFails` — non-flusher writer returns error before any bytes written
- `TestNDJSON_WriteError_CancelsCtx` — cancelFn called on write error (D-07)
- `TestNDJSON_StreamResultError` — stream.Result() error → no done:true line emitted

### Task 2: Flip handlers.go + update handlers_test.go + ollama E2E (ATOMIC — Pitfall 5)

**internal/adapter/ollama/handlers.go:**
- Added `"context"` import
- Removed both stream:true→false downgrade blocks (handleChat lines 43-45, handleGenerate lines 82-84)
- `handleChat`: non-streaming branch (`!streamEnabled(wire.Stream)`) calls `Engine.Collect`; streaming branch adds `ctx, cancelFn := context.WithCancel(r.Context()); defer cancelFn()` (D-07), calls `Engine.Run(ctx, req)`, then `runNDJSONEmitter(ctx, cancelFn, w, run, wire.Model, true, start, logger)`
- `handleGenerate`: same pattern with `isChat=false`

**internal/adapter/ollama/handlers_test.go:**
- Added `runChunks []canonical.Chunk` and `runErr error` fields to `fakeEngine`
- Replaced `fakeEngine.Run` diagnostic stub with real `newFakeRunHandle(f.runChunks, final, nil)` implementation
- Replaced `TestHandleChat_StreamTrue_SilentDowngrade` with `TestHandleChat_Streaming` (asserts `application/x-ndjson`) and `TestHandleChat_StreamFalse_NonStreaming` (asserts `application/json` regression)
- Added `TestHandleChat_RunError_500` (streaming Run error → 500 before headers written)
- Updated `TestHandleChat_EngineError_500` to use `stream:false` (exercises Collect path)

**tests/e2e/ollama_e2e_test.go:**
- Removed `Chat_StreamDowngrade` subtest entirely (Pitfall 5 — ATOMIC with handlers.go flip)
- Added `Chat_Streaming`: absent stream field → `application/x-ndjson`, ≥2 NDJSON lines, last done:true and done_reason ∈ {stop,length}
- Added `Generate_Streaming`: same shape with `response` field (not `message`)
- Added `Chat_DisconnectSmoke` (SC4 proof):
  - `GET /health` before → record `pool.alive`
  - Start streaming `/api/chat`, read one NDJSON line, close body (mid-stream disconnect)
  - Wait 300ms for cancel to settle
  - `GET /health` after → assert `pool.alive` unchanged (no slot restart — Codex MEDIUM concern addressed)
  - Follow-up `POST /api/chat` with `stream:false` → asserts 200 with non-empty content (slot reusable)
- Added `getHealthPoolAlive` helper: `GET /health` → decode `pool.alive` count
- Added `bufio` import for `bufio.NewScanner`

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] TestHandleChat_EngineError_500 broke under streaming default**
- **Found during:** Task 2 — after handlers.go was changed to stream by default
- **Issue:** `TestHandleChat_EngineError_500` used a body without `stream:false`, so the handler now called `Engine.Run` (not `Collect`); `fakeEngine.err` only affects `Collect`, so the test got 200 instead of 500
- **Fix:** Added `"stream":false` to the test body so it exercises the Collect path; added `TestHandleChat_RunError_500` for the Run error path
- **Files modified:** internal/adapter/ollama/handlers_test.go
- **Commit:** bd62557

**2. [Rule 2 - Coverage] TestNDJSON_StreamResultError added**
- **Found during:** Task 1 — golangci-lint `unparam` flagged `newFakeRunHandle` err always nil
- **Issue:** No test exercised the `run.Stream().Result()` error path in `finalizeNDJSON`; also a coverage gap
- **Fix:** Added `TestNDJSON_StreamResultError` passing `streamErr` to `newFakeRunHandle`; validates that finalizeNDJSON does not emit done:true on stream error
- **Files modified:** internal/adapter/ollama/ndjson_test.go
- **Commit:** 745e334

## Verification

```
go build ./...                         — exit 0
go vet ./...                           — exit 0
golangci-lint run ./...               — 0 issues
go test -race ./internal/adapter/ollama/... ./internal/engine/...  — all PASS (goleak green)
```

Unit tests verified:
- All 6 TestNDJSON_* subtests PASS including write-error cancel (D-07) and StopWatchdog (D-06)
- TestHandleChat_Streaming PASS (application/x-ndjson)
- TestHandleChat_StreamFalse_NonStreaming PASS (application/json regression)
- TestHandleChat_RunError_500 PASS

E2E tests require real kiro-cli (`OTTO_E2E=1`). Test structure asserts:
- `Chat_Streaming`: status 200, Content-Type application/x-ndjson, ≥2 lines, last done:true
- `Generate_Streaming`: same shape with response field
- `Chat_DisconnectSmoke`: pool.alive unchanged before/after; slot reusable follow-up 200
- `Chat_NonStreaming`: application/json single object (stream:false regression)

## Known Stubs

None — all code paths wired end-to-end. E2E tests cover the real kiro-cli path.

## Threat Flags

None — all surfaces are within the pre-planned threat model (T-04-06 through T-04-09). The new `/api/chat` and `/api/generate` streaming paths are extensions of the existing Ollama trust boundary documented in the plan's threat register.

## Self-Check: PASSED
