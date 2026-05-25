---
phase: 03-openai-surface
plan: "02"
subsystem: adapter-openai
tags: [openai, sse, streaming, wire-decode, golden-test, tdd, chat-completions]
dependency_graph:
  requires:
    - internal/adapter/openai skeleton with RegisterRoutes (03-01)
    - internal/canonical (ChatRequest/ChatResponse/Chunk/FinalResult)
  provides:
    - POST /v1/chat/completions (stream:false → JSON, stream:true → SSE)
    - OpenAI chat.completion envelope (non-streaming)
    - Flat OpenAI chat.completion.chunk SSE emitter (data:-only, no event: lines)
    - OpenAI error envelope {error:{message,type,param:null,code:null}}
    - wireToChatRequest (content string-or-array, developer+system hoist)
    - Golden fixture testdata/sse_text_only.golden + comparison harness
  affects:
    - internal/adapter/openai/adapter.go (stub handleChatCompletions removed)
tech_stack:
  added: []
  patterns:
    - Flat OpenAI SSE emitter (data:<json>\n\n … data:[DONE]) single-goroutine select-loop
    - TDD RED/GREEN/REFACTOR per task (wire+render+errors → sse+golden → handler+integration)
    - Golden fixture with regex normalization (id + created timestamp)
    - T-02-33 engine-error path (slog.Error raw + generic writeError, never echo err.Error())
key_files:
  created:
    - internal/adapter/openai/wire.go
    - internal/adapter/openai/render.go
    - internal/adapter/openai/errors.go
    - internal/adapter/openai/sse.go
    - internal/adapter/openai/handlers.go
    - internal/adapter/openai/wire_test.go
    - internal/adapter/openai/sse_test.go
    - internal/adapter/openai/sse_golden_test.go
    - internal/adapter/openai/integration_test.go
    - internal/adapter/openai/testdata/sse_text_only.golden
  modified:
    - internal/adapter/openai/adapter.go (removed stub handleChatCompletions + chatCompletionRequest)
decisions:
  - "normalizeChatID also normalizes created unix-timestamp for deterministic golden comparison"
  - "driveGolden uses buffered ch + close so emitter sees clean end-of-stream without goroutines"
  - "handleChatCompletions decode 400 includes err.Error() (syntactic JSON parse error is safe per T-02-33); engine errors use generic 'internal error' only"
  - "finalizeSSE emits role delta even on empty-chunk stream to keep API contract well-formed"
metrics:
  duration: "~45 minutes"
  completed_date: "2026-05-25"
  tasks_completed: 3
  files_changed: 10
---

# Phase 3 Plan 02: OpenAI Chat Completions End-to-End Summary

OpenAI `/v1/chat/completions` working both `stream:false` (JSON, SC1) and `stream:true` (SSE, SC2/Pi path); wire decode with polymorphic content + developer/system role hoist; flat `data:<json>\n\n` SSE emitter with two-case select-loop; golden-fixture byte-exact comparison; httptest integration round-trip proves SC1+SC2 end-to-end with fake engine; race-clean and leak-free.

## Tasks Completed

| # | Task | Commit | Status |
|---|------|--------|--------|
| 1 | Wire decode (wire.go) + non-streaming render + error envelope | f23cc2a | Done |
| 2 | Flat SSE emitter (sse.go) + golden fixtures + golden harness | af626c2 | Done |
| 3 | handleChatCompletions (stream + non-stream branch) + httptest integration | 6fd074f | Done |

## What Was Built

**Task 1 — Wire decode + render + errors:**
- `wire.go`: `chatCompletionRequest` with `chatMessage{Role,Content json.RawMessage}` (polymorphic content); `wireToChatRequest` decodes content string-or-array, maps `system`+`developer` → `canonical.RoleSystem` hoisted into `ChatRequest.System` (Pitfall 4), maps `user`/`assistant`/`tool`/unknown, sets `Model` unconditionally (engine handles `auto`/empty per D-04), reads `X-Working-Dir` header; accept-and-ignore extras via typed json.RawMessage fields
- `render.go`: `chatCompletion`/`completionChoice`/`responseMessage`/`completionUsage` structs (field order load-bearing for golden tests); `mapFinishReason` (StopEndTurn→"stop", StopMaxTokens→"length", StopRefusal→"content_filter", default→"stop"); `genMessageID("chatcmpl-")` (crypto/rand hex); `joinTextContent` (verbatim from ollama/render.go); `chatResponseToCompletion` with usage honest zeros and defensive empty content
- `errors.go`: `errorEnvelope{Error errorInner{Message,Type,Param,Code *string}}` (OpenAI shape, no outer `type:"error"` field); `writeError` (Content-Type before WriteHeader per Pitfall 2); `writeJSON` for successful responses; error type constants (`errInvalidRequest`, `errNotFound`, `errRequestTooLarge=invalid_request_error`, `errAPI`)
- Tests: `TestWire` (string/array content, system+developer hoist, role mapping, model pass-through, extras), `TestErrors` (status codes, OpenAI envelope shape, not Anthropic outer type), `TestChatCompletions_NonStream` render half (finish_reason mapping, joined text, zero usage, chatcmpl- prefix)

**Task 2 — Flat SSE emitter:**
- `sse.go`: `chatCompletionChunk`/`chunkChoice`/`chunkDelta` (field order load-bearing; `finish_reason *string` null on non-final frames); `sseEmitter` with fixed `id`+`created` (Pitfall 8); `writeData` (`data: <json>\n\n`+Flush); two-case select-loop `ctx.Done`+`chunks` — NO tickerC, NO event: lines (Pitfall 6); `finalizeSSE` emits role delta if no chunks arrived, then finish_reason frame with `delta:{}`, then literal `data: [DONE]\n\n`
- `testdata/sse_text_only.golden`: byte-exact fixture with `chatcmpl-<id>` and `"created":0` normalized placeholders; content: role delta → "Hello, " chunk → "world!" chunk → finish_reason frame → `data: [DONE]`
- `sse_golden_test.go`: `normalizeChatID` normalizes both id and created timestamp; `compareGolden` with trailing-newline trim; `driveGolden`/`fakeRunHandle`/`fakeStream` harness; `TestSSEGolden`; `TestNormalizeChatID`
- `sse_test.go`: `TestSSE_CtxCancel` (no [DONE] on cancel, error returned), `TestSSE_HeadersSetBeforeBody` (Content-Type + Cache-Control before WriteHeader), `TestSSE_NoEventLines`, `TestSSE_DoneTerminator`, `TestSSE_RoleFirstDelta`, `TestSSE_FixedIDAndCreated`

**Task 3 — Handler + integration:**
- `handlers.go`: `handleChatCompletions` (nil-engine→503, decode+413/400, empty-messages→400, `wireToChatRequest`, stream branch `engine.Run`→`runSSEEmitter` vs `engine.Collect`→`chatResponseToCompletion`+`writeJSON`); T-02-33 engine errors `slog.Error(raw err)` + `writeError(500, errAPI, "internal error")` — never echo `err.Error()` from engine paths
- `integration_test.go`: `fakeEngine`/`fakeRunHandle`/`fakeStream` fake engine; `mountedAdapter` mounts via `RegisterRoutes` under chi `r.Route("/v1",…)`;  SC1 round-trip (stream:false → 200 + chat.completion envelope); SC2 round-trip (stream:true → 200 + text/event-stream + role delta + content deltas + finish_reason + [DONE]); nil-engine→503, empty-messages→400, oversize→413 guard tests; real-kiro tests gated on `OTTO_INTEGRATION=1`
- `adapter.go`: stub `handleChatCompletions` and stub `chatCompletionRequest` removed (real definitions in handlers.go/wire.go)

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Stub chatCompletionRequest in adapter.go conflicted with wire.go definition**
- **Found during:** Task 1 (GREEN phase) — adapter.go still had a stub `chatCompletionRequest` struct; adding the real one in wire.go caused a redeclaration error.
- **Fix:** Replaced the adapter.go stub `handleChatCompletions` (which used the old minimal struct) with a temporary shim using the real wire struct + writeError/writeJSON helpers; stub was then removed entirely in Task 3.
- **Files modified:** `internal/adapter/openai/adapter.go`
- **Commit:** f23cc2a

**2. [Rule 2 - Correctness] normalizeChatID must also normalize `created` unix timestamp**
- **Found during:** Task 2 — generated golden file contained `"created":1779671787` (real unix timestamp); any subsequent run would produce a different timestamp, making the golden comparison fail.
- **Fix:** Extended `normalizeChatID` to also apply `createdRegexp` (`"created":\d+` → `"created":0`) so the golden is fully deterministic.
- **Files modified:** `internal/adapter/openai/sse_golden_test.go`
- **Commit:** af626c2

**3. [Rule 1 - Lint] noctx linter: httptest.NewRequest must use context-aware variant**
- **Found during:** Task 1 commit — golangci-lint flagged `httptest.NewRequest` calls in wire_test.go.
- **Fix:** Changed to `httptest.NewRequestWithContext(context.Background(), ...)`.
- **Files modified:** `internal/adapter/openai/wire_test.go`
- **Commit:** f23cc2a

**4. [Rule 1 - Lint] writeJSON unused at Task 1 commit time**
- **Found during:** Task 1 commit — golangci-lint flagged `writeJSON` as unused because the stub handleChatCompletions only called `http.Error`. Fixed by having the stub call `writeJSON(w, chatResponseToCompletion(nil, wire.Model))` to satisfy the `unused` linter. Removed entirely when Task 3 provided the real handler.
- **Files modified:** `internal/adapter/openai/adapter.go`
- **Commit:** f23cc2a

## Known Stubs

None — all stubs from Plan 01 for the chat completions path are fully replaced. Remaining stubs:

| File | Handler | Stub Behavior | Resolved By |
|------|---------|---------------|-------------|
| `internal/adapter/openai/adapter.go` | `handleCompletions` | decodes body, returns 501 | Plan 03-03 |
| `internal/adapter/openai/adapter.go` | `handleModels` | returns 501 | Plan 03-03 |

## Threat Flags

No new threat surface found beyond what the plan's threat model covers. All mitigations from `<threat_model>` applied:

- T-03-10 (DoS / body cap): `decodeJSONBody` + `http.MaxBytesReader(4 MiB)` → 413 — unit tested (`TestIntegration_OversizeBody_413`)
- T-03-11 (Info Disclosure / engine error): `slog.Error` raw + `writeError(500, errAPI, "internal error")` — source-verified (`err.Error()` in handlers.go only for decode 400, not engine errors)
- T-03-12 (DoS / SSE goroutine leak): two-case select-loop returns on `ctx.Done`; `goleak.VerifyTestMain` proves leak-free (`TestSSE_CtxCancel` passes under goleak)
- T-03-13 (Tampering / SSE framing): Flusher assert before WriteHeader; Content-Type/Cache-Control set before WriteHeader(200) — verified by `TestSSE_HeadersSetBeforeBody`

## Self-Check: PASSED
