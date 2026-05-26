---
phase: 05
slug: pool-stateful-sessions
status: verified
threats_open: 0
threats_total: 23
threats_closed: 23
asvs_level: 1
created: 2026-05-26
audited: 2026-05-26
---

# Phase 05 — Security

> Per-phase security contract: threat register aggregated from plans
> 05-01..05-04 `<threat_model>` blocks, verified by gsd-security-auditor
> against the implementation. Plan 05-05 contributed no new threats
> (perf-only documentation plan).

---

## Trust Boundaries

| Boundary | Description | Data Crossing |
|----------|-------------|---------------|
| HTTP surface → gateway router | `/v1/*` and `/api/*` require Bearer token (`AUTH_TOKEN` env); `/health` and `/health/agents` are auth-exempt by design (D-18). | Bearer token, optional `X-Session-Id` header, JSON request bodies |
| Gateway → `kiro-cli` subprocess | Gateway owns subprocess lifecycle; `kiro-cli` is the untrusted half of the trust boundary. Spawned via `os/exec.CommandContext` with operator-controlled env (`KIRO_CMD`, `KIRO_ARGS`, `KIRO_CWD`). | JSON-RPC over stdio framed by `internal/acp` |
| Pool / registry → exit-watcher and reaper goroutines | In-process concurrency boundary. Trust property is leak control (goleak gate) and deadlock avoidance (TryLock + snapshot-then-iterate), not data integrity. | Goroutine lifecycle signals (`Done()` channel, `closing` channel, `wg.Wait`) |
| `cmd/otto-gateway` → `internal/config` env vars | Operator-controlled env: `POOL_SIZE`, `SESSION_TTL_MS`, `SESSION_TICK_INTERVAL_MS`, `SESSION_MAX`, `AUTH_TOKEN`, `ALLOWED_IPS`, `KIRO_*`. Phase 5 inherits Phase 1 trust posture for these. | Strings / integers parsed via `getEnvInt` with safe defaults |
| Operator-only: kiro-shim tool → repo internals | `tools/kiro-shim/main.go` is a diagnostic stdio tee. It MUST NOT import any `internal/*` package so it cannot inject into the runtime via shared types. arch-lint and grep gate verify. | None — pure pass-through stdio recorder |

---

## Threat Register

Threat IDs are inherited verbatim from plan `<threat_model>` blocks. `T-05-02` and `T-05-04` were referenced by multiple plans; they are listed once each with all referenced components. All 23 threats resolve to `closed`.

| Threat ID | Category | Component | Disposition | Mitigation / Evidence | Status |
|-----------|----------|-----------|-------------|-----------------------|--------|
| T-05-06-A | Denial-of-Service (goroutine leak) | `internal/pool/exit_watcher.go` | mitigate | Per-slot watcher select on `<-done` vs `<-p.closing`; goleak gate in `internal/pool/testmain_test.go`; `TestExitWatcher_ExitsOnPoolClose` + `TestPool_ExitWatcher_RespawnSpawnsNewWatcher` | closed |
| T-05-07 | Denial-of-Service (subprocess re-spawn under load) | `internal/pool/pool.go:respawnSlot` | accept | Synchronous re-spawn — one caller pays warmup latency. Pool.Detail() exposes shrink visibility via `/health/agents`. Documented in 05-01-SUMMARY. | closed |
| T-05-DOS-RESPAWN-FAILURE | Denial-of-Service (respawn failure) | `internal/pool/pool.go` D-03 path (removeSlot) | mitigate | Re-spawn failure drops slot from `p.all` and returns wrapped typed error for 503 rendering. `TestPool_DeadSlot_RespawnFailure_PoolShrinks`. | closed |
| T-POOL-CTX-IGNORE | Denial-of-Service (ctx-cancelled caller blocked) | `internal/pool/pool.go:respawnSlot` | mitigate | `respawnSlot(ctx, slot)` honors ctx through Factory.Spawn. `TestPool_DeadSlot_RespawnRespectsCtxCancel`. | closed |
| T-ACP-DONE-SC | Tampering (supply-chain) | `internal/acp/client.go:Done()` | accept | One-line accessor over existing private `clientCtx`. Zero new dependencies. Documented in 05-01-SUMMARY. | closed |
| T-05-01 | Denial-of-Service (resource exhaustion) | `internal/session/registry.go:Get` | mitigate | D-06 SESSION_MAX cap (default 32). `ErrSessionMaxExceeded` raised at admission time; surface adapters render 503. `TestRegistry_Get_SessionMaxExceeded`. | closed |
| T-05-02 | Denial-of-Service (deadlock — reaper vs handlers) | `internal/session/reaper.go` + `Registry.Delete` + adapter handlers | mitigate | D-12 TryLock + skip-in-flight at reaper. Snapshot-then-iterate (release `r.mu` before `entry.Mu`). Delete map-delete-first. Adapter handlers `Lock` + defer `Unlock` + defer `MarkUsed` in correct LIFO order (`MarkUsed` runs under lock, then `Unlock`) — code-review fix CR-01 applied in commit `d48b461`. `TestReaper_SkipsInFlightSession`, `TestReaper_DeadlockFree_ReverseLockOrder`, e2e `Reaper_DoesNotReapActiveSession`. | closed |
| T-05-03 | Repudiation (Pitfall-4 lazy-create double-spawn) | `internal/session/registry.go:Get` | mitigate | Creating sentinel + ready chan; racing same-sid callers wait on `entry.ready`. Unit `TestRegistry_Get_RacingSameSid_NoDoubleSpawn`. E2E `ConcurrentSameSid_OneSession` (6 concurrent → exactly 1 registry entry). | closed |
| T-05-04 | Information Disclosure / Tampering (X-Session-Id is client-supplied) | `internal/session/registry.go:Get` + `internal/server/agents.go` (auth-exempt) | accept | Single-tenant deployment assumption inherited from Phase 1/2. Multi-tenant operators MUST set `AUTH_TOKEN` (Phase 2 OBSV-01 gates `/api` + `/v1`); `/health/agents` is intentionally auth-exempt per D-17/D-18. Documented across 05-02-PLAN, 05-03-PLAN, 05-03-SUMMARY threat sections. | closed |
| T-05-04-MT | Information Disclosure (multi-tenant session hijack) | `internal/adapter/*/handlers.go` | mitigate (docs-only) | Same as T-05-04. ASVS-L1 single-tenant deployment is the documented default. Multi-tenant requires an upstream reverse-proxy auth gate; documented in 05-03 plan body. | closed |
| T-05-05 | Tampering / IDOR (DELETE any sid) | `internal/server/sessions_delete.go` | accept | Any `AUTH_TOKEN` holder may DELETE any sid. Consistent with single-tenant assumption and Phase 2's existing auth posture (no per-user identity). Severity LOW. Documented in 05-03-PLAN. | closed |
| T-05-04-CHILDREN | Tampering (subprocess control via env-var injection) | `cmd/otto-gateway/main.go` → `session.New` / `pool.New` | accept | `KIRO_CMD`, `KIRO_ARGS`, `KIRO_CWD` are operator-controlled env vars at the Phase 1 trust boundary. Subprocess spawn at `internal/acp/client.go:284` uses `os/exec.CommandContext`. `gosec G204` annotation in place; gate active per CLAUDE.md. Phase 5 introduces no new tainted-input flow into the exec call. | closed |
| T-OBSV-DOS | Denial-of-Service (large `/health/agents` response) | `internal/server/agents.go` | accept | Bounded by `SESSION_MAX=32` + `POOL_SIZE=4` → ~6 KB max response. No DoS concern. Documented in 05-03-PLAN. | closed |
| T-05-06 | Denial-of-Service (reaper goroutine leak — Pitfall-5) | `internal/session/reaper.go` + `Registry.Close` | mitigate | `close(r.closing); r.wg.Wait()` bounded by `TickInterval`. goleak gate in `internal/session/testmain_test.go`. `TestReaper_ExitsOnRegistryClose`. | closed |
| T-05-06-SHUTDOWN | Denial-of-Service (slow shutdown if registry hangs) | `cmd/otto-gateway/main.go` cleanup | mitigate | Registry.Close FIRST, pool.Close SECOND (verified by `TestNewApp_CleanupOrdersRegistryBeforePool`). `TestNewApp_RegistryCloseIsBoundedTime` asserts <2 s with `TickInterval=100ms`. WR-06 nil-guard fix in commit `73c0401` defends against partial-init shutdown. | closed |
| T-05-08 | Tampering (per-entry mutex misuse) | `internal/session/registry.go:Entry.Mu` + adapter handlers | mitigate | All three adapter handlers (`ollama`, `openai`, `anthropic`) take `entry.Mu.Lock()` + defer `Unlock` + defer `MarkUsed`. Reaper uses `TryLock` so it never blocks an active handler. Verified live by e2e `Reaper_DoesNotReapActiveSession` and `Pool_Session_Coexistence_UnderLoad`. | closed |
| T-05-CTX-LEAK | Information Disclosure (kiro-cli orphan on lazy-create failure) | `internal/session/registry.go:Get` error path | mitigate | Best-effort `_ = client.Close()` on every `createEntry` error path. No orphaned kiro-cli children observed in `tests/e2e/pool_sessions_e2e_test.go` smoke runs. | closed |
| T-05-04-01 | Information Disclosure (wire transcripts contain prompt content) | `.planning/phases/.../*-WIRE-*.jsonl` files | accept | Synthetic input only (`"hi"`, `"Remember the number 7."`); no PII; no credentials in body. Files live under `.planning/` (project-internal). Documented in 05-04-PLAN. | closed |
| T-05-04-02 | Tampering (kiro-shim) | `tools/kiro-shim/main.go` | mitigate | Source committed and reviewable; `grep "internal/" tools/kiro-shim/main.go` returns zero import matches (comment-string matches only). Audit header at line 73 (`# kiro-shim invocation:`) makes the resolved command line traceable per recording. arch-lint excludes `tools/` from internal-import gates by design. | closed |
| T-05-04-03 | Denial-of-Service (per-request NewSession RPC overhead) | `internal/session/entry_acp.go:Prompt` | mitigate | `Entry.NewSession` returns cached `e.SessionID` with **zero** Client RPC — the H-A reverse-regression guard `TestEntry_NewSession_ReturnsCachedSessionID` confirms. Per-request overhead is unchanged from pre-fix path. | closed |
| T-05-04-04 | Repudiation (source comment citing WIRE-DIFF.md) | `internal/session/entry_acp.go:26` + `internal/session/registry.go:224` | accept | Comments cite `.planning/phases/05-pool-stateful-sessions/05-04-WIRE-DIFF.md`; artifact committed; git history is the audit trail. | closed |

*Status: open · closed*
*Disposition: mitigate (implementation required) · accept (documented risk) · transfer (third-party)*

---

## Accepted Risks Log

| Risk ID | Threat Ref | Rationale | Accepted By | Date |
|---------|------------|-----------|-------------|------|
| AR-05-01 | T-05-07 | Synchronous re-spawn pattern: one caller pays warmup latency on first request to a dead slot. Pool shrink is loud (surfaced in `/health/agents`), preferable to silent capacity loss. | Phase author (planning) | 2026-05-26 |
| AR-05-02 | T-ACP-DONE-SC | One-line `Done()` accessor over an existing private field adds zero supply-chain surface beyond Phase 1. | Phase author (planning) | 2026-05-26 |
| AR-05-03 | T-05-04 + T-05-04-MT | Single-tenant deployment is the documented default for the gateway, consistent with Phase 1/2 posture. Multi-tenant operators are responsible for an upstream auth gate. `/health/agents` auth-exempt is a deliberate ops-affordance per D-18 (auth-token rotation must not lock out observability). | Phase author (planning) | 2026-05-26 |
| AR-05-04 | T-05-05 | DELETE-any-sid: same single-tenant rationale as AR-05-03. Any `AUTH_TOKEN` holder may delete any session. Severity LOW. | Phase author (planning) | 2026-05-26 |
| AR-05-05 | T-05-04-CHILDREN | `KIRO_*` env-var-driven subprocess spawn is the Phase 1 trust boundary. `gosec G204` annotation is in place at the spawn site; Phase 5 added no new tainted-input flow. | Phase author (planning) | 2026-05-26 |
| AR-05-06 | T-OBSV-DOS | `/health/agents` response size is bounded by `SESSION_MAX * row_size + POOL_SIZE * row_size` ≈ 6 KB at defaults. Not a DoS surface. | Phase author (planning) | 2026-05-26 |
| AR-05-07 | T-05-04-01 | Wire-trace JSONL files captured during Plan 05-04's root-cause diagnosis contain only synthetic prompt content. No PII, no credentials. Files live under `.planning/`. | Phase author (planning) | 2026-05-26 |
| AR-05-08 | T-05-04-04 | Source-comment-to-artifact references are auditable through git history; the artifact itself documents the confirmatory experiment so the root cause is reproducible. | Phase author (planning) | 2026-05-26 |

---

## Security Audit Trail

| Audit Date | Threats Total | Closed | Open | Run By |
|------------|---------------|--------|------|--------|
| 2026-05-26 | 23 | 23 | 0 | gsd-security-auditor (Claude) |

### Audit notes — 2026-05-26

- Register aggregated from `<threat_model>` blocks across plans 05-01, 05-02, 05-03, 05-04. Plan 05-05 (perf documentation) contributed no threats.
- `T-05-02` and `T-05-04` appear in multiple plans with slightly different framings; deduplicated into one register row each, with all referenced components listed.
- All `mitigate` dispositions verified against the implementation: cited code locations present, cited tests exist and are green (12 unit packages + 15 e2e subtests passing under the v1.5 build).
- All `accept` dispositions documented in the corresponding PLAN, SUMMARY, or in this file's Accepted Risks Log.
- CR-01 (defer LIFO ordering for `MarkUsed` vs `Unlock`) was the load-bearing concern for `T-05-02`. The fix landed in commit `d48b461` and the current code at all three adapter sites uses the correct order (`MarkUsed` runs under lock, then `Unlock`). T-05-02 mitigation is intact.
- No new attack surface discovered during the audit that lacks a threat mapping in the register.

---

## Sign-Off

- [x] All threats have a disposition (mitigate / accept / transfer)
- [x] Accepted risks documented in Accepted Risks Log
- [x] `threats_open: 0` confirmed
- [x] `status: verified` set in frontmatter

**Approval:** verified 2026-05-26 (auto-audited by gsd-security-auditor)
