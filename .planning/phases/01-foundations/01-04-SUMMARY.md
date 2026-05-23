---
phase: 01-foundations
plan: "04"
subsystem: docs
tags: [docs, operating, wrapper-scripts, lifecycle, env-vars]
dependency_graph:
  requires:
    - 01-01 (scripts/loop24, scripts/loop24.ps1, Makefile start/stop/status targets)
  provides:
    - docs/operating.md (developer operations guide)
    - README.md Running section
  affects: []
tech_stack:
  added: []
  patterns:
    - docs match DEVELOPERS.md style (H2 sections, pipe tables, code blocks)
key_files:
  created:
    - docs/operating.md
  modified:
    - README.md
decisions:
  - "LOOP24_LOGERR (not LOOP24_LOG_ERR) documented as the Windows stderr override — verified by reading scripts/loop24.ps1 which reads $env:LOOP24_LOGERR"
  - "PING_INTERVAL default documented as 60000 (integer ms = 60s) with explicit parsing rule: integers treated as milliseconds, Go duration strings also accepted"
  - "docs/operating.md documents LOOP24_LOGERR as Windows-only; notes it is not applicable on macOS/Linux where stderr is merged into stdout"
metrics:
  duration_minutes: 5
  completed_date: "2026-05-23"
  tasks_completed: 1
  tasks_total: 1
  files_created: 1
  files_modified: 1
---

# Phase 01 Plan 04: Documentation Additions (D-23) Summary

**One-liner:** Developer operations guide (docs/operating.md) with PID/log locations, env-var overrides including LOOP24_LOGERR and PING_INTERVAL parsing semantics, status computation explanation; README Running section with macOS/Linux and Windows quick-start commands.

## What Was Built

### Task 1: Create docs/operating.md and add README Running section

**docs/operating.md** — New developer operations guide covering:

- **Quick Start** sections for macOS/Linux and Windows with build + start/status/stop commands and Makefile shortcut equivalents
- **Subcommands** table: start, stop, status, restart, logs [-f], run — with descriptions of stop's bounded wait (eliminates restart race) and Windows parallel log tailing
- **File Locations** table: binary path, PID file, stdout log, and stderr log (Windows-only separate file) with platform defaults
- **Environment Variable Overrides** table (script-level): `LOOP24_BIN`, `LOOP24_PID`, `LOOP24_LOG`, `LOOP24_LOGERR` (Windows only), `LOOP24_ADDR` — names verified by reading both scripts before writing
- **Gateway Environment Variables** table (pass-through to binary): `HTTP_ADDR`, `KIRO_CMD`, `KIRO_ARGS`, `KIRO_CWD`, `DEBUG`, `PING_INTERVAL` with defaults from `internal/config/config.go`
- **How status Works** section: three-step process (PID file check → kill -0 liveness check → GET /health JSON print); explains stale PID handling and exit codes
- **Logs** section: JSON format explanation, tail commands for both platforms, note on non-rotation

**README.md** — Added `## Running` section before `## Development`:
- macOS/Linux quick-start code block (make build + ./scripts/loop24 start/status/stop)
- Windows PowerShell code block
- Note about Makefile shortcuts
- Link to `docs/operating.md` for full reference

## Verification Results

| Check | Result |
|-------|--------|
| `test -f docs/operating.md` | PASS |
| `grep -c "LOOP24_BIN" docs/operating.md` >= 1 | PASS (1) |
| `grep -c "LOOP24_" docs/operating.md` >= 4 | PASS (10) |
| `grep -c "HTTP_ADDR" docs/operating.md` >= 1 | PASS (1) |
| `grep -c "KIRO_CMD" docs/operating.md` >= 1 | PASS (2) |
| `grep -c "PING_INTERVAL" docs/operating.md` >= 1 | PASS (1) |
| `grep -c "milliseconds" docs/operating.md` >= 1 | PASS (1) |
| `grep -c "## Running" README.md` >= 1 | PASS (1) |
| `grep -c "docs/operating.md" README.md` >= 1 | PASS (1) |
| `grep -c "How.*status.*Works" docs/operating.md` >= 1 | PASS (1) |
| `LOOP24_LOG_ERR` absent (wrong name not documented) | PASS (0 occurrences) |
| `LOOP24_LOGERR` present (correct name verified in script) | PASS (1) |
| Trailing whitespace check (both files) | PASS |
| End-of-file newline (both files) | PASS |
| pre-commit hooks | PASS — all hooks passed |

## Commits

| Task | Commit | Description |
|------|--------|-------------|
| 1 | 778ea12 | docs(01-04): add docs/operating.md and README Running section |

## Deviations from Plan

### Variable Name: LOOP24_LOGERR vs LOOP24_LOG_ERR

**Found during:** Task 1 (mandatory script read before writing docs)
**Issue:** The plan's interface notes suggested verifying whether `LOOP24_LOG_ERR` is an override. Reading `scripts/loop24.ps1` revealed the actual variable is `$env:LOOP24_LOGERR` (no underscore between LOG and ERR), not `$env:LOOP24_LOG_ERR`.
**Fix:** Documented `LOOP24_LOGERR` as the correct override variable name. `LOOP24_LOG_ERR` does not appear anywhere in the docs.
**Files modified:** docs/operating.md
**This is correct behavior per the plan's acceptance criteria:** "LOOP24_LOG_ERR only appears in docs if scripts/loop24.ps1 exposes it as an env override (executor verified by reading the script)". The script exposes `LOOP24_LOGERR` instead.

## Known Stubs

None. All documented defaults are accurate:
- `LOOP24_BIN`, `LOOP24_PID`, `LOOP24_LOG`, `LOOP24_LOGERR`, `LOOP24_ADDR` defaults match the script implementations
- `HTTP_ADDR`, `KIRO_CMD`, `KIRO_ARGS`, `KIRO_CWD`, `DEBUG`, `PING_INTERVAL` defaults match `internal/config/config.go` (verified by reading the file)

## Threat Flags

No new security surfaces introduced. All documented paths are dev-laptop local (`/tmp`, `%TEMP%`). No credentials documented.

T-04-02 (spoofing risk: misleading docs) mitigated — both wrapper scripts were read before writing any documentation, and env-var names were verified against the actual implementations.

## Self-Check: PASSED

Files confirmed to exist:
- docs/operating.md: FOUND
- README.md (modified): FOUND

Commits confirmed to exist:
- 778ea12: FOUND (verified via git rev-parse)
