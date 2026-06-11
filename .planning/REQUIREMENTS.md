# Requirements: OTTO Gateway — v1.9 Reliability Hardening

**Defined:** 2026-06-11
**Core Value:** All three API surfaces (OpenAI, Ollama, Anthropic) serve their respective clients without those clients knowing kiro-cli exists, with one place to enforce policy.
**Driver:** [docs/reviews/2026-06-11-reliability-review.md](../docs/reviews/2026-06-11-reliability-review.md) — 35 findings total; this milestone scopes the 23 Critical/High/Medium findings. The 12 Lows roll to v1.10 backlog.

This is a brownfield reliability milestone, not a feature milestone. Each REQ-ID below maps 1:1 to a numbered finding in the review document — the review IS the requirements source, and the requirement text is the inverse of the failure mode ("user does NOT experience X anymore").

## v1.9 Requirements

### Verify reliability findings (Phase 14)

Each of the 23 in-scope findings is re-verified against the current `main` source. A finding survives verification only when the cited file:line still matches the described failure path. False-positives are logged with evidence and dropped from later phases. Findings that need deeper investigation get a follow-up note + carryover decision before Phase 15 starts.

- [ ] **REL-VERIFY-CRIT**: All 1 Critical finding verified against current source — confirmed / false-positive / needs-investigation tag with evidence per finding (P-1)
- [ ] **REL-VERIFY-HIGH**: All 8 High findings verified against current source — confirmed / false-positive / needs-investigation tag with evidence per finding (P-2, P-3, H-1, H-2, H-3, T-1, T-2, T-3)
- [ ] **REL-VERIFY-MED**: All 14 Medium findings verified against current source — confirmed / false-positive / needs-investigation tag with evidence per finding (P-4, P-5, P-6, H-4, H-5, G-1, T-4, T-5, T-6, T-7, C-1, C-2, C-3, O-1)
- [ ] **REL-VERIFY-GATE**: Phase 14 produces a verification ledger that gates Phase 15/16 scope — only confirmed findings flow downstream; deferrals are documented with reason

### Pool / ACP lifecycle reliability (Phase 15 + 16)

- [ ] **REL-POOL-01** (P-1, Critical): The kiro-cli pool no longer shrinks permanently to zero on transient respawn failures. Slot acquisition has a bounded wait that surfaces as a typed HTTP 503 instead of an indefinite hang. Operator can recover without restarting the binary.
- [ ] **REL-POOL-02** (P-2, High): Ctrl-C during a long generation no longer orphans `kiro-cli` process trees. The `defer cleanup()` path runs on every shutdown exit code; in-flight streams are cancelled during the shutdown grace; a second SIGINT forces immediate exit *after* cleanup.
- [ ] **REL-POOL-03** (P-3, High): The "stale `awaitPromptResult` nils the next prompt's `activeStream`" race is closed via compare-and-swap. A queued request acquiring a recycled slot can no longer receive a silent empty 200 response.
- [x] **REL-POOL-04** (P-4, Medium): A slow/stalled chunk consumer no longer starves the readLoop into ping-escalation SIGKILLing a healthy worker. The readLoop signals liveness independently of consumer drain rate.
- [x] **REL-POOL-05** (P-5, Medium): `Entry.LastUsed` no longer races between `Registry.Get`'s alive-entry handoff and `MarkUsed` / reaper reads. `go test -race ./...` passes clean; trust-gate posture is restored.
- [x] **REL-POOL-06** (P-6, Medium): Windows `cmd.Cancel` and `killProcessGroup` actually kill the kiro-cli process tree (not silent no-ops). No 2s `WaitDelay` penalty on every slot teardown; grandchildren do not survive.

### HTTP surface reliability (Phase 15 + 16)

- [ ] **REL-HTTP-01** (H-1, High): Graceful shutdown no longer blocks the full 30s grace and exits non-zero when an admin log-tail (or any long-lived SSE) connection is open. Long-lived streams receive a shutdown signal and unwind cleanly during the grace period.
- [ ] **REL-HTTP-02** (H-2, High): On the OpenAI idle-timeout and mid-stream write-error branches, the hung kiro-cli session is explicitly cancelled before the slot returns to the free pool. Subsequent requests cannot acquire a slot whose worker is still mid-abandoned-prompt.
- [ ] **REL-HTTP-03** (H-3, High): Mid-stream worker death emits a surface-native terminal error frame on OpenAI (`data: {"error":...}` + `[DONE]`) and Ollama (`done:true, done_reason:"error"`) and is logged at WARN. Clients no longer see a half-finished answer presented as complete.
- [ ] **REL-HTTP-04** (H-4, Medium): A stalled mid-request-body upload no longer parks the handler goroutine for hours. Per-request body read deadlines bound the read phase without breaking long SSE response writes.
- [ ] **REL-HTTP-05** (H-5, Medium): The admin tailer's per-line cap is enforced for newline-terminated lines too. A multi-MB chat-trace line cannot fan out uncapped through the ring buffer or the SSE stream.

### PostHook / goroutine discipline (Phase 16)

- [x] **REL-HOOKS-01** (G-1, Medium): Non-streaming aggregation error paths (idle-timeout 504, `Result()`-error 500) run the PostHook chain before propagating the error. `LoggingHook.startTimes` / `ChatTraceHook.startTimes` `sync.Map` entries no longer leak under retry storms on a wedged kiro. `chat-trace.log` gets its `post_chain_out` record on failed requests too.

### Tray / wrapper reliability (Phase 15 + 16)

- [ ] **REL-TRAY-01** (T-1, High): Wrapper Stop/Restart and tray probes verify PID identity (process name / command line) before trusting the pidfile. A recycled PID is treated as "stopped" rather than "error", and Stop/Restart cannot kill an unrelated process.
- [ ] **REL-TRAY-02** (T-2, High): The Windows support bundle completes when the gateway is down. `Get-GatewayStatus`'s `exit 1` no longer aborts `Invoke-Support` mid-collection. Bundle is obtainable in the primary triage scenario it was built for.
- [ ] **REL-TRAY-03** (T-3, High): Gateway death is visibly surfaced on macOS — icon/tooltip change per FSM state, and critical failures route through a channel that does not silently no-op for LSUIElement agents.
- [ ] **REL-TRAY-04** (T-4, Medium): Windows `notify()` is non-blocking. Calling it from `applyState` does not stall the uiLoop for up to 30s or pop a foreground-stealing modal on every intentional stop.
- [ ] **REL-TRAY-05** (T-5, Medium): The tray reports degraded when the pool is wedged (busy-but-not-serving). The status probe consumes `/health/pool` and treats snapshot errors as degraded-unknown rather than zero-value-healthy.
- [ ] **REL-TRAY-06** (T-6, Medium): The Windows tray parses the support-bundle archive path correctly even when the wrapper writes config chatter to stdout. `revealBundle` opens the actual bundle, not a path containing log lines.
- [ ] **REL-TRAY-07** (T-7, Medium): The support bundle's size and time budgets are actually bounded. Live-log copies are capped before redaction. The wrapper's verb timeout accommodates long redactions and emits progress to stderr; staging directories are cleaned up on timeout.

### Config / observability (Phase 16)

- [ ] **REL-CFG-01** (C-1, Medium): Negative or zero values for `POOL_SIZE`, `SESSION_MAX`, `SESSION_TTL_MS`, `SESSION_TICK_INTERVAL_MS`, `CHAT_TRACE_MAX_AGE_DAYS` are loud boot errors that name the variable, matching the existing fail-fast posture for `STREAM_IDLE_TIMEOUT_SEC`. `POOL_SIZE` gets a sanity upper bound.
- [ ] **REL-CFG-02** (C-2, Medium): `PING_INTERVAL <= 0` is validated in `config.Load` as a boot error (named) rather than crashing the process via a raw goroutine panic from `time.NewTicker`. The panic — if it ever fires defensively — lands in the structured log file.
- [ ] **REL-CFG-03** (C-3, Medium): `EMBEDDING_MODEL_DEFAULT` is either implemented/stubbed coherently OR the gateway logs an explicit startup `Warn` when it is set, and the docs / CLAUDE.md env-var contract are corrected.
- [x] **REL-CFG-04** (O-1, Medium): Pool exhaustion (acquire blocked) is visible at default log level via a `Warn("pool: waiting for free slot", ...)` line the first time a request parks. Operators can diagnose "the gateway silently stopped answering" from the log alone.

## v2 Requirements (Deferred to v1.10)

The 12 Low-severity findings from the review, deferred for two reasons: (a) milestone scope discipline — Critical/High/Medium is already 23 findings, (b) cost/benefit — Lows are mostly cosmetic-logging gaps and cross-platform polish that don't change the load-bearing failure surface.

### Sleep/wake + resource discipline

- **REL-LOW-01** (P-7): TTL and ping cadence pause across laptop sleep — idle dedicated subprocesses outlive their TTL by the sleep duration.
- **REL-LOW-02** (P-8): No kiro session is ever closed on warm pool subprocesses — per-slot session objects accumulate for the gateway's lifetime; `session/cancel` silently dropped on full write channel.

### HTTP surface polish

- **REL-LOW-03** (H-6): Ollama streaming `eng.Run` failure echoes the raw engine error string to the client and is never logged server-side.
- **REL-LOW-04** (H-7): Process-killing goroutines without panic recovery (admin tailer, engine watchdog, pool ctx-watcher).

### Tray / config / observability polish

- **REL-LOW-05** (T-8): dotenv read errors are silent — unreadable/changed `HTTP_ADDR` makes the tray poll the wrong port.
- **REL-LOW-06** (T-9): Support bundle's tray diagnostics are wrong — macOS LaunchAgent plist name mismatch; `tray-state.txt` read but never written.
- **REL-LOW-07** (C-4): Degenerate-but-set `ALLOWED_IPS` / `AUTH_TOKEN` env values silently disable security.
- **REL-LOW-08** (C-5): No tilde expansion or path normalization for `KIRO_CMD` / `KIRO_CWD` — boot fails with a low-level OS error rather than a config-named one.
- **REL-LOW-09** (C-6): Port-in-use is discovered only after full pool warmup.
- **REL-LOW-10** (O-2): Worker lifecycle logging is asymmetric — death logged, recovery not.
- **REL-LOW-11** (O-3): `kiro-cli` stderr bypasses the structured log file.
- **REL-LOW-12** (O-4): Admin log-tail path resolution can silently diverge from the actual log sink; open failures Debug-only.

## Out of Scope

| Feature | Reason |
|---------|--------|
| New API surfaces or new feature work | This milestone is purely reliability hardening; no surface or feature additions. |
| Performance optimization | The review explicitly prioritized reliability over performance; perf wins land in a different milestone. |
| Multi-tenant pool model (Phase 08.3.1 ACP demux) | Still awaits its multi-tenant deployment driver — not unblocked by this review. |
| Windows Authenticode code-signing | Still awaits cert procurement — unrelated to reliability findings. |
| Refactoring not in service of a specific REL-* REQ-ID | Strict scope discipline per `CLAUDE.md` "Don't add features, refactor, or introduce abstractions beyond what the task requires." |

## Traceability

To be filled by `gsd-roadmapper` during Phase 14/15/16 creation.

| REQ-ID | Phase | Status |
|--------|-------|--------|
| REL-VERIFY-CRIT | 14 | pending |
| REL-VERIFY-HIGH | 14 | pending |
| REL-VERIFY-MED | 14 | pending |
| REL-VERIFY-GATE | 14 | pending |
| REL-POOL-01 (P-1) | 15 | pending |
| REL-POOL-02 (P-2) | 15 | pending |
| REL-POOL-03 (P-3) | 15 | pending |
| REL-POOL-04 (P-4) | 16 | pending |
| REL-POOL-05 (P-5) | 16 | pending |
| REL-POOL-06 (P-6) | 16 | pending |
| REL-HTTP-01 (H-1) | 15 | pending |
| REL-HTTP-02 (H-2) | 15 | pending |
| REL-HTTP-03 (H-3) | 15 | pending |
| REL-HTTP-04 (H-4) | 16 | pending |
| REL-HTTP-05 (H-5) | 16 | pending |
| REL-HOOKS-01 (G-1) | 16 | pending |
| REL-TRAY-01 (T-1) | 15 | pending |
| REL-TRAY-02 (T-2) | 15 | pending |
| REL-TRAY-03 (T-3) | 15 | pending |
| REL-TRAY-04 (T-4) | 16 | pending |
| REL-TRAY-05 (T-5) | 16 | pending |
| REL-TRAY-06 (T-6) | 16 | pending |
| REL-TRAY-07 (T-7) | 16 | pending |
| REL-CFG-01 (C-1) | 16 | pending |
| REL-CFG-02 (C-2) | 16 | pending |
| REL-CFG-03 (C-3) | 16 | pending |
| REL-CFG-04 (O-1) | 16 | pending |
