---
phase: quick-260601-a3z
plan: 01
subsystem: admin-ui
tags: [admin-ui, templates, theming, runtime-cfg-surfacing]
dependency_graph:
  requires:
    - quick-260601-98c (layout extraction — base.html.tmpl + about.html.tmpl placeholders)
    - quick-260601-9je (palette + component restyle — --otto-fg-muted, --otto-card-hover tokens)
  provides:
    - "/admin/about renders runtime cfg from server-side AboutData"
    - "Icon-only header theme toggle keyed on [data-theme]"
  affects:
    - internal/admin (Deps surface grew by 12 fields; aboutData view-model added)
    - cmd/otto-gateway/main.go (admin.Handler call wires the 12 new fields)
tech_stack:
  added: []  # no new deps; runtime + strings + fmt are stdlib
  patterns:
    - "Append-wins CSS cascade for theme-toggle restyle (no rule replacement, no specificity bump)"
    - "Empty-string fallbacks in the handler (not the template) — preserves template-as-presentation discipline"
key_files:
  created:
    - .planning/quick/260601-a3z-admin-ui-redesign-step-3-about-page-cont/260601-a3z-SUMMARY.md
  modified:
    - internal/admin/admin.go
    - cmd/otto-gateway/main.go
    - internal/admin/templates/base.html.tmpl
    - internal/admin/templates/about.html.tmpl
    - internal/admin/static/css/admin.css
decisions:
  - "aboutData declared as unexported package-level type (not anonymous struct) — cleaner site for future About-page helpers and survives the test boundary."
  - "Empty-string fallbacks for KiroCmd/KiroArgs/KiroCwd done in aboutHandler, NOT in about.html.tmpl — keeps the template presentation-only and the substitutions testable in Go."
  - "CSS theme-toggle restyle is APPEND-ONLY at the end of admin.css (banner-commented), relying on same-specificity cascade order to override the step-1 .otto-theme-toggle rules. No specificity bump or !important required."
  - "len(cfg.AuthToken)>0 and len(cfg.AllowedIPs)>0 derivations for AuthEnabled / IPAllowlistEnabled match the existing 'auth mode' startup log line — single source of truth for 'is this knob on?'"
metrics:
  duration: 8m40s
  completed: 2026-06-01T11:30:07Z
  tasks_completed: 2
  files_changed: 5
---

# Phase quick-260601-a3z Plan 01: Admin UI redesign step 3 — About page real content + icon theme toggle — Summary

Replace the /admin/about "Coming soon" placeholder with six populated cards driven by an `aboutData` view-model the handler builds from a 12-field-extended `admin.Deps` + `runtime.*`; swap the header's text-based theme toggle for an icon-only 32x32 button whose sun/moon SVG visibility is CSS-driven off `[data-theme]` on `<html>`.

## Tasks completed

| # | Name                                                                       | Commit    | Files                                                                                              |
| - | -------------------------------------------------------------------------- | --------- | -------------------------------------------------------------------------------------------------- |
| 1 | Extend admin.Deps + aboutHandler + main.go wiring (Go side)                | a8dcb01   | internal/admin/admin.go, cmd/otto-gateway/main.go                                                  |
| 2 | About page template + icon theme toggle + CSS (UI side)                    | f911d8f   | internal/admin/templates/base.html.tmpl, internal/admin/templates/about.html.tmpl, internal/admin/static/css/admin.css |

## What changed

### Go side (commit a8dcb01)

- `admin.Deps` gained 12 runtime cfg fields: `HTTPAddr`, `PoolSize`, `SessionTTL` (`time.Duration`), `StreamIdleTimeoutSec` (`int`), `AuthEnabled`/`IPAllowlistEnabled` (`bool`), `KiroCmd`, `KiroArgs` (`[]string`), `KiroCwd`, `OllamaPathPrefix`, `OpenAIPathPrefix`, `AnthropicPathPrefix`. Doc-comment block explains they mirror `cfg.*` and are read-only snapshots taken at wire-up time.
- New unexported package-level type `aboutData` is the render-time view-model for `/admin/about`. It includes the 12 new Deps fields plus `Version`, `Commit`, `GoVersion` (`runtime.Version()`), `GOOS`, `GOARCH`, `StartedAt` (RFC3339), `Debug`, `ChatTrace`, `TabActive`, `PageTitle`, plus computed display strings (`StreamIdleDisplay` reads `"disabled"` when zero, `"30s"` otherwise; `SessionTTL` is `.String()`-stringified to `"30m0s"`).
- `aboutHandler` substitutes human-readable fallbacks BEFORE template execution:
  - `KiroCmd` empty → `"(unset — degraded mode)"`
  - `KiroArgs` joined empty → `"(none)"`
  - `KiroCwd` empty → `"(empty)"`
  Keeps the template presentation-only.
- Imports added (all stdlib — TRST-04 preserved): `fmt`, `runtime`, `strings`. No new third-party deps.
- `cmd/otto-gateway/main.go` wires all 12 fields at the existing `admin.Handler(admin.Deps{...})` call site. `cfg.KiroCWD` (Go field name uses all-caps `CWD`) maps to `admin.Deps.KiroCwd` (mixed-case per the new Deps spec).

### UI side (commit f911d8f)

- `base.html.tmpl` theme-toggle button: text node `Theme` removed; inline sun + moon SVGs (Heroicons-style 1.5-stroke outline) inserted, each with `class="icon-sun"` / `class="icon-moon"` and `aria-hidden="true"`. `data-theme-toggle`, `aria-label="Toggle theme"`, and `title="Toggle theme"` preserved so the existing JS click handler at the bottom of the file still binds via `[data-theme-toggle]`.
- `about.html.tmpl` fully rewritten: Identity row (full-width `.otto-card.otto-about-identity` with wordmark + tagline) + responsive `.otto-about-grid` holding six `<article class="otto-card">` cards (Build Info, Runtime Status, Feature Flags, Upstream Worker, Endpoints, Project Links). Each card uses `<dl class="otto-about-dl">` with dt = label / dd = value. ChatTrace=true renders `<span class="otto-badge is-warning">SENSITIVE</span>` next to "on" on the Feature Flags card.
- `admin.css` appends a new banner-commented `Quick 260601-a3z` block:
  - `.otto-theme-toggle` icon-only restyle (32x32, transparent, hover bg from `--otto-card-hover`, scale-on-active 0.95). `.otto-theme-toggle .icon-sun, .otto-theme-toggle .icon-moon { display: none; }` baseline; `[data-theme="dark"] .otto-theme-toggle .icon-sun { display: block; }` and `[data-theme="light"] .otto-theme-toggle .icon-moon { display: block; }` reveal exactly one icon per theme.
  - `.otto-about-identity`, `.otto-about-wordmark` (32px / 700 / tracked), `.otto-about-tagline`.
  - `.otto-about-grid` uses `grid-template-columns: repeat(auto-fit, minmax(280px, 1fr))` so the layout collapses 3 → 2 → 1 columns responsively with no media-query glue.
  - `.otto-about-dl` is a two-column `max-content 1fr` grid with `--otto-fg-muted` dt and `--otto-fg` dd, plus `--otto-link` `dd a` styling for the Project Links card.

## Verification

| Gate                                                                          | Result |
| ----------------------------------------------------------------------------- | ------ |
| `go build ./...`                                                              | clean  |
| `go test ./internal/admin/... -count=1`                                       | ok     |
| `grep -c 'HTTPAddr.*cfg\.HTTPAddr' cmd/otto-gateway/main.go`                  | 2      |
| 12 Deps fields present in admin.go                                            | yes (grep matches 41 across struct + handler + aboutData) |
| `"runtime"` imported in admin.go                                              | yes    |
| TRST-04 (no internal/{pool,session,engine,server,adapter,config,plugin,canonical} imports) | preserved |
| `<svg class="icon-sun"` in base.html.tmpl                                     | 1      |
| `<svg class="icon-moon"` in base.html.tmpl                                    | 1      |
| `aria-label="Toggle theme"` preserved                                         | yes    |
| `>Theme<` text node removed                                                   | yes (no match) |
| `[data-theme="dark"] .otto-theme-toggle .icon-sun` CSS rule                   | present |
| `[data-theme="light"] .otto-theme-toggle .icon-moon` CSS rule                 | present |
| `.otto-about-grid` CSS rule                                                   | present |
| `{{.HTTPAddr}}` in about.html.tmpl                                            | present (Listen address row) |
| `{{.PoolSize}}` in about.html.tmpl                                            | present (Pool size row) |
| `KIRO_CMD` label in about.html.tmpl                                           | present |
| `Coming soon` removed                                                         | yes (no match) |

### Render smoke test (one-off, executed then deleted)

Wrote a temporary `cmd/otto-gateway/a3z_about_render_test.go` that drove `admin.Handler` with a populated `Deps` (including `ChatTrace=true` to exercise the SENSITIVE chip), called `GET /about` via `httptest`, and asserted the response body contained all of: `OTTO Gateway`, `LLM gateway with unified`, `Version`, `v1.5.0-smoke`, `Pool size`, `4`, `:18080`, `30m0s`, `30s`, `KIRO_CMD`, `(unset — degraded mode)`, `(none)`, `(empty)`, `<svg class="icon-sun"`, `<svg class="icon-moon"`, `aria-label="Toggle theme"`, `SENSITIVE`, `otto-about-grid`, `otto-about-identity`; AND did NOT contain `>Theme<` or `Coming soon`. PASS in 0.302s. The throwaway file was deleted before commit so it does not pollute the test corpus — the assertions are recorded here for the verifier.

## Deviations from plan

None. Plan executed exactly as written.

### Workflow note (operator-relevant, not a plan deviation)

The initial Edit calls for Task 1 inadvertently landed in the **main repo** rather than the worktree — the first Bash call that ran `cd /Users/.../otto-gateway && ...` reset cwd to the main checkout, and absolute paths constructed against the un-`cd`'d location of the orchestrator targeted the main repo's working tree. Caught it before commit via `git diff --stat HEAD` showing zero changes inside the worktree. Reverted with `git checkout -- internal/admin/admin.go cmd/otto-gateway/main.go` in the main repo (sanctioned per-file discard, not a blanket reset), then re-applied via worktree-rooted absolute paths. The agent role explicitly warns about this cwd-drift / absolute-path-safety hazard; documenting here so the same recovery is shorter next time. No commits ever landed on the wrong branch.

## Known stubs

None. Every visible value on the About page is driven by `cfg.*` or `runtime.*`; no hardcoded placeholder data flows to the UI. The Repository link URL (`https://github.com/cmetech/otto_app`) is verifiable against the project's canonical home — not a placeholder. The Documentation link (`/admin/docs`) routes to the existing docs page; that page is itself still a placeholder, but its content is owned by a future plan (see step-4 of the admin UI redesign series) and out of scope here.

## TDD Gate Compliance

Plan was `type: execute` (non-TDD). Not applicable.

## Self-Check: PASSED

- FOUND: internal/admin/admin.go (modified)
- FOUND: cmd/otto-gateway/main.go (modified)
- FOUND: internal/admin/templates/base.html.tmpl (modified)
- FOUND: internal/admin/templates/about.html.tmpl (modified)
- FOUND: internal/admin/static/css/admin.css (modified)
- FOUND: commit a8dcb01 (Task 1)
- FOUND: commit f911d8f (Task 2)
