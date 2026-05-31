---
phase: quick-260530-df2
plan: 01
subsystem: engine + adapters (canonical-layer PostHook invocation)
tags:
  - posthook
  - streaming
  - chat-trace
  - logging-hook
  - sync-map-leak
  - aggregator
requires:
  - quick-260529-ll2/ChatTraceHook (Pre+Post NDJSON tracer)
  - engine.PreHook / engine.PostHook interfaces (Phase 8 D-04)
provides:
  - Engine.RunPostHooks helper (streaming-adapter-facing companion to Collect)
  - sseEmitter.aggregatedResponse (anthropic, openai)
  - aggregateOllamaResponse (ollama)
  - Per-surface PostHook invocation on all three streaming surfaces
  - Non-streaming Anthropic PostHook gap closure (CollectAnthropicChat)
affects:
  - chat-trace.log post_chain_out records now appear on streaming requests
  - plugin.after slog records now fire on streaming requests
  - sync.Map.LoadAndDelete leak closure for LoggingHook/ChatTraceHook startTimes
tech-stack:
  added: []
  patterns:
    - per-adapter aggregator mirroring engine.Collect's strings.Builder discipline
    - WARN-and-swallow PostHook errors on streaming paths (T-df2-02)
    - propagate PostHook errors on non-streaming paths (consistent with engine.Collect)
    - return partial aggregated response on disconnect / Result()-error for forensics
key-files:
  created:
    - internal/adapter/anthropic/sse_posthook_test.go
    - internal/adapter/anthropic/collect_posthook_test.go
    - internal/adapter/anthropic/chat_trace_e2e_test.go
    - internal/adapter/ollama/ndjson_posthook_test.go
    - internal/adapter/ollama/chat_trace_e2e_test.go
    - internal/adapter/openai/sse_posthook_test.go
    - internal/adapter/openai/chat_trace_e2e_test.go
  modified:
    - internal/engine/engine.go (added RunPostHooks method)
    - internal/engine/engine_test.go (5 RunPostHooks unit tests)
    - internal/adapter/anthropic/adapter.go (Engine interface + RunPostHooks)
    - internal/adapter/anthropic/handlers.go (streaming branch fires PostHooks)
    - internal/adapter/anthropic/sse.go (aggregator state + (resp, err) return)
    - internal/adapter/anthropic/collect.go (non-streaming gap fix)
    - internal/adapter/ollama/adapter.go (Engine interface + RunPostHooks)
    - internal/adapter/ollama/handlers.go (chat + generate fire PostHooks)
    - internal/adapter/ollama/ndjson.go (aggregator state + (resp, err) return)
    - internal/adapter/openai/adapter.go (Engine interface + RunPostHooks)
    - internal/adapter/openai/handlers.go (streaming branch fires PostHooks)
    - internal/adapter/openai/sse.go (aggregator state + (resp, err) return)
    - cmd/otto-gateway/main.go (three engine adapter shims delegate RunPostHooks)
decisions:
  - "operators want forensics: return partial aggregated response on disconnect / Result()-error so PostHooks fire (T-df2-03 sync.Map leak mitigation)"
  - "streaming path: WARN-and-swallow PostHook errors (T-df2-02 — stream already over from client perspective)"
  - "non-streaming path (CollectAnthropicChat): propagate PostHook errors (response bytes have not been written yet — mirrors engine.Collect)"
  - "double-fire impossible by construction: handler paths branch on wire.Stream — streaming uses Run+RunPostHooks, non-streaming uses Collect (or CollectAnthropicChat which internally calls RunPostHooks)"
  - "aggregator richness invariant: each adapter MUST capture text + thinking + tool_use chunks, not stop-reason-only, or post_chain_out records ship empty content[]"
metrics:
  duration_minutes: ~45
  completed: 2026-05-30
  tasks: 5
  files_created: 7
  files_modified: 13
  tests_added: 21
  commits: 5
---

# Quick 260530-df2: Fire PostHooks on Streaming Responses — Summary

JWT pre/post observation parity across all three API surfaces — streaming
requests now produce matching `pre_chain_in` + `post_chain_out` NDJSON
records in chat-trace.log AND matching `plugin.before` + `plugin.after`
slog records in the main log, correlated by `request_id`.

## What changed

Operators saw zero `post_chain_out` records in `chat-trace.log` (and zero
`plugin.after` lines) despite many `pre_chain_in` records. Root cause:
`engine.Collect` at `collect.go:118-122` was the only call site for the
`PostHook` chain, but every streaming adapter called
`eng.Run` + `runSSEEmitter`/`runNDJSONEmitter` and returned WITHOUT calling
`Collect`. PostHook invocation was gated on a code path streaming
requests don't take. The pre-existing non-streaming Anthropic path
(`CollectAnthropicChat`, the D-07 exception) had the same gap — it
bypassed `engine.Collect` for the tool_use rendering work and lost the
PostHook traversal in the process.

This fix:
1. Adds `Engine.RunPostHooks(ctx, req, resp)` — same iteration
   discipline as `Collect`'s PostHook traversal, exposed as a callable
   method so streaming adapters can fire PostHooks after they finish
   ranging `Stream().Chunks()` themselves.
2. Extends every streaming emitter with a per-stream **aggregator**
   (text + thinking + tool_use parts where applicable) that builds a
   canonical response after stream completion. The load-bearing
   correctness risk identified in the plan: if the aggregator only
   captures `stop_reason`, the `post_chain_out` record's `content[]`
   field ships empty and chat-trace.log loses its product value. The
   aggregator in every surface mirrors `engine.Collect`'s
   `strings.Builder` discipline.
3. Wires `eng.RunPostHooks` calls into all three streaming branches
   (anthropic `handleMessages`, ollama `handleChat` + `handleGenerate`,
   openai `handleChatCompletions`).
4. Wires `eng.RunPostHooks` calls into `CollectAnthropicChat`'s tail AND
   short-circuit branch — closes the pre-existing non-streaming
   Anthropic gap.

## Tasks

### Task 1 — Engine.RunPostHooks helper + unit tests (commit 6e7620a)

Added `func (e *Engine) RunPostHooks(ctx, req, resp) error` mirroring
`collect.go:118-122` exactly. Wraps the first non-nil hook error with
"engine: posthook: ..." — same prefix `Collect` uses so downstream log
filters keep working.

5 unit tests pin: registration order, error wrap prefix, defensive
nil-resp pass-through, empty-chain no-op, stop-on-first-error.

### Task 2 — Anthropic streaming + non-streaming gap fix (commit 519f253)

**Streaming (`sse.go`):**
- Extended `sseEmitter` with `aggText`, `aggThought`, `aggToolParts`,
  `aggToolCalls` builders. Mirrors `CollectAnthropicChat` discipline
  (collect.go:75-119).
- Added `aggregatedResponse(req, stop)` method matching
  `assembleAnthropicChatResponse` (text at Content[0], thinking next,
  tool_use after; ToolCalls populated per D-07 exception).
- `runSSEEmitter` / `runSSEEmitterLoop` / `finalizeStream` now return
  `(*canonical.ChatResponse, error)`. On clean completion, ctx-cancel,
  AND mid-stream Result() errors the partial aggregated response is
  returned for forensics.
- `handleMessages` streaming branch reads `resp` and calls
  `eng.RunPostHooks(streamCtx, req, resp)`. PostHook errors are logged
  at WARN and swallowed (T-df2-02 — stream is over, can't tear down).

**Non-streaming (`collect.go`):**
- `CollectAnthropicChat` now invokes `eng.RunPostHooks` at the tail
  (after `assembleAnthropicChatResponse`) AND on the short-circuit
  branch (mirrors `engine.Collect` at `collect.go:114-122` Codex H-5).
- Non-streaming path propagates the PostHook error (response bytes not
  yet on the wire) — divergence from streaming WARN-and-swallow,
  matching `engine.Collect`'s behavior.

**Tests:** 6 new (4 SSE PostHook + 2 collect PostHook).

### Task 3 — Ollama NDJSON streaming (chat + generate) (commit 3f615ce)

**Aggregator (`ndjson.go`):**
- Extended `emitterState` with `aggregatedText` + `aggregatedThinking`
  builders that capture every chunk written or buffered (including
  the `[tool: name]\n` narration for kiro-native ChunkKindToolCall).
- Added `aggregateOllamaResponse(req, state, stop)` builder.
- `runNDJSONEmitter` / `finalizeNDJSON` return
  `(*canonical.ChatResponse, error)` with partial-aggregation
  guarantee on disconnect / mid-stream errors.
- Coerce-hit path: the synthesized resp (already carries
  Message.ToolCalls) is handed to RunPostHooks with Model + StopReason
  patched for canonical consistency.

**Handlers (`handlers.go`):**
- Both `handleChat` (stream:true) and `handleGenerate` (stream:true)
  call `eng.RunPostHooks` with the surface tag (`ollama.chat` vs
  `ollama.generate`) for log-analysis discrimination.

**Tests:** 6 new (chat + generate post-stream, partial error, error
swallow, disconnect, kiro-native ToolCalls observation).

### Task 4 — OpenAI SSE streaming (commit 44b48f0)

**Aggregator (`sse.go`):**
- Extended `sseEmitter` with `aggregatedText` builder + `aggregatedResponse`
  method.
- `runSSEEmitter` / `finalizeSSE` return `(*canonical.ChatResponse,
  error)` with partial-aggregation guarantee.
- `tryStreamingCoerce` returns the synthesized `[]canonical.ToolCall`
  on coerce-hit so the post-stream resp carries Message.ToolCalls
  (mirrors the wire shape PostHooks observe).

**Handlers (`handlers.go`):**
- `handleChatCompletions` streaming branch fires `eng.RunPostHooks`.
- `handleCompletions` legacy text shim downgrades stream:true to
  false and routes through `eng.Collect` — PostHooks already fire via
  Collect's traversal. No change needed.

**Tests:** 5 new (post-stream, partial error, error swallow,
disconnect, streaming-coerce ToolCalls observation).

### Task 5 — E2E ChatTraceHook integration + double-fire guard (commit c9043d7)

Three per-surface e2e tests (`chat_trace_e2e_test.go` in each adapter
pkg) wire a real `*plugin.ChatTraceHook` (the production hook) against
a `bytes.Buffer` writer and drive a streaming request through the
adapter's emitter + RunPostHooks call site. Each asserts:
- Exactly 2 NDJSON records in the buffer.
- Record 1 stage="pre_chain_in" with non-empty request_id.
- Record 2 stage="post_chain_out" with SAME request_id, non-empty
  `content[]` (the load-bearing aggregator richness invariant).

Three double-fire guards (`TestAnthropic/Ollama/OpenAI_NoDoublePostHookFire`):
- Counter PostHook attached to a single engine fake instance.
- Non-streaming → counter == 1.
- Streaming (back-to-back) → counter == 2.
- Proves no handler path invokes both `eng.Collect` and
  `eng.RunPostHooks` for the same request.

Path-exclusivity verified by grep gate over
`internal/adapter/{anthropic,ollama,openai}/{handlers,sse,ndjson,collect}.go`
(only un-commented `eng.Collect` / `eng.RunPostHooks` / `CollectAnthropicChat`
call sites considered).

## Architectural decisions

### D1: operators DO want forensics on partial completion

The plan initially considered returning `(nil, err)` on mid-stream
Result() errors, but the spec was revised mid-Task 2 to favor returning
the partial aggregated response. This is the **forensics-friendly**
choice: even on terminal stream errors operators get a `post_chain_out`
record with whatever content arrived + a `duration_ms` value. The
sync.Map leak in `LoggingHook.startTimes` / `ChatTraceHook.startTimes`
(T-df2-03) only closes via `LoadAndDelete` inside `After` — without this
choice, partial-error requests would leak entries indefinitely.

### D2: WARN-and-swallow on streaming, propagate on non-streaming

Streaming branch contract: PostHook errors are logged at WARN and
SWALLOWED. The stream is over from the client's perspective — bytes
are on the wire — and a misbehaving observability hook MUST NOT tear
down a request that has already succeeded for the client (T-df2-02).

Non-streaming branch (`CollectAnthropicChat`): PostHook errors are
PROPAGATED. The response bytes have not been written yet, so the
caller (handlers.go) CAN respond with a 500. This mirrors
`engine.Collect`'s behavior verbatim (collect.go:118-122) and is
documented inline at the call sites in collect.go.

### D3: aggregator richness — text + thinking + tool_use, not stop-reason-only

The plan's load-bearing correctness risk. Every adapter aggregator
mirrors `engine.Collect`'s `strings.Builder` discipline:
- **Anthropic**: aggText / aggThought / aggToolParts / aggToolCalls
  (D-07 native tool_use blocks).
- **Ollama**: aggregatedText (includes ToolCall narration text) +
  aggregatedThinking. ToolCalls populated only on the coerce-hit path
  via the synthetic resp (HIGH #2 two-path rule preserved).
- **OpenAI**: aggregatedText (includes ToolCall narration text).
  ToolCalls populated only on the coerce-hit path via the synthetic
  ToolCalls slice surfaced by `tryStreamingCoerce`.

The Anthropic adapter's `CollectAnthropicChat` non-streaming path
(`collect.go:75-119`) is the canonical aggregator template — Task 2's
SSE state mirrors it field-for-field. Test
`TestAnthropicSSE_PostHooksFireAfterStreamCompletes` pins all four
fields are populated.

### D4: thread req through aggregator for Model echo

All three aggregators take `req *canonical.ChatRequest` so they can
populate `resp.Model = req.Model` — mirrors `assembleChatResponse`
(`collect.go:147-175`). Without this echo, PostHooks observing
`resp.Model` would see an empty string.

## Deviations from Plan

### Auto-applied (no deviation rules triggered beyond test naming)

**1. [Plan/Spec ambiguity, Rule 3 — clarify intent] Test name + intent revision in Task 2**

- **Found during:** Task 2 RED phase
- **Issue:** Plan's Test 2 was named "PostHooksDoNotFireOnRunError"
  with the assertion that `resp == nil` on the Result()-error path. But
  the plan's action step 2 explicitly favors returning `(resp, err)` on
  rerr != nil "so operators DO want forensics on partial completion".
  These two statements contradict each other.
- **Fix:** Renamed the test to `TestAnthropicSSE_PostHooksFireOnPartialStreamError`
  and aligned the assertions with the action-step choice. The
  Run-error-pre-headers case (where the handler returns at line
  179-187 BEFORE runSSEEmitter is called) is implicitly covered by
  the handler integration tests — runSSEEmitter is never reached in
  that case, so no PostHook fires.
- **Files modified:** internal/adapter/anthropic/sse_posthook_test.go
- **Commit:** 519f253

**2. [Rule 2 — auto-add missing critical functionality] All five fakeEngine satisfiers gain RunPostHooks**

- **Found during:** Task 2 RED phase (build failures)
- **Issue:** Adding `RunPostHooks` to the local `Engine` interface in
  each adapter package broke compilation for every fake implementation
  the test suite (and production main.go) had.
- **Fix:** Added `RunPostHooks` (no-op or delegate) to all impls:
  - Anthropic: notImplementedEngine, shortCircuitFakeEngine, sessionEngine, anthropicCaptureEngine, parityFakeEngine, realEngineAdapter
  - Ollama: shortCircuitFakeEngine, sessionEngine, captureEngine, testEngineAdapter
  - OpenAI: shortCircuitFakeEngine, sessionEngine, openaiCaptureEngine, captureEngine (completions), realEngineAdapter
  - cmd/otto-gateway/main.go: anthropicEngineAdapter, ollamaEngineAdapter, openaiEngineAdapter (production wiring — delegates to *engine.Engine.RunPostHooks).
- **Files modified:** see commit 519f253 (Task 2 spillover into Tasks 3 + 4)

**3. [Test infrastructure] Per-package e2e test reuses adapter test fakes verbatim**

- **Found during:** Task 5 planning
- **Issue:** Plan suggested using a "real *engine.Engine with a fake ACPClient"
  but engine_test.go's fakeACP is package-private and can't be reused
  from adapter packages without exporting.
- **Fix:** Each per-surface e2e test defines a self-contained
  `chatTraceFakeEngine` that wraps the adapter's existing fake
  RunHandle harness. The engine fake iterates the supplied PreHook
  chain in Run() (mirroring *engine.Engine.Run) and the PostHook
  chain in RunPostHooks (mirroring *engine.Engine.RunPostHooks). This
  exercises the production code paths (real ChatTraceHook, real
  RequestIDHook) without requiring an exported engine harness.
- **Files modified:** internal/adapter/anthropic/chat_trace_e2e_test.go,
  internal/adapter/ollama/chat_trace_e2e_test.go,
  internal/adapter/openai/chat_trace_e2e_test.go

## Verification

### Per-task automated gates

| Task | Verify command | Result |
|------|----------------|--------|
| 1 | `go test ./internal/engine/ -run TestEngine_RunPostHooks -race -v` | 5/5 pass |
| 2 | `go test ./internal/adapter/anthropic/ -race -v -run 'PostHook\|Collect'` | All pass |
| 3 | `go test ./internal/adapter/ollama/ -race -v -run 'PostHook\|NDJSON'` | All pass |
| 4 | `go test ./internal/adapter/openai/ -race -v -run 'PostHook\|SSE'` | All pass |
| 5 | `go test ./internal/adapter/{anthropic,ollama,openai}/ -race -run 'ChatTrace_E2E\|NoDoublePostHookFire' -v` | 6/6 pass |

### Full repo suite

```
ok  	otto-gateway/cmd/otto-gateway	2.280s
ok  	otto-gateway/internal/acp	(integration; passes)
ok  	otto-gateway/internal/adapter/anthropic	1.642s
ok  	otto-gateway/internal/adapter/ollama	1.970s
ok  	otto-gateway/internal/adapter/openai	3.375s
ok  	otto-gateway/internal/admin	18.447s
ok  	otto-gateway/internal/auth	2.376s
ok  	otto-gateway/internal/canonical	2.506s
ok  	otto-gateway/internal/config	3.124s
ok  	otto-gateway/internal/engine	1.625s
ok  	otto-gateway/internal/plugin	1.768s
ok  	otto-gateway/internal/plugin/pii	2.146s
ok  	otto-gateway/internal/pool	2.527s
ok  	otto-gateway/internal/server	3.495s
ok  	otto-gateway/internal/session	4.076s
```

`go vet ./internal/engine/... ./internal/adapter/... ./internal/plugin/... ./cmd/otto-gateway/...` clean.

### Path-exclusivity grep gate (Task 5)

Eyeball confirmed: every handler branch is either `Collect`-shaped
(non-streaming, PostHooks via Collect's internal traversal) OR
`Run + RunPostHooks`-shaped (streaming). No path does both for the same
request. Anthropic non-streaming uses `CollectAnthropicChat` which
internally calls `eng.RunPostHooks` once at its tail (or short-circuit
branch). The four counters (anthropic, ollama × 2, openai) on the
double-fire guard tests all read exactly the expected value (1 after
non-streaming + 1 after streaming = 2 total).

## Success criteria status

1. Streaming Anthropic emits matched pre/post pair — **confirmed by `TestChatTrace_E2E_AnthropicStreaming`**
2. Streaming Ollama (chat + generate) emits matched pre/post pair — **confirmed by `TestChatTrace_E2E_OllamaStreaming` + `TestOllama_NoDoublePostHookFire` covering both surfaces**
3. Streaming OpenAI emits matched pre/post pair — **confirmed by `TestChatTrace_E2E_OpenAIStreaming`**
4. Non-streaming continues to emit pre/post pair AND does not fire PostHooks twice — **confirmed by all three double-fire guard tests (counter == 2 after one streaming + one non-streaming request)**
5. Malformed-request fail-before-engine.Run does not fire PostHooks — **architecturally guaranteed: handler returns at lines 179-187 (anthropic) / equivalent in other adapters BEFORE runSSEEmitter; verified by code reading + grep gate**
6. Streaming-path PostHook error logged at WARN and does not fail the client — **confirmed by `TestAnthropicSSE_PostHookErrorDoesNotFailResponse`, `TestOllamaNDJSON_PostHookErrorLoggedNotPropagated`, `TestOpenAISSE_PostHookErrorLoggedNotPropagated`**
7. Aggregated canonical response has non-empty Message.Content when stream emitted text — **load-bearing test: `TestAnthropicSSE_PostHooksFireAfterStreamCompletes` (concat = "Hello world"), `TestOllamaNDJSON_Chat_PostHooksFireAfterStreamCompletes` ('Hello world'), `TestOpenAISSE_PostHooksFireAfterStreamCompletes` ('Hello OpenAI')**
8. sync.Map leak on streaming path closed — **architecturally guaranteed: streaming PostHook fire → LoggingHook.After / ChatTraceHook.After called → LoadAndDelete(rid) reclaims the entry. The leak only persisted while After was never called on streaming requests; now it is.**
9. All Go race tests pass; no new package dependencies — **`go test ./... -race` clean; `go.mod` unchanged**

Operator Task 6 verification (Pattern A automation + manual live-binary
checks for the 5-item checklist) is the remaining gate; it has its own
PLAN entry as `<task type="checkpoint:human-verify">`.

## Self-Check: PASSED

All 20 file paths referenced in this SUMMARY exist on disk. All 5 commit
hashes referenced (6e7620a, 519f253, 3f615ce, 44b48f0, c9043d7) are
reachable from the worktree branch.
