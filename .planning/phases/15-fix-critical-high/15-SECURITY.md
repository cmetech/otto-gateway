---
phase: 15
slug: fix-critical-high
status: verified
threats_total: 10
threats_open: 0
threats_closed: 10
asvs_level: 1
register_authored_at_plan_time: true
created: 2026-06-11
---

# Phase 15 — Security

> Per-phase security contract: threat register, accepted risks, and audit trail. Phase 15 shipped 1 Critical + 8 High reliability fixes (CR-01/02 + WR-01..08 review-fix round) across pool, ACP, HTTP adapters, admin SSE, and tray subsystems. Threat register authored at plan time across 3 PLAN files; this audit verifies each declared mitigation is present in the implementation.

---

## Trust Boundaries

| Boundary | Description | Data Crossing |
|----------|-------------|---------------|
| operator env -> pool config | `POOL_ACQUIRE_TIMEOUT_MS` parsed at boot by `applyDefaults`; 0 maps to 30s default | integer (milliseconds) |
| pidfile -> shutdown signal | `shutdownCh` is an internal channel closed by `RegisterOnShutdown`; no external input | internal signal only |
| Subprocess spawn (`pool.respawnSlot`) | Existing gosec G204 baseline; Phase 15 fixes do not add new spawn call sites | static argv only |
| kiro-cli stdout -> SSE/NDJSON frame writer | Terminal error frames built from static strings + internal session UUID | static error envelope |
| `rerr` chain -> client error message | Static `"upstream_disconnect: worker terminated mid-stream"` written; `rerr.Error()` is never echoed | static error string |
| pidfile -> PID -> process-name query | T-1 reads PID from local file, converts via `strconv.Itoa`, calls read-only `ps -p` / `QueryFullProcessImageName` | integer PID, OS response |
| wrapper stdout -> bundle path parsing | Bundle assembly reads status object; never `Invoke-Expression` on stdout | object fields only |
| systray API -> tray event goroutine | `systray.SetIcon` / `SetTooltip` dispatched internally by energye/systray@v1.0.3 | UI calls only |

---

## Threat Register

| Threat ID | Category | Component | Disposition | Mitigation | Status |
|-----------|----------|-----------|-------------|------------|--------|
| T-15-01 | Denial of Service | `pool.NewSession` acquire select | mitigate | `ErrPoolExhausted` sentinel + bounded acquire select; per-surface 503 with `Retry-After: 5`. Evidence: `internal/pool/pool.go:19,678` (sentinel decl + timeoutC return); `internal/pool/config.go:111-139` (AcquireTimeout field + env parse); `internal/adapter/openai/sse.go:875`, `internal/adapter/ollama/ndjson.go:849`, `internal/adapter/anthropic/sse.go:1058` (Retry-After: 5). Commit: 4fd879b. | closed |
| T-15-02 | Tampering | `awaitPromptResult` `activeStream` pointer | mitigate | Identity-guarded nil assignment under `streamMu` prevents stale goroutine from clobbering newer session's stream. Evidence: `internal/acp/client.go:876,906` (`if c.activeStream == stream`). Commit: fcf9f3c. | closed |
| T-15-03 | Denial of Service | admin `sseLoop` blocking on shutdown | mitigate | `shutdownCh` parameter + select arm in sseLoop; `RegisterOnShutdown` closes channel; 2-signal SIGINT in `RunUntilSignal`. Evidence: `internal/admin/sse.go:179,195` (param + select); `internal/server/server.go:180,207,433` (field + init + close); `cmd/otto-gateway/main.go` explicit `cleanup()` before `os.Exit(1)`. Commits: 4f33e89, 57ecbdd (WR-05 tightens shutdown budget to 250ms), 0d8ca72 (WR-08 srv.Close force-close). | closed |
| T-15-04 | Information Disclosure | `finalizeSSE` / `finalizeNDJSON` error frame on mid-stream death | accept | Error message body is static literal `"upstream_disconnect: worker terminated mid-stream"` — `rerr.Error()` is never written to the client. WARN log captures internal `err` server-side only. Evidence: `internal/adapter/openai/sse.go:586` (static fprintf); `internal/adapter/ollama/ndjson.go:597,603` (static `frame.Error`). Accepted-risks log entry below. Commit: 3db0ae8. | closed |
| T-15-05 | Denial of Service | ACP Cancel on OpenAI idle-timeout path | mitigate | Removed `StopWatchdog()` from `idleC` arm and `applyChunk` write-error arm so AfterFunc-driven ACP Cancel fires naturally on handler return; no slot stuck in indeterminate state. Evidence: `internal/adapter/openai/sse.go:462-469,489` (H-2 fix comments + removed calls); only success-path `StopWatchdog` calls remain at `:449,562,602`. Commit: a8df54e. | closed |
| T-15-06 | Spoofing | tray + wrappers PID identity check | mitigate | `verifyGatewayIdentity` defined in both pidfile_darwin.go (ps comm=) and pidfile_windows.go (QueryFullProcessImageName); wired into `makeProbe`; bash + ps1 wrappers check before kill. WR-06 tightened darwin check from `HasSuffix` to exact-or-`/otto-gateway` suffix to block `fake-otto-gateway` planting. Evidence: `cmd/otto-tray/pidfile_darwin.go:42,50,55`; `cmd/otto-tray/pidfile_windows.go:43`; `cmd/otto-tray/tray.go:147`; `scripts/otto-gw:780-782` (`actual_comm` check); `scripts/otto-gw.ps1:582` (`MainModule.FileName`). Commits: 41b0f0a, 8694f9a (WR-06). | closed |
| T-15-07 | Tampering | `Get-GatewayStatus` return value | accept | Return is a local `[pscustomobject]` constructed from local OS state (PID file, running process); no external input reaches it. Evidence: 5 `pscustomobject` occurrences in `scripts/otto-gw.ps1`. Accepted-risks log entry below. Commit: e161e4e. | closed |
| T-15-08 | Elevation of Privilege | T-1 `exec.CommandContext("ps", ...)` | mitigate | Args are static literal strings + `strconv.Itoa(int)` — no tainted operator input. `//nolint:gosec` annotation with rationale comment applied per gosec G204 pattern. Evidence: `cmd/otto-tray/pidfile_darwin.go:45` (`//nolint:gosec // args are static+integer`). Commit: 41b0f0a. | closed |
| T-15-SC (supply chain — pool subprocess spawn) | Tampering | subprocess spawn in pool | accept | No new spawn call sites added in Phase 15; existing gosec G204 baseline covers `pool.respawnSlot` unchanged. Re-queue logic preserves call-site invariants. Accepted-risks log entry below. | closed |
| T-15-SC (supply chain — npm/pip/cargo) | Tampering | external package installs | accept | No new packages installed in Phase 15; only stdlib (`os/exec`, `errors`) added on H-3 fix. Accepted-risks log entry below. | closed |

*Status: open · closed*
*Disposition: mitigate (implementation required) · accept (documented risk) · transfer (third-party)*

---

## Accepted Risks Log

| Risk ID | Threat Ref | Rationale | Accepted By | Date |
|---------|------------|-----------|-------------|------|
| AR-15-01 | T-15-04 | Static error string `"upstream_disconnect: worker terminated mid-stream"` is the only client-facing message on mid-stream worker death. Internal `rerr` and `kiro_exit_code` are captured at WARN log server-side; no kiro-cli stdout/stderr is echoed to the wire. Risk of internal-state disclosure to remote client is closed by construction. | Phase 15 plan author (15-02-PLAN.md threat_model block) | 2026-06-11 |
| AR-15-02 | T-15-07 | `Get-GatewayStatus` builds its return object from local OS calls (`Get-Content $PidFile`, `Get-Process`); no caller-controlled input is interpolated into the object. Tampering risk requires local filesystem write access to `$PidFile`, which is already a trusted-write boundary. | Phase 15 plan author (15-03-PLAN.md threat_model block) | 2026-06-11 |
| AR-15-03 | T-15-SC (pool subprocess) | Phase 15 modified pool respawn / shutdown logic but did not introduce new `exec.Command` / `cmd.Run` call sites. The lone new exec is the read-only `ps -p` identity check (T-15-08) which is mitigated, not accepted. Existing gosec G204 baseline on `pool.respawnSlot` remains in force. | Phase 15 plan author (15-01-PLAN.md, 15-03-PLAN.md threat_model blocks) | 2026-06-11 |
| AR-15-04 | T-15-SC (package installs) | Phase 15 added zero third-party Go modules. The two new stdlib imports (`os/exec`, `errors` in H-3 finalizers) are inert from a supply-chain perspective. RESEARCH.md package-legitimacy audit confirms clean. | Phase 15 plan author (15-02-PLAN.md threat_model block) | 2026-06-11 |

*Accepted risks do not resurface in future audit runs.*

---

## Unregistered Flags

`## Threat Flags` sections from each SUMMARY:
- 15-01-SUMMARY.md `## Threat Flags`: "None. No new network endpoints, auth paths, file access patterns, or schema changes introduced." -> no unregistered flags.
- 15-02-SUMMARY.md `## Threat Flags`: "None. Error messages are static strings ... no kiro-cli internal error detail is echoed to clients. ... T-15-04 disposition confirmed: accept." -> maps cleanly to T-15-04; no unregistered flags.
- 15-03-SUMMARY.md: no `## Threat Flags` section. Executor did not flag any new attack surface for Plan 03. The verified tray-PID identity check is the largest new surface and is already covered by T-15-06 and T-15-08.

**Net: no unregistered flags.**

---

## Security Audit Trail

| Audit Date | Threats Total | Closed | Open | Run By |
|------------|---------------|--------|------|--------|
| 2026-06-11 | 10 | 10 | 0 | gsd-secure-phase (Claude Opus 4.7) |

---

## Verification Methodology

For each `mitigate` threat, the audit ran the following grep checks against the implementation files cited in PLAN `<threat_model>` blocks:

| Threat | Grep Pattern | File | Lines Found |
|--------|-------------|------|-------------|
| T-15-01 | `ErrPoolExhausted` | `internal/pool/pool.go` | 19, 620, 624, 678 |
| T-15-01 | `AcquireTimeout` | `internal/pool/config.go` | 111-139 |
| T-15-01 | `Retry-After` | `internal/adapter/{openai/sse.go,ollama/ndjson.go,anthropic/sse.go}` | 875 / 849 / 1058 |
| T-15-02 | `c.activeStream == stream` | `internal/acp/client.go` | 876, 906 (exactly 2 sites per plan) |
| T-15-03 | `shutdownCh` | `internal/admin/sse.go` | 179, 195 |
| T-15-03 | `shutdownCh` | `internal/server/server.go` | 180, 207, 259, 274, 433, 535 |
| T-15-05 | `H-2 fix` comments + absence of StopWatchdog on idle path | `internal/adapter/openai/sse.go` | 462-469, 489 (removed); only :449, :562, :602 remain on success paths |
| T-15-06 | `verifyGatewayIdentity` | `cmd/otto-tray/{pidfile_darwin.go,pidfile_windows.go,tray.go}` | darwin:42 / windows:43 / tray:147 |
| T-15-06 | `actual_comm` | `scripts/otto-gw` | 780 |
| T-15-06 | `MainModule.FileName` | `scripts/otto-gw.ps1` | 582 |
| T-15-08 | `//nolint:gosec` | `cmd/otto-tray/pidfile_darwin.go` | 45 (with rationale comment) |

For each `accept` threat, the audit confirmed:
- T-15-04: client-facing fprintf is a static string literal (no `%v` of rerr); WARN log is server-side only.
- T-15-07: 5 `pscustomobject` occurrences in `scripts/otto-gw.ps1` — all from local OS state.
- T-15-SC (both variants): no new spawn call sites and no new third-party modules introduced in commits `4e64ac3..HEAD`.

---

## Sign-Off

- [x] All threats have a disposition (mitigate / accept / transfer)
- [x] Accepted risks documented in Accepted Risks Log (AR-15-01..04)
- [x] `threats_open: 0` confirmed
- [x] `status: verified` set in frontmatter
- [x] Register origin: authored at plan time across 15-01-PLAN.md, 15-02-PLAN.md, 15-03-PLAN.md `<threat_model>` blocks (no mid-flight additions)

**Approval:** verified 2026-06-11

---

## Commit Provenance

Phase 15 fixes verified across these commits:

| Commit | Finding | Threat Mitigated |
|--------|---------|------------------|
| 4fd879b | REL-POOL-01 (P-1) | T-15-01 |
| 4f33e89 | REL-POOL-02 + REL-HTTP-01 (P-2 + H-1) | T-15-03 |
| fcf9f3c | REL-POOL-03 (P-3) | T-15-02 |
| a8df54e | REL-HTTP-02 (H-2) | T-15-05 |
| 3db0ae8 | REL-HTTP-03 (H-3) | T-15-04 |
| 41b0f0a | REL-TRAY-01 (T-1) | T-15-06, T-15-08 |
| e161e4e | REL-TRAY-02 (T-2) | T-15-07 |
| 26d89c7 | REL-TRAY-03 (T-3) | (no new threat — UI signalling) |
| e63e7bf | WR-01: log inflight-cancel count on pool close | strengthens T-15-03 observability |
| 2893de3 | WR-01: remove unsafe `warnOnce` reassignment | hardens T-15-03 |
| 0d8ca72 | WR-08: force-close uses `srv.Close` | hardens T-15-03 |
| e6914ed | WR-07: surface unreachable admin in otto-gw.ps1 status | tray observability (T-15-07 path) |
| 8694f9a | WR-06: tighten darwin `verifyGatewayIdentity` HasSuffix check | hardens T-15-06 |
| 57ecbdd | WR-05: tighten REL-HTTP-01 shutdown budget 1s -> 250ms | hardens T-15-03 |
| fea95f8 | WR-04: tighten REL-POOL-02 per-client cancel parity | hardens T-15-03 |
| 3c5e175 | WR-03 (referenced in objective) | reliability hardening |
| ab2593f | WR-02 (referenced in objective) | reliability hardening |
| 827beba | WR-01 round (referenced in objective) | reliability hardening |
| c01b47d | WR-01 round (referenced in objective) | reliability hardening |
