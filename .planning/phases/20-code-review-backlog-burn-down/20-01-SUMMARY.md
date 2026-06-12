---
phase: 20-code-review-backlog-burn-down
plan: 1
subsystem: code-review-burn-down
tags: [refactor, code-quality, qual-backlog, v1.10.3]
requirements_addressed: [QUAL-01, QUAL-02, QUAL-03, QUAL-04, QUAL-05, QUAL-06]
dependency_graph:
  requires: []
  provides:
    - "Clean v1.10.3 code-review backlog (QUAL-01..06 all Closed)"
    - "Hardened AppleScript escape contract with table-driven unit tests"
    - "Single shared tooltipForState (no duplication across darwin/windows)"
    - "Documented forceCloseCh contract with a Run-direct shutdown regression guard"
  affects:
    - "cmd/otto-tray (escape contract; tray.go tailLines; tooltip dedup)"
    - "internal/server (forceCloseCh allocation lifecycle)"
    - "internal/pool (test-file cleanup only; no production behavior change)"
tech_stack:
  added: []
  patterns:
    - "Nil-channel select-never idiom for asymmetric Run vs RunUntilSignal call paths"
    - "Collect-then-reverse over append-prepend for O(n) tail-window construction"
    - "Build-tag combinator `//go:build darwin || windows` for shared cross-platform helpers"
key_files:
  created:
    - cmd/otto-tray/escapeApplescript_darwin_test.go
    - cmd/otto-tray/tooltip.go
    - internal/server/run_direct_test.go
  modified:
    - cmd/otto-tray/uihelpers_darwin.go
    - cmd/otto-tray/uihelpers_windows.go
    - cmd/otto-tray/tray.go
    - internal/server/server.go
    - internal/pool/regression_rel_pool_02_test.go
    - internal/pool/respawn_ctx_cancel_test.go
decisions:
  - "D-20-01..09 honored verbatim; no decision conflicts surfaced during execution"
  - "QUAL-06: Option A (describe with parenthetical historical note) per Phase 17 dead-code stale-comment policy"
  - "QUAL-01 test file named escapeApplescript_darwin_test.go (no existing *_darwin_test.go collided)"
  - "QUAL-03: regression test placed in a new internal-package file (internal/server/run_direct_test.go) so the unexported forceCloseCh field sentinel check is reachable; mirrors the package layout decision the existing server_test.go (external package) could not satisfy"
metrics:
  duration: "~25 min"
  completed: "2026-06-12"
---

# Phase 20 Plan 1: Code-Review Backlog Burn-Down Summary

**One-liner:** Closed v1.10.3 Info-level code-review findings QUAL-01..06 as six atomic refactor commits; AppleScript escape contract hardened (one narrow behavior expansion), forceCloseCh allocation relocated to RunUntilSignal with a Run-direct regression guard, tooltipForState deduplicated, tailLines O(n²) prepend replaced with collect-then-reverse, two stale test artifacts cleaned up.

## Tasks Completed

| # | QUAL | Commit | One-line | Files |
|---|------|--------|----------|-------|
| 1 | QUAL-01 | `aa5ebd8` | Expand escapeApplescript escape set + table-driven tests | `cmd/otto-tray/uihelpers_darwin.go`, `cmd/otto-tray/escapeApplescript_darwin_test.go` |
| 2 | QUAL-02 | `bf617ed` | Dedup tooltipForState into shared build-tag file | `cmd/otto-tray/tooltip.go` (new), `cmd/otto-tray/uihelpers_{darwin,windows}.go` |
| 3 | QUAL-03 | `3dabe7c` | Relocate forceCloseCh allocation to RunUntilSignal | `internal/server/server.go`, `internal/server/run_direct_test.go` (new) |
| 4 | QUAL-04 | `57c1314` | tailLines collect-then-reverse (drop O(n²) prepend) | `cmd/otto-tray/tray.go` |
| 5 | QUAL-05 | `2216074` | Drop dead sessions/sessionsMu vars from REL-POOL-02 test | `internal/pool/regression_rel_pool_02_test.go` |
| 6 | QUAL-06 | `834016f` | Refresh stale removeSlot comment in respawn_ctx_cancel test | `internal/pool/respawn_ctx_cancel_test.go` |

## Verification

- `make ci` exits **0** at phase end (race + vet + staticcheck + gosec + arch-lint + govulncheck all green).
- `make ci` was rerun after each of the six commits to catch regressions early; every commit exits 0 individually.
- `go test ./internal/server/... -race -count=3 -run TestServer_Run_DirectShutdown` passes — the QUAL-03 nil-channel guard rail.
- `GOOS=windows go build ./cmd/otto-tray/...` passes — Windows cross-compile remains clean after the tray edits.
- `go test ./cmd/otto-tray/... -race -count=1` passes (QUAL-01 table-driven test green; QUAL-04 algorithm swap byte-equivalent).

## Behavior Change Inventory

Exactly one intentional behavior change, scoped to QUAL-01 (defense-in-depth):

- `escapeApplescript` (`cmd/otto-tray/uihelpers_darwin.go`) now:
  - Translates raw `\n`/`\r`/`\t` into the two-byte AppleScript escape sequences `\n`/`\r`/`\t` (backslash + letter) instead of forwarding the raw byte that would prematurely terminate the AS string literal.
  - Strips other C0 control bytes (0x00..0x1F excluding `\t`/`\n`/`\r`) and DEL (0x7F) entirely.
  - Continues to escape `"` and `\` as before.

All other five commits are pure refactors with byte-equivalent I/O (QUAL-04) or no observable behavior change at all (QUAL-02 / QUAL-03 / QUAL-05 / QUAL-06). `Server.Run`'s signature is unchanged. The `case <-s.forceCloseCh:` select arm in `Run` is unchanged — the change is the lifecycle of the channel, not the select shape.

## Deviations from Plan

None substantive. Items worth recording for traceability:

- **gofumpt reformatting on QUAL-03**: after editing the struct-literal alignment in `NewWithCommit` / `NewFromConfig`, `gofumpt` re-collapsed the alignment columns (since fields no longer needed equal padding). Applied via `gofumpt -l -w` before committing — captured in the same commit. Not a behavior deviation; project policy requires `gofumpt`-clean source.
- **`.gitignore` `-f` flag**: `cmd/otto-tray/` paths trip the `otto-tray` binary-name pattern in `.gitignore`. Existing tracked files in that directory were originally added with `git add -f` (verified via `git log`); new files in this plan followed the same convention. Not a deviation from D-20-09 — D-20-09 is silent on `.gitignore` mechanics.
- **QUAL-06 option chosen**: Option A (describe with parenthetical historical note). The Phase 17-03 dead-code stale-comment policy ("kept 'removed in Phase 17' annotations rather than full comment removal") explicitly favors retaining a historical pointer when the regression-guard intent of the assertion depends on the prior-bug context, which is the case here.

## Auth Gates

None encountered.

## Baseline Note

This plan executed on top of commit `af850a2` (`fix(18): drop unused level param from startAndDrain (unblock make ci)`), which retroactively cleared a pre-existing `unparam` lint violation in `internal/acp/regression_rel_obsv_03_test.go`. Without that fix, the baseline `make ci` would have been red and Plan 20-01 could not have executed. The unblock is upstream of Phase 20 (Phase 18 follow-up) and is not double-counted here.

## Known Stubs

None.

## Threat Flags

None. The threat surface introduced by this plan (T-20-01 / T-20-02 in PLAN.md `<threat_model>`) was already documented and mitigated per D-20-01 (QUAL-01 escape set) and D-20-04 (QUAL-03 nil-channel guard rail). QUAL-02/04/05/06 carry no security surface.

## Self-Check

- File `cmd/otto-tray/escapeApplescript_darwin_test.go` — FOUND.
- File `cmd/otto-tray/tooltip.go` — FOUND.
- File `internal/server/run_direct_test.go` — FOUND.
- Commit `aa5ebd8` — FOUND in `git log`.
- Commit `bf617ed` — FOUND in `git log`.
- Commit `3dabe7c` — FOUND in `git log`.
- Commit `57c1314` — FOUND in `git log`.
- Commit `2216074` — FOUND in `git log`.
- Commit `834016f` — FOUND in `git log`.

## Self-Check: PASSED
