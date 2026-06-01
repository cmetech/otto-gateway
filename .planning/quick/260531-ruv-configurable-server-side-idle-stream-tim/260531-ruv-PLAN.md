---
quick_id: 260531-ruv
type: execute
autonomous: true
files_modified:
  - internal/engine/idle.go
  - internal/engine/idle_test.go
  - internal/engine/collect.go
  - internal/engine/engine.go
  - internal/config/config.go
  - internal/config/config_test.go
  - cmd/otto-gateway/main.go
  - internal/adapter/anthropic/adapter.go
  - internal/adapter/anthropic/collect.go
  - internal/adapter/anthropic/collect_test.go
  - internal/adapter/anthropic/sse.go
  - internal/adapter/anthropic/sse_test.go
  - internal/adapter/anthropic/handlers.go
  - internal/adapter/ollama/adapter.go
  - internal/adapter/ollama/ndjson.go
  - internal/adapter/ollama/ndjson_test.go
  - internal/adapter/ollama/handlers.go
  - internal/adapter/openai/adapter.go
  - internal/adapter/openai/sse.go
  - internal/adapter/openai/sse_test.go
  - internal/adapter/openai/handlers.go
  - scripts/otto-gw
  - scripts/otto-gw.ps1
  - scripts/.env.otto-gw.example
must_haves:
  truths:
    - "When the engine chunk stream produces no chunks for STREAM_IDLE_TIMEOUT_SEC seconds, the active loop tears down, the pool slot releases (via the engine.AfterFunc watchdog firing on streamCtx cancel), and the client sees a terminal error frame."
    - "STREAM_IDLE_TIMEOUT_SEC defaults to 30; an unset env var yields 30s behavior."
    - "STREAM_IDLE_TIMEOUT_SEC=0 disables the idle timer entirely (legacy hang-forever behavior, opt-in)."
    - "Non-int and negative STREAM_IDLE_TIMEOUT_SEC values cause a boot error (mirrors PII_REDACTION_MODE pattern)."
    - "All chunk-loop sites (engine.Collect, anthropic.CollectAnthropicChat, anthropic SSE, ollama NDJSON, openai SSE) go through the same engine.RangeChunksWithIdleTimeout helper so semantics cannot drift between surfaces."
    - "Idle fire emits a WARN-level structured log stream.idle_timeout with attrs surface, session_id, elapsed_ms, request_id."
    - "Operators get a --idle-timeout SEC flag on scripts/otto-gw (POSIX) and -IdleTimeout INT on scripts/otto-gw.ps1 (Windows parity)."
  artifacts:
    - path: internal/engine/idle.go
      provides: "engine.RangeChunksWithIdleTimeout helper plus engine.ErrStreamIdleTimeout sentinel"
    - path: internal/engine/idle_test.go
      provides: "Unit test for the helper using a never-producing fake Stream"
    - path: internal/config/config.go
      provides: "StreamIdleTimeoutSec field, env parse, default 30, 0 disables, negative/non-int fails boot"
    - path: scripts/.env.otto-gw.example
      provides: "Documented STREAM_IDLE_TIMEOUT_SEC env var block"
  key_links:
    - from: cmd/otto-gateway/main.go
      to: internal/engine/idle.go
      via: "cfg.StreamIdleTimeoutSec is converted to time.Duration once and threaded into engine.Config plus each adapter Config"
      pattern: "StreamIdleTimeout"
    - from: internal/adapter/anthropic/sse.go (runSSEEmitterLoop)
      to: internal/engine/idle.go
      via: "select adds a fourth arm whose timer/Reset semantics match the engine helper exactly"
      pattern: "ErrStreamIdleTimeout|idleTimer"
---

OBJECTIVE

Add a configurable server-side idle-stream watchdog at every chunk-receive loop so a stuck kiro (no chunks emitted, no client write attempts, no broken-pipe signal) cannot hold a pool slot indefinitely. Today the engine watchdog (context.AfterFunc at internal/engine/engine.go:207-217) only fires when the request ctx terminates — but no chunks means no writes means no client disconnect, so the slot stays held until the kiro process dies on its own.

Purpose: Free the pool slot on stuck kiro within a configurable bound (default 30s; 0 disables) without touching the pool, the hook chain, or the PII machinery. Surface a clean error to the client and a WARN log for operators.

Output: One helper in internal/engine, one config field plus boot validator, five chunk-loop sites converted to use the helper (engine.Collect, anthropic.CollectAnthropicChat, anthropic SSE, ollama NDJSON, openai SSE), one new env var (STREAM_IDLE_TIMEOUT_SEC), one new flag pair (--idle-timeout / -IdleTimeout). 3-4 atomic commits.

EXECUTION CONTEXT

@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md

CONTEXT

@.planning/STATE.md
@./CLAUDE.md

Background — why this exists (today's live repro). The engine context.AfterFunc watchdog at internal/engine/engine.go:207-217 only fires when the request ctx terminates. When kiro hangs and emits zero chunks, the SSE emitter never tries to write (no chunks → no writes → no broken-pipe error → streamCtx never gets canceled by the adapter's defer cancelFn). Quick 260531-ra6 SUMMARY explicitly flagged this: "stuck-slot symptom seen in live testing has a different root cause (watchdog likely not firing on silent kiro) — follow-up investigation needed". This quick IS that follow-up.

Chunk-loop sites (the 5 places that need the helper):

@internal/engine/collect.go                  # lines 56-99 — generic engine.Collect chunk loop (Ollama non-stream, OpenAI non-stream)
@internal/adapter/anthropic/collect.go       # lines 91-128 — anthropic.CollectAnthropicChat loop (D-07 exception)
@internal/adapter/anthropic/sse.go           # lines 656-691 — runSSEEmitterLoop (Anthropic streaming, PRIMARY tested surface)
@internal/adapter/ollama/ndjson.go           # lines 386-413 — runNDJSONEmitter (Ollama streaming)
@internal/adapter/openai/sse.go              # lines 415-436 — runSSEEmitter (OpenAI streaming)

Engine plus Stream interface (shape the helper consumes):

@internal/engine/engine.go                   # Stream interface lines 56-64 plus Run+watchdog 155-226

Config pattern reference (PII_REDACTION_MODE — boot-error-on-invalid):

@internal/config/config.go                   # lines 138-150 (struct field) plus 281-292 (validation) plus 670-685 (getEnvInt)

Main wiring — where Config gets propagated to each adapter Config struct:

@cmd/otto-gateway/main.go                    # lines 287-293 (engine.New) plus 287-440 (adapter construction) plus the EngineForSession closures 346-372

Adapter Config structs (each gets a StreamIdleTimeout field added):

@internal/adapter/anthropic/adapter.go       # Config struct lines 109-126
@internal/adapter/ollama/adapter.go          # Config struct (grep for "type Config struct")
@internal/adapter/openai/adapter.go          # Config struct (grep for "type Config struct")

Wrapper script patterns (mirror --pii / --trace style for --idle-timeout):

@scripts/otto-gw                             # parse_flags lines 195-216 plus apply_cli_flags lines 172-188 plus usage lines 988-1046
@scripts/otto-gw.ps1                         # head lines 1-60 (param block)
@scripts/.env.otto-gw.example                # head lines 1-50 (structure to mirror)

TASKS

TASK 1 — Add engine.RangeChunksWithIdleTimeout helper plus unit test
Files: internal/engine/idle.go, internal/engine/idle_test.go
Type: auto, tdd=true

Behavior contract:
- Helper signature: func RangeChunksWithIdleTimeout(ctx context.Context, stream Stream, idle time.Duration, onChunk func(canonical.Chunk) error) error
- When idle == 0: behaves identically to a bare for chunk := range stream.Chunks() loop with onChunk dispatch and ctx.Done short-circuit. NO timer is created (zero-cost when disabled).
- When idle > 0: builds a time.NewTimer(idle) and selects on ctx.Done / chunks / timer.C.
- On chunk arrival: stops the timer drain-safely (the standard `if !t.Stop() { <-t.C }` pattern), invokes onChunk(c). If onChunk returns an error, propagate it (does NOT swallow). Resets the timer for the next idle window.
- On chunks channel close: returns nil (clean stream end). Caller still invokes stream.Result() afterward.
- On ctx.Done: returns `fmt.Errorf("engine: idle range ctx: %w", ctx.Err())`. Drain the timer cleanly before return.
- On timer fire: returns the sentinel `engine.ErrStreamIdleTimeout` (wrapped with elapsed: `fmt.Errorf("engine: stream idle timeout after %s: %w", idle, ErrStreamIdleTimeout)`). Define `var ErrStreamIdleTimeout = errors.New("stream idle timeout")` at package level so callers can errors.Is-check it.

Tests (5, all timeouts ≤200ms, all t.Parallel()):
- TestRangeChunksWithIdleTimeout_FiresOnSilentStream — fake Stream whose Chunks() returns a channel that never produces. idle=50ms. Expect errors.Is(err, ErrStreamIdleTimeout) within 200ms.
- TestRangeChunksWithIdleTimeout_ChunkResetsTimer — fake Stream emits 5 chunks at 25ms intervals then closes. idle=50ms. Expect nil error, onChunk called 5 times.
- TestRangeChunksWithIdleTimeout_ZeroDisables — fake Stream never produces. idle=0. Run with a hard-deadline ctx (50ms). Expect ctx.Err() wrapped, NOT ErrStreamIdleTimeout (proves the timer path is bypassed).
- TestRangeChunksWithIdleTimeout_OnChunkErrorPropagates — onChunk returns errors.New("boom") on first chunk. Expect that error returned verbatim (not wrapped, not swallowed).
- TestRangeChunksWithIdleTimeout_CtxCancelBeatsIdle — idle=200ms, ctx canceled at 25ms with never-producing stream. Expect ctx.Err() wrap, NOT ErrStreamIdleTimeout.

Action: Create internal/engine/idle.go with the helper described above. Use the package-local Stream interface (no new types). Use time.NewTimer (not time.After — time.After leaks until the duration elapses, time.NewTimer+Stop is the established Go pattern; grep internal/acp for prior examples). Export ErrStreamIdleTimeout at package level so adapters can errors.Is-check it.

Create internal/engine/idle_test.go with the 5 tests. Use a local helper `type fakeStream struct { ch chan canonical.Chunk; result *canonical.FinalResult; err error }` with Chunks() returning ch and Result() returning result/err. Do NOT introduce testify — stdlib testing only (project convention).

Strictly engine-package internal. Do NOT export anything beyond RangeChunksWithIdleTimeout and ErrStreamIdleTimeout. No adapter imports yet (Task 3 wires those).

Verify: cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && go test ./internal/engine/ -run TestRangeChunksWithIdleTimeout -race -count=1 -v

Done: All 5 tests pass under -race. go build ./... clean. The helper is internal-engine-only with no adapter consumers yet. Single atomic commit: "feat(engine): add RangeChunksWithIdleTimeout helper for chunk-loop idle watchdog".

---

TASK 2 — Config plumbing: StreamIdleTimeoutSec env field, boot validation, adapter/engine Config wiring
Files: internal/config/config.go, internal/config/config_test.go, internal/engine/engine.go, cmd/otto-gateway/main.go, internal/adapter/anthropic/adapter.go, internal/adapter/ollama/adapter.go, internal/adapter/openai/adapter.go
Type: auto, tdd=true

Behavior contract:
- New config.Config field: StreamIdleTimeoutSec int (raw seconds at the env layer).
- getEnvInt("STREAM_IDLE_TIMEOUT_SEC", 30) — unset = 30. Set-but-not-int = wrapped boot error via existing getEnvInt path.
- Validation: if StreamIdleTimeoutSec < 0 → errs = append(errs, fmt.Errorf("STREAM_IDLE_TIMEOUT_SEC: must be >= 0, got %d", n)). Zero is VALID (disables).
- engine.Config gains StreamIdleTimeout time.Duration. Engine reads it from e.cfg.StreamIdleTimeout in Collect (and in any future internal chunk loop). New() accepts it without validation (a zero value means "disabled" — matches the helper contract).
- Adapter Config structs (anthropic.Config, ollama.Config, openai.Config) each gain StreamIdleTimeout time.Duration with a doc comment: "StreamIdleTimeout is the duration to wait for a chunk before tearing down the stream. Zero disables. Loaded from STREAM_IDLE_TIMEOUT_SEC and converted to Duration in main.go."
- main.go converts: streamIdle := time.Duration(cfg.StreamIdleTimeoutSec) * time.Second once, passes that Duration into engine.New AND into each adapter's New Config block. Also pass through the EngineForSession closures (lines 346-372) so per-session engines get the same timeout via the engine.Config they construct.

Config tests:
- TestLoad_StreamIdleTimeoutSec_Default — env unset → cfg.StreamIdleTimeoutSec == 30
- TestLoad_StreamIdleTimeoutSec_Explicit — env "60" → 60
- TestLoad_StreamIdleTimeoutSec_Zero — env "0" → 0 (no error)
- TestLoad_StreamIdleTimeoutSec_Negative — env "-5" → boot error containing "must be >= 0"
- TestLoad_StreamIdleTimeoutSec_NonInt — env "abc" → boot error from getEnvInt ("cannot parse")

Action steps:
- Step 1 (config.go): Add StreamIdleTimeoutSec int to the Config struct (place near PoolSize for cognitive locality). In Load(), after the SESSION_TICK_INTERVAL_MS block: streamIdleTimeoutSec, err := getEnvInt("STREAM_IDLE_TIMEOUT_SEC", 30) with the standard errs = append pattern. Add the negative-value check immediately after (mirrors validatePIIMode pattern). Add the field to the Config return literal.
- Step 2 (config_test.go): Add the 5 tests above. Use t.Setenv to set/clear the env var (avoids os.Setenv leakage). Negative and NonInt tests assert err != nil plus err.Error() contains the documented substring.
- Step 3 (engine.go): Add StreamIdleTimeout time.Duration to engine.Config (next to DefaultCWD). The engine.New constructor accepts it without validation. The Engine struct stores cfg, so reads go through e.cfg.StreamIdleTimeout — no struct mutation needed.
- Step 4 (each adapter.go): Add StreamIdleTimeout time.Duration to anthropic.Config, ollama.Config, openai.Config with the doc comment from the contract. Place next to existing operational fields (KiroCWD is fine). Do NOT consume it in handlers/sse yet — Task 3 does that. The struct field exists, the constructor accepts it, but nothing reads it yet.
- Step 5 (main.go): After cfg validation but before adapter construction (around line 287), add streamIdle := time.Duration(cfg.StreamIdleTimeoutSec) * time.Second. Pass StreamIdleTimeout: streamIdle into engine.New at line 287, into each EngineForSession closure's engine.New at lines 346-372, and into each adapter.New Config literal (ollama.New, anthropic.New, openai.New). Verify the "time" import already exists (it does for warmupDeadline).

Strict scope: surfaces only carry the Duration. Adapters do not know the env var name.

Verify: cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && go test ./internal/config/ -run TestLoad_StreamIdleTimeoutSec -race -count=1 -v && go build ./... && GOOS=windows go build ./...

Done: 5 config tests pass under -race. go build ./... clean on darwin/amd64 AND windows. Single atomic commit: "feat(config): add STREAM_IDLE_TIMEOUT_SEC with default 30, 0 disables, wire into engine and adapter Config".

---

TASK 3 — Wire the helper into all 5 chunk-loop sites plus per-surface error frames plus idle log marker
Files: internal/engine/collect.go, internal/adapter/anthropic/collect.go, internal/adapter/anthropic/sse.go, internal/adapter/anthropic/handlers.go, internal/adapter/ollama/ndjson.go, internal/adapter/ollama/handlers.go, internal/adapter/openai/sse.go, internal/adapter/openai/handlers.go, internal/adapter/anthropic/sse_test.go, internal/adapter/ollama/ndjson_test.go, internal/adapter/openai/sse_test.go, internal/adapter/anthropic/collect_test.go
Type: auto, tdd=true

Behavior contract:
- All 5 chunk loops route through engine.RangeChunksWithIdleTimeout (non-streaming sites) or replicate its semantics inline via a 4-arm select (streaming sites that also have a ping/keepalive ticker). For NON-streaming sites (engine.Collect, anthropic.CollectAnthropicChat), call the helper directly with an onChunk closure that runs the existing kind switch. For STREAMING sites with a ping ticker (anthropic SSE, ollama NDJSON, openai SSE), keep the existing outer select but ADD an idleTimer.C arm next to chunks/tickerC, gated by `if streamIdle > 0`.
- Streaming sites — idleTimer setup before the for loop:
  ```
  var idleTimer *time.Timer
  var idleC <-chan time.Time
  if streamIdle > 0 {
      idleTimer = time.NewTimer(streamIdle)
      defer idleTimer.Stop()
      idleC = idleTimer.C
  }
  ```
  A nil idleC is a never-ready channel arm (stdlib idiom) — when disabled, the case never fires. On chunk arrival in the chunks arm, drain+reset: if idleTimer != nil { if !idleTimer.Stop() { <-idleTimer.C }; idleTimer.Reset(streamIdle) }
- Anthropic streaming idle fire: emit event: error using the existing writeSSEError(e.w, e.flusher, errAPI, fmt.Sprintf("upstream stream idle for %ds", int(streamIdle.Seconds()))) then return aggregatedResponse + fmt.Errorf("anthropic: sse %w", engine.ErrStreamIdleTimeout). writeSSEError already emits the anthropic-shaped envelope (errors.go:107).
- Ollama streaming idle fire: emit a final NDJSON terminal line `{"error":"stream idle timeout","done":true}` (find the existing ollama error-line shape in ndjson.go — if there isn't one for mid-stream errors, this `done:true` shape matches the existing terminal pattern). Return aggregated response + wrapped ErrStreamIdleTimeout.
- OpenAI streaming idle fire: SSE data-frame shape — emit `data: {"error":{"message":"stream idle timeout","type":"api_error"}}\n\n` followed by `data: [DONE]\n\n`. Match openai/errors.go errorInner shape. Return aggregated response + wrapped ErrStreamIdleTimeout.
- Non-streaming Collect path: each adapter's non-streaming handler already wraps errors from Collect/CollectAnthropicChat into a 500. Add an errors.Is(err, engine.ErrStreamIdleTimeout) branch BEFORE the generic 500 that renders 504 Gateway Timeout with each surface's existing error envelope helper.
- Idle log marker: every site that detects an idle-fire calls logger.Warn("stream.idle_timeout", "surface", "<anthropic|ollama|openai>", "session_id", run.SessionID(), "elapsed_ms", streamIdle.Milliseconds(), "request_id", plugin.RequestIDFromContext(ctx)). WARN level (not Debug) — operators must notice. If plugin.RequestIDFromContext (the existing helper used by RequestIDHook) is not imported, add it.

Tests (one per loop site touched, all timeouts ≤100ms):
- internal/adapter/anthropic/sse_test.go: TestSSE_IdleTimeout_EmitsErrorFrame — fake Stream with never-producing Chunks(), streamIdle=100ms, assert response writer received an `event: error` frame, function returned err where errors.Is(err, engine.ErrStreamIdleTimeout) is true, aggregated response non-nil.
- internal/adapter/anthropic/sse_test.go: TestSSE_IdleTimeout_Disabled — streamIdle=0, never-producing stream, ctx hard-deadline 50ms, assert returns ctx.Err()-wrapped (NOT ErrStreamIdleTimeout) — proves disable works.
- internal/adapter/ollama/ndjson_test.go: TestNDJSON_IdleTimeout_EmitsErrorLine — analogous, ollama error-line shape.
- internal/adapter/openai/sse_test.go: TestSSE_IdleTimeout_EmitsErrorFrame — analogous, openai data-frame shape.
- internal/adapter/anthropic/collect_test.go: TestCollectAnthropicChat_IdleTimeout — fake engine with never-producing stream, streamIdle=100ms, assert errors.Is(err, engine.ErrStreamIdleTimeout).
- engine.Collect already has the helper tested directly in Task 1; the non-streaming OpenAI/Ollama paths use engine.Collect which now reads StreamIdleTimeout from engine.Config — no extra test required for those (the Task 1 tests cover the helper itself).

Action steps:
- Step 1 (internal/engine/collect.go): Replace the for chunk := range run.stream.Chunks() switch chunk.Kind {...} loop (lines 56-99) with a call to RangeChunksWithIdleTimeout(ctx, run.stream, e.cfg.StreamIdleTimeout, onChunk). The onChunk callback receives a canonical.Chunk and runs the existing switch (text → sb.WriteString, thought → thoughtSB.WriteString, tool_call → narration). On non-nil error, return nil, fmt.Errorf("engine: collect: %w", err).
- Step 2 (internal/adapter/anthropic/collect.go): Replace the chunk range loop (lines 91-128) with RangeChunksWithIdleTimeout. The onChunk closure does the existing kind switch (text → sb, thought → thoughtSB, tool_call → toolParts/toolCalls append). Change the CollectAnthropicChat signature to accept the streamIdle Duration: CollectAnthropicChat(ctx context.Context, eng Engine, req *canonical.ChatRequest, streamIdle time.Duration). Update collect_test.go callers to pass an appropriate test-specific duration (zero in most existing tests; 100ms in the new idle test).
- Step 3 (internal/adapter/anthropic/sse.go): Add the fourth select arm to runSSEEmitterLoop per the streaming idleTimer pattern above. Thread streamIdle in: change runSSEEmitter signature to accept it, change runSSEEmitterLoop signature to accept it, plumb from handlers.go where the streaming branch calls runSSEEmitter (handlers.go:202 — add a.cfg.StreamIdleTimeout as an arg).
- Step 4 (internal/adapter/anthropic/handlers.go): Plumb a.cfg.StreamIdleTimeout into the runSSEEmitter call (streaming branch line ~202) AND into the CollectAnthropicChat call (non-streaming branch line ~239). Add an explicit errors.Is(err, engine.ErrStreamIdleTimeout) check BEFORE the generic 500 in BOTH branches — non-streaming returns 504 via writeError(w, http.StatusGatewayTimeout, errAPI, "upstream stream idle timeout"); streaming has already emitted the error frame so just log at debug ("anthropic: sse stream idle timeout" with session_id).
- Step 5 (internal/adapter/ollama/ndjson.go and handlers.go): Same select-arm-addition pattern as Step 3, ollama error-line shape on fire. Thread streamIdle through runNDJSONEmitter signature. Update internal/adapter/ollama/handlers.go callers plus add errors.Is 504 mapping on the non-streaming branch.
- Step 6 (internal/adapter/openai/sse.go and handlers.go): Same pattern, openai SSE data-frame shape on fire. Thread streamIdle in. Update handlers.go callers plus add 504 mapping on the non-streaming branch.
- Step 7 (tests): Add one per surface as described. Tests use a fakeStream that never sends on Chunks() and returns a synthetic FinalResult on Result(). Test the streaming sites by calling runSSEEmitterLoop / runNDJSONEmitter directly (mirror the existing sse_test.go / ndjson_test.go harness). For the non-streaming Anthropic Collect test, use a fake Engine that returns a fake Run/Stream with a never-producing Chunks().
- Step 8 (strict scope guards): do NOT touch the PII hook chain. Do NOT refactor the SSE emitter beyond adding the select arm plus the timer plumbing. Do NOT introduce a new error type besides the engine.ErrStreamIdleTimeout sentinel from Task 1. Do NOT change the public shape of canonical types. The pool slot releases on idle-timeout via the existing AfterFunc watchdog firing on streamCtx cancel from the adapter's defer cancelFn at handlers.go:178 — verified by quick 260531-ra6's TestPool_Cancel_ReleasesSlot_WithoutResultDrain. The idle helper triggers this existing mechanism; it does NOT reach into the pool directly.

Verify: cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && go test ./internal/adapter/... ./internal/engine/... ./internal/pool/... ./internal/config/... -race -count=1 && go build ./... && GOOS=windows go build ./...

Done: All new tests pass under -race. Full adapter+engine+pool+config test suite green under -race. go build ./... clean on darwin AND windows. 5 chunk-loop sites converted; all use the same engine.RangeChunksWithIdleTimeout helper or its exact replicated select-arm semantics. 504 maps correctly on the non-streaming paths. Idle WARN logs include surface/session_id/elapsed_ms/request_id. Single atomic commit: "feat(adapters): wire stream-idle watchdog into all chunk-loop sites" — or split into 2 commits if the diff is awkward (engine+anthropic first, then ollama+openai).

---

TASK 4 — Wrapper plus env-example doc plus --idle-timeout flag (POSIX + PowerShell parity)
Files: scripts/.env.otto-gw.example, scripts/otto-gw, scripts/otto-gw.ps1
Type: auto

Action steps:
- Step 1 (scripts/.env.otto-gw.example): Add a documented STREAM_IDLE_TIMEOUT_SEC block. Mirror the structure of the existing POOL_SIZE block. Place it after POOL_SIZE / before the hook chain section. Block text (leave the actual env line commented — matches default-is-fine convention used for POOL_SIZE):

  # --- Stream idle watchdog -------------------------------------------------
  # Server-side idle timeout (in seconds) for the chunk-receive loop. If kiro
  # produces no chunks within this window, the gateway tears down the stream,
  # emits a terminal error frame to the client, and frees the pool slot.
  # Default 30. Set to 0 to disable (legacy hang-forever behavior). Negative
  # or non-integer values cause a boot error.
  # STREAM_IDLE_TIMEOUT_SEC=30

- Step 2 (scripts/otto-gw): Add the --idle-timeout SEC flag.
    - In parse_flags() at line ~195: add cases for --idle-timeout) and --idle-timeout=*) mirroring --hash-key exactly. Store in FLAG_IDLE_TIMEOUT.
    - In apply_cli_flags() at line ~172: add if [[ -n "${FLAG_IDLE_TIMEOUT:-}" ]]; then export STREAM_IDLE_TIMEOUT_SEC="$FLAG_IDLE_TIMEOUT"; fi
    - In the header comment block at line ~12 (the Gateway config flags section): add a --idle-timeout SEC line mirroring the existing --hash-key style.
    - In usage() at line ~1005 (the Gateway config flags section): add a --idle-timeout SEC line.
    - Run shellcheck on the result.

- Step 3 (scripts/otto-gw.ps1): Add -IdleTimeout INT parameter for Windows parity.
    - In the param() block at line ~28: add [int]$IdleTimeout = -1 (using -1 as the "not passed" sentinel since PSv5 nullable-int handling is finicky). Place near -ChatTrace.
    - Wherever the script applies CLI flags to $env variables (grep for $env:DEBUG = "true" to find the section), add: if ($IdleTimeout -ge 0) { $env:STREAM_IDLE_TIMEOUT_SEC = $IdleTimeout.ToString() }. The -ge 0 lets 0 (disabled) pass through.
    - Add a -IdleTimeout INT line to the comment block at the top mirroring the bash header.

- Step 4 (pwsh parse verification): Quick 260531-oax noted "pwsh parse unverified (not installed on macOS)". Document the same caveat in the commit message: "PowerShell parse not verified on macOS dev box; bash + shellcheck verified." Operators on Windows will hit the parse issue first if there is one.

Strict scope guards: do NOT add a new env file (the .example is updated in place). Do NOT touch the init_cmd subcommand — STREAM_IDLE_TIMEOUT_SEC has a sensible default and does not need an interactive prompt during init. Do NOT touch otto-gw.bat (the .bat is a pass-through dispatcher).

Verify: cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && shellcheck scripts/otto-gw && grep -q "STREAM_IDLE_TIMEOUT_SEC" scripts/.env.otto-gw.example && grep -q "FLAG_IDLE_TIMEOUT" scripts/otto-gw && grep -q "IdleTimeout" scripts/otto-gw.ps1

Done: shellcheck clean on scripts/otto-gw. STREAM_IDLE_TIMEOUT_SEC appears in .env.otto-gw.example. FLAG_IDLE_TIMEOUT plumbing complete in scripts/otto-gw. IdleTimeout parameter present in scripts/otto-gw.ps1. Single atomic commit: "feat(scripts): add --idle-timeout / -IdleTimeout flag and document STREAM_IDLE_TIMEOUT_SEC".

---

VERIFICATION (overall)

- go build ./... clean (darwin/amd64)
- GOOS=windows go build ./... clean
- go test ./internal/adapter/... ./internal/engine/... ./internal/pool/... ./internal/config/... -race -count=1 clean
- shellcheck scripts/otto-gw clean
- All new regression tests run in under 5 seconds total (100ms-or-less per timeout)
- New regression tests: 4-5 total (one per loop site touched + config tests)

SUCCESS CRITERIA

- Live repro fixed: a hung kiro producing zero chunks no longer holds the pool slot beyond STREAM_IDLE_TIMEOUT_SEC seconds.
- Default 30s applies without any operator action.
- 0 disables (operators with non-kiro test rigs that legitimately stream silently can opt out).
- Single helper means semantics are identical across all 5 chunk-loop sites.
- WARN-level stream.idle_timeout log marker fires once per idle event with surface/session_id/elapsed_ms/request_id attrs.
- POSIX (scripts/otto-gw) and PowerShell (scripts/otto-gw.ps1) wrappers both support the flag.

OUTPUT

Create .planning/quick/260531-ruv-configurable-server-side-idle-stream-tim/260531-ruv-SUMMARY.md when done.
