---
phase: 17-trust-gate-restoration
plan: "03"
subsystem: trust-gates
tags: [gofmt, gofumpt, gosec, golangci-lint, dead-code, mechanical-fixes]

# Dependency graph
requires:
  - phase: 16-fix-mediums
    provides: Phase 16-04 introduced the tray.go support-bundle staging path that surfaced G301/G306 at v1.9 milestone close
  - phase: 5
    provides: Original D-03 dead-slot drop logic whose Pool.removeSlot half is now unreachable (re-queue path supersedes it)
provides:
  - gofmt-clean internal/server/server.go (NewWithCommit struct-literal alignment)
  - gofumpt-clean internal/pool/regression_rel_pool_0{1,2}_test.go (leading blank lines stripped)
  - gosec G301/G306-clean cmd/otto-tray/tray.go (0o755->0o750 mkdir, 0o644->0o600 writefile)
  - Pool.removeSlot dead code removed from internal/pool/pool.go; three stale doc-comments updated
affects: [17-01, 17-02, v1.9.1-release]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "D-17-02 batched mechanical-fix commit pattern — 5 trust-gate items in one atomic commit; revert-as-unit guarantee"
    - "D-17-05 single-user laptop posture rationale documented inline in the gosec-tightening commit body"

key-files:
  created: []
  modified:
    - internal/server/server.go
    - internal/pool/regression_rel_pool_01_test.go
    - internal/pool/regression_rel_pool_02_test.go
    - cmd/otto-tray/tray.go
    - internal/pool/pool.go

key-decisions:
  - "D-17-02 atomic-batch executed: 5 mechanical fixes ship in one commit (b78fd09); revert-as-unit acceptable"
  - "D-17-05 single-user laptop posture rationale recorded in commit body for the 0o600 WriteFile tightening on tray.go last-error.log"
  - "Dead-code removal kept stale doc-comments intact otherwise: removed `removeSlot` mention only; preserved surrounding logic-explanation prose (operator preference per plan Step 4 — minimize the diff)"

patterns-established:
  - "Mechanical batched fix commit: title `fix({phase}-{plan}): trust-gate mechanical batch — <items>`; body lists per-file change with closing-gate annotation; D-rationale references inline"

requirements-completed:
  - REL-FMT-GOFMT
  - REL-FMT-GOFUMPT
  - REL-LINT-G301
  - REL-LINT-G306
  - REL-LINT-UNUSED

# Metrics
duration: 12min
completed: 2026-06-11
---

# Phase 17 Plan 03: Trust-Gate Mechanical Batch Summary

**Five trust-gate items closed in one atomic commit — gofmt struct alignment, gofumpt blank lines (x2), gosec G301+G306 perm tightening, and Pool.removeSlot dead-code removal — restoring `make ci` fmt/vet/build/lint/test-race stages to clean.**

## Performance

- **Duration:** 12 min
- **Started:** 2026-06-11T22:27:00Z (approx; plan dispatch)
- **Completed:** 2026-06-11T22:39:32Z
- **Tasks:** 5 (batched per D-17-02 into 1 atomic commit)
- **Files modified:** 5
- **Commits:** 1 (`b78fd09`)

## Accomplishments

- `internal/server/server.go` NewWithCommit constructor body is now gofmt-clean — struct field column alignment fixed for cfg/logger/version/commit/start to match addr/shutdownCh/forceCloseCh. Closes the fmt-check (gofmt) `make ci` gate.
- `internal/pool/regression_rel_pool_01_test.go` and `regression_rel_pool_02_test.go` are now gofumpt-clean — leading blank lines after each test function's opening brace stripped. Closes the fmt-check (gofumpt) `make ci` gate.
- `cmd/otto-tray/tray.go` support-bundle error-logging path now uses tightened permissions: `MkdirAll(logDir, 0o750)` (was 0o755 — G301) and `WriteFile(logPath, content, 0o600)` (was 0o644 — G306). Closes the lint (gosec G301+G306) gates. Single-user laptop posture rationale documented in commit body.
- `internal/pool/pool.go` (`*Pool).removeSlot` dead function removed (Phase 5 D-03 leftover; zero non-comment callers in production). Three stale doc-comments at the former lines 274, 695, 747 updated to note the function was removed in Phase 17. Closes the lint (unused) gate.
- `golangci-lint run ./...` reports `0 issues.` repo-wide. `gofmt -l .` and `gofumpt -l .` both report 0 files (non-vendor). `go test -race -count=1 ./...` clean (REL-POOL-02 happened to pass this run; underlying flake is still 17-02's scope).

## Task Commits

All 5 tasks batched into 1 atomic commit per D-17-02 ("five mechanical fixes in one commit; reverting the batch as a unit is acceptable"):

1. **Tasks 1-5 (batched): trust-gate mechanical batch** — `b78fd09` (fix)

No separate plan-metadata commit yet — SUMMARY/STATE/ROADMAP commit follows as the standard close-out.

## Files Created/Modified

- `internal/server/server.go` — Reformatted struct-literal column alignment in the NewWithCommit constructor body (lines 200-209). 10 line diff, whitespace-only — `git diff --stat` confirmed bounded scope (no full-file rewrite).
- `internal/pool/regression_rel_pool_01_test.go` — Removed one leading blank line at the start of `TestRegression_REL_POOL_01_PoolShrinksToZero` (was at line 57). 1 line deletion.
- `internal/pool/regression_rel_pool_02_test.go` — Removed one leading blank line at the start of `TestRegression_REL_POOL_02_CtrlCOrphansChildren` (was at line 62). 1 line deletion.
- `cmd/otto-tray/tray.go` — Tightened MkdirAll perm at line 367 (0o755→0o750) and WriteFile perm at line 372 (0o644→0o600) in the support-bundle error-logging branch. 2 line diff.
- `internal/pool/pool.go` — Deleted `Pool.removeSlot` function (former lines 338-351) and updated 3 stale doc-comment references at the former lines 274, 695, 747 to note the function was removed in Phase 17. 26 line diff (mostly deletion).

## Decisions Made

- **D-17-02 executed as single commit (not 2)** — plan offered a fallback "1 commit OR 2 commits if operator prefers" but per the D-17-02 default ("five mechanical fixes in one commit") and the small bounded diff (14 ins / 28 del across 5 files), a single atomic commit is correct. Revert-as-unit guarantee is intact.
- **D-17-05 single-user posture rationale recorded inline in commit body** — the 0o600 WriteFile tightening is acceptable because (a) the project's documented single-user laptop posture means no cross-user reader exists, (b) the support-bundle log file may contain kiro-cli stderr (model output, tool-call args, env-var echoes) that should not be world-readable. Group-readable retained on the staging dir for Linux dev-box tooling; world-readable removed.
- **Dead-code stale-comment policy: minimize the diff** — the plan offered two policies for updating the 3 stale comments at the former lines 274, 695, 747: (a) remove the `removeSlot` mention entirely, or (b) note "removed in Phase 17". Chose (b) for all three — preserves the surrounding logic-explanation prose (which is load-bearing for understanding the WR-07 / CR-01 / REL-POOL-01 D-08 design rationale) and minimizes the diff. Future readers grepping for `removeSlot` will find the comments explaining the historical context, which is the right thing for archaeology.

## Deviations from Plan

None — plan executed exactly as written. Tasks 1-5 collapsed into one commit per D-17-02's explicit batching guidance (not a deviation; the plan's own commit-granularity section endorsed this default).

## Issues Encountered

- **`gofumpt` and `golangci-lint` are not in `PATH` on the dev box** — both binaries exist in `$(go env GOPATH)/bin` and are wired up by the Makefile via `command -v`. Resolved by prefixing each verification command with `export PATH="$(go env GOPATH)/bin:$PATH"`. Not a code issue; tooling discoverability only. Confirmed gofumpt v0.10.0 and golangci-lint 2.12.2 match what the Makefile expects.
- **First `git add ... && git commit` chain failed at `git add` stage with "cmd/otto-tray is ignored"** — the project's `.gitignore` ignores the binary name `otto-tray`, which matched the directory name `cmd/otto-tray/` when staging the tree-level path. Resolved by re-issuing as separate `git add <files>` (succeeded silently because individual `.go` files are not ignored) + standalone `git commit`. No data loss; the 5 files were already staged from a prior partial attempt. Cosmetic / .gitignore-design issue; not a code defect. Flagged for future tracking but out of scope for this plan.
- **REL-POOL-02 passed once under `-race`** during the `make ci` sweep but per 17-CONTEXT.md D-17-04 the flake is ~1/10 — this single green run is NOT sufficient evidence the flake is fixed. The flake remains 17-02's responsibility.
- **make ci still fails at arch-lint** with 3 adapter→pool notices (`adapter/anthropic/handlers.go:13`, `adapter/ollama/handlers.go:17`, `adapter/openai/handlers.go:15`) — this is owned by Plan 17-01 (D-17-01 canonical.ErrPoolExhausted relocation). NOT this plan's responsibility; expected per 17-CONTEXT.md plan-coordination notes.

## `make ci` Post-Batch Evidence

```
=== fmt-check (gofumpt) ===
PASS — gofumpt -l . non-vendor count: 0

=== vet ===
PASS — go vet ./... clean

=== build ===
PASS — go build ./... clean (binary built: bin/otto-gateway with v1.9-3-g0c252cb-dirty version stamp)

=== lint (golangci-lint) ===
PASS — `0 issues.` repo-wide
  - G301 in cmd/otto-tray: 0 findings
  - G306 in cmd/otto-tray: 0 findings
  - unused-removeSlot in internal/pool: 0 findings

=== test-race ===
PASS — all packages green end-to-end (REL-POOL-02 passed this run; flake recurrence still possible)

=== arch-lint ===
FAIL — 3 notices (adapter→pool):
  - adapter_anthropic shouldn't depend on otto-gateway/internal/pool in adapter/anthropic/handlers.go:13
  - adapter_ollama shouldn't depend on otto-gateway/internal/pool in adapter/ollama/handlers.go:17
  - adapter_openai shouldn't depend on otto-gateway/internal/pool in adapter/openai/handlers.go:15
  (Owned by Plan 17-01 / D-17-01.)
```

Log captured at `/tmp/17-03-makeci.log` (transient; not committed).

## Verification Checklist (vs success_criteria)

| # | Criterion | Status |
|---|-----------|--------|
| 1 | `gofmt -l .` produces no non-vendor output | PASS (0) |
| 2 | `gofumpt -l .` produces no non-vendor output | PASS (0) |
| 3 | `golangci-lint run --enable=gosec ./cmd/otto-tray/...` reports zero G301 / G306 | PASS (0 / 0) |
| 4 | `golangci-lint run --enable=unused ./internal/pool/...` reports zero `removeSlot` findings | PASS (0) |
| 5 | tray.go line 367 reads `0o750`, line 372 reads `0o600` | PASS (verified via grep) |
| 6 | `internal/pool/pool.go` no longer contains a `removeSlot` function definition; stale comments updated | PASS (`grep -c "func (p \*Pool) removeSlot"` = 0) |
| 7 | `go build ./...` + `go vet ./...` + `go test -race -count=1 ./...` exit 0 | PASS |
| 8 | One atomic commit with the trust-gate-batch title | PASS (`b78fd09`) |
| 9 | Combined with 17-01 + 17-02 landing, `make ci` exits 0 end-to-end | Pending 17-01 + 17-02 (this plan's contribution is fmt + lint clean; arch-lint and REL-POOL-02 are out of scope) |

## D-17-05 Single-User Posture Documentation

Verified the commit body for `b78fd09` includes the D-17-05 rationale for the 0o600 WriteFile tightening:

> Per D-17-05 risk-mitigation row 4, the 0o600 tightening is acceptable under the project's single-user laptop posture documented in docs/operating.md "v1 no-auth posture": no cross-user reader exists for this file, and the support-bundle log may contain kiro-cli stderr (model output, tool-call args, env-var echoes) that should not be world-readable. Group-readable retained on the staging dir for Linux dev-box tooling; world-readable removed.

This satisfies the success-criteria requirement that the commit message document the rationale for future operators.

## Threat Surface Scan

No new security-relevant surface introduced. The G301/G306 tightening REDUCES surface (less-permissive perms on a per-user log file). The dead-code removal reduces the Pool API surface (function was unexported but discoverable via grep; removing it removes a mistake risk where a future contributor might re-introduce the pool-shrinks-to-zero bug by calling the dead-code drop path). No `threat_flag:` annotations needed.

## User Setup Required

None — pure in-tree Go edits, no environment / external service changes.

## Self-Check: PASSED

- `internal/server/server.go` modified: FOUND (gofmt-clean confirmed)
- `internal/pool/regression_rel_pool_01_test.go` modified: FOUND (gofumpt-clean confirmed)
- `internal/pool/regression_rel_pool_02_test.go` modified: FOUND (gofumpt-clean confirmed)
- `cmd/otto-tray/tray.go` modified: FOUND (0o750 + 0o600 present at expected lines)
- `internal/pool/pool.go` modified: FOUND (`removeSlot` function definition gone; 3 stale comments updated)
- Commit `b78fd09` exists: FOUND (`git log --oneline -3` confirms)
- SUMMARY.md exists at expected path: FOUND (this file)

## Next Plan Readiness

- **Plan 17-01 (arch-lint relocation)** can proceed independently — no file overlap with 17-03's edits. Touches `internal/canonical/`, `internal/pool/pool.go` (re-export addition), `internal/adapter/{anthropic,ollama,openai}/`. The `internal/pool/pool.go` overlap is benign (17-03's edit was in the dead-code-removal area at the former line 338-351 and the 3 comment updates; 17-01 will add a `var ErrPoolExhausted = canonical.ErrPoolExhausted` re-export, likely near the existing error declarations — different stretch of the file).
- **Plan 17-02 (REL-POOL-02 deflake)** can proceed independently — touches `internal/pool/regression_rel_pool_02_test.go` (the resultWg / Result-drain plumbing). 17-03 only stripped a blank line; 17-02's edits will land cleanly on top. Per the plan's caveat: gofumpt is idempotent, so 17-02 just needs to re-run gofumpt on the file after its edits if it adds new function bodies.
- **Phase 17 close** is blocked on 17-01 + 17-02 completing successfully and `make ci` exit 0 end-to-end (phase-close criterion #1 per 17-CONTEXT.md). 17-03's contribution to that close-out is fmt-clean + lint-clean + dead-code-removed.

---
*Phase: 17-trust-gate-restoration*
*Completed: 2026-06-11*
