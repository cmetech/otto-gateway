---
gsd_state_version: 1.0
milestone: v1.6
milestone_name: Tooling Cleanup
status: planning
last_updated: "2026-06-07T00:56:58.244Z"
last_activity: 2026-06-07
progress:
  total_phases: 2
  completed_phases: 0
  total_plans: 0
  completed_plans: 0
  percent: 0
---

# Project State

## Project Reference

See: .planning/PROJECT.md (updated 2026-05-23)

**Core value:** All three API surfaces (OpenAI for Pi SDK, Ollama for LangFlow, Anthropic for loop24-client/GSD Pi) serve their respective clients without those clients knowing kiro-cli exists, with one place to enforce policy.
**Current focus:** Phase 10 — golangci-lint v2 cleanup + re-gate (v1.6 Tooling Cleanup)

## Current Position

Phase: 10 — golangci-lint v2 cleanup + re-gate
Plan: — (awaiting `/gsd-plan-phase 10`)
Status: Roadmap drafted; ready for phase planning
Last activity: 2026-06-07 — v1.6 roadmap created (Phases 10, 11)

## Performance Metrics

**Velocity:**

- Total plans completed: 42
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

**Recent Trend:**

- Last 5 plans: n/a (no plans executed yet)
- Trend: n/a

*Updated after each plan completion*
| Phase 08.2 P01 | 45m | 8 tasks | 10 files |
| Phase 08.4 P01 | 35m | 3 tasks (R/G/F) + Task H pending | 7 files |

## Accumulated Context

### Roadmap Evolution

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

## Deferred Items

Items acknowledged and carried forward from previous milestone close:

| Category | Item | Status | Deferred At |
|----------|------|--------|-------------|
| Concurrency | Phase 08.3.1 ACP Per-Session Stream Demux | Re-deferred to v1.7 | 2026-06-07 (v1.6 narrow-scope decision) |

## Session Continuity

Last session: 2026-06-07T00:56:58.244Z
Stopped at: v1.6 roadmap drafted (Phase 10 lint cleanup, Phase 11 fmt + pre-commit gate); awaiting `/gsd-plan-phase 10`
Resume file: .planning/ROADMAP.md

## Operator Next Steps

- Begin Phase 10 with `/gsd-plan-phase 10` (golangci-lint v2 cleanup + re-gate)
