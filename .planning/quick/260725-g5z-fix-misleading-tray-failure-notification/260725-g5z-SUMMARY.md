---
phase: quick-260725-g5z
plan: 01
subsystem: otto-tray
status: complete
tags: [tray, notifications, windows, stderr, ux]
requires:
  - scripts/gw load_config stderr contract
  - scripts/gw.ps1 Initialize-Config stderr contract (REL-TRAY-06)
provides:
  - firstMeaningfulLine stderr-selection helper for tray notifications
affects:
  - cmd/otto-tray failure notification bodies (5 call sites)
tech-stack:
  added: []
  patterns:
    - "Prefix-list filtering of known-informational subprocess stderr before display"
    - "Per-line TrimSpace as the CRLF normalisation strategy (no explicit \\r handling)"
key-files:
  created:
    - cmd/otto-tray/uihelpers_test.go
  modified:
    - cmd/otto-tray/uihelpers.go
    - cmd/otto-tray/tray.go
    - cmd/otto-tray/desktoptray.go
decisions:
  - "Skip wrapper informational stderr lines at the display layer rather than changing the wrappers, preserving REL-TRAY-06's stdout reservation for the support-bundle path"
  - "Fall back to the last non-empty line when stderr is informational-only, so the toast is never empty"
  - "Delete the legacy firstLine helper in the same commit as the call-site migration, since any intermediate revision would fail .golangci.yml's unused linter"
metrics:
  duration: ~25 min
  completed: 2026-07-25
  tasks: 3
  files: 4
  commits: 3
---

# Quick Task 260725-g5z: Fix Misleading Tray Failure Notification Summary

Tray failure toasts now show the actual cause (e.g. `kiro: command not found`)
instead of the wrapper's routine `loaded env file: ...` preamble line.

## What Was Built

The gw wrappers write `loaded env file: <path>` and `loaded overrides:  <path>`
to stderr on **every** invocation — deliberately, because REL-TRAY-06 reserves
stdout for the support-bundle archive path. The tray rendered `firstLine(stderr)`
into its notification body, so every failure toast on a fresh Windows install
read `Failed to start: loaded env file: C:\Users\you\.gw\.env`, which looks like
a .env loading failure while the real cause sat one or two lines below.

`firstMeaningfulLine` walks stderr line by line, trims each line, skips empties
and any line matching a known informational prefix, and returns the first
survivor. If every non-empty line is informational it returns the last one
(showing something beats showing nothing); if there is no non-empty line at all
it returns the literal `(no stderr)`.

The prefix list stops at the colon (`loaded env file:`, `loaded overrides:`) so
it matches both the one-space spelling and the two-space spelling the wrappers
actually emit. Per-line `strings.TrimSpace` strips the `\r` of a CRLF pair, so
Windows stderr needs no separate code path — which matters, since Windows is
where this was hit for real.

All five stderr-derived notification bodies migrated: `handleStart`,
`handleStop`, `handleRestart` in `tray.go`, plus `runDesktopInstall` and
`handleDesktopStop` in `desktoptray.go`. The `err.Error()` branch at
`desktoptray.go:211` was deliberately left alone — it renders a Go error, not
subprocess stderr, so it has no informational preamble to skip.

## Task Commits

| Task | Description | Commit |
|------|-------------|--------|
| 1 (RED) | Failing `TestFirstMeaningfulLine` table, 7 cases | `9e59166` |
| 1 (GREEN) | `firstMeaningfulLine` + informational prefix list | `d9a2f84` |
| 2 | Migrate 5 call sites, delete orphaned `firstLine` | `9ae13c8` |
| 3 | CI trust gates only — no source changes, no commit | — |

## Verification

TDD RED was genuinely observed before GREEN: the test file failed to build with
`cmd/otto-tray/uihelpers_test.go:60:14: undefined: firstMeaningfulLine`.

| Gate | Result |
|------|--------|
| `go test ./cmd/otto-tray/... -run TestFirstMeaningfulLine -v` | PASS — all 7 subtests |
| `go build ./... && go vet ./cmd/otto-tray/... && go test ./cmd/otto-tray/...` | PASS |
| `gofumpt -l cmd/otto-tray/` (`@latest`, as CI installs it) | no output |
| `golangci-lint v2.12.2 run ./cmd/otto-tray/...` | 0 new issues (see Deferred) — `unused` clean, confirming no orphaned helper |
| `GOOS=windows go build ./...` | exit 0 |
| `GOOS=windows go vet ./cmd/otto-tray/...` | exit 0 |
| `GOOS=windows go test -c -o /dev/null ./cmd/otto-tray/` | exit 0 — confirms the new test file's `//go:build darwin \|\| windows` tag |

Plan gates, all met: `func firstMeaningfulLine` defined once; `loaded overrides:`
present in `uihelpers.go`; 3 `firstMeaningfulLine(res.Stderr)` call sites in
`tray.go`; 2 in `desktoptray.go`; `grep -rc 'func firstLine' cmd/otto-tray/`
reports 0 across every file; `git diff --name-only` lists nothing under
`scripts/`.

Read-through of the reported scenario: stderr of
`loaded env file: C:\Users\you\.gw\.env` / `loaded overrides:  C:\Users\you\.gw\overrides.env` /
`kiro: command not found` now yields the body
`Failed to start: kiro: command not found`. This is covered by the
`informational preamble is skipped in favour of the real error` subtest, and by
its CRLF twin.

Per CLAUDE.md and the builds-via-CI-only rule: no `make build`, no `make
package`, no tag, no push.

## Deviations from Plan

**1. [Stylistic] Extracted `hasInformationalPrefix` as a second unexported helper**

- **Found during:** Task 1
- **Issue:** The plan described the prefix match inline inside
  `firstMeaningfulLine`'s loop. Inlining a nested loop that must `continue` the
  *outer* loop requires either a labelled continue or a boolean flag, both of
  which read worse than a named predicate.
- **Change:** Added `func hasInformationalPrefix(line string) bool` alongside the
  main helper. Behaviour is identical to the planned algorithm.
- **Files modified:** `cmd/otto-tray/uihelpers.go`
- **Commit:** `d9a2f84`

No functional deviations. No architectural decisions (Rule 4) were required, and
no authentication gates were hit.

## Deferred Issues

`golangci-lint` surfaced two findings in files this branch never touched:
`unparam` on `cmd/otto-tray/openfolder.go:120` and `wrapcheck` on
`cmd/otto-tray/desktop_darwin.go:28`. Both are pre-existing — confirmed by
`golangci-lint run --new-from-rev=695462c16f0f19678618e77717bb0d97f6ae4f72 ./cmd/otto-tray/...`
reporting `0 issues`. They are invisible to CI because the lint job runs on
`ubuntu-latest`, where `desktop_darwin.go` is excluded by build tag. Logged to
`deferred-items.md` in this directory rather than fixed, per the scope boundary.

## Known Stubs

None. No placeholder values, TODO markers, or unwired data paths were introduced.

## Threat Flags

None. The change alters *which* stderr line is displayed, not how the body is
escaped or where it is routed. The macOS AppleScript escaping path is untouched,
and no new security-relevant surface (network, auth, file access, schema) was
introduced. The plan's threat register dispositions (T-g5z-01, T-g5z-02,
T-g5z-SC — all `accept`) hold as written: the wrappers print paths only, never
`AUTH_TOKEN` or override values, and no new module dependency was added.

## Self-Check: PASSED

- `cmd/otto-tray/uihelpers_test.go` — FOUND
- `cmd/otto-tray/uihelpers.go` — FOUND
- `cmd/otto-tray/tray.go` — FOUND
- `cmd/otto-tray/desktoptray.go` — FOUND
- Commit `9e59166` — FOUND
- Commit `d9a2f84` — FOUND
- Commit `9ae13c8` — FOUND
