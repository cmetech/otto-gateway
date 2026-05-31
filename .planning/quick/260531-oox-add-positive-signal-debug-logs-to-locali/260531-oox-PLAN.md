---
phase: quick-260531-oox
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - internal/plugin/pii/pii.go
  - cmd/otto-gateway/main.go
  - internal/engine/engine.go
  - internal/adapter/anthropic/sse.go
  - internal/pool/pool.go
autonomous: true
requirements:
  - OBSV-stall-localization
must_haves:
  truths:
    - "With DEBUG=true, an operator can read the gateway log and localize a stalled chat request to one of four hops: PII Pre-hook, engine session setup, pool slot acquisition, or Anthropic SSE first chunk."
    - "Existing tests pass unchanged because slog at Debug level is a no-op when configured level is INFO."
    - "No behavior change: every new log line is a single side-effect-free Logger.Debug call."
  artifacts:
    - path: "internal/plugin/pii/pii.go"
      provides: "Logger field on PIIRedactionHook + pii.redact.done DEBUG at end of Before"
      contains: "pii.redact.done"
    - path: "cmd/otto-gateway/main.go"
      provides: "Logger wired into PIIRedactionHook construction"
      contains: "Logger: logger"
    - path: "internal/engine/engine.go"
      provides: "engine.new_session.ok + engine.prompt.sent DEBUG markers in Run"
      contains: "engine.new_session.ok"
    - path: "internal/adapter/anthropic/sse.go"
      provides: "anthropic.sse.first_chunk one-shot DEBUG inside runSSEEmitterLoop"
      contains: "anthropic.sse.first_chunk"
    - path: "internal/pool/pool.go"
      provides: "pool.acquire + pool.release DEBUG using p.cfg.Logger"
      contains: "pool.acquire"
  key_links:
    - from: "cmd/otto-gateway/main.go"
      to: "internal/plugin/pii/pii.go (PIIRedactionHook.Logger)"
      via: "construction-site field assignment"
      pattern: "PIIRedactionHook\\{[^}]*Logger:"
---

<objective>
Add positive-signal DEBUG log markers at four request-flow boundaries so that, with DEBUG=true, an operator can localize a stalled chat request (PII-bearing message that hangs) to a single hop by reading the log timeline. Pure observability change — no behavior change, no new tests.

Purpose: ChatTraceHook.After only fires on chain completion, so a hung request currently produces a `pre_chain_in` NDJSON line and silence. The four targets below (PII hook completion, engine NewSession success, engine Prompt success, Anthropic SSE first chunk, pool slot acquire/release) bracket every hop a request can stall in.

Output: Five Go source files modified with `Logger.Debug` calls and one Logger field added to `PIIRedactionHook`. No tests, no docs, no SUMMARY (executor handles SUMMARY).
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@.planning/STATE.md
@./CLAUDE.md

# Files this plan modifies — read sections referenced in <action> bodies, not whole files:
@internal/plugin/pii/pii.go
@cmd/otto-gateway/main.go
@internal/engine/engine.go
@internal/adapter/anthropic/sse.go
@internal/pool/pool.go

# Pattern reference (nil-fallback Logger field) — read only the struct + nil-fallback block:
@internal/plugin/logging.go
</context>

<tasks>

<task type="auto">
  <name>Task 1: Add Logger to PIIRedactionHook + emit pii.redact.done DEBUG, wire Logger in main.go</name>
  <files>internal/plugin/pii/pii.go, cmd/otto-gateway/main.go</files>
  <action>
In `internal/plugin/pii/pii.go`:

1. Add `"log/slog"` to the import block (currently only imports `context`, `otto-gateway/internal/canonical`, `otto-gateway/internal/engine`).

2. Add a new field to the `PIIRedactionHook` struct (around lines 71-77) AFTER `EnabledEntities`:
   - Field name: `Logger`
   - Type: `*slog.Logger`
   - Comment: optional; nil-falls-back to `slog.Default()` at first use. Wired by main.go for the production-path adapter. Tests may leave nil — defensive fallback keeps the hook side-effect-free.

3. Add a small unexported helper method on `*PIIRedactionHook` named `logger()` that returns `h.Logger` when non-nil, else `slog.Default()`. Mirror the exact pattern used in `internal/plugin/logging.go` around lines 104-109 (LoggingHook's lazy-default logger lookup). Place it just below `activeRecognizers` (around line 152) so the public API surface (Name/Describe/Before) stays at the top of the file.

4. In `Before` (line 174), after the `Enabled` short-circuit gate but BEFORE the redaction work (i.e., insert the DEBUG line at the very END of `Before`, right before the final `return nil, nil` at line 253), emit one DEBUG line. Use the log name convention from the codebase background:

   - Log name: `"pii.redact.done"`
   - Attributes (slog key/value pairs):
     - `"request_id"` — from `plugin.RequestIDFromContext(ctx)`. NOTE: that helper lives in package `internal/plugin` which package `pii` does NOT currently import. Do NOT add a `pii → plugin` import cycle. Instead omit `request_id` and document the omission with a one-line code comment: `// request_id intentionally omitted to avoid plugin→pii→plugin import cycle; correlate via timestamps + active_recognizers count.`
     - `"active_recognizers"` — `len(recs)` (the `activeRecognizers()` slice already computed at line 188)
     - `"mode"` — `h.Mode`
   - Use `h.logger().Debug(...)`.

   Important: do NOT log the redaction counter map, do NOT log canonical values, do NOT log the summary. T-8-LEAK forbids publishing recognizer matches in non-aggregated form. The `active_recognizers` count is safe (it is the same data `/health/hooks` already publishes).

5. The disabled-path early return at line 175-177 stays silent — when `!h.Enabled`, the hook is a total no-op by D-02 contract, and emitting a DEBUG line there would falsely suggest the hook ran. (This is intentional; do not "be helpful" and add a log there.)

In `cmd/otto-gateway/main.go`:

6. At the `PIIRedactionHook` struct literal around line 203, add `Logger: logger,` as a new field assignment. The local variable `logger` (an `*slog.Logger`) is already in scope at that point — RequestIDHook on line 201 uses the same identifier.

Style: match the existing `slog.Debug(name, "k", v, "k2", v2)` namespaced-colon style (see engine.go line 208 `"engine: watchdog: ..."`). Note: the spec in background_for_planner uses a DOT-separated form `pii.redact.done` rather than the COLON form `pii: redact: done` — keep the DOT form for these new names because the operator audit grep target was specified that way. The new names form a stable observability vocabulary distinct from the existing `pkg: subsystem: msg` narrative log style.
  </action>
  <verify>
    <automated>cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && go build ./... && go vet ./internal/plugin/pii/... ./cmd/otto-gateway/... && grep -c "pii.redact.done" internal/plugin/pii/pii.go | grep -v '^0$' && grep -c "Logger: logger" cmd/otto-gateway/main.go | grep -v '^0$'</automated>
  </verify>
  <done>
- `internal/plugin/pii/pii.go` imports `log/slog`, `PIIRedactionHook` has a `Logger *slog.Logger` field, an unexported `logger()` helper with nil-default-to-slog.Default() exists, and `Before` emits exactly one `h.logger().Debug("pii.redact.done", ...)` at the tail of the active code path with attrs `active_recognizers` (count) and `mode`.
- `cmd/otto-gateway/main.go` passes `Logger: logger` into the `PIIRedactionHook{}` literal.
- `go build ./...` succeeds, `go vet` clean. Existing tests untouched.
  </done>
</task>

<task type="auto">
  <name>Task 2: Emit engine.new_session.ok + engine.prompt.sent DEBUG in engine.Run</name>
  <files>internal/engine/engine.go</files>
  <action>
In `internal/engine/engine.go`, function `Engine.Run` (starts at line 155):

1. After the successful `NewSession` call (immediately after line 183, BEFORE the SetModel block at line 186), emit:
   ```
   e.cfg.Logger.Debug("engine.new_session.ok", "session_id", sid, "cwd", cwd)
   ```
   This is the positive-signal twin to the existing failure path at line 181-183. Operator sees `engine.new_session.ok` followed by silence → stall is between ACP NewSession returning and the next hop (SetModel or Prompt). The `cwd` attr is cheap and answers "did we resolve to the right working dir" without scrolling.

2. After the successful `Prompt` call (immediately after line 198, BEFORE the watchdog `context.AfterFunc` registration at line 205), emit:
   ```
   e.cfg.Logger.Debug("engine.prompt.sent", "session_id", sid, "blocks", len(blocks))
   ```
   Do NOT log the block content (T-8-PII: blocks contain user-input text after PII redaction, but logging them re-introduces a leak surface that DEBUG-by-itself should not). The `len(blocks)` attr is the maximally-useful safe signal.

3. Do NOT add a DEBUG line for the SetModel call. The existing SetModel path is rarely the stall source (kiro responds promptly to model switches) and adding it would clutter the timeline. If the operator finds SetModel IS the stall source, a follow-up quick task will instrument it.

4. The existing watchdog DEBUG lines (lines 208/210/212) stay unchanged.

Style: dot-separated names match Task 1 (`engine.new_session.ok`, `engine.prompt.sent`), distinct from the existing `engine: watchdog: ...` narrative format. The new names are observability markers; existing ones are diagnostic narration.
  </action>
  <verify>
    <automated>cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && go build ./... && go test ./internal/engine/... -count=1 -timeout 30s && grep -c 'engine\.new_session\.ok\|engine\.prompt\.sent' internal/engine/engine.go | grep -vx 0</automated>
  </verify>
  <done>
- `Engine.Run` emits `engine.new_session.ok` (with session_id + cwd) on the success path immediately after `NewSession`.
- `Engine.Run` emits `engine.prompt.sent` (with session_id + blocks count) on the success path immediately after `Prompt`.
- Existing engine tests pass unchanged.
  </done>
</task>

<task type="auto">
  <name>Task 3: Emit anthropic.sse.first_chunk one-shot + pool.acquire/release DEBUG</name>
  <files>internal/adapter/anthropic/sse.go, internal/pool/pool.go</files>
  <action>
In `internal/adapter/anthropic/sse.go`, function `runSSEEmitterLoop` (starts at line 656):

1. Add a local bool `firstChunkSeen` declared at the top of the function body (before the `chunks := run.Stream().Chunks()` line on 657). One-shot guard: fires exactly once per stream so a high-throughput response does not flood the log.

2. Inside the `case c, ok := <-chunks:` branch (line 674), AFTER the `if !ok` close-handling at lines 675-677 and BEFORE the `if err := e.applyChunk(c); ...` at line 678, add:
   ```
   if !firstChunkSeen {
       firstChunkSeen = true
       e.logger.Debug("anthropic.sse.first_chunk", "session_id", run.SessionID(), "kind", c.Kind)
   }
   ```
   Operator sees `engine.prompt.sent` then silence (no `anthropic.sse.first_chunk`) → stall is between ACP Prompt accepting the blocks and kiro emitting the first chunk back. This is the highest-signal marker in the chain because it is the LAST point before the response surface starts streaming bytes.

3. The `c.Kind` attr is a `canonical.ChunkKind` (already used in the existing dropped-chunk debug log at line 324) so its slog rendering is established. No nil guards needed — `c` is a value type from a channel, kind has a zero value.

In `internal/pool/pool.go`:

4. The Pool has access to `p.cfg.Logger` (confirmed by `respawnSlot` using `p.cfg.Logger` indirectly via `acp.Config{Logger: p.cfg.Logger, ...}` at line 181 + 239; explicitly: `pool.Config` carries a `Logger` field — verify by reading pool/config.go briefly if needed). Use `p.cfg.Logger.Debug(...)` directly.

5. In `Pool.NewSession` (line 478), AFTER the slot is acquired from `p.slots` (line 481-485, immediately after the select block but BEFORE the `slotAlive` check at line 492), add:
   ```
   p.cfg.Logger.Debug("pool.acquire", "slot", slot.Label)
   ```
   This fires after a successful acquire (the ctx.Done path returns above and does not reach here, so this only emits on the happy acquire path). If `slot` is somehow nil, this would panic — but `slot.Label` is read shortly after in the dead-slot branch without a nil-guard, so an existing invariant already protects this access pattern.

6. In `Pool.releaseSlotForSession` (line 637), AFTER the slot has been successfully looked up and is about to be returned to `p.slots` (after the `p.slots <- slot` on line 645), add:
   ```
   p.cfg.Logger.Debug("pool.release", "slot", slot.Label, "session_id", sid)
   ```
   This is the single release path used by both `Pool.Cancel` and the Prompt-error path. The `release` closure inside `Pool.Prompt` (lines 557-571) is the OTHER release path — it fires on stream-Result and ctx-cancellation. Mirror the DEBUG there too: in the `release` closure body, immediately after `p.slots <- s` on line 570, add the same line with `s.Label`:
   ```
   p.cfg.Logger.Debug("pool.release", "slot", s.Label, "session_id", sid)
   ```
   Both release sites are needed because they cover the three Codex M-3 terminal paths described in the comment at lines 461-471. A missing log on either side would create a false-positive "pool slot stuck" reading.

7. Do NOT instrument `Warmup`, `respawnSlot`, or `removeSlot` — those are lifecycle (startup/recovery) paths, not request-stall hops. Adding noise there obscures the per-request acquire/release timeline.

If `pool.Config.Logger` does NOT exist (a possibility flagged in background_for_planner), STOP and surface the omission: skip pool instrumentation, add a one-line comment at the top of `pool.go` saying `// TODO(260531-oox): pool slot acquire/release not instrumented — pool.Config has no Logger field; refactoring deferred.` and adjust the must_haves table at the top of this plan to record the omission. Do not refactor pool just for logs.
  </action>
  <verify>
    <automated>cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway && go build ./... && go test ./internal/adapter/anthropic/... ./internal/pool/... -count=1 -timeout 60s && grep -c 'anthropic\.sse\.first_chunk' internal/adapter/anthropic/sse.go | grep -vx 0 && grep -c 'pool\.acquire\|pool\.release' internal/pool/pool.go | grep -vx 0</automated>
  </verify>
  <done>
- `runSSEEmitterLoop` declares a `firstChunkSeen` bool and fires exactly one `anthropic.sse.first_chunk` DEBUG (with session_id + kind) on the first chunk received per stream.
- `Pool.NewSession` emits `pool.acquire` (with slot label) after a successful acquire.
- Both release sites (`Pool.releaseSlotForSession` and the `release` closure inside `Pool.Prompt`) emit `pool.release` (with slot label + session_id).
- Existing anthropic + pool tests pass unchanged.
- (Conditional) If `pool.Config.Logger` is absent, pool instrumentation is skipped and a TODO comment + must_haves adjustment is recorded — DO NOT silently drop the requirement.
  </done>
</task>

</tasks>

<verification>
Full repository checks after all three tasks land:

```
cd /Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway
go build ./...
go vet ./...
go test ./... -count=1 -timeout 120s
```

All must pass with zero new failures. The five new log names are present (one grep per name):

```
grep -rn 'pii\.redact\.done' internal/plugin/pii/
grep -rn 'engine\.new_session\.ok\|engine\.prompt\.sent' internal/engine/
grep -rn 'anthropic\.sse\.first_chunk' internal/adapter/anthropic/
grep -rn 'pool\.acquire\|pool\.release' internal/pool/
```

Smoke test (manual, OPTIONAL — operator runs after merge to confirm the stall-localization story):

```
DEBUG=true ./otto-gw start
# send a chat message containing a fake email
# tail the log; expect this sequence on a healthy request:
#   pii.redact.done      active_recognizers=6 mode=replace
#   pool.acquire         slot=slot-0
#   engine.new_session.ok  session_id=...  cwd=...
#   engine.prompt.sent   session_id=... blocks=N
#   anthropic.sse.first_chunk  session_id=... kind=Text
#   pool.release         slot=slot-0  session_id=...
# A stall manifests as the sequence truncating at one of these markers.
```
</verification>

<success_criteria>
- All five new DEBUG log names emit on the happy path (verified by reading code, not by adding tests).
- No existing test broken; `go test ./... -count=1` passes.
- No new behavior: every code change is either a struct-field addition with nil-default, a Logger wiring line in main.go, or a single `Logger.Debug` call.
- `pool.Config.Logger` reachability handled per Task 3 fallback (no refactor of pool just for logs).
- PII hook does NOT log redaction matches, canonical values, or summary — only the aggregate `active_recognizers` count + `mode`.
</success_criteria>

<output>
Create `.planning/quick/260531-oox-add-positive-signal-debug-logs-to-locali/260531-oox-SUMMARY.md` when done (executor handles this — do NOT pre-write).
</output>
