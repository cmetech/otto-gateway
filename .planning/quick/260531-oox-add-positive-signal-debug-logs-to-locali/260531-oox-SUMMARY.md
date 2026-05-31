---
phase: quick-260531-oox
plan: 01
subsystem: observability
tags: [debug-logs, stall-localization, pii, engine, anthropic-sse, pool]
requires: []
provides:
  - "pii.redact.done DEBUG marker (active_recognizers + mode)"
  - "engine.new_session.ok DEBUG marker (session_id + cwd)"
  - "engine.prompt.sent DEBUG marker (session_id + blocks)"
  - "anthropic.sse.first_chunk DEBUG marker (session_id + kind, one-shot per stream)"
  - "pool.acquire + pool.release DEBUG markers (slot + session_id)"
affects: [internal/plugin/pii/pii.go, cmd/otto-gateway/main.go, internal/engine/engine.go, internal/adapter/anthropic/sse.go, internal/pool/pool.go]
tech-stack:
  added: []
  patterns:
    - "nil-default Logger field with unexported logger() helper (mirrors LoggingHook pattern)"
    - "one-shot per-stream boolean guard for first-event DEBUG markers"
    - "nil-safe Pool.debugLog helper since pool.Config.Logger is documented optional"
key-files:
  created:
    - .planning/quick/260531-oox-add-positive-signal-debug-logs-to-locali/260531-oox-SUMMARY.md
  modified:
    - internal/plugin/pii/pii.go
    - cmd/otto-gateway/main.go
    - internal/engine/engine.go
    - internal/adapter/anthropic/sse.go
    - internal/pool/pool.go
decisions:
  - "request_id intentionally omitted from pii.redact.done to avoid plugin→pii→plugin import cycle; operators correlate via timestamps + active_recognizers."
  - "Pool.debugLog wrapper added because cfg.Logger is documented optional (nil-tolerated); direct .Debug calls would panic in nil case."
  - "SetModel hop intentionally NOT instrumented (per plan §Task 2.3); follow-up quick task can add if it surfaces as a stall source."
  - "Lifecycle pool paths (Warmup/respawnSlot/removeSlot) intentionally NOT instrumented; they are startup/recovery, not per-request hops."
metrics:
  duration: "3m 37s"
  completed: "2026-05-31T21:54:05Z"
---

# Phase quick-260531-oox: Add Positive-Signal DEBUG Logs to Localize Stalled Chat Requests

One-liner: Five DEBUG markers (`pii.redact.done`, `engine.new_session.ok`, `engine.prompt.sent`, `anthropic.sse.first_chunk`, `pool.acquire`/`pool.release`) added to the request flow so an operator with `DEBUG=true` can localize a stalled chat request to one of four hops by reading the log timeline.

## What Was Built

Pure-observability instrumentation. Every code change is one of three shapes:
1. A `Logger *slog.Logger` field added to `PIIRedactionHook` (with unexported nil-default `logger()` helper).
2. A `Logger: logger` field assignment in `main.go` where `PIIRedactionHook` is constructed.
3. A single `Logger.Debug(...)` (or `e.cfg.Logger.Debug(...)` / `p.debugLog(...)`) call.

No new logic, no new branches, no new tests. The only added state is the one-shot `firstChunkSeen bool` in `runSSEEmitterLoop` so the first-chunk marker fires exactly once per stream.

### Task 1 — PIIRedactionHook + main.go wiring (`651400e`)

- Added `log/slog` import to `internal/plugin/pii/pii.go`.
- Added `Logger *slog.Logger` field to `PIIRedactionHook` struct.
- Added unexported `logger()` helper returning `h.Logger` or `slog.Default()` — mirrors the pattern in `internal/plugin/logging.go` (`LoggingHook.logger()`).
- Emits `pii.redact.done` DEBUG at the tail of `Before` with attrs `active_recognizers` (count, safe — same data /health/hooks publishes) + `mode`. Disabled-path early return stays silent (D-02 contract).
- `request_id` omitted with a one-line code comment explaining the cycle avoidance (`pii → plugin → pii`).
- `Logger: logger` wired into `&pii.PIIRedactionHook{...}` literal in `cmd/otto-gateway/main.go` line ~209.

### Task 2 — engine.go markers (`7abbae6`)

- `engine.new_session.ok` DEBUG immediately after the successful `NewSession` call, with `session_id` + `cwd`.
- `engine.prompt.sent` DEBUG immediately after the successful `Prompt` call, with `session_id` + `blocks` (count only — T-8-PII; never log the block content).
- `SetModel` hop NOT instrumented per plan §Task 2.3.

### Task 3 — Anthropic SSE first_chunk + pool acquire/release (`4269f9c`)

- Added `firstChunkSeen` one-shot bool to `runSSEEmitterLoop` in `internal/adapter/anthropic/sse.go`. Emits `anthropic.sse.first_chunk` DEBUG (with `session_id` + `kind`) the first time the chunks channel yields a value in a given stream.
- Added nil-safe `Pool.debugLog(msg, attrs...)` helper at the top of `internal/pool/pool.go` because `pool.Config.Logger` is documented optional ("A nil Logger is tolerated"). Direct `.Debug` calls on a nil `*slog.Logger` would panic; the wrapper guards.
- `pool.acquire` DEBUG emitted in `Pool.NewSession` right after the slot is received from `p.slots` (only the happy acquire path — the `ctx.Done()` arm returns above).
- `pool.release` DEBUG emitted in BOTH release sites:
  - The inline `release` closure in `Pool.Prompt` (covers Result-drained and ctx-cancel paths).
  - `Pool.releaseSlotForSession` (covers `Pool.Cancel` and the Prompt-error path).
- Lifecycle paths (`Warmup`, `respawnSlot`, `removeSlot`) intentionally NOT instrumented.

## Verification

All required gates pass:

```
$ go build ./...           # clean
$ go test ./internal/plugin/pii/... ./internal/engine/... \
          ./internal/adapter/anthropic/... ./internal/pool/... -count=1 -timeout 120s
ok  otto-gateway/internal/plugin/pii    0.703s
ok  otto-gateway/internal/engine        0.338s
ok  otto-gateway/internal/adapter/anthropic   0.424s
ok  otto-gateway/internal/pool          0.853s
```

All five DEBUG names found at expected sites:

```
$ grep -rn 'pii\.redact\.done' internal/plugin/pii/
  internal/plugin/pii/pii.go:271

$ grep -rn 'engine\.new_session\.ok\|engine\.prompt\.sent' internal/engine/
  internal/engine/engine.go:184, 200

$ grep -rn 'anthropic\.sse\.first_chunk' internal/adapter/anthropic/
  internal/adapter/anthropic/sse.go:684

$ grep -rn 'pool\.acquire\|pool\.release' internal/pool/
  internal/pool/pool.go:498  (acquire)
  internal/pool/pool.go:584, 660  (release × 2 sites)
```

## go vet ./... Findings

`go vet ./...` surfaces 11 pre-existing findings in `internal/admin/tail_test.go` (`testing.Context requires go1.24 or later (module is go1.23)`). These are NOT caused by this task — the affected file was not touched (see `git diff c889ab1..HEAD --name-only`). They are out of scope per the executor SCOPE BOUNDARY rule and logged here for visibility.

```
internal/admin/tail_test.go:165:28: testing.Context requires go1.24 or later (module is go1.23)
internal/admin/tail_test.go:192:29: ...
internal/admin/tail_test.go:193:29: ...
internal/admin/tail_test.go:232:28: ...
internal/admin/tail_test.go:282:28: ...
internal/admin/tail_test.go:334:28: ...
internal/admin/tail_test.go:396:28: ...
internal/admin/tail_test.go:458:28: ...
internal/admin/tail_test.go:503:32: ...
internal/admin/tail_test.go:508:32: ...
internal/admin/tail_timberjack_test.go:54:28: ...
```

Triage suggestion: bump the `go.mod` directive from 1.23 → 1.24 OR rewrite the `tail_test.go` calls to use the older `context.Background()` pattern. Out of scope for this task.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 — Blocking] Added nil-safe `Pool.debugLog` helper**
- **Found during:** Task 3 (pool instrumentation)
- **Issue:** `pool.Config.Logger` documents itself as optional ("A nil Logger is tolerated; the pool itself does not log"). Direct `p.cfg.Logger.Debug(...)` calls would panic on nil — and the plan §Task 3.4 explicitly says "If `pool.Config.Logger` does NOT exist … STOP." The field DOES exist but is nil-tolerated, which is a stricter precondition than the plan checked for.
- **Fix:** Added a 6-line unexported `Pool.debugLog(msg string, attrs ...any)` method that returns silently when `p.cfg.Logger == nil` and forwards to `Debug` otherwise. All three new pool DEBUG call sites use the helper. Tests that construct a bare `&Pool{}` without a logger no longer NPE.
- **Files modified:** `internal/pool/pool.go`
- **Commit:** `4269f9c`
- **Why this is in-scope:** The plan's only alternative was a sweeping TODO + skipping pool instrumentation entirely, which would have lost coverage of two of the four stall-localization hops. A 6-line nil-safe wrapper preserves the must_have artifact ("pool.acquire + pool.release DEBUG using p.cfg.Logger") with zero behavior risk.

No other deviations.

## Known Stubs

None.

## Threat Flags

None. Every new attribute is structural metadata (counts, session IDs, slot labels, cwd, kinds). No request content, no canonical PII values, no recognizer matches, no hash keys, no patterns. The `active_recognizers` count is already published by `/health/hooks` per Describe (T-8-LEAK aware).

## Commits

| # | Hash      | Task                                                                          |
|---|-----------|-------------------------------------------------------------------------------|
| 1 | `651400e` | feat(260531-oox): add pii.redact.done DEBUG + Logger field to PIIRedactionHook |
| 2 | `7abbae6` | feat(260531-oox): emit engine.new_session.ok + engine.prompt.sent DEBUG        |
| 3 | `4269f9c` | feat(260531-oox): emit anthropic.sse.first_chunk + pool.acquire/release DEBUG  |

## Self-Check: PASSED

- internal/plugin/pii/pii.go — FOUND (Logger field + logger() helper + pii.redact.done call)
- cmd/otto-gateway/main.go — FOUND (`Logger: logger` field assignment)
- internal/engine/engine.go — FOUND (engine.new_session.ok + engine.prompt.sent)
- internal/adapter/anthropic/sse.go — FOUND (firstChunkSeen bool + anthropic.sse.first_chunk)
- internal/pool/pool.go — FOUND (debugLog helper + pool.acquire + pool.release × 2)
- Commit 651400e — FOUND in git log
- Commit 7abbae6 — FOUND in git log
- Commit 4269f9c — FOUND in git log
