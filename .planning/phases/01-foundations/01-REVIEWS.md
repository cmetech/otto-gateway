---
phase: 1
reviewers: [gemini, codex]
reviewed_at: 2026-05-23T17:43:13Z
plans_reviewed:
  - 01-01-PLAN.md
  - 01-02-PLAN.md
  - 01-03-PLAN.md
  - 01-04-PLAN.md
  - 01-SKELETON.md
skipped:
  - claude (self — running in Claude Code session)
  - cursor (auth required — `cursor agent login` not configured)
  - opencode/qwen/coderabbit (not installed)
  - ollama/llama_cpp (false positive detection — :11434 is project port, :8080 is Docker)
---

# Cross-AI Plan Review — Phase 1: Foundations

## Gemini Review

The foundations plan for the Loop24 Gateway is exceptionally well-structured, prioritizing architectural integrity and automated trust gates from the very first commit. It correctly identifies and mitigates subtle Go pitfalls related to subprocess management, concurrency, and I/O. By establishing a "walking skeleton" that includes both a runnable HTTP server and a tested ACP client, the plan provides a solid base for the functional adapters in Phase 2.

### Strengths
- **Zero Global State:** Strict adherence to explicit logger injection (D-15) prevents testing interference and promotes clean dependency management.
- **Robust Subprocess Lifecycle:** The ACP client implementation correctly handles the non-trivial shutdown order (cancel → close stdin → wait for goroutines → reap process) and the "auto-grant" notification race that often plagues stdio-based protocols.
- **"Trust First" Engineering:** Integrating `goleak`, `govulncheck`, and architectural linting before functional code is finished ensures the project won't accumulate technical debt in its core boundaries.
- **Dual Surface Preparedness:** The early separation into `internal/canonical` and the use of streaming channels from day one (D-03) simplifies the future implementation of OpenAI and Ollama adapters.

### Concerns
- **PowerShell Redirect Limitation (MEDIUM):** As noted in Pattern 13, `Start-Process` cannot redirect stdout and stderr to the same file. Plans 01-01 and 01-04 use separate files for Windows (`loop24-gateway.log` and `loop24-gateway-err.log`). This may lead to developers missing error logs if they only tail the primary log file.
- **Integration Test Visibility (LOW):** While auto-skipping is correct for portability (D-17), the "loud skip" must be clearly visible in developer feedback so they are aware the ACP layer is not being verified if `kiro-cli` is missing from their PATH.
- **`go-arch-lint` Dependency (LOW):** The slopcheck warning on `go-arch-lint` is correctly handled with a human checkpoint. However, if rejected, architectural boundary enforcement (REQ-CI-04) will remain manual, which increases the risk of import creep between adapters and the engine.

### Suggestions
- **Windows Log Merging:** Consider updating `scripts/loop24.ps1` to have the `logs` subcommand tail both stdout and stderr files simultaneously (e.g., using `Get-Content -Wait`), providing a unified log view similar to the POSIX implementation.
- **Subprocess Exit Capture:** In `internal/acp/client.go`, ensure that when `readLoop` receives an `io.EOF`, the underlying exit state of the subprocess (from `cmd.Wait()`) is captured and propagated to any active `Stream` callers to differentiate between clean exits and crashes.
- **Health Check "Connected" State:** Even though the pool doesn't exist yet, once `main.go` wires the ACP client, the `/health` endpoint could include a boolean `acp_connected` field. This provides immediate operational feedback that the binary can successfully spawn its backend.

### Risk Assessment
**LOW.** The plan is highly grounded in idiomatic Go patterns and proactively identifies critical pitfalls (like the `bufio.Scanner` buffer limit and the need for byte slice copying). The reliance on the Go standard library for core logic reduces supply-chain risk. The primary remaining risk is platform-specific behavior of the `kiro-cli` subprocess, which is effectively mitigated by the gated integration test.

**Plan Review Verdict: APPROVED**

---

## Codex Review

**Overall Summary**

The plan set is well sequenced and mostly pragmatic: Plan 01 creates a useful runnable slice, Plan 02 tackles the hard ACP client, Plan 03 gates quality, and Plan 04 documents operations. The main weakness is Plan 02. It currently has several lifecycle/protocol gaps that could cause deadlocks, missed phase criteria, or compile failures. I would treat Phase 1 risk as **MEDIUM-HIGH until Plan 02 is tightened**.

### 01-01 Walking Skeleton

**Strengths**

- Good vertical slice: config, logger, chi server, `/health`, `/api/version`, scripts, Makefile.
- Middleware order is explicitly called out.
- D-12 health contract is locked early, which helps later phases extend it additively.
- Keeps binary foreground-only and delegates lifecycle to scripts, matching D-22.

**Concerns**

- **MEDIUM:** Testing `RunUntilSignal` by sending `os.Interrupt` can be flaky in Go tests and may affect the whole test process.
- **MEDIUM:** `scripts/loop24 restart` may race because `stop` removes the PID file immediately after `kill` without waiting for process exit.
- **MEDIUM:** Config helpers silently falling back on invalid `PING_INTERVAL` / bool values hides operator mistakes.
- **LOW:** Access-log tests using `testutil.Logger(t)` cannot easily assert the actual log line unless a buffer-backed logger is used.

**Suggestions**

- Split server lifecycle into a testable `Run(ctx)` or inject the signal channel; keep `RunUntilSignal` as a thin wrapper.
- Add a bounded wait loop in `stop` before removing PID or restarting.
- Make invalid env values return errors from `config.Load()`.
- Use a buffer logger in middleware tests when asserting log fields.

**Risk Assessment:** **LOW-MEDIUM.** The shape is sound; risks are mostly testability and operational polish.

### 01-02 ACP Client Core

**Strengths**

- Correctly identifies the hard part: stdio JSON-RPC framing, id correlation, notifications, shutdown, and leak detection.
- Good use of `sync.Mutex` over `sync.Map` for pending requests.
- Explicit scanner buffer bump and copy semantics are strong Go details.
- `goleak.VerifyTestMain` from day one is the right gate.

**Concerns**

- **HIGH:** Plan requires "one reader goroutine, one writer goroutine," but the described implementation writes directly through `framer.writeFrame`. This does not satisfy ACP-02 and can block caller/read-loop paths.
- **HIGH:** `NewWithConn` can deadlock on `Close()` unless the `io.ReadWriteCloser` itself is closed. Closing only subprocess stdin does not unblock a scanner reading from an injected connection.
- **HIGH:** The plan uses `exec.Command`, but ACP-01 requires `exec.CommandContext`.
- **HIGH:** Pending requests are not failed on `Close()` or read-loop EOF. Callers using non-expiring contexts can hang.
- **HIGH:** `Prompt` references `canonical.Block`, but Plan 01 only defines chunk types. That is a compile-time gap.
- **HIGH:** Integration coverage may not satisfy success criterion #4. The fallback "if triggering permission is difficult, just verify NewSession+Ping" does not prove auto-grant or `session/update` translation against a subprocess.
- **MEDIUM:** `Stream.push` dropping chunks when the channel is full is not acceptable for gateway streaming semantics.
- **MEDIUM:** A single `activeStream` pointer is fragile. Even if Phase 1 supports one prompt at a time, session/update routing and stream close semantics need to be explicit.
- **MEDIUM:** Wiring `acp.New` into `main.go` makes `/health` startup depend on `kiro-cli` being available, while tests skip cleanly when it is absent. That tradeoff should be explicit.

**Suggestions**

- Add a writer goroutine with an outbound request channel and close-aware draining.
- Store and close the injected `io.ReadWriteCloser` in `Close()`.
- Use `exec.CommandContext(clientCtx, cfg.Command, cfg.Args...)`.
- Add a shared `call(ctx, method, params)` helper that selects on request context and client lifetime context.
- On read-loop exit or `Close()`, fail all pending requests with a sentinel error.
- Define `canonical.Block` before implementing `Prompt`, or change the prompt API to use a type that exists.
- Do not drop stream chunks. Prefer backpressure with context cancellation.
- Make auto-grant and update translation deterministic in tests, either with a fake ACP subprocess binary/script plus a real `kiro-cli` smoke test, or with a proven prompt that reliably emits those frames.

**Risk Assessment:** **HIGH.** This plan is the phase's critical path and needs correction before execution.

### 01-03 Trust Gates

**Strengths**

- Correctly runs after implementation so lint and race gates validate the real scaffold.
- Human checkpoint for `go-arch-lint` supply-chain risk is appropriate.
- `make ci`, `make lint`, `make test-race`, `govulncheck`, and `pre-commit run --all-files` cover the requested gates.

**Concerns**

- **MEDIUM:** `go install github.com/fe3dback/go-arch-lint@latest` is not reproducible. The checkpoint discusses v1.15.0, but `latest` can change later.
- **MEDIUM:** The "reject" path conflicts with must-haves that require `.go-arch-lint.yml` and arch-lint execution.
- **MEDIUM:** `pre-commit run --all-files` proves hooks pass, but not that the local Git hook is installed and blocking commits.
- **LOW:** Makefile should define tool paths once, e.g. `GOVULNCHECK := $(shell go env GOPATH)/bin/govulncheck`, instead of repeating shell expansion.

**Suggestions**

- Pin `go-arch-lint` to the reviewed version, e.g. `@v1.15.0`.
- If the package is rejected, make the phase explicitly blocked or document the accepted replacement.
- Verify `pre-commit install` and check `.git/hooks/pre-commit` exists.
- Add a dedicated `arch-lint` target and include it in `ci` only after the tool is approved.

**Risk Assessment:** **MEDIUM.** Strong gate design, but reproducibility and the reject path need cleanup.

### 01-04 Docs

**Strengths**

- Properly scoped to operational docs only.
- Good requirement to read the actual scripts before documenting them.
- Covers PID files, logs, env overrides, status behavior, and both POSIX/Windows paths.

**Concerns**

- **MEDIUM:** Docs mention `LOOP24_LOG_ERR`, but Plan 01's PowerShell script action does not clearly require that env override. Avoid documenting variables the script does not implement.
- **LOW:** `PING_INTERVAL` default should be documented as `60s`; integer env values are interpreted as milliseconds.
- **LOW:** Windows `make build` assumes a make environment. Fine for this repo if expected, but worth noting if Windows users may not have GNU Make.

**Suggestions**

- Align docs exactly with script variables after implementation.
- Use "Default: 60s; integer values are treated as milliseconds" for `PING_INTERVAL`.
- Keep README minimal and leave edge cases in `docs/operating.md`.

**Risk Assessment:** **LOW.** Main risk is documentation drift.

### Phase Goal Coverage

- Success criteria 1, 2, 3, and 5 are covered in intent.
- Success criterion 4 is not fully proven by the current Plan 02 because auto-grant and `session/update` translation can be skipped if hard to trigger.
- The highest-priority fix is to revise Plan 02 around writer-loop ownership, close semantics, pending-request failure, `canonical.Block`, and deterministic ACP integration coverage.

---

## Consensus Summary

Two reviewers, sharply divergent risk verdicts: **Gemini says LOW (APPROVED), Codex says MEDIUM-HIGH (Plan 01-02 needs revision before execution).** Gemini reviewed at a high level and trusted the documented decisions; Codex inspected the plan body and found specific technical gaps that pre-execute revision can cheaply close.

### Agreed Strengths

Both reviewers independently endorsed:

- **`goleak.VerifyTestMain` from day one** — caught explicitly as a strength by both
- **Locked `/health` D-12 JSON contract** — sets the additive-only extension contract cleanly
- **chi middleware order** (RequestID → Recoverer → accessLog) — both flagged this as correctly specified
- **Foreground-only binary + wrapper-script lifecycle** (D-22) — both noted as a clean separation
- **Strict trust gates active from the scaffold** (lint + race + govulncheck before functional code)
- **`internal/canonical` as the leaf package** — sets up Phase 2 adapters cleanly

### Agreed Concerns

Issues raised independently by both reviewers — highest priority:

1. **Integration test cannot prove ACP-04 / ACP-05 without `kiro-cli` deterministically emitting auto-grant + session/update frames** (gemini LOW visibility / codex HIGH coverage gap). Codex's stronger reading: success criterion #4 may not be proven if the integration test falls back to `NewSession + Ping` only. **Action:** make the fallback path itself satisfy the criterion, OR commit to running with `kiro-cli` reliably triggering both frames, OR add a scripted fake subprocess that emits the wire shapes deterministically.

2. **Windows wrapper script stdout/stderr separation** (gemini MEDIUM / codex LOW via docs drift). Logs get split across two files on Windows; developers tailing one may miss the other. **Action:** PowerShell `logs` subcommand should tail both, or merge them.

3. **`go-arch-lint` reject-path dead end** (gemini LOW / codex MEDIUM). If the SUS-flagged tool is rejected at checkpoint, Plan 03's `must_haves` still require `.go-arch-lint.yml` and arch-lint execution — a contradiction. **Action:** define an explicit alternative or accept the manual-enforcement fallback in writing.

### Codex-Only HIGH Concerns (Plan 01-02 — Critical Path)

These were not surfaced by Gemini's higher-level read but are concrete plan-text defects:

1. **HIGH: Writer goroutine missing.** Plan requires "one reader, one writer goroutine per session" (ACP-02), but the described implementation writes directly through `framer.writeFrame` from caller goroutines. Need an outbound request channel + writer goroutine + close-aware drain.

2. **HIGH: `NewWithConn` can deadlock on `Close()`.** Closing subprocess stdin doesn't unblock a scanner reading from an injected `io.ReadWriteCloser`. The injected RWC must be closed in `Close()` for the read loop to exit.

3. **HIGH: `exec.Command` vs `exec.CommandContext`.** ACP-01 requires context-aware subprocess spawning so client-lifetime context cancellation kills the subprocess. The plan currently uses `exec.Command`. Switch to `exec.CommandContext(clientCtx, cfg.Command, cfg.Args...)`.

4. **HIGH: Pending requests not failed on `Close()` or read-loop EOF.** Callers using non-expiring contexts will hang forever on the closed channel. Sentinel error (`ErrClientClosed`) + drain pending map on close.

5. **HIGH: `Prompt` references `canonical.Block`, but Plan 01-01 only defines chunk types.** Compile-time gap unless `canonical.Block` is added to plan 01-01 or `Prompt`'s parameter type is changed to use what 01-01 actually defines.

### Codex-Only MEDIUM Concerns

- **`Stream.push` dropping chunks when channel is full** — unacceptable for gateway streaming semantics. Use backpressure with context cancellation, not silent drop.
- **Single `activeStream` pointer fragile** — even at one-prompt-at-a-time, session/update routing and stream close semantics need explicit handling.
- **`acp.New` in `main.go` couples `/health` startup to `kiro-cli` availability** — should be lazy/optional or this dependency should be documented.
- **`scripts/loop24 restart` race** — `stop` removes PID file before process exit; restart can race. Add bounded wait loop.
- **Config helpers silently fall back on invalid `PING_INTERVAL` / bool values** — hides operator mistakes. Return errors from `config.Load()`.
- **`go install github.com/fe3dback/go-arch-lint@latest` not reproducible** — pin to `@v1.15.0` (the reviewed version).
- **Docs mention `LOOP24_LOG_ERR` but plan 01-01 doesn't clearly require that env override** — align after implementation.

### Divergent Views

- **Plan 01-02 risk:** Gemini reads it as low-risk (trusted the documented patterns); Codex inspected the plan text and found 6 HIGH technical gaps. **Codex's reading is more rigorous and should win** — these are concrete plan-level defects, not stylistic preferences. The fixes are surgical (1–5 lines each in plan 01-02).

- **`RunUntilSignal` testability:** Codex flags `os.Interrupt`-driven test as flaky (MEDIUM); Gemini didn't raise it. **Codex's concern is legitimate** — Go's `signal` package test patterns are notoriously fragile; refactoring to expose a testable `Run(ctx)` is cheap and standard Go practice.

- **Health check should expose ACP connection state:** Gemini suggests adding `acp_connected` to `/health`. This is a Phase 2+ concern per CONTEXT.md `<deferred>` "/health returning 503 when ACP is dead — Phase 5 (Pool) when there's actually something to be down. Phase 1 always returns 200 from /health." **Gemini's suggestion conflicts with locked decisions and should be deferred.**

---

## Recommended Next Step

Run `/gsd:plan-phase 1 --reviews` to revise plan 01-02 incorporating the Codex HIGH concerns (writer goroutine, `NewWithConn` close, `CommandContext`, pending-request failure, `canonical.Block`, integration coverage), plus the agreed-on Plan 03 reproducibility fix (`@v1.15.0` pin) and Plan 01-01 testability split (`Run(ctx)` + `RunUntilSignal` wrapper).

Lower-priority items (Windows log merging, config validation errors, docs drift on `LOOP24_LOG_ERR`) can be addressed in the same revision or deferred to execution.
