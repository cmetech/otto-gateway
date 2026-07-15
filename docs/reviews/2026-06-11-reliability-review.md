# OTTO Gateway — Reliability & Stability Code Review

**Date:** 2026-06-11
**Focus:** Production readiness for a laptop deployment — no SRE, no auto-restart, no log aggregation. Reliability first; performance secondary. Silent failure modes treated as high severity.
**Method:** Five parallel review passes (subprocess pool/ACP lifecycle, HTTP surface, goroutine/resource discipline, tray/UI, config/startup/observability), each tracing failure paths end-to-end in the actual code. All file:line references verified against the source at commit `9212d5b`. Two findings (P-3, P-5) were independently discovered by two separate passes.

**Totals:** 1 Critical · 8 High · 14 Medium · 12 Low

---

## Top 5 most critical issues

1. **[Critical] The pool can shrink permanently to zero, after which every request blocks forever** (`internal/pool/pool.go:534`, `:491-505`). A transient spawn failure (disk full, binary replaced mid-upgrade, fd exhaustion) permanently removes slots; nothing ever re-adds them, and slot acquisition has no timeout and no empty-pool check. Once empty, all requests hang indefinitely with no HTTP error and no recovery short of a manual restart. → [P-1](#p-1)
2. **[High] Ctrl-C during a long generation orphans kiro-cli process trees** (`cmd/otto-gateway/main.go:131`). `http.Server.Shutdown` doesn't cancel in-flight streams, the 30s grace expires, and `os.Exit(1)` skips the deferred `cleanup()` — so `pool.Close()` never runs and the per-pgid kiro-cli trees survive, reparented to init, burning CPU/battery. A second Ctrl-C is swallowed. → [P-2](#p-2)
3. **[High] Stale `awaitPromptResult` clobbers the next request's stream → silent empty 200** (`internal/acp/client.go:868-870`, `:894-896`). After early slot release (disconnect, idle-timeout cancel), the old prompt's goroutine unconditionally nils `activeStream`, dropping every chunk of the *next* prompt on that worker. The client receives a well-formed, empty response — no error anywhere. Independently confirmed by two review passes. → [P-3](#p-3)
4. **[High] Mid-stream worker death is silent truncation on OpenAI and Ollama** (`internal/adapter/openai/sse.go:543-557`, `internal/adapter/ollama/ndjson.go:541-549`). When kiro-cli dies mid-stream, the client gets HTTP 200 + partial output + clean close — no error frame, no `[DONE]`, no `done:true` — and the gateway logs it at *debug*. Users see a half answer presented as complete. → [H-3](#h-3)
5. **[High] The OpenAI idle-timeout path returns a hung worker to the free pool without cancelling it** (`internal/adapter/openai/sse.go:460-462`). The idle timeout fires *because* kiro is wedged, but `StopWatchdog()` suppresses the only `session/cancel` mechanism — the still-generating worker goes back into the free queue and the next request acquires it mid-abandoned-prompt. → [H-2](#h-2)

---

## 1. Kiro subprocess pool management

<a id="p-1"></a>
### [Critical] P-1: Pool shrinks permanently to zero on respawn failure, then all requests block forever with no HTTP error

- **Files:** `internal/pool/pool.go:534` (`removeSlot` on genuine respawn failure), `internal/pool/pool.go:491-505` (slot acquire has no timeout and no empty-pool check), `internal/pool/pool.go:297-306` (`removeSlot` — nothing ever re-adds a slot; `Warmup` at pool.go:137 is the only producer).
- **Failure scenario:** Disk fills up (or brew/npm replaces `kiro-cli` mid-upgrade, or fd exhaustion, or OOM makes `fork/exec` fail). A worker dies; the next request hits the dead-slot branch in `NewSession`, `respawnSlot` fails with a genuine (non-ctx) error, and `removeSlot` drops the slot from `p.all` — permanently. This repeats once per slot until the pool is empty. From then on every `Pool.NewSession` blocks on `<-p.slots`, a channel that will never receive again, until the client disconnects. The transient condition may clear seconds later; the pool never recovers without a process restart.
- **Why it matters:** `/health/pool` reports `Healthy:false` but nothing consumes it to recover. The user experience is "the gateway silently stopped answering"; the only fix is knowing to restart the binary.
- **Fix:** (1) Don't permanently remove the slot on respawn failure — re-queue it (like the ctx-cancel path at pool.go:526-531) and return the error, optionally with a per-slot backoff timestamp so the next acquirer doesn't hammer spawn. (2) Bound the acquire: add a timeout arm (10–30s) to the `select` at pool.go:492 that returns a typed `ErrPoolExhausted` the surfaces map to HTTP 503, so a wedged/empty pool produces errors, not hangs.

<a id="p-2"></a>
### [High] P-2: `os.Exit(1)` on shutdown-grace expiry skips `cleanup()` — kiro-cli process trees orphaned on the most common shutdown path

- **Files:** `cmd/otto-gateway/main.go:127` (`defer cleanup()`), `cmd/otto-gateway/main.go:129-132` (`os.Exit(1)` on `RunUntilSignal` error), `internal/server/server.go:377-381` (`srv.Shutdown` 30s deadline).
- **Failure scenario:** User hits Ctrl-C while a long LLM generation is streaming (routinely > 30s). `http.Server.Shutdown` does NOT cancel in-flight request contexts, and the stream is healthy, so neither the engine watchdog nor the 30s `StreamIdleTimeout` fires. The shutdown deadline expires, `Run` returns an error, and `main` calls `os.Exit(1)` — bypassing `defer cleanup()`, so `registry.Close()`/`pool.Close()` never run. Because `applyPgidAttr` puts every kiro-cli in its own process group (`internal/acp/pool_pgid_unix.go:28-37`), the terminal's SIGINT never reached them and parent death does not kill them: up to `POOL_SIZE` + active-session kiro-cli trees keep running, reparented to init.
- **Additional note:** a second Ctrl-C during the 30s grace is swallowed — `RunUntilSignal`'s signal goroutine exits after the first signal (`server.go:396-403`) and `signal.NotifyContext` keeps the registration, so the default terminate disposition never returns. The user cannot force-quit except `kill -9` (which also orphans the children).
- **Why it matters:** This is exactly the orphan scenario the pgid work (RA6-02) was built to prevent — and it triggers on the everyday "Ctrl-C during a long answer" path.
- **Fix:** (1) Replace the `os.Exit(1)` at main.go:131 with `cleanup(); closeLogger(); os.Exit(1)` (or set an exit code and fall through to defers). (2) Wire `http.Server.BaseContext`/per-request context to a shutdown-cancelable parent so in-flight streams abort during grace. (3) Make a second SIGINT force immediate exit *after* running `cleanup()`.

<a id="p-3"></a>
### [High] P-3: Stale `awaitPromptResult` unconditionally nils `activeStream`, clobbering the next request's stream — silent empty 200 response

*Independently discovered by both the pool-lifecycle and concurrency-discipline review passes.*

- **Files:** `internal/acp/client.go:868-870` (ctx-cancel arm: `c.activeStream = nil` with no identity check; same pattern at client.go:894-896), `internal/pool/pool.go:618-635` (ctx-watcher releases the slot on the same cancel, concurrently), `internal/acp/client.go:795-798` (next `Prompt` installs the new stream).
- **Failure scenario:** The enabling condition is by design: the pool releases a slot back to `p.slots` *before* the slot's previous `awaitPromptResult` goroutine has run. On stream-idle timeout, `ACP.Cancel(sid)` → `Pool.Cancel` (`internal/pool/pool.go:655-672`) returns the slot to the free queue while `awaitPromptResult(A)` is still parked. On client disconnect, the ctx-watcher and the engine watchdog both release the slot concurrently with A's `ctx.Done()` arm. A queued request B then acquires the same slot, `NewSession` + `Prompt` set `c.activeStream = streamB` — and A's late goroutine runs `c.activeStream = nil`. Every subsequent `session/update` for B hits the `s == nil` branch in `handleNotification` (`client.go:1065-1069`) and is dropped with only a Warn log. B's prompt response still arrives via `respCh`, the stream closes cleanly with `stop_reason: end_turn` and **zero content**.
- **Why it matters:** It triggers most often in the exact recovery scenarios (idle-timeout on a wedged kiro, disconnect/retry storms after laptop sleep) where the next request is queued and hot. The result is not an error but a silently empty answer, which users blame on the model.
- **Fix:** Compare-and-swap in both arms (and the send-error path at client.go:815-817 for symmetry):
  ```go
  c.streamMu.Lock()
  if c.activeStream == stream {
      c.activeStream = nil
  }
  c.streamMu.Unlock()
  ```
  Optionally also guard the overwrite in `Prompt` (client.go:796-798) by closing/logging a still-active old stream.

<a id="p-4"></a>
### [Medium] P-4: Slow/stalled chunk consumer blocks the readLoop, which starves ping dispatch — ping escalation then SIGKILLs a healthy worker

- **Files:** `internal/acp/stream.go:105-122` (`push` blocks the caller — the readLoop — when the 64-chunk buffer is full), `internal/acp/client.go:1085` (push called from `handleNotification`, on the readLoop goroutine), `internal/acp/client.go:503-526` (pingLoop: a ping that times out after 10s with `DeadlineExceeded` escalates to `c.cancel()` and kills the subprocess).
- **Failure scenario:** A client laptop lid closes (or the client is SIGSTOP'd/paused in a debugger) mid-stream. The SSE write stalls (no `WriteTimeout` — intentional), the handler stops draining `Chunks`, the 64-slot buffer fills, and `push` blocks the readLoop — the only goroutine that dispatches inbound frames, including ping responses. The next ping times out and pingLoop SIGKILLs a perfectly healthy kiro-cli, failing the in-flight request with `ErrClientClosed`. The pool respawns the slot (process churn), and the log line (`acp.ping.escalated_to_close`) blames the worker, not the consumer.
- **Fix:** Distinguish "worker dead" from "readLoop busy": have the readLoop bump an atomic `lastFrameRead` timestamp and have pingLoop skip escalation if frames (or a push-in-progress flag) show the readLoop is alive but blocked on a consumer; or bound `push` with the *request* ctx in addition to clientCtx so a stalled consumer fails its own request instead of the worker.

<a id="p-5"></a>
### [Medium] P-5: Data race on `Entry.LastUsed` — written under `Registry.mu` in one place, under `Entry.Mu` everywhere else

*Independently discovered by both the pool-lifecycle and concurrency-discipline review passes.*

- **Files:** `internal/session/registry.go:206` (`e.LastUsed = time.Now()` in `Get`'s alive-entry handoff, under `r.mu` only), `internal/session/entry_acp.go:77-79` (`MarkUsed` writes under `e.Mu` — handlers defer it, e.g. `internal/adapter/ollama/handlers.go:138-149`), `internal/session/reaper.go:79` (read under `e.Mu` via TryLock), `internal/session/registry.go:358` (`time.Since(e.LastUsed)` in `watchEntry` with no lock), `internal/session/stats.go:100`.
- **Failure scenario:** Handler A is finishing for sid X (deferred `MarkUsed` under `e.Mu`); handler B issues a request for the same sid and `Registry.Get` writes `e.LastUsed` under `r.mu` concurrently — a write/write race on a multi-word `time.Time`. The reaper can observe a torn value (new wall seconds, stale monotonic field) and mis-evaluate `LastUsed.Before(cutoff)` — worst case reaping a just-used session: the dedicated kiro-cli subprocess is killed and the conversation context silently vanishes; the next turn lazily recreates a blank session. Also a guaranteed `-race` trip, poisoning the project's own trust gates. The codebase already fixed the identical pattern for `Entry.Dead` (CR-04, reaper.go:87-98) but missed `LastUsed`.
- **Fix:** Store `LastUsed` as an `atomic.Int64` of unix nanos so all four sites are race-free without lock coupling; or in `Get`'s alive-entry branch use `e.Mu.TryLock()` for the refresh (skip on failure — a busy entry is by definition not idle) and read it under the same lock in `watchEntry`/reaper.

<a id="p-6"></a>
### [Medium] P-6: Windows: `cmd.Cancel` is a silent no-op and no job object exists — grandchildren orphaned, leader kill delayed 2s

- **Files:** `internal/acp/pool_pgid_windows.go:15` (`applyPgidAttr` no-op) and `:21` (`killProcessGroup` returns nil — a no-op that reports success), `internal/acp/client.go:317-326` (`cmd.Cancel` wired to that no-op; `WaitDelay = 2s`), `internal/acp/client.go:1182-1184` (post-Wait defensive pgrp kill — also a no-op on Windows).
- **Failure scenario (verified against Go stdlib `os/exec.watchCtx`):** On ctx cancel, `cmd.Cancel` runs the no-op and returns nil — Go treats the command as "successfully interrupted" and only after `WaitDelay` (2s) falls back to `TerminateProcess` on the *leader only*. So on every Windows slot teardown/respawn: (a) the worker lives 2 extra seconds during which `Close()` blocks in `cmd.Wait`, and (b) any kiro-cli children (MCP servers, tool helpers) are never killed — `TerminateProcess` does not cascade and no job object was created. The file's comment ("handled by job objects / kernel close") describes machinery that does not exist anywhere in the repo.
- **Why it matters:** A Windows dev accumulates orphaned kiro child processes across every ping-escalation respawn, every reaped session, and every gateway restart — invisible until Task Manager fills up or ports/files stay locked.
- **Fix:** Create a job object with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` per spawn and assign the child to it (close the job handle in `cmd.Cancel`/`Close`). Minimally: make the Windows `cmd.Cancel` call `cmd.Process.Kill()` directly and implement `killProcessGroup` via `taskkill /T /F /PID` as a best-effort tree kill.

<a id="p-7"></a>
### [Low] P-7: SESSION_TTL and ping cadence pause across laptop sleep — idle dedicated subprocesses outlive their TTL by the sleep duration

- **Files:** `internal/session/reaper.go:70-79` (cutoff comparison uses monotonic-bearing `time.Now()`), `internal/session/registry.go:307`, `internal/acp/client.go:505`.
- **Failure scenario:** User opens 10 stateful sessions (10 dedicated kiro-cli subprocesses), closes the lid for 8 hours. Go's monotonic clock does not advance during suspend, so the 30-minute TTL effectively becomes 8.5 hours of wall time — the subprocesses survive the night. (The flip side is correct: no spurious timeouts on wake.)
- **Fix:** If TTL-as-wall-time is the intent, strip monotonic on store (`e.LastUsed = time.Now().Round(0)`) so `Before` falls back to wall-clock comparison — one line, and the reaper becomes sleep-aware. Keep monotonic for in-request watchdogs (correct as-is).

<a id="p-8"></a>
### [Low] P-8: No kiro session is ever closed on warm pool subprocesses — per-slot session objects accumulate for the gateway's lifetime

- **Files:** `internal/pool/pool.go:166-171` (warmup "cleanup" is just `Cancel`; comment acknowledges no session-close RPC), `internal/pool/pool.go:539` (every pool-path request creates a fresh `session/new` on the slot's long-lived subprocess), `internal/acp/client.go:561-567` (`sendNotification` silently drops `session/cancel` when `writeCh` is full).
- **Failure scenario:** A week of uptime means thousands of dead session objects per warm slot inside kiro-cli. If kiro-cli retains per-session state, slot RSS grows until OOM kills a worker (the pool at least detects and respawns). Separately: dropping `session/cancel` when the write channel is full means the cancel most likely to matter (worker busy, stdin backed up) is the one most likely to be lost — the worker keeps generating into the void after client disconnect.
- **Fix:** Verify kiro-cli's per-session retention; if sessions are retained, recycle slots after N sessions served. For the cancel drop: retry once via a short-timeout blocking send instead of `default:`-dropping, and log at Warn when a cancel is dropped.

---

## 2. HTTP surface reliability

<a id="h-1"></a>
### [High] H-1: Graceful shutdown blocks 30s and exits non-zero whenever an admin log-tail (or any long-lived SSE) connection is open

- **Files:** `internal/server/server.go:346-383` (Run/Shutdown), `internal/admin/sse.go:167-203` (sseLoop).
- **Failure scenario:** `http.Server.Shutdown` waits for active connections; it does not cancel request contexts, and no server-shutdown signal is wired into them (no `BaseContext` cancel, no `RegisterOnShutdown`). The admin `/admin/logs/stream` handler loops forever on `r.Context()` — indefinite by design. Operator has the Log Tail tab open and hits Ctrl-C or tray "stop": `Run` waits the full 30s, `Shutdown` returns `DeadlineExceeded`, and `main.go:129-132` logs "server stopped with error" and exits 1. Every restart takes 30s and reports failure; combined with [P-2](#p-2), the deferred cleanup is skipped and kiro-cli children are orphaned.
- **Fix:** Wire a shutdown signal into streams: `srv.RegisterOnShutdown(cancelStreams)` plus have admin `sseLoop` (and ideally the chat emitters) select on it; or use `BaseContext`/`ConnContext` with a cancel invoked before `srv.Shutdown`. Failing that, call `srv.Close()` after `Shutdown` times out and treat the timeout as a clean exit.

<a id="h-2"></a>
### [High] H-2: OpenAI SSE stops the engine watchdog on the idle-timeout and mid-stream write-error paths without ever issuing ACP Cancel — hung kiro session keeps running and the slot returns to the free pool

- **Files:** `internal/adapter/openai/sse.go:460-462` (idle-timeout branch) and `:482-484` (applyChunk write-error branch).
- **Failure scenario:** The D-06 watchdog (`context.AfterFunc` → `ACP.Cancel(sid)`, `internal/engine/engine.go:255-265`) is the only mechanism that sends `session/cancel` when an adapter abandons a stream. On the OpenAI `idleC` branch, `run.StopWatchdog()()` unregisters the AfterFunc while the ctx is still live — and no explicit Cancel is issued. The comment claims it suppresses a "redundant" cancel; that's only true on the `ctx.Done` branch. The idle case is exactly backwards: the timeout fires *because* kiro is wedged. The pool slot is still released via the ctx-watcher (`internal/pool/pool.go:614-635`), so the hung, still-generating worker goes back into the free queue — the next request acquires a slot whose kiro process is mid-abandoned-prompt. Compare the verified-correct siblings: `internal/engine/collect.go:159-165` adds an explicit Cancel on idle-timeout for precisely this reason; ollama's idle branch (`ndjson.go:420-452`) and anthropic's error path (`anthropic/sse.go:798-806`) both leave Cancel intact.
- **Fix:** In both branches, either don't call `stop()` (let the deferred `cancelFn` fire the watchdog like ollama does), or call an explicit Cancel before `stop()` like `CollectFromRun` does.

<a id="h-3"></a>
### [High] H-3: Mid-stream worker death = silent truncation on OpenAI and Ollama surfaces: HTTP 200, partial output, no terminal/error frame

- **Files:** `internal/adapter/openai/sse.go:543-557` (finalizeSSE `rerr != nil`), `internal/adapter/ollama/ndjson.go:541-549` (finalizeNDJSON `rerr != nil`).
- **Failure scenario:** kiro-cli dies mid-stream; the chunk channel closes and `Result()` returns an error. OpenAI logs at *debug* and returns — no error data-frame, no `finish_reason`, no `[DONE]`. Ollama logs at debug and returns — no `done:true` line. The client sees a 200 with partial deltas and a clean TCP close. Pi-SDK (hard-coded `stream:true`) and most OpenAI SDKs end iteration on stream close — a half-finished answer is presented to the user as if it completed. LangFlow's NDJSON consumer loses the `done:true` marker it aggregates on. Debug-level logging means nobody ever learns it happened.
- **Why it matters:** Both surfaces already prove a terminal frame is feasible — the idle-timeout paths emit `data: {"error":...}` + `[DONE]` (openai/sse.go:470-472) and a full `done:true, done_reason:"error"` envelope (ndjson.go:437-450). Anthropic handles this correctly (`anthropic/sse.go:794` emits `event: error`).
- **Fix:** In both `finalize*` `rerr` branches, emit the same surface-native terminal error frame the idle-timeout path emits, and log at WARN — worker death is an operational event, not noise.

<a id="h-4"></a>
### [Medium] H-4: No ReadTimeout and no body-read deadline — a connection that stalls mid-request-body is held for hours

- **File:** `internal/server/server.go:347-360`.
- **Failure scenario:** `ReadHeaderTimeout: 10s` covers headers only; `IdleTimeout: 120s` covers between requests. Once headers arrive, a client that stalls while sending a POST body parks the handler goroutine inside `decodeJSONBody` with no deadline — until kernel TCP keepalive reaps it (~2h default). Wi-Fi drops or the machine sleeps mid-upload from LangFlow; each occurrence leaks a goroutine + fd + connection for hours. The size is capped (4 MiB) but the time is not. Note: a blanket `ReadTimeout` would be wrong — its expiry also breaks long SSE responses — which is presumably why it's absent, but the body-read phase is left fully unbounded.
- **Fix:** Set a per-request read deadline around body decode using `http.ResponseController.SetReadDeadline(time.Now().Add(30*time.Second))` before `decodeJSONBody` and clear it (`time.Time{}`) afterwards in the chat handlers; this bounds body reads without touching streaming writes.

<a id="h-5"></a>
### [Medium] H-5: Admin tailer's 1 MB per-line cap is only enforced for *unterminated* lines — newline-terminated multi-MB lines flow uncapped into the ring buffer and the SSE stream

- **File:** `internal/admin/tail.go:393-427` (cap check at :402).
- **Failure scenario:** `readLines` truncates only when `len(current) > TailerMaxLineBytes && !strings.HasSuffix(current, "\n")`. `bufio.Reader.ReadString('\n')` returns the entire line regardless of length, so any complete line bypasses the cap. With `CHAT_TRACE=true` (chat-trace NDJSON embeds full prompts; bodies allowed up to 4 MiB) and the chat-trace tail open in the admin UI, the 500-line ring can hold 500 × multi-MB strings (GB-scale RSS) and each line ships whole to the browser over SSE. The doc comment claims the cap "bounds memory growth"; it doesn't for terminated lines.
- **Fix:** Enforce the cap unconditionally — truncate `current` to `TailerMaxLineBytes` before broadcast whenever it exceeds the cap, terminated or not.

<a id="h-6"></a>
### [Low] H-6: Ollama streaming `eng.Run` failure echoes the raw engine error string to the client and is never logged server-side

- **Files:** `internal/adapter/ollama/handlers.go:234-237` (handleChat) and `:487-490` (handleGenerate).
- **Failure scenario:** `writeError(w, 500, err.Error())` sends the wrapped engine error verbatim (`"engine: prehook: …"`, ACP/pool internals, possibly hook-originated detail) — and this path is not logged server-side at all, so the only record of the failure goes to the client. Every other error site in all three adapters logs the raw error and returns a generic `"internal error"` (e.g. ollama handlers.go:173-174, openai handlers.go:155-156, anthropic handlers.go:185-186).
- **Fix:** Mirror the sibling sites: `a.cfg.Logger.Error("ollama: engine.Run error", "err", err)` + `writeError(w, 500, "internal error")` at both sites.

<a id="h-7"></a>
### [Low] H-7: Process-killing goroutines without panic recovery: admin tailer, engine watchdog, pool ctx-watcher

- **Files:** `internal/admin/tail.go:279` (`Tailer.run`), `internal/engine/engine.go:255-265` (watchdog `context.AfterFunc` body), `internal/pool/pool.go:625-635` (ctx-watcher).
- **Failure scenario:** chi's `Recoverer` covers only handler goroutines (acknowledged at `server.go:166-173`). The tailer goroutine, the per-run watchdog AfterFunc (which calls into `pool.Cancel` → mutex + channel ops), and the pool ctx-watcher have no `defer recover()`. No concrete panic path was found today (`p.slots` is never closed, so the release send cannot panic), but an unrecovered panic in any of them silently kills the entire gateway — and engine hook panics are already carefully recovered (`callPreHookSafe`/`callPostHookSafe`); these goroutines deserve the same insurance.
- **Fix:** Add `defer func(){ if r := recover(); r != nil { logger.Error(...) } }()` at the top of `Tailer.run`, the watchdog AfterFunc closure, and the ctx-watcher.

---

## 3. Goroutine and resource discipline

Every goroutine launch site in the gateway binary (14 sites), all channel/mutex usage in those packages, every timer/ticker, and every growing map was traced end-to-end. Two findings from this pass duplicate section 1 (the `activeStream` clobber → [P-3](#p-3); the `LastUsed` race → [P-5](#p-5)) — both independently confirmed. One unique finding survived:

<a id="g-1"></a>
### [Medium] G-1: Non-streaming aggregation error paths skip PostHooks — `LoggingHook.startTimes` and `ChatTraceHook.startTimes` sync.Map entries leak unboundedly

- **Files:** `internal/engine/collect.go:150-166` (idle-timeout return) and `:167-171` (`Result()` error return) — both return before the PostHook traversal at `:187-191`; `internal/adapter/anthropic/collect.go:169-180` and `:182-185` — both return before `RunPostHooks` at `:207`. Leaked state: `internal/plugin/logging.go:186` (`startTimes.Store`, reclaimed only by `:215` `LoadAndDelete` in `After`); `internal/plugin/trace.go:222` (reclaimed only by `:248`).
- **Failure scenario:** `engine.Run`'s own error paths were audited and fixed (`runErrCleanup`, engine.go:212-216), and all *streaming* paths deliberately run PostHooks even on disconnect — but every **non-streaming** request that dies in aggregation (idle timeout → 504, stream `Result()` error → 500) permanently strands one entry per stateful hook, keyed by a never-repeating ULID. kiro-cli wedges while LangFlow polls `/api/chat` non-streaming on retry: every request leaks two sync.Map entries forever — slow, invisible memory growth on an unattended laptop. Secondary silent failure: `chat-trace.log` never gets its `post_chain_out` record for failed requests, so the operator's primary forensic tool has gaps precisely on the failures they'd investigate.
- **Fix:** Mirror the streaming discipline — in `CollectFromRun` and `CollectAnthropicChat`, run the PostHook chain (with a partial/synthetic or nil response, which production hooks already nil-guard) on the idle-timeout and `Result()`-error returns before propagating the error.

---

## 4. Tray / UI reliability

<a id="t-1"></a>
### [High] T-1: PID identity is never verified — Stop/Restart can kill an unrelated process, and a stale pidfile wedges the tray in an unrecoverable "error" state

- **Files:** `scripts/otto-gw:553-561` (start), `scripts/otto-gw:776-786` (stop), `scripts/otto-gw.ps1:388-394` (Start-Gateway), `scripts/otto-gw.ps1:560-566` (Stop-Gateway), `cmd/otto-tray/tray.go:144-151` (makeProbe), `cmd/otto-tray/fsm.go:40-50`.
- **Failure scenario:** Both wrappers and the tray probe treat "pid in pidfile is alive" as "that pid is otto-gateway" — nothing checks the process name/command line, even though identity-aware machinery exists (`gateway_pids()` at `otto-gw:646`, `Stop-GatewayByName` in the ps1) but is only used when the pid is *dead*. Windows recycles PIDs aggressively; macOS recycles after reboot, and the pidfile survives both. After a crash/power loss: (1) the OS hands the PID to another process; (2) the tray probe sees alive-pid + failing `/health` → FSM shows error; (3) user clicks **Start** → wrapper says "already running (PID N)", exit 1 — Start fails forever with no in-UI recovery; (4) user clicks **Stop**/**Restart** instead → `kill "$pid"` / `$proc.Kill()` **kills the innocent recycled-PID process**, then Restart masks what happened.
- **Fix:** Before trusting a live pid, verify it is the gateway (darwin: `ps -p $pid -o comm=`/`args=` contains `$OTTO_BIN`; ps1: `$proc.Path -eq $BinPath`). On mismatch, treat the pidfile as stale (delete, fall through to `stop_by_name`/start). Mirror the same check in `makeProbe` (tray.go:145) so a recycled PID reads as *stopped*, not *error*.

<a id="t-2"></a>
### [High] T-2: Windows support bundle fails exactly when the gateway is down — `exit 1` inside `Get-GatewayStatus` terminates the whole `support` run

- **Files:** `scripts/otto-gw.ps1:584-593` (`exit 1` at 587 and 593), called from `Invoke-Support` at `scripts/otto-gw.ps1:1464`.
- **Failure scenario:** `Invoke-Support` captures status via `try { Get-GatewayStatus 2>&1 | Out-String } catch {...}`, but PowerShell `exit` is script-terminating flow control — `try/catch` does not catch it. When the pidfile is missing (587) or stale (593), the whole script exits 1 mid-collection; the `finally` at 1645 deletes the staging tree. The bash wrapper explicitly solved this exact gotcha with a subshell capture (`otto-gw:1838-1840` and its comment); the ps1 port did not. Gateway crashed → user opens tray → "Create Support Bundle…" to gather evidence → "Support Bundle Failed" with no useful stderr. The bundle is unobtainable in the primary triage scenario it was built for.
- **Fix:** Refactor `Get-GatewayStatus` to `return` a status (or inline the probe in `Invoke-Support`) and keep `exit 1` only in the top-level `status` dispatch arm.

<a id="t-3"></a>
### [High] T-3: Gateway death is effectively invisible on macOS — the only proactive signal is a notification the codebase itself documents as silently no-op'ing, and the icon/tooltip never change

- **Files:** `cmd/otto-tray/tray.go:199-201` (death notify), `cmd/otto-tray/tray.go:74-75` (icon/tooltip set once), `cmd/otto-tray/uihelpers_darwin.go:43-58`.
- **Failure scenario:** On FSM transition Running→Stopped/Error, the only signal is `notify(...)`, which on macOS is `osascript display notification`. The comment at `uihelpers_darwin.go:51-58` states that notification banners silently no-op for LSUIElement agents without notification permission — the known-broken channel that already forced About onto `display dialog`. Meanwhile `setIcon`/`SetTooltip` are called exactly once in `onReady`; `applyState` only edits menu-item titles, invisible until the menu is opened. Gateway OOMs while the user is in LangFlow: the menu-bar icon looks identical to healthy, the banner never renders, and the user's first signal is their client erroring — exactly the failure the tray exists to prevent. Same applies to "Failed to start: …" from `handleStart` (tray.go:224).
- **Fix:** Change the icon (or append "⛔/⚠" via `SetTitle`) per state in `applyState`, and update `SetTooltip`. Route critical failures through `infoDialog` or fix notification registration for the .app bundle.

<a id="t-4"></a>
### [Medium] T-4: Windows `notify()` is a blocking modal MessageBox invoked synchronously on the uiLoop — status pipeline stalls up to 30s and a modal pops on every stop

- **Files:** `cmd/otto-tray/tray.go:199-201` (applyState → notify, on the uiLoop goroutine), `cmd/otto-tray/uihelpers_windows.go:50-68`.
- **Failure scenario:** `uihelpers_windows.go:57-59` asserts the caller runs notify from a background goroutine — false for the `applyState` call site: `uiLoop` (tray.go:169-173) calls `applyState` → `notify` synchronously. The MessageBox blocks until dismissed (or the 30s ctx kills PowerShell). While blocked, `stateCh` (cap 4) fills and the poller blocks on send (`poller.go:56-59`): polling and menu updates freeze. Every intentional "Stop gateway" pops a foreground-stealing modal; if the gateway crashes while the user is away, tray status is stale until the MessageBox times out.
- **Fix:** Dispatch `notify` from `applyState` in a goroutine, or replace the Windows notify primitive with a non-modal toast and reserve MessageBox for `infoDialog`.

<a id="t-5"></a>
### [Medium] T-5: Tray can show "running" while the pool is wedged — snapshot errors are swallowed, `/health` hardcodes "ok", and the purpose-built `/health/pool` probe is never used

- **Files:** `cmd/otto-tray/tray.go:153` (`snap, _ := client.snapshot()`), `cmd/otto-tray/fsm.go:52-54`, `internal/server/health.go:70-77` (Status always "ok"), `internal/server/server.go:248-251` (`/health/pool` exists, unused by tray).
- **Failure scenario:** Two gaps. (1) `makeProbe` ignores the snapshot error: on any snapshot failure `snap` is zero ⇒ `PoolSize == 0` ⇒ the `PoolAlive == 0` degraded check is skipped ⇒ StateRunning — a failure of the very endpoint that detects degradation silently upgrades status to healthy. (2) Degraded only fires on `Alive == 0`; workers alive-but-hung (`Busy == Size` forever after sleep/wake) show "running" while every chat request times out. The gateway already ships `/health/pool` ("is the pool actually serving requests?") and the tray never calls it.
- **Fix:** Treat a snapshot error as degraded-unknown (e.g. `Detail: "snapshot unavailable"`) instead of zero-value running; add a `/health/pool` fetch and degrade on its non-OK verdict.

<a id="t-6"></a>
### [Medium] T-6: Windows bundle-path parsing breaks — the tray treats the *entire* wrapper stdout as the archive path, and `Initialize-Config`'s `Write-Host` lines land on redirected stdout

- **Files:** `cmd/otto-tray/tray.go:296-299`, `scripts/otto-gw.ps1:321,330` (`Write-Host "loaded env file: …"`), `scripts/otto-gw.ps1:1644` (`Write-Output $outPath`).
- **Failure scenario:** `handleSupportBundle` does `path := strings.TrimSpace(res.Stdout)` — the whole buffer. The bash wrapper keeps stdout clean (`load_config` writes to stderr, `otto-gw:467,476`); the ps1 does not — with stdout redirected to a pipe (as `runner.go:37-39` does), `Write-Host` output lands on the redirected stdout handle. Whenever an env file exists (every real install), stdout = `"loaded env file: …\r\n…\r\nC:\…\otto-support-….zip"`. `revealBundle` runs `explorer /select,<multi-line garbage>` (fails silently, `uihelpers_windows.go:137-143`); the bundle exists but the user isn't taken to it.
- **Fix:** Parse the last non-empty line of stdout as the path, and/or convert the ps1 `Write-Host` config chatter to `Write-Verbose`/stderr for parity with bash.

<a id="t-7"></a>
### [Medium] T-7: Support bundle size/time is not actually bounded — live-log copies are exempt from the `--max-mb` cap, and `runWrapper`'s 30s SIGKILL produces an opaque failure plus a leaked staging dir

- **Files:** `scripts/otto-gw:1864-1873` (uncapped live-log copies) and `scripts/otto-gw:1957-1989` (cap drops only `*.log.gz`), `scripts/otto-gw.ps1:1489-1494` + `1586-1596` (same), `cmd/otto-tray/runner.go:29-33` (30s ctx, `Cancel` = SIGKILL, no `WaitDelay`), `scripts/otto-gw:1766` (EXIT trap).
- **Failure scenario:** The size-cap loop deletes only rotated `.log.gz` files; the redacted copies of the current-day `otto-gateway.log` and `chat-trace.log` are copied unconditionally. With `CHAT_TRACE`/`DEBUG` on, a single day's log can be hundreds of MB: the bundle blows past `--max-mb 50` with no drop possible, and `redact_stream` (sed over the whole file) + `tar` push the run past the 30s `runWrapper` budget (the comment at tray.go:257-258 assumes "collection completes in seconds"). On timeout, the bash wrapper is SIGKILLed: the `trap … EXIT` never runs (staging dir leaks in `$TMPDIR`), in-flight sed/tar children keep the pipes open so `cmd.Run` blocks until they finish, then the user gets "Failed to create support bundle." with empty stderr.
- **Fix:** Cap (tail) the live-log copies to a per-file byte budget before redaction; raise the support-verb timeout (per-verb timeout in `runWrapper`, e.g. 120s) and set `cmd.WaitDelay`; have the wrapper print progress to stderr so a timeout failure isn't empty.

<a id="t-8"></a>
### [Low] T-8: dotenv read errors are silent — an unreadable/changed `HTTP_ADDR` makes the tray poll the wrong port and report a healthy gateway as stopped

- **Files:** `cmd/otto-tray/dotenv.go:84` (`m, _ := readDotenvFile(...)`), `cmd/otto-tray/tray.go:66` (`dashboardURL` resolved once at startup).
- **Failure scenario:** If `.otto-gw.overrides.env` is unreadable or `HTTP_ADDR` is edited after tray launch, `lookupHTTPAddr` silently falls back and the URL is computed once. The poller probes `127.0.0.1:18080` while the gateway serves elsewhere: PID alive + health failing → tray shows error against a healthy gateway, "Open dashboard" opens the wrong port, and nothing ever mentions the dotenv read failure.
- **Fix:** Log + surface read errors (the function already returns one — it's dropped), and re-resolve `dashboardURL`/status client on each `handleStart`/`handleRestart`.

<a id="t-9"></a>
### [Low] T-9: Support bundle's tray diagnostics are always wrong — macOS autostart probe checks a plist name that has never existed, and `tray-state.txt` reads a file nothing writes

- **Files:** `scripts/otto-gw:1945` (`com.otto.tray.plist`) vs `cmd/otto-tray/autostart_darwin.go:15` (`launchAgentLabel = "io.cmetech.otto-tray"`); `scripts/otto-gw:1922` / `scripts/otto-gw.ps1:1549` read `.otto/tray/state`, which no code ever writes.
- **Failure scenario:** Every macOS bundle reports "LaunchAgent: absent" even when autostart is installed, and `tray/tray-state.txt` is always "(unavailable)" — actively misleading data in the exact artifact built for triage. (The Windows Run-key probe is correct.)
- **Fix:** Correct the plist filename to `io.cmetech.otto-tray.plist`; either have the tray persist its FSM state to `.otto/tray/state` on each `applyState`, or drop the file from the bundle layout. Consider also collecting `support/last-error.log` into the bundle.

---

## 5. Config, secrets, and startup

<a id="c-1"></a>
### [Medium] C-1: Negative/zero POOL_SIZE, SESSION_MAX, SESSION_TTL_MS, SESSION_TICK_INTERVAL_MS, CHAT_TRACE_MAX_AGE_DAYS are silently coerced, not rejected

- **Files:** `internal/config/config.go:313, 336, 343, 353, 487` (parse sites; sign never checked), `internal/pool/config.go:119-122` (`Size <= 0` → 1), `internal/session/config.go:99-108` (TTL ≤ 0 → 30m, tick ≤ 0 → 60s, max ≤ 0 → 32), `cmd/otto-gateway/main.go:296` (negative MaxAge into timberjack).
- **Failure scenario:** An operator sets `SESSION_TTL_MS=0` intending "never expire" — sessions silently get reaped after 30 minutes anyway. `POOL_SIZE=0` silently spawns one worker. Negative `CHAT_TRACE_MAX_AGE_DAYS` goes into timberjack `MaxAge` with undefined pruning behavior on a file holding raw prompts. This contradicts the project's own fail-fast posture (`STREAM_IDLE_TIMEOUT_SEC < 0` IS a boot error at config.go:366-368, and the doc comment on `Config.PoolSize` promises errors for bad values). There is also no upper bound on `POOL_SIZE` — `POOL_SIZE=200` spawns kiro-cli processes until the 30s warmup deadline kills the boot.
- **Fix:** In `Load()`, reject `<= 0` (or `< 0` where 0 is meaningful) for each with the same `errs = append(...)` pattern used for `STREAM_IDLE_TIMEOUT_SEC`; consider a sanity cap on `POOL_SIZE`.

<a id="c-2"></a>
### [Medium] C-2: Negative PING_INTERVAL crashes the process with a raw goroutine panic instead of a config error

- **Files:** `internal/config/config.go:295, 1057-1071` (accepts negative), `internal/acp/client.go:59-61` (`applyDefaults` only fills when `== 0`), `internal/acp/client.go:505` (`time.NewTicker` panics on non-positive interval, in a goroutine with no recover).
- **Failure scenario:** `PING_INTERVAL=-60000` → process dies during pool warmup with a panic stack on **stderr** — and when `LOG_FILE` is set the panic is not in the structured log file, so a user tailing `logs/otto-gateway.log` sees the gateway die with nothing logged.
- **Fix:** Validate `PingInterval > 0` in `config.Load` (boot error naming `PING_INTERVAL`), and defensively change the `applyDefaults` guard to `<= 0`.

<a id="c-3"></a>
### [Medium] C-3: EMBEDDING_MODEL_DEFAULT is documented as a backward-compat env var but is never read anywhere

- **Files:** CLAUDE.md env-var contract; `internal/server/health.go:22-23, 44-47` (`EmbeddingStats` envelope ships) — repo-wide grep finds no code reading the variable and no embeddings endpoint at all.
- **Failure scenario:** A deployment swapping the Node binary for this one keeps `EMBEDDING_MODEL_DEFAULT=...` and it is silently ignored — and any LangFlow flow calling `/api/embeddings` 404s.
- **Fix:** Implement/stub the surface, or at minimum log a startup `Warn("EMBEDDING_MODEL_DEFAULT set but embeddings are not implemented")` and correct the docs.

<a id="c-4"></a>
### [Low] C-4: Degenerate-but-set ALLOWED_IPS / AUTH_TOKEN env values silently disable security — env path lacks the hardening the flag path got

- **Files:** `internal/config/config.go:959-975` (`getEnvStrSliceComma` returns default `nil` when the value trims/splits to nothing) vs `config.go:732-734` (flag path rejects `--allowed-ips=""` as a boot error, with an audit comment describing the exact unset-shell-variable scenario).
- **Failure scenario:** `ALLOWED_IPS=","`, `ALLOWED_IPS="  "`, or `AUTH_TOKEN=" , "` — classic unset-shell-variable artifacts in an env file — silently yield allow-all / auth-off. Mitigation keeping this Low: the startup "auth mode" line (`cmd/otto-gateway/main.go:115-120`) does print `enabled=false ip_allowlist=false` — but only to someone who reads the log.
- **Fix:** In `Load()`, treat "set but resolves to zero entries" as a boot error for these two security knobs (mirroring the flag-path rule), keeping truly-unset = disabled for Node parity.

<a id="c-5"></a>
### [Low] C-5: No tilde expansion or path normalization for KIRO_CMD / KIRO_CWD — boot fails with a low-level OS error rather than a config-named one

- **File:** `internal/acp/client.go:286-287` (passed verbatim to `exec.CommandContext` / `cmd.Dir`).
- **Failure scenario:** `KIRO_CWD=~/projects` (quoted in an env file, so no shell expansion) fails at warmup as `chdir ~/projects: no such file or directory` — loud and pre-listen (good), but nothing names `KIRO_CWD` as the variable to fix. Relative paths resolve against the process cwd, which differs between wrapper-started and hand-started runs.
- **Fix:** In `config.Load`, expand leading `~/` via `os.UserHomeDir`, and stat `KIRO_CWD` when set, returning an error that names the variable.

<a id="c-6"></a>
### [Low] C-6: Port-in-use is discovered only after full pool warmup

- **Files:** `internal/server/server.go:362-368` (listen), `cmd/otto-gateway/main.go:354-359` (warmup first).
- **Failure scenario:** The ordering is correct for readiness, but the most common laptop misconfig — a stale gateway instance still holding the port — is reported only after spawning and initializing all kiro-cli workers, then tearing everything down.
- **Fix:** Bind the listener before warmup (pass it to `http.Serve`), or do a cheap `net.Listen`-and-close probe on `cfg.HTTPAddr` first, surfacing "address already in use" in under a second.

---

## 6. Observability

<a id="o-1"></a>
### [Medium] O-1: Pool exhaustion is completely silent at default log level — requests hang with zero diagnostic

- **Files:** `internal/pool/pool.go:490-505` (acquire parks with no log at any level; acquire/release markers at pool.go:506, 611, 687 are `debugLog` only).
- **Failure scenario:** 4 slots wedged by long generations; the 5th request hangs indefinitely (no acquire timeout — see [P-1](#p-1); the stream-idle watchdog only covers streams after they start). The user's only diagnostic tool — the log — shows the access-log line never completing and nothing else.
- **Fix:** Log `Warn("pool: waiting for free slot", "busy", ..., "size", ...)` when the immediate acquire fails (a `select` with `default` before the blocking wait), and/or bound the acquire wait.

<a id="o-2"></a>
### [Low] O-2: Worker lifecycle logging is asymmetric — death is logged, recovery is not

- **Files:** `internal/pool/exit_watcher.go:41-43` (`Info("pool: slot died")`), `internal/pool/pool.go:242-291` (`respawnSlot` has no success-path log; only the ctx-deferred case logs, at Debug, pool.go:528).
- **Failure scenario:** After a kiro-cli crash the log shows "slot died" and then silence — no way to tell whether the pool healed or is limping.
- **Fix:** One `Info("pool: slot respawned", "label", ...)` after the final step of `respawnSlot`.

<a id="o-3"></a>
### [Low] O-3: kiro-cli stderr bypasses the structured log file

- **File:** `internal/acp/client.go:289` (`cmd.Stderr = os.Stderr`).
- **Failure scenario:** When `LOG_FILE` is set and the binary is started directly (no wrapper redirecting stderr), kiro-cli's own crash output — often the actual reason a slot died — goes to the terminal or nowhere, while the log file shows only the generic "slot died". The admin Log Tail "boot-err" source depends on the wrapper convention (`cmd/otto-gateway/main.go:640`).
- **Fix:** Pipe subprocess stderr through a line-scanner into `logger.Warn("kiro-cli stderr", "line", ..., "slot", ...)`.

<a id="o-4"></a>
### [Low] O-4: Admin log-tail path resolution can silently diverge from the actual log sink, and open failures are Debug-only

- **Files:** `cmd/otto-gateway/main.go:980` (`buildLogger` trims `LOG_FILE`) vs `main.go:638-639, 947-952` (admin tail uses untrimmed `envOrDefault`); `internal/admin/tail.go:303, 345` (open/stat failures logged at Debug only).
- **Failure scenario:** `LOG_FILE=" /tmp/otto.log"` (stray space in an env file) writes to `/tmp/otto.log` but tails `" /tmp/otto.log"`. With `DEBUG` off, the admin UI shows an empty stream with no explanation — on a box where logs are the only diagnostic.
- **Fix:** Reuse one trimmed resolution helper for both sink and tailer; promote the first per-path open failure to `Warn` (once, not per 250ms tick).

---

## 7. Cross-platform pitfalls

Platform-specific findings are filed in their functional sections: Windows process-tree kill is a no-op ([P-6](#p-6)); Windows support-bundle `exit 1` ([T-2](#t-2)), modal notify ([T-4](#t-4)), and stdout pollution ([T-6](#t-6)); macOS notification no-op ([T-3](#t-3)) and wrong LaunchAgent plist name ([T-9](#t-9)); sleep/wake TTL under-enforcement ([P-7](#p-7)).

Verified clean cross-platform: NDJSON/ACP framing tolerates CRLF (`bufio.ScanLines`, 16MiB frame cap); Windows `kiro-cli` → `.exe`/`.cmd` resolution handled by Go's PATHEXT-aware LookPath; `pickCwd` strips the leading slash from Windows `file:///C:/...` URIs (`internal/engine/pickcwd.go:83-85`); Windows tray liveness probe uses `OpenProcess`+`GetExitCodeProcess` (the `Signal(0)`-on-Windows bug is fixed); all in-request watchdogs use monotonic time, so laptop sleep cannot mass-expire in-flight timeouts.

---

## Appendix: verified solid

Areas explicitly traced and found correct (so future reviews can skip re-deriving them):

**Pool / ACP lifecycle**
- `acp.Client.Close` ordering (cancel → drain pending → close stdin → wg.Wait → cmd.Wait with WaitDelay, post-Wait pgrp re-kill on unix): bounded on all paths traced, including wedged-stdin and blocked-readLoop; idempotent via `sync.Once`.
- No hung RPC callers on worker death: `failPending`/`drainAll` uses non-blocking sentinel sends; readLoop EOF, writerLoop write error, and ping escalation converge on the same teardown; every RPC wait is bounded.
- Exactly-once slot release: map-delete-first + `sync.Once` across all three terminal paths (Result drained / ctx cancel / Pool.Cancel); `p.slots` sends can never block or panic.
- Handler-panic slot recovery: net/http cancels the request ctx when a panicking handler returns; the pool ctx-watcher releases the slot.
- Exit-watcher binding (WR-01) and respawn ordering: done-channel captured at spawn site; close-old-first prevents watcher misbinding; watchers exit on `p.closing`.
- WR-07: ctx-cancel-during-respawn re-queues the slot instead of shrinking the pool.
- Unix process-group kill: `Setpgid` + pgrp SIGKILL in `cmd.Cancel` + defensive post-Wait pgrp kill reaps grandchildren on macOS/Linux.
- Registry creation-sentinel discipline: single spawn per sid; waiters bounded by ctx/closing; the `Close`-vs-`createEntry` `wg.Add` race is prevented by the publish-step map check.
- stderr cannot wedge the child in-process (`cmd.Stderr = os.Stderr` — no undrained Go pipe; see [O-3](#o-3) for the observability gap).
- Stream push/close concurrency: no send-on-closed panic; blocked pushes wake on close; cross-session late-update guard prevents stream cross-contamination.
- `STREAM_IDLE_TIMEOUT_SEC` defaults to 30 — zero-chunk hangs bounded by default, mapped to 504 on non-streaming surfaces.

**HTTP surface**
- Request body limits: every decode path uses `http.MaxBytesReader` with sane caps (4 MiB chat/messages/generate, 1 MiB show, 64 KiB stubs), 413 mapped per surface.
- Flush discipline: all three chat surfaces and admin SSE flush after every frame; `Flusher` asserted before any bytes written, with pre-header JSON 500 fallback.
- Client disconnect teardown (normal path): stream contexts derive from `r.Context()`; engine watchdog fires `ACP.Cancel`; slots released exactly once.
- Hook panic containment: `callPreHookSafe`/`callPostHookSafe` recover, log with stack, report to `/health/hooks`; PreHook short-circuits caught before stream headers open on all three surfaces.
- Error envelope fidelity: OpenAI `{"error":{message,type,param,code}}`, Anthropic `{"type":"error","error":{...}}` (including spec-correct mid-stream `event: error` and the `tool_use` stop_reason override), Ollama `{"error": msg}`; `anthropic-version` header check present; both `x-api-key` and `Authorization: Bearer` work.
- Admin SSE backpressure: non-blocking fan-out with per-subscriber drop (16-buffer), bounded 500-line ring, lazy tailer start/stop, `defer Unsubscribe` on all exit paths.
- WriteTimeout omission is deliberate and correct for streaming (documented at server.go:350-359); `ReadHeaderTimeout` 10s and `IdleTimeout` 120s present.
- Idle-stream watchdog consistently armed with drain-safe timer Stop/Reset in all five chunk loops.

**Concurrency**
- All 14 goroutine launch sites traced; aside from the findings above, every one has a guaranteed exit path under worker death, client disconnect, ctx cancel, and shutdown.
- No `time.After` in any loop; all timers use the drain-safe Stop/Reset idiom; all four tickers (pingLoop, reaper, tailer, SSE keepalive) are defer-stopped.
- FD/spawn error paths: pipe-creation failures call `cancel()`; `initSlot`/`respawnSlot`/`createEntry` all `Close()` the fresh client on Initialize/NewSession failure.
- Reaper snapshot-then-TryLock avoids lock inversion; `closeReady` is `sync.Once`-idempotent across all five racing close paths; `entries` bounded by `MaxSessions` + TTL.

**Tray**
- Every menu callback is wrapped in `go ...`; no synchronous HTTP/exec on the systray event handler thread.
- Status client has a 1s `http.Client.Timeout`; poller sends are ctx-cancelable; a wedged gateway cannot freeze the poller or menu.
- Sleep/wake: ticker-driven loop over loopback; ticks coalesce; `consecutiveFailures` resets on recovery.
- Gateway survives tray death (nohup'd, own process group, LaunchAgent `KeepAlive=false`) — intended.
- `systray.Run` called from the main goroutine; macOS/Windows click-to-menu wiring present.
- `/health`, `/health/hooks`, `/admin` are auth-exempt, so the unauthenticated tray probe works with `AUTH_TOKEN` set.
- tray.json: atomic tmp+rename writes; corrupt file degrades to defaults.

**Config / startup / logging**
- Startup ordering: pool `Warmup` is blocking, bounded by 30s, and completes before `ListenAndServe` binds — no request can arrive before workers are ready; warmup failure aborts boot non-zero.
- Missing KIRO_CMD binary / KIRO_CWD dir fail at warmup, pre-listen (clarity caveat in [C-5](#c-5)).
- Auth defaults visible: `auth mode enabled=… ip_allowlist=… trust_xff=…` logged every boot; XFF untrusted by default; `ALLOWED_IPS` parse errors fail closed; constant-time token compare.
- Bad values for DEBUG, AUTH_TRUST_XFF, ENABLED_SURFACES, ENABLED_HOOKS, all PII_* knobs, and unparseable ints/durations are loud boot errors naming the variable.
- Log rotation bounded on both sinks: main log timberjack 500MB + 7-day gzip retention; chat-trace 100MB + 3-day retention, mode 0600. No per-token logging above Debug.
- Error logs correlated: access log + hooks carry `request_id`; pool errors carry slot label; ACP/engine/session logs carry `session_id`.
