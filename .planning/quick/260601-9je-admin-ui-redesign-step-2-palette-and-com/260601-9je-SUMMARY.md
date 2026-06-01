---
phase: quick-260601-9je
plan: 01
subsystem: admin-ui
tags: [css, theming, design-system, admin, ui-refresh]
dependency_graph:
  requires:
    - quick-260601-98c (admin UI step 1 — structural scaffolding: base/dashboard/about/docs templates, header + tab nav, [data-theme] plumbing, no-op light palette)
  provides:
    - "admin.css token contract — 14 dark tokens (+5 typography) in :root, 14 light tokens in [data-theme=\"light\"]; --otto-card-hover, --otto-header (always dark in both themes), --otto-fg-muted (renamed from --otto-muted), --otto-fg-dim, --otto-accent-fg, --otto-text-{xs,sm,base,lg,xl} are NEW and now part of the public contract"
    - "additive component classes — .otto-card--accent-{healthy,warning,failed,accent}, .otto-badge--pill (with ::before status dot), .otto-btn--primary, .otto-btn--secondary, [data-theme=\"light\"] .otto-card box-shadow"
    - "smooth ~150ms theme transitions on body / .otto-card / .otto-header"
  affects:
    - internal/admin/templates/* (visually — markup unchanged but renders with new palette)
    - admin.js (visually — classList writes unchanged, but is-busy/is-paused/is-active/log-level chips/etc. now render in the new palette tokens)
tech_stack:
  added: []
  patterns:
    - "Token rename with no back-compat alias: --otto-muted dropped entirely, all ~17 callers migrated to --otto-fg-muted in the same atomic commit (clean break, single source of truth)"
    - "Always-dark header + tab-nav-on-body-bg split: the header bar uses var(--otto-header) which intentionally evaluates to the SAME dark value (#1E2128) in both :root and [data-theme=\"light\"], while the tab nav sits on the theme-able body bg and uses var(--otto-border) instead of a translucent-white hairline so it renders correctly in light mode"
    - "Additive variant naming (BEM-ish double-dash modifier): .otto-card--accent-* and .otto-badge--pill and .otto-btn--{primary,secondary} are new variants layered on top of existing base classes — no existing classList write needs to change"
key_files:
  created: []
  modified:
    - internal/admin/static/css/admin.css (full palette rewrite + 9 new component-variant rules + transition + typography changes; 1 file changed, 185 insertions, 59 deletions)
decisions:
  - "Light-mode card shadow is 0 1px 2px rgba(0,0,0,0.04) ONLY; dark mode has no shadow because it would be invisible against dark surfaces and add noise."
  - "html { color-scheme: dark; } stays a hard-coded dark value (not theme-aware) so native scrollbar / form chrome stays dark in light mode as well. Documented inline as a known step-2 tradeoff; can be revisited if it becomes a complaint."
  - "--otto-link in dark theme is the OTTO yellow #FAD22D (was blue #1174E6); this makes the pause-button paused-state render YELLOW, which is the desired step-2 outcome (yellow = OTTO accent everywhere)."
  - "Header (.otto-header) hard-codes color: #FAFAFA so text/icons stay readable on the always-dark header bar in BOTH themes; that one literal is intentional and documented inline."
metrics:
  duration: ~15 minutes
  completed: 2026-06-01
  files_modified: 1
  lines_added: 185
  lines_removed: 59
  tasks_completed: 1
  tasks_total: 1
---

# Quick 260601-9je: Admin UI Redesign Step 2 — Palette and Components Summary

CSS-only refresh that lands the Liberty-inspired visual identity on the admin page: dark theme moves from purple-heavy (#28243D / #3A3A3A) to a near-black + elevated-card palette (#1A1D24 / #242832), the step-1 no-op light overrides are replaced with a real light palette (F7F8FA / white) that keeps the header dark for Liberty parity, and admin.css gains additive component variants (.otto-card--accent-*, .otto-badge--pill, .otto-btn--primary/--secondary) plus smooth 150ms theme transitions.

## What Was Built

A single atomic CSS commit on `internal/admin/static/css/admin.css` that:

1. **Rewrote the `:root` token block** — 14 dark color tokens + 5 typography size tokens. Replaced (and renamed where needed): `--otto-bg` → `#1A1D24`, `--otto-card` → `#242832`, added `--otto-card-hover`, `--otto-header` (always-dark sentinel), `--otto-border` → `#2D3340`, renamed `--otto-muted` → `--otto-fg-muted` (with companion `--otto-fg-dim`), added `--otto-accent-fg`, changed `--otto-link` to `#FAD22D` (OTTO yellow). Kept spacing/radius/font-stack tokens untouched.
2. **Replaced the step-1 no-op `[data-theme="light"]` block** with a real light palette — `#F7F8FA` bg, `#FFFFFF` cards, `#1A1D24` fg, `#4B5563` muted, `#B8930A` link (AA-contrast yellow on light bg), and the intentional `--otto-header: #1E2128` (dark in both themes, Liberty parity). Semantic colors shifted to their darker variants (`#059669` healthy, `#D97706` warning, `#DC2626` failed, `#7C3AED` activity) for accessible contrast on light surfaces.
3. **Hardcoded-hex sweep** — replaced every bare hex literal outside the two palette blocks with a token reference:
   - `.otto-status-pill { color: #28243D; }` → `color: var(--otto-accent-fg);`
   - `.otto-badge.is-alive` solid `#1B3B2E` bg → `rgba(15, 195, 115, 0.12)` (matches sibling `.is-healthy`)
   - `.otto-badge.is-dead` solid `#3D1E1E` bg → `rgba(255, 50, 50, 0.12)` (matches sibling `.is-failed`)
   - `.otto-tab.is-active { border-bottom-color: #FAD22D; }` → `var(--otto-accent)`
   - `.otto-tab-nav` border `rgba(255,255,255,0.08)` → `var(--otto-border)` (theme-able since the tab nav sits below the dark header on the body bg)
4. **Token-name migration** — all 17 prior `var(--otto-muted)` callers replaced with `var(--otto-fg-muted)`. The old `--otto-muted` token does not exist anywhere in the file (no back-compat alias — clean break).
5. **Always-dark header rule** — `.otto-header` now declares `background: var(--otto-header); color: #FAFAFA;` plus a 150ms transition. The `#FAFAFA` literal is intentional (hard-codes readable foreground for the always-dark header surface regardless of body theme) and documented inline.
6. **New additive component classes appended at the end of the file**:
   - `.otto-card--accent-{healthy,warning,failed,accent}` — 4px left border keyed to semantic color
   - `.otto-badge--pill` (with `::before` status dot) — fully rounded pill modifier on the existing `.otto-badge` base
   - `.otto-btn--primary` — accent-filled (OTTO yellow) primary action
   - `.otto-btn--secondary` — transparent + bordered alternative
   - `[data-theme="light"] .otto-card { box-shadow: 0 1px 2px rgba(0, 0, 0, 0.04); }` — light-mode-only subtle separator shadow
7. **Smooth theme transitions** — added `transition: background-color 150ms, color 150ms, border-color 150ms;` to `body`, `.otto-card`, `.otto-header`. Added narrower hover transitions to `.otto-theme-toggle` (`background 150ms`) and `.otto-tab` (`color/border-color 150ms`).
8. **Typography refresh** — `.otto-h2` now uses `--otto-text-xl` (20px) + `text-transform: uppercase` + `letter-spacing: 0.05em` (Liberty-style section heading). `.otto-summary-label` uses `--otto-text-xs` + tighter `0.06em` tracking. All previously-bare px font sizes that were already correct switched to the matching size token for consistency.

## Verification Results

```
go build ./...                           — clean
go test ./internal/admin/...             — PASS (16.815s)
```

All 12 grep gates from the PLAN ran green except gate 5 — see Deviations.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 — Plan self-contradiction] Bare `#FAD22D` count gate vs. token spec mismatch**

- **Found during:** Verification gate 5 of Task 1
- **Issue:** The plan's `<verify>` gate 5 asserts `test "$(... grep -c '#FAD22D')" -le "2"` (at most 2 occurrences in non-comment lines). The plan's `<action>` token spec for the dark `:root` block (lines 99-108 of PLAN.md) explicitly mandates BOTH `--otto-accent: #FAD22D` AND `--otto-link: #FAD22D` in `:root`, and the `[data-theme="light"]` block (line 135) also mandates `--otto-accent: #FAD22D`. That's 3 occurrences by construction, all inside palette declaration blocks.
- **Resolution:** Implemented the explicit token spec (3 occurrences, all in palette blocks: dark `--otto-accent`, dark `--otto-link`, light `--otto-accent`). The gate's INTENT ("no bare `#FAD22D` outside the two palette blocks") is fully satisfied; only the numeric ceiling is off by one because the plan's gate didn't account for both the accent and the link tokens both pointing at OTTO yellow in dark theme. Token spec wins over gate count.
- **Files modified:** internal/admin/static/css/admin.css (lines 23, 25, 762)
- **Commit:** 99f2d37
- **Verification:** All three `#FAD22D` literals are inside `:root` (lines 16-49) or `[data-theme="light"]` (lines 750-764) — no bare `#FAD22D` exists outside the palette blocks.

### Auth Gates

None — pure CSS work, no external services touched.

### Step-1 Retrospective Honored

The step-1 SUMMARY flagged a `git stash` / `git stash pop` violation of the worktree destructive-git prohibition. This commit was prepared using `git status` and `git diff` only — no `git stash` invocation occurred at any point during execution. Tracked under `<constraints>` in the executor prompt.

## Files Touched

| File | Change Type | Notes |
|------|-------------|-------|
| `internal/admin/static/css/admin.css` | modified | 1 file, +185 / -59. Single atomic commit. |

## Untouched (Sanity-Checked)

| File | Status |
|------|--------|
| `internal/admin/templates/base.html.tmpl` | unchanged (`git diff` clean) |
| `internal/admin/templates/dashboard.html.tmpl` | unchanged |
| `internal/admin/templates/about.html.tmpl` | unchanged |
| `internal/admin/templates/docs.html.tmpl` | unchanged |
| `internal/admin/static/js/admin.js` | unchanged (verified in gate 12) |
| `internal/admin/assets.go` | unchanged |
| `internal/admin/admin.go` | unchanged |

embed.FS continues to ship admin.css automatically (no Go change required).

## Known Stubs / Follow-ups

None. The CSS refresh is complete — every dashboard section, the About + Docs Coming-Soon placeholders, the header, and the tab nav all pick up the new palette automatically through the token system.

Optional follow-up (not blocking step 3+):
- `html { color-scheme: dark; }` is intentionally hard-coded for step 2; if light-mode native scrollbar / form chrome becomes a real complaint, a one-line `html[data-theme="light"] { color-scheme: light; }` addition would close the gap.

## Threat Flags

None — CSS-only commit, no new trust boundaries, no network endpoints, no auth paths, no file-access patterns introduced.

## Self-Check: PASSED

- internal/admin/static/css/admin.css: FOUND (modified by commit 99f2d37; 1 file, +185/-59)
- commit 99f2d37: FOUND (`style(quick-260601-9je): admin UI redesign step 2 — Liberty-inspired palette + components`)
- go build ./...: CLEAN
- go test ./internal/admin/...: PASS
- Grep gates 1-12: 11 pass + 1 documented deviation (gate 5; see Deviations § 1)
