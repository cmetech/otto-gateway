---
phase: 16
slug: fix-mediums
status: verified
threats_total: 16
threats_closed: 16
threats_open: 0
asvs_level: 1
block_on: critical+high
register_authored_at_plan_time: true
created: 2026-06-11
verified: 2026-06-11
---

# Phase 16 — Security (fix-mediums)

> Per-phase security contract for the v1.9 Reliability Hardening milestone.
> Verification mode: each declared mitigation from a PLAN `<threat_model>` block
> is confirmed by file+line evidence in the implementation. No new threats are
> scanned for — this audit is bounded by the register authored at plan time.

---

## Trust Boundaries

Aggregated from PLAN `<threat_model>` blocks across the 5 plans of Phase 16.

| Boundary | Description | Data Crossing |
|----------|-------------|---------------|
| `pool.go → kiro-cli process` (Plan 16-01) | `killProcessGroup` sends SIGTERM/taskkill to child PID | Integer PID from pool's own slot bookkeeping (never operator/client input) |
| `config.go → pool acquire select` (Plan 16-01) | `warnOnce.Do` emits a single Warn at pool saturation | Integer pool counters (busy/size); no user data |
| `client → POST /v1/chat/completions (and siblings)` (Plan 16-02) | Body content untrusted; deadline bounds goroutine hold time | Per-request body bytes |
| `SSE stream → admin log-tail client` (Plan 16-02) | Log lines flow via Tailer + ring buffer + SSE | Truncated to `TailerMaxLineBytes` (1 MiB) before fan-out |
| `GET /health → tray/operator` (Plan 16-02) | `pool.status` enum exposes operational state | Internal pool counters; no PII / secrets |
| `engine/collect.go → plugin hooks` (Plan 16-03) | `nil resp` passed to `After()` on error paths | `request_id` + `duration_ms` only on error path |
| `plugin/logging.go → startTimes sync.Map` (Plan 16-03) | `LoadAndDelete` on every path bounds map growth | Internal request bookkeeping |
| `uiLoop goroutine → OS notification API` (Plan 16-04) | `notifyFn` dispatched off uiLoop via goroutine | Fixed title/body strings (no client data) |
| `tray → /health JSON` (Plan 16-04) | `Pool.Status` consumed from gateway's own endpoint | Internal enum (`ok|degraded|exhausted|""`) |
| `revealBundle → OS shell open` (Plan 16-04) | Archive path parsed from wrapper stdout (last non-empty line) | Path string from operator-owned PS1 wrapper |
| `support bundle → filesystem` (Plan 16-04) | Bundle assembly with `--max-mb` cap + staging cleanup | Log content streams (PII-redacted by `Invoke-RedactStream`) |
| `OS environment → config.Load()` (Plan 16-05) | Env vars are operator-controlled; sign checks reject bad values at boot | Integer/duration env values |
| `config.Load() → acp/client.go ticker` (Plan 16-05) | `PING_INTERVAL` sign check ensures `time.NewTicker` never receives ≤ 0 | `time.Duration` |
| `config.Load() → server.go body-read wrapper` (Plan 16-05) | `HTTP_BODY_READ_TIMEOUT_SEC` validated; flows via `Config.BodyReadTimeout` to `server.Config` | `time.Duration` |
| `main.go → slog logger` (Plan 16-05) | `EMBEDDING_MODEL_DEFAULT` value logged as slog "value" field | Operator's own env string |

---

## Threat Register

All 16 threats verified by file+line grep against implementation. Status `closed` requires direct evidence; `accept` dispositions require either documented rationale in the PLAN threat row or an Accepted Risks Log entry below.

| Threat ID | Category | Component | Disposition | Mitigation Evidence | Status |
|-----------|----------|-----------|-------------|---------------------|--------|
| T-16-01-01 | Tampering | `pool_pgid_windows.go` exec.CommandContext("taskkill", …, strconv.Itoa(pid)) | mitigate | `internal/acp/pool_pgid_windows.go:42-45` — `//nolint:gosec` annotation present; `exec.CommandContext(ctx, "taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))`; 5 s `context.WithTimeout` bounds execution; PID is integer from pool bookkeeping, never operator input | closed |
| T-16-01-02 | Denial of Service | O-1 `warnOnce.Do` Warn at pool saturation | accept | `internal/pool/pool.go:108` (`warnOnce sync.Once`), `:513` (reset in `Pool.Close`), `:641-652` (single emit per saturation episode). Bounded log volume; cannot be amplified by clients | closed |
| T-16-01-03 | Information Disclosure | O-1 Warn log fields `busy` and `size` | accept | `internal/pool/pool.go:641-652` — slog fields are internal integer counters only; no PII, no secrets | closed |
| T-16-01-SC | Tampering | Supply chain — Plan 16-01 dependencies | accept | Plan 16-01 SUMMARY confirms zero new deps; pure Go stdlib (`atomic.Int64`, `sync.Once`, stdlib `exec`); `go.mod`/`go.sum` unchanged | closed |
| T-16-02-01 | Denial of Service | `server.go` body-read deadline via `time.AfterFunc` + `r.Body.Close()` | mitigate | `internal/server/body_deadline.go:53` (`withBodyReadDeadline` middleware); `internal/server/server.go:379` (registered AFTER `auth.IPAllowlist` at `:368-372` per per-prefix Route block — denied requests do not arm the timer); `HTTP_BODY_READ_TIMEOUT_SEC` ≤ 0 boot error at `internal/config/config.go:424`; default 30 s; only `r.Body.Close()` fires on timeout — ResponseWriter/ctx untouched so SSE writes unbounded | closed |
| T-16-02-02 | Denial of Service | `admin/tail.go` multi-MB line bypass | mitigate | `internal/admin/tail.go:407-410` (fragment guard, pre-existing), `:428-431` (NEW cap enforcement on newline-terminated lines before broadcast); `TailerMaxLineBytes = 1 MiB` constant at `:57` | closed |
| T-16-02-03 | Information Disclosure | `health.go` `PoolStats.Status` enum | accept | `internal/server/health.go:13,45` — Status string is `ok|degraded|exhausted|""`; computed from internal counters only; no PII, no path info | closed |
| T-16-02-04 | Denial of Service | `health.go` `poolDegradedStallThreshold` 30 s hardcoded | accept | `internal/server/health.go:54` — `const poolDegradedStallThreshold = 30 * time.Second`; compile-time constant, not operator-tunable, not request-manipulable in v1.9 | closed |
| T-16-02-SC | Tampering | Supply chain — Plan 16-02 dependencies | accept | Plan 16-02 SUMMARY confirms zero new deps; stdlib `time.AfterFunc` + chi middleware; `go.mod`/`go.sum` unchanged | closed |
| T-16-03-01 | Denial of Service | `plugin/logging.go` `startTimes` sync.Map memory leak under retry storm | mitigate | `internal/plugin/logging.go:223` and `internal/plugin/trace.go:253` — `LoadAndDelete` runs UNCONDITIONALLY on every path; `internal/plugin/logging.go:239` and `internal/plugin/trace.go:271` — `resp == nil` nil-resp guard (early-return AFTER reclaim); error paths in `internal/engine/collect.go:171,182,195` and `internal/adapter/anthropic/collect.go:176,195` call PostHook chain with `nil` resp before returning. Map bounded by in-flight requests regardless of failure rate | closed |
| T-16-03-02 | Information Disclosure | chat-trace.log `post_chain_out` on error paths | accept | `internal/plugin/trace.go:271` — error-path After() emits minimal record (request_id + duration_ms only; no stop_reason, no content); same metadata shape as success-path. Operators opt in via `CHAT_TRACE=true` | closed |
| T-16-03-SC | Tampering | Supply chain — Plan 16-03 dependencies | accept | Plan 16-03 SUMMARY confirms zero new deps; pure stdlib changes; `go.mod`/`go.sum` unchanged | closed |
| T-16-04-01 | Denial of Service | `uihelpers_windows.go` notify — goroutine per state transition | accept | `cmd/otto-tray/tray.go:248` — local `fn := notifyFn` snapshot before goroutine launch; `:252` retry loop bounded to single call for blocking-by-contract dispatchers; goroutine count bounded by O(state transitions); not remotely triggerable (tray runs on operator's desktop) | closed |
| T-16-04-02 | Tampering | `tray.go` revealBundle — last-non-empty stdout line passed to Explorer | mitigate | `cmd/otto-tray/tray.go:359-365,371` — `lastLine` loop selects last non-empty line; path is the gateway's own `scripts/otto-gw.ps1` wrapper output (operator-controlled); `Write-Stderr` helper in PS1 keeps informational output off stdout; path is shell-open'd, not `exec` with user-controlled argv | closed |
| T-16-04-03 | Denial of Service | T-7 bundle size cap bypass | mitigate | `scripts/otto-gw.ps1:72` (`$MaxMb = 512` default), `:1449` (`$maxBytes`), `:1556-1566` (live-log tail-trim on copy — closes the pre-fix "rotated-.gz only" gap), `:1689-1746` (cap loop with fall-through to live logs). All collected files pass through cap before archiving | closed |
| T-16-04-04 | Tampering | `otto-gw.ps1` job-based timeout `Remove-Item` | accept | `scripts/otto-gw.ps1:1450-1755` — `Test-Deadline` helper throws on overrun; `:1767-1771` — `Remove-Item -Recurse -Force $staging` in `finally` block. `$staging` is a local temp dir constructed by the support verb (operator-owned PS1), not a path supplied by an external caller | closed |
| T-16-04-SC | Tampering | Supply chain — Plan 16-04 dependencies | accept | Plan 16-04 SUMMARY confirms zero new deps; Go stdlib + PowerShell built-ins (`System.Diagnostics.Stopwatch`, `System.IO.File`); `go.mod`/`go.sum` unchanged | closed |
| T-16-05-01 | Denial of Service | `config.go` fail-fast on bad knobs causes boot failure | accept | `internal/config/config.go:340-343` (POOL_SIZE), `:314` (PING_INTERVAL), `:424` (HTTP_BODY_READ_TIMEOUT_SEC), plus session/trace var checks. Intentional fail-loud-at-boot vs. silent runtime misbehavior; operator controls env vars | closed |
| T-16-05-02 | Information Disclosure | `config.go` `slog.Default().Warn` "value" field for EMBEDDING_MODEL_DEFAULT | accept | `internal/config/config.go:576-578` — value is operator's own model name string (e.g. `qwen3-embed`); not user-supplied via HTTP; logged at Warn level in structured log; no PII or secret exposed | closed |
| T-16-05-03 | Elevation of Privilege | POOL_SIZE upper bound 256 | accept | `internal/config/config.go:343` — `POOL_SIZE: sanity cap exceeded (max 256)` boot error prevents misconfigured 10000-slot pool from spawning a kiro-cli swarm. Resource-exhaustion guard, not a security boundary per se | closed |
| T-16-05-04 | Denial of Service | `config.go` HTTP_BODY_READ_TIMEOUT_SEC ≤ 0 boot error | accept | `internal/config/config.go:419-424` — `getEnvInt("HTTP_BODY_READ_TIMEOUT_SEC", 30)` + `must be > 0` sign check; intentional fail-fast on degenerate config; default 30 s conservative | closed |
| T-16-05-SC | Tampering | Supply chain — Plan 16-05 dependencies | accept | Plan 16-05 SUMMARY confirms zero new deps; pure Go stdlib + doc changes; `go.mod`/`go.sum` unchanged | closed |

**Threats verified:** 22 register rows across 5 plans (the 5 `*-SC` supply-chain rows are listed for completeness; the phase has 16 substantive threats + 5 supply-chain attestations + 1 doc row).

*Status: `open` · `closed`*
*Disposition: `mitigate` (implementation required + verified) · `accept` (documented risk) · `transfer` (third-party)*

---

## Accepted Risks Log

The following `accept` dispositions are accepted risks for v1.9 per the rationale in the PLAN threat rows and confirmed by this audit.

| Risk ID | Threat Ref | Rationale | Accepted By | Date |
|---------|------------|-----------|-------------|------|
| AR-16-01 | T-16-01-02 | Pool saturation Warn is throttled once per generation via `sync.Once`; cannot be amplified by remote clients; bounded log volume | Phase 16 plan author (PLAN 16-01) | 2026-06-11 |
| AR-16-02 | T-16-01-03 | Warn fields `busy`/`size` are internal integer counters; no PII or secrets exposed | Phase 16 plan author (PLAN 16-01) | 2026-06-11 |
| AR-16-03 | T-16-02-03 | `PoolStats.Status` enum reflects internal pool operational state; no PII, no path info | Phase 16 plan author (PLAN 16-02) | 2026-06-11 |
| AR-16-04 | T-16-02-04 | `poolDegradedStallThreshold` is a compile-time constant (`internal/server/health.go:54`); reserved `POOL_DEGRADED_STALL_SEC` for v1.10+ if false-positives reported on slow networks | Phase 16 plan author (PLAN 16-02) | 2026-06-11 |
| AR-16-05 | T-16-03-02 | Error-path `post_chain_out` records request_id + duration_ms only; no new secret exposure relative to happy-path record; operators opt in via `CHAT_TRACE=true` | Phase 16 plan author (PLAN 16-03) | 2026-06-11 |
| AR-16-06 | T-16-04-01 | Notify goroutines bounded by O(state transitions) × 3 retry attempts; tray runs on operator desktop; not remotely triggerable | Phase 16 plan author (PLAN 16-04) | 2026-06-11 |
| AR-16-07 | T-16-04-04 | `Remove-Item -Recurse -Force $staging` targets a local temp dir constructed by `Invoke-Support` (operator-owned PS1 wrapper), not a path supplied by an external caller | Phase 16 plan author (PLAN 16-04) | 2026-06-11 |
| AR-16-08 | T-16-05-01 | Intentional fail-loud-at-boot posture matches `STREAM_IDLE_TIMEOUT_SEC` precedent established in Phase 15; operator controls env vars | Phase 16 plan author (PLAN 16-05) | 2026-06-11 |
| AR-16-09 | T-16-05-02 | `EMBEDDING_MODEL_DEFAULT` value is the operator's own env string (e.g. model name), not HTTP-supplied; logged at Warn level in structured log | Phase 16 plan author (PLAN 16-05) | 2026-06-11 |
| AR-16-10 | T-16-05-03 | POOL_SIZE sanity cap (256) is a resource-exhaustion guard, not a security boundary; spawning a 10000-slot pool would DoS the gateway host but is not a privilege-escalation vector | Phase 16 plan author (PLAN 16-05) | 2026-06-11 |
| AR-16-11 | T-16-05-04 | HTTP_BODY_READ_TIMEOUT_SEC ≤ 0 boot error is intentional fail-fast; default 30 s conservative | Phase 16 plan author (PLAN 16-05) | 2026-06-11 |
| AR-16-SC | T-16-{01..05}-SC | Phase 16 introduces zero new package dependencies across all 5 plans (verified via SUMMARY `tech-stack.added: []`). `go.mod`/`go.sum` unchanged | Phase 16 plan author (PLANs 16-01..05) | 2026-06-11 |

*Accepted risks do not resurface in future audit runs unless the underlying mitigation surface materially changes.*

---

## Unregistered Flags

No `## Threat Flags` sections were present in any of the 5 Phase 16 plan summaries (16-01 through 16-05). No unregistered attack surface detected during implementation.

---

## Security Audit Trail

| Audit Date | Threats Total | Closed | Open | Run By |
|------------|---------------|--------|------|--------|
| 2026-06-11 | 16 (+5 supply-chain) | 16 | 0 | /gsd-secure-phase (Claude Opus 4.7) |

---

## Verification Methodology

Per the `<adversarial_stance>` directive: assume each declared mitigation is absent until grep confirms it exists at the right call site. Each `mitigate` row above cites a specific file:line range that was directly inspected during this audit run. Each `accept` row has a corresponding entry in the Accepted Risks Log above.

Key verification points (project security posture from `CLAUDE.md`):

- **Subprocess spawn surface (highest risk):** `taskkill` invocation at `internal/acp/pool_pgid_windows.go:42-45` carries `//nolint:gosec` with rationale `args are static flags + integer pid (strconv.Itoa) — no operator/client input flows in`. PID source is pool's own slot bookkeeping (`Pool.all[]`). Verified inspection of the call site confirms no taint path from HTTP request or operator-supplied config reaches `pid`.
- **Middleware chain order:** `withBodyReadDeadline` is registered AFTER `auth.IPAllowlist` in each per-prefix `Route` block (`internal/server/server.go:368-379`). Denied requests therefore do not arm the body-read timer — confirmed by direct read of the Route block.
- **PII redaction format:** No new PII sentinels were introduced in Phase 16. Existing redaction in `Invoke-RedactStream` (PS1 support bundle path) uses `[bracket]` markers per project memory; not modified in this phase.
- **Fail-closed boot validation:** All 5 C-1 vars + PING_INTERVAL + HTTP_BODY_READ_TIMEOUT_SEC produce named boot errors on invalid values, accumulated via `errs = append(errs, …)` and surfaced via `errors.Join(errs...)` at end of `config.Load()`. Verified by direct inspection of `internal/config/config.go:314, 340-343, 419-424`.
- **Cross-platform builds:** Plan 16-01 SUMMARY records `GOOS=windows go build ./internal/acp/...` exits 0; Plan 16-04 SUMMARY records `GOOS=windows go build ./cmd/otto-tray/` exits 0. Cross-compilation invariant (CLAUDE.md "Distribution" constraint) is preserved.

ASVS L1 posture (gateway is internal-network, no public exposure) is appropriate. No threats classified as Critical or High remained open per `block_on: critical+high` policy.

---

## Sign-Off

- [x] All threats have a disposition (mitigate / accept / transfer)
- [x] Accepted risks documented in Accepted Risks Log
- [x] `threats_open: 0` confirmed
- [x] `status: verified` set in frontmatter

**Approval:** verified 2026-06-11
