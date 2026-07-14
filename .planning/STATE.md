---
gsd_state_version: 1.0
milestone: v1.10.3
milestone_name: Reliability Closeout
status: Awaiting next milestone
stopped_at: Phase 20 context gathered
last_updated: "2026-06-12T17:32:29.113Z"
last_activity: 2026-06-12 — Milestone v1.10.3 completed and archived
progress:
  total_phases: 26
  completed_phases: 25
  total_plans: 86
  completed_plans: 86
  percent: 96
---

# Project State

## Project Reference

See: .planning/PROJECT.md (updated 2026-06-12)

**Core value:** All three API surfaces (OpenAI for Pi SDK, Ollama for LangFlow, Anthropic for loop24-client/GSD Pi) serve their respective clients without those clients knowing kiro-cli exists, with one place to enforce policy.
**Current focus:** Planning next milestone (v1.10.4 or next). v1.10.3 Reliability Closeout SHIPPED 2026-06-12.

## Current Position

Phase: Milestone v1.10.3 complete
Plan: —
Status: Awaiting next milestone
Last activity: 2026-06-12 — Milestone v1.10.3 completed and archived

## Performance Metrics

**Velocity:**

- Total plans completed: 53
- Average duration: n/a
- Total execution time: 0 hours

**By Phase:**

| Phase | Plans | Total | Avg/Plan |
|-------|-------|-------|----------|
| 01 | 5 | - | - |
| 01.1 | 5 | - | - |
| 02 | 6 | - | - |
| 03.1 | 6 | - | - |
| 05 | 5 | - | - |
| 6 | 5 | - | - |
| 08.3 | 1 | - | - |
| 08.3.2 | 3 | - | - |
| 08.4 | 1 | - | - |
| 13 | 6 | - | - |
| 14 | 4 | - | - |
| 20 | 1 | - | - |

**Recent Trend:**

- Last 5 plans: n/a (no plans executed yet in v1.8)
- Trend: n/a

*Updated after each plan completion*
| Phase 08.2 P01 | 45m | 8 tasks | 10 files |
| Phase 08.4 P01 | 35m | 3 tasks (R/G/F) + Task H pending | 7 files |
| Phase 16 P01 | 35min | 4 tasks | 18 files |
| Phase 16 P03 | 15min | 1 tasks | 5 files |
| Phase 16 P05 | 12min | 3 tasks (TDD R/G × 3) | 7 files |
| Phase 16 P02 | 25min | 3 tasks | 9 files |
| Phase 16 P04 | 13min | 4 tasks | 10 files |
| Phase 17 P03 | 12min | 5 tasks (batched 1 commit per D-17-02) | 5 files |
| Phase 17 P01 | 6min | 2 tasks | 9 files |
| Phase 17 P02 | 17min | 2 tasks (T1 RED baseline + T2 GREEN iter 1; iter 2-3 skipped) | 1 file |
| Phase 19 P01 | ~75min | 4 tasks | 3 files |

## Accumulated Context

### Roadmap Evolution

- 2026-06-07: v1.8 "Nyquist Coverage Uplift" roadmap drafted — single Phase 13 with 6 parallel plans (one per non-compliant v1.5 target phase: 02, 03, 06, 06.1, 08, 08.4). NYQ-02/03/06/06.1/08/08.4 mapped to Phase 13 plans; NYQ-ALL satisfied at milestone close (no 7th plan). Phase 08.3.1 ACP demux + Windows Authenticode re-deferred to v1.9+ per the v1.8-opens narrow-scope decision; both await external triggers (multi-tenant deployment driver / code-signing cert procurement).
- 2026-06-07: v1.7 "Go Stdlib CVE Cleanup" roadmap drafted — single Phase 12 (Go toolchain CVE remediation; CVE-01/02/03 + CI-02). Carryover items (Phase 08.3.1 ACP demux, Nyquist uplift, Windows Authenticode) explicitly re-deferred to v1.8 to keep v1.7 narrow.
- 2026-06-07: v1.6 "Tooling Cleanup" roadmap drafted — Phase 10 (golangci-lint v2 cleanup + re-gate; LINT-01/02/03) and Phase 11 (gofumpt tree-wide cleanup + pre-commit gate; FMT-01/02 + CI-01). Phase 08.3.1 ACP Per-Session Stream Demux re-scoped out of v1.6 to v1.7 to keep this milestone narrow and ship-fast.
- Phase 3.1 inserted after Phase 3: Anthropic Surface — adapter/anthropic for Messages API at /v1/messages with SSE streaming day-one (loop24-client / GSD Pi via ANTHROPIC_BASE_URL). Promotes SURF-V2-01 to v1; adds ANTH-01..07 + SURF-08. (URGENT)
- Phase 1.1 inserted after Phase 1: ACP Wire Alignment — fix 10 wire-shape defects discovered during Phase 2 discuss; add real-kiro session/prompt round-trip integration test as Phase 2 unblock gate (URGENT)
- Phase 8 edited: edited fields: goal, requirements (+PLUG-06), success_criteria (+SC6 PIIRedactionHook)
- Phase 8 edited: edited fields: requirements (+OBSV-04), success_criteria (+SC7 GET /health/hooks view-only)
- Phase 6.1 inserted after Phase 6: Admin Observability UI — dark-mode /admin page rendering /health + /health/agents with brand palette; auto-refresh polling; live log tail nice-to-have (URGENT)
- Phase 8.1 inserted after Phase 8: Close gap: INTEG-01 streaming-mode PreHook short-circuit + v1.5 audit WARNINGs (URGENT)
- Phase 8.3 inserted after Phase 8.2: ACP Prompt() Non-Blocking Refactor — fix chunk-buffer-overflow deadlock in synchronous Prompt() (Windows v1.9.2 PII smoke-test regression) (URGENT)
- Phase 08.3.1 inserted after Phase 8.3: ACP Per-Session Stream Demux — close WR-04 cross-session chunk-leak race from Phase 8.3 code review (URGENT)
- Phase 08.3.2 inserted after Phase 8.3: PII Smoke Test Methodology Fix — decouple round-trip verification from LLM cooperation (Claude refuses PII echo in v1.9.3 live test) (URGENT)
- Phase 8.4 inserted after Phase 8.3: US Address PII Coverage — add USZIP, USState, USAddress regex recognizers + silence prose-v2 PERSON false positives on street names (URGENT)

### Decisions

Decisions are logged in PROJECT.md Key Decisions table.
Recent decisions affecting current work:

- 2026-06-07: v1.8 scope narrowed to 1 phase (Phase 13 — Nyquist coverage uplift, 6 parallel plans). Phase 08.3.1 ACP demux and Windows Authenticode re-deferred to v1.9+ per the v1.8-opens narrow-scope decision: ACP demux awaits a real multi-tenant deployment driver; Authenticode awaits cert procurement. The `gsd-nyquist-auditor` agent's read-only-implementation rule holds milestone-wide — any test that surfaces an actual bug ESCALATEs to a separate phase, never a silent patch.
- 2026-06-07: v1.7 scope narrowed to 1 phase (Phase 12 — CVE backlog). Phase 08.3.1 ACP demux, Nyquist uplift, and Windows Authenticode re-deferred to v1.8 per the v1.7-opens decision to keep the milestone narrow and ship fast.
- 2026-06-07: v1.6 scope narrowed to 2 phases (lint + fmt/gate). Phase 08.3.1 ACP demux deferred to v1.7 per REQUIREMENTS.md "Future Requirements" section.
- Pre-Phase 1: Go over Rust (cross-compile triviality, first systems project for author)
- Pre-Phase 1: Dual API surface (OpenAI + Ollama) on one binary, one canonical engine
- Pre-Phase 1: Adapter-over-canonical layout — `internal/adapter/{ollama,openai}` ↔ `internal/canonical` ↔ `internal/engine`
- Pre-Phase 1: stdlib `net/http` + `chi` (reject `fasthttp`)
- Pre-Phase 1: Trust-gate suite required from day one (Phase 1 establishes lint/test/security baseline)
- 2026-05-27: Embeddings (Phase 7) cut from milestone — `/api/embed`, `/api/embeddings`, `/v1/embeddings` will not be implemented in v1. Provisional sidecar decision now moot.
- 2026-06-04: Phase 08.4 USState regex restructured to two alternation arms (comma-prefixed permissive trail OR line-start ZIP-required trail) to honor AP-2 in production. Diverges from literal RESEARCH.md regex. See 08.4-01-SUMMARY.md.
- 2026-06-04: Phase 08.4 USAddress whitespace tightened from `\s+` to `[ \t]+` so RE2's `\s` does not let `\n` smuggle multi-line text into a single span (Pitfall 3).
- 2026-06-04: `make ci` lint step has 93 pre-existing project tech-debt issues (verified pre-existing on base 315f1cc); PII-01 introduced none. Deferred to a future lint-debt-closeout phase. All other trust gates (gofumpt, vet, build, test-race, arch-lint, examples) green.
- [Phase ?]: Plan 16-01: Entry.LastUsed converted to unexported atomic.Int64 (lastUsedNs) — lowercase name prevents accidental direct field access
- [Phase ?]: Plan 16-01: Stream.ctx field carries //nolint:containedctx — per-request ctx is load-bearing for P-4 push backpressure scoping
- [Phase ?]: Plan 16-01: P-6 used taskkill /T /F (Option A) rather than CreateJobObject — stdlib-only, single nolint annotation
- [Phase ?]: Plan 16-01: lastProgressAt seeded at Warmup completion to avoid post-warmup false-degraded window
- [Phase ?]: Plan 16-03: engine.Collect rangeErr block split into idle-timeout + generic loopErr branches; both call PostHooks-with-nil before return
- [Phase ?]: Plan 16-03: After() methods LoadAndDelete unconditionally then nil-resp early-return — reclaim runs first regardless of resp shape
- [Phase 16]: Plan 16-05: C-1 error message uses literal substring "must be >= 0" matching Phase 14 regression test contract (not "must be >= 1" / "must be > 0" suggested by plan must_haves). Test contract binds over plan text.
- [Phase 16]: Plan 16-05: C-3 EMBEDDING_MODEL_DEFAULT Warn emitted from config.Load() via slog.Default() (not main.go) — regression test captures slog.Default() and calls only config.Load(); main.go does not slog.SetDefault (D-15).
- [Phase 16]: Plan 16-05 (H-4 partial): server.Config grew BodyReadTimeout time.Duration field; cmd/otto-gateway/main.go wires cfg.BodyReadTimeout into NewFromConfig. Plan 16-02 (Wave 2 consumer) reads it without further config-side changes.
- [Phase ?]: Plan 16-02: H-4 body-read deadline via path-scoped chi middleware (chatBodyDeadlinePaths static map) registered AFTER auth.IPAllowlist; only r.Body.Close() fires on timeout so SSE writes remain unbounded (D-04b)
- [Phase ?]: Plan 16-02: PoolStatsSource interface extended with IsExhausted/LastProgressAt (vs sibling interface) so cmd/otto-gateway/main.go poolStatsAdapter naturally owns the full pool→health bridge
- [Phase ?]: Plan 16-02: PoolStats.Status field rendered WITHOUT omitempty — empty string is a meaningful 'pool not wired' signal; tray probe (Plan 16-04) can distinguish degraded-mode boot from a wired-but-OK pool
- [Phase ?]: Plan 16-02: Task 3 D-05 shipped atomic per D-02 — new health_status_test.go could not compile against unmodified PoolStats; same posture as Plan 16-05 Task 3 (be7abbc) and Plan 16-01 Task 4 (775015d)
- [Phase ?]: Phase 16 Plan 04: notifyTransition extracted from applyState (testability — applyState touches systray.MenuItem pointers, untestable without systray.Run); local fn := notifyFn snapshot before goroutine launch closes -race window
- [Phase ?]: Phase 16 Plan 04: T-6/T-7 regression tests permanent-skip stubs (Phase 15 T-2/T-3 precedent for Windows-only PS1-resident fixes); T-7 timeout via Stopwatch+Test-Deadline+throw rather than Start-Job to avoid 20+ $using: scoping in pwsh-unverifiable code
- [Phase 17]: Plan 17-03 executed as single atomic commit per D-17-02 (5 mechanical fixes in 1 commit; revert-as-unit guarantee). 14 ins / 28 del across 5 files; small bounded diff. Commit b78fd09.
- [Phase 17]: Plan 17-03 D-17-05 single-user laptop posture rationale recorded inline in commit body for the 0o600 WriteFile tightening on tray.go last-error.log — no cross-user reader exists; support-bundle may contain kiro-cli stderr.
- [Phase 17]: Plan 17-03 dead-code stale-comment policy: minimize the diff — kept "removed in Phase 17" annotations rather than full comment removal so future readers grepping for removeSlot find the historical context explaining the WR-07 / CR-01 / REL-POOL-01 D-08 design rationale.
- [Phase ?]: [Phase 17]: Plan 17-01 executed atomically per D-17-01. Worktree spike (/tmp/otto-17-01-spike) confirmed go build + go vet + go-arch-lint (OK - No warnings found) + targeted go test -race clean before main-worktree commit. Single atomic commit f727b24 lands all 9 files: canonical.ErrPoolExhausted added, pool.ErrPoolExhausted aliased, 8 adapter errors.Is sites flipped, 3 pool imports dropped, 3 comment refs updated, sentinel-identity test added.
- [Phase ?]: [Phase 17]: Plan 17-01 sentinel-identity test placed in canonical_test (blackbox) per testmain_test.go precedent — keeps the existing goleak.VerifyTestMain gate uniform with the rest of canonical's test suite. Three assertions: self-identity, byte-exact message ('pool: all workers busy; retry in 5s'), errors.Is wrap-traversal via fmt.Errorf %w.
- [Phase 17]: Plan 17-02 closed REL-POOL-02 goleak flake at iter 1 (planned 3-iter budget; iters 2 + 3 not needed). Three test-scaffolding edits in regression_rel_pool_02_test.go: (1) resultWg tracks the orphan stream-drain goroutines and is waited on after both gate-closes + the outer wg; (2) per-instance unique sids (fake-sess-bc0 / fake-sess-bc1) override the shared fakeClient default — pre-existing sessionSlots overwrite bug surfaced by iter 1 reliable draining (deviation Rule 1 fix); (3) drain stream.Chunks() to channel close BEFORE calling Result() — uses chan-close write barrier as synchronization edge with acp.Stream close's StopReason mutation, routing around a real production race (close(s.done) BEFORE acquiring s.mu, exposed by `go test -race`) without production code changes per D-17-05. 20/20 PASS final + 60/60 PASS across three independent rounds. Commit ca258f9.
- [Phase 17]: acp.Stream close-vs-read race flagged for v1.10 hardening (out of scope per D-17-05). close() closes s.done before acquiring s.mu, so Result waiters can return the *FinalResult pointer before close writes StopReason; downstream pointer dereferences in poolStreamWrapper.Result race the write. Test workaround inherits chan-close-on-s.chunks as the synchronization edge. Recommended fix: copy *s.result into a local value inside Result's s.mu critical section so downstream reads see a snapshot.
- [Phase 17]: make ci exit 0 end-to-end at HEAD ca258f9 — fmt-check (gofmt + gofumpt), vet, build, lint (golangci-lint 0 issues), test-race (all packages green incl. REL-POOL-02 deflaked), arch-lint (OK - No warnings), examples, govulncheck (No vulnerabilities). v1.9.1 release tag (D-17-03) is unblocked.
- [Phase ?]: Phase 19 D-19-01 applied byte-precisely: Result() now snapshots *s.result under s.mu via cp := *s.result; returns &cp. Signature unchanged. REL-ACP-01 race report closed.
- [Phase ?]: Phase 19 D-19-03 test assertion adapted (Rule 1 deviation): from PATTERNS strict StopReason==StopEndTurn to dual invariant (no race report AND value in StopUnknown/StopEndTurn).
- [Phase ?]: Pre-existing Phase 18 unparam warning at internal/acp/regression_rel_obsv_03_test.go:72 deferred to v1.10.4 per Phase 18-02 SUMMARY's same-class noctx deferral precedent.

### Pending Todos

None yet.

### Blockers/Concerns

- Pi SDK env var / config key for setting OpenAI base URL needs verification before Phase 3 starts (per PROJECT.md "Context — Clients" — open verification item).

### Quick Tasks Completed

| # | Description | Date | Commit | Directory |
|---|-------------|------|--------|-----------|
| 260523-gna | DEVELOPERS.md + idempotent macOS/Windows setup scripts | 2026-05-23 | 84562ce | [260523-gna-create-developers-md-with-step-by-step-d](./quick/260523-gna-create-developers-md-with-step-by-step-d/) |
| 260524-ldx | CLI flag support (flag-wins-over-env) for gateway binary | 2026-05-24 | f325d04 | [260524-ldx-cli-flags](./quick/260524-ldx-cli-flags/) |
| 260524-md7 | Rebrand loop24-gateway → OTTO Gateway (Tier 2 full code rebrand; dir rename deferred to Tier 3) | 2026-05-24 | e89cbf3 | [260524-md7-otto-rebrand](./quick/260524-md7-otto-rebrand/) |
| 260524-pee | E2E suite (real-binary boot + kiro, markdown report) automating HUMAN-UAT 1/2/3/6; opt-in Node SDK harness for 4/5 | 2026-05-24 | a57cbf5 | [260524-pee-e2e-suite](./quick/260524-pee-e2e-suite/) |
| 260524-pyd | E2E Ollama contract coverage (LangFlow surface): version/auth/tags/chat/generate + stream-downgrade guard; live 18 pass/0 fail/0 skip | 2026-05-24 | 49fb09e | [260524-pyd-ollama-e2e](./quick/260524-pyd-ollama-e2e/) |
| 260528-ng1 | otto-gateway publish script: scripts/publish.sh (754L, shellcheck clean) + Layer-1 dry-run harness (9/9 pass) + CI publish-dry-run job + operator-quickstart `## Publishing a build` section; lean rewrite of oscar bulk_upload_verify per docs/superpowers/specs/2026-05-28 spec | 2026-05-28 | 795f99b | [260528-ng1-implement-otto-gateway-publish-script-pe](./quick/260528-ng1-implement-otto-gateway-publish-script-pe/) |
| 260528-qe9 | Wrapper naming + probe consistency: bash wrapper OTTO_ADDR default localhost→127.0.0.1 (matches 7185293 PS fix) + gateway KIRO_CMD default kiro-cli→kiro (matches what wrappers auto-detect via `command -v kiro`); 2 atomic commits, build + race tests clean | 2026-05-28 | 0770c79 | [260528-qe9-wrapper-naming-probe-consistency-cleanup](./quick/260528-qe9-wrapper-naming-probe-consistency-cleanup/) |
| 260528-rom | Windows operator .bat surface: setup.bat (one-time MOTW strip + execution policy fix) + otto-gw.bat (cmd.exe dispatcher) + start/stop/status.bat (Explorer shortcuts); Makefile + operator-quickstart.md updated; bats shipped in all 4 packages (ASCII no-BOM); single atomic commit | 2026-05-28 | 86b76ac | [260528-rom-windows-operator-bat-surface](./quick/260528-rom-windows-operator-bat-surface/) |
| 260528-tbg | docs/INSTALL.md install/upgrade reference (481 lines, UTF-8 no BOM, TOC + per-OS checklists + .env load order + wrapper tradeoff table + upgrade behavior + pitfalls + verification commands); Makefile PKG_INSTALL declaration; operator-quickstart.md cross-reference; shipped in all 4 dist archives | 2026-05-28 | 07f4f46 | [260528-tbg-ship-docs-install-md-as-part-of-every-di](./quick/260528-tbg-ship-docs-install-md-as-part-of-every-di/) |
| 260528-d84 | Phase 9 close-out: goleak coverage gaps + property tests for buildBlocks/CoerceToolCall + .github/workflows/ci.yml + ROADMAP/REQUIREMENTS housekeeping; v1.5 milestone closed | 2026-05-28 | 91dd162 | [260528-d84-phase-9-closeout-goleak-property-tests-e](./quick/260528-d84-phase-9-closeout-goleak-property-tests-e/) |
| 260529-f8r | Admin Log Tail panel → 4-column CSS-grid table (Time/Level/Source/Message) with sticky header, level chips, wrapping message cells, dataset.raw grep plumbing, full-width fallback row; pool-slot grid bumped to 4 columns at desktop | 2026-05-29 | 4d8c147 | [260529-f8r-convert-log-tail-panel-to-css-grid-table](./quick/260529-f8r-convert-log-tail-panel-to-css-grid-table/) |
| 260531-ebi | Surface DEBUG + chat-trace enablement: debug/chat_trace booleans cfg→admin.Deps→snapshot JSON + HTML summary strip; otto-gw status (POSIX + PowerShell) reads /admin/api/snapshot (not D-12-locked /health); BSD-sed portability fix for bool parse | 2026-05-31 | eb639ef | [260531-ebi-surface-debug-and-chat-trace-enablement-](./quick/260531-ebi-surface-debug-and-chat-trace-enablement-/) |
| 260531-f1i | Align scripts/.env.otto-gw.example with OOB install defaults so it is the golden-copy config reference (active-key set verified identical to `init --non-interactive`); foundation for env-merge-on-upgrade | 2026-05-31 | 645a79a | [260531-f1i-align-env-template-golden-copy](./quick/260531-f1i-align-env-template-golden-copy/) |
| 260531-fba | Refactor init_cmd() heredoc + PS1 @"..."@ to template-copy + set_env_line: eliminate dual key-list drift; set_env_line helper is building block for env-merge-on-upgrade; shellcheck clean; 7-variant diff clean | 2026-05-31 | de28247 | [260531-fba-refactor-otto-gw-init-to-render-the-env-](./quick/260531-fba-refactor-otto-gw-init-to-render-the-env-/) |
| 260531-o4s | Add --trace flag to scripts/otto-gw: parse_flags + apply_cli_flags + usage(); exports DEBUG=true + CHAT_TRACE=true for full observability in one flag; shellcheck clean | 2026-05-31 | 7ec090b | [260531-o4s-add-trace-flag-to-otto-gw-enabling-debug](./quick/260531-o4s-add-trace-flag-to-otto-gw-enabling-debug/) |
| 260531-oax | Add -Trace switch to scripts/otto-gw.ps1 (Windows parity with bash --trace): [switch]$Trace → $env:DEBUG=true + $env:CHAT_TRACE=true; .bat needs no change (pass-through %*); pwsh parse unverified (not installed on macOS) | 2026-05-31 | 1d68a7f | [260531-oax-add-trace-switch-to-otto-gw-ps1-mirrorin](./quick/260531-oax-add-trace-switch-to-otto-gw-ps1-mirrorin/) |
| 260531-oox | Positive-signal DEBUG markers to localize PII-redaction request stalls: pii.redact.done (Logger field added to PIIRedactionHook), engine.new_session.ok, engine.prompt.sent, anthropic.sse.first_chunk (one-shot), pool.acquire/release (nil-safe debugLog helper); 3 atomic commits, go build + go vet clean (vet finds 11 pre-existing unrelated findings) | 2026-05-31 | 4269f9c | [260531-oox-add-positive-signal-debug-logs-to-locali](./quick/260531-oox-add-positive-signal-debug-logs-to-locali/) |
| 260531-pt8 | Fix PII redaction sentinel hang: change <EMAIL_1>/<EMAIL>/<EMAIL:h-...> → [EMAIL_1]/[EMAIL]/[EMAIL:h-...] in internal/plugin/pii/modes.go so kiro-cli/Claude no longer treats the sentinel as an opening XML tag (was causing engine.ACP.Prompt to never return → 120s client timeout). Confirmed live: replace mode hung; mask mode (asterisks) returned correctly. Added TestApplyMode_NoAngleBrackets_RegressionForKiroHang. 3 atomic commits, go build ./... + go test ./internal/plugin/pii/... clean | 2026-05-31 | 0a65e28 | [260531-pt8-change-pii-redaction-sentinel-from-angle](./quick/260531-pt8-change-pii-redaction-sentinel-from-angle/) |
| 260531-ra6 | Kiro-lifecycle hygiene: (1) regression test TestPool_Cancel_ReleasesSlot_WithoutResultDrain (Pool.Cancel already releases — defense-in-depth lock); (2) spawn kiro-cli in own pgrp on darwin/linux via build-tagged internal/acp/pool_pgid_{unix,windows}.go so SIGTERM cascades to children; (3) scripts/otto-gw + .ps1 stop-time reap of stray $KIRO_CMD orphans (EXACT path match, never by name). 3 atomic commits, go build ./... + GOOS=windows go build ./... + shellcheck + tests clean. Note: stuck-slot symptom seen in live testing has a different root cause (watchdog likely not firing on silent kiro) — follow-up investigation needed | 2026-05-31 | 0123225 | [260531-ra6-pool-slot-release-on-cancel-kiro-process](./quick/260531-ra6-pool-slot-release-on-cancel-kiro-process/) |
| 260531-ruv | Configurable server-side stream-idle watchdog: STREAM_IDLE_TIMEOUT_SEC env (default 30, 0 disables, negative=boot-error). New engine.RangeChunksWithIdleTimeout helper + canonical.ErrStreamIdleTimeout sentinel wired into all 5 chunk-loop sites (engine.Collect, anthropic.Collect, anthropic.sse, ollama streaming, openai streaming). On idle: WARN log marker stream.idle_timeout, per-surface error frame to client (Anthropic SSE event:error, Ollama NDJSON error, OpenAI SSE error+[DONE]); engine watchdog then releases pool slot via existing Cancel path. scripts/otto-gw --idle-timeout SEC + .ps1 -IdleTimeout INT flags. 4 atomic commits, go build ./... + GOOS=windows go build ./... + shellcheck + tests clean. Stops 120s client-timeout-then-stuck-slot pattern | 2026-06-01 | 0c934e4 | [260531-ruv-configurable-server-side-idle-stream-tim](./quick/260531-ruv-configurable-server-side-idle-stream-tim/) |
| 260531-t8a | Close install-flow gap from ruv: add STREAM_IDLE_TIMEOUT_SEC to the wrapper init registry (scripts/otto-gw + .ps1) — flag parser, existing-value extract, resolution block, set_env_line/Set-EnvLine call, env-subcommand for-key list, usage text. Precedence: CLI flag > existing-file value > default 30 commented. Smoke-tested re-init idempotency on bash; PS1 mirrors signatures (static review only per oax caveat). Single atomic commit. shellcheck + bash -n clean. Follow-up phase needed for proper env-merge-on-upgrade command | 2026-06-01 | (inline) | [260531-t8a-add-stream-idle-timeout-sec-to-wrapper-i](./quick/260531-t8a-add-stream-idle-timeout-sec-to-wrapper-i/) |
| 260531-tl1 | otto-gw upgrade-env feature (overrides.env split model, mirrors OSCAR's two-file pattern): chain .otto-gw.env (generated template copy) + .otto-gw.overrides.env (operator-owned, loaded second, holds secrets + customizations) in wrapper loader; new upgrade-env subcommand regenerates .otto-gw.env from template with added/orphaned/unchanged reporting + orphan log; new migrate-to-overrides one-time migration extracts non-default values + backs up original; init refactor writes secrets to overrides always; deprecation WARN on start when single-file model detected. Bash + PS1 mirror surfaces. 6 atomic commits + smoke harness (7 sections all green). shellcheck + bash -n clean. PS1 live smoke deferred (no pwsh on macOS) | 2026-06-01 | 30a9c21 | [260531-tl1-otto-gw-upgrade-env-feature-with-overrid](./quick/260531-tl1-otto-gw-upgrade-env-feature-with-overrid/) |
| 260601-98c | Admin UI redesign step 1 — structural scaffolding for multi-step UI refresh inspired by samsara_apps/liberty_elevator: split internal/admin/templates/index.html.tmpl → base.html.tmpl (shared layout: <html data-theme>, pre-paint theme bootstrap inline script before stylesheet, header with wordmark + theme toggle, tab nav Dashboard\|About\|Docs) + dashboard.html.tmpl (existing sections in {{define "content"}}, all data-* selectors preserved for admin.js) + about.html.tmpl + docs.html.tmpl (Coming soon placeholders, real content in steps 3-4). assets.go now parses per-page templates with ParseFS. admin.go renames pageHandler→dashboardHandler, adds aboutHandler + docsHandler routing GET /about + GET /docs with same buffer-then-write WR-05 pattern; TabActive field added to template data. admin.css adds no-op [data-theme="light"] overrides (real palette in step 2) + minimal .otto-header/.otto-tab-nav/.otto-tab/.otto-theme-toggle styling with #FAD22D active tab underline. TRST-04 boundary preserved. handlers_test.go embed-glob assertion updated for new 4-template set. Single atomic commit | 2026-06-01 | 86460e5 | [260601-98c-admin-ui-redesign-step-1-layout-extracti](./quick/260601-98c-admin-ui-redesign-step-1-layout-extracti/) |
| 260601-9je | Admin UI redesign step 2 — Liberty-inspired palette + component restyle (CSS-only, no template or JS changes): replace purple-heavy dark palette (#28243D body, #3A3A3A card) with neutral slate (--otto-bg #1A1D24, --otto-card #242832, --otto-header #1E2128, --otto-border #2D3340) keeping OTTO yellow #FAD22D as primary accent; replace step-1 no-op [data-theme="light"] overrides with real light palette (off-white #F7F8FA body, white cards, dark header still for brand continuity, darker yellow link #B8930A for AA contrast); add 14-token semantic palette per theme + 5 typography size tokens (--otto-text-xs/sm/base/lg/xl); migrate --otto-muted→--otto-fg-muted across all sites; new component classes (.otto-card + .otto-card--accent-{healthy,warning,failed,accent}, .otto-badge); modernized .otto-h2 (uppercase, tracked, 20px), .otto-summary-label (11px tracked 0.06em); light-mode-only card box-shadow; 150ms color/bg/border transitions on body/.otto-card/.otto-header for smooth theme switching; 11 of 12 grep gates pass (gate 5 #FAD22D count=3 expected — all 3 sites inside palette declaration blocks per spec). Single atomic commit (99f2d37, +185/-59 in admin.css) | 2026-06-01 | 99f2d37 | [260601-9je-admin-ui-redesign-step-2-palette-and-com](./quick/260601-9je-admin-ui-redesign-step-2-palette-and-com/) |
| 260601-a3z | Admin UI redesign step 3 — About page real content + icon theme toggle (2 atomic commits: a8dcb01 Go side, f911d8f UI side). admin.Deps extended with 12 runtime cfg fields (HTTPAddr, PoolSize, SessionTTL, StreamIdleTimeoutSec, AuthEnabled, IPAllowlistEnabled, KiroCmd, KiroArgs, KiroCwd, OllamaPathPrefix, OpenAIPathPrefix, AnthropicPathPrefix); cmd/otto-gateway/main.go wires from cfg at admin.Handler construction (cfg.KiroCWD→Deps.KiroCwd field-name translation). aboutHandler builds AboutData with runtime.Version/GOOS/GOARCH + empty-string fallbacks ("(unset — degraded mode)", "(empty)") computed in handler not template. about.html.tmpl replaces "Coming soon" placeholder with identity block (wordmark + tagline) + 5 .otto-card cards in CSS grid (auto-fit minmax 280px): Build info, Runtime status, Feature flags (with SENSITIVE chip when ChatTrace on), Upstream worker, Endpoints; project links footer (github.com/cmetech/otto_app). Theme toggle in base.html.tmpl swapped text button→icon-only 32x32 button with both Heroicons-style sun + moon SVGs inline (currentColor stroke); CSS reveal via [data-theme="dark"] .icon-sun{display:block} / light .icon-moon. TRST-04 preserved (new imports fmt/runtime/strings all stdlib). All 13 plan grep gates + go build + admin tests pass. Workflow note: executor recovered from cwd-drift mid-task via per-file git checkout (no destructive bulk reset), documented in SUMMARY | 2026-06-01 | f911d8f | [260601-a3z-admin-ui-redesign-step-3-about-page-cont](./quick/260601-a3z-admin-ui-redesign-step-3-about-page-cont/) |
| 260601-aix | Admin UI redesign step 4 — Docs/Help page real content (2 atomic commits: c6e5531 Go side, de2bffe UI side). Completes the 4-step UI redesign inspired by samsara_apps/liberty_elevator. admin.Deps gains 2 chat-trace fields (ChatTraceFile string, ChatTraceMaxAgeDays int) wired from cfg in main.go. New docsHandler builds DocsData (mirrors aboutHandler/aboutData pattern from step 3 with same WR-05 buffer-then-write) populating EnvVarRow{Name,Default,Description,CurrentValue} slice for every cfg env var + CliFlagRow{Flag,EnvMapping,Notes} slice; AUTH_TOKEN safety: handler renders "(set)"/"(unset)" never the plaintext token. docs.html.tmpl replaces Coming soon placeholder with 7 .otto-card sections: intro, env vars table (in scrollable .otto-docs-table-scroll), Files & paths (chat trace + .otto-gw.env/.overrides.env load order + log destination), CLI flags / startup (code block + flag table), Endpoints reference (Admin/Public API/Internal subsections), Basic admin usage (dashboard interpretation), Troubleshooting bullets; project links footer matches About. admin.css adds .otto-docs-table-scroll + .otto-code-block + env-table column styling. TRST-04 preserved (only new import strconv — stdlib). 13 end-to-end checks pass including sentinel test that AUTH_TOKEN plaintext is absent from response. go build + admin tests clean. Workflow note: executor recovered from cwd-drift via per-file cp + per-file git checkout (no destructive bulk reset, no stash), caught by per-commit cwd assertion | 2026-06-01 | de2bffe | [260601-aix-admin-ui-redesign-step-4-docs-page-conte](./quick/260601-aix-admin-ui-redesign-step-4-docs-page-conte/) |
| 260601-cx3 | Admin UI feedback round 1 — post-v1.7.0 UI tweaks (single atomic commit c1dfd8f). (1) About page: removed Endpoints card + project-links footer, wrapped remaining 4 cards in new .otto-card-grid-4 for single-row desktop layout (responsive fallback to auto-fit minmax 280px); aboutData trimmed (OllamaPathPrefix/OpenAIPathPrefix/AnthropicPathPrefix fields removed — Deps still keeps them, docsHandler still uses). (2) Docs page: removed project-links footer; page ends after Troubleshooting. (3) Docs CLI card: rebuilt around the otto-gw wrapper — POSIX + PowerShell code blocks (no bare otto-gateway binary invocations); new 3-column POSIX flag ↔ PowerShell switch ↔ Description table with 26 rows derived directly from scripts/otto-gw parse_flags() and scripts/otto-gw.ps1 param() block (em-dash for PowerShell-only -Follow). (4) Docs Files & paths card: new top-of-card callout with .otto-h3 sub-sections for Install root ($HOME/.otto-gw / $env:USERPROFILE\.otto-gw, OTTO_HOME override), Env files (user + project + overrides), Runtime files (preserved existing content). All 17 plan grep gates + bare-otto-gateway negative check pass. TRST-04 preserved (no new imports). go build + admin tests clean. Workflow improvement: planner used repo-relative paths only (lesson from steps 3+4) — executor reported NO cwd-drift incident this round | 2026-06-01 | c1dfd8f | [260601-cx3-admin-ui-feedback-round-1-trim-about-res](./quick/260601-cx3-admin-ui-feedback-round-1-trim-about-res/) |
| 260601-def | Admin UI feedback round 2 — post-v1.7.1 Docs page polish (single atomic commit 06361e5). (1) Flag/switch reference table: added .otto-flag-table CSS class with table-layout:fixed + ~25%/25%/50% column widths + white-space:nowrap on flag cols + word-wrap:break-word on description — stops "PATH" placeholders from wrapping in the Flag/Switch columns. (2) New Hooks documentation card on Docs page: 4-row chain table (RequestIDHook Pre / AuthHook Pre / PIIRedactionHook Pre / LoggingHook Pre,Post — same instance reused per cmd/otto-gateway/main.go), intro paragraph about ENABLED_HOOKS env var, PII redaction subsection with 4 env vars (PII_REDACTION_ENABLED, PII_REDACTION_MODE replace/mask/hash/drop, PII_ENABLED_ENTITIES with canonical names Email/IPv4/IPv6/SSN/CreditCard/USPhone from internal/plugin/pii/recognizers.go, PII_HASH_KEY required for hash mode), sentinel format note ([EMAIL_1] bracketed not angle-bracketed per fix 260531-pt8), two .otto-code-block examples (replace + hash mode), SENSITIVE callout about CHAT_TRACE writing raw prompts to disk BEFORE PIIRedactionHook runs. (3) Env vars table now alphabetically sorted by Name via sort.Slice in docsHandler (sort is stdlib — TRST-04 preserved). All 19 plan grep gates pass. go build + admin tests clean | 2026-06-01 | 06361e5 | [260601-def-admin-ui-feedback-round-2-flag-table-wid](./quick/260601-def-admin-ui-feedback-round-2-flag-table-wid/) |
| 260601-dzc | otto-gw wrapper -h/--help/-Help support (single atomic commit 1461294). POSIX scripts/otto-gw: usage() refactored to take optional exit-code arg ($1, default 1) — exit 0 routes heredoc to stdout, otherwise stderr; new `help\|-h\|--help) usage 0 ;;` dispatch branch BEFORE catch-all `*) usage ;;` (preserves stderr+exit1 regression behavior for unknown commands). PowerShell scripts/otto-gw.ps1: [switch]$Help added to param() block; early-return guard before switch dispatch handles -Help / "help" / "-h" / "--help" → Show-Usage exit 0; Show-Usage refactored to take [int]$ExitCode = 1 param mirroring POSIX pattern (auto-fix for shadow-bug where original Show-Usage had its own exit 1 that swallowed the guard's exit 0); explicit "help" case added to switch (defensive). Docs page (internal/admin/templates/docs.html.tmpl) CLI flags card: new prose line announcing `-h`/`--help` (POSIX) and `-Help` (PowerShell). Live smoke tests on macOS bash: `-h`/`--help`/`help` → stdout + exit 0 (87 lines); no-args / bogus → stderr + exit 1. shellcheck + bash -n clean. pwsh smoke deferred to operator (no pwsh on macOS dev box, mirroring 260531-oax policy). Drift findings recorded in SUMMARY (not fixed): --show-secrets inline-only in usage but row in Docs table; new -h/-Help intentionally not yet in formal Docs table. Workflow note: executor recovered from cwd-drift mid-task via per-file git checkout (no destructive bulk reset) | 2026-06-01 | 1461294 | [260601-dzc-otto-gw-wrappers-proper-h-help-help-supp](./quick/260601-dzc-otto-gw-wrappers-proper-h-help-help-supp/) |
| 260603-bxf | Resolve pre-existing tech debt surfaced by Phase 8.2 trust gates (two atomic commits: 8299705 gofmt + 945a9b2 go.mod). (1) `gofmt -w tests/e2e/` normalizes godoc block-quote indentation (3-space → tab) across 9 files (95 ins / 94 del, comment whitespace only — no behavior change). (2) `go.mod`: `go 1.23` → `go 1.24` so `t.Context()` usage in `internal/admin/tail_test.go` (11 call sites) + `tail_timberjack_test.go` (1 site) matches the Go version they require — pre-bump `go vet ./...` failed with `testing.Context requires go1.24 or later` × 10. Both items confirmed pre-existing on commit 468e231 (pre-Phase-8.2 main). Scope strictly limited to `tests/e2e/` per plan D-3; `gofmt -l .` (whole-repo) reports 18 additional files outside `tests/e2e/` with intentional struct field column alignment that gofmt would collapse — flagged in SUMMARY as a team decision (install gofumpt, accept gofmt-strict, or drop fmt-check gate) rather than mechanically resolved. Post-fix verification: `go vet ./...` clean, `gofmt -l tests/e2e/` clean, `go build ./...` clean, `make test-race` clean on admin + jsonformat + ollama + engine packages | 2026-06-03 | 945a9b2 | [260603-bxf-resolve-pre-existing-tech-debt-gofmt-dri](./quick/260603-bxf-resolve-pre-existing-tech-debt-gofmt-dri/) |
| 260608-m1j | Support-bundle feature: new `support` subcommand on otto-gw (bash) + otto-gw.ps1 (pwsh) + shared scripts/lib/redact.{sh,ps1} (rules don't drift), tray "Create Support Bundle…" menu item shelling to the wrapper. Bundle layout per spec (MANIFEST/env/health/logs/system/tray), redaction mandatory (no --include-secrets flag), 50MB cap with oldest-first drop, ~/.otto-gw/support output. 38/38 redact unit tests + 16/16 bundle integration tests pass; bash -n + shellcheck + go build (darwin + GOOS=windows) + go vet all clean | 2026-06-08 | 85c7734 | [260608-m1j-implement-support-bundle-feature-per-doc](./quick/260608-m1j-implement-support-bundle-feature-per-doc/) |
| 260713-pbk | Fix Windows console-window flicker (v2.1.0 regression): otto-tray GUI spawned `tasklist` every 3s (desktop poller) + `taskkill`/`powershell` (stop/install) without HideWindow/CREATE_NO_WINDOW, flashing a console each spawn. Added per-OS `hideConsole()` seam (windows: HideWindow+CREATE_NO_WINDOW; darwin: no-op) applied to `platformDesktopRunning` + `runCmd`; windows-only regression test. macOS unaffected. gofmt/gofumpt/vet clean, darwin tests pass, GOOS=windows build+vet+test-compile clean. Shipped v2.1.1. | 2026-07-13 | 33185b2 | [260713-pbk-tray-windows-console-flicker](./quick/260713-pbk-tray-windows-console-flicker/) |
| 260713-qw7 | Fix "Start OTTO Desktop" launching the Electron app with no visible window on Windows (v2.1.0 regression): `spawnDetached` launched the GUI app via `detachProcessGroup`, which sets HideWindow (SW_HIDE) — correct for the headless gateway wrapper, wrong for a GUI app (5 live OTTO.exe procs, "running", but hidden). Added per-OS `detachGUIProcess()` (windows: new process group, NO HideWindow; darwin: Setpgid) and pointed `spawnDetached` at it; gateway wrapper keeps HideWindow. Windows-only regression test. gofmt/gofumpt/vet clean, darwin tests pass, GOOS=windows build+vet+test-compile clean. Shipped v2.1.2. | 2026-07-13 | 797d4f1 | [260713-qw7-tray-desktop-start-hidden-window](./quick/260713-qw7-tray-desktop-start-hidden-window/) |
| 260713-saf | Tray "Advanced ▸ Open Folder" links: new Advanced submenu with 3 read-only links opening App folder (win: %LOCALAPPDATA%\Programs\<Brand>; mac: reveal .app), Data folder (HERMES_HOME ~/.otto \| %LOCALAPPDATA%\otto, brand-derived via env->win-registry->default), and Gateway folder (~/.otto-gw) in Explorer/Finder. explorer/open launched fire-and-forget WITHOUT HideWindow (GUI must show); only the win `reg` probe uses hideConsole. Pure read/open, nothing deleted. Replaced the cancelled tray-uninstall design. Pure unit tests; gofmt/gofumpt/vet/golangci-lint clean, darwin tests pass, GOOS=windows build+vet+test-compile clean. Shipped v2.2.0. | 2026-07-13 | 5890381 | [260713-saf-tray-open-folder-links](./quick/260713-saf-tray-open-folder-links/) |
| 260713-t1p | Brand-aware tray icon: use OTTO icon ONLY when desktop brand.json present + OTTO; else (brand.json absent — tray ships with gw, can precede desktop — or brand != OTTO) show new loop24 mark. Added loop24.png (44px darwin) + loop24.ico (16/32/48 win) from ~/loop24 bare mark; loop24 via SetIcon (colored, non-adaptive), OTTO keeps SetTemplateIcon. brandUsesLoop24/resolveBrandJSON report brand.json presence (vs today's OTTO-default); brandLoop24 atomic flag refreshed each desktop tick, read by single gateway icon-writer so a later desktop install flips within a couple ticks. Only Running/idle glyph branded; Warning/Error keep status icons; removed dead setIcon. Seam-injected tests; gofmt/gofumpt/vet/golangci-lint clean, darwin tests pass, GOOS=windows+linux build clean. Runtime check pending release. | 2026-07-13 | 755b53d | [260713-t1p-tray-brand-aware-icon](./quick/260713-t1p-tray-brand-aware-icon/) |

## Deferred Items

Items acknowledged and carried forward at each milestone close:

| Category | Item | Status | Deferred At |
|----------|------|--------|-------------|
| Concurrency | Phase 08.3.1 ACP Per-Session Stream Demux | Re-deferred (awaits multi-tenant deployment driver) | 2026-06-12 (v1.10.3 close — carry-forward from v1.8) |
| Distribution | Windows Authenticode code-signing (SEED-001 dormant) | Re-deferred (awaits code-signing cert procurement) | 2026-06-12 (v1.10.3 close — carry-forward from v1.8) |
| Reliability | WR-03 — `time.Sleep(100ms)` readiness pattern in `internal/pool/regression_rel_pool_02_test.go:134` (predates v1.10.3) | Deferred to a future phase that owns pool-test readiness signalling | 2026-06-12 (v1.10.3 close, audit-recorded) |
| Quality | Phase 20 self-review IN-01..IN-05 (half-done tooltipForState dedup, missing escapeApplescript test cases, 2-elem range alloc, dead RunUntilSignal select arm, missing tooltip.go test file) | Deferred to v1.10.4 | 2026-06-12 (v1.10.3 close) |
| Reliability | WR-01 ADR — bounded `bufio.Reader.ReadString` in `stderrDrainLoop` (todo captured 2026-06-12) | Deferred to v1.10.4 | 2026-06-12 (v1.10.3 close) |
| Lint hygiene | `golangci-lint cache clean` pre-step in `make lint` (phantom G703 from stale worktree cache) | Candidate for future hygiene phase | 2026-06-12 (v1.10.3 close, audit-recorded) |
| Tracking | 21 stale `quick_tasks` (substantive work shipped v1.5–v1.8; tracking metadata never backfilled) | Acknowledged (re-carry from v1.8) | 2026-06-12 (v1.10.3 close) |
| Verification | 4 UAT-gap + 3 verification-gap inherited operator-deferred items (Phase 02, 06, 06.1, 08, 15 — Windows/macOS GUI gates from v1.5/v1.8/v1.9; no platform access change) | Acknowledged — none are new v1.10.3 blockers | 2026-06-12 (v1.10.3 close) |
| Verification | REL-TRAY-02/03/08/09 — 4 tray operator gates awaiting hand-eyes-on confirmation on Windows + macOS GUI session | Code wired + statically verified; human verification out-of-band | 2026-06-12 (v1.10.3 close, audit-recorded) |
| Performance | `perf-baseline-vs-node` todo (pre-existing) | Deferred to v1.10.4+ | 2026-06-12 (v1.10.3 close, todo carried) |

## Session Continuity

Last session: 2026-06-12 — Milestone v1.10.3 completed and archived
Stopped at: Awaiting next milestone (v1.10.4 or next)
Resume file: —

**Next steps:**

- Tag `v1.10.3` at the milestone-close commit: `git tag -a v1.10.3 -m "v1.10.3 — Reliability Closeout: 17/17 reqs (10 Lows + REL-ACP-01 + 6 QUAL cleanups); make ci green end-to-end"`.
- Push tag: `git push origin v1.10.3` (origin: github.com:cmetech/otto-gateway.git).
- Optional: `gh release create v1.10.3` referencing v1.10.3 milestone audit + Phase 18/19/20 SUMMARYs.
- Open next milestone with `/gsd-new-milestone v1.10.4` — first candidate phase: WR-01 bounded `bufio.Reader.ReadString` ADR + Phase 20 IN-01..IN-05 tail. See PROJECT.md "Next Milestone Goals" for full candidate list.

## Operator Next Steps

- Start the next milestone with /gsd-new-milestone
