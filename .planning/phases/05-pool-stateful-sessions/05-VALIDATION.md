---
phase: 5
slug: pool-stateful-sessions
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-05-26
---

# Phase 5 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test (Go 1.23+) with goleak v1.3.0 |
| **Config file** | none — packages own their `testmain_test.go` goleak gate |
| **Quick run command** | `go test ./internal/pool/... ./internal/session/... ./internal/server/... -count=1 -timeout=60s` |
| **Full suite command** | `go test ./... -count=1 -race -timeout=180s` |
| **Estimated runtime** | quick ~15s · full ~60s (-race adds ~3x) |

---

## Sampling Rate

- **After every task commit:** Run `go test ./internal/pool/... ./internal/session/... ./internal/server/... -count=1 -timeout=60s`
- **After every plan wave:** Run `go test ./... -count=1 -race -timeout=180s`
- **Before `/gsd-verify-work`:** Full suite must be green AND `go vet ./...` AND `gosec ./...` clean
- **Max feedback latency:** ~60 seconds for the quick path; ~180s with race detector for the full suite

---

## Per-Task Verification Map

> Filled in during planning. Each plan task will be added here with its REQ-ID, test type, and the exact `go test` selector or behavior assertion. See `05-RESEARCH.md` §Validation Architecture for the canonical instrument/sample/threshold contract per success criterion.

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| TBD | TBD | TBD | TBD | TBD | TBD | TBD | TBD | TBD | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] `internal/session/testmain_test.go` — `goleak.VerifyTestMain(m)` gate from day one (mirrors `internal/pool/testmain_test.go`)
- [ ] `internal/session/registry_test.go` — stubs covering SESS-01 (lazy create), SESS-02 (reaper at-rest), SESS-03 (DELETE in-flight cancel), with deterministic `TTL` + `TickInterval` constructor params
- [ ] `internal/pool/exit_watcher_test.go` — stub for POOL-04 lazy re-spawn + goleak watcher-exit assertion
- [ ] `internal/server/agents_test.go` — stub for OBSV-02 `/health/agents` shape (per-slot + per-session row shape stability)
- [ ] No new framework install needed — `go.mod` already pins `go.uber.org/goleak v1.3.0` and `github.com/go-chi/chi/v5 v5.3.0`

---

## Acceptance Thresholds (from RESEARCH §Validation Architecture)

> Concrete pass/fail thresholds derived from ROADMAP success criteria SC1..SC5.

| SC | Requirement | Threshold |
|----|-------------|-----------|
| SC1 | Pool warmup pre-bind | 2nd post-startup request latency within ±10% of 10th request latency (no warmup tax on user's first real call); listener must not accept connections until `Warmup` returns. |
| SC2 | Slot saturation under concurrency | N ≤ POOL_SIZE concurrent `/api/chat` requests each get a distinct slot label; N > POOL_SIZE excess callers block on Acquire and proceed FIFO as slots free; zero deadlocks under -race. |
| SC3 | Session affinity by sid | Two requests with same `X-Session-Id` route to the same dedicated subprocess (verified via per-slot label in `/health/agents`); requests without the header use the warm pool. |
| SC4 | Idle reap + on-demand DELETE | Idle session reaped after `SESSION_TTL_MS` (verified with `TTL: 200ms, TickInterval: 50ms` — test completes <1s); `DELETE /v1/sessions/:id` cancels in-flight prompt and returns `200 {deleted: "<id>"}` within bounded time; unknown sid → 404. |
| SC5 | `/health/agents` detail + dead-slot lazy re-spawn | Endpoint returns per-slot (`label`, `alive`, `busy`, `current_session_id`) and per-session (`id`, `alive`, `busy`, `last_used`, `model`) detail; killed slot detected push-side (D-01) and lazy re-spawned at next Acquire without blocking other Acquires; pool shrink on re-spawn failure visible in next snapshot. |

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Real kiro-cli RSS per session | D-06 (SESSION_MAX heuristic validation) | Process RSS is platform- and binary-version-dependent; not deterministic in unit tests. | Run gateway with `POOL_SIZE=4 SESSION_MAX=8`, spawn 8 sessions via Pi SDK, observe `/health/agents` + system RSS to validate 32 default is conservative. |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 60s
- [ ] `nyquist_compliant: true` set in frontmatter
- [ ] `goleak` gate present in `internal/session/testmain_test.go` before any non-stub session code lands

**Approval:** pending
