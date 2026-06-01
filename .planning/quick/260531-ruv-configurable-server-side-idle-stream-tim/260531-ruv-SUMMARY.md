---
quick_id: 260531-ruv
type: execute
status: complete
completed: 2026-05-31
commits:
  - 6fe489a feat(engine): add RangeChunksWithIdleTimeout helper
  - 697cd10 feat(config): add STREAM_IDLE_TIMEOUT_SEC, wire engine + adapter Config
  - aadcdb7 feat(adapters): wire stream-idle watchdog into all chunk-loop sites
  - 0c934e4 feat(scripts): add --idle-timeout / -IdleTimeout flag + env doc
files_created:
  - internal/engine/idle.go
  - internal/engine/idle_test.go
  - internal/canonical/errors.go
files_modified:
  - internal/engine/engine.go
  - internal/engine/collect.go
  - internal/config/config.go
  - internal/config/config_test.go
  - cmd/otto-gateway/main.go
  - internal/adapter/anthropic/adapter.go
  - internal/adapter/anthropic/collect.go
  - internal/adapter/anthropic/collect_test.go
  - internal/adapter/anthropic/collect_posthook_test.go
  - internal/adapter/anthropic/chat_trace_e2e_test.go
  - internal/adapter/anthropic/sse.go
  - internal/adapter/anthropic/sse_test.go
  - internal/adapter/anthropic/sse_golden_test.go
  - internal/adapter/anthropic/sse_posthook_test.go
  - internal/adapter/anthropic/handlers.go
  - internal/adapter/ollama/adapter.go
  - internal/adapter/ollama/ndjson.go
  - internal/adapter/ollama/ndjson_test.go
  - internal/adapter/ollama/ndjson_posthook_test.go
  - internal/adapter/ollama/chat_trace_e2e_test.go
  - internal/adapter/ollama/handlers.go
  - internal/adapter/openai/adapter.go
  - internal/adapter/openai/sse.go
  - internal/adapter/openai/sse_test.go
  - internal/adapter/openai/sse_golden_test.go
  - internal/adapter/openai/sse_posthook_test.go
  - internal/adapter/openai/handlers.go
  - scripts/otto-gw
  - scripts/otto-gw.ps1
  - scripts/.env.otto-gw.example
---

# Quick 260531-ruv — Configurable server-side idle-stream timeout

## One-liner

Per-chunk-loop idle watchdog (`engine.RangeChunksWithIdleTimeout` + per-surface
inline replicas) bounds silent-kiro pool-slot hold time at
`STREAM_IDLE_TIMEOUT_SEC` (default 30s, 0 disables) with WARN log marker +
504 on non-streaming paths.

## What was built

Quick 260531-ra6's closing note flagged the live stuck-slot symptom: the engine
`context.AfterFunc` watchdog only fires when the request ctx terminates. When
kiro hangs and emits zero chunks, the SSE/NDJSON emitter never writes, the
client never disconnects, and `streamCtx` never gets canceled — so the pool
slot is held indefinitely.

This quick adds a configurable idle-stream watchdog at every chunk-receive loop
so a stuck kiro cannot hold a pool slot beyond `STREAM_IDLE_TIMEOUT_SEC`
seconds.

### Task 1 — engine.RangeChunksWithIdleTimeout helper

`internal/engine/idle.go` introduces the canonical helper:

```go
func RangeChunksWithIdleTimeout(
    ctx context.Context,
    stream Stream,
    idle time.Duration,
    onChunk func(canonical.Chunk) error,
) error
```

- `idle == 0` → bare ctx-aware range, zero timer cost (legacy hang-forever path)
- `idle > 0` → `time.NewTimer(idle)` with drain-safe Stop/Reset on each chunk
- Returns `fmt.Errorf("...%w", ErrStreamIdleTimeout)` on fire; ctx wrap on
  cancel; verbatim onChunk error propagation
- 5 unit tests, all `<=200ms` idle, all under `-race`

### Task 2 — config + wiring

- `config.Config.StreamIdleTimeoutSec int` (raw seconds at env layer)
- `getEnvInt("STREAM_IDLE_TIMEOUT_SEC", 30)`; negative = boot error
- `engine.Config.StreamIdleTimeout time.Duration` (next to DefaultCWD)
- `anthropic.Config / ollama.Config / openai.Config` each gain
  `StreamIdleTimeout time.Duration`
- `cmd/otto-gateway/main.go` computes
  `streamIdle := time.Duration(cfg.StreamIdleTimeoutSec) * time.Second` once
  and threads into `engine.New`, every adapter `.New`, every
  `EngineForSession` factory closure
- 5 config tests (default / explicit / zero / negative / non-int)

### Task 3 — wire all 5 chunk-loop sites

Sentinel relocation (TRST-04 fix): `canonical.ErrStreamIdleTimeout` lives in
`canonical` because adapter_* packages may not import internal/engine.
`engine.ErrStreamIdleTimeout` is a package-level alias.

| Site | File | Pattern |
|------|------|---------|
| 1 | `internal/engine/collect.go` | Routes via `RangeChunksWithIdleTimeout`. WARN-logs `stream.idle_timeout` with `surface=engine.collect` on fire. |
| 2 | `internal/adapter/anthropic/collect.go` | `CollectAnthropicChat` accepts `streamIdle`; inline 3-arm select (ctx + idleC + chunks) replicates helper semantics (TRST-04 forbids engine import). |
| 3 | `internal/adapter/anthropic/sse.go` | `runSSEEmitter` + `runSSEEmitterLoop` gain `streamIdle`. 4-arm select adds `idleC` next to `ctx.Done / tickerC / chunks`. On fire: `writeSSEError` emits Anthropic `event: error` frame + WARN log + return wrapped `canonical.ErrStreamIdleTimeout`. |
| 4 | `internal/adapter/ollama/ndjson.go` | `runNDJSONEmitter` 3-arm select gains `idleC`. On fire: emits `{"error":"stream idle timeout","done":true}` terminal line + Flush + WARN log + wrapped sentinel. |
| 5 | `internal/adapter/openai/sse.go` | `runSSEEmitter` 3-arm select gains `idleC` (OpenAI has no ping ticker). On fire: emits SSE data-frame error envelope + `[DONE]` + WARN log + wrapped sentinel. |

Handler 504 mapping (non-streaming paths via engine.Collect): each
`handlers.go` checks `errors.Is(err, canonical.ErrStreamIdleTimeout)` BEFORE
the generic 500 and emits `504 Gateway Timeout` with the per-surface error
envelope helper + WARN log.

WARN log marker (single shape, every site):

```go
logger.Warn("stream.idle_timeout",
    "surface", "<anthropic|ollama|openai|engine.collect>",
    "session_id", run.SessionID(),
    "elapsed_ms", streamIdle.Milliseconds(),
    "request_id", plugin.RequestIDFromContext(ctx))
```

4 new tests added (one per surface + anthropic non-streaming Collect), all
with `<=100ms` idle, all unblock channels in `t.Cleanup` to avoid
goroutine-leak hangs:

- `anthropic/sse_test.go::TestSSE_IdleTimeout_EmitsErrorFrame`
- `anthropic/sse_test.go::TestSSE_IdleTimeout_Disabled` (idle=0 → ctx wins)
- `anthropic/collect_test.go::TestCollectAnthropicChat_IdleTimeout`
- `ollama/ndjson_test.go::TestNDJSON_IdleTimeout_EmitsErrorLine`
- `openai/sse_test.go::TestSSE_IdleTimeout_EmitsErrorFrame`

Pool slot release relies on the existing engine `context.AfterFunc` watchdog
firing on `streamCtx` cancel from the adapter's `defer cancelFn` — the idle
helper triggers this mechanism via the wrapped error returning to the
handler, which lets `defer cancelFn()` cascade. The helper does NOT reach
into the pool directly (per quick 260531-ra6's
`TestPool_Cancel_ReleasesSlot_WithoutResultDrain` regression test).

### Task 4 — wrapper scripts + env doc

- `scripts/.env.otto-gw.example`: documented `STREAM_IDLE_TIMEOUT_SEC` block
  after `POOL_SIZE` (env line commented; default-30 is fine).
- `scripts/otto-gw`: `--idle-timeout SEC` / `--idle-timeout=SEC` →
  `STREAM_IDLE_TIMEOUT_SEC`. shellcheck clean.
- `scripts/otto-gw.ps1`: `-IdleTimeout INT` (default `-1` sentinel for
  "not passed"; `0` is the explicit-disable value the operator may pass) →
  `$env:STREAM_IDLE_TIMEOUT_SEC`. pwsh parse unverified on macOS dev box
  (no pwsh installed; matches the caveat documented in quick 260531-oax).

## Verification

| Gate | Result |
|------|--------|
| `go build ./...` (darwin/arm64) | clean |
| `GOOS=windows go build ./...` | clean |
| `go test ./internal/adapter/... ./internal/engine/... ./internal/pool/... ./internal/config/...` `-race -count=1` | all pass |
| `shellcheck scripts/otto-gw` | clean |
| `bash -n scripts/otto-gw` | clean |
| New idle tests total runtime | well under 5 seconds (all timeouts ≤200ms) |

## Deviations from plan

**1. [Rule 3 — Blocking issue] Sentinel relocated to `canonical`**

- **Found during:** Task 3 wiring anthropic SSE.
- **Issue:** Plan called for `engine.ErrStreamIdleTimeout` to be the
  canonical sentinel, used by `errors.Is` in adapter handlers. TRST-04
  (`.go-arch-lint.yml`) prohibits `adapter_anthropic / adapter_openai`
  from importing `internal/engine`. (Note: `adapter_ollama` IS allowed
  to depend on engine — see `engine.CoerceToolCall` — but the Anthropic
  and OpenAI adapter packages cannot.)
- **Fix:** Created `internal/canonical/errors.go` with the canonical
  `ErrStreamIdleTimeout` sentinel. `engine.ErrStreamIdleTimeout` becomes
  a package-level alias so the engine-internal call site (`Collect`)
  is preserved verbatim. Adapter handlers now `errors.Is` against
  `canonical.ErrStreamIdleTimeout` without violating TRST-04.
- **Files modified:** `internal/canonical/errors.go` (new),
  `internal/engine/idle.go` (alias + import cleanup).
- **Commit:** aadcdb7

**2. [Rule 3 — Blocking issue] `idleFakeEngine` test fake added**

- **Found during:** Task 3 writing `TestCollectAnthropicChat_IdleTimeout`.
- **Issue:** The existing `parityFakeEngine` in `collect_test.go` always
  returns a pre-populated closed channel (because parity tests need
  deterministic chunk lists). The idle test needs a never-producing
  channel — `parityFakeEngine` cannot satisfy this without breaking
  every parity test.
- **Fix:** Added a minimal `idleFakeEngine` struct in `collect_test.go`
  scoped to the new test. No production code change.
- **Commit:** aadcdb7

## Follow-ups (out of scope; tracked here for the next quick)

- **OpenAI SSE has no ping ticker** — `runSSEEmitter` is a 3-arm select
  (`ctx + idleC + chunks`). If a future quick adds keepalive to OpenAI
  the idle reset logic stays correct because the chunk arm is the
  only path that resets the timer.
- **Anthropic non-streaming WARN log lacks `session_id`** — the
  session id is bound inside `CollectAnthropicChat` and not surfaced
  to the handler-side WARN log. Setting it to empty string is
  acceptable for the v1 marker (operator filters on `surface=anthropic`
  + `request_id`); next iteration could thread the session id back
  through the error chain.

## Authentication gates

None.

## Self-Check: PASSED

- `internal/engine/idle.go` — FOUND
- `internal/engine/idle_test.go` — FOUND
- `internal/canonical/errors.go` — FOUND
- All 4 commit hashes (6fe489a, 697cd10, aadcdb7, 0c934e4) — FOUND in
  `git log --oneline -5`
