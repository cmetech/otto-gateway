# OTTO Gateway Production Reliability Audit
**Date:** 2026-06-06
**Scope:** OTTO Gateway Go LLM gateway, single-user laptop deployment posture
**Agents spawned:** 12 finders + 108 verifiers (3 x 36 confirmed) + 1 synthesis
**Raw findings:** 42  |  **Confirmed:** 36  |  **Dropped in verify:** 6

Note: One MEDIUM finding (`anthropic-streaming-shortcircuit-skips-posthooks`) was merged into the HIGH cross-surface `plugin-chain-streaming-shortcircuit-skips-posthooks` since they describe the same root issue across all three adapter surfaces. Final unique count: 35.

## Critical (process crash, resource leak until restart, or silent SDK breakage)

### acp-push-send-on-closed-channel-panic — Stream.push sends on closed chunks channel when caller-ctx cancel races handleNotification
- **Location:** `internal/acp/stream.go:81-91`
- **Category:** PANIC_HOT_PATH
- **Scenario:** 1) readLoop receives a session/update frame and enters handleNotification (client.go:967). 2) Under streamMu it captures s := c.activeStream, then releases the lock (client.go:987-989). 3) Before the call to s.push at client.go:997, the caller's HTTP/prompt context cancels (HTTP client drops, engine watchdog fires). 4) awaitPromptResult's ctx.Done() arm runs on its goroutine: sets c.activeStream = nil and calls stream.close(...) which closes s.chunks (stream.go:117). 5) readLoop resumes and executes s.push(c.clientCtx, chunk). push's select has only two arms: send on s.chunks (closed -> panic) and <-c.clientCtx.Done() (not fired -- only the PROMPT ctx cancelled). Go picks the send arm -> `send on closed channel` panic in the readLoop goroutine. readLoop has no recover; the entire gateway process crashes. On a laptop with no supervisor this stops all four pool slots and all client traffic.
- **Current behavior:** stream.go push selects only on send-to-chunks and clientCtx.Done(). It does NOT observe the stream's own closeOnce / done channel, so once stream.close() has run the channel is closed but push still tries to send -> panic.
- **Recommended fix:** Make push detect a closed stream. Two viable options: (a) hold a sync.RWMutex (or check s.done) before sending -- under s.mu read s.done's state, and if already closed return ErrSessionClosed instead of sending; (b) protect close+send under the same mutex (close acquires mu.Lock, push checks a `closed` bool under mu.RLock before the send and abandons if true). Either way, after stream.close fires push must NOT send on the closed channel. As a belt-and-suspenders, add `defer func(){ recover() }()` at the top of readLoop so a future regression doesn't crash the process -- but the real fix is the closed-state check in push.
- **Complexity:** small
- **Observability impact:** When this race fires today the process dies with a runtime panic stack -- no slog event, no /health flag, no /admin signal. After the fix, add a Warn slog event 'acp.stream.push_after_close' so operators can see when a late chunk arrived for an already-closed stream (currently silent loss vs. a crash).

## High (user-visible failure without operator notification)

### boot-sigint-before-signal-handler-leaks-kiro-cli — SIGINT during pool.Warmup terminates the process before signal.Notify is wired, orphaning kiro-cli subprocesses
- **Location:** `cmd/otto-gateway/main.go:104-115`
- **Category:** SUBPROCESS_LIFECYCLE
- **Scenario:** Operator launches the binary; pool.Warmup is in flight (e.g., kiro-cli is slow to respond to Initialize on the first slot, or the operator decided they passed the wrong KIRO_CWD and presses Ctrl-C). RunUntilSignal (which installs signal.Notify in internal/server/server.go:372) has not been entered yet because newApp at main.go:104 is still inside pool.Warmup (called at main.go:329 with a 30s warmupDeadline). Go's default SIGINT handler terminates the process. Because internal/acp/pool_pgid_unix.go:32 sets Setpgid=true on every kiro-cli child, the children are in their own process group and do NOT receive the terminal-driven SIGINT. They survive the parent's exit and become orphans/zombies (and on macOS, defunct entries that may persist until reboot if the OS does not reparent them quickly).
- **Current behavior:** main() does the wiring in this order: config.LoadArgs (l.63), buildLogger (l.81), env-file log (l.89), auth-mode log (l.97), newApp (l.104, which calls pool.Warmup at l.329), then ONLY at l.111 does it call srv.RunUntilSignal which installs the SIGINT/SIGTERM handler. There is no signal handler covering the boot window between process start and srv.RunUntilSignal. cleanup() is captured as a return value from newApp at l.104 but the defer on l.109 has not yet run when newApp is mid-Warmup; even if it had, Go's default signal handler does not run deferred functions on signal-driven exit.
- **Recommended fix:** Install signal.Notify(SIGINT, SIGTERM) at the very TOP of main(), before config.LoadArgs, into a buffered channel; or use signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM) and thread that ctx into newApp so pool.Warmup's warmCtx (currently context.WithTimeout(ctx, warmupDeadline) at main.go:327) inherits cancellation. When the boot-time signal fires, call pool.Close on whatever partial pool state newApp constructed before exiting non-zero. The simplest patch: wrap main's body in ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM); defer cancel(); pass ctx into newApp(ctx, ...) so warmupDeadline derives from it; and add explicit pool.Close on the err-branch at main.go:106-108. This ensures Ctrl-C during boot drains kiro-cli children via the same Setpgid-aware kill path that exit_watcher uses post-boot.
- **Complexity:** small
- **Observability impact:** Add a slog.Info("boot: signal handler installed") line at the top of main so an operator sees the signal handler is engaged from t=0. Without it, the silent leak window is invisible in logs.

### session-subprocess-crash-leaves-orphan-entry — Registry never observes kiro-cli subprocess exit (PoolClient.Done() unused) -- crashed sessions stay 'alive' until TTL reap
- **Location:** `internal/session/registry.go:130-216`
- **Category:** SUBPROCESS_LIFECYCLE
- **Scenario:** A kiro-cli subprocess owned by an Entry exits unexpectedly mid-life: OOM kill, segfault, SIGTERM from outside, laptop wake from sleep with broken pipe, or readLoop EOF from the wire. The acp.Client's Done() channel closes. No goroutine in internal/session watches that channel, so Entry.Dead is never flipped. The Entry remains in r.entries with a dead Client. The next request for that X-Session-Id calls e.Client.Prompt() against the dead subprocess, the call returns an error, the surface handler emits 500. The same sid keeps hitting the dead client on every retry for up to TTL (default 30 minutes) -- Get only treats Dead==true as not-present and lazy-recreates. Operator must DELETE /v1/sessions/:id to force replacement.
- **Current behavior:** PoolClient interface declares Done() <-chan struct{} (config.go:33) and acpClientFactory wraps acp.Client which fires Done() on subprocess exit, but registry.go never spawns a watcher goroutine that selects on entry.Client.Done() to set entry.Dead=true and delete from the map. Dead is set exclusively by the reaper after TTL expiry (reaper.go:96). Subprocess deaths between reap ticks are invisible to the registry.
- **Recommended fix:** On successful publish in createEntry (registry.go:285-300), spawn a per-Entry watcher goroutine tracked by r.wg: `go r.watchEntry(sid, e)`. The watcher selects on `e.Client.Done()`, `r.closing`. On Done() fire: acquire r.mu, if entries[sid]==e then delete + set e.Dead=true under r.mu (same pattern as reaper.go:94-97), then close client (idempotent). On r.closing: return. This mirrors how internal/pool/exit_watcher handles dead slots and matches the existing Dead-aware retry path in Get (registry.go:178-181).
- **Complexity:** medium
- **Observability impact:** Add slog.Warn 'session: subprocess exited unexpectedly' with sid + idle_for fields when the watcher fires. /health/agents would show Alive=false immediately rather than stale Alive=true for up to 30 minutes.

### acp-framer-oversized-frame-kills-slot — 1MB scanner buffer cap turns any large ACP frame into a slot-killing EOF
- **Location:** `internal/acp/framer.go:27-53`
- **Category:** SUBPROCESS_LIFECYCLE
- **Scenario:** kiro-cli emits a single JSON-RPC frame larger than 1 MiB. Realistic triggers on a laptop: (a) a session/update tool_call_update carrying a file read of >1 MB (kiro 'read' tool on a large log/JSON), (b) a prompt response embedding a large code block, (c) any future image-result block whose base64 payload exceeds 1.4 MB. bufio.Scanner returns bufio.ErrTooLong on Scan(); framer.readFrame returns the wrapped error; readLoop treats it as EOF/teardown, calls failPending(ErrClientClosed), defer-cancels clientCtx, and exits. Every in-flight prompt on that Client gets ErrClientClosed. The pool's exit_watcher sees the dead Client (via Done()) and respawns a fresh kiro-cli subprocess -- a full slot replacement caused by one oversized message. With POOL_SIZE=4 and a user reading large files via tools, this is observable churn (slot respawn latency ~hundreds of ms per replacement, session state lost).
- **Current behavior:** framer.go:30 fixes the scanner max-token-size at 1024*1024. Any frame >1MB causes readFrame to return an error which the readLoop interprets identically to subprocess exit, killing the Client.
- **Recommended fix:** Three options, in order of preference: (1) Replace bufio.Scanner with a json.Decoder reading off the same io.Reader -- json.Decoder has no fixed line-length cap and naturally handles arbitrary-size frames (ACP frames ARE NDJSON but each is also a complete JSON value). (2) Bump the max-frame size to a much larger cap (e.g. 16 MiB) and surface the cap in Config so operators can tune. (3) On bufio.ErrTooLong specifically, log a loud Warn with the frame prefix and the configured cap, then skip ahead to the next newline and continue rather than treating it as EOF -- preserves session continuity at the cost of one lost frame. Option (1) is the right fix; the others are stopgaps.
- **Complexity:** medium
- **Observability impact:** Today an oversized frame produces a generic 'acp: framer read' error log and a Client teardown with no specific signal that the cause was frame size. Add a distinct slog event 'acp.framer.frame_too_long' with the configured cap and observed prefix length so operators can correlate slot churn with payload size in /admin tail.

### plugin-chain-streaming-shortcircuit-skips-posthooks — Streaming auth short-circuit bypasses PostHooks across all three surfaces -- no plugin.after audit, leaks startTimes entries
- **Location:** `internal/adapter/anthropic/handlers.go:193-201, internal/adapter/ollama/handlers.go:243-252, internal/engine/collect.go:179-183` (and the equivalent OpenAI short-circuit branch)
- **Category:** ERROR_SWALLOWED
- **Scenario:** Client sends a wire request with stream=true and a missing/invalid bearer token. Adapter stamps ctx, calls eng.Run. AuthHook.Before returns a short-circuit *ChatResponse; engine.Run returns a Run handle with run.response != nil. The streaming handler detects ShortCircuitResponse() != nil, writes a 401 error envelope, and returns. RunPostHooks is NEVER invoked on this path. Compare to the non-streaming path (Collect), which iterates PostHooks even on short-circuit at collect.go:179-183. This applies symmetrically to the Anthropic streaming and synthetic-SSE re-route branches (handlers.go:193-201 and 240-243) and the OpenAI streaming short-circuit branch -- LoggingHook.After / ChatTraceHook.After never observe rejected streaming requests, breaking audit pairing and leaking startTimes entries.
- **Current behavior:** Every auth-rejected streaming request: (a) skips LoggingHook.After so the operator NEVER sees a plugin.after slog record for rejected streaming requests -- audit gap on the laptop hot path; (b) skips ChatTraceHook.After so the post_chain_out NDJSON line for that request_id never lands in chat-trace.log (broken pre/post pair, duration_ms correlation broken); (c) ChatTraceHook.Before ran first (prepended at index 0) and stored an entry in its sync.Map startTimes -- that entry is orphaned forever, accumulating one leak per rejected streaming request.
- **Recommended fix:** On streaming short-circuit, before writing the error envelope, invoke eng.RunPostHooks(streamCtx, req, sc) so the chain observes the rejection symmetrically with the non-streaming Collect path. Wrap any PostHook error in a Warn log and swallow it (same pattern handlers.go:287-289 uses after normal stream completion). Apply identically in anthropic/handlers.go (lines 193-201 and the synthetic-SSE re-route at 240-243), ollama/handlers.go (lines 243-252), and openai/handlers.go (the equivalent short-circuit branch).
- **Complexity:** small
- **Observability impact:** Operator loses plugin.after slog record AND ChatTraceHook post_chain_out NDJSON line for every auth-rejected streaming request. /admin live tail shows plugin.before with no matching plugin.after. chat-trace.log gets pre_chain_in lines with no pre/post pair, breaking duration_ms correlation.

### ollama-reroute-double-posthook-fires — PII-encrypt re-route fires PostHook chain TWICE -- corrupts decrypt round-trip
- **Location:** `internal/adapter/ollama/handlers.go:270-320`
- **Category:** STREAM_CORRUPTION
- **Scenario:** A client POSTs /api/chat with stream:true. PreHook flips req.Stream=false (e.g. PII encrypt-mode round-trip). handleChat takes the re-route branch and calls eng.CollectFromRun(streamCtx, run, req) at line 270. CollectFromRun internally iterates e.cfg.PostHooks and calls h.After for every PostHook (collect.go:179-183). Control returns to the handler. After runSyntheticNDJSONFromResponse emits the synthetic NDJSON frames, the handler then calls eng.RunPostHooks(streamCtx, req, resp) again at line 313 -- running the SAME PostHook chain a second time on the SAME response. The exact PII decrypt PostHook that motivated this re-route path is therefore invoked twice; on the second pass it tries to decrypt already-plaintext content and either errors out (swallowed at WARN) or corrupts the rendered text. The same bug exists verbatim in handleGenerate at lines 508-545.
- **Current behavior:** PostHook chain runs once inside CollectFromRun (text is correctly decrypted / unredacted), the synthetic NDJSON is sent to the client with the correct content, then PostHooks run a SECOND time after the bytes are already on the wire. If a PostHook is idempotent (LoggingHook, ChatTraceHook) the duplicate produces duplicate log/trace records. If a PostHook is non-idempotent (PII decrypt -- the documented motivation for this branch per the T-5b comment) it operates on already-decrypted content and either fails silently (logged at WARN, swallowed because bytes are already on the wire) or produces undefined output. The bytes on the wire match the first PostHook pass so the client sees the intended content -- but log/trace state diverges and the in-memory resp.Message.Content is mutated again before any further consumer reads it.
- **Recommended fix:** Remove the eng.RunPostHooks call in the re-route branches: handlers.go lines 312-320 (handleChat) and lines 537-545 (handleGenerate). CollectFromRun already runs the PostHook chain, so the explicit call is redundant. Alternatively, factor the re-route branch into a helper that uses Run + manual aggregation (skipping CollectFromRun's PostHook invocation) and then calls RunPostHooks exactly once after the synthetic stream is emitted -- match the NDJSON-streaming branch's invariant of 'aggregator + RunPostHooks' for symmetry. Add a regression test that injects a hook counting After invocations and asserts count == 1 on the re-route path.
- **Complexity:** small
- **Observability impact:** LoggingHook.After and ChatTraceHook.After emit duplicate post_chain_out records keyed by request_id, so /admin live-tail and trace files double-count finished requests on every PII-encrypt re-route. Operators correlating request_id across surfaces will see two terminal records per request and misread it as a retry.

### pii-decrypt-regex-misses-underscore-entities — decryptTokenRe entity group [A-Za-z0-9]+ silently drops SIP_URI / MAC_ADDRESS encrypted tokens -- encrypted PII leaks through to client
- **Location:** `internal/plugin/pii/pii.go:339`
- **Category:** STREAM_CORRUPTION
- **Scenario:** Operator runs PII_REDACTION_MODE=encrypt (or PII_ENTITY_ACTIONS includes SIP_URI/MAC_ADDRESS -> encrypt). User sends a chat message containing 'sip:user@host'. Before encrypts it to '[PII:SIP_URI:<base64url>]' and forwards to kiro-cli. The LLM echoes that token in its assistant turn. After runs decryptTokenRe.ReplaceAllStringFunc on cp.Text, but the regex entity group '[A-Za-z0-9]+' does NOT contain '_', so the token never matches. The encrypted, opaque [PII:SIP_URI:...] is returned verbatim to the OpenAI/Anthropic/Ollama-shaped response surface. The client SDK sees gibberish where the SIP URI / MAC address should be -- a permanent fidelity hole on the entity classes that contain underscores in their canonical Name (SIP_URI, MAC_ADDRESS, and any future *_*-named recognizer).
- **Current behavior:** decryptTokenRe = regexp.MustCompile(`\[PII:([A-Za-z0-9]+):([A-Za-z0-9_-]+)\]`). The comment at line 337-338 says the entity class was widened from letters-only to letters+digits to fix IPv4/IPv6; underscores were not added even though SIP_URI and MAC_ADDRESS (registered in recognizers.go:382,406) contain underscores. EncryptValue (encrypt.go:75) embeds the entity name verbatim into the token between '[PII:' and ':', so an encrypted SIP_URI token is shaped '[PII:SIP_URI:<payload>]'. The After hook never recognizes it, never calls DecryptToken, never logs a pii.decrypt.failed warning -- the token flows out to the client surface unchanged.
- **Recommended fix:** Widen the entity capture group to include underscore: `\[PII:([A-Za-z0-9_]+):([A-Za-z0-9_-]+)\]`. Add a regression test that round-trips an encrypted SIP_URI / MAC_ADDRESS through Before -> fake kiro-cli echo -> After and asserts the original value is restored. Optionally derive the alternation from SourceAuditNames() so adding a new recognizer cannot drift the decrypt regex again.
- **Complexity:** small
- **Observability impact:** When the token fails to match, NO log line fires at all -- operator has zero signal that decrypt is silently broken for these entities. Even a 'no tokens matched' debug log would surface the regression.

### pool-respawn-ctx-cancel-shrinks-pool-permanently — Caller-disconnect during dead-slot respawn permanently shrinks the pool
- **Location:** `internal/pool/pool.go:505-510`
- **Category:** POOL_SESSION_EXHAUSTION
- **Scenario:** User has 4 warm slots. One subprocess dies (e.g., kiro-cli crash mid-stream -> exit_watcher flips slot.dead=true). The next request to land on that slot calls Pool.NewSession, slotAlive returns false, respawnSlot starts. While the new kiro-cli is still booting (multi-second startup), the HTTP client disconnects (LangFlow tab closed, Pi CLI Ctrl-C, browser refresh, laptop sleep dropping TCP) and the handler's ctx is cancelled. cfg.Factory.Spawn or newClient.Initialize returns context.Canceled. respawnSlot deliberately distinguishes ctx-cancellation from genuine spawn failure (WR-07, lines 262, 273) and returns an 'aborted' error WITHOUT marking it as a spawn error -- BUT the NewSession caller at line 506 unconditionally calls p.removeSlot(slot) on ANY respawn error. The slot is dropped from p.all forever. A pattern of disconnect-during-respawn (laptop reconnecting after sleep with multiple cached client tabs hitting dead slots) walks the pool down 4->3->2->1->0. Once at 0, HealthSummary.Healthy stays true only while Size==0; otherwise reports unhealthy, but no recovery path exists short of a process restart.
- **Current behavior:** respawnSlot returns 'aborted' error on ctx-cancel; NewSession then calls removeSlot which drops the slot from p.all. No retry, no re-queue, no 'put it back as still-dead so the next caller can try again' path. The slot is gone for the lifetime of the process.
- **Recommended fix:** In Pool.NewSession's respawn-error branch (pool.go:506-510), distinguish ctx-cancellation aborts from genuine spawn/initialize failures. On context.Canceled / context.DeadlineExceeded, return the slot to p.slots (still marked dead) so the next caller will retry the respawn -- do NOT call removeSlot. Only call removeSlot when respawnSlot recorded a genuine spawn error via recordSpawnErr. Concretely: check errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded); if true, do p.slots <- slot and return the wrapped ctx error; otherwise removeSlot as today. The exit_watcher will not re-fire because the underlying OLD client is already closed and Done() already fired; slot.dead remains true so the next acquirer re-enters respawnSlot.
- **Complexity:** small
- **Observability impact:** Operator currently sees /health/pool Alive count silently decreasing across the day with empty LastSpawnError (because recordSpawnErr is intentionally skipped on ctx-cancel). They have no signal that the pool is bleeding slots until traffic stalls. Even after the fix, emit a structured 'pool.respawn.deferred' debug event with slot label + ctx.Err so the operator can see why a slot remained dead.

## Medium (degraded behavior with at least some visibility)

### engine-hook-panic-no-recover — PreHook/PostHook panic crashes the request goroutine with no recover; mid-stream responses break the client SDK parser
- **Location:** `internal/engine/engine.go:170,259 + internal/engine/collect.go:180`
- **Category:** PANIC_HOT_PATH
- **Scenario:** Any PreHook (AuthHook, RequestIDHook, LoggingHook, ChatTraceHook, PII redact/encrypt) or PostHook with a latent nil deref / map nil-write / unguarded type assertion. Concrete example: an AuthHook that derefs a config field set to nil, a PII hook whose regex tagger walks a nil ContentPart slice, a ChatTraceHook that writes into a not-yet-initialised map. The hooks run synchronously inside the HTTP handler goroutine (engine.Run line 170, CollectFromRun line 180, RunPostHooks line 259). On panic, the handler goroutine unwinds; net/http's per-handler recover catches it (process survives), but for streaming surfaces that have already written response headers and partial SSE/NDJSON frames, the connection is torn down with no terminal `event: message_stop`, `data: [DONE]`, or `"done":true` line. The Anthropic/OpenAI/Ollama SDK on the other end then parses a truncated stream and surfaces a generic parse error to the operator.
- **Current behavior:** engine.Run iterates `for _, h := range e.cfg.PreHooks { resp, err := h.Before(...) }` with no recover. engine.CollectFromRun and engine.RunPostHooks do the same on the PostHook side. A panicking hook propagates up the call stack into the HTTP handler goroutine. net/http recovers at the handler boundary (process intact) but no slog event is emitted from the engine layer, no terminal stream frame is written, and the pool slot is released only by the AfterFunc watchdog when the request ctx eventually cancels.
- **Recommended fix:** Wrap each `h.Before` / `h.After` invocation in an inline closure with `defer func() { if r := recover(); r != nil { panicErr = fmt.Errorf("engine: hook panic: %v", r); e.cfg.Logger.Error("engine.hook.panic", "hook", fmt.Sprintf("%T", h), "err", r, "stack", string(debug.Stack())) } }()`. Treat the recovered panic as a normal hook error so Run/Collect's existing error-wrapping (`engine: prehook:` / `engine: posthook:`) handles teardown. Adapters already render that error class into a wire-shaped error envelope when the stream has not yet started; for already-streaming responses, the existing error path will at least stop writing rather than dying mid-frame.
- **Complexity:** small
- **Observability impact:** Operator cannot diagnose hook crashes today: net/http's recover logs to its own ErrorLog (often stderr) with no session_id / request_id / hook-type context. Adding `engine.hook.panic` slog event with hook type, session_id, request_id, and stack trace makes the failure mode visible and correlatable against the /hooks admin endpoint.

### acp-grandchildren-not-killed-on-close — kiro-cli grandchildren survive Client.Close -- killProcessGroup defined but never called
- **Location:** `internal/acp/client.go:1067-1075, internal/acp/pool_pgid_unix.go:46-48`
- **Category:** SUBPROCESS_LIFECYCLE
- **Scenario:** Pool slot respawn (any reason -- write error, frame-too-long teardown, ping failure, gateway shutdown). Close()'s step 1 cancel() triggers exec.CommandContext's signal-on-cancel -- which sends SIGKILL to the kiro-cli leader's PID only (not to -PID). Because applyPgidAttr put kiro-cli into its OWN pgrp (leader=child PID), the SIGKILL to that PID kills the leader but does NOT signal the rest of the process group -- any tool-invocation subprocesses kiro-cli spawned (mcp servers, file readers, language servers) are reparented to init and survive. On a laptop, repeated slot churn (e.g., the frame-too-long path firing on tool outputs, or the user restarting the gateway) accumulates orphaned kiro-cli grandchildren. killProcessGroup IS defined in pool_pgid_unix.go:46 for exactly this purpose but is never called from Close() or anywhere else.
- **Current behavior:** Close() calls c.cmd.Wait() (line 1068) after waiting for goroutines, relying on exec.CommandContext's default cancel behavior (signal to PID). On Unix that signals only the leader; grandchildren survive.
- **Recommended fix:** Two-part fix: (1) In New(), set cmd.Cancel = func() error { return killProcessGroup(cmd.Process.Pid, syscall.SIGKILL) } and cmd.WaitDelay = 2*time.Second so context cancellation propagates to the whole pgrp via -pid. (2) On Close() teardown, after cmd.Wait() returns, defensively call killProcessGroup(pid, syscall.SIGKILL) one more time (best-effort, ignore ESRCH) to catch any race where the leader exited but grandchildren still raced reparent. Both are no-ops on Windows via the existing build-tagged stub.
- **Complexity:** small
- **Observability impact:** No current visibility into grandchild process count. After fix, log Debug 'acp.subprocess.pgrp_killed' with the pid and result so operators can confirm cleanup happened. Optionally on /health/agents, surface 'acp.orphan.suspected=true' if cmd.Wait() returns before a brief sleep but pgrp signal-0 still returns success (catches the leak in real-time).

### acp-ping-loop-exits-silently-on-non-cancel-failure — pingLoop returns and stops health-checking on any ping error -- silent subprocess freeze
- **Location:** `internal/acp/client.go:439-460`
- **Category:** SUBPROCESS_LIFECYCLE
- **Scenario:** kiro-cli is alive but unresponsive (e.g., wedged after laptop sleep/wake, blocked on a hung tool call, or transient I/O stall). A ping fires; the 10-second timeout expires; c.Ping returns a wrapped context.DeadlineExceeded. The pingLoop's error branch (lines 452-453) logs Warn and returns. pingLoop is now gone -- no further heartbeats, no detection of subsequent freezes, no replacement trigger. The subprocess is now untrusted but no exit signal fires; clientCtx stays alive. The slot is wedged but the pool thinks it's healthy because there's no Done() signal.
- **Current behavior:** pingLoop treats any non-{Canceled, ErrClientClosed} ping error as a reason to exit the loop entirely. There's no retry, no escalation to Close, no Done() signal. The Client carries on as if everything is fine.
- **Recommended fix:** Either: (a) On unexpected ping failure, escalate by calling c.cancel() so clientCtx fires Done() and the pool's exit_watcher replaces the slot -- this matches the laptop-sleep recovery story; or (b) Keep retrying with backoff for 2-3 consecutive failures before escalating, so a transient stutter doesn't kill the slot. Option (a) is simpler and matches the 'crash-and-replace' deployment posture.
- **Complexity:** small
- **Observability impact:** Currently a wedged subprocess just sits there silently -- no slog event after the single Warn line. Add a slog event 'acp.ping.escalated_to_close' when pingLoop triggers the cancel so operators see in /admin tail when a heartbeat failure forced a slot replacement.

### acp-writer-loop-error-deadlocks-pending-callers — writerLoop returns on write error without cancelling clientCtx -- callers stuck in send()
- **Location:** `internal/acp/client.go:418-422`
- **Category:** GOROUTINE_LEAK
- **Scenario:** writerLoop encounters a framer.writeFrame error (subprocess stdin pipe closed prematurely -- e.g., kiro-cli crashed but stdout pipe still has a buffered last line that hasn't EOFd yet). writerLoop logs and returns at line 421-422 WITHOUT cancelling clientCtx and WITHOUT draining writeCh. Concurrent send() callers are now blocked: their select has `case c.writeCh <- data:` (no consumer remains, so blocks), `<-ctx.Done()` (per-request ctx still live), and `<-c.clientCtx.Done()` (NOT cancelled because writerLoop didn't cancel). They hang until: either readLoop also fails (subprocess crash -> EOF -> readLoop's defer cancels clientCtx -- happens eventually but only when stdout EOFs), or the per-request ctx times out. In the worst case (a kiro-cli that closes stdin but keeps stdout alive holding the pipe), pending callers block until per-request ctx fires.
- **Current behavior:** writerLoop on write error logs `acp: write error` and returns. No state mutation. No clientCtx cancel. send() callers see writeCh as full+unread until the readLoop independently detects subprocess death.
- **Recommended fix:** On write error, writerLoop must propagate the failure to clientCtx so send() callers exit promptly. Add `defer c.cancel()` at the top of writerLoop (parallels readLoop's pattern at client.go:382). This makes write failures fail-fast for in-flight callers and triggers exit_watcher.
- **Complexity:** small
- **Observability impact:** Today this hang is invisible until a request times out hundreds of seconds later, hard to diagnose. After fix, the existing `acp: write error` log plus the immediate ErrClientClosed return from send() gives operators a clear correlated signal. Consider adding a /health field for 'acp.last_writer_exit_reason' so the most recent reader/writer/ping failure is visible without log scraping.

### acp-late-update-cross-session-leak — Late session/update for a stale session pushes chunks onto next prompt's Stream
- **Location:** `internal/acp/client.go:967-999`
- **Category:** STREAM_CORRUPTION
- **Scenario:** 1) Prompt A runs to completion on session S1; awaitPromptResult's happy-path arm parses the prompt response, sets c.activeStream = nil (client.go:828-830), closes stream A. 2) The same Client is bound to the same kiro-cli subprocess. The pool releases the slot, and a new request arrives -- possibly on a DIFFERENT session S2 -- and Prompt B starts, setting c.activeStream = streamB. 3) kiro-cli emits a delayed session/update notification carrying sessionId=S1 (rare but possible if kiro-cli buffers updates after the prompt response, or on certain tool_call_update flushes). 4) handleNotification doesn't compare update.SessionID against activeStream's SessionID -- it just pushes onto whatever activeStream is non-nil. The S1 chunk lands in streamB, corrupting the S2 response visible to the SDK client (LangFlow/Pi CLI sees text from the old prompt mid-stream of the new one).
- **Current behavior:** translateUpdate parses sessionUpdateParams.SessionID but the result is discarded; only the body is used. handleNotification pushes to activeStream without verifying the update's session matches activeStream.SessionID (which is stored in the stream's FinalResult).
- **Recommended fix:** In handleNotification, after acquiring s := c.activeStream under streamMu, compare update.SessionID against s.result.SessionID (or pass sessionID into newStream and store on the Stream struct). On mismatch log Warn 'acp.stream.cross_session_drop' with both ids and DROP the chunk rather than push. This is a one-line defensive check that prevents silent corruption of a downstream SDK's SSE/NDJSON stream.
- **Complexity:** small
- **Observability impact:** Today this corruption is silent -- the wrong text just appears in the new stream. After the fix, the Warn event makes it visible in /admin tail with both session ids; operators can correlate with kiro-cli version/behavior.

### pool-hung-subprocess-not-replaceable-no-liveness-preemption — Hung-but-not-exited kiro-cli holds a slot indefinitely with no pool-level liveness preemption
- **Location:** `internal/pool/pool.go:550-610`
- **Category:** POOL_SESSION_EXHAUSTION
- **Scenario:** kiro-cli's pingLoop (acp/client.go:439-460) detects ping failures by RETURNING from the loop without calling c.cancel() -- so a ping timeout marks the loop as gone but does NOT fire Done(). If a kiro-cli subprocess deadlocks (stops responding to pings AND to the in-flight Prompt RPC, but doesn't exit), the pool's exit_watcher never fires (Done() never closes), the slot.dead flag never flips, and the slot remains 'checked out' by the in-flight Run for as long as the caller's ctx lives. With laptop sleep/wake or a kiro-cli internal hang under embedding-model contention, all 4 slots can park on hung subprocesses. The pool's only liveness primitive is the exit_watcher reading Done(); there is no Prompt-level timeout or slot-level deadline. A fresh NewSession call blocks on <-p.slots forever (subject only to caller ctx).
- **Current behavior:** Pool trusts acp.Client.Done() as the sole liveness signal. Hung-but-alive subprocesses are not detected. Slot acquisition starves silently. /health/pool shows Alive=4 because !slot.dead is true (Done() never fired).
- **Recommended fix:** Either (a) in acp.Client.pingLoop, call c.cancel() when ping fails (so Done() fires and the pool can respawn the slot) -- fix lives in internal/acp/client.go:447-454 but the pool's lifecycle assumption is what's broken; or (b) at the pool layer, surface a per-slot 'last activity' timestamp updated on every NewSession/Prompt/Result and treat slots whose checkout exceeds an idle deadline as candidates for forced respawn. Cheaper: fix the acp ping-loop to cancel on ping failure (one line change), and update pool.exit_watcher comment to reflect this is now the dead-detection trigger.
- **Complexity:** small
- **Observability impact:** No 'pool.slot.hung' or 'pool.slot.respawning' event today when a hung subprocess wedges a slot. Operator sees /health/pool Alive=4 Busy=4 forever and no LastSpawnError -- symptoms look like normal high traffic. Emit slog event with slot label + duration when respawn-on-hang fires.

### session-reap-vs-get-handoff-race — Reaper can reap an Entry between Get returning and the handler's entry.Mu.Lock -- handler then runs against a closed client
- **Location:** `internal/session/registry.go:170-216 + internal/session/reaper.go:72-112`
- **Category:** POOL_SESSION_EXHAUSTION
- **Scenario:** Entry for sid X has been idle for ~TTL. Request arrives. Get acquires r.mu, finds the alive entry, returns it (registry.go:195-198). Get does NOT update LastUsed (per D-11, only MarkUsed at response-complete advances LastUsed). The handler now has the *Entry but has not yet executed `entry.Mu.Lock()` (a few microseconds of scheduling gap). Meanwhile the reaper tick fires: snapshots entries under RLock, releases, TryLocks Entry.Mu -- succeeds because handler hasn't locked yet, sees LastUsed.Before(cutoff) true, calls Client.Cancel + Client.Close, deletes from map, sets Dead=true, unlocks Mu. The handler then acquires Mu (now released), calls eng.Run -> entry.Prompt -> Client.Prompt on the freshly-closed client -> error -> 500 to client. The request is lost despite Get having handed out an 'alive' entry.
- **Current behavior:** Get returns an alive Entry without holding entry.Mu and without updating LastUsed. The reaper has no signal that the entry was just handed out, only LastUsed.Before(cutoff). The TTL boundary is wall-clock vs LastUsed, not 'is this entry currently checked out'.
- **Recommended fix:** Either (a) have Get refresh entry.LastUsed under r.mu before returning the alive entry -- a single-line fix at registry.go:196 (`e.LastUsed = time.Now()` before unlock) -- which closes the boundary race because the reaper's snapshot will see a fresh timestamp; or (b) take entry.Mu.Lock inside Get before releasing r.mu and return a locked entry the caller must Unlock (more invasive, changes the handler contract). Option (a) is the minimal fix; D-11's 'never update at request start' invariant is about stream-continuity reaping (D-12 TryLock handles that), not about Get's handoff.
- **Complexity:** small
- **Observability impact:** No new observability needed; the fix is preventative. If kept, add slog.Warn in reaper at reaper.go:84 when the gap-between-Get-and-Lock is suspected (cannot be detected directly, so logging the reap itself is sufficient -- already present at line 105).

### config-loadargs-empty-allowed-ips-flag-silently-disables-allowlist — --allowed-ips="" (explicit empty string) silently overrides env ALLOWED_IPS to nil, disabling the IP allowlist
- **Location:** `internal/config/config.go:589,648-655`
- **Category:** AUTH_GAP
- **Scenario:** Operator deploys with ALLOWED_IPS=10.0.0.0/24,192.168.1.0/24 in their env file and a wrapper script that constructs argv. A misconfigured wrapper passes --allowed-ips="" (e.g., from an unset shell variable that expands to empty in the argv). flag.Visit fires because the flag was explicitly set; splitCommaTrim("") returns nil; cfg.AllowedIPs is overwritten to nil. The IPAllowlist middleware at internal/auth/ipallowlist.go:22 then takes the allow-all fast path. The gateway is now serving traffic with no IP restriction, and the auth-mode log at main.go:97 still reports ip_allowlist=false (no warning that this state is the result of a CLI override that nuked an env-configured list).
- **Current behavior:** LoadArgs at line 589 declares --allowed-ips with default "" (NOT strings.Join(cfg.AllowedIPs, ",")). At line 648-655, when the flag is visited and the value parses to a nil/empty prefix list, cfg.AllowedIPs is assigned nil. The case body has no guard against "flag was passed but value is empty" -- it accepts the nil silently. validateEnabledSurfaces has no symmetric requirement here because parseCIDRs([]string{}) returns (nil, nil) without error.
- **Recommended fix:** Either: (a) seed the flag default with strings.Join of cfg.AllowedIPs entries (mirroring the --enabled-surfaces pattern at line 585) so the flag default carries the env-resolved value and unset flag = env wins without a Visit-side overwrite; or (b) in the case "allowed-ips" branch at line 648, refuse explicit empty: if strings.TrimSpace(*allowedIPs) == "" { errs = append(errs, errors.New("allowed-ips: explicit empty value disables the allowlist; use --allowed-ips=0.0.0.0/0 to opt in to allow-all")); return }. (a) is simpler and matches the contract comment at lines 572-574 ("defaults are seeded from the already-resolved cfg"). The same issue affects --kiro-args via the empty default at l.578 -- strings.Join([], " ")="" but the existing default is cfg.KiroArgs which is fine; allowed-ips is the outlier.
- **Complexity:** small
- **Observability impact:** When cfg.AllowedIPs becomes nil via this path, the auth-mode log at main.go:97 reports ip_allowlist=false with no indication an env value was overridden. Adding a Warn-level log when LoadArgs detects flag-overrides-env-to-empty on a security-relevant knob would close the silent-divergence gap.

## Low (cosmetic observability gaps -- not blocking laptop launch)

### admin-sse-log-source-key-mismatch-silent-broken-tailer — SSE log source listed in LogPathOrder but missing from LogPaths silently constructs a broken tailer
- **Location:** `internal/admin/sse.go:88-95`
- **Category:** CONFIG_MISVALIDATION
- **Scenario:** Operator wires admin.Deps with LogPathOrder=["main","chat"] but LogPaths only contains {"main": "/path/to/log"} (key "chat" missing or empty). A client opens /admin/logs/stream?source=chat. The slices.Contains check on LogPathOrder passes (chat is listed), then path := h.deps.LogPaths["chat"] returns the zero value "". h.tailers.Get("chat", "") constructs and caches a Tailer with path="". When the first subscriber arrives, the run goroutine calls os.Open("") which fails forever; the tailer DEBUG-logs once per 250ms tick and never serves any data. The SSE client receives only keepalive pings, never log lines, and gets no error.
- **Current behavior:** SSE connection opens with 200 OK, sends empty backfill, emits only ping events. The operator sees a dead log stream with no diagnostic -- neither a 4xx nor a structured error event. Cached broken tailer persists for the life of the process (TailerRegistry never evicts).
- **Recommended fix:** In sseHandler after looking up path, validate path != "" and return 400 with a JSON envelope (similar to the unknown-source branch) before calling tailers.Get. Alternatively, validate LogPathOrder is subset of keys(LogPaths) at admin.Handler() construction time and slog.Warn (or panic) on the mismatch -- the comment at admin.go:64 says "Filling LogPaths without LogPathOrder means the SSE handler cannot resolve sources" but the inverse (LogPathOrder entry with empty LogPaths value) is not guarded.
- **Complexity:** small
- **Observability impact:** Operator opening the admin SSE tail on a misconfigured source sees no log lines and no error -- symptom is indistinguishable from a quiet log file. Adding a startup slog.Warn on key mismatch or a 400 at request time gives an immediate diagnostic.

### openai-empty-messages-after-decode-not-rejected — All-empty-content messages produce empty canonical request, engine receives messages-less req
- **Location:** `internal/adapter/openai/handlers.go:81-84`
- **Category:** CONFIG_MISVALIDATION
- **Scenario:** Client sends `{"messages": [{"role":"user","content":[]}]}` or `{"messages":[{"role":"user","content":[{"type":"image","text":""}]}]}`. The handler's `len(wire.Messages) == 0` check passes (one entry exists). decodeMessageContent returns "" for the empty/non-text parts. wireToChatRequest's `if text == "" { continue }` skip drops every entry. The canonical request has zero canonical Messages and is sent to the engine, which may hang waiting for a non-existent prompt or produce an empty response.
- **Current behavior:** Engine receives ChatRequest with empty Messages slice. Behavior depends on kiro-cli's reaction to an empty prompt -- likely a long-running no-op that ties up a pool slot until idle-timeout fires.
- **Recommended fix:** After wireToChatRequest, check `len(req.Messages) == 0 && req.System == ""` and return 400 invalid_request_error "messages decoded to empty content". Mirrors the wire-level check just above.
- **Complexity:** small
- **Observability impact:** No structured log fires on this path. Operator only sees idle-timeout or successful empty response. Adding a 400 + slog line at the handler would expose pathological clients.

### pii-hashtag-empty-key-warns-but-still-emits — hashTag empty-key path emits UnkeyedHashSentinel without counter -- PII shape leaks even though key is missing
- **Location:** `internal/plugin/pii/modes.go:68-75`
- **Category:** CONFIG_MISVALIDATION
- **Scenario:** Operator sets PII_REDACTION_MODE=hash but boot validation regresses (or test path constructs hook with empty HashKey). Every hash-mode match emits '[ENTITY:h-UNKEYED]'. Operator's log filters look for entity hash patterns and see uniform UNKEYED tags. Worse: emitting a per-call slog.Default().Warn on every match (could be hundreds per request) spams the log stream and could buffer-back the slog handler under heavy traffic.
- **Current behavior:** modes.go:70 emits slog.Default().Warn for every empty-key invocation, even if the same request has 500 hash applies. No rate-limit / once-per-request guard.
- **Recommended fix:** Either fail the request at Before entry when Mode=='hash' but len(HashKey)==0 (preferred -- fail closed), or gate the warn behind a sync.Once so the log fires once per process. Boot validation should already prevent this state; this is defense in depth.
- **Complexity:** small
- **Observability impact:** Log spam per match instead of a single boot-level error. Operator may miss the WARN buried in per-request stream.

### engine-rangechunks-ctx-exit-caller-must-cancel — RangeChunksWithIdleTimeout godoc does not warn callers that ctx-exit leaves the ACP producer running
- **Location:** `internal/engine/idle.go:69-70,98-99`
- **Category:** CONTEXT_CANCELLATION
- **Scenario:** A new caller adds a chunk-loop site that calls RangeChunksWithIdleTimeout with a ctx whose cancellation is NOT also wired to an engine.AfterFunc watchdog (e.g., a derived ctx via context.WithTimeout in a future surface adapter). On ctx cancellation the helper returns, the caller returns, but the engine never fires ACP.Cancel and the underlying acp.Stream.readLoop continues producing into the 64-slot buffer, then blocks indefinitely on client-lifetime ctx. Today's only callers (engine.CollectFromRun and the inline replica in anthropic/collect.go) happen to use the same ctx the engine.AfterFunc watchdog is registered on, so the pattern works; but the helper does not document that contract.
- **Current behavior:** Godoc explains the ctx-exit error wrapping and timer semantics, but does not state that callers own the ACP.Cancel call when ctx cancellation is not paired with engine.Run's AfterFunc watchdog.
- **Recommended fix:** Add a NOTE to RangeChunksWithIdleTimeout godoc stating: `Callers MUST ensure ctx cancellation also triggers ACP.Cancel(sessionID) either via engine.Run's AfterFunc watchdog (default for engine.CollectFromRun callers) or by explicitly invoking it on error return. The helper does not own session lifetime.` Alternatively, extend the helper to take an `onExit func()` callback the engine layer wires to its session Cancel.
- **Complexity:** small

### plugin-chain-empty-request-id-collides-starttimes — LoggingHook startTimes key collision when request_id is empty
- **Location:** `internal/plugin/logging.go:149,166,194`
- **Category:** DATA_RACE
- **Scenario:** Two concurrent requests enter engine.Run without an adapter-stamped WithRequestID (test invocation, future internal caller forgets to stamp, or RequestIDHook is filtered out of ENABLED_HOOKS). RequestIDFromContext returns "" for both. LoggingHook.Before stores at key "" twice; the second overwrites the first. When After runs for request 1, LoadAndDelete on "" returns request 2's stamp -- duration_ms is wrong. ChatTraceHook.Before has a defensive `if rid == "" { rid = NewRequestID() }` (trace.go:201-203) that mitigates the trace side; LoggingHook has no such fallback.
- **Current behavior:** duration_ms emitted by LoggingHook.After is wrong (mixed across concurrent requests). Not a leak, not a panic, but observability is corrupted. In production the adapter always stamps via stampPluginCtx so RequestIDFromContext returns a ULID -- this manifests only in misconfigured/test scenarios.
- **Recommended fix:** In LoggingHook.Before, mirror ChatTraceHook's defensive fallback: if rid is empty, generate a local NewRequestID() and use it for both the slog record and the startTimes key. After uses the same locally-derived rid (would need a sync.Map entry mapping incoming-ctx-rid to derived-rid, OR simpler: just don't store when rid=="", emit a Warn slog line so operator knows the chain ran without a stamped id).
- **Complexity:** small
- **Observability impact:** plugin.after duration_ms is wrong when concurrent unstamped requests race -- wrong correlation in operator slog output.

### anthropic-flusher-assertion-fail-swallowed — runSSEEmitter Flusher assertion failure returns nil response -- handler silently drops the request with no error to client and no PostHook observability
- **Location:** `internal/adapter/anthropic/handlers.go:265-291`
- **Category:** ERROR_SWALLOWED
- **Scenario:** If the http.ResponseWriter does not implement http.Flusher (a wrapped middleware writer, or a test harness writer), runSSEEmitter returns (nil, error) BEFORE any headers are written. The handler logs at debug only and falls through. resp is nil so the PostHook block at line 286-291 is skipped; the client receives no response body.
- **Current behavior:** Client sees an empty 200 (chi default) or no response. No JSON error envelope sent. No PostHooks fired. The comment at handlers.go:267-275 incorrectly claims headers were already written -- sse.go:584-590 returns before WriteHeader, so a JSON 500 envelope is still possible at this point.
- **Recommended fix:** Distinguish the Flusher-assertion error from in-flight emitter errors. Either (a) return a typed sentinel from runSSEEmitter when the Flusher cast fails so handlers.go can call writeError(500, errAPI, 'internal error'), or (b) move the Flusher assertion above the streaming branch in handlers.go so the failure mode is handled with a normal JSON 500.
- **Complexity:** small
- **Observability impact:** No slog event distinguishes 'flusher missing' from 'client disconnect mid-stream' -- operator sees the same debug-level 'sse emitter terminated' marker for two very different failure modes.

### acp-permission-response-direct-write-races-shutdown — Direct framer write for permission response can race Close-time pipe close
- **Location:** `internal/acp/client.go:963-965`
- **Category:** ERROR_SWALLOWED
- **Scenario:** handleNotification on the readLoop goroutine writes a permission response directly to framer (bypassing writeCh per WR-02 comment). If Close() races: step 3a closes stdin -> the direct writeFrame fails with `file already closed` -> the error is logged as Warn 'acp: permission response write failed' and dropped. The bypass means the response is dropped silently; kiro-cli may then deadlock waiting for that response (D-20 explicitly warns about this deadlock). Since we're shutting down, deadlock-on-shutdown doesn't matter long-term, BUT: the more concerning case is when the direct write fails for a non-shutdown reason (transient pipe stall) -- the dropped response still risks the kiro-cli deadlock during normal operation.
- **Current behavior:** Permission response write failure is logged Warn and dropped. No retry, no escalation. Comment at client.go:951-962 justifies the bypass for backpressure reasons but doesn't address failure semantics.
- **Recommended fix:** On write failure of a permission response specifically, escalate -- call c.cancel() so the slot replaces. The D-20 contract is that kiro-cli WILL deadlock without this response; if we can't send it, the slot is unusable regardless. Alternatively, retry once via writeCh as a fallback. Either path is better than the current silent drop.
- **Complexity:** small
- **Observability impact:** Today the Warn is the only signal that a permission response was lost -- operators may not connect that to a subsequent slot freeze. After fix, the escalated cancel makes the slot replacement visible via exit_watcher events.

### pii-walk-maxdepth-silent-passthrough — WalkStrings at depth>64 silently returns subtree unredacted with no log -- PII at depth 65+ flows through ToolUse.Input
- **Location:** `internal/plugin/pii/walk.go:53-55`
- **Category:** ERROR_SWALLOWED
- **Scenario:** An adversarial or buggy tool-server returns a tool_use.Input map nested >64 deep. The redact pipeline descends to depth 64, then the recursive call returns `v` (the entire deep subtree) verbatim. Any PII inside that subtree -- emails, phone numbers, SSNs -- flows untouched to the LLM provider, and no log line is emitted to signal the truncation. Counter is not bumped, Summary.Add is not called, so LoggingHook reports `redacted={}` for that request and the operator believes the content was clean.
- **Current behavior:** walkStrings returns v unchanged when depth > maxDepth (64). No slog event is emitted at the truncation point.
- **Recommended fix:** When depth > maxDepth, log a single WARN per request (`pii.walk.depth_truncated` with depth + a content-kind hint) so the operator sees the silent passthrough. Optionally bump a Summary counter for an artificial 'TRUNCATED' entity so /health/hooks surfaces the operational signal. Consider lowering maxDepth -- 64 is far deeper than any realistic tool schema -- and returning a redacted sentinel for the over-depth subtree instead of the verbatim input.
- **Complexity:** small
- **Observability impact:** No log fires on depth truncation. Operators cannot detect that PII redaction was bypassed on adversarial inputs. Summary.Counts() reports zero redactions for the request even though the content is unredacted.

### pii-ner-empty-entity-text-zero-length-span — NER Detect creates zero-length span when prose returns empty Entity.Text -- pollutes Summary counter
- **Location:** `internal/plugin/pii/ner.go:96-117`
- **Category:** ERROR_SWALLOWED
- **Scenario:** prose.Document.Entities() returns an entity with e.Text == "" (possible when tokenizer normalizes an entity to empty after stripping punctuation). strings.Index(text[cursor:], "") returns 0, so a span with Start==End is emitted. mergeSpansGreedy accepts it (overlaps is false-positive only when Start < End on at least one side). Counter is incremented for an empty-string canonical-form key. ApplyMode replaces an empty span -- for replace mode it emits '[PERSON_1]' at position cursor, inserting noise into the user's text.
- **Current behavior:** No empty-text guard in the entity loop. Empty-text entities pass through as zero-length spans and contaminate counter/Summary state.
- **Recommended fix:** Add `if e.Text == "" { continue }` at the top of the entity loop in ner.go:84.
- **Complexity:** small
- **Observability impact:** None -- pollution shows up as spurious '[PERSON_N]' insertions but no log line ties it to the cause.

### plugin-chain-run-error-leaks-starttimes-entries — LoggingHook.startTimes and ChatTraceHook.startTimes leak entries when engine.Run fails after PreHooks succeed
- **Location:** `internal/plugin/logging.go:166, internal/plugin/trace.go:222, internal/engine/engine.go:188-207`
- **Category:** GOROUTINE_LEAK
- **Scenario:** Pre hooks all succeed (RequestIDHook -> AuthHook -> JSONFormatSteeringHook -> PIIRedactionHook -> LoggingHook.Before STORES an entry in startTimes keyed by request_id; ChatTraceHook.Before similarly when prepended). engine.Run continues to NewSession/SetModel/Prompt. Any of those calls fails (pool slot unavailable, kiro-cli subprocess died, model not found, ACP write failed, laptop sleep/wake stale wire). engine.Run returns an error. The HTTP handler logs it and writes a 5xx envelope. RunPostHooks/Collect is NEVER called. LoggingHook.After and ChatTraceHook.After never run, so LoadAndDelete is never called for this request_id.
- **Current behavior:** Each failed-after-Pre request leaks one entry in both LoggingHook.startTimes and (when ChatTrace is enabled) ChatTraceHook.startTimes. Entries are keyed by unique ULIDs, never collide, never reclaimed. Slow but unbounded growth across a long laptop-resident process lifetime -- every transient ACP failure, every pool-exhaustion event, every sleep/wake-induced wedge produces an orphan. logging.go:30-32 doc comment explicitly claims LoadAndDelete prevents map growth -- that promise holds only when After is reached.
- **Recommended fix:** On engine.Run error paths (after PreHooks succeeded), have the HTTP handler call eng.RunPostHooks(ctx, req, nil) before returning the error response. Both LoggingHook.After and ChatTraceHook.After already nil-guard resp and will LoadAndDelete their startTimes entry, emitting a final record with stop_reason=0. Alternative: add a Cleanup(rid) method on hooks-with-state and have engine.Run call it on error in a deferred best-effort sweep.
- **Complexity:** small
- **Observability impact:** Slow memory growth invisible to operator. No /admin metric exposes sync.Map size. The leak does NOT trip a slog warning until the process is OOM-killed.

### openai-watchdog-stop-leaked-on-error-paths — StopWatchdog never called on Result()-error, ctx-done, idle-timeout, or applyChunk-error paths
- **Location:** `internal/adapter/openai/sse.go:438-481,529-538`
- **Category:** GOROUTINE_LEAK
- **Scenario:** Streaming SSE request takes any non-happy-path exit: (1) client disconnect (`<-ctx.Done()`), (2) stream idle timeout, (3) `applyChunk` returns an error (e.g., w.Write failure on disconnect), (4) `run.Stream().Result()` returns an error after the chunks channel closed. Only `finalizeSSE`'s clean-completion path calls `run.StopWatchdog()` (sse.go:543). On every other exit path the watchdog AfterFunc goroutine remains armed and the stop function is never invoked.
- **Current behavior:** The handler defers `cancelFn()` (handlers.go:137) which cancels streamCtx, so the AfterFunc watchdog observing ctx.Done() will fire its session/cancel and exit naturally. This is functionally correct on the laptop deployment -- no leaked goroutine -- but the watchdog will fire a session/cancel ACP message AFTER the client disconnect or idle-timeout, racing with engine teardown. The CONTEXT.md D-06 design intent was for stop() to suppress that spurious cancel on natural-end paths; it now only suppresses on the truly clean completion path.
- **Recommended fix:** Call `run.StopWatchdog()` before returning from the ctx.Done, idle-timeout, applyChunk-error, and Result()-error branches in runSSEEmitter / finalizeSSE. On Result-error the watchdog already had its trigger so stop() returning false is expected and harmless (Cancel is idempotent per the local doc). The current asymmetry means a normal stream-idle exit fires a spurious session/cancel that may show up in audit logs.
- **Complexity:** small
- **Observability impact:** Spurious `session/cancel` ACP events emitted on idle-timeout and client-disconnect paths. Confuses post-mortem audit when correlating disconnect vs cancel intent.

### server-recoverer-blind-to-handler-goroutines — chi.Recoverer does not cover goroutines spawned inside streaming handlers
- **Location:** `internal/server/server.go:166,217`
- **Category:** PANIC_HOT_PATH
- **Scenario:** chi/middleware.Recoverer only catches panics on the same goroutine that runs next.ServeHTTP. If a streaming handler (anthropic/openai/ollama) ever spawns a worker goroutine (e.g., a background drainer for a Cancel path, a tee for /admin SSE tap, or an engine plug-in that fans out work), a panic in that goroutine bypasses Recoverer and crashes the whole process. Per the deployment posture (single-user laptop, no supervisor), a process crash means manual restart. Current adapter/engine code in scope does not spawn such goroutines, but this is a structural gap to flag for future hook/admin work -- particularly the admin SSE tail handler, which is a likely candidate.
- **Current behavior:** middleware.Recoverer is the only panic guard. Any goroutine started inside a handler runs uninstrumented and a panic there terminates the process.
- **Recommended fix:** Document the constraint as a comment near the Use(middleware.Recoverer) line: 'Recoverer covers only the request goroutine -- any go func() spawned inside a handler MUST defer-recover locally or use a withRecover() helper.' Optionally add a small package-local panic-safe wrapper (recoverAndLog(reqLogger, fn)) for future handler-spawned goroutines to call. No immediate code defect in scope, but the structural reliance on every future handler-author to remember the rule is fragile.
- **Complexity:** small
- **Observability impact:** A handler-goroutine panic today produces a stderr dump from the Go runtime and process exit -- no structured slog event with request_id / surface / session_id. A withRecover() helper would route the panic through the per-request logger so post-mortem is grep-able.

### pool-newsession-blocks-on-closed-pool — NewSession blocks on p.slots receive after Pool.Close, only unblocking via caller ctx
- **Location:** `internal/pool/pool.go:491-497`
- **Category:** SHUTDOWN_GAP
- **Scenario:** Operator hits Ctrl-C on the gateway. server.Shutdown waits for in-flight handlers. A new request lands during the shutdown window after Pool.Close has started but before all handlers drain. NewSession's select has only two cases: <-p.slots and <-ctx.Done(). It does NOT select on <-p.closing. If all slots are checked out by other in-flight requests at the moment Close runs (worst case after a burst), this NewSession blocks until its caller ctx fires. With server.Shutdown's default behavior (or any handler that uses a long ctx without a deadline), the new request hangs until graceful-shutdown-timeout expires, prolonging shutdown and surfacing as 'why does the gateway take 30s to exit?' on the operator's laptop. Worse, if a slot does get released and received by the post-close NewSession, slotAlive returns true (exit_watcher saw <-p.closing and exited without flipping dead), respawnSlot is not entered, and slot.Client.NewSession then errors with ErrClientClosed -- wasted round trip during shutdown.
- **Current behavior:** Post-Close NewSession races between caller-ctx and a slot-release that returns a closed client. Either path produces an error, but the path through 'receive a stale slot, call NewSession on closed client' is needless work during shutdown.
- **Recommended fix:** Add a third select case in Pool.NewSession (pool.go:492): case <-p.closing: return "", errors.New("pool: closed"). Same treatment in the dead-slot respawn branch's spawn error so a Close mid-respawn returns 'pool: closed' rather than 'aborted'. This also lets the gateway distinguish shutdown-induced session failures from real failures in the slog stream.
- **Complexity:** small
- **Observability impact:** Without the fix, operator log shows 'pool: new-session: acp: client closed' during shutdown -- looks like a bug. With the fix, the operator sees a clean 'pool: closed' error which they can filter out of incident triage.

### server-http-no-idle-readtimeout — http.Server has only ReadHeaderTimeout -- no IdleTimeout or ReadTimeout
- **Location:** `internal/server/server.go:338-342`
- **Category:** SHUTDOWN_GAP
- **Scenario:** A laptop wake-from-sleep, flaky Wi-Fi disconnect, or a buggy client (LangFlow tab left open, Pi CLI killed at OS layer) can leave TCP connections half-open. The kernel will eventually clean them, but with HTTP/1.1 keep-alive there is no application-side cap on how long an idle established connection survives. Over hours of laptop sleep/wake cycles the gateway can accrue dead keep-alive sockets that count against the process FD limit without ever being closed by the gateway itself.
- **Current behavior:** srv only sets ReadHeaderTimeout: 10s. WriteTimeout is intentionally omitted (would break streaming) -- but IdleTimeout, which controls keep-alive idle connection lifetime, is also omitted. Idle keep-alive connections live forever from the server's perspective until the client closes or the kernel TCP-keepalive cleans up.
- **Recommended fix:** Add IdleTimeout: 120 * time.Second (or similar) to the &http.Server{} literal in Run(). This caps how long a keep-alive connection can sit idle on the server side without affecting in-flight stream writes (which use the handler-driven cancellation path via ctx). Do NOT add WriteTimeout -- that one would truncate legitimate long-running SSE/NDJSON streams.
- **Complexity:** small

### anthropic-rerouted-stream-writes-json-on-short-circuit — Re-routed (PII encrypt) stream:true path writes JSON 401 envelope when the SDK already expects text/event-stream
- **Location:** `internal/adapter/anthropic/handlers.go:240-243`
- **Category:** STREAM_CORRUPTION
- **Scenario:** Client sends stream:true. A PreHook flips req.Stream=false (PII encrypt mode). eng.CollectFromRun returns a response with StopReason == canonical.StopError (e.g., from a second PreHook firing post-encrypt, or future short-circuit hook composition). Handler writes JSON 401 envelope -- but the client's HTTP layer is still in 'expecting SSE' mode (the wire request had stream:true).
- **Current behavior:** writeError sets Content-Type: application/json + status 401 + JSON body. The Anthropic SDK's MessageStream parser is the parser the client wired up because wire.Stream was true; depending on the SDK version it may either degrade to a parse error or surface a confusing 'request ended without sending any chunks' regression -- exactly the v1.8.3 condition the synthetic SSE branch (line 251) was designed to avoid.
- **Recommended fix:** On the re-route branch, when a short-circuit is detected post-CollectFromRun, emit the short-circuit content as a synthetic SSE 200 with an `event: error` frame instead of a JSON 401. Or alternatively: emit the short-circuit message as one text content_block followed by message_delta/message_stop, matching how the streaming emitter would have rendered it. Status code 200 + SSE error frame mirrors how the streaming branch handles mid-stream errors (sse.go:781).
- **Complexity:** medium
- **Observability impact:** No slog event distinguishes 'short-circuit on re-routed stream' from 'short-circuit on non-streaming JSON request' -- both render via writeError(401) so the wire-shape mismatch is invisible until an SDK error surfaces in client logs.

### admin-static-handler-accepts-any-http-method — /admin/static/* registered via r.Handle accepts arbitrary HTTP methods
- **Location:** `internal/admin/admin.go:185-191`
- **Category:** SURFACE_COMPAT_DRIFT
- **Scenario:** r.Handle("/static/*", ...) registers a handler for all HTTP methods (GET/POST/PUT/DELETE/PATCH/etc). A client sending POST /admin/static/css/admin.css is served the CSS file with 200 OK rather than 405 Method Not Allowed. Not a security issue (static assets are public) but diverges from typical static-asset surface semantics and complicates request-shape audits.
- **Current behavior:** Non-GET methods to /admin/static/* return 200 OK with the file body. http.ServeFileFS does not gate by method.
- **Recommended fix:** Change r.Handle to r.Get to restrict to GET only. The other admin routes already use r.Get; this is the lone outlier. One-line change.
- **Complexity:** small

### ollama-ndjson-idle-timeout-terminal-frame-missing-fields — Idle-timeout terminal NDJSON line omits required model/created_at fields -- Ollama SDKs may reject
- **Location:** `internal/adapter/ollama/ndjson.go:420-438`
- **Category:** SURFACE_COMPAT_DRIFT
- **Scenario:** Stream-idle watchdog fires (no chunk for STREAM_IDLE_TIMEOUT_SEC). runNDJSONEmitter writes a hand-rolled terminal line: `{"error":"stream idle timeout","done":true}\n`. Real Ollama streaming clients (ollama-js, langchain Ollama integration) parse every NDJSON frame as a chat-response envelope expecting at minimum `model`, `created_at`, and `message`/`response` fields. A frame missing those keys may be silently dropped, throw a parse error, or surface as 'undefined model' downstream. LangFlow's Ollama loader specifically reads `created_at` and `model` from the terminal frame for its trace UI.
- **Current behavior:** Terminal idle-timeout frame is `{"error":"stream idle timeout","done":true}` -- no model, no created_at, no message object. The shape diverges from every other terminal frame in the package (chatResponseToWire / generateResponseToWire produce fully-populated envelopes). Clients that strictly type the NDJSON stream against the documented Ollama shape will see a missing-key error or, worse, silently render the previous done:false frame as the final.
- **Recommended fix:** Build the terminal frame via chatResponseToWire/generateResponseToWire (passing the in-flight req model and start time) and stamp DoneReason='error' with a separate Error field, OR add the missing fields inline: `{"model":"...","created_at":"...","message":{"role":"assistant","content":""},"done":true,"done_reason":"error","error":"stream idle timeout"}`. Mirror the isChat branch since /api/generate would need `response` instead of `message`. Also drop the no-arg fmt.Sprintf wrapper -- use a plain string literal.
- **Complexity:** small
- **Observability impact:** Operators reading the access log + trace correlations see a 200 status (headers already flushed) with a malformed final frame, hard to distinguish from network truncation. The slog warn event 'stream.idle_timeout' fires correctly, so server-side observability is intact, but client-side reporting may be misleading.

### pii-encrypt-failure-leaks-plaintext — ApplyMode encrypt-on-error returns plaintext value -- privacy boundary fails OPEN instead of CLOSED
- **Location:** `internal/plugin/pii/modes.go:156-165`
- **Category:** SURFACE_COMPAT_DRIFT
- **Scenario:** Operator enables encrypt mode. A subtle EncryptKey corruption (truncation, wrong length, byte flip) or transient crypto failure occurs on a single request. EncryptValue returns an error (aes.NewCipher rejects, cipher.NewGCM rejects, or rand.Read fails). ApplyMode logs a warning then returns `value` -- the raw, original PII plaintext. That plaintext flows into req.System / cp.Text / cp.ToolUse.Input and is dispatched to the kiro-cli LLM. The whole point of encrypt mode is 'LLM provider does not see plaintext'; this failure path quietly violates that contract on every request that trips it.
- **Current behavior:** modes.go:158-164: on EncryptValue error, logs 'pii.ApplyMode: encrypt failed, leaving plaintext' and returns `value`. Comment says 'visible failure is preferable to emitting a broken token that the Post hook cannot decrypt' -- but the alternative chosen (plaintext) is the worst-case outcome for the encrypt-mode threat model (LLM provider sees PII).
- **Recommended fix:** On encrypt failure, return a replacement token (e.g., ApplyMode('replace', entity, value, counter, ...)) so the PII is at least redacted. Plaintext should NEVER flow when the operator opted into encrypt mode. Bonus: surface the error up so Before can choose to fail the request (mode=block-style) when encrypt is the contract.
- **Complexity:** small
- **Observability impact:** A WARN is logged ('pii.ApplyMode: encrypt failed') but the operator has no way to know how many requests have leaked plaintext. A metric counter (pii.encrypt.failures.total) tied to /health/hooks would let operators detect a key-corruption regression.

### engine-collect-idle-timeout-no-explicit-cancel — CollectFromRun idle-timeout and Result-error returns rely on AfterFunc watchdog firing via request ctx; pool slot held for handler-return window
- **Location:** `internal/engine/collect.go:150-164`
- **Category:** POOL_SESSION_EXHAUSTION
- **Scenario:** Kiro stalls mid-response (no chunks for StreamIdleTimeout). RangeChunksWithIdleTimeout returns wrapped ErrStreamIdleTimeout. CollectFromRun returns immediately without calling `e.cfg.ACP.Cancel(run.sessionID)` and without calling `run.StopWatchdog()`. The request ctx is still alive (no client disconnect, no deadline). The underlying acp.Stream.readLoop keeps trying to push to the 64-slot buffered chunks channel; once full it blocks on `<-c.clientCtx.Done()`. The AfterFunc watchdog only fires when the request ctx terminates. So the kiro session is held until the HTTP handler returns and net/http cancels the request ctx, which for the surfaces that immediately render a 504 is fast, but for any caller that does additional cleanup work before returning the window widens. Same path applies on the `run.stream.Result()` error branch at line 161.
- **Current behavior:** Idle-timeout exit returns `nil, fmt.Errorf("engine: collect: %w", rangeErr)` at line 159 with no Cancel and no watchdog stop. Result-error exit returns `nil, fmt.Errorf("engine: collect result: %w", rerr)` at line 163 with the same omission. Pool slot release happens only when AfterFunc later fires on request ctx cancel. With POOL_SIZE=4 and a multi-tab laptop user, two stalled requests can pin 2/4 slots until the user closes their browser tab.
- **Recommended fix:** On the idle-timeout branch (after the Warn log) and on the Result-error branch, call `e.cfg.ACP.Cancel(run.sessionID)` explicitly before returning. Cancel is idempotent (RESEARCH.md Pitfall 4) so the still-armed AfterFunc watchdog firing later is harmless. This decouples pool slot release from handler return timing.
- **Complexity:** small
- **Observability impact:** Today the `stream.idle_timeout` warn log fires but there is no `engine.session.cancel` follow-up event, so an operator watching the admin UI cannot tell whether the slot has actually been released. Adding an explicit Cancel pairs the timeout log with the slot-release signal.

## Already Hardened (defensive coding worth noting)

### engine-acp-driver
- pickCwd nil-safety property-tested; Windows file:// URI defect handled in extractFileURIs
- engine.Run watchdog: AfterFunc-based teardown with idempotent Cancel and nil-safe StopWatchdog
- PreHook short-circuit reuses emptyStream + closedChunkChan to avoid alloc churn
- buildBlocks tools-marshal has defensive header-only fallback on json.Marshal failure
- CoerceToolCall short-circuits cleanly on nil req/resp, empty tools, pre-populated ToolCalls
- RangeChunksWithIdleTimeout drain-safe timer.Stop/Reset prevents timer-channel leak
- acpStreamShim nil-FinalResult guard returns (nil, err) when underlying stream has no result
- assembleChatResponse preserves Phase 2 Ollama len(Content)==1 invariant via positional text part
- RunPostHooks documented idempotency contract + per-surface double-fire guard tests on all three adapters
- longestCommonParent handles empty/single/divergent-root paths without panic
- buildBlocks defensive nil-req branch returns single empty text block

### acp-protocol
- Dispatcher pending-map atomic delete-then-send prevents send-on-closed for response correlation
- drainAll uses non-blocking select to send sentinel, avoiding deadlock against buffered-1 channel
- rpcFrame.ID is json.RawMessage (CR-01) so string-ids on permission requests echo back verbatim (D-20 guard)
- Client.Close is sync.Once-guarded with documented shutdown order
- isExpectedTeardownExit filters self-induced SIGKILL exit errors from happy-path Close
- Stream.close uses sync.Once so double-close from readLoop + awaitPromptResult is safe
- send() observes BOTH per-call ctx and clientCtx so Close after-accept produces ErrClientClosed (not strand)
- Framer scanner.Bytes() is copied before return -- buffer reuse cannot corrupt concurrent parsing
- applyPgidAttr places kiro-cli in its own process group (cross-platform Windows stub)
- translateUpdate returns ok=false on inner-payload malformed JSON (WR-05)
- parseStopReason maps unknown wire stop-reasons to StopUnknown (D-02 forward-compat)
- writerLoop drains buffered writeCh on ctx.Done before returning
- awaitPromptResult goroutine registered with c.wg BEFORE `go` so Close's wg.Wait observes it

### pool-worker
- Warmup fail-fast on partial pool spawn: closeAll teardown + wrapped error
- Slot release exactly-once via map-delete-first + sync.Once across Result-drained / ctx-cancelled / engine-Cancel terminal paths
- respawnSlot ordering: close OLD then spawn NEW then swap then spawn fresh watcher
- WR-01 done-channel capture at spawn site so future slot.Client swap cannot misbind watcher
- Pool.Close idempotency via closeOnce + closeAll's p.all=nil for second-call no-op
- close(p.closing) BEFORE closeAll so exit-watchers see close-signal first (goleak-clean)
- NewSession / Prompt release slot on error via releaseSlotForSession
- Stats and HealthSummary use !s.dead consistently across surfaces
- Detail snapshot copies sid to avoid loop-variable aliasing
- Pool.Cancel captures slot.Client under p.mu before invoking Cancel
- PoolClient interface seam + compile-time assertion synchronizes test injection with production
- respawnSlot distinguishes ctx-cancellation from genuine spawn failure for clean telemetry
- Pool config Size floor of 1 prevents zero-capacity slots channel deadlock

### session-registry
- Map-delete-first in Delete prevents blocking on slow Client.Close while holding r.mu
- Idempotent closeReady via sync.Once (CR-02 fix) prevents double-close ready channel panic
- Reaper snapshot-then-iterate with TryLock prevents reverse-lock-order deadlock (D-12)
- Reaper writes Entry.Dead UNDER r.mu sharing mutex with readers (CR-04 fix)
- Bounded reaper shutdown via close(closing) + wg.Wait
- createEntry handles concurrent-removal correctly via post-Spawn recheck
- Close handles mid-creation placeholders cleanly
- Get's wait-on-ready selects on ctx.Done() and r.closing
- Detail() reads LastModel / LastUsed inside entry.Mu.TryLock critical section (CR-04 fix)
- SESSION_MAX gate enforced at placeholder-install before subprocess spawn
- Handler defer order (Unlock first, MarkUsed second) ensures MarkUsed runs under entry.Mu (CR-01 fix)
- Dead entries treated as not-present in Get's lookup -- auto-recovery path exists

### adapter-openai
- JSON body cap via decodeJSONBody + MaxBytesReader
- Engine errors logged raw + generic 500 message -- never echoes user payload
- Flusher assertion BEFORE WriteHeader so caller can fall back to JSON 500
- SSE single-goroutine invariant -- w + flusher touched only inside the select loop
- ID + created computed once per response, reused every chunk
- Streaming-coerce buffering with split-stream guard (textFlushed)
- PreHook short-circuit caught BEFORE SSE headers open
- Stream re-route on req.Stream=false flip emits synthetic SSE (avoids SDK 'request ended without chunks')
- Tools decode is per-entry tolerant
- finish_reason mapping centralized with safe default 'stop'
- ToolCall arguments emitted as JSON-encoded string per OpenAI spec
- Idle-timeout drain-safe Stop/Reset
- responseMessage.Content non-omitempty per OpenAI spec
- PostHooks fire on disconnect / mid-stream Result-error via aggregatedResponse forensics path

### adapter-anthropic
- tool_use input rendered as JSON object via *map[string]any + omitempty
- stop_reason override to tool_use applied on streaming / synthetic-SSE / non-streaming
- Dual-header auth: x-api-key wins, falls back to Authorization Bearer
- anthropic-version required check; anthropic-beta accept-and-ignore with debug log
- PreHook short-circuit caught BEFORE SSE headers written
- Watchdog StopWatchdog nil-guarded on short-circuit Run
- input_json_delta atomicity guard via pendingToolUseFlush (SDK MessageStream parser safety)
- Zero-arg tool_use flush emits `{}` partial_json so parser never sees empty accumulator
- redacted_thinking dropped at wire decode with debug log (D-13)
- Synthetic SSE re-route preserves stream:true contract on PreHook flip (avoids v1.8.3 regression)
- PostHook errors WARN-and-swallow on streaming; propagate on non-streaming
- Idle watchdog drain-safe Stop/Reset on both streaming and aggregated paths
- Error envelope shape matches docs.anthropic.com spec
- Inbound thinking blocks preserved as ContentKindThinking (D-11); outbound thinking emitted as content_block_start/thinking
- 4 MiB body cap with distinct 413 vs 400 mapping
- writeError never echoes engine err.Error() raw (T-02-33)
- SSE writes use json.Marshal so content newlines cannot break event:/data:/blank framing
- D-06 watchdog stop on clean completion suppresses spurious session/cancel

### adapter-ollama
- Body-cap discipline via decodeJSONBody on every handler (chat 4MiB, generate 4MiB, show 1MiB, stubs 64KiB)
- NDJSON line discipline -- every emit through marshalAndWrite or controlled `%s\n`
- Flusher assertion BEFORE WriteHeader(200) on emitter open
- D-07 cancelFn signaling on every write/marshal failure
- D-05 single-goroutine invariant on w/flusher/emitterState
- Watchdog stop() nil-guarded on PreHook short-circuit (Pitfall 4)
- PreHook short-circuit caught BEFORE runNDJSONEmitter opens headers
- JSON struct tags match Ollama spec
- Tool-call Arguments rendered as JSON object (correct for Ollama surface)
- /api/generate surface gates ChunkKindToolCall / ChunkKindThought
- X-Forwarded-For untrusted by default
- Idle-timer drain-safe Stop/Reset mirrors engine pattern
- WR-01 textFlushed guard prevents split-stream coerce (Pitfall 3)

### plugin-chain
- AuthHook uses subtle.ConstantTimeCompare (timing side-channel mitigated)
- AuthHook.Describe publishes only token_count, never bytes
- AuthHook empty-tokens path matches Node disabled-auth parity
- RequestIDHook uses oklog/ulid/v2.Make backed by crypto/rand monotonic source
- ctxKey is unexported struct type -- cross-package key collision impossible
- ChatTraceHook serializes writes via sync.Mutex so concurrent Pre/Post don't interleave
- ChatTraceHook.emit nil-Writer short-circuit
- ChatTraceHook + LoggingHook use LoadAndDelete on happy path
- Engine.RunPostHooks documents + tests no-double-fire invariant
- Chain.Filter typo-fail-fast surfaces all unknown ENABLED_HOOKS via errors.Join
- Chain.Filter preserves registration order -- env-var ordering cannot rewrite hook sequence
- chi.Recoverer catches PreHook/PostHook panics on the HTTP handler stack
- JSONFormatSteeringHook stateless Pre-only mutator
- JSONFormatSteeringHook nil-guards req and req.Format
- ChatTraceHook.Before defends against empty rid by minting NewRequestID() locally
- LoggingHook nil-Logger fallback to slog.Default()

### plugin-pii
- Per-request counter / nextN maps freshly allocated each Before call
- WalkStrings allocates fresh map/slice on every recursive level -- input never mutated
- Summary.Add / Counts mutex-guarded for future concurrent walkers
- EntityActions / HashKey / EncryptKey never surfaced via Describe
- EncryptValue uses fresh nonce per call with entity bound as GCM AAD
- Compile-time PreHook + PostHook interface satisfaction assertions
- DecryptToken failures classified by reason and logged per match without panic
- Recognizer regexes compiled once at package init via MustCompile
- hashTag uses HMAC-SHA256 (not raw SHA256) -- length-extension mitigated
- Per-request Summary fallback when ctx is not stamped
- mergeSpansGreedy gives regex spans absolute priority over NER spans on overlap
- Encrypt round-trip flips req.Stream=false at Before so After runs on aggregated response

### server-handlers
- Middleware ordering enforced and documented: RequestID -> Recoverer -> accessLog
- Request body limits at the adapter decode layer (correctly NOT global, preserves streaming uploads)
- ReadHeaderTimeout: 10s set (Slowloris mitigation per gosec G112)
- Run/Shutdown lifecycle has 30s drain deadline and clean error propagation
- RunUntilSignal goroutine has guaranteed exit path via derivedCtx.Done() + signal.Stop on defer
- /health, /health/agents, /health/hooks, /health/pool are all nil-safe with canonical zero envelopes
- /health/hooks and /health/pool register MethodFunc for POST/PUT/DELETE returning 405 with Allow: GET
- SessionsRouter handleDelete maps ErrSessionNotFound to 404 (D-08 wire contract)
- Registry.Delete uses map-delete-first then drops lock before Close
- NewFromConfig installs discard logger when cfg.Logger is nil (WR-03 fix)
- /admin mounted at OUTER router with auth-exempt posture (documented v1 carve-out)
- auth.IPAllowlist does NOT trust X-Forwarded-For unless AuthTrustXFF is set (Codex H-7), strips ::ffff:
- Surface routing groups SurfaceMount by Prefix and opens ONE r.Route per prefix (avoids chi double-Mount panic)

### admin-observability
- SSE single-goroutine-per-request invariant (D-05)
- defer Unsubscribe on every SSE exit path including ctx cancel and write errors
- ctx.Done() selected in sseLoop so client disconnect immediately exits
- Multi-line SSE payload splitting prevents \n injection of frame boundaries (T-6.1-13)
- CRLF and lone CR normalization in writeSSELine
- Non-blocking broadcast with select/default drops lines for slow subscribers (T-6.1-11)
- subscriber.closed flag under t.mu before close(sub.C) prevents send-on-closed-channel panic
- Lazy tailer goroutine start + auto-exit on subscriber-count=0
- TailerMaxLineBytes (1MB) cap bounds memory growth on producer never emitting newline
- Rotation detection via os.SameFile + size-shrink (D-10)
- Lock ordering t.mu then ring.mu consistent across broadcast/Snapshot/Push
- Template render uses bytes.Buffer + post-render WriteHeader (WR-05)
- Snapshot JSON encoded into bytes.Buffer before writing
- snapshotHandler nil-safe for PoolDetail and Registry
- AUTH_TOKEN, PII_HASH_KEY, PII_ENCRYPT_KEY never surfaced -- only presence booleans (T-8-LEAK)
- Static asset path traversal blocked by fs.ValidPath enforcement
- TailerRegistry mu.Lock spans check+insert preventing duplicate tailers
- Flusher cast checked at SSE handler entry with clean 500 fallback
- Snapshot taken AFTER Subscribe so no line is missed in backfill

### boot-config-shutdown
- Bearer token compare uses crypto/subtle.ConstantTimeCompare
- Authorization-wins precedence with case-insensitive scheme match
- X-Forwarded-For NOT trusted unless AuthTrustXFF explicitly enabled; strips ::ffff: prefix
- auth.Config zero-value passthrough matches Node parity defaults
- Pool.Close is sync.Once-guarded; cleanup closure nil-safe for partial-init failures
- Pool.Warmup bounded by 30s context deadline
- Pool warmup runs BEFORE HTTP listener accepts; closeAll on partial failure
- RunUntilSignal cancels derivedCtx + 30s Shutdown deadline + signal.Stop on defer
- ENABLED_SURFACES, PII_REDACTION_MODE, PII_ENABLED_ENTITIES, PII_ENTITY_ACTIONS enforce typo-fail-fast at boot
- PII_REDACTION_MODE=hash with empty PII_HASH_KEY refuses to boot
- encrypt-active with empty PII_ENCRYPT_KEY refuses to boot
- STREAM_IDLE_TIMEOUT_SEC negative values rejected at boot
- ALLOWED_IPS env parse accumulates per-entry errors via errors.Join
- Config errors accumulated and joined so operator sees ALL boot errors at once
- config package never calls os.Exit; main owns process exit
- Cleanup closure preserves load-bearing close ordering: chatTraceRotator -> registry -> pool

## Top 5 Things To Fix Before Daily-Driver Use

1. **acp-push-send-on-closed-channel-panic**: A close-races-late-update race in `Stream.push` sends on a closed channel, crashing the entire gateway process from the readLoop goroutine with no recover -- a single hot-path race kills the binary on a laptop with no supervisor.
2. **boot-sigint-before-signal-handler-leaks-kiro-cli**: SIGINT during pool warmup terminates the process before any signal handler is installed, leaving kiro-cli children in their own pgrps as orphans/zombies that survive every aborted boot.
3. **session-subprocess-crash-leaves-orphan-entry**: Registry never watches Entry.Client.Done(), so a crashed kiro-cli subprocess leaves an Entry returning 500 to every retry on its X-Session-Id for up to 30 minutes until the TTL reaper fires -- a single sleep/wake or OOM kill stalls all reuse of a session id.
4. **pool-respawn-ctx-cancel-shrinks-pool-permanently**: HTTP client disconnect during a dead-slot respawn drops the slot from the pool forever; repeated disconnect-during-respawn (laptop sleep/wake with cached client tabs) walks the pool 4->3->2->1->0 with no recovery short of a process restart.
5. **pii-decrypt-regex-misses-underscore-entities**: SIP_URI and MAC_ADDRESS encrypted tokens never match the decrypt regex (missing `_` in the entity capture group), so encrypted opaque `[PII:SIP_URI:...]` tokens leak verbatim to the client surface with zero log signal -- a silent privacy/fidelity hole on the encrypt round-trip.
