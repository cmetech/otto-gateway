---
phase: 18-reliability-long-tail
discussed: 2026-06-11
status: ready_for_planning
---

# Phase 18 — Reliability long-tail (CONTEXT)

## Phase Goal

Close 10 of the 11 deferred Low-severity reliability findings from the
2026-06-11 audit (the 11th, REL-ACP-01, is its own Phase 19). Three loosely-
coupled fix areas with zero file overlap → 3 parallel plans:

- **18-01 Config hardening** (REL-CFG-05, REL-CFG-06, REL-CFG-07)
- **18-02 Observability symmetry + HTTP error logging** (REL-HTTP-06, REL-HTTP-07, REL-OBSV-02, REL-OBSV-03, REL-OBSV-04)
- **18-03 Tray honesty** (REL-TRAY-08, REL-TRAY-09)

Read REQUIREMENTS.md for the v1.10.3 REQ-ID list and per-item descriptions.
This document captures only the implementation decisions that downstream
agents need locked before research/planning.

## Locked Decisions

### D-18-01: REL-CFG-05 — Warn + treat as unset for degenerate auth env values

`ALLOWED_IPS=","`, `ALLOWED_IPS="  "`, `ALLOWED_IPS=" , "`, or
`AUTH_TOKEN=" , "` (and friends — empty after trim, or only delimiters / whitespace)
must produce a loud Warn at boot AND be treated as unset (auth off, allowlist off).

**Do NOT fail-fast** on these like REL-CFG-01 does for numeric vars. The project's
documented posture (CLAUDE.md) is "no auth if env unset" — a degenerate value is
operationally identical to unset for the user's intent. Refusing to boot would
break that contract and surprise existing operators who have noisy `.env` files.

Pattern:
```go
// config.go
rawAllowed := strings.TrimSpace(os.Getenv("ALLOWED_IPS"))
allowed := parseAllowlistCSV(rawAllowed)  // existing fn
if rawAllowed != "" && len(allowed) == 0 {
    logger.Warn("ALLOWED_IPS looks degenerate (no entries after trim+CSV split), treating as unset",
        "raw", rawAllowed)
}
```

Same shape for `AUTH_TOKEN`: after trim, if the token consists only of
delimiters or whitespace, log Warn and treat as unset.

**Side-effect requirement:** The existing boot-time auth-state line at
`cmd/otto-gateway/main.go:115-120` (`enabled=false ip_allowlist=false`)
stays at INFO. Operators who want loud signal already have it via the
new Warn lines.

### D-18-02: REL-CFG-06 — Named config errors for KIRO_CMD / KIRO_CWD

Replace the low-level OS error (`exec: "x": executable file not found in $PATH`,
`stat /Users/foo/work: no such file or directory`) with a config-named error
the operator can act on:

- KIRO_CMD not found → `config: KIRO_CMD (\"<value>\"): not found in PATH or unreadable`
- KIRO_CWD missing → `config: KIRO_CWD (\"<value>\"): directory does not exist`
- KIRO_CWD not a directory → `config: KIRO_CWD (\"<value>\"): not a directory`

**Tilde expansion:** `KIRO_CWD=~/work/kiro` resolves to `$HOME/work/kiro`
(POSIX) and `%USERPROFILE%\work\kiro` (Windows). `$HOME` env-var substitution
NOT in scope — only `~` prefix expansion. The Go pattern is `strings.HasPrefix(v, "~/")` +
`os.UserHomeDir()`. Apply once during `config.Load()`; the expanded path is
what gets stored in `Config.KiroCWD`.

KIRO_CMD does NOT get tilde expansion — it's expected to be a binary name
resolvable by `exec.LookPath` (which uses PATH). If an operator passes an
absolute path with `~`, they get the same named error as any other missing
binary.

### D-18-03: REL-CFG-07 — Bind-then-close port probe in config.Load()

After config.Load() resolves `HTTP_ADDR`, attempt to bind a TCP listener,
immediately close it. If bind fails, emit a config-named error:
`config: HTTP_ADDR (\"<addr>\"): port already in use` (or whatever the
underlying syscall error decodes to).

**Why bind-then-close, not lsof-style probe:** Bind is the only authoritative
test — exactly matches what server.go will do later. Lsof / netstat parsing
is OS-specific and lies under TOCTOU races (a port that's "free" via probe
can be bound in the µs between probe and real bind).

**TOCTOU note:** Bind-then-close has its own TOCTOU window — another process
could grab the port between the probe close and server.go's real bind. That
window is small (the same Go process owns the bind both times, microseconds
apart) and not worth a more complex retry mechanism. The fix prevents the
common case: operator runs second instance, gateway boots through 5–10s of
pool warmup, THEN bind fails. After D-18-03, the bind error is the first
config check, surfaced pre-warmup.

### D-18-04: REL-OBSV-03 — kiro-cli stderr → structured slog entries

Replace `cmd.Stderr = os.Stderr` (currently at `internal/acp/client.go:289`)
with a per-line scanner that emits structured `slog.Warn` entries:

```go
// internal/acp/client.go (around line 289)
stderrPipe, err := cmd.StderrPipe()
if err != nil {
    return fmt.Errorf("acp: StderrPipe: %w", err)
}
go func() {
    defer stderrPipe.Close()
    scanner := bufio.NewScanner(stderrPipe)
    scanner.Buffer(make([]byte, 0, 64*1024), 1*1024*1024)  // 1MB max line
    for scanner.Scan() {
        if line := strings.TrimSpace(scanner.Text()); line != "" {
            c.logger.Warn("kiro-cli stderr",
                "worker_pid", cmd.Process.Pid,
                "slot_id", c.slotID,  // if available; otherwise omit
                "line", line)
        }
    }
}()
```

**Lifecycle:** the scanner goroutine exits when the pipe closes, which
happens automatically when kiro-cli exits. No explicit shutdown needed.
Pipe close is idempotent so the deferred close is safe.

**Line cap:** 1MB per line (matches REL-HTTP-05 TailerMaxLineBytes posture).
Lines longer than 1MB get split at the buffer boundary — acceptable for
runaway debug output.

**Log level:** Warn. kiro-cli stderr is by definition unstructured noise from
a subprocess; if it's quiet kiro is healthy. If it's loud, the operator
needs to see it. INFO would mix it with normal request logging; ERROR
would over-signal for what's usually just debug-style chatter.

**slot_id field:** Plumb the slot index into `acp.Client` at construction
(currently the client doesn't know its slot). If the plumbing is non-trivial,
emit without slot_id for v1.10.3 and add it as a follow-up — the worker_pid
is the load-bearing correlation key.

### D-18-05: REL-OBSV-02 — Worker recovery logged at the lazy-respawn success site

The death log already fires when the pool exit-watcher observes a worker
exit. The recovery log should fire when the lazy-respawn path successfully
constructs a new client and re-arms the slot.

Site: inside `Pool.respawnSlot` (or wherever the WR-03 fix landed —
`internal/pool/pool.go` after `slot.dead = false` and before re-queue).
Field set: `slot_id`, `worker_pid` (new), `previous_pid` (the dead one),
`reason` (e.g. "ctx-cancel-respawn" or "transient-failure-respawn" — the
two existing re-queue arms have different log messages, mirror that).

Log level: INFO. Operator wants to know self-healing happened but doesn't
need to be paged about it.

### D-18-06: REL-HTTP-06 — Ollama streaming err WARN, mirror REL-HTTP-03 fields

Mirror the field set Phase 15-02 used for REL-HTTP-03's `finalizeNDJSON`
error log (`worker_pid`, `kiro_exit_code`, `bytes_streamed`, `session_id`,
`request_id`, `err`). REL-HTTP-06 is the precursor `eng.Run` failure path
that today only echoes the raw engine error to the client.

Add a `slog.Warn("ollama: streaming eng.Run failed", ...)` BEFORE the
error frame write. Fields are best-effort — if `bytes_streamed` is not
yet known (run failed before any chunk emitted), log `0`.

### D-18-07: REL-HTTP-07 — Panic recovery at three known sites

Three goroutines have no panic recovery today and would crash the gateway
if they paniced:

- admin tailer goroutine (`internal/admin/tail.go` `Watch` loop)
- engine watchdog (`internal/engine/` stream watchdog)
- pool ctx-watcher (`internal/pool/pool.go` per-session ctx done watcher)

Each gets a defensive `defer recover()` at the top of the goroutine body:

```go
go func() {
    defer func() {
        if r := recover(); r != nil {
            logger.Error("goroutine panic recovered",
                "site", "admin-tailer",  // or engine-watchdog / pool-ctx-watcher
                "panic", fmt.Sprintf("%v", r),
                "stack", string(debug.Stack()))
        }
    }()
    // existing body
}()
```

The goroutine exits cleanly after recover (no auto-restart) — defense in
depth, not fault tolerance. If a panic happens, something is structurally
wrong and the operator needs to see it; silently restarting would mask the
issue.

### D-18-08: REL-OBSV-04 — Single source of truth for log-tail path

Today the writer (`ChatTraceHook` / configured log destination) and the
tailer (`internal/admin/tail.go`) can resolve different paths if env state
has drifted between writer init and tailer init.

Fix: add a `Config.AdminTailPath` field populated once in `config.Load()`
using the same `deriveChatTraceFile(logFileForDerive)` helper the writer
already uses. Both the writer and the tailer read from this field exclusively.
If the file doesn't exist when the tailer attempts open, log at WARN (not
Debug) with `path=<resolved>` so the operator sees the divergence.

### D-18-09: REL-TRAY-08 — Reuse StateError with Detail = "config error: ..."

The tray FSM already has `StateError` (line ~10 of `cmd/otto-tray/fsm.go`).
On dotenv parse failure:

1. The wrapper script (scripts/otto-gw + .ps1) logs the parse error to
   stderr at boot (operator sees it if running from terminal).
2. The wrapper writes a sentinel file (e.g. `~/.otto-gw/.config-error`) with
   the parse error text on parse failure; deletes it on parse success.
3. The tray's `makeProbe` checks for the sentinel; if present, returns
   StateError with `Detail = "config error: <sentinel contents trimmed to one line>"`.
4. Icon + tooltip pipeline already understands StateError + Detail — no
   FSM changes, no per-platform icon additions.

This is a low-touch fix: 3-line sentinel write/read on the wrapper side, ~10
lines in makeProbe to read the sentinel.

**Out of scope:** Adding a new StateConfigError. Tray FSM expansion is
heavier than v1.10.3 should take on.

### D-18-10: REL-TRAY-09 — Remove both broken macOS tray diagnostic rows

The support-bundle's macOS autostart probe checks a plist name that has
never existed in this codebase. `tray-state.txt` reads a file the tray has
never written. Both rows are misleading.

Fix:
- Remove the autostart probe from the bundle's macOS path. If we ship a
  macOS launch agent in a future milestone, re-add the row pointing at the
  real plist.
- Remove the `tray-state.txt` row from the bundle's tray section. The tray
  has structured logging elsewhere; this row was never useful.

**Bundle structure impact:** Documented in 18-03-PLAN.md verification —
the support-bundle test (`tests/reliability/manual/REL-TRAY-02-repro.ps1`)
asserts presence of specific rows; this fix changes the macOS-side row set,
so the test needs updating in the same commit. The Windows bundle is
unaffected.

## Cross-Plan Wires

All three plans are **independent** — no shared files, no shared symbols
that change ownership. Wave 1 parallel-safe.

| Plan | Files modified (rough) |
|------|------------------------|
| 18-01 Config | `internal/config/config.go`, `cmd/otto-gateway/main.go` (boot-time auth state line — INFO, unchanged level), 3 regression tests under `internal/config/` |
| 18-02 Observability | `internal/acp/client.go` (stderr scanner), `internal/pool/pool.go` (recovery log, panic recover), `internal/engine/` (panic recover, watchdog), `internal/admin/tail.go` (panic recover, single-source path), `internal/adapter/ollama/handlers.go` or `ndjson.go` (REL-HTTP-06 WARN), regression tests |
| 18-03 Tray | `cmd/otto-tray/tray.go` (makeProbe sentinel read), `scripts/otto-gw` + `scripts/otto-gw.ps1` (sentinel write), `tests/reliability/manual/REL-TRAY-02-repro.ps1` (bundle row assertion update — for TRAY-09 side-effect), possibly `cmd/otto-tray/support-bundle.go` if that's where the broken rows live |

## Verification (phase close criteria)

1. `make ci` exit 0 end-to-end (the v1.10.2 baseline must not regress).
2. Each REL-* / REQ-ID has at least one regression test asserting the new
   behavior (or, for tests that already exist and were t.Skip'd, the Skip
   is removed in the same commit as the implementation fix per the D-02
   unskip-in-same-commit pattern v1.9 established).
3. `go test -race ./...` clean tree-wide.
4. `grep -rn "cmd.Stderr = os.Stderr"` returns no production-code hits
   after D-18-04 lands.
5. The boot-time auth-state line at `cmd/otto-gateway/main.go:115-120`
   remains INFO (D-18-01 explicitly does not escalate it).
6. Tray FSM state list unchanged after D-18-09 (no `StateConfigError`
   added).

## Risks

| Risk | Likelihood | Impact | Mitigation |
|------|------------|--------|------------|
| D-18-04 stderr scanner goroutine leak on kiro-cli restart | Medium | Low — scanner exits when pipe closes | The goroutine's exit is tied to pipe closure, which happens automatically when the kiro-cli process exits. Verify in test by spawning + killing a fake kiro and observing goroutine exit (goleak.VerifyNone). |
| D-18-08 single-source path breaks Phase 6.1 admin /admin tail UI | Low | Medium — admin Log Tail panel goes blank | The fix is field-additive (Config.AdminTailPath); existing read paths get updated to read from the new field. If any read site is missed, the existing fallback (whatever path it reads today) still works. Verify by manual /admin smoke test after merge. |
| D-18-09 sentinel-file race between wrapper write and tray read | Low | Low — momentary state flap | The wrapper writes the sentinel BEFORE attempting the launch; tray reads it on every poll (~3-6s). Worst case: tray briefly shows config-error during a config reload. Acceptable. |
| D-18-10 bundle row removal breaks an undocumented external consumer | Very Low | Low | Bundles are operator-only diagnostic artifacts. No external parser exists. |
| Phase 18-02's panic-recover changes mask a real bug | Low | Medium | The recover logs at ERROR with stack trace. Any panic that fires will be loud in the log. The contract is "log + exit goroutine cleanly", not "silently restart". |

## Out of Scope (D-17-05 style)

- REL-POOL-07 (P-7 sleep-pause TTL) and REL-POOL-08 (P-8 kiro session
  accumulation) — explicitly out of v1.10.3 per REQUIREMENTS.md "Out of
  Scope" list.
- Tray FSM expansion (no StateConfigError per D-18-09).
- macOS launch agent (no autostart support per D-18-10).
- Performance work on stderr scanner (1MB line cap is fine for v1.10.3).
- New `LOG_FILE` config options or path normalization beyond what
  D-18-08 already does.

## Next Steps

`/gsd-plan-phase 18` — splits the 3 plans per the wires above.
Recommended order (if sequential auto-degrade per #683): 18-01 → 18-02 → 18-03
(config-first so observability tests can rely on the config path resolution
landing first; tray last because TRAY-08 sentinel-file naming may want to
align with config-error semantics from 18-01).
