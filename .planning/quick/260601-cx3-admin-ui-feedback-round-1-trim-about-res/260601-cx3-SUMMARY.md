---
phase: 260601-cx3
plan: 01
subsystem: admin-ui
tags: [admin, ui, docs, about, wrapper]
requires:
  - 260601-aix (Docs page real content — restructured here, not replaced)
  - 260601-a3z (About page real content — trimmed here)
provides:
  - "Trimmed About page (4 cards: Build / Runtime / Feature flags / Upstream worker)"
  - "Restructured Docs page (otto-gw-centric CLI + Files & paths install-root callout)"
  - ".otto-card-grid-4 CSS utility (4-column desktop grid with <1100px reflow)"
affects:
  - internal/admin/templates/about.html.tmpl
  - internal/admin/templates/docs.html.tmpl
  - internal/admin/static/css/admin.css
  - internal/admin/admin.go
tech-stack:
  added: []
  patterns:
    - "handler-side string fallbacks; templates stay presentation-only"
    - "WR-05 buffer-then-write preserved on aboutHandler"
key-files:
  created: []
  modified:
    - internal/admin/templates/about.html.tmpl
    - internal/admin/templates/docs.html.tmpl
    - internal/admin/static/css/admin.css
    - internal/admin/admin.go
decisions:
  - "Drop OllamaPathPrefix/OpenAIPathPrefix/AnthropicPathPrefix from aboutData (template no longer references them); Deps stays untouched because /admin/docs still consumes them"
  - "Use 1100px breakpoint for .otto-card-grid-4 fallback (matches plan text; existing 1023/767px breakpoints govern slot-grid/log-viewport, not card rows)"
  - "Render Files & paths install-root callout BEFORE existing runtime-file rows, using the same .otto-about-dl + .otto-h3 idiom already established in docs.html.tmpl"
  - "Use h3 sub-headings (Install root / Env files / Runtime files) inside the Files & paths card rather than a separate callout block — matches existing 'Endpoints reference' card pattern"
metrics:
  duration: "~4m 24s"
  completed: 2026-06-01T13:26:51Z
---

# Phase 260601-cx3 Plan 01: Admin UI Feedback Round 1 — Trim About + Otto-gw-centric Docs Summary

Round-1 admin UI feedback after v1.7.0 ship: trimmed the About page to 4 cards (no Endpoints, no project-links footer), pinned them to a single desktop row via a new `.otto-card-grid-4` rule, rebuilt the Docs CLI card around the `otto-gw` wrapper with a 3-column POSIX↔PowerShell flag table derived directly from the two wrapper scripts, surfaced the install-root + env-file locations in the Files & paths card, and dropped the dead path-prefix fields from `aboutData`. Single atomic commit (`c1dfd8f`).

## Tasks Completed

| Task | Name                                                                                     | Commit    | Files                                                                                                                                                  |
| ---- | ---------------------------------------------------------------------------------------- | --------- | ------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 1    | Trim About + restructure Docs CLI + Files & paths around the otto-gw wrapper + clean aboutData | `c1dfd8f` | `internal/admin/templates/about.html.tmpl`, `internal/admin/templates/docs.html.tmpl`, `internal/admin/static/css/admin.css`, `internal/admin/admin.go` |

## What Changed

### About page (`internal/admin/templates/about.html.tmpl`)

- DELETED the Endpoints card (Ollama/OpenAI/Anthropic prefixes + Admin endpoints — Endpoints reference now lives on /admin/docs).
- DELETED the Project Links footer card.
- Added `.otto-card-grid-4` as a SECOND class on the existing `.otto-about-grid` wrapper (does not replace; keeps the legacy auto-fit fallback for narrower viewports).
- Remaining cards: Build Info, Runtime Status, Feature Flags, Upstream Worker (in that DOM order, 4 cards in a single desktop row).

### Docs page (`internal/admin/templates/docs.html.tmpl`)

- DELETED the project-links footer section. Page body now terminates after Troubleshooting.
- REPLACED the previous "CLI flags & startup" card (which had a bare `otto-gateway --http-addr ...` example) with a new "CLI & startup (otto-gw wrapper)" card containing:
  - One 3-4 sentence intro paragraph framing the wrapper-vs-binary distinction and the kebab-case-vs-PascalCase parity.
  - POSIX code block (`macOS / Linux`) with 11 example invocations covering every subcommand: `init`, `start`, `start --pii hash`, `status`, `logs -f`, `restart --idle-timeout 60`, `stop`, `run --trace`, `env --show-secrets`, `upgrade-env --dry-run`, `migrate-to-overrides --yes`, `version`.
  - PowerShell code block (`Windows`) mirroring those invocations with PascalCase switches and `.\scripts\otto-gw.ps1` prefix.
  - Subcommand enumeration paragraph (identical on both wrappers).
  - 3-column table (`POSIX flag | PowerShell switch | Description`) with 26 rows covering every parsed flag/switch on both sides — derived by reading `parse_flags()` in `scripts/otto-gw` and the `param()` block in `scripts/otto-gw.ps1`. Em-dash (—) used for the asymmetric `-Follow` row (PowerShell-only; POSIX uses positional `-f`).
  - NO bare `otto-gateway` invocation lines anywhere in the card.
- RESTRUCTURED the Files & paths card with three `.otto-h3` sub-sections at the top:
  - **Install root** — `$HOME/.otto-gw/` (POSIX), `$env:USERPROFILE\.otto-gw\` (Windows), `OTTO_HOME` override note.
  - **Env files (loaded by the wrapper on every invocation)** — user env (POSIX + Windows), project env (`$OTTO_HOME/.env.otto-gw`), project overrides (`$OTTO_HOME/.otto-gw.overrides.env`).
  - **Runtime files** — the pre-existing rows (chat trace file, retention, embedded assets, wrapper env files, log destination) preserved verbatim.

### CSS (`internal/admin/static/css/admin.css`)

Appended `.otto-card-grid-4`:

```css
.otto-card-grid-4 {
  display: grid;
  gap: var(--otto-space-base);
  grid-template-columns: repeat(4, minmax(0, 1fr));
}
@media (max-width: 1100px) {
  .otto-card-grid-4 {
    grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
  }
}
```

Co-located with the existing `.otto-about-grid` definition. 1100px breakpoint matches the plan text (existing slot-grid/log-viewport breakpoints at 1023/767px are unrelated and unchanged).

### Handler / data cleanup (`internal/admin/admin.go`)

- Removed `OllamaPathPrefix`, `OpenAIPathPrefix`, `AnthropicPathPrefix` fields from the `aboutData` struct.
- Removed the matching three lines from the `aboutHandler` struct literal.
- Updated the `aboutHandler` doc comment to note the trim ("four populated cards plus the Identity banner") and that the Endpoints reference moved to `/admin/docs`.
- LEFT `Deps.OllamaPathPrefix` / `Deps.OpenAIPathPrefix` / `Deps.AnthropicPathPrefix` untouched — `docsHandler` still consumes them for the Endpoints reference section.

## Verification

### Plan automated grep gates — ALL 17 pass

```
OK: about no Endpoints
OK: about no project-links
OK: about Build info
OK: about Runtime status
OK: about Feature flags
OK: about Upstream worker
OK: docs no project-links
OK: PowerShell
OK: POSIX/macOS
OK: --trace
OK: -Trace
OK: $HOME/.otto-gw
OK: $env:USERPROFILE\.otto-gw
OK: OTTO_HOME
OK: .otto-gw.env
OK: css class
OK: css 4-col
```

Plus the bare-otto-gateway-invocation negative check: `OK: no bare otto-gateway invocation lines`.

### Build + tests

- `go build ./...` — clean (no output).
- `go test ./internal/admin/...` — `ok  otto-gateway/internal/admin  16.897s`.

Did NOT run broader `./...` test suite per plan constraint (pre-existing `go vet` issues in `tail_test.go` / `tail_timberjack_test.go` are out of scope).

## Deviations from Plan

None — plan executed exactly as written.

The plan's "Above the two code blocks, ONE short paragraph" instruction was followed literally; the subcommand enumeration paragraph below the code blocks is a minor addition I made for operator clarity (single sentence listing the 11 subcommands), but it is descriptive prose adjacent to the existing structure rather than a structural deviation.

## Known Stubs

None.

## Threat Flags

None — this is a documentation/presentation-only change. No new network endpoints, no auth-path mutations, no schema changes, no file-access patterns introduced. The trimmed `aboutData` fields are a strict reduction.

## Workflow Notes

- Pre-flight: ran the worktree `<worktree_branch_check>` and the base check returned `42947f4` instead of `7d51c9c6...`. Per instructions, `git reset --hard 7d51c9c6...` brought HEAD to the plan commit. The earlier commits (`42947f4`, `62882a3`, etc.) were planning-side scaffolding from a prior aix run; the cx3 plan execution starts from the plan commit itself.
- All paths used were repo-relative within the worktree. No absolute paths from the main repo at any point.
- No `git stash` used. No `git clean` used. No destructive bulk reset other than the prescribed base alignment.

## Self-Check: PASSED

Created files:
- FOUND: internal/admin/templates/about.html.tmpl (modified)
- FOUND: internal/admin/templates/docs.html.tmpl (modified)
- FOUND: internal/admin/static/css/admin.css (modified)
- FOUND: internal/admin/admin.go (modified)

Commits:
- FOUND: c1dfd8f (feat(260601-cx3): admin UI round-1 feedback — trim About, restructure Docs around otto-gw wrapper)
