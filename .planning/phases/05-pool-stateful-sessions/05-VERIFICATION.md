---
phase: 05-pool-stateful-sessions
verified: 2026-05-26T14:31:03Z
status: gaps_found
score: 3/5 must-haves verified
overrides_applied: 0
gaps:
  - truth: "Two requests with same X-Session-Id route to same dedicated kiro-cli subprocess (verifiable via per-slot label in /health/agents); requests without the header use the warm pool."
    status: failed
    reason: "X-Session-Id stateful path is reproducibly broken end-to-end against the real kiro-cli. The session entry IS created and visible in /health/agents (Registry.Get works), but every subsequent /api/chat with that header returns HTTP 500 with `engine: prompt: session: prompt: acp: prompt: rpc error -32603: Internal error`. The pool (stateless) path works correctly — the failure is specific to the registry path. SC3 is observably not met."
    artifacts:
      - path: "internal/session/entry_acp.go"
        issue: "Entry.Prompt calls e.Client.Prompt(ctx, sessionID, blocks) with the SessionID cached in createEntry at session-creation time. Against the real kiro-cli (kiro-cli 2.x in PATH), this RPC always returns JSON-RPC code -32603 (Internal error). Root cause not isolated by the executor — could be a SetModel sequence requirement, a cwd handshake difference between pool path and session path, or a kiro-cli session lifecycle mismatch (the session is created during createEntry but used later by a different ctx chain)."
      - path: "tests/e2e/pool_sessions_e2e_test.go"
        issue: "Three subtests fail against the live kiro-cli: SessionIDAffinity (line 218: status 500 want 200), IdleReap_RealTime (line 265: create session status 500), DeleteSession_OK (line 302: create session status 500). The DeleteSession_CancelsInFlight subtest passes only because it does not require the stream to produce any tokens — it asserts only that the stream terminates within 5s, which it does because Prompt returns immediately with the 500."
      - path: "internal/adapter/ollama/handlers_session_test.go"
        issue: "Unit tests use session.NewEntryForTest(fakeACPClient{}, sid) which substitutes a fake ACP client whose Prompt method does not exercise kiro-cli's session/prompt JSON-RPC. The unit-test suite therefore cannot catch the live integration failure. The fake ACP client is internal to the test file."
    missing:
      - "An end-to-end working stateful prompt against real kiro-cli. Currently 100% of stateful /api/chat requests fail. Likely root cause is in Entry.Prompt's ACP call sequence — needs trace of the wire protocol between the gateway and kiro-cli to compare with the working pool path. May require additional handshake (e.g., setSessionMode, or implicit SetModel) that the pool path acquires via its NewSession call but the session path does not."
      - "An e2e or integration smoke that pings the stateful path with real kiro-cli BEFORE phase closure — the existing e2e tests with the failing subtests need to be run against live kiro and pass."
  - truth: "Idle session reaped after SESSION_TTL_MS (default 30 min) — testable with shortened TTL — and DELETE /v1/sessions/:id immediately tears down, returns {deleted: \"<id>\"}."
    status: partial
    reason: "Unit-level coverage is comprehensive and passes: TestReaper_ReapsIdleSessionInRealTime (D-13 real-time with TTL=200ms), TestSessionsRouter_Delete_KnownSid_Returns200WithDeleted (D-08 wire shape), TestRegistry_Delete_KnownSid (cancels + closes). However, the integration path (e2e) for both reaping AND DELETE happy-path REQUIRES a session to exist first via Registry.Get — and that requires a successful stateful prompt to populate the entry. Because the stateful prompt fails (see SC3 gap above), the e2e subtests IdleReap_RealTime and DeleteSession_OK cannot validate the reap/delete behavior end-to-end. The DELETE handler IS correctly wired (TestSessionsRouter_Delete_UnknownSid_Returns404 passes against the live gateway in e2e), but the happy-path 'create-then-delete' integration cannot be demonstrated until the SC3 root cause is fixed."
    artifacts:
      - path: "tests/e2e/pool_sessions_e2e_test.go"
        issue: "IdleReap_RealTime subtest is unable to create the session it needs to reap (kiro-cli returns 500); DeleteSession_OK is similarly blocked."
    missing:
      - "End-to-end validation of session creation → reap and session creation → DELETE → 200 {deleted}. Blocked behind the SC3 fix."

deferred: []

human_verification:
  - test: "Manual perf-vs-Node delta (CLAUDE.md non-functional constraint)"
    expected: "p99(Go) ≤ p99(Node) + 10% under 4-thread × 8-conn × 30s wrk load against /api/chat with POOL_SIZE=4. Per-thread tail latency captured in tests/e2e/reports/PHASE5-PERF.md."
    why_human: "Requires the Node implementation at ../gitlab.rosetta.ericssondevops.com/loop_24/acp_server to be running side-by-side with wrk/ab installed; not hermetic. Plan 05-03 Task 6 was auto-approved with a placeholder, but the placeholder report does NOT exist on disk (tests/e2e/reports/PHASE5-PERF.md is missing). This human gate has not been satisfied."
  - test: "Manual SESSION_MAX RSS sanity (05-VALIDATION.md Manual-Only Verifications)"
    expected: "8 sessions populated via curl loop; per-kiro-cli child RSS captured via ps -o pid,rss; per-session RSS within ±20% of mean AND 32 × avg ≤ 2 GB on an 8 GB host. Numbers recorded in PHASE5-PERF.md."
    why_human: "Platform- and binary-version-dependent (kiro-cli RSS varies). Cannot run in CI without a sized test host. SUMMARY claims auto-approval but no measurement report exists."
  - test: "Root-cause and fix the SC3 integration failure (X-Session-Id → kiro-cli)"
    expected: "Manual investigation of the wire protocol difference between pool.Pool.NewSession + Prompt and session.Entry.Client.NewSession + Prompt. Likely places to look: cwd handshake, implicit SetModel call, session/prompt parameters that differ between paths. After fix, OTTO_E2E=1 go test -tags=e2e ./tests/e2e/... -run TestE2E_PoolSessions passes all 10 subtests."
    why_human: "Requires interactive debugging of the JSON-RPC wire between the gateway and kiro-cli. The fix may involve protocol-level changes that need to be discussed before encoding into the next plan."
---

# Phase 5: Pool + Stateful Sessions Verification Report

**Phase Goal:** Replace the single-session engine with a real warm pool (POOL_SIZE=4 default) and add stateful sessions keyed by X-Session-Id via a registry with idle reaping. Observable via /health/agents.
**Verified:** 2026-05-26T14:31:03Z
**Status:** gaps_found
**Re-verification:** No — initial verification

## Executive Summary

Phase 5's POOL slice (POOL-01..04) and OBSV-02 slice are observably complete. The pool warmup blocks server startup (SC1), saturates correctly at POOL_SIZE=4 (SC2), exposes the D-15/D-16 wire shape (SC5), and lazily respawns dead slots (SC5 dead-slot detection). All unit and race-detector tests pass green across 12 packages.

However, **SC3 (X-Session-Id session affinity) is observably broken end-to-end against the real kiro-cli.** The session registry correctly creates an entry — the entry shows up in `/health/agents` — but every subsequent chat request through the registry path returns HTTP 500 with `acp: prompt: rpc error -32603: Internal error`. The pool (stateless) path works correctly with the same kiro-cli binary and the same configuration. The failure is reproducible across multiple X-Session-Id values, fresh boots, and both Ollama and (by symmetry of the code paths) OpenAI/Anthropic surfaces.

Because SC3 is broken, SC4's happy-path (idle reap, DELETE) cannot be validated end-to-end either — there is no surviving session to reap or to delete. The unit tests at the session-registry level all pass (the registry mechanics are correct in isolation with fake ACP clients), but the integration with real kiro-cli has never actually worked at the prompt boundary.

The code reviewer surfaced 4 BLOCKER findings (CR-01..CR-04) about concurrency/ordering bugs. These are real defects (CR-01 in particular is confirmed in the code), but they are **separate from the SC3 integration failure** — they are post-phase polish/race-detector cleanliness items. The SC3 failure is a goal-blocking integration bug that the reviewer did not surface because the review did not run the live e2e suite.

## Goal Achievement

### Observable Truths (Success Criteria)

| #   | Truth                                                                                                                                          | Status         | Evidence       |
| --- | ---------------------------------------------------------------------------------------------------------------------------------------------- | -------------- | -------------- |
| SC1 | Pool warmup completes before http.Server.ListenAndServe() accepts; cold boot — request #2 ≈ request #10                                       | ✓ VERIFIED     | tests/e2e/pool_sessions_e2e_test.go::WarmupBeforeListen PASS (14.11s) — confirms warmup-before-listen invariant against real kiro-cli with POOL_SIZE=4 |
| SC2 | Under N concurrent stateless /api/chat (N ≤ POOL_SIZE) each gets a distinct slot; N > POOL_SIZE blocks                                         | ✓ VERIFIED     | tests/e2e/pool_sessions_e2e_test.go::SaturationBlocking PASS — "SC2 peak busy: 4 (POOL_SIZE=4)" with 8 concurrent goroutines |
| SC3 | Two requests with same X-Session-Id route to same dedicated kiro-cli subprocess; requests without the header use the warm pool                | ✗ FAILED       | Live curl reproduces 100% failure rate: stateful request returns `{"error":"... acp: prompt: rpc error -32603: Internal error"}`. tests/e2e/.../SessionIDAffinity FAIL line 218. Session entry IS created in registry (visible in /health/agents) but Prompt RPC always fails. Stateless path on the same gateway returns 200 with real model output. |
| SC4 | Idle session reaped after SESSION_TTL_MS (default 30 min) — testable with shortened TTL — and DELETE /v1/sessions/:id immediately tears down, returns {deleted: "<id>"} | ⚠ PARTIAL (FAIL on goal) | Unit tests pass: TestReaper_ReapsIdleSessionInRealTime, TestSessionsRouter_Delete_KnownSid_Returns200WithDeleted, TestRegistry_Delete_UnknownSid_ReturnsErrSessionNotFound. E2E DeleteSession_Unknown passes. E2E IdleReap_RealTime + DeleteSession_OK FAIL (cannot create session). DELETE handler IS reachable and returns 404 for unknown sids; the 200-on-success happy path is unverifiable end-to-end until SC3 is fixed. |
| SC5 | GET /health/agents returns per-pool-slot detail (alive, busy, label) AND per-session detail (alive, last_used); dead slots detected and lazily re-spawned without blocking other acquires | ✓ VERIFIED     | tests/e2e/pool_sessions_e2e_test.go::HealthAgentsShape PASS (D-15/D-16 wire shape verified); DeadSlotLazyRespawn PASS (SIGKILL of one kiro-cli child → /health/agents shows alive:false → next chat respawns → all 4 slots alive again, no blocking). Auth-exempt access (D-18) verified. |

**Score:** 3/5 truths verified (SC1, SC2, SC5); 1 failed (SC3); 1 partial (SC4 unit-green but integration blocked by SC3).

### Required Artifacts

| Artifact | Expected    | Status | Details |
| -------- | ----------- | ------ | ------- |
| `internal/acp/client.go` | `Done() <-chan struct{}` accessor | ✓ VERIFIED | Line confirmed: `func (c *Client) Done() <-chan struct{}` |
| `internal/pool/exit_watcher.go` | startExitWatcher per-slot goroutine | ✓ VERIFIED | Function declared; uses two-branch select on `<-slot.Client.Done()` vs `<-p.closing` |
| `internal/pool/pool.go` | dead bool field, closing chan, respawnSlot, removeSlot, slotAlive, NewSession dead-slot branch | ✓ VERIFIED | All grep gates pass; 2 invocation sites for startExitWatcher (initSlot + respawnSlot); `if !p.slotAlive(slot)` branch present in NewSession |
| `internal/pool/detail.go` | AgentSlot row + Pool.Detail() | ✓ VERIFIED | Type + method present; D-15 JSON tags `label`, `alive`, `busy`, `current_session_id` all present |
| `internal/config/config.go` | POOL_SIZE env default 4; SESSION_TTL_MS, SESSION_MAX, SESSION_TICK_INTERVAL_MS env loaders | ✓ VERIFIED | `getEnvInt("POOL_SIZE", 4)` confirmed; SESSION_* env vars present |
| `internal/session/registry.go` | Registry + Entry + Get (Pitfall 4) + Delete (Codex M-3) + Close (bounded shutdown) + error vars | ✓ VERIFIED | ErrSessionNotFound + ErrSessionMaxExceeded declared; creating sentinel + ready chan present; map-delete-first in Delete; close(r.closing) + r.wg.Wait() in Close |
| `internal/session/reaper.go` | reaperLoop + reapOnce with TryLock + snapshot-then-iterate | ✓ VERIFIED | time.NewTicker(r.cfg.TickInterval), es.entry.Mu.TryLock(), delete(r.entries, es.sid), all present |
| `internal/session/entry_acp.go` | Entry methods satisfying engine.ACPClient | ✓ VERIFIED (compile) / ✗ FAILED (integration) | `var _ engine.ACPClient = (*Entry)(nil)` compile-time gate passes; however, Entry.Prompt against real kiro-cli reproducibly returns rpc -32603 (see SC3 gap) |
| `internal/session/stats.go` | SessionDetail D-16 row shape + Stats + Detail | ✓ VERIFIED | Type present with all five JSON tags (id, alive, busy, last_used, model); Stats() + Detail() methods present |
| `internal/server/agents.go` | AgentsResponse + AgentSlot + AgentSession + agentsHandler + PoolDetailSource | ✓ VERIFIED | All types declared with correct JSON tags; agentsHandler renders pool + sessions |
| `internal/server/sessions_delete.go` | SessionsRouter + handleDelete with D-08 wire shape | ✓ VERIFIED | Type + RouteRegistrar method + 200/404/500 branches; `errors.Is(err, session.ErrSessionNotFound)` 404 path; `{"deleted": sid}` 200 path |
| `internal/adapter/{ollama,openai,anthropic}/handlers.go` | X-Session-Id branch + entry.Mu.Lock + defer MarkUsed | ⚠ ORPHANED (CR-01 defer LIFO bug) | All three handlers contain the X-Session-Id branch; HOWEVER, the defer order is `defer entry.MarkUsed()` BEFORE `defer entry.Mu.Unlock()` which means MarkUsed runs OUTSIDE the mutex (LIFO). This is a data race per CR-01. Code present, semantic incorrect. |
| `cmd/otto-gateway/main.go` | Registry construction, Start, registryStatsAdapter, poolDetailAdapter, SessionsRouter mount, cleanup ordering registry→pool | ✓ VERIFIED | All five insertions present; `a.registry.Close()` precedes `a.pool.Close()` in cleanup closure |
| `tests/e2e/pool_sessions_e2e_test.go` | 10 subtests under TestE2E_PoolSessions | ⚠ PARTIAL | File exists with all 10 t.Run blocks; 7 pass + 3 fail (SessionIDAffinity, IdleReap_RealTime, DeleteSession_OK); 1 skips intentionally (AllDeadRespawnFails — by design when warmup fails); 1 always passes for the wrong reason (DeleteSession_CancelsInFlight does not validate stream content) |
| `tests/e2e/reports/PHASE5-PERF.md` | Manual perf + RSS report | ✗ MISSING | File does NOT exist on disk locally despite SUMMARY claim of "auto-approval placeholder". Task 6 acceptance criteria not satisfied. |

### Key Link Verification

| From | To  | Via | Status | Details |
| ---- | --- | --- | ------ | ------- |
| `Pool.NewSession` | `respawnSlot` | `p.slotAlive(slot) == false branch` | ✓ WIRED | `if !p.slotAlive(slot) { p.respawnSlot(ctx, slot) }` present |
| `startExitWatcher` | `Client.Done` | `<-slot.Client.Done() select branch` | ⚠ WIRED-WITH-RACE | Channel evaluated at goroutine entry, not captured at spawn site (WR-01 in REVIEW). Practical risk low because OLD client Close() takes ms; OLD watcher virtually always wins the schedule before respawn step 4. |
| `Pool.Close` | `startExitWatcher goroutine` | `close(p.closing)` | ✓ WIRED | First line of Close body; goleak gate confirms cleanup |
| `NewFromConfig` | `agentsHandler` | `s.router.Get("/health/agents", s.agentsHandler)` outside auth.Route | ✓ WIRED | On outer router; tests confirm D-18 auth-exempt |
| `SessionsRouter.RegisterRoutes` | `Registry.Delete` | `Registry.Delete(sid) inside DELETE handler` | ✓ WIRED | handleDelete calls sr.Registry.Delete(sid) |
| `Ollama/OpenAI/Anthropic handlers` | `Registry.Get` | `r.Header.Get("X-Session-Id")` non-empty branch | ✓ WIRED (code) / ✗ INTEGRATION FAILED | Code path is present and traced; the FAIL is that the entry returned is unusable downstream |
| `cmd/otto-gateway/main.go` | `Registry.Close` | cleanup closure: registry.Close BEFORE pool.Close | ✓ WIRED | TestNewApp_CleanupOrdersRegistryBeforePool passes |

### Data-Flow Trace (Level 4)

Tracing the stateful chat flow against real kiro-cli reveals a HOLLOW data flow at the Prompt boundary:

| Artifact | Data Variable | Source | Produces Real Data | Status |
| -------- | ------------- | ------ | ------------------ | ------ |
| `internal/server/agents.go` | resp.Pool.Slots, resp.Sessions | s.poolDetail.Detail(), s.registry.Detail() | ✓ Yes — live curl shows populated slots and session entries | ✓ FLOWING |
| `internal/session/registry.go::createEntry` | client, sessionID | Factory.Spawn → client.Initialize → client.NewSession | ✓ Yes — kiro-cli child PID spawns, NewSession returns a real session id (verifiable via /health/agents showing the entry) | ✓ FLOWING |
| `internal/session/entry_acp.go::Entry.Prompt` | raw *acp.Stream | e.Client.Prompt(ctx, sessionID, blocks) | ✗ No — kiro-cli rejects every Prompt with rpc -32603 | ✗ DISCONNECTED |
| `internal/adapter/ollama/handlers.go` | engine result | eng.Collect(r.Context(), req) | ✗ No — propagates the upstream Prompt failure to the client as 500 | ✗ HOLLOW_UPSTREAM |

The registry path correctly spawns a kiro-cli child and creates a session id, but the session id is never successfully used to send a Prompt. The data flow terminates at the JSON-RPC error returned by kiro-cli's session/prompt method.

### Behavioral Spot-Checks

| Behavior | Command | Result | Status |
| -------- | ------- | ------ | ------ |
| Stateless /api/chat returns real content | `curl POST /api/chat` (no X-Session-Id) | 200 with assistant message body | ✓ PASS |
| Stateful /api/chat returns real content | `curl POST /api/chat -H 'X-Session-Id: smoke-1'` | 500 with `acp: prompt: rpc error -32603: Internal error` | ✗ FAIL |
| `/health/agents` returns D-15/D-16 shape, auth-exempt | `curl /health/agents` (no auth) | 200 with pool + sessions arrays, correct JSON tags | ✓ PASS |
| `/health` populates sessions.active | `curl /health` after creating a session | sessions.active increments from 0→1 | ✓ PASS |
| DELETE /v1/sessions/unknown returns 404 | `curl DELETE /v1/sessions/does-not-exist` | 404 with `{"error":"unknown session"}` | ✓ PASS |
| Pool.Detail() reflects dead slot after SIGKILL | `pgrep -P <gw-pid> kiro-cli` + SIGKILL + curl /health/agents | One slot shows alive:false; next chat respawns it | ✓ PASS |
| Build + vet + race all green | `go test ./... -count=1 -race -timeout=180s; go vet ./...; go build ./...` | All 12 packages green; vet clean; build clean | ✓ PASS |

### Probe Execution

| Probe | Command | Result | Status |
| ----- | ------- | ------ | ------ |
| `scripts/.../probe-*.sh` (conventional) | `find scripts -path '*/tests/probe-*.sh' -type f` | No probes found in repo | N/A (no probe-based phase) |
| Full unit + race suite | `go test ./... -count=1 -race -timeout=180s` | All 12 packages PASS | ✓ PASS |
| Full e2e suite | `OTTO_E2E=1 go test -tags=e2e -run TestE2E_PoolSessions ./tests/e2e/... -count=1 -timeout=180s` | 3 of 10 subtests FAIL (SessionIDAffinity, IdleReap_RealTime, DeleteSession_OK); 1 SKIP (AllDeadRespawnFails — by design); 6 PASS | ✗ FAILED |

### Requirements Coverage

| Requirement | Source Plan | Description | Status | Evidence |
| ----------- | ---------- | ----------- | ------ | -------- |
| POOL-01 | 05-01 | Fixed-size pool (default POOL_SIZE=4) of warm kiro-cli subprocesses | ✓ SATISFIED | `getEnvInt("POOL_SIZE", 4)` in internal/config/config.go; live /health shows pool.size=4 |
| POOL-02 | 05-01 (regression) | Pool warmup completes before http.Server.ListenAndServe() accepts | ✓ SATISFIED | TestPool_Warmup_* unit tests pass; e2e WarmupBeforeListen PASS |
| POOL-03 | 05-01 (regression) | Acquire returns the first free slot or blocks on a buffered channel of free slots | ✓ SATISFIED | Existing TestPool_NewSession_* tests stay green; e2e SaturationBlocking confirms 8-concurrent requests under POOL_SIZE=4 (4 block then proceed) |
| POOL-04 | 05-01 | Dead slots are detected and re-spawned lazily without blocking other acquires | ✓ SATISFIED | TestPool_DeadSlot_LazyRespawn, TestPool_DeadSlot_RespawnFailure_PoolShrinks, TestPool_DeadSlot_ConcurrentAcquiresUnaffected, TestExitWatcher_FiresOnClientDone, TestExitWatcher_ExitsOnPoolClose all pass; e2e DeadSlotLazyRespawn PASS against live kiro-cli (SIGKILL + observe respawn) |
| SESS-01 | 05-02 (registry) + 05-03 (surface) | Requests with X-Session-Id header use a dedicated kiro-cli subprocess via SessionRegistry, not the warm pool | ⚠ PARTIAL — UNIT-OK / INTEGRATION-FAILED | Registry.Get lazy-create + Pitfall 4 race resolution + SESSION_MAX gate all pass unit tests; surface handlers route on X-Session-Id; HOWEVER the end-to-end stateful prompt against real kiro-cli fails 100% (SC3 gap) |
| SESS-02 | 05-02 | Idle sessions reaped after SESSION_TTL_MS (default 1,800,000 = 30 min); reaper runs every 60s | ⚠ PARTIAL — UNIT-OK / INTEGRATION-BLOCKED | TestReaper_ReapsIdleSessionInRealTime + TestReaper_SkipsInFlightSession + TestReaper_DoesNotReapRecentlyUsed + 5 more reaper tests all pass; SESSION_TTL_MS default 30 min in internal/config; e2e IdleReap_RealTime cannot validate because session never gets created (blocked behind SC3) |
| SESS-03 | 05-02 (Delete) + 05-03 (HTTP) | DELETE /v1/sessions/:id tears down a stateful session immediately and returns {deleted: "<id>"} | ⚠ PARTIAL — UNIT-OK / INTEGRATION-PARTIAL | TestSessionsRouter_Delete_KnownSid_Returns200WithDeleted, TestSessionsRouter_Delete_UnknownSid_Returns404, TestRegistry_Delete_KnownSid, TestRegistry_Delete_CancelsInFlight all pass; e2e DeleteSession_Unknown PASS (404); DeleteSession_OK FAIL (cannot create session to delete); DeleteSession_CancelsInFlight PASS but only because stream terminates early due to the SC3 failure (not for the right reason) |
| OBSV-02 | 05-03 | GET /health/agents returns per-pool-slot detail (alive, busy, label) and per-session detail (alive, last_used) | ✓ SATISFIED | All 8 server-level agents tests pass; D-15/D-16 wire shape locked via reflect-based JSON-tag assertion (TestAgentsHandler_TypesAreReachable); auth-exempt D-18 verified (TestAgentsHandler_NoAuthRequired); live e2e HealthAgentsShape PASS |

### Anti-Patterns Found

The reviewer's CR-01..CR-04 are all real defects present in the code. CR-01 is confirmed by inspection of the defer order at the cited lines.

| File | Line | Pattern | Severity | Impact |
| ---- | ---- | ------- | -------- | ------ |
| `internal/adapter/ollama/handlers.go` | 65-66 (and parallel sites in handleGenerate, openai/handlers.go, anthropic/handlers.go) | `defer entry.MarkUsed()` BEFORE `defer entry.Mu.Unlock()` (CR-01) | 🛑 BLOCKER for the reviewer's claim; ⚠ WARNING for phase goal | MarkUsed runs outside Mu (LIFO). Race detector did not flag in current suite because fake ACP clients don't interleave with the live reaper. With production timing + reaper tick + concurrent MarkUsed, a session that just streamed successfully could be reaped due to stale LastUsed observed between Unlock and MarkUsed. The comment in code asserts the orderings are "semantically equivalent" — that is incorrect. |
| `internal/session/registry.go` | 294-301, 336-342 | select-default close(e.ready) is not race-safe (CR-02) | ⚠ WARNING | Under concurrent createEntry/Delete/Close, a double-close panic is possible. Risk is narrow but real. Should be replaced with sync.Once or single-owner pattern. |
| `internal/pool/pool.go` | 284-289 | `Pool.Stats.Alive` does not check `!s.dead` (CR-03) | ⚠ WARNING | /health reports pool.alive == size even when N slots are dead. /health/agents agrees with reality (uses !slot.dead). Operator dashboards using /health see a stale picture. |
| `internal/session/reaper.go` | 94 + `internal/session/entry_acp.go` | `e.Dead = true` and `e.LastModel = ...` written outside r.mu (CR-04) | ⚠ WARNING | Race-detector hazard. Did NOT flag in -race suite (likely because reader and writer use different locks; race detector requires concurrent access — the test suite doesn't have concurrent Detail() + reap + SetModel). Production risk: stale reads on amd64/arm64; under heavier loads or race-targeted tests this surfaces. |
| `internal/pool/pool.go` | 214-247 + `exit_watcher.go` | OLD exit-watcher reads `slot.Client.Done()` at select entry rather than capturing at spawn (WR-01) | ⚠ WARNING | Latent goroutine-scheduling-timing dependency on respawn |
| `tests/e2e/reports/PHASE5-PERF.md` | N/A | File missing on disk despite SUMMARY's auto-approval claim | ⚠ WARNING | Manual perf+RSS gate is not satisfied; SUMMARY claim is misleading |

No `TBD`/`FIXME`/`XXX` markers found in modified files.

### Human Verification Required

#### 1. Root-cause and fix the SC3 integration failure (X-Session-Id → kiro-cli)

**Test:** Run `OTTO_E2E=1 go test -tags=e2e -run TestE2E_PoolSessions/SessionIDAffinity ./tests/e2e/... -count=1 -timeout=60s` — currently failing at pool_sessions_e2e_test.go:218 with "sid request 0: status 500, want 200". Reproducible via:

```
KIRO_CMD=$(which kiro-cli) POOL_SIZE=2 /tmp/otto-gateway-verify &
curl -X POST -H 'X-Session-Id: smoke' http://127.0.0.1:11434/api/chat \
  -d '{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}'
# Expected: 200 with assistant message body
# Actual:   500 {"error":"... acp: prompt: rpc error -32603: Internal error"}
```

**Expected:** Once root-caused (likely in `internal/session/entry_acp.go::Entry.Prompt` or `internal/session/registry.go::createEntry`'s NewSession handshake — possibly a missing setSessionMode or cwd mismatch), stateful chat requests return 200 with real model output. All three failing e2e subtests (SessionIDAffinity, IdleReap_RealTime, DeleteSession_OK) pass.

**Why human:** Requires interactive debugging of the JSON-RPC wire between gateway and kiro-cli to compare working pool path vs broken session path. The unit tests pass because they use a fake ACP client that does not exercise kiro-cli's actual session/prompt JSON-RPC. Trace-level enabling (or a packet capture / strace of kiro-cli's stdin) is needed to spot the protocol difference.

#### 2. Manual perf-vs-Node delta (CLAUDE.md non-functional constraint)

**Test:** Boot the Node implementation at the same port with POOL_SIZE=4 + the gateway with POOL_SIZE=4, run `wrk -t4 -c8 -d30s --latency` against both, capture p50/p99/tail. Record numbers in `tests/e2e/reports/PHASE5-PERF.md`.

**Expected:** p99(Go) ≤ p99(Node) + 10%. Report committed to `tests/e2e/reports/PHASE5-PERF.md`.

**Why human:** Requires the Node implementation at `../gitlab.rosetta.ericssondevops.com/loop_24/acp_server` running side-by-side, wrk/ab installed; not hermetic. **The placeholder file claimed by the SUMMARY does not actually exist on disk.**

#### 3. Manual SESSION_MAX RSS sanity

**Test:** Boot the gateway with `POOL_SIZE=4 SESSION_MAX=8 AUTH_TOKEN=hunter2`. Spawn 8 sessions via `curl` loop with unique X-Session-Id values. Capture per-kiro-cli RSS via `ps -o pid,rss -p $(pgrep -P $(pgrep otto-gateway) kiro-cli | tr '\n' ',')`. Record in PHASE5-PERF.md.

**Expected:** Per-session RSS consistent (±20% of mean) AND 32 × avg ≤ 2 GB on an 8 GB host.

**Why human:** Platform- and binary-version-dependent. Cannot run in CI. **Also blocked by the SC3 failure** — sessions cannot be populated until stateful chat works.

### Gaps Summary

**Blocker:**

1. **SC3 stateful affinity is broken end-to-end.** The session registry correctly creates entries (visible in /health/agents) but every Prompt against the dedicated kiro-cli subprocess returns rpc -32603. 100% failure rate across multiple sids and fresh boots. The pool (stateless) path works on the same gateway with the same kiro-cli. This blocks the load-bearing phase goal property: "stateful sessions keyed by X-Session-Id via a registry."

**Partial / cascading:**

2. **SC4 cannot be validated end-to-end** until SC3 is fixed. Unit tests at registry/reaper/SessionsRouter levels all pass (the in-isolation mechanics are correct), but the e2e IdleReap_RealTime and DeleteSession_OK subtests fail because session creation fails first. The DELETE 404 path is verified end-to-end.

**Concurrency defects identified by the reviewer (CR-01..CR-04):**

3. **CR-01 (defer LIFO)** is a real and confirmed defect in five handler sites — MarkUsed runs outside Mu. Race detector did not flag it because the unit-test suite uses fakes that don't exercise the reaper/handler interleaving. **This is goal-affecting** because a misordered MarkUsed combined with a real reaper tick could reap a just-completed session, defeating SC3+SC4. **Recommendation: fix before phase close.**

4. **CR-02..CR-04** are race-detector hazards / observability skew. The race detector did not flag them in current tests, but the code is structurally racy. Acceptable to defer to a polish/cleanup pass; document the deviation if accepted.

**Manual residual gate not satisfied:**

5. **tests/e2e/reports/PHASE5-PERF.md is missing.** SUMMARY claims auto-approval with placeholder, but the file is absent on disk. The CLAUDE.md non-functional constraint "must not be slower than Node" is not measured.

**This looks intentional in part — the deviations are post-phase polish for the reviewer's CR-02..CR-04 and possibly CR-01. But the SC3 failure is a goal-blocking bug, not a polish item.** To accept any of the CR findings as deferred, add to VERIFICATION.md frontmatter:

```yaml
overrides:
  - must_have: "Specific must-have text"
    reason: "Why deviation is acceptable"
    accepted_by: "username"
    accepted_at: "ISO timestamp"
```

The SC3 failure cannot be overridden — it is a binary integration failure observable via the live test suite, and it is the load-bearing property the phase goal demands.

---

_Verified: 2026-05-26T14:31:03Z_
_Verifier: Claude (gsd-verifier)_
