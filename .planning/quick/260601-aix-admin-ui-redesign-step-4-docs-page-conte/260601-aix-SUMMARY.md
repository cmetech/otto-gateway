---
phase: quick-260601-aix
plan: 01
subsystem: admin-ui
tags: [admin-ui, templates, docs-page, env-reference]
requires:
  - quick-260601-98c (admin UI step 1 — structural scaffolding)
  - quick-260601-9je (admin UI step 2 — Liberty palette)
  - quick-260601-a3z (admin UI step 3 — About page content)
provides:
  - GET /admin/docs operator reference page (env vars + CLI flags + endpoints + troubleshooting)
  - admin.Deps fields ChatTraceFile (string) + ChatTraceMaxAgeDays (int)
  - unexported types: envVarRow, cliFlagRow, docsData
affects:
  - internal/admin/admin.go (Deps + handler rewrite)
  - cmd/otto-gateway/main.go (Deps wiring)
  - internal/admin/templates/docs.html.tmpl (full replacement)
  - internal/admin/static/css/admin.css (appended block)
tech-stack:
  added:
    - stdlib strconv (admin package; TRST-04-safe)
  patterns:
    - WR-05 buffer-then-write render (mirrors aboutHandler)
    - view-model-in-handler / template-stays-presentational
    - hand-mirrored env/flag seed lists (avoids importing internal/config — TRST-04)
    - sensitive-value display: (set)/(unset) for AUTH_TOKEN + ALLOWED_IPS
key-files:
  created: []
  modified:
    - internal/admin/admin.go
    - cmd/otto-gateway/main.go
    - internal/admin/templates/docs.html.tmpl
    - internal/admin/static/css/admin.css
decisions:
  - Render ALLOWED_IPS as (set)/(unset) same as AUTH_TOKEN — not secret, but CIDR lists in a table are noisy and break the at-a-glance affordance
  - 12 rows whose Deps fields don't yet exist render literal "(see startup log)" rather than extending Deps further (deferred to a follow-up step)
  - Two atomic commits (Go / UI split) — mirrors step 3's commit cadence
metrics:
  duration_minutes: 11
  tasks_completed: 2
  files_modified: 4
  completed: 2026-06-01
---

# Phase quick-260601-aix Plan 01: Admin UI Redesign Step 4 — Docs Page Real Content Summary

Replaced the `/admin/docs` "Coming soon" placeholder with a self-contained operator reference page driven by a hand-mirrored env-var + CLI-flag seed list populated with live `h.deps.*` current values, plus files & paths, endpoints reference, basic admin usage, and troubleshooting sections — closing the last placeholder route from the step-1 scaffolding.

## What Changed

### Go side (commit `c6e5531`)
- **`admin.Deps`** gained two new fields appended after the step-3 block:
  - `ChatTraceFile string`
  - `ChatTraceMaxAgeDays int`
- **New unexported package-level types** declared near `aboutData`:
  - `envVarRow{Name, Default, Description, CurrentValue string}` — one row of the env-vars reference table.
  - `cliFlagRow{Flag, EnvMapping, Notes string}` — one row of the flag/env mapping table.
  - `docsData` — render-time view-model (TabActive, PageTitle, Version, Commit, EnvVars, CliFlags, ChatTraceEnabled/File/MaxAgeDays, Ollama/OpenAI/Anthropic path prefixes).
- **`docsHandler` rewritten** to build:
  - 27-row `envVars` literal — exact Names, Defaults, Descriptions verbatim from the plan; CurrentValue computed inline from `h.deps.*` with safety helpers:
    - `boolOnOff(bool) string` (local closure) — "on"/"off".
    - `truncate(s, n)` (local closure) — used on KIRO_ARGS join only.
    - `authCurrent` → `(set)` when `h.deps.AuthEnabled`, else `(unset)` — **NEVER plaintext**.
    - `allowedIPsCurrent` → same on/off pattern for ALLOWED_IPS.
    - `chatTraceFileCurrent` → `h.deps.ChatTraceFile` when ChatTrace enabled, else `(disabled — CHAT_TRACE=false)`.
    - `streamIdleCurrent` → `"disabled"` when 0, else `fmt.Sprintf("%ds", n)`.
  - 17-row `cliFlags` literal mirroring `internal/config/config.go LoadArgs()` flag declarations.
  - Renders via WR-05 buffer-then-write (`docsTemplate.ExecuteTemplate(&buf, "base", data)`) — identical pattern to `aboutHandler`.
- **`cmd/otto-gateway/main.go`** admin.Handler Deps literal gained two field assignments adjacent to `ChatTrace: cfg.ChatTrace`:
  - `ChatTraceFile:       cfg.ChatTraceFile,`
  - `ChatTraceMaxAgeDays: cfg.ChatTraceMaxAgeDays,`
- **New import**: stdlib `strconv` (used for `strconv.Itoa(h.deps.PoolSize)` and `strconv.Itoa(h.deps.ChatTraceMaxAgeDays)`). TRST-04 preserved — no new non-stdlib / internal-package imports.

### UI side (commit `de2bffe`)
- **`internal/admin/templates/docs.html.tmpl`** fully replaced — 7-section operator reference page wrapped in `<main class="otto-page">`:
  1. Intro card (h1 "Documentation" + cross-link to Dashboard/About + "server-rendered snapshot" disclaimer).
  2. Environment variables card — scrollable `.otto-docs-table-scroll` container with sticky-header `.otto-docs-env-table`, ranges `{{range .EnvVars}}` (Variable / Default / Description / Current value).
  3. Files & paths card — `dl.otto-about-dl` with chat-trace file + retention + admin asset embed + wrapper env-file load order + log destination.
  4. CLI flags & startup card — basic launch + wrapper run + `--trace` mode in `.otto-code-block` `<pre>` elements, then full flag/env mapping table `{{range .CliFlags}}`.
  5. Endpoints reference card — three subsections (Admin auth-exempt / Public API surfaces with live `{{.OllamaPathPrefix}}` etc / Internal `/health`).
  6. Basic admin usage card — `.otto-docs-bullets` explaining status pill, pool slots, sessions, log tail.
  7. Troubleshooting card — `.otto-docs-bullets` covering 5 most common operator pain points (degraded mode, down state, 120s hang, chat-trace warning, log location).
  8. Footer card — repository link (`github.com/cmetech/otto_app`) mirroring About page footer.
- **`internal/admin/static/css/admin.css`** — appended under banner `/* ====== Quick 260601-aix — admin UI redesign step 4 (Docs page) ====== */` with 9 new classes:
  - `.otto-docs-intro h1 { margin-top: 0 }`
  - `.otto-docs-table-scroll` — max-height 600px scroll container with border.
  - `.otto-docs-env-table` + sticky header, `td.current code` foreground color.
  - `.otto-docs-flags-table` mirroring env table treatment.
  - `.otto-code-block` — monospace `<pre>` block with palette tokens.
  - `.otto-h3` — sub-heading style for Endpoints subsections.
  - `.otto-docs-endpoint-list` + `.otto-docs-bullets` — list styles.
  - `.otto-docs-footer` — centered link row.
  All rules reuse existing palette/spacing/typography tokens — no new design tokens added.

## Acceptance — All 13 End-to-End Checks PASS

Verified with sentinel env (`AUTH_TOKEN='sentinel-aix-do-not-render-12345' POOL_SIZE=4 HTTP_ADDR=127.0.0.1:18099 go run ./cmd/otto-gateway`):

| # | Check | Result |
|---|-------|--------|
| 1 | "Environment variables" section present | OK |
| 2 | HTTP_ADDR row present | OK |
| 3 | KIRO_CMD row present | OK |
| 4 | POOL_SIZE row present | OK |
| 5 | CHAT_TRACE row present | OK |
| 6 | "Files &amp; paths" section present | OK |
| 7 | "Endpoints reference" section present | OK |
| 8 | "Troubleshooting" section present | OK |
| 9 | Repository footer link present | OK |
| 10 | AUTH on/off rendered (`(set)` or `(unset)`) | OK |
| 11 | **Sentinel AUTH_TOKEN plaintext NOT in response** | OK (no leak) |
| 12 | Live current value visible (`:18099` or `>4<` or `30s`) | OK |
| 13 | Docs tab marked `otto-tab is-active aria-current="page"` | OK |

Response size: 18854 bytes. Server bootstrap to first 200 on `/admin/docs`: ~5s (includes go build).

## Build / Test Gates

- `go build ./...` — clean (after each commit + final verification).
- `go test ./internal/admin/... -count=1` — clean (after each commit + final verification).
- Pre-existing `tail_test.go` / `tail_timberjack_test.go` go vet errors NOT touched (per plan constraint).

## Grep Gates

All plan grep gates pass — see verification output. Key ones:
- `grep -E 'ChatTraceFile\s+string' internal/admin/admin.go` — present.
- `grep -E 'ChatTraceMaxAgeDays\s+int' internal/admin/admin.go` — present.
- `grep -E 'ChatTraceFile:\s+cfg\.ChatTraceFile' cmd/otto-gateway/main.go` — present.
- `grep -E 'type docsData struct' internal/admin/admin.go` — present.
- `grep -E '\{\{range \.EnvVars\}\}' internal/admin/templates/docs.html.tmpl` — present.
- `grep -E '\{\{range \.CliFlags\}\}' internal/admin/templates/docs.html.tmpl` — present.
- `! grep 'Coming soon' internal/admin/templates/docs.html.tmpl` — confirmed removed.
- `grep -E '\.otto-docs-table-scroll' internal/admin/static/css/admin.css` — present.
- `grep -E '\.otto-code-block' internal/admin/static/css/admin.css` — present.
- TRST-04 boundary: no internal package imports added (only stdlib `strconv` new). Verified clean.

## Deviations from Plan

### Workflow Recovery

**1. [Rule 3 — Blocking issue] CWD-drift recovery between Read-context and Write-paths**
- **Found during:** Task 1, before first commit.
- **Issue:** The plan body instructed absolute paths rooted at `/Users/coreyellis/code/github.com/cmetech/otto_app/otto-gateway/` for all Edit calls, but the worktree root is `.claude/worktrees/agent-ac94a1f49a17e1182/` under that. Edits were applied to the main repo, not the worktree. Per-commit cwd assertion in the orchestrator protocol caught the divergence — the worktree showed no changes while the main repo had the diff.
- **Fix:** Copied the modified `internal/admin/admin.go` + `cmd/otto-gateway/main.go` from the main repo into the worktree via `cp`, then `git checkout -- <file>` (per-file revert, **NOT** blanket reset) on the main repo to restore it to clean. Worktree branch then had the diff in the right place. No `git clean`, no `git stash`, no blanket `git checkout -- .`.
- **Subsequent Task 2 edits** used absolute paths rooted at the worktree root explicitly to avoid recurrence.
- **Files affected:** none net — main repo restored to HEAD, worktree carried both commits as designed.
- **Commits:** none for the recovery itself (zero-diff intermediate state).

### Issues fixed

None — both tasks executed as planned. No bugs, missing functionality, or blocking issues encountered in code.

## Known Stubs

None — all 27 env-var rows render a CurrentValue (live `h.deps.*` for the 15 Deps-backed rows, literal `"(see startup log)"` for the 12 not yet in Deps). The literal-string rows are an intentional explicit stub documented in the plan's seed table and the page's intro paragraph; extending Deps further is a follow-up step. No empty/null/placeholder cells.

## Commits

- `c6e5531` — refactor(quick-260601-aix): extend admin.Deps with chat-trace surfacing + docsData (step 4 part 1)
- `de2bffe` — feat(quick-260601-aix): Docs page real content — env vars + flags + endpoints + troubleshooting (step 4 part 2)

## Self-Check: PASSED

Files verified to exist:
- FOUND: internal/admin/admin.go (with docsData type)
- FOUND: cmd/otto-gateway/main.go (with ChatTraceFile wiring)
- FOUND: internal/admin/templates/docs.html.tmpl (no "Coming soon")
- FOUND: internal/admin/static/css/admin.css (with .otto-docs-table-scroll)

Commits verified to exist on `worktree-agent-ac94a1f49a17e1182`:
- FOUND: c6e5531
- FOUND: de2bffe
