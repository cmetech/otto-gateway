---
phase: 5
slug: pool-stateful-sessions
status: approved
nyquist_compliant: true
wave_0_complete: true
created: 2026-05-26
approved: 2026-05-26
---

# Phase 5 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test (Go 1.23+) with goleak v1.3.0 |
| **Config file** | none — packages own their `testmain_test.go` goleak gate |
| **Quick run command** | `go test ./internal/pool/... ./internal/session/... ./internal/server/... ./internal/acp/... -count=1 -race -timeout=60s` |
| **Full suite command** | `make test-race` (= `go test ./... -count=1 -race -timeout=180s`) |
| **Estimated runtime** | quick ~15-20s · full ~60s (-race adds ~3x) |

---

## Sampling Rate

- **After every task commit:** Run the quick command (touched-package selector preferred)
- **After every plan wave:** Run the full suite command
- **Before `/gsd-verify-work`:** Full suite must be green AND `go vet ./...` AND `make ci` (lint + race + arch-lint + govulncheck) clean
- **Max feedback latency:** ~60 seconds for the quick path; ~180s with race detector for the full suite

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 05-01-T1 | 05-01 | 1 | POOL-01 | — | Push-exit signal accessor closes on Close + pingLoop kill (no panic, no leak) | unit | `go test -run 'TestClientDoneClosesOnClose\|TestClientDoneClosesOnPingFail' ./internal/acp/... -race -count=1` | ❌ W0 (acp test extension) | ⬜ pending |
| 05-01-T1 | 05-01 | 1 | POOL-01 | — | POOL_SIZE env default is 4 when env unset | unit | `go test -run TestPoolSizeDefaultsToFour ./internal/config/... -race -count=1` | ❌ W0 | ⬜ pending |
| 05-01-T2 | 05-01 | 1 | POOL-04 | T-05-06 | Dead slot detected pre-Acquire; lazy synchronous re-spawn at next Acquire; respawn failure shrinks pool + returns wrapped 503-ready error; exit-watcher goroutine exits on Pool.Close | unit | `go test -run 'TestDeadSlotLazyRespawn\|TestRespawnFailureShrinks\|TestExitWatcher_ExitsOnPoolClose' ./internal/pool/... -race -count=1` | ❌ W0 (`pool_test.go` + `exit_watcher_test.go` + `export_test.go`) | ⬜ pending |
| 05-01-T2 | 05-01 | 1 | POOL-02, POOL-03 | — | Warmup-before-listen behavior preserved; Acquire blocks when all busy | unit | `go test -run 'TestWarmupBeforeListen\|TestAcquireBlocksWhenAllBusy' ./internal/pool/... ./cmd/otto-gateway/... -race -count=1` | ✅ existing (Phase 2) — re-run unchanged | ⬜ pending |
| 05-01-T3 | 05-01 | 1 | OBSV-02 (pool side) | — | `Pool.Detail()` returns AgentSlot rows with `label`, `alive`, `busy`, `current_session_id` per D-15 | unit | `go test -run 'TestPoolDetail_Rows\|TestPoolDetail_CurrentSessionID' ./internal/pool/... -race -count=1` | ❌ W0 (`detail_test.go`) | ⬜ pending |
| 05-02-T0 | 05-02 | 2 | All SESS-* (Wave 0 gate) | T-05-06 | goleak gate active for new `internal/session` package; compile-time assertion `var _ engine.ACPClient = (*Entry)(nil)`; STUB bodies for Registry/Reaper/Entry construction | unit | `go test ./internal/session/... -count=1 -timeout=10s` (testmain enforces goleak) | ❌ W0 (`testmain_test.go`, `registry_test.go`, `reaper_test.go`, `export_test.go`) | ⬜ pending |
| 05-02-T1 | 05-02 | 2 | SESS-01 | T-05-01, T-05-03 | Lazy create on first sid; double-check-locking prevents same-sid double-spawn (Pitfall 4); SESSION_MAX cap surfaces typed error → 503; Codex M-3 map-delete-first Delete; SetModel diff-skip + 4xx surface (no entry teardown) | unit | `go test -run 'TestRegistry_Get_FirstCreatesEntry\|TestRegistry_Get_RacingSameSid_NoDoubleSpawn\|TestRegistry_Get_SessionMaxExceeded\|TestRegistry_Delete_MapFirst\|TestEntry_SetModel_DiffSkip\|TestEntry_SetModel_ErrorNoTeardown' ./internal/session/... -race -count=1` | ❌ W0 | ⬜ pending |
| 05-02-T2 | 05-02 | 2 | SESS-02 | T-05-02, T-05-06 | Reaper at-rest: 60s ticker (configurable); LastUsed updated at response-complete (D-11); TryLock skips in-flight (D-12); snapshot-then-iterate avoids holding r.mu across Close; real-time TTL=200ms / Tick=50ms reaps idle <1s; reaper exits on registry.Close (goleak) | unit | `go test -run 'TestReaper_ReapsIdleSessionInRealTime\|TestReaper_SkipsInFlightSession\|TestReaper_ExitsOnRegistryClose\|TestReaper_DeadlockFree_ReverseLockOrder' ./internal/session/... -race -count=1` | ❌ W0 (`reaper_test.go`) | ⬜ pending |
| 05-03-T1 | 05-03 | 3 | OBSV-02 | T-OBSV-DOS | `GET /health/agents` returns 200 with D-14/D-15/D-16 shape; auth-exempt (works without `AUTH_TOKEN`); `Sessions.Active` populated from registry.Stats() | unit/integration | `go test -run 'TestHealthAgents_Shape\|TestHealthAgents_AuthExempt\|TestHealth_SessionsActive' ./internal/server/... -race -count=1` | ❌ W0 (`agents_test.go`) | ⬜ pending |
| 05-03-T2 | 05-03 | 3 | SESS-03 | T-05-05 | `DELETE /v1/sessions/{id}` returns `200 {"deleted":"<id>"}` for known sid; 404 for unknown; cancels in-flight prompt via Cancel→Close→map-delete-first; auth-protected | unit/integration | `go test -run 'TestDeleteSession_OK\|TestDeleteSession_Unknown\|TestDelete_CancelsInFlight\|TestDeleteSession_AuthRequired' ./internal/server/... ./internal/session/... -race -count=1` | ❌ W0 (`sessions_test.go`) | ⬜ pending |
| 05-03-T3 | 05-03 | 3 | SESS-01 | T-05-04 | Each surface handler (Ollama/OpenAI/Anthropic) reads `X-Session-Id`; routes through `engine.Run(ctx, registry.Entry(sid))` vs `engine.Run(ctx, pool)`; per-entry mutex acquired before Prompt; MarkUsed defer fires at response-complete | integration | `go test -run 'TestStatefulSessionRoutesToRegistry\|TestStatelessUsesPool\|TestSameSidReusesEntry\|TestPerSessionMutex_SerializesConcurrent' ./internal/adapter/...  ./internal/session/... -race -count=1` | ❌ W0 (per-adapter `*_session_test.go`) | ⬜ pending |
| 05-03-T4 | 05-03 | 3 | All (wiring) | T-05-06 | main.go constructs registry; reaper started before listener accepts; ordered shutdown (registry drain → pool Close); registry.Close failure logged + does not short-circuit pool.Close (per RESEARCH Q3 RESOLVED); bounded-time shutdown; goleak-clean | integration | `go test -run 'TestMain_Lifecycle\|TestMain_OrderedShutdown' ./cmd/otto-gateway/... -race -count=1 -timeout=30s` | ❌ W0 (extend existing `main_test.go`) | ⬜ pending |
| 05-03-T5 | 05-03 | 3 | SC1 (warmup pre-bind) | T-05-06 | E2E suite subtest: 10 sequential `/api/chat` requests; req #2 latency within ±25% of req #10 (CI-noise tolerance loose enough to absorb GitHub-runner jitter while still catching warmup-tax regressions) | e2e | `OTTO_E2E=1 go test -tags=e2e -run TestE2E_PoolSessions/WarmupBeforeListen ./tests/e2e/... -count=1 -timeout=60s` | ❌ W0 (`tests/e2e/pool_sessions_e2e_test.go`) | ⬜ pending |
| 05-03-T5 | 05-03 | 3 | SC2 (slot saturation) | T-05-06, T-05-07 | E2E suite subtest: 8 concurrent goroutines fire `/api/chat` against POOL_SIZE=4; `/health/agents` shows `pool.busy==4` at peak; all 8 complete with 200; no deadlock under -race | e2e | `OTTO_E2E=1 go test -tags=e2e -run TestE2E_PoolSessions/SaturationBlocking ./tests/e2e/... -count=1 -timeout=60s` | ❌ W0 | ⬜ pending |
| 05-03-T5 | 05-03 | 3 | SC3 (session affinity) | T-05-04, T-05-04-MT | E2E suite subtest: two `/api/chat` with same `X-Session-Id: e2e-sid-1` create exactly one `sessions[]` entry; subsequent request without header uses pool (no new session entry) | e2e | `OTTO_E2E=1 go test -tags=e2e -run TestE2E_PoolSessions/SessionIDAffinity ./tests/e2e/... -count=1 -timeout=60s` | ❌ W0 | ⬜ pending |
| 05-03-T5 | 05-03 | 3 | SC4 (idle reap, real time) | T-05-02, T-05-06 | E2E suite subtest: `SESSION_TTL_MS=500 SESSION_TICK_INTERVAL_MS=100`; create session via header; wait 1.5s idle; session entry removed from `/health/agents` `sessions[]` | e2e | `OTTO_E2E=1 go test -tags=e2e -run TestE2E_PoolSessions/IdleReap_RealTime ./tests/e2e/... -count=1 -timeout=60s` | ❌ W0 | ⬜ pending |
| 05-03-T5 | 05-03 | 3 | SC4 (DELETE OK) | T-05-05 | E2E suite subtest: create session via header; `DELETE /v1/sessions/<id>` returns 200 with body `{"deleted":"<id>"}` per RESEARCH Q2 RESOLVED; subsequent `/health/agents` `sessions[]` does not list the sid | e2e | `OTTO_E2E=1 go test -tags=e2e -run TestE2E_PoolSessions/DeleteSession_OK ./tests/e2e/... -count=1 -timeout=60s` | ❌ W0 | ⬜ pending |
| 05-03-T5 | 05-03 | 3 | SC4 (DELETE unknown) | T-05-05 | E2E suite subtest: `DELETE /v1/sessions/does-not-exist` returns 404 (D-08 unknown-sid semantics) | e2e | `OTTO_E2E=1 go test -tags=e2e -run TestE2E_PoolSessions/DeleteSession_Unknown ./tests/e2e/... -count=1 -timeout=60s` | ❌ W0 | ⬜ pending |
| 05-03-T5 | 05-03 | 3 | SC4 (DELETE cancels in-flight) | T-05-05, T-05-02 | E2E suite subtest: streaming `/api/chat` with `X-Session-Id: e2e-del-cancel` in flight; mid-stream `DELETE /v1/sessions/e2e-del-cancel`; streaming response terminates cleanly within ≤3s bounded timeout (D-08 in-flight cancel) | e2e | `OTTO_E2E=1 go test -tags=e2e -run TestE2E_PoolSessions/DeleteSession_CancelsInFlight ./tests/e2e/... -count=1 -timeout=60s` | ❌ W0 | ⬜ pending |
| 05-03-T5 | 05-03 | 3 | SC5 (/health/agents shape) | T-OBSV-DOS, T-05-04 | E2E suite subtest: `GET /health/agents` without auth returns 200; strict-decode body asserts D-14/D-15/D-16 keys: `pool.{size,alive,busy,slots}`, `slot.{label,alive,busy,current_session_id}`, `session.{id,alive,busy,last_used,model}` | e2e | `OTTO_E2E=1 go test -tags=e2e -run TestE2E_PoolSessions/HealthAgentsShape ./tests/e2e/... -count=1 -timeout=60s` | ❌ W0 | ⬜ pending |
| 05-03-T5 | 05-03 | 3 | SC5 (dead-slot lazy respawn) | T-05-07 | E2E suite subtest: kill one kiro-cli child via SIGKILL (PID discovered via `pgrep -P <gateway-pid> kiro-cli`); `/health/agents` shows one `alive: false`; next `/api/chat` succeeds (lazy synchronous respawn); `/health/agents` shows all 4 slots `alive: true` again with no shrink | e2e | `OTTO_E2E=1 go test -tags=e2e -run TestE2E_PoolSessions/DeadSlotLazyRespawn ./tests/e2e/... -count=1 -timeout=60s` | ❌ W0 | ⬜ pending |
| 05-03-T5 | 05-03 | 3 | SC5 (all-dead respawn fails → 503 + shrink) | T-05-07, T-05-06-SHUTDOWN | E2E suite subtest: boot with `KIRO_CMD` pointing at test-written failing stub (`tests/e2e/cmd/stub-kiro-fails/`); all 4 slots dead at warmup; next `/api/chat` returns 503; `/health/agents` `pool.slots[]` length < initial `POOL_SIZE=4` (pool shrunk per D-03) | e2e | `OTTO_E2E=1 go test -tags=e2e -run TestE2E_PoolSessions/AllDeadRespawnFails ./tests/e2e/... -count=1 -timeout=60s` | ❌ W0 | ⬜ pending |
| 05-03-T5 | 05-03 | 3 | SC1..SC5 aggregate (suite-level gate) | T-05-06, T-05-07 | Full `TestE2E_PoolSessions` suite — all 10 subtests pass under one `go test` invocation; bounded total runtime under 180s timeout; no goroutine leaks across boot/teardown cycles (`bootGateway` helper is leak-clean, proven by existing ollama/openai/anthropic e2e suites) | e2e | `OTTO_E2E=1 go test -tags=e2e -run TestE2E_PoolSessions ./tests/e2e/... -count=1 -timeout=180s` | ❌ W0 | ⬜ pending |
| 05-03-T6 | 05-03 | 3 | Manual residual (perf-vs-Node + RSS sanity) | — | Blocking human-verify checkpoint: side-by-side `wrk` p50/p99/tail vs Node implementation (`../gitlab.rosetta.ericssondevops.com/loop_24/acp_server` per CLAUDE.md) + per-session RSS capture for SESSION_MAX=8; report at `tests/e2e/reports/PHASE5-PERF.md` | manual | `kiro-cli` on PATH; Node implementation runnable; `wrk` or `ab` installed; report file present | ❌ manual | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [x] `internal/session/testmain_test.go` — `goleak.VerifyTestMain(m)` gate from day one (05-02 Task 0; mirrors `internal/pool/testmain_test.go`)
- [x] `internal/session/registry_test.go` + `reaper_test.go` — stub bodies covering SESS-01 / SESS-02 / SESS-03 race scenarios with deterministic `TTL` + `TickInterval` constructor params (05-02 Task 0)
- [x] `internal/session/export_test.go` — test-only helpers including `NewEntryForTest(client PoolClient, sid string) *Entry` so adapter tests in 05-03 Task 3 can construct Entry without going through Registry (05-02 Task 0 — addresses checker warning #9)
- [x] `internal/pool/exit_watcher_test.go` + `pool_test.go` extension — POOL-04 lazy re-spawn + goleak watcher-exit assertion (05-01 Task 2)
- [x] `internal/pool/detail_test.go` — D-15 row shape assertions (05-01 Task 3)
- [x] `internal/server/agents_test.go` — OBSV-02 `/health/agents` shape stability (05-03 Task 1)
- [x] `internal/server/sessions_test.go` — DELETE handler tests (05-03 Task 2)
- [x] Per-adapter `*_session_test.go` files under `internal/adapter/{ollama,openai,anthropic}` — surface-routing tests (05-03 Task 3)
- [x] No new framework install needed — `go.mod` already pins `go.uber.org/goleak v1.3.0` and `github.com/go-chi/chi/v5 v5.3.0`

---

## Acceptance Thresholds (from RESEARCH §Validation Architecture)

> Concrete pass/fail thresholds derived from ROADMAP success criteria SC1..SC5. Authoritative source for the 05-03 Task 5 human-verify checkpoint.

| SC | Requirement | Threshold |
|----|-------------|-----------|
| SC1 | Pool warmup pre-bind | 2nd post-startup request latency within ±10% of 10th request latency (no warmup tax on user's first real call); listener must not accept connections until `Warmup` returns. |
| SC2 | Slot saturation under concurrency | N ≤ POOL_SIZE concurrent `/api/chat` requests each get a distinct slot label; N > POOL_SIZE excess callers block on Acquire and proceed FIFO as slots free; zero deadlocks under -race. |
| SC3 | Session affinity by sid | Two requests with same `X-Session-Id` route to the same dedicated subprocess (verified via per-slot label in `/health/agents`); requests without the header use the warm pool. |
| SC4 | Idle reap + on-demand DELETE | Idle session reaped after `SESSION_TTL_MS` (verified with `TTL: 200ms, TickInterval: 50ms` — test completes <1s); `DELETE /v1/sessions/:id` cancels in-flight prompt and returns `200 {deleted: "<id>"}` within bounded time; unknown sid → 404. |
| SC5 | `/health/agents` detail + dead-slot lazy re-spawn | Endpoint returns per-slot (`label`, `alive`, `busy`, `current_session_id`) and per-session (`id`, `alive`, `busy`, `last_used`, `model`) detail; killed slot detected push-side (D-01) and lazy re-spawned at next Acquire without blocking other Acquires; when ALL slots are simultaneously dead and respawn FAILS, the next request returns 503 per D-03 and `Pool.Detail()` shows the shrink in its next snapshot. |

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Real kiro-cli RSS per session | D-06 (SESSION_MAX heuristic validation) | Process RSS is platform- and binary-version-dependent; not deterministic in unit tests. | **05-03 Task 6 Step 2.** Run gateway with `POOL_SIZE=4 SESSION_MAX=8`, spawn 8 sessions via curl loop, observe `/health/agents` + `ps -o pid,rss` to validate 32 default is conservative. Result captured in `tests/e2e/reports/PHASE5-PERF.md`. |
| Perf delta vs Node implementation | CLAUDE.md ("must not be slower than Node under concurrent load. Tail latency should improve.") | Side-by-side load comparison against a different runtime (Node + V8) on the same host; not a hermetic unit-test concern — depends on Node deps, system load, and tooling (`wrk`/`ab`) availability. | **05-03 Task 6 Step 1.** Boot Node implementation at `:11434`, run `wrk -t4 -c8 -d30s` against `/api/chat`, capture p50/p99/tail. Repeat with `bin/otto-gateway` at same port. Pass criterion: p99(Go) ≤ p99(Node) + 10%. Result captured in `tests/e2e/reports/PHASE5-PERF.md`. |

---

## Validation Sign-Off

- [x] All tasks have `<automated>` verify or Wave 0 dependencies (Task 5 is the documented exception — manual gate)
- [x] Sampling continuity: no 3 consecutive tasks without automated verify (Task 5 is terminal; preceded by 4 auto tasks in 05-03)
- [x] Wave 0 covers all MISSING references
- [x] No watch-mode flags
- [x] Feedback latency < 60s
- [x] `nyquist_compliant: true` set in frontmatter
- [x] `goleak` gate present in `internal/session/testmain_test.go` before any non-stub session code lands (05-02 Task 0 is Wave 2 prereq for Tasks 1/2)
- [x] Open Questions in RESEARCH.md marked RESOLVED (Q1–Q4 resolved 2026-05-26)

**Approval:** approved 2026-05-26
