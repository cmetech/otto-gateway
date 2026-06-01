---
phase: 260601-def
plan: 01
subsystem: admin-ui
tags: [admin, ui, docs, hooks, pii]
requires:
  - 260601-cx3 (round-1 Docs page restructure around otto-gw wrapper)
provides:
  - DOCS-FLAG-WRAP — Flag/Switch table renders --env-file PATH / -EnvFile PATH on a single line
  - DOCS-HOOKS-CARD — /admin/docs has Hooks card with default 4-hook chain + PII env vars + sentinel format + SENSITIVE CHAT_TRACE callout
  - DOCS-ENV-ALPHA — Environment variables table rendered alphabetically by Name
affects:
  - internal/admin (template + handler + CSS)
tech-stack:
  added:
    - sort (stdlib) in internal/admin/admin.go
  patterns:
    - handler-side sort (presentation-only template) — extends round-1 260601-cx3 pattern of handler-side fallbacks
    - additive CSS class (.otto-flag-table layered on existing .otto-docs-flags-table) to avoid disturbing the env-vars table styling
key-files:
  created: []
  modified:
    - internal/admin/templates/docs.html.tmpl
    - internal/admin/static/css/admin.css
    - internal/admin/admin.go
decisions:
  - Additive .otto-flag-table class instead of mutating .otto-docs-flags-table — operator explicitly said "DO NOT disturb env-vars table styling"; safer to layer than to modify the shared baseline
  - Handler-side sort.Slice on envVars (not template-side sort) — keeps docs.html.tmpl presentation-only and matches round-1 pattern; sort is stdlib so TRST-04 boundary preserved
  - cliFlags intentionally NOT sorted — that table has its own per-subcommand operator-facing grouping preserved by round-1 (out of scope here)
metrics:
  duration: ~3 minutes
  completed: 2026-06-01
  tasks: 1/1
  files: 3
---

# Phase 260601-def Plan 01: Admin UI Round-2 Feedback Summary

One-liner: Fixed Flag/Switch column wrap on /admin/docs, added a Hooks documentation card naming the default 4-hook chain + PII redaction env vars + bracketed sentinel format + SENSITIVE CHAT_TRACE callout, and sorted the Environment variables table alphabetically by Name.

## What Shipped

Single atomic commit `06361e5` modifying three files in `internal/admin/`:

1. **`internal/admin/static/css/admin.css`** — appended a `/* 260601-def — round-2 docs polish */` block at end of file with a new `.otto-flag-table` class providing `table-layout: fixed`, `width: 100%`, 25/25/50 column-width split, `white-space: nowrap` on the flag/switch columns, and `word-wrap`/`overflow-wrap: break-word` on the description column. The existing `.otto-docs-flags-table` rule is unchanged — `.otto-flag-table` is additive only.
2. **`internal/admin/templates/docs.html.tmpl`** — (a) added `otto-flag-table` as a second class on the existing `<table class="otto-table otto-docs-flags-table ...">` in the CLI & startup card; (b) inserted a new `<section class="otto-card">` titled "Hooks" between the Endpoints reference card and the Basic admin usage card. The Hooks card contains: intro paragraph mentioning `ENABLED_HOOKS`, a 4-row hook-chain table (RequestIDHook / AuthHook / PIIRedactionHook / LoggingHook[Pre,Post] with descriptions), a "PII redaction" subsection naming all six recognizers (Email, IPv4, IPv6, SSN, CreditCard, USPhone), a definition list of the four PII env vars (`PII_REDACTION_ENABLED`, `PII_REDACTION_MODE`, `PII_ENABLED_ENTITIES`, `PII_HASH_KEY`) with defaults + one-line descriptions, a paragraph explaining bracketed sentinels with `[EMAIL_1]` / `[EMAIL:h-abc12345]` examples and the 260531-pt8 angle-bracket-vs-kiro-cli rationale, two `.otto-code-block` examples (replace mode + hash mode), and a SENSITIVE callout noting `CHAT_TRACE=true` writes raw prompts before PIIRedactionHook runs.
3. **`internal/admin/admin.go`** — added `"sort"` to the stdlib import block (alphabetical position between `runtime` and `strconv`) and inserted `sort.Slice(envVars, func(i, j int) bool { return envVars[i].Name < envVars[j].Name })` immediately after the `envVars := []envVarRow{ ... }` literal closes and before the `cliFlags := []cliFlagRow{ ... }` literal opens. The template's `{{range .EnvVars}}` iteration is unchanged — the render is naturally alphabetical because the slice is.

## Verification

- `go build ./...` — clean.
- `go test ./internal/admin/...` — passes (`ok otto-gateway/internal/admin 16.830s`).
- All 19 grep gates from the plan's `verify.automated` block pass:
  - CSS: `otto-flag-table`, `table-layout: fixed`, `white-space: nowrap`
  - Template: `otto-docs-flags-table otto-flag-table`, `{{range .EnvVars}}`, `>Hooks<`, `RequestIDHook`, `AuthHook`, `PIIRedactionHook`, `LoggingHook`, `PII_REDACTION_ENABLED`, `PII_REDACTION_MODE`, `PII_ENABLED_ENTITIES`, `PII_HASH_KEY`, `ENABLED_HOOKS`, `[EMAIL_1]`, `CHAT_TRACE`
  - Handler: `sort.Slice(envVars`, `"sort"`
- TRST-04 boundary preserved: only new import is stdlib `sort` — no `internal/*` imports added to `internal/admin`.
- WR-05 buffer-then-write flow in `docsHandler` intact — only the slice ordering changed; the `bytes.Buffer` path through `docsTemplate.ExecuteTemplate` is unchanged.

## Deviations from Plan

None — plan executed exactly as written. All three edits applied verbatim, single atomic commit as specified.

## Files Modified

| File                                          | Change                                                                          |
| --------------------------------------------- | ------------------------------------------------------------------------------- |
| internal/admin/static/css/admin.css           | +18 lines appended at EOF: `.otto-flag-table` rule block                        |
| internal/admin/templates/docs.html.tmpl       | +51 lines (Hooks card) + 1-char class addition on flag table                    |
| internal/admin/admin.go                       | +1 import line (`"sort"`) + 3 lines (`sort.Slice(envVars, ...)`)                |

## Commit

- `06361e5` — feat(260601-def): admin UI round-2 feedback — flag table wrap fix, Hooks doc card, alphabetical env vars

## Self-Check: PASSED

- File internal/admin/admin.go — FOUND
- File internal/admin/static/css/admin.css — FOUND
- File internal/admin/templates/docs.html.tmpl — FOUND
- Commit 06361e5 — FOUND in git log
