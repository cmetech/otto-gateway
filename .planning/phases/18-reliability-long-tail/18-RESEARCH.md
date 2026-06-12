# Phase 18: Reliability long-tail — Research

**Researched:** 2026-06-11
**Domain:** Go reliability hardening — config validation, observability symmetry, tray honesty
**Confidence:** HIGH (codebase grounded; every site read at the line referenced in CONTEXT.md)

## Summary

CONTEXT.md already locks the WHAT for all 10 D-IDs. This research grounds each
locked decision in the existing codebase: which file/line each fix attaches to,
which existing pattern it must mirror, and which test scaffolding (fakeClient,
captureSlogDefault, goleak.VerifyNone) is already available to back the
TDD-mode RED→GREEN test commits.

Three findings worth surfacing to the planner before tasks are written:

1. **`engine.callPreHookSafe` at `internal/engine/engine.go:317-329` is the
   canonical panic-recover template** Phase 18-02 D-18-07 must replicate. It
   already logs at `Error` with `"err"` + `"stack", string(debug.Stack())`
   and uses `runtime/debug.Stack()`. No new helper is required; copy the
   shape into each of the three goroutine bodies.

2. **D-18-07 "engine watchdog" is ambiguous**: the engine's `context.AfterFunc`
   call at `engine.go:255-265` is NOT a goroutine the engine package owns
   (`AfterFunc` is Go-runtime managed; there is no `go func()` to wrap). The
   three actual goroutines without panic recovery are: admin tailer
   (`tail.go:217`), pool ctx-watcher (`pool.go:859`), and pool exit-watcher
   (`exit_watcher.go:32`). The planner should confirm whether the "engine
   watchdog" reference in CONTEXT.md means the exit-watcher (which is the
   closest engine-adjacent goroutine missing recovery) or whether a different
   site is intended. See Open Questions §1.

3. **`worker_pid` is already logged as `0` in Phase 15-02 REL-HTTP-03 sites**
   (`internal/adapter/ollama/ndjson.go:569` and `internal/adapter/openai/sse.go:573`)
   because `RunHandle` does not expose a PID accessor. D-18-06 must mirror this
   constraint — log `worker_pid: 0` as a placeholder, not try to thread a real
   PID through the engine→adapter boundary. Same for `bytes_streamed: 0`. The
   planner should NOT add interface-extension tasks; the existing constraint
   ladders directly into the new D-18-06 site.

**Primary recommendation:** Three plans, each pinned to its file set per
CONTEXT.md's "Cross-Plan Wires" table. TDD ordering: RED test commit → GREEN
fix commit per REQ-ID, except D-18-03 (bind-then-close) and D-18-10 (row
removal) where the test and fix are tightly coupled and may land as one commit
per the v1.9 D-02 "unskip-in-same-commit" precedent.

## User Constraints (from CONTEXT.md)

### Locked Decisions

- **D-18-01 REL-CFG-05**: Warn + treat-as-unset for degenerate `ALLOWED_IPS` /
  `AUTH_TOKEN`. **No fail-fast.** Boot-time auth-state line at
  `cmd/otto-gateway/main.go:115-120` stays INFO. Pattern:
  `rawAllowed := strings.TrimSpace(os.Getenv("ALLOWED_IPS")); allowed := parseAllowlistCSV(rawAllowed); if rawAllowed != "" && len(allowed) == 0 { logger.Warn(...) }`.
- **D-18-02 REL-CFG-06**: Named config errors for `KIRO_CMD` /  `KIRO_CWD`.
  Tilde expansion for `KIRO_CWD` only (POSIX + Windows). `KIRO_CMD` does NOT
  get `~` expansion. Apply once during `config.Load()`; expanded path is what
  gets stored in `Config.KiroCWD`.
- **D-18-03 REL-CFG-07**: Bind-then-close port probe in `config.Load()` after
  `HTTP_ADDR` resolves. Error message: `config: HTTP_ADDR ("<addr>"): port already in use`.
  TOCTOU window acknowledged and accepted.
- **D-18-04 REL-OBSV-03**: Replace `cmd.Stderr = os.Stderr` at
  `internal/acp/client.go:289` with a `bufio.Scanner` goroutine; 1MB line cap;
  Warn level; fields `worker_pid`, `slot_id` (optional — omit if plumbing is
  non-trivial), `line`. Goroutine exits when pipe closes.
- **D-18-05 REL-OBSV-02**: Recovery log at the lazy-respawn success site
  (`Pool.respawnSlot`); INFO level; fields `slot_id`, `worker_pid` (new),
  `previous_pid`, `reason`.
- **D-18-06 REL-HTTP-06**: `slog.Warn("ollama: streaming eng.Run failed", ...)`
  BEFORE the error frame write; mirror Phase 15-02 REL-HTTP-03 field set
  (`worker_pid`, `kiro_exit_code`, `bytes_streamed`, `session_id`, `request_id`,
  `err`); best-effort fields default to `0` (matches existing pattern in
  `ndjson.go:567-577`).
- **D-18-07 REL-HTTP-07**: `defer recover()` at three goroutines: admin tailer
  (`internal/admin/tail.go` `Watch`/`run` loop), engine watchdog
  (location to be confirmed — see Open Questions §1), pool ctx-watcher
  (`internal/pool/pool.go` per-session ctx done watcher at line 859). Log at
  `Error` with `site`, `panic`, `stack` fields. Goroutine exits cleanly after
  recover — no auto-restart.
- **D-18-08 REL-OBSV-04**: Add `Config.AdminTailPath` field populated once in
  `config.Load()` via the existing `deriveChatTraceFile(logFileForDerive)`
  helper. Writer and tailer read from this field exclusively. Tailer open
  failures log at WARN (not Debug) with `path=<resolved>`.
- **D-18-09 REL-TRAY-08**: Reuse `StateError` (at `cmd/otto-tray/fsm.go:14`)
  with `Detail = "config error: <sentinel contents trimmed to one line>"`.
  Wrapper writes sentinel `~/.otto-gw/.config-error` on parse failure; deletes
  on success. `makeProbe` (`cmd/otto-tray/tray.go:140-179`) checks sentinel.
  **Out of scope:** new `StateConfigError`; FSM expansion.
- **D-18-10 REL-TRAY-09**: Remove the macOS autostart probe and `tray-state.txt`
  row from `scripts/otto-gw` (lines 1928-1962). Update
  `tests/reliability/manual/REL-TRAY-02-repro.ps1` in the same commit (side-
  effect, Windows bundle unaffected).

### Claude's Discretion

- D-18-04 `slot_id` plumbing: if non-trivial, ship without `slot_id` for
  v1.10.3 — `worker_pid` is the load-bearing correlation key. Research
  recommends shipping WITHOUT `slot_id` initially (see §Codebase Map note on
  `acp.Client` construction — `acp.Config` does not currently know the slot
  label; threading it through requires adding a field to `acp.Config` AND
  rewiring `pool.Factory.Spawn`).
- D-18-05 `reason` string values: CONTEXT.md suggests
  `"ctx-cancel-respawn"` / `"transient-failure-respawn"` to mirror the two
  existing re-queue arms. Research confirms two re-queue sites exist
  (`pool.go:705-733` ctx-cancel; `pool.go:735-765` transient). The "happy path"
  recovery log fires from `respawnSlot` itself (around line 337), and the
  `reason` for that case is best modeled as `"lazy-respawn-success"` — the
  two CONTEXT.md examples cover the FAILURE re-queue paths, not the success
  path. Planner should lock the success-path reason string before tasks are
  written.
- D-18-03 error message format: CONTEXT.md gives
  `config: HTTP_ADDR ("<addr>"): port already in use` as a template. Research
  recommends following the existing `config.go` named-error shape (see
  `config.go:314, 340, 412` — `fmt.Errorf("HTTP_ADDR: bind probe failed for %q: %w", addr, err)`)
  so the error chain preserves the underlying syscall error for diagnosis.

### Deferred Ideas (OUT OF SCOPE)

- REL-POOL-07 / REL-POOL-08 — explicitly out of v1.10.3.
- New `StateConfigError` tray state — D-18-09 reuses `StateError`.
- macOS launch agent (no autostart support) — D-18-10 removes the row, does
  not add a launch agent.
- Performance work on stderr scanner — 1MB line cap is fine.
- New `LOG_FILE` config options or path normalization beyond D-18-08.
- Extending `RunHandle` to expose a PID accessor — Phase 18-02 D-18-06
  inherits the `worker_pid: 0` placeholder from Phase 15-02.

## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| REL-CFG-05 | Degenerate `ALLOWED_IPS` / `AUTH_TOKEN` no longer silently disable security | `internal/config/config.go:317-320` (`AUTH_TOKEN` load) + `:319-323` (`ALLOWED_IPS` load via `parseCIDRs`); existing `getEnvStrSliceComma` returns nil for whitespace/separator-only input — this is where the degenerate-detection branch attaches |
| REL-CFG-06 | Named config errors for `KIRO_CMD` / `KIRO_CWD`; `~` expansion for cwd | `internal/config/config.go:296-298` (load); no validation today. `os.UserHomeDir()` is stdlib (no new dep). `exec.LookPath` is already used by `acp.New` via `exec.CommandContext` — Phase 18 adds a pre-load `LookPath` check + cwd stat |
| REL-CFG-07 | Port-in-use discovered during boot validation, not after 5–10s warmup | `internal/config/config.go:295` (`HTTP_ADDR` load) is where the bind-then-close probe attaches. The real bind is at `internal/server/server.go:443` (`srv.ListenAndServe()`); the probe must use `net.Listen("tcp", addr)` + immediate `Close()` |
| REL-HTTP-06 | Ollama streaming `eng.Run` failure WARN log | `internal/adapter/ollama/handlers.go:240-252` — currently `writeError(w, http.StatusInternalServerError, err.Error())` with no WARN log. Mirror REL-HTTP-03 shape from `internal/adapter/ollama/ndjson.go:567-578` |
| REL-HTTP-07 | Panic recovery at 3 goroutines | Sites: `internal/admin/tail.go:217` (`go t.run(runCtx)`), `internal/pool/pool.go:859` (`go func()` ctx-watcher), `internal/pool/exit_watcher.go:32` (`go func()` exit-watcher — likely the "engine watchdog" target; see Open Questions §1). Pattern: `engine.go:317-329` |
| REL-OBSV-02 | Worker recovery INFO log | `internal/pool/pool.go:331-338` (the `slot.Client = newClient; slot.dead = false; ...` block inside `respawnSlot`) is the success site. Death log already fires at `exit_watcher.go:42` (`"pool: slot died", "label", slot.Label`) |
| REL-OBSV-03 | kiro-cli stderr → structured slog | `internal/acp/client.go:289` (`cmd.Stderr = os.Stderr`) — replace with `cmd.StderrPipe()` + scanner goroutine |
| REL-OBSV-04 | Single source of truth for log-tail path | `internal/config/config.go:542-543` (where `chatTraceFile := getEnvStr("CHAT_TRACE_FILE", deriveChatTraceFile(logFileForDerive))` runs); writer site `cmd/otto-gateway/main.go:302` (`Filename: cfg.ChatTraceFile`); tailer site `internal/admin/sse.go` + `cmd/otto-gateway/main.go:624-680` |
| REL-TRAY-08 | dotenv parse error → tray reflects "config error" via `StateError` | Wrapper: `scripts/otto-gw:200-225` (`load_env_file`) — bash loader is parse-tolerant today (silently skips malformed lines). Tray: `cmd/otto-tray/tray.go:140-179` (`makeProbe`) is where the sentinel-file check attaches |
| REL-TRAY-09 | Remove broken macOS tray diagnostic rows | `scripts/otto-gw:1928-1935` (`tray-state.txt`) + `:1951-1962` (autostart probe). The plist name in the probe is `com.otto.tray.plist`; the **actual** plist shipped is `io.cmetech.otto-tray.plist` (confirmed at `cmd/otto-tray/autostart_darwin.go:15` `launchAgentLabel = "io.cmetech.otto-tray"`) — the row checks the wrong file. Test side-effect: `tests/reliability/manual/REL-TRAY-02-repro.ps1` (Windows bundle is unaffected; the PS1 test only inspects Windows-side bundle contents — likely no update needed, but planner should confirm before locking the test-edit task) |

## Project Constraints (from CLAUDE.md)

- Go 1.23+; no cgo. All D-18-* fixes use only stdlib (`bufio`, `net`,
  `os/exec`, `os/user`, `path/filepath`, `runtime/debug`) — no new deps.
- stdlib `net/http` + `chi`. Fine for D-18-07 panic-recover on goroutines that
  do not flow through `chi.middleware.Recoverer` (those goroutines are
  spawned outside HTTP handlers, so the chi recoverer at
  `server.go:223, 279` does NOT cover them — confirms the need for D-18-07).
- Env var names match the Node implementation; **no rename or addition** in
  scope. `Config.AdminTailPath` (D-18-08) is a NEW field but NOT a new env var
  — it derives from existing `LOG_FILE` / `CHAT_TRACE_FILE` via
  `deriveChatTraceFile`.
- `gosec G204` flags tainted-input regressions on subprocess spawn. D-18-04
  does not spawn a new subprocess; it pipes stderr from an existing one (no
  G204 implication).
- `make ci` exit 0 end-to-end required at phase close. `Makefile:259`:
  `ci: fmt-check vet build lint test-race arch-lint examples`. Phase 18 must
  not regress any gate.
- `go test -race ./...` clean tree-wide. D-18-04 adds a goroutine; verify with
  `goleak.VerifyNone(t)` in the regression test.
- PII sentinel format: `[bracket]` not `<angle>`. Not directly relevant to
  Phase 18 (no PII fixes), but the WARN log lines added by D-18-04 / D-18-06
  must NOT use angle-bracket interpolation for any PII-adjacent field
  (`worker_pid` is numeric — safe).
- TDD mode ON (`workflow.tdd_mode: true` per `.planning/config.json`): every
  behavior-adding REL-* gets a RED test commit before the GREEN fix commit.

## Codebase Map

### 18-01 Config hardening

| Fix | File:Line | Current state | What changes |
|-----|-----------|---------------|--------------|
| D-18-01 REL-CFG-05 (`AUTH_TOKEN` degenerate) | `internal/config/config.go:317` | `authTokens := getEnvStrSliceComma("AUTH_TOKEN", nil)` — degenerate input returns `def == nil` silently | Add `rawAuth := strings.TrimSpace(os.Getenv("AUTH_TOKEN"))` BEFORE the existing load; `if rawAuth != "" && len(authTokens) == 0 { slog.Default().Warn(...) }` AFTER. Emit via `slog.Default()` to mirror REL-CFG-03's pattern at `config.go:576-580` so the regression test (which captures `slog.Default()` via `captureSlogDefault` in `regression_rel_cfg_03_test.go:31-39`) can observe it from `config.Load()` alone |
| D-18-01 REL-CFG-05 (`ALLOWED_IPS` degenerate) | `internal/config/config.go:319-323` | `allowedIPEntries := getEnvStrSliceComma("ALLOWED_IPS", nil); allowedIPs, err := parseCIDRs(allowedIPEntries)` — degenerate returns nil prefixes silently | Same pattern as `AUTH_TOKEN`: capture raw, compare to parsed; Warn when raw non-empty + parsed empty |
| D-18-02 REL-CFG-06 (`KIRO_CMD`) | `internal/config/config.go:296` | `kiroCmd := getEnvStr("KIRO_CMD", "kiro-cli")` — no validation | Add `_, err := exec.LookPath(kiroCmd)` after load; on error: `errs = append(errs, fmt.Errorf("config: KIRO_CMD (%q): not found in PATH or unreadable", kiroCmd))`. Note: `exec.LookPath` handles absolute paths too (just checks file exists and is executable) |
| D-18-02 REL-CFG-06 (`KIRO_CWD`) | `internal/config/config.go:298` | `kiroCWD := getEnvStr("KIRO_CWD", "")` — no validation; no `~` expansion | After load: if `strings.HasPrefix(kiroCWD, "~/") || kiroCWD == "~"` then `home, _ := os.UserHomeDir(); kiroCWD = filepath.Join(home, kiroCWD[1:])`. Then `stat, err := os.Stat(kiroCWD); ...` returning named errors per CONTEXT.md. Skip both checks when `kiroCWD == ""` (default — Cwd is optional; `acp.Client` already handles empty Cwd) |
| D-18-03 REL-CFG-07 | `internal/config/config.go:295` | `httpAddr := getEnvStr("HTTP_ADDR", "127.0.0.1:18080")` — no probe | After load: `ln, err := net.Listen("tcp", httpAddr); if err == nil { _ = ln.Close() } else { errs = append(errs, fmt.Errorf("config: HTTP_ADDR (%q): bind probe failed: %w", httpAddr, err)) }`. Probe runs unconditionally — even when HTTP_ADDR uses the default. Order: must run AFTER all other env loads so a single `errors.Join` at line 583 surfaces every config issue together (existing pattern) |
| D-18-01 INFO line preservation | `cmd/otto-gateway/main.go:115-120` | `logger.Info("auth mode", "enabled", ..., "ip_allowlist", ..., "trust_xff", ...)` | UNCHANGED — D-18-01 explicitly preserves this at INFO. New Warn lines emit from inside `config.Load()` before this fires |

### 18-02 Observability symmetry + HTTP error logging

| Fix | File:Line | Current state | What changes |
|-----|-----------|---------------|--------------|
| D-18-04 REL-OBSV-03 | `internal/acp/client.go:289` | `cmd.Stderr = os.Stderr` — raw subprocess stderr leaks to gateway stderr | Replace with `stderrPipe, err := cmd.StderrPipe()` (BEFORE `cmd.Start()` at line 327) + scanner goroutine launched AFTER `cmd.Start()` succeeds (need `cmd.Process.Pid` for the `worker_pid` field). Pattern in CONTEXT.md §D-18-04. Logger is already available on `acp.Client` via `cfg.Logger` (see `acp.Config` struct in `client.go`) |
| D-18-05 REL-OBSV-02 | `internal/pool/pool.go:331-338` (in `respawnSlot` after `slot.dead = false`) | No recovery log; death log at `exit_watcher.go:42` (`"pool: slot died", "label", slot.Label`) is the asymmetric sibling | Add INFO log AFTER `slot.dead = false` (line 333) and BEFORE `p.startExitWatcher(slot, newDone)` (line 336): `p.cfg.Logger.Info("pool: slot recovered", "slot_id", slot.Label, "worker_pid", <NEW>, "previous_pid", <OLD>, "reason", "lazy-respawn-success")`. `previous_pid` requires capturing the OLD `cmd.Process.Pid` BEFORE step 1 closes the client — needs a new public accessor on `acp.Client` (e.g., `Pid() int`) since `c.cmd.Process.Pid` is already used internally at `client.go:1188`. `worker_pid` (the new client's PID) is similarly available |
| D-18-06 REL-HTTP-06 | `internal/adapter/ollama/handlers.go:240-252` | `run, err := eng.Run(streamCtx, req); if err != nil { ... writeError(...) }` — no WARN log | Insert BEFORE `writeError`: `a.cfg.Logger.Warn("ollama: streaming eng.Run failed", "worker_pid", 0, "kiro_exit_code", <if errors.As exec.ExitError>, "bytes_streamed", 0, "session_id", "", "request_id", plugin.RequestIDFromContext(ctx), "err", err)`. Mirror `ndjson.go:567-577`. `session_id` is empty because `eng.Run` failed BEFORE session creation; document as field-included-with-empty-value (planner can rename to omit if all REL-HTTP-03 sites would also be inconsistent) |
| D-18-07 REL-HTTP-07 (admin tailer) | `internal/admin/tail.go:217` (`go t.run(runCtx)`) | No panic recovery inside the goroutine body that starts at line 279 (`func (t *Tailer) run(ctx context.Context)`) | Add `defer func() { if r := recover(); r != nil { t.logger.Error("goroutine panic recovered", "site", "admin-tailer", "panic", fmt.Sprintf("%v", r), "stack", string(debug.Stack())) } }()` at the top of `run` (line 285, before the variable declarations) |
| D-18-07 REL-HTTP-07 (pool ctx-watcher) | `internal/pool/pool.go:859-869` | `go func() { select { case <-watchCtx.Done(): ...; case <-w.doneCh: ... } }()` — no recovery | Add the same defer at top of the goroutine body. `logger` not currently in closure — capture `p.cfg.Logger` before the `go func()` |
| D-18-07 REL-HTTP-07 (exit-watcher / "engine watchdog") | `internal/pool/exit_watcher.go:32-50` | `go func() { select { case <-done: ...; case <-p.closing: ... } }()` — no recovery | Same defer. See Open Questions §1 — confirm with planner that this is the intended third site |
| D-18-08 REL-OBSV-04 | `internal/config/config.go:267-271` (struct field) + `:542-543` (load) + `cmd/otto-gateway/main.go:624-680` (tailer wiring) | `Config.ChatTraceFile` exists; tailer at `main.go:624` derives path independently via different code paths | Add `AdminTailPath` field to `Config` struct; populate in `Load()` via the same `deriveChatTraceFile(logFileForDerive)` helper used at line 543 for `ChatTraceFile`. Update `main.go:624+` to read `cfg.AdminTailPath`. Tailer's `reopen()` at `tail.go:298-305` already logs at `Debug` on open failure — change to `Warn` AND include `path=<t.path>` (already in scope) |

### 18-03 Tray honesty

| Fix | File:Line | Current state | What changes |
|-----|-----------|---------------|--------------|
| D-18-09 REL-TRAY-08 (wrapper sentinel write) | `scripts/otto-gw:204-225` (`load_env_file`) + `:466, :475, :686` (call sites) | Loader silently skips malformed lines; no error propagation | Wrap loader call: detect parse failure (e.g., loader returns non-zero or sets an error variable), then `echo "$err_msg" > "$HOME/.otto-gw/.config-error"` on failure; `rm -f "$HOME/.otto-gw/.config-error"` on success. PowerShell mirror in `scripts/otto-gw.ps1` |
| D-18-09 REL-TRAY-08 (tray sentinel read) | `cmd/otto-tray/tray.go:140-179` (`makeProbe`) | Returns `(pidAlive, healthOK, snap)` tuple based on PID + HTTP probes | Before checking PID at line 144: `if data, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".otto-gw", ".config-error")); err == nil { ... }`. On hit: probe must return a signal that makes `computeState` (`fsm.go:39-75`) resolve to `StateError`. Current `probeFunc` returns `(bool, bool, Snapshot)`; `computeState` uses `PIDAlive=false` to short-circuit to `StateStopped`, NOT `StateError`. **Architecture gap**: the existing `probeFunc` signature has no slot for "config error detail" — the planner needs to either (a) extend `probeFunc` to return a detail string, (b) thread the sentinel-file path through to `runPoller` and have `runPoller` synthesize a `stateOutput{State: StateError, Detail: ...}`, or (c) add a separate poller arm. See Open Questions §2 |
| D-18-10 REL-TRAY-09 | `scripts/otto-gw:1927-1962` | Two broken probes: `tray-state.txt` reads `$OTTO_INSTALL_ROOT/.otto/tray/state` (never written by tray) and `autostart.txt` checks `com.otto.tray.plist` (wrong name; actual is `io.cmetech.otto-tray.plist` at `cmd/otto-tray/autostart_darwin.go:15`) | Remove both blocks. PowerShell wrapper (`scripts/otto-gw.ps1:1647-1676`) Windows path is UNCHANGED (it probes the Run-key correctly) |
| D-18-10 test side-effect | `tests/reliability/manual/REL-TRAY-02-repro.ps1` | Asserts presence of specific bundle rows in the Windows-only path | Per CONTEXT.md, "the Windows bundle is unaffected." Read of the test confirms: it only inspects `health.json` for `unreachable:` sentinel and the bundle path on stdout — no assertion on `tray-state.txt` / `autostart.txt`. **Likely no update needed for this PS1 test**; planner should confirm before locking a no-op task |

### Phase 15-02 / WR-03 prior patterns (for 18-02 mirror)

- **REL-HTTP-03 field set**: `internal/adapter/ollama/ndjson.go:567-577` and
  `internal/adapter/openai/sse.go:566-577`. Both log `worker_pid: 0` +
  `bytes_streamed: 0` as documented placeholders. The Anthropic equivalent
  (`internal/adapter/anthropic/handlers.go:191` `a.cfg.Logger.Error("anthropic: engine.Run error", "err", err)`)
  is logged at `Error` level (asymmetric with REL-HTTP-06 which CONTEXT.md
  specifies as Warn) — planner should note that REL-HTTP-06's `Warn` level
  diverges from the Anthropic precedent but matches CONTEXT.md D-18-06.
- **WR-03 ordering invariant**: `pool.go:317-338` documents the
  close-OLD-first → spawn-NEW → swap-under-mu → fresh-watcher-under-mu
  ordering. D-18-05 INFO emission must land between steps 4 and 5 (after
  `slot.dead = false` but inside the `p.mu` critical section so the log line
  is causally ordered with the swap). Alternatively, emit AFTER `p.mu.Unlock()`
  at line 337 — the log itself does not need `p.mu`; only the field reads do.

## Existing Patterns to Mirror

### Pattern 1: Panic-recover (D-18-07 template)

Direct copy from `internal/engine/engine.go:317-329`:

```go
func (e *Engine) callPreHookSafe(ctx context.Context, h PreHook, req *canonical.ChatRequest) (resp *canonical.ChatResponse, err error) {
    defer func() {
        if r := recover(); r != nil {
            e.cfg.Logger.Error("engine.hook.panic",
                "hook", fmt.Sprintf("%T", h),
                "kind", "pre",
                "err", fmt.Sprintf("%v", r),
                "stack", string(debug.Stack()))
            resp = nil
            err = fmt.Errorf("engine: hook panic: %v", r)
        }
        // ...
    }()
    // body
}
```

For D-18-07, drop the `resp`/`err` recovery (goroutines have no return values)
and rename `"engine.hook.panic"` to `"goroutine panic recovered"` per
CONTEXT.md, with `site` (string) instead of `kind`/`hook`.

### Pattern 2: slog.Default capture in regression tests (D-18-01, D-18-08)

Direct copy from `internal/config/regression_rel_cfg_03_test.go:31-57`:

```go
func captureSlogDefault(t *testing.T) *bytes.Buffer {
    t.Helper()
    buf := &bytes.Buffer{}
    h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
    prev := slog.Default()
    slog.SetDefault(slog.New(h))
    t.Cleanup(func() { slog.SetDefault(prev) })
    return buf
}
func decodeLogRecords(t *testing.T, buf *bytes.Buffer) []map[string]any { /* ... */ }
```

D-18-01 should use this for the degenerate `ALLOWED_IPS` / `AUTH_TOKEN` Warn
assertion. The helper is in `internal/config/regression_rel_cfg_03_test.go`
and can be reused in the same package (`config_test`). Same applies to a new
regression test for D-18-02 / D-18-03.

### Pattern 3: Existing exit-watcher slog field set (D-18-05 mirror)

`internal/pool/exit_watcher.go:42`:

```go
if p.cfg.Logger != nil {
    p.cfg.Logger.Info("pool: slot died", "label", slot.Label)
}
```

The death log uses `"label"`. CONTEXT.md D-18-05 specifies `"slot_id"`. There
is a naming inconsistency — planner should decide whether to (a) follow
CONTEXT.md and use `slot_id` for the new recovery log (operators grep two
different keys), (b) use `label` to match the death log, or (c) add `slot_id`
to the death log as a follow-up to keep both symmetric. Research recommends
(b) `label` for the recovery log to mirror death exactly, since CONTEXT.md
itself notes `slot_id` is "if available; otherwise omit" in the adjacent
D-18-04 — implying CONTEXT.md is permissive on the precise key name.

### Pattern 4: Sentinel file conventions (D-18-09)

The codebase already has one sentinel-file pattern at `cmd/otto-tray/tray.go:366-376`
(writes `support/last-error.log` at mode 0o600 when bundle creation fails).
Reuse this shape:

```go
logDir := filepath.Join(s.installRoot, "support")
if mkErr := os.MkdirAll(logDir, 0o750); mkErr == nil {
    logPath := filepath.Join(logDir, "last-error.log")
    content := /* ... */
    if writeErr := os.WriteFile(logPath, []byte(content), 0o600); writeErr == nil {
        // ...
    }
}
```

For D-18-09: sentinel path `$HOME/.otto-gw/.config-error` (per CONTEXT.md),
mode 0o600 (file contains potentially-sensitive env parse errors), parent
dir creation via `MkdirAll(filepath.Dir(path), 0o750)`. PowerShell mirror in
`scripts/otto-gw.ps1` already uses `Set-Content` patterns extensively
(e.g., line 1647-1649) — same shape.

### Pattern 5: fakeClient with Pid accessor (D-18-04 / D-18-05 test scaffolding)

`internal/pool/pool_test.go:48-167` defines `fakeClient` implementing
`pool.PoolClient`. It does NOT currently expose `Pid()`. D-18-05's
`previous_pid` / `worker_pid` fields require either:

- Add `Pid() int` to `pool.PoolClient` interface; fakeClient returns a test-controlled value
- Or threading PID through a different channel (e.g., capturing the `slot.Client` and using a type assertion to `*acp.Client` — fragile)

Research recommends the interface extension. The new method is read-only,
trivial, and unblocks deterministic tests.

### Pattern 6: REL-HTTP-03 WARN log field set (D-18-06 verbatim mirror)

`internal/adapter/ollama/ndjson.go:567-578`:

```go
logArgs := []any{
    "session_id", run.SessionID(),
    "worker_pid", 0, // worker_pid: counter not yet wired in RunHandle interface
    "bytes_streamed", 0, // bytes_streamed: counter not yet wired in ndjson emitter
    "err", rerr,
}
var exitErr *exec.ExitError
if errors.As(rerr, &exitErr) {
    logArgs = append(logArgs, "kiro_exit_code", exitErr.ExitCode())
}
logger.Warn("ollama: ndjson worker terminated mid-stream", logArgs...)
```

D-18-06 site is BEFORE `eng.Run` returns successfully (no session yet, so no
`run.SessionID()`), but otherwise the same shape applies.

## Validation Architecture

### Test Framework

| Property | Value |
|----------|-------|
| Framework | Go stdlib `testing` + `go-test-race` + `goleak.VerifyNone` |
| Config file | none (Go convention) — `internal/*/testmain_test.go` per package wires `goleak.VerifyTestMain` |
| Quick run command | `go test -race ./internal/config/... ./internal/pool/... ./internal/admin/... ./internal/adapter/ollama/... ./internal/acp/... ./cmd/otto-tray/...` |
| Full suite command | `make ci` (resolves to `fmt-check vet build lint test-race arch-lint examples`) |

### Phase Requirements → Test Map

| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|--------------|
| REL-CFG-05 | Degenerate ALLOWED_IPS / AUTH_TOKEN produces Warn slog record and treats as unset | unit | `go test -race ./internal/config/ -run TestRegression_REL_CFG_05` | New file `internal/config/regression_rel_cfg_05_test.go` (Wave 0). Use `captureSlogDefault` from existing `regression_rel_cfg_03_test.go:31-39` |
| REL-CFG-06 | KIRO_CMD-not-found / KIRO_CWD-missing / KIRO_CWD-not-a-dir each return a named error containing the variable name and the offending value | unit | `go test -race ./internal/config/ -run TestRegression_REL_CFG_06` | New file `internal/config/regression_rel_cfg_06_test.go` (Wave 0). Pattern: table-driven like `regression_rel_cfg_01_test.go:32-87` |
| REL-CFG-06 (`~` expansion) | `KIRO_CWD=~/work` expands to `$HOME/work` after `Load()` | unit | same file | new subtest |
| REL-CFG-07 | HTTP_ADDR already-bound port surfaces a named error from `config.Load()` | unit | `go test -race ./internal/config/ -run TestRegression_REL_CFG_07` | New file `internal/config/regression_rel_cfg_07_test.go` (Wave 0). RED test binds to `127.0.0.1:0` (kernel-assigned), reads addr, sets `t.Setenv("HTTP_ADDR", addr)`, calls `config.Load()`, expects error containing `HTTP_ADDR` |
| REL-HTTP-06 | Ollama `eng.Run` failure emits WARN before error frame | unit | `go test -race ./internal/adapter/ollama/ -run TestRegression_REL_HTTP_06` | New file `internal/adapter/ollama/regression_rel_http_06_test.go` (Wave 0). Reuse `nilLogger()` replacement: capture slog handler in buffer; inject via `adapter.Config.Logger`; drive a request through `handleChat` with a fake engine that returns error from `Run` |
| REL-HTTP-07 (tailer) | Tailer goroutine recovers from panic and logs at Error | unit | `go test -race ./internal/admin/ -run TestRegression_REL_HTTP_07_AdminTailer` | New file `internal/admin/regression_rel_http_07_test.go` (Wave 0). Drive `Tailer.run` with a path that triggers a panic (planner needs to identify an injection seam — possibly a fake `*RingBuffer` with a `Push` that panics) |
| REL-HTTP-07 (pool ctx-watcher) | Ctx-watcher recovers from panic and logs | unit | `go test -race ./internal/pool/ -run TestRegression_REL_HTTP_07_PoolCtxWatcher` | New file `internal/pool/regression_rel_http_07_test.go` (Wave 0). Use `fakeClient` to force a panic path |
| REL-HTTP-07 (exit-watcher) | Exit-watcher recovers from panic and logs | unit | `go test -race ./internal/pool/ -run TestRegression_REL_HTTP_07_PoolExitWatcher` | same file as above |
| REL-OBSV-02 | Worker recovery emits INFO at lazy-respawn success | unit | `go test -race ./internal/pool/ -run TestRegression_REL_OBSV_02` | New file `internal/pool/regression_rel_obsv_02_test.go` (Wave 0). Drive via `regression_rel_pool_01_test.go:59-90` scaffolding (the warmup → fireDone → respawn flow already exists); inject a logger that captures and assert "pool: slot recovered" record |
| REL-OBSV-03 | kiro-cli stderr lines flow through structured slog at Warn with `worker_pid` | unit | `go test -race ./internal/acp/ -run TestRegression_REL_OBSV_03` + `goleak.VerifyNone` | New file `internal/acp/regression_rel_obsv_03_test.go` (Wave 0). Spawn a fake "kiro-cli" via `os/exec` that writes a known string to stderr then exits (e.g., `sh -c 'echo "test stderr line" 1>&2'`); assert log capture; assert `goleak.VerifyNone(t)` after `client.Close()` |
| REL-OBSV-04 | Writer and tailer use the same `Config.AdminTailPath`; tailer open-fail logs at Warn | unit | `go test -race ./internal/config/ -run TestRegression_REL_OBSV_04` + `go test -race ./internal/admin/ -run TestRegression_REL_OBSV_04_TailerWarn` | New files (config side asserts the field is populated; admin side asserts the Warn log level on missing file) |
| REL-TRAY-08 | When sentinel file is present, tray probe returns a signal that resolves to `StateError` with `Detail` containing the sentinel contents | unit | `go test -race ./cmd/otto-tray/ -run TestRegression_REL_TRAY_08` | New file `cmd/otto-tray/regression_rel_tray_08_test.go` (Wave 0). Use `t.TempDir()` for the sentinel; mock `$HOME` via `t.Setenv("HOME", ...)` |
| REL-TRAY-09 | Bundle does NOT contain `tray-state.txt` or autostart row on macOS | manual smoke + bash unit | `bats tests/wrappers/otto-gw-support-rows_test.bats` (NEW) | New bash test (Wave 0) — verify bundle produced by `scripts/otto-gw support` on macOS does NOT contain `tray/tray-state.txt` or `tray/autostart.txt`. Existing manual repro `tests/reliability/manual/REL-TRAY-02-repro.ps1` likely needs no change (asserts Windows-only fields) |

### Sampling Rate

- **Per task commit:** `go test -race ./<changed-package>/...` for the package
  the task touches.
- **Per wave merge:** `make test-race` (all packages, race detector on).
- **Phase gate:** `make ci` exit 0; `grep -rn "cmd.Stderr = os.Stderr" internal/ cmd/`
  returns no production-code hits.

### Wave 0 Gaps

- [ ] `internal/config/regression_rel_cfg_05_test.go` — REL-CFG-05 degenerate-env Warn assertion
- [ ] `internal/config/regression_rel_cfg_06_test.go` — REL-CFG-06 named-error + `~` expansion
- [ ] `internal/config/regression_rel_cfg_07_test.go` — REL-CFG-07 bind-then-close probe
- [ ] `internal/adapter/ollama/regression_rel_http_06_test.go` — REL-HTTP-06 WARN log capture
- [ ] `internal/admin/regression_rel_http_07_test.go` — REL-HTTP-07 tailer panic-recover
- [ ] `internal/pool/regression_rel_http_07_test.go` — REL-HTTP-07 ctx-watcher + exit-watcher panic-recover
- [ ] `internal/pool/regression_rel_obsv_02_test.go` — REL-OBSV-02 recovery INFO log
- [ ] `internal/acp/regression_rel_obsv_03_test.go` — REL-OBSV-03 stderr scanner + goleak gate
- [ ] `internal/config/regression_rel_obsv_04_test.go` + admin side — REL-OBSV-04 single-source path
- [ ] `cmd/otto-tray/regression_rel_tray_08_test.go` — REL-TRAY-08 sentinel detection
- [ ] `tests/wrappers/otto-gw-support-rows_test.bats` — REL-TRAY-09 bundle absence assertion

No new framework install needed. `goleak` is already a dependency (used
extensively in `internal/acp/*_test.go`, `internal/pool/testmain_test.go`).

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Tilde expansion (D-18-02) | Custom `$HOME` substitution | `os.UserHomeDir()` + `filepath.Join` | Stdlib; cross-platform; documented |
| Path resolution (D-18-08) | Re-derive `deriveChatTraceFile` in another package | Reuse the existing helper at `config.go:665` | CONTEXT.md explicitly mandates single source of truth |
| Stderr line scanning (D-18-04) | Manual byte-loop | `bufio.Scanner` + `scanner.Buffer(make([]byte, 0, 64*1024), 1*1024*1024)` | CONTEXT.md gives the exact pattern; stdlib |
| Port bind probe (D-18-03) | `lsof` / `netstat` parsing | `net.Listen("tcp", addr)` + `Close()` | CONTEXT.md: bind is the only authoritative test |
| Panic recovery (D-18-07) | New helper package | Direct `defer func() { if r := recover(); r != nil { ... } }()` at the top of each goroutine body | The codebase already has the exact pattern in `engine.go:317-329`; copy-paste with site-renamed fields |
| Goroutine leak detection | Custom goroutine accounting | `go.uber.org/goleak.VerifyNone(t)` | Already used in 18+ test files |

**Key insight:** Every Phase 18 fix has a 1:1 codebase analog. The fixes are
small (1-15 LOC each). The plan should NOT introduce new abstractions; this
is a long-tail closeout, not a refactor.

## Common Pitfalls

### Pitfall 1: `slog.Default()` vs `cfg.Logger` divergence in tests (D-18-01)

**What goes wrong:** A regression test that uses `captureSlogDefault` will
only see records emitted via `slog.Default()` (or a `slog.New(...)` with the
test handler installed). If D-18-01 emits via `slog.Default().Warn(...)` like
REL-CFG-03 at `config.go:576-580`, the test sees it. If the implementation
emits via `cfg.Logger` (which `config.Load()` does not have), the test
silently passes pre-fix.

**Why it happens:** `config.Load()` takes no logger argument; it can only
emit via `slog.Default()`.

**How to avoid:** Mirror the REL-CFG-03 emission shape exactly. Test ASSERT
that the Warn comes from inside `config.Load()` — not from `main.go`.

### Pitfall 2: `cmd.StderrPipe()` must be called BEFORE `cmd.Start()` (D-18-04)

**What goes wrong:** Calling `cmd.StderrPipe()` after `cmd.Start()` returns
`exec: StderrPipe after process started`.

**Why it happens:** `os/exec` invariant — pipes are wired during start.

**How to avoid:** Reorder `client.go:289` — the StderrPipe call goes BEFORE
the existing `cmd.Start()` at line 327. The goroutine that reads from the
pipe is launched AFTER `cmd.Start()` succeeds (we need `cmd.Process.Pid` for
the `worker_pid` field).

### Pitfall 3: Goroutine leak on early test exit (D-18-04 / D-18-07)

**What goes wrong:** The stderr scanner goroutine reads in a loop. If the
test exits before the pipe closes (e.g., subprocess hangs on a slow shutdown),
`goleak.VerifyNone` flags a leak.

**Why it happens:** The goroutine's exit edge is "pipe close", which happens
when the subprocess exits or `client.Close()` is called.

**How to avoid:** Tests MUST call `client.Close()` and wait for `Done()`
before `goleak.VerifyNone(t)`. The existing `internal/acp/client_test.go`
files already follow this pattern — copy.

### Pitfall 4: TOCTOU on bind-then-close (D-18-03, acknowledged)

**What goes wrong:** Another process binds the port between probe close and
real bind.

**Why it happens:** The probe and the real bind happen in two separate
syscalls.

**How to avoid:** CONTEXT.md accepts this — the window is microseconds and
the fix prevents the common case (operator runs a second instance, gateway
boots through 5–10s of pool warmup, THEN bind fails). Do NOT add retry logic.

### Pitfall 5: Slot label vs slot_id naming inconsistency (D-18-05)

**What goes wrong:** Death log uses `"label"` field name; recovery log per
CONTEXT.md uses `"slot_id"`. Operators grepping for `"slot-0"` see only one
half of the lifecycle.

**Why it happens:** CONTEXT.md doesn't reconcile field names with the
existing death log.

**How to avoid:** Planner should lock the field name BEFORE tasks are
written. Research recommends `"label"` for symmetry; alternatively, add
`"slot_id"` to the death log in the same plan to keep both lines symmetric.

### Pitfall 6: Sentinel-file race on wrapper restart (D-18-09, acknowledged)

**What goes wrong:** Wrapper writes sentinel, then exits before launching
gateway. Tray reads sentinel and shows config-error. Operator fixes config
and re-runs wrapper — wrapper deletes sentinel but tray's next poll (up to
3s later per `tray.go:104` `time.NewTicker(3 * time.Second)`) still shows
the error.

**Why it happens:** Polling cadence.

**How to avoid:** CONTEXT.md acknowledges this as acceptable. Document the
3s upper-bound flap window in the tray probe comment.

### Pitfall 7: Bundle row removal breaks bash strict-mode (D-18-10)

**What goes wrong:** Removing the `tray-state.txt` / `autostart.txt` blocks
leaves a dangling `# ---- tray/ ---------------------------------------------------------`
section header pointing at `pidfile.txt` only. If a future maintainer adds
back a row without re-checking, the section structure is non-obvious.

**Why it happens:** Bash heredoc / block scoping.

**How to avoid:** Verify `bash -n scripts/otto-gw` and `shellcheck scripts/otto-gw`
exit clean post-removal. Add a one-line comment noting that the section
historically had two more rows that were removed in v1.10.3 per REL-TRAY-09.

## Code Examples

### D-18-01 REL-CFG-05 (degenerate AUTH_TOKEN/ALLOWED_IPS) — full pattern

```go
// internal/config/config.go (insertion at line 317 area)
rawAuth := strings.TrimSpace(os.Getenv("AUTH_TOKEN"))
authTokens := getEnvStrSliceComma("AUTH_TOKEN", nil)
if rawAuth != "" && len(authTokens) == 0 {
    slog.Default().Warn(
        "AUTH_TOKEN looks degenerate (no entries after trim+CSV split); treating as unset",
        "raw", rawAuth,
    )
}

rawAllowed := strings.TrimSpace(os.Getenv("ALLOWED_IPS"))
allowedIPEntries := getEnvStrSliceComma("ALLOWED_IPS", nil)
allowedIPs, err := parseCIDRs(allowedIPEntries)
if err != nil {
    errs = append(errs, fmt.Errorf("ALLOWED_IPS: %w", err))
}
if rawAllowed != "" && len(allowedIPs) == 0 && err == nil {
    slog.Default().Warn(
        "ALLOWED_IPS looks degenerate (no entries after trim+CSV split); treating as unset",
        "raw", rawAllowed,
    )
}
```

### D-18-03 REL-CFG-07 (port probe) — full pattern

```go
// internal/config/config.go (insertion after httpAddr := getEnvStr("HTTP_ADDR", ...))
ln, lerr := net.Listen("tcp", httpAddr)
if lerr != nil {
    errs = append(errs, fmt.Errorf("HTTP_ADDR: bind probe failed for %q: %w", httpAddr, lerr))
} else {
    _ = ln.Close()
}
```

### D-18-07 panic-recover wrapper — generic

```go
// internal/admin/tail.go run() top
func (t *Tailer) run(ctx context.Context) {
    defer func() {
        if r := recover(); r != nil {
            t.logger.Error("goroutine panic recovered",
                "site", "admin-tailer",
                "panic", fmt.Sprintf("%v", r),
                "stack", string(debug.Stack()))
        }
    }()
    // existing body
}
```

`internal/admin/tail.go` already imports `log/slog`, `bufio`, `context`,
`errors`, `fmt`, `io`, `os`, `strings`, `sync`, `time`. Adding
`runtime/debug` is the only new import; `fmt` is already there.

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| Raw `cmd.Stderr = os.Stderr` for subprocess stderr | `cmd.StderrPipe()` + bufio.Scanner → structured slog | v1.10.3 D-18-04 | Operators get one log destination; kiro-cli stderr correlates by `worker_pid` |
| Silent degenerate env handling | Warn + treat-as-unset | v1.10.3 D-18-01 | Operators see misconfigured `.env` files in logs |
| `slot.dead = true` death log; no recovery log | Symmetric `pool: slot died` ↔ `pool: slot recovered` | v1.10.3 D-18-05 | Operators verify self-healing without health endpoint polling |
| Two divergent log-tail path resolutions | Single `Config.AdminTailPath` field | v1.10.3 D-18-08 | Tailer always reads the file the writer writes |

**Deprecated/outdated:**

- `slog.Logger.Debug` for missing-log-file in `tail.go:303` — D-18-08
  promotes to `Warn`.
- Bundle's `tray-state.txt` and macOS-`autostart.txt` rows — D-18-10 removes
  outright; no successor.

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | The "engine watchdog" goroutine in D-18-07 refers to `internal/pool/exit_watcher.go:32` (the per-slot exit-watcher) rather than the `context.AfterFunc` at `engine.go:255` (which is not a goroutine the caller owns) | Open Questions §1 | If wrong, the wrong site gets panic-recover; the actual engine-internal goroutine that needs hardening is missed. Planner needs to confirm before locking tasks |
| A2 | `Config.AdminTailPath` is a NEW field name — CONTEXT.md uses this exact name but the codebase has no occurrences | Codebase Map | If conflict (some other field already exists), rename to avoid shadowing. Verified via `grep "AdminTailPath" --include="*.go"` returns zero hits in source — safe |
| A3 | REL-TRAY-02 PowerShell test does NOT assert on `tray/tray-state.txt` or `tray/autostart.txt` row presence, so removing those rows from the bash wrapper does NOT break the existing PS1 test | Codebase Map (18-03 test side-effect) | If wrong, the PS1 test will fail in CI on Windows. Mitigation: planner adds a no-op verification task that runs the PS1 test or visually inspects it before merge |
| A4 | `worker_pid: 0` placeholder is acceptable for D-18-06, mirroring REL-HTTP-03 sites at `ndjson.go:569` and `sse.go:573` | Codebase Map (REL-HTTP-06) | If a "real PID" requirement is now load-bearing, the interface extension blocks Phase 18 and bumps the scope. Recommend continuing with the placeholder and tracking a follow-up "wire worker_pid through RunHandle" as a separate v1.10.4 task |
| A5 | D-18-09 architecture: `probeFunc` signature `(bool, bool, Snapshot)` has no slot for "config error detail", so the sentinel-file check requires extending the probe-return shape OR the poller's state-synthesis logic | Codebase Map (D-18-09) + Open Questions §2 | If wrong, the existing signature accommodates a path the research missed; planner discovers a simpler integration. Recommended: lock the integration shape in plan-checker before tasks land |

## Open Questions

1. **D-18-07 "engine watchdog" identity**
   - What we know: CONTEXT.md lists three sites: admin tailer, engine
     watchdog, pool ctx-watcher. The admin tailer is unambiguously
     `internal/admin/tail.go run()` (the only goroutine in that package).
     The pool ctx-watcher is unambiguously `pool.go:859`. The engine
     watchdog reference is ambiguous: `engine.go:255` uses
     `context.AfterFunc` which is NOT a goroutine the caller spawns or
     owns (it's runtime-managed).
   - What's unclear: Did CONTEXT.md intend (a) the exit-watcher at
     `exit_watcher.go:32` (the closest engine-adjacent goroutine missing
     recovery), (b) a different site entirely, or (c) the
     `context.AfterFunc` callback (which executes on a runtime-managed
     goroutine and can panic into Go's runtime if not protected)?
   - Recommendation: Planner ASK during discuss-phase clarification, or
     default to (a) the exit-watcher AND (c) wrapping the
     `context.AfterFunc` callback body in defer-recover (the callback at
     `engine.go:255-265` does `Cancel(sid)` which can hypothetically
     panic if `e.cfg.ACP` is nil or stale). Both are cheap.

2. **D-18-09 probeFunc signature extension**
   - What we know: `probeFunc` returns `(bool, bool, Snapshot)`. The
     poller (at `cmd/otto-tray/poller.go`) converts these into
     `stateInput` for `computeState`. `stateInput` has no "config error
     present" field today.
   - What's unclear: Should D-18-09 (a) extend `probeFunc` to return a
     fourth value (e.g., `configErrorDetail string`), (b) add the
     sentinel-file check inside the poller and synthesize a different
     `stateOutput` directly bypassing `computeState`, or (c) extend
     `stateInput` and let `computeState` add a new short-circuit branch
     at the top?
   - Recommendation: (c) is cleanest — `stateInput.ConfigError string`,
     and `computeState` returns `stateOutput{State: StateError, Detail: "config error: " + in.ConfigError}` at the top of the function (before the PIDAlive check). Lock in discuss-phase or planner.

3. **D-18-05 success-path `reason` field**
   - What we know: CONTEXT.md gives two reason values for re-queue paths
     (failure cases). The actual lazy-respawn SUCCESS path (`respawnSlot`
     returns nil) does not have a CONTEXT.md-specified reason.
   - What's unclear: Should the success log use `"lazy-respawn-success"`
     or a different label?
   - Recommendation: `"lazy-respawn-success"` for explicitness; planner
     locks before tasks.

4. **D-18-04 `slot_id` plumbing scope**
   - What we know: `acp.Config` has no slot identifier today; threading
     it requires adding a field to `acp.Config` AND rewiring
     `pool.Factory.Spawn` to pass it from the slot at construction time.
   - What's unclear: Is "non-trivial plumbing" worth doing now, or punt
     to v1.10.4?
   - Recommendation: Punt. CONTEXT.md explicitly permits shipping
     without `slot_id`; `worker_pid` is the load-bearing key.

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Go toolchain | All Go fixes | ✓ | 1.24 (per go.mod) | — |
| `bash` 3.2+ (POSIX) | D-18-09 wrapper sentinel write | ✓ | 3.2 on darwin (minimum supported) | — |
| PowerShell 5.1+ / pwsh 7+ | D-18-09 wrapper sentinel mirror in `otto-gw.ps1` | partial — pwsh not on dev darwin | n/a | Lint-only check (`shellcheck` equivalent for PS not on this box). Smoke-test in CI Windows job (existing pattern from quick 260531-oax) |
| `shellcheck` | bash wrapper validation | assumed in CI | n/a | — |
| `goleak` Go module | Tests for D-18-04 / D-18-07 | ✓ | `go.uber.org/goleak` (already in `go.mod`) | — |

**Missing dependencies with no fallback:** none.

**Missing dependencies with fallback:** pwsh on dev machine — PS1 changes
(D-18-09 wrapper, D-18-10 test-update verification) get static review only on
darwin per established quick-task policy (e.g., quick 260531-oax).

## Integration Points & Risks

### Ordering within Phase 18 (cross-plan)

CONTEXT.md says "All three plans are independent — Wave 1 parallel-safe." This
research confirms NO file overlap across plans. However, the planner should
note three soft ordering preferences for cleaner diffs:

1. **18-01 before 18-02**: Plan 18-02 D-18-08 adds `Config.AdminTailPath` —
   the field addition to `Config` (a 1-line change) lives in
   `internal/config/config.go`, which Plan 18-01 is also editing
   (D-18-01/02/03 all touch `config.go`). Sequential execution avoids a
   trivial merge conflict on the struct field block.

2. **18-02 D-18-08 before any other 18-02 task**: Within Plan 18-02, the
   `Config.AdminTailPath` addition is a precondition for the
   `cmd/otto-gateway/main.go` tailer wiring change. The tailer's WARN-on-
   open-fail change is a smaller, independent edit but reads the same
   field.

3. **18-03 last**: Per CONTEXT.md "Recommended order ... 18-01 → 18-02 →
   18-03 (config-first so observability tests can rely on the config path
   resolution landing first; tray last because TRAY-08 sentinel-file naming
   may want to align with config-error semantics from 18-01)."

### Within-task TOCTOU and lifecycle concerns

- **D-18-03 bind-then-close TOCTOU**: Accepted per CONTEXT.md.
- **D-18-04 stderr scanner goroutine**: Lifecycle tied to pipe closure
  (auto on subprocess exit). `goleak.VerifyNone` in the regression test
  guarantees no leak. The only risk: if a future change makes
  `cmd.StderrPipe()` fail (e.g., `cmd.Stderr` already set elsewhere), the
  goroutine never launches and the kiro stderr is silently dropped. Plan
  should NOT log to stderr on `StderrPipe()` failure (would defeat the
  purpose) — but SHOULD log at `Warn` via `acp.Config.Logger` with a
  fallback hint.
- **D-18-08 `AdminTailPath` readability**: `config.Load()` runs before
  `buildLogger`. The writer (chat-trace rotator at `main.go:302`) is
  constructed BEFORE the admin handler (which sets up the tailer at
  `main.go:624+`). Both consume `cfg.AdminTailPath` — same source, no
  divergence possible.
- **D-18-09 wrapper write ↔ tray read race**: ≤ 3s flap (poll interval).
  Accepted.
- **D-18-05 INFO emission ordering inside `respawnSlot`**: Two viable
  sites — inside the `p.mu` critical section (between steps 4 and 5 at
  lines 333-336) OR after `p.mu.Unlock()` (line 337). Research
  recommends AFTER unlock to keep the critical section narrow (matches
  the codebase's documented anti-pattern at
  `exit_watcher.go:12-14`: "Concurrency contract: the watcher holds
  p.mu ONLY for the slot.dead assignment — no slot.Client method calls
  under p.mu").

### Risks vs. CONTEXT.md Risks table

CONTEXT.md's Risks table covers the load-bearing scenarios. Research adds:

- **D-18-04 stderr scanner blocks on a non-newline-terminated stream that
  exceeds 1MB**: `bufio.Scanner.Scan()` returns false on the buffer-overflow
  error (`bufio.ErrTooLong`); the goroutine then exits silently. This means
  a runaway kiro-cli stderr (a single 100MB unterminated debug dump) would
  cause the scanner to stop logging after the first 1MB, but NOT leak.
  Acceptable per CONTEXT.md "Lines longer than 1MB get split at the buffer
  boundary." Verify in test: `scanner.Err()` after the loop checks
  `bufio.ErrTooLong` — the scanner with `Buffer(buf, max)` returns this
  error on overflow. The CONTEXT.md statement "split at the buffer boundary"
  is INCORRECT for plain `bufio.Scanner` — the scanner STOPS on overflow.
  Planner should clarify whether D-18-04 needs custom token-splitting (use
  `bufio.Reader.ReadString('\n')` like `tail.go` does) or accepts the
  scanner's stop-on-overflow behavior.

## Security Domain

Phase 18 is reliability long-tail, not a new attack surface. ASVS
applicability is narrow:

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | yes (D-18-01) | Bearer token + IP allowlist already implemented; D-18-01 strictly tightens UX (loud warn) without changing the security posture (no auth if env unset, same as Node parity) |
| V3 Session Management | no | — |
| V4 Access Control | yes (D-18-01) | IP allowlist via `netip.Prefix`; D-18-01 closes silent-bypass-via-degenerate-env path |
| V5 Input Validation | yes (D-18-02) | `KIRO_CMD` / `KIRO_CWD` operator-controlled boot inputs; D-18-02 adds validation; G204 already exempted at `client.go:286` (operator-controlled, not request-time) |
| V6 Cryptography | no | — |
| V7 Error Handling and Logging | yes (D-18-04, D-18-06, D-18-07, D-18-08) | Structured slog; existing `chi.middleware.Recoverer` covers HTTP handlers; D-18-07 extends to background goroutines |

### Known Threat Patterns for Go subprocess + slog stack

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| Subprocess stderr injection of fake log records (D-18-04) | Tampering (forge log entries) | Log the raw stderr line as a STRING field `"line"` — slog handlers JSON-escape the value. A malicious kiro-cli cannot forge a separate JSON record because the `"line"` field's value is escaped. **Verified in test**: assert that an attempted forge string `"\",\"level\":\"ERROR\",\"msg\":\"FORGED"` arrives as a single record with the literal payload |
| Path-injection via `KIRO_CWD` after tilde expansion (D-18-02) | Tampering | `os.UserHomeDir()` returns the OS-managed home; `filepath.Join` cleans `..` traversal. Operator-controlled boot input (not HTTP-request input) — same trust boundary as today |
| Config error detail leak via sentinel file (D-18-09) | Information Disclosure | Sentinel mode 0o600; contains env parse error message which may include partial env var contents. **Mitigation**: trim sentinel to one line per CONTEXT.md; truncate to a max length (e.g., 200 bytes) to bound disclosure |
| Goroutine panic carrying request context to ERROR log (D-18-07) | Information Disclosure | `debug.Stack()` includes goroutine state; ensure stack does not include request bearer tokens. Existing `engine.go:317-329` pattern is precedent and has not surfaced as a leak in v1.5–v1.10.2 audits — same posture applies |

## Sources

### Primary (HIGH confidence — codebase, verified by Read tool)

- `internal/config/config.go:1-1154` — env load, validation precedents
- `internal/acp/client.go:280-340, 1188` — subprocess spawn, PID access
- `internal/pool/pool.go:258-339, 620-770, 840-870` — respawnSlot, exit-watcher, ctx-watcher
- `internal/pool/exit_watcher.go:1-51` — death log site
- `internal/admin/tail.go:1-541` — tailer goroutine + ring buffer
- `internal/engine/engine.go:255-340` — context.AfterFunc + panic-recover template
- `internal/adapter/ollama/ndjson.go:550-610` — REL-HTTP-03 WARN field set
- `internal/adapter/ollama/handlers.go:220-340` — eng.Run error path (D-18-06 site)
- `cmd/otto-gateway/main.go:80-145, 295-310, 615-690` — boot sequence, logger build, tailer wiring
- `cmd/otto-tray/tray.go:1-468` — probe, FSM application
- `cmd/otto-tray/fsm.go:1-75` — state computation
- `cmd/otto-tray/autostart_darwin.go:15` — actual plist label `io.cmetech.otto-tray`
- `scripts/otto-gw:200-225, 1920-1962` — env loader, broken bundle rows
- `internal/server/server.go:411-535` — Run / RunUntilSignal goroutines (chi.middleware.Recoverer at 223, 279)
- `internal/config/regression_rel_cfg_03_test.go:31-93` — `captureSlogDefault` pattern
- `internal/pool/regression_rel_pool_01_test.go:59-90` — fakeClient + fireDone() scaffolding
- `internal/adapter/ollama/regression_rel_http_03_test.go` — REL-HTTP-03 regression precedent
- `.planning/phases/18-reliability-long-tail/18-CONTEXT.md` — locked decisions
- `.planning/REQUIREMENTS.md` — REQ-ID descriptions
- `Makefile:255-260` — CI gate composition

### Secondary (MEDIUM confidence)

- `tests/reliability/manual/REL-TRAY-02-repro.ps1` — Windows bundle test scope confirmed (no assertion on rows that 18-03 removes)
- CLAUDE.md project constraints — stack, env-var contract, gosec posture

### Tertiary (LOW confidence)

- None — Phase 18 is entirely codebase-grounded.

## Metadata

**Confidence breakdown:**

- Standard stack: HIGH — no new libraries; all stdlib + existing deps
  (`goleak`, `chi`, `slog`).
- Architecture: HIGH for 18-01 and 18-02; MEDIUM for 18-03 (sentinel-file
  probe integration with `probeFunc` requires a small architecture decision
  flagged in Open Questions §2).
- Pitfalls: HIGH — every pitfall is codebase-verified (line refs to existing
  precedents).
- Validation Architecture: HIGH — all 10 REQ-IDs have a test framing and
  test-file path; goleak / captureSlogDefault / fakeClient scaffolds already
  exist.

**Research date:** 2026-06-11
**Valid until:** 2026-07-11 (Go ecosystem stable; codebase change cadence ~weekly — re-verify line numbers if Phase 19 lands before Phase 18 execution)
