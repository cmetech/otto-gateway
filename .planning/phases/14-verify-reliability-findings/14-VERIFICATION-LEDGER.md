# Phase 14 Verification Ledger

**Generated:** 2026-06-11
**Source review:** docs/reviews/2026-06-11-reliability-review.md
**Scope:** 1 Critical · 8 High · 14 Medium = 23 in-scope findings; 12 Low deferred to v1.10
**Merged from:** 14-LEDGER-FRAGMENT-{01,02,03,04}.md (orchestrator merge — all 4 plans deferred, worktree isolation prevented cross-fragment visibility)

## Summary

- **Critical:** 1 confirmed / 0 false-positive / 0 needs-investigation
- **High:** 8 confirmed / 0 false-positive / 0 needs-investigation
- **Medium:** 14 confirmed / 0 false-positive / 0 needs-investigation
- **Low:** 12 deferred to v1.10 (not verified this phase)

**needs_investigation_count:** 0 — gate satisfied (REL-VERIFY-GATE: must be 0 before Phase 15 can begin)

**Phase 15 scope (8 findings):** P-1 · P-2 · P-3 · H-1 · H-2 · H-3 · T-1 · T-2 · T-3
*(Note: 9 finding IDs listed because Phase 15 covers all Critical + High items.)*

**Phase 16 scope (14 findings):** P-4 · P-5 · P-6 · H-4 · H-5 · T-4 · T-5 · T-6 · T-7 · G-1 · C-1 · C-2 · C-3 · O-1

## Master Findings Table

| Finding ID | Sev | REL-* ID | Status | File:line | Evidence | Target phase |
|---|---|---|---|---|---|---|
| P-1 | C | REL-POOL-01 | confirmed | `internal/pool/pool.go:534` (removeSlot on genuine spawn failure), `:491-505` (blocking acquire), `:297-306` (removeSlot impl) | 14-FINDING-P-1.md | 15 |
| P-2 | H | REL-POOL-02 | confirmed | `cmd/otto-gateway/main.go:131` (os.Exit skips defer), `:127` (deferred cleanup), `internal/server/server.go:377-381` (30s shutdown) | 14-FINDING-P-2.md | 15 |
| P-3 | H | REL-POOL-03 | confirmed | `internal/acp/client.go:868-870` (unconditional nil, ctx arm), `:894-896` (unconditional nil, frame arm), `internal/pool/pool.go:618-635` (concurrent slot release) | 14-FINDING-P-3.md | 15 |
| P-4 | M | REL-POOL-04 | confirmed | `internal/acp/stream.go:105-122` (push blocks readLoop), `internal/acp/client.go:1085` (push uses c.clientCtx), `:503-526` (pingLoop escalation) | 14-FINDING-P-4.md | 16 |
| P-5 | M | REL-POOL-05 | confirmed | `internal/session/registry.go:206` (write under r.mu), `internal/session/entry_acp.go:77-79` (write under e.Mu), `internal/session/registry.go:358` (read unguarded) | 14-FINDING-P-5.md | 16 |
| P-6 | M | REL-POOL-06 | confirmed | `internal/acp/pool_pgid_windows.go:15` (applyPgidAttr no-op), `:21` (killProcessGroup no-op returning nil) | 14-FINDING-P-6.md | 16 |
| H-1 | H | REL-HTTP-01 | confirmed | `internal/server/server.go:346-383` + `internal/admin/sse.go:167-203` | 14-FINDING-H-1.md | 15 |
| H-2 | H | REL-HTTP-02 | confirmed | `internal/adapter/openai/sse.go:460-462` + `:482-484` | 14-FINDING-H-2.md | 15 |
| H-3 | H | REL-HTTP-03 | confirmed | `internal/adapter/openai/sse.go:543-557` + `internal/adapter/ollama/ndjson.go:541-549` | 14-FINDING-H-3.md | 15 |
| H-4 | M | REL-HTTP-04 | confirmed | `internal/server/server.go:347-360` | 14-FINDING-H-4.md | 16 |
| H-5 | M | REL-HTTP-05 | confirmed | `internal/admin/tail.go:393-427` (cap at :402) | 14-FINDING-H-5.md | 16 |
| T-1 | H | REL-TRAY-01 | confirmed | `cmd/otto-tray/tray.go:144-145`, `scripts/otto-gw:782-784`, `scripts/otto-gw.ps1:562-564` | 14-FINDING-T-1.md | 15 |
| T-2 | H | REL-TRAY-02 | confirmed | `scripts/otto-gw.ps1:581-593` (Get-GatewayStatus exit 1), `scripts/otto-gw.ps1:1464` (Invoke-Support) | 14-FINDING-T-2.md | 15 |
| T-3 | H | REL-TRAY-03 | confirmed | `cmd/otto-tray/tray.go:199-201`, `cmd/otto-tray/tray.go:74-75`, `cmd/otto-tray/uihelpers_darwin.go:43-58` | 14-FINDING-T-3.md | 15 |
| T-4 | M | REL-TRAY-04 | confirmed | `cmd/otto-tray/tray.go:199-201` (applyState), `cmd/otto-tray/uihelpers_windows.go:50-68` | 14-FINDING-T-4.md | 16 |
| T-5 | M | REL-TRAY-05 | confirmed | `cmd/otto-tray/tray.go:153` (snapshot error swallowed), `cmd/otto-tray/fsm.go:52-54` | 14-FINDING-T-5.md | 16 |
| T-6 | M | REL-TRAY-06 | confirmed | `cmd/otto-tray/tray.go:296-299`, `scripts/otto-gw.ps1:321,330,1644` | 14-FINDING-T-6.md | 16 |
| T-7 | M | REL-TRAY-07 | confirmed | `scripts/otto-gw:1864-1873` (live logs uncapped), `scripts/otto-gw:1957-1989` (gz-only cap), `cmd/otto-tray/runner.go:29-33` | 14-FINDING-T-7.md | 16 |
| G-1 | M | REL-HOOKS-01 | confirmed | `internal/engine/collect.go:165,171` (return before PostHook loop at `:187`); `internal/adapter/anthropic/collect.go:177,184` (return before RunPostHooks at `:207`) | 14-FINDING-G-1.md | 16 |
| C-1 | M | REL-CFG-01 | confirmed | `internal/config/config.go:313,336,343,353,487` (no sign check); `internal/pool/config.go:119-122`; `internal/session/config.go:99-108` | 14-FINDING-C-1.md | 16 |
| C-2 | M | REL-CFG-02 | confirmed | `internal/config/config.go:295` (no sign check); `internal/acp/client.go:59` (only defaults == 0); `internal/acp/client.go:505` (time.NewTicker panics) | 14-FINDING-C-2.md | 16 |
| C-3 | M | REL-CFG-03 | confirmed | CLAUDE.md (documented); `internal/` + `cmd/` grep: zero occurrences (never read) | 14-FINDING-C-3.md | 16 |
| O-1 | M | REL-CFG-04 | confirmed | `internal/pool/pool.go:490-505` (blocks with no Warn log; debugLog only after acquire at `:506`) | 14-FINDING-O-1.md | 16 |
| P-7 | L | — | n/a (deferred to v1.10) | — | docs/reviews/2026-06-11-reliability-review.md | — |
| P-8 | L | — | n/a (deferred to v1.10) | — | docs/reviews/2026-06-11-reliability-review.md | — |
| H-6 | L | — | n/a (deferred to v1.10) | — | docs/reviews/2026-06-11-reliability-review.md | — |
| H-7 | L | — | n/a (deferred to v1.10) | — | docs/reviews/2026-06-11-reliability-review.md | — |
| T-8 | L | — | n/a (deferred to v1.10) | — | docs/reviews/2026-06-11-reliability-review.md | — |
| T-9 | L | — | n/a (deferred to v1.10) | — | docs/reviews/2026-06-11-reliability-review.md | — |
| C-4 | L | — | n/a (deferred to v1.10) | — | docs/reviews/2026-06-11-reliability-review.md | — |
| C-5 | L | — | n/a (deferred to v1.10) | — | docs/reviews/2026-06-11-reliability-review.md | — |
| C-6 | L | — | n/a (deferred to v1.10) | — | docs/reviews/2026-06-11-reliability-review.md | — |
| O-2 | L | — | n/a (deferred to v1.10) | — | docs/reviews/2026-06-11-reliability-review.md | — |
| O-3 | L | — | n/a (deferred to v1.10) | — | docs/reviews/2026-06-11-reliability-review.md | — |
| O-4 | L | — | n/a (deferred to v1.10) | — | docs/reviews/2026-06-11-reliability-review.md | — |

**Total rows:** 23 in-scope (rows 1-23) + 12 Low placeholders (rows 24-35) = 35 rows matching the source review's count.

## REQUIREMENTS.md traceability

All 23 in-scope findings are `confirmed`. No `false-positive` rows → no REL-* status flips in REQUIREMENTS.md §Traceability beyond marking REL-VERIFY-* rows complete at phase close. The phase-close commit handles that flip.
