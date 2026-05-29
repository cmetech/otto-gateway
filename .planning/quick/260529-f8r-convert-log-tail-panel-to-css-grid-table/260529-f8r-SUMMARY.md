---
phase: quick-260529-f8r
plan: 01
subsystem: admin-ui
tags: [admin-ui, log-tail, css-grid, refactor, ux]
requires: [phase-6.1]
provides:
  - "Log Tail panel as 4-column CSS grid (Time | Level | Source | Message) with sticky header"
  - "Per-level chip palette (.is-debug/.is-info/.is-warn/.is-error/.is-unknown) using existing palette tokens"
  - "parseLogLine returning {level,time,msg,source,raw} (extended shape) for both JSON and slog-text branches"
  - "dataset.raw plumbing so grep filter matches the original SSE line, not concatenated cell text"
  - "Full-width fallback row for unparseable subprocess / raw stderr lines (nothing dropped)"
affects:
  - internal/admin/templates/index.html.tmpl
  - internal/admin/static/js/admin.js
  - internal/admin/static/css/admin.css
tech-stack:
  added: []
  patterns:
    - "CSS grid with `display: contents` on row containers so child cells participate in parent grid (one row per logical entry, but cells flow into 4 grid columns)"
    - "Sticky header pinned by per-cell `position: sticky; top: 0` + matching `--otto-bg` background so scrolling rows clip cleanly"
    - "dataset.raw persistence on rendered rows so filter re-derive matches the original SSE line (decouples render shape from filter semantics)"
key-files:
  created: []
  modified:
    - internal/admin/templates/index.html.tmpl
    - internal/admin/static/js/admin.js
    - internal/admin/static/css/admin.css
decisions:
  - "Header row rendered ONCE in the template (not injected by JS) — selector compatibility, no JS bootstrap order coupling, header survives a full DOM cap eviction cycle"
  - "Color lives on the level chip, not the row — removes `.otto-log-line.is-*` row-wide rules; rest of the row stays default fg for scanability"
  - "Fallback discriminator is `level === null && time === '' && msg === ''` (all three) — a line that parsed a level but had no msg/time still gets a 4-cell row with parsed.raw as the message, so the operator never loses content"
  - "1000-row DOM cap counts only `.otto-log-row, .otto-log-row-fallback` selector matches — header and empty placeholder are never evicted"
  - "Used `minmax(0, max-content)` for first three columns + `1fr` for message — prevents pathological column blowout when a source value is unexpectedly long while letting message wrap naturally"
metrics:
  duration: "~25 min"
  completed: "2026-05-29"
---

# Quick Task 260529-f8r: Convert Log Tail Panel to CSS Grid Table — Summary

Converted the `/admin` Log Tail panel from a one-line-per-row monospace stream into a 4-column CSS-grid table (Time | Level | Source | Message) with a sticky header, per-level colored chips, and wrapping message cells — without changing any backend behavior, SSE wire shape, or existing functional surface (pause, grep, level filter, dedup, autoscroll, 1000-line cap, textContent-only safety).

## One-Liner

Refactored admin log tail UI from text stream to 4-column CSS grid with sticky header and level chips; preserved every Phase 6.1 invariant (T-6.1-16 textContent-only, WR-06 persisted level, WR-03/04 backfill dedup, 1000-row cap).

## Changes by File

### internal/admin/templates/index.html.tmpl

- Replaced the single-line viewport child with a 4-cell header row + the existing empty-state placeholder, all as direct children of the same `<div class="otto-log-viewport" data-log-viewport ...>` outer container.
- The header row uses `class="otto-log-header-row"` with four cells (`otto-log-cell otto-log-cell-{time|level|source|message}`) whose textContent is `Time`, `Level`, `Source`, `Message`.
- All selector-load-bearing attributes preserved on the outer element: `class="otto-log-viewport"`, `data-log-viewport`, `role="log"`, `aria-live="polite"`, `aria-relevant="additions"`.
- Header text is static — no new server-side template variables.

### internal/admin/static/css/admin.css

- `.otto-log-viewport` switched from a plain block to `display: grid; grid-template-columns: minmax(0, max-content) minmax(0, max-content) minmax(0, max-content) 1fr; align-content: start;` while keeping `height: 480px; overflow-y: auto;` and the existing font/color/background tokens. Removed the prior viewport `padding` so per-cell padding can drive layout cleanly.
- Added `.otto-log-header-row { display: contents; }` and per-cell sticky styling: `position: sticky; top: 0; background: var(--otto-bg); z-index: 1; padding: 4px var(--otto-space-sm); font: 600 11px/1.4 var(--otto-font-ui); color: var(--otto-muted); border-bottom: 1px solid var(--otto-border); text-transform: uppercase; letter-spacing: 0.04em;`.
- Added `.otto-log-row { display: contents; }` and default cell rules: `.otto-log-cell { padding: 2px var(--otto-space-sm); white-space: nowrap; }`. Message cell override: `.otto-log-cell-message { white-space: pre-wrap; word-break: break-word; }` so long messages wrap inside the message column.
- Added `.otto-log-row-fallback { display: contents; }` + `.otto-log-row-fallback .otto-log-cell-fallback { grid-column: 1 / -1; padding: 2px var(--otto-space-sm); white-space: pre-wrap; word-break: break-word; color: var(--otto-muted); }` for the full-width fallback variant (unparseable subprocess / raw stderr lines).
- Added level chip palette: `.otto-log-level-chip` base + `.is-debug` (muted bg), `.is-info` (fg bg), `.is-warn` (warning bg), `.is-error` (failed bg), `.is-unknown` (transparent w/ border). All use existing palette tokens for theme parity.
- `.otto-log-empty` got `grid-column: 1 / -1;` so the empty-state placeholder spans all 4 columns when visible.
- REMOVED `.otto-log-line.is-debug`, `.otto-log-line.is-info`, `.otto-log-line.is-warn`, `.otto-log-line.is-error` row-wide color rules and the prior `.otto-log-line { white-space: pre-wrap; word-break: break-all; padding: 2px 0; }` block — color now lives on the chip, not the row.

### internal/admin/static/js/admin.js

- **parseLogLine shape change.** Return type extended from `{ level, msg }` to `{ level, time, msg, source, raw }`. JSON branch reads `obj.time`, `obj.msg`, and prefers `obj.logger` over `obj.source`. slog-text branch matches `/\btime=("[^"]*"|\S+)/`, `/\bmsg=("[^"]*"|\S+)/`, and `/\b(logger|source)=("[^"]*"|\S+)/`, with a small `unquote` helper that strips surrounding double-quotes. Fallback return shape is `{ level: null, time: '', msg: '', source: '', raw: line }`.
- **appendLine grid construction.** Rewrote to acquire the viewport via `[data-log-viewport]` (unchanged selector), hide `[data-log-empty]` on first line (unchanged), then branch:
  - **Fallback row** when `parsed.level === null && parsed.time === '' && parsed.msg === ''`: create a `.otto-log-row-fallback` div with `dataset.level='unknown'`, `dataset.raw=parsed.raw`, and a single `.otto-log-cell .otto-log-cell-fallback` child whose textContent is `parsed.raw`.
  - **Standard row** otherwise: create a `.otto-log-row` div with `dataset.level=parsed.level||'unknown'`, `dataset.raw=parsed.raw`, and four child cells (`otto-log-cell otto-log-cell-{time|level|source|message}`). The level cell contains a `<span class="otto-log-level-chip is-{level}">` whose textContent is the uppercase level name (or `?` for unknown). The message cell falls back to `parsed.raw` when `parsed.msg` is empty so the operator never loses content.
- **dataset.raw plumbing (load-bearing correctness change).** Filter visibility now calls `matchesFilters(parsed, parsed.raw)` so the grep regex matches the original SSE line. Both `initLevelFilter` and `initGrepFilter` re-derive paths switched from `vp.querySelectorAll('.otto-log-line')` + `el.textContent` to `vp.querySelectorAll('.otto-log-row, .otto-log-row-fallback')` + `el.dataset.raw || ''`. This preserves prior grep behavior exactly — existing patterns keep matching.
- **1000-row DOM cap.** Eviction loop now uses `vp.querySelectorAll('.otto-log-row, .otto-log-row-fallback').length > 1000` and removes the first matching log row. The in-template header row and empty placeholder are never evicted.
- **textContent-only invariant preserved.** No `innerHTML`, no `insertAdjacentHTML` introduced anywhere new (T-6.1-16). One comment that named both APIs in a constraint description was rephrased to avoid a false positive in the audit regex.
- **No SSE / dedup changes.** `onLogEvent`, `onSSEOpen`, `onSSEError`, `logBackfillStart/End`, `logIsDupe`, `logTrackLine`, `autoScroll`, `updateNewestBadge`, `initPauseButton` are functionally untouched. Pause-buffer flush still calls `appendLine(entry.line, entry.parsed)` with the same shape; `parsed.raw` is always set because `onLogEvent` calls `parseLogLine` before buffering.

## Verification

- **Task 1 automated check (CSS + template grep):** all 12 checks PASS.
- **Task 2 automated check (JS static safety):** all 12 checks PASS (no `innerHTML`, no `insertAdjacentHTML`, dataset.raw + chip + fallback markers + 1000 cap present, selectors updated).
- **`go test ./internal/admin/... -count=1`:** PASS — `ok otto-gateway/internal/admin 16.272s`. No regressions in handler / SSE / tail tests; UI markup change is server-side test invariant by construction.
- **`go build ./...`:** clean.

## Deviations from Plan

None. Plan executed exactly as written; only adjustment was rephrasing one comment in `admin.js` (the appendLine doc-comment) so the static audit regex for `insertAdjacentHTML` does not false-positive on the constraint description itself. The underlying invariant (never call the API) is preserved.

## Commits

| Task | Hash    | Description                                                                          |
| ---- | ------- | ------------------------------------------------------------------------------------ |
| 1    | 6a5afb8 | feat(quick-260529-f8r): convert log tail viewport to 4-column CSS grid template + styles |
| 2    | 5882ded | feat(quick-260529-f8r): rewrite admin log tail JS for 4-column grid render            |

## Outstanding — Human Verification (Task 3)

Task 3 is `checkpoint:human-verify (blocking)` — the operator must build and run the gateway locally, open `/admin`, and walk through the 10-step browser smoke list in PLAN.md to confirm:

1. Sticky header row pinned at viewport top with matching background.
2. Steady-state rows render as 4 cells; level appears as a colored chip (not row color); long messages wrap inside the Message column.
3. Fallback row renders for non-slog raw lines as one full-width cell spanning all 4 columns.
4. Level filter (Warn/etc.) hides non-matching rows; "All levels" restores them.
5. Grep filter still matches substrings that appear ONLY in the raw line and are split across cells in the rendered output (validates dataset.raw plumbing).
6. Pause → "N new" badge increments → Resume flushes in order and autoscrolls.
7. Activity dot + status text behave on SSE disconnect/reconnect.
8. DevTools inspection confirms no `innerHTML` usage on rendered rows.

Resume signal: `approved` if all 10 checks pass, or describe the failing step.

## Self-Check: PASSED

- internal/admin/templates/index.html.tmpl: FOUND (modified)
- internal/admin/static/css/admin.css: FOUND (modified)
- internal/admin/static/js/admin.js: FOUND (modified)
- Commit 6a5afb8: FOUND
- Commit 5882ded: FOUND
