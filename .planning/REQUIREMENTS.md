# Requirements: OTTO Gateway — v1.10.3 Reliability Closeout

**Defined:** 2026-06-11
**Driver:** Deferred items from `docs/reviews/2026-06-11-reliability-review.md` (8 Lows) + 16-REVIEW.md / 17-REVIEW.md Info findings (6 cleanups) + Phase 17 17-02-SUMMARY.md surfaced production race (1 concurrency fix).

This milestone closes the long-tail of issues the v1.9 + Phase 17 work explicitly deferred. None of these items is a load-bearing failure path on its own — they each remove a specific way the gateway lies to its operator (silently disabled security, misreported state, asymmetric logging) or makes future maintenance harder (test-side workaround, duplicated code, dead variables).

## v1.10.3 Requirements

### Config hardening (Phase 18-01)

- [ ] **REL-CFG-05** (C-4, Low): Degenerate `ALLOWED_IPS=","`, `ALLOWED_IPS="  "`, `ALLOWED_IPS=" , "`, or `AUTH_TOKEN=" , "` values no longer silently disable security. Treat as unset with a loud boot Warn that names the variable, matching the existing fail-fast posture on numeric config vars from Phase 16-05 REL-CFG-01.
- [ ] **REL-CFG-06** (C-5, Low): Boot errors for `KIRO_CMD` (binary not found) and `KIRO_CWD` (directory missing) name the offending variable in the error string instead of leaking a low-level OS error (`exec: "x": executable file not found in $PATH`). Tilde expansion for `KIRO_CWD` so `~/work/kiro` resolves cleanly.
- [ ] **REL-CFG-07** (C-6, Low): Port-in-use is discovered during boot validation (pre-warmup), not after a 5–10s pool warmup completes. Operator does not pay the warmup cost only to hit a bind error on the listener.

### Observability symmetry (Phase 18-02)

- [ ] **REL-HTTP-06** (H-6, Low): Ollama streaming `eng.Run` failures emit a server-side WARN log (with `request_id`, `model`, `err`) before the error frame is written to the client. Mirrors REL-HTTP-03 (Phase 15) for the non-streaming path that was already symmetric.
- [ ] **REL-HTTP-07** (H-7, Low): Three goroutines (admin tailer, engine watchdog, pool ctx-watcher) have a deferred `recover()` that logs and exits the goroutine cleanly instead of crashing the process. Defense in depth — no known panic source exists today, but the contract that a goroutine failure does not take down the gateway is now enforced.
- [ ] **REL-OBSV-02** (O-2, Low): Worker recovery is logged symmetrically with worker death. When the pool's exit-watcher detects a kiro-cli death and the lazy-respawn path produces a new client, the success path emits an INFO log line ("pool: slot recovered"). Operator can verify self-healing from logs alone.
- [ ] **REL-OBSV-03** (O-3, Low): kiro-cli stderr is captured into the structured log file, not the process's bare stderr. New entries are tagged with `worker_pid` + `slot_id` so an operator tailing the log sees kiro-cli warnings inline with gateway events.
- [ ] **REL-OBSV-04** (O-4, Low): Admin log-tail path resolution can no longer diverge from the actual log sink. Single source of truth (the configured `CHAT_TRACE_FILE` / log path) reused by both the tailer and the writer. Open failures log at WARN (not Debug) so the operator sees them.

### Tray honesty (Phase 18-03)

- [ ] **REL-TRAY-08** (T-8, Low): dotenv read errors are loud. When the wrapper fails to parse `.otto-gw.env` / `.otto-gw.overrides.env`, the wrapper logs the parse error to stderr, and the tray reflects a distinct "config error" state instead of polling the wrong port and showing "stopped".
- [ ] **REL-TRAY-09** (T-9, Low): Support bundle's macOS-tray diagnostics either report correct data or are removed. The autostart probe checks the actual launch agent plist name we ship (or skips the check on macOS where we don't ship one). `tray-state.txt` either reads a file the tray writes, or the row is removed from the bundle's tray section.

### Concurrency fix (Phase 19-01)

- [x] **REL-ACP-01** (production race surfaced by Phase 17 17-02-SUMMARY.md): `acp.Stream.Result` copies `*s.result` into a local value under `s.mu` instead of returning a pointer-deref that races `close(s.done)` against the StopReason write. After this fix, the Phase 17 test-side workaround (drain `stream.Chunks()` before calling `stream.Result()`) becomes optional and can be reverted in `regression_rel_pool_02_test.go`.

### Code-review backlog burn-down (Phase 20-01)

- [ ] **QUAL-01** (16-REVIEW IN-01): `escapeApplescript` in `cmd/otto-tray/uihelpers_darwin.go` escapes newlines + control chars in addition to `"` and `\`. Defense-in-depth for future operator-controlled strings reaching the AppleScript dialog body.
- [ ] **QUAL-02** (16-REVIEW IN-02): `tooltipForState` is no longer duplicated across `uihelpers_windows.go` + `uihelpers_darwin.go`. Move into a shared build-tag file (e.g., `cmd/otto-tray/tooltip.go` with `//go:build darwin || windows`).
- [ ] **QUAL-03** (16-REVIEW IN-04): `forceCloseCh` channel contract is visible at the type level. Either document the field as "only signaled by `RunUntilSignal`" or move allocation into that method so the `Run`-only path doesn't carry a dead select arm.
- [ ] **QUAL-04** (16-REVIEW IN-05): `tailLines` in `cmd/otto-tray/tray.go` switches from the O(n²) prepend pattern (`kept = append([]string{t}, kept...)`) to a collect-then-reverse pattern. Performance is irrelevant at n=20; this is a readability fix.
- [ ] **QUAL-05** (17-REVIEW IN-01): Dead `sessions` / `sessionsMu` variables removed from `internal/pool/regression_rel_pool_02_test.go:109-110, 122-124`.
- [ ] **QUAL-06** (17-REVIEW IN-02): Stale comment ref to `removeSlot` in `internal/pool/respawn_ctx_cancel_test.go:119` updated to reflect the function's removal in Phase 17-03.

## Cross-cutting constraints

- `make ci` exit 0 end-to-end at every phase close (the v1.10.2 baseline must not regress).
- `go test -race ./...` clean tree-wide (REL-POOL-05 trust gate posture).
- Read-only-implementation rule applies where reasonable — the QUAL-* items are pure refactor, no behavior change.
- TDD mode (`workflow.tdd_mode: true`) — every behavior-adding REL-* requirement gets a failing test commit before the fix commit. The QUAL-* items are behavior-preserving (refactor exemption per `gsd-core/references/tdd.md`).
- PII sentinel format reminder: `[bracket]` not `<angle>` (kiro-cli hangs on angle-bracket sentinels).

## Out of Scope (acknowledged)

- **SEED-001 Authenticode** — still dormant; trigger condition (cert procurement unblocked) not met.
- **Phase 08.3.1 ACP Per-Session Stream Demux** — still waiting for the multi-tenant deployment driver.
- **3 inherited operator-deferred smoke tests** from v1.8 (Phase 06/08.4) — Windows + macOS GUI gates, no platform access change.
- **REL-TRAY-02 + REL-TRAY-03** Windows / macOS GUI operator gates from v1.9 — code wired + statically verified; still awaits human runs on target hardware.
- Performance work.
- New features (no surface expansion in v1.10.3).

## Traceability

| REQ-ID | Phase | Status |
|--------|-------|--------|
| REL-CFG-05 | 18 | Open |
| REL-CFG-06 | 18 | Open |
| REL-CFG-07 | 18 | Open |
| REL-HTTP-06 | 18 | Open |
| REL-HTTP-07 | 18 | Open |
| REL-OBSV-02 | 18 | Open |
| REL-OBSV-03 | 18 | Open |
| REL-OBSV-04 | 18 | Open |
| REL-TRAY-08 | 18 | Open |
| REL-TRAY-09 | 18 | Open |
| REL-ACP-01 | 19 | Open |
| QUAL-01 | 20 | Open |
| QUAL-02 | 20 | Open |
| QUAL-03 | 20 | Open |
| QUAL-04 | 20 | Open |
| QUAL-05 | 20 | Open |
| QUAL-06 | 20 | Open |
