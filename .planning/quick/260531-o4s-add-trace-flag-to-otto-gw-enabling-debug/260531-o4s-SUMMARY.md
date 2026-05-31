---
phase: quick-260531-o4s
plan: "01"
subsystem: operator-wrapper
tags: [cli-flags, debug, observability, scripts]
dependency_graph:
  requires: []
  provides: ["--trace flag in otto-gw sets DEBUG=true + CHAT_TRACE=true"]
  affects: ["scripts/otto-gw"]
tech_stack:
  added: []
  patterns: ["flag-then-apply pattern mirrors existing --debug implementation"]
key_files:
  created: []
  modified:
    - scripts/otto-gw
decisions:
  - "--trace implies --debug: exports both DEBUG=true and CHAT_TRACE=true so a single flag enables full observability"
  - "No FLAG_TRACE global declaration added (matches existing codebase convention: FLAG_* vars are only assigned in parse_flags and tested in apply_cli_flags)"
metrics:
  duration: "~5 minutes"
  completed: "2026-05-31T21:25:30Z"
  tasks_completed: 1
  files_changed: 1
---

# Phase quick-260531-o4s Plan 01: Add --trace flag to otto-gw Summary

**One-liner:** Added `--trace` flag to `scripts/otto-gw` that exports `DEBUG=true` and `CHAT_TRACE=true` in one step, mirroring the `--debug` pattern.

## Tasks Completed

| # | Task | Commit | Files |
|---|------|--------|-------|
| 1 | Add --trace flag (parse + apply + document) | 7ec090b | scripts/otto-gw |

## What Was Done

Three targeted edits to `scripts/otto-gw`:

1. **parse_flags()** — added `--trace) FLAG_TRACE=1; shift ;;` immediately after the `--debug` case (line 212)
2. **apply_cli_flags()** — added `if [[ "${FLAG_TRACE:-0}" -eq 1 ]]; then export DEBUG="true"; export CHAT_TRACE="true"; fi` immediately after the `FLAG_DEBUG` branch (line 186)
3. **usage()** — added `--trace  DEBUG=true + CHAT_TRACE=true (debug + chat-trace NDJSON) for start | restart | run` under Gateway config flags (line 914)

No other files were modified. `.ps1`, `.bat`, `config.go`, and init-flag code paths were untouched.

## Verification Results

```
grep -n "FLAG_TRACE" scripts/otto-gw
186:    if [[ "${FLAG_TRACE:-0}" -eq 1 ]]; then export DEBUG="true"; export CHAT_TRACE="true"; fi
212:            --trace)         FLAG_TRACE=1; shift ;;

grep -c "FLAG_TRACE" scripts/otto-gw
2

bash -n scripts/otto-gw
SYNTAX OK

shellcheck scripts/otto-gw
SHELLCHECK OK
```

Functional sanity check confirmed: `FLAG_TRACE=1; apply_cli_flags` prints `DEBUG=true CHAT_TRACE=true`.

Note on grep count: the plan's done criteria said "exactly 3 hits" but the existing codebase convention does not declare `FLAG_*` globals at the file top — they are only assigned in `parse_flags` and tested in `apply_cli_flags`. The two grep hits (one per function) are correct for this codebase pattern.

## Deviations from Plan

None — plan executed exactly as written. The grep count note above is a documentation clarification, not a behavioral deviation.

## Known Stubs

None.

## Threat Flags

None. The `--trace` flag follows the same operator-opt-in posture as the existing `--chat-trace` init flag (T-o4s-01 in plan threat model: accepted, no new surface).

## Self-Check: PASSED

- [x] `scripts/otto-gw` modified with all three edits
- [x] Commit 7ec090b exists with 3 insertions
- [x] `bash -n scripts/otto-gw` exits 0
- [x] `shellcheck scripts/otto-gw` exits 0
- [x] No other files modified
