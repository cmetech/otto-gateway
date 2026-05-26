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
| 05-03-T5 | 05-03 | 3 | SC1..SC5 against real kiro-cli | T-05-07 | Blocking human-verify checkpoint: 21-step reproducible script exercises SC1 warmup, SC2 saturation, SC3 affinity, SC4 reap + DELETE, SC5 /health/agents detail + dead-slot lazy respawn against a live kiro-cli binary | manual | `kiro-cli` on PATH; script lives in 05-03 Task 5 acceptance | ❌ manual | ⬜ pending |

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
| Real kiro-cli RSS per session | D-06 (SESSION_MAX heuristic validation) | Process RSS is platform- and binary-version-dependent; not deterministic in unit tests. | Run gateway with `POOL_SIZE=4 SESSION_MAX=8`, spawn 8 sessions via Pi SDK, observe `/health/agents` + system RSS to validate 32 default is conservative. |
| SC1..SC5 against real kiro-cli | POOL-01..04 + SESS-01..03 + OBSV-02 | Live ACP wire interaction with a real binary not deterministically reproducible at sub-second cadence in unit tests; race-with-real-network would be flaky. | 05-03 Task 5 — 21-step blocking checkpoint with env-var setup + exact curl commands + expected outputs. |

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
