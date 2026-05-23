---
status: issues_found
phase: 01-foundations
reviewed: 2026-05-23
depth: standard
files_reviewed: 28
findings:
  critical: 6
  warning: 11
  info: 7
  total: 24
---

# Phase 1: Code Review Report

Phase 1 foundation is functionally sound for what it tries to do: the framer/dispatcher split is clean, the writer-goroutine serialization is documented and tested, and the middleware order matches D-13. However, an adversarial pass surfaces multiple correctness and contract defects — the most serious being a guaranteed `send on closed channel`-style deadlock in `drainAll`, a `Stream.Result()` deadlock if the active stream never receives a terminal frame, a `Close()` ordering inversion that races the readLoop deferred stream-close against `failPending`, and several departures from the documented JSON-RPC and ACP wire contract (missing `jsonrpc` validation on inbound, `permission` field not preserved, `Block` has no `MarshalJSON` so `session/prompt` will emit a discriminator instead of the wire shape kiro-cli expects). Tests are extensive but lean on `time.Sleep` and never exercise the `New()` subprocess code path with even a minimal fake binary, so a large slice of `client.go` is unverified.

## Critical Issues

### CR-01: `dispatcher.drainAll` can deadlock under concurrent `route()` send
**File:** `internal/acp/dispatcher.go:82-89`
**Severity:** BLOCKER

`drainAll` does a blocking send to every pending channel while holding `d.mu`. If a buffered-1 channel is already full because `route()` raced in a write before `drainAll` took the lock, `drainAll` blocks forever holding the mutex → every subsequent `register/cancel/route` blocks → goroutine-leak deadlock on `Close()`.

**Fix:** make `drainAll` use a non-blocking send (`select { case ch <- ...: default: }`).

### CR-02: `Stream.Result()` deadlocks — only `readLoop` ever closes the stream
**File:** `internal/acp/stream.go:72-92`, `internal/acp/client.go:243-255`
**Severity:** BLOCKER

`stream.close` is called only from `readLoop`'s defer (on EOF). There is no terminal-frame handling for `session/update`, no `done`/`stop` reason recognized. Consequence: a successful `Prompt(...)` returns a `Stream` whose `Chunks` channel is never closed; `Result()` blocks forever on `<-s.done`. The next `Prompt` overwrites `activeStream`, silently dropping the prior leaked stream.

**Fix:** close the stream when the prompt response arrives, OR add a stop-notification handler. Either way, the current code produces a leaked, never-closed Stream on every successful Prompt.

### CR-03: `Close()` ordering races readLoop's deferred stream-close against `failPending`; readLoop death not propagated
**File:** `internal/acp/client.go:636-674` + `internal/acp/client.go:243-255`
**Severity:** BLOCKER

When readLoop returns first (subprocess crashes before `Close()`), `clientCtx` is never cancelled. Subsequent callers of `Prompt`/`Initialize` set `c.activeStream` and block on `<-respCh` forever — readLoop is dead and no notifications will arrive.

**Fix:** `defer c.cancel()` at the top of `readLoop` to propagate readLoop death to `clientCtx`.

### CR-04: `permission` field silently discarded — auto-grant cannot honor scoped requests; audit log loses payload
**File:** `internal/acp/translate.go:19-21` + `internal/acp/client.go:570-601`
**Severity:** BLOCKER

`permissionParams` carries only `RequestID`. The wire payload includes a `permission` object describing scope (`shell_exec`, `file_write`, …). Auto-grant unconditionally responds `allow_always` regardless of scope and the audit log can never record what was granted because the field never enters program memory. This violates CLAUDE.md's "one place to enforce policy" core value.

**Fix:** capture `Permission json.RawMessage`; log at Info on every auto-grant; consider echoing it back in `grantParams` if the protocol requires it.

### CR-05: `canonical.Block` has no JSON tags / `MarshalJSON` — `session/prompt` will emit Go default discriminator, not the ACP wire shape
**File:** `internal/canonical/chunk.go:71-95` + `internal/acp/client.go:107-110`
**Severity:** BLOCKER

`Block` marshals via Go's reflect-default encoder as `{"Kind":0,"Text":{"Content":"..."},"ResourceLink":null}` — almost certainly not what kiro-cli expects. The asymmetry is: incoming updates are translated through `translateUpdate`, outgoing blocks are not translated at all. Zero test coverage of an actual Prompt payload masks this.

**Fix:** add a `translateBlock` mirror to `translate.go` (or proper `MarshalJSON` with discriminator) + a `TestPromptWirePayload` asserting on captured JSON.

### CR-06: `rpcFrame.ID` is `*uint64` only — JSON-RPC 2.0 string IDs are silently dropped as malformed; no `jsonrpc` field validation
**File:** `internal/acp/framer.go` + `internal/acp/dispatcher.go:11-17`
**Severity:** BLOCKER

If kiro-cli ever emits a string ID, `json.Unmarshal` fails → readLoop logs "malformed frame" and continues → caller hangs forever. Nothing validates the `jsonrpc: "2.0"` field on inbound.

**Fix:** model ID as `json.RawMessage`, compare by raw bytes (or canonical string form). Add a guard + test for non-numeric IDs. Validate `jsonrpc: "2.0"` on inbound.

## Warnings

### WR-01: `writerLoop` drain-after-cancel races `send()`; a write can win the select after ctx.Done() is already closed
**File:** `internal/acp/client.go:285-294`

A racing `send()` can pick the `c.writeCh <- data` arm in its select even though `c.clientCtx.Done()` is ready, then the writerLoop drains in a separate sequence and silently drops the item. The send returned nil → caller blocks on the response forever.

**Fix:** in `send()`, check `c.clientCtx.Done()` deterministically before attempting the write; or sequence the close so the dispatcher fails-pending strictly before writerLoop drain.

### WR-02: `handleNotification` permission-grant uses `default: drop` — silently loses grants under bursty load
**File:** `internal/acp/client.go:595-601`

The grant write has a `default:` arm that drops on full `writeCh`. The auto-grant docstring literally says "kiro-cli blocks forever if grant is missed." Dropping reintroduces the exact hang the auto-grant is designed to prevent.

**Fix:** block on the send (no `default:` arm). Backpressure pauses incoming reads — that is correct.

### WR-03: `Ping` silently swallows every RPC error as success
**File:** `internal/acp/client.go:458-466`

Treating "method-not-found" as non-fatal is fine, but the code collapses every error code into nil. Real internal-server errors and panics are invisible to the heartbeat.

**Fix:** return `nil` only on `-32601`; return the error otherwise.

### WR-04: `pingLoop` exits permanently on any transient ping failure
**File:** `internal/acp/client.go:301-322`

One `i/o timeout` removes the heartbeat for the rest of the client's life. No restart, no surfacing.

**Fix:** log and continue. Return only on `ErrClientClosed`/`context.Canceled`. If you prefer fail-fast, call `c.cancel()` to tear down the whole client.

### WR-05: `Stream.push` blocks readLoop when consumer is slow — stalls subsequent permission notifications
**File:** `internal/acp/stream.go:58-68`

The buffered-64 channel blocks readLoop once full. While stalled, no other notifications dispatch — including `session/request_permission` — so the whole pipeline hangs.

**Fix:** decouple push from readLoop via a per-stream goroutine + unbounded queue, or document and audit drops.

### WR-06: `cmd.Wait()` returns "signal: killed" on Close() and the code captures it as a real error despite the comment claiming it ignores it
**File:** `internal/acp/client.go:666-672`

Comment says "Ignore signal: killed", code does not.

**Fix:** actually skip signal-kill exits in the `if errors.As(err, &exitErr)` branch.

### WR-07: `New()` leaks stdin/stdout pipes if `cmd.Start()` fails after pipe acquisition
**File:** `internal/acp/client.go:158-207`

`StdoutPipe()` succeeded, `Start()` fails, pipes are never closed → fd leak.

**Fix:** wrap pre-Start in a cleanup helper that closes pipes on Start() failure.

### WR-08: Subprocess stderr wired to `os.Stderr` defeats the JSON-only log contract
**File:** `internal/acp/client.go:166`

`operating.md` and CLAUDE.md commit to JSON `log/slog` lines. kiro-cli plain-text stderr will interleave and break per-line JSON parsers.

**Fix:** capture stderr into a goroutine that emits `slog.Warn("kiro-cli stderr", "line", ...)`.

### WR-09: `PING_INTERVAL=0` parses to 0ms → `time.NewTicker(0)` panics on startup
**File:** `internal/config/config.go:112-126`

No defensive guard.

**Fix:** reject `<= 0` durations, OR skip pingLoop startup when `cfg.PingInterval <= 0`.

### WR-10: scripts/loop24 `status` hardcodes `python3` — silently swallows health output on systems without it
**File:** `scripts/loop24:66`

`|| true` hides the failure; operators see "running" with no health JSON on stock macOS shells where `python3` is missing.

**Fix:** detect `jq`, then `python3`, then `python`, then raw cat.

### WR-11: PowerShell `[int]$null` is `0` — empty PID file targets the system Idle process
**File:** `scripts/loop24.ps1:20, 46, 64`

Empty PID file (kill -9 mid-write) parses to PID 0 → `Get-Process -Id 0` returns Idle → `.Kill()` fails with `Access is denied`.

**Fix:** validate parsed int > 0 before using it.

## Info

### IN-01: `_ = ctx` in `readLoop` is dead — remove the parameter or use it
**File:** `internal/acp/client.go:269`

### IN-02: `_ = acpClient` placeholder in main.go is a TODO that should be flagged so it doesn't ship in Phase 2 as dead code
**File:** `cmd/loop24-gateway/main.go:54`

### IN-03: `Stream.newStream` accepts a `context.Context` it never uses
**File:** `internal/acp/stream.go:44`

### IN-04: `permissionParams` struct has inline-comment padding that gofumpt will flatten
**File:** `internal/acp/translate.go:14-15`

### IN-05: `TestPendingRequestsFailedOnClose` uses `time.Sleep(50ms)` — flaky on CI
**File:** `internal/acp/client_test.go:203`

Replace with a synchronization signal.

### IN-06: `TestSessionUpdateAfterStreamClose` relies on `time.Sleep(100ms)`
**File:** `internal/acp/client_test.go:342`

Same flakiness as IN-05.

### IN-07: README "Project layout" lists Phase 2+ directories that don't exist yet
**File:** `README.md:21-36`

Mark them as planned, or include only the existing ones.

## Structural / Cross-File Observations

- `internal/canonical` correctly has zero imports, but `Block` types lack symmetric JSON tags with `Chunk` types — see CR-05.
- `internal/server` does not depend on `acp` in Phase 1 (correct per `.go-arch-lint.yml`), but Phase 2 wiring will need to add `acp` to `server`'s `mayDependOn`.
- `accessLog` middleware does not capture panics into structured log lines — `middleware.Recoverer` writes panic output to stdlib log, breaking the "every line is a single JSON object" promise from `operating.md`. Consider a custom slog recoverer.

## Test Coverage Gaps

1. `Client.Prompt` is never exercised end-to-end — no test sends `session/prompt`, captures the wire payload, and asserts on translated `canonical.Chunk` values.
2. `Client.New()` (subprocess path) has zero non-skipped coverage — the only exercise is `TestIntegration_RealKiroCLI_SmokeTest` which is skipped without kiro-cli installed.
3. No test covers the `drainAll` deadlock case described in CR-01.
4. No test covers concurrent `Close()` + in-flight `Prompt`.
5. No test covers `pingLoop` transient-failure recovery (WR-04).

---

_Reviewer: gsd-code-reviewer (Claude)_
_Depth: standard_
_Reviewed: 2026-05-23_
