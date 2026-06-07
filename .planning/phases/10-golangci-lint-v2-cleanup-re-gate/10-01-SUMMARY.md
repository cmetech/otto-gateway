---
phase: 10-golangci-lint-v2-cleanup-re-gate
plan: 01
subsystem: tooling
tags: [golangci-lint, staticcheck, unused, revive, gosec, noctx, lint-cleanup]

# Dependency graph
requires:
  - phase: 09 (and earlier)
    provides: pre-existing baseline of 49 lint violations captured in 10-BASELINE.txt
provides:
  - 16 of 49 baseline lint violations drained (Wave 1 mechanical tier)
  - LINT-03 per-category decision records for: staticcheck (QF1001), unused, revive (redefines-builtin-id subset), gosec (G301 subset), noctx
  - Phase-10 deferred-items.md initialised with discoveries that exceed Wave 1 scope (newly surfaced QF1001 and G703 hits)
affects: [Phase 10 Wave 2, Phase 10 Wave 3, Phase 10 Wave 4 (re-gate)]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Mechanical lint drain: scope_guard discipline — only fix sites named in the baseline; out-of-scope discoveries go to deferred-items.md instead of in-line refactoring"
    - "Per-category decision record (LINT-03): the PLAN.md authoritative table is lifted verbatim into SUMMARY.md so the cleanup evidence travels with phase artifacts"

key-files:
  created:
    - .planning/phases/10-golangci-lint-v2-cleanup-re-gate/10-01-SUMMARY.md
    - .planning/phases/10-golangci-lint-v2-cleanup-re-gate/deferred-items.md
  modified:
    - internal/adapter/anthropic/handlers_test.go
    - internal/engine/collect_test.go
    - internal/plugin/logging_test.go
    - internal/plugin/chain_test.go
    - internal/plugin/request_id.go
    - internal/acp/client_test.go
    - internal/admin/tail.go
    - internal/admin/tail_test.go
    - cmd/otto-gateway/main.go
    - internal/config/config.go
    - internal/admin/sse_test.go
    - tools/kiro-shim/main.go

key-decisions:
  - "Apply De Morgan rewrites to satisfy QF1001 (no //nolint exemptions); preserve operand and side-effect order"
  - "Delete fakePostHook (plugin pkg) + newRequestIDFromReader as truly dead — verified no in-tree refs outside declaration sites"
  - "Rename min/cap function-scope shadows (min→minVal, cap→capacity); leave struct field r.cap untouched because revive did not flag it"
  - "Tighten dir perms 0o755→0o750 in both flagged MkdirAll sites (operator-side dirs, no world access needed)"
  - "Thread context.Background() through 4 baseline noctx sites + 1 post-baseline site (line 653) — same mechanical fix as the per-category policy"
  - "kiro-shim uses context.Background() rather than refactoring the wrapper invocation chain to thread a real ctx (CLAUDE.md: minimum-scope discipline)"

patterns-established:
  - "Out-of-scope discovery handling: log to deferred-items.md, do not fix in-line, surface to next wave"
  - "Atomic per-task commits with subject `<type>(10-01-T<N>): <description>` — one commit per lint category"

requirements-completed:
  - LINT-01
  - LINT-03

# Metrics
duration: 8min
completed: 2026-06-07
---

# Phase 10 Plan 01: golangci-lint v2 Wave 1 mechanical drain Summary

**Drained 16 of 49 baseline lint violations (33%) across 5 mechanical categories in 5 atomic commits with zero behavior changes and all race tests green.**

## Performance

- **Duration:** 8 min
- **Started:** 2026-06-07T01:18:24Z
- **Completed:** 2026-06-07T01:27:04Z
- **Tasks:** 5/5 completed
- **Files modified:** 12

## Accomplishments

- All 16 Wave-1 baseline violations removed: 3 staticcheck QF1001, 4 unused, 3 revive redefines-builtin-id, 2 gosec G301, 4 noctx (+ 1 post-baseline noctx site on the same mechanical fix).
- Per-category decision record for the 5 categories is captured both in PLAN.md and in this SUMMARY (LINT-03 evidence).
- Out-of-scope lint discoveries (2 new QF1001 in `internal/plugin/pii/*`, 2 G703 unmaskings on the G301-tightened MkdirAll sites) routed to `deferred-items.md` rather than mixed into Wave 1.
- `go build ./...` clean; `go test -race ./...` green across the entire tree (all 19 test packages pass).
- `go vet ./...` clean.

## Task Commits

Each task was committed atomically:

1. **Task 1: Apply staticcheck QF1001 De Morgan rewrites (3 sites)** — `9ddc55b` (style)
2. **Task 2: Delete unused identifiers (fakePostHook + newRequestIDFromReader)** — `e8b2861` (chore)
3. **Task 3: Rename built-in shadows min/cap in 3 sites** — `0186248` (refactor)
4. **Task 4: Tighten 2x 0o755 dir perms to 0o750 (gosec G301)** — `18574ea` (fix)
5. **Task 5: Thread context through noctx call sites (4 sites)** — `678911e` (fix)

## Files Created/Modified

### Created
- `.planning/phases/10-golangci-lint-v2-cleanup-re-gate/10-01-SUMMARY.md` — this file
- `.planning/phases/10-golangci-lint-v2-cleanup-re-gate/deferred-items.md` — out-of-scope discoveries log (Wave 2/3 routing)

### Modified
- `internal/adapter/anthropic/handlers_test.go` — Task 1 De Morgan rewrite (1 line)
- `internal/engine/collect_test.go` — Task 1 De Morgan rewrite (1 line)
- `internal/plugin/logging_test.go` — Task 1 De Morgan rewrite (1 line)
- `internal/plugin/chain_test.go` — Task 2 removed dead `fakePostHook` type + methods
- `internal/plugin/request_id.go` — Task 2 removed dead `newRequestIDFromReader` + unused `io` import
- `internal/acp/client_test.go` — Task 3 param `min`→`minVal`
- `internal/admin/tail.go` — Task 3 param `cap`→`capacity` (struct field untouched)
- `internal/admin/tail_test.go` — Task 3 local `cap`→`capacity`
- `cmd/otto-gateway/main.go` — Task 4 `0o755`→`0o750`
- `internal/config/config.go` — Task 4 `0o755`→`0o750`
- `internal/admin/sse_test.go` — Task 5 4× `httptest.NewRequest`→`NewRequestWithContext` (216, 510, 608, 653)
- `tools/kiro-shim/main.go` — Task 5 `exec.Command`→`exec.CommandContext` + `context` import

## Per-category decision record (LINT-03 evidence)

Lifted verbatim from PLAN.md so the cleanup record is searchable from SUMMARY artifacts alone.

- **staticcheck (QF1001 — De Morgan's law refactor, 3 sites) — Policy: fix.** De Morgan rewrites are local-only and improve readability with zero behavior risk. Baseline lines: `internal/adapter/anthropic/handlers_test.go:865`, `internal/engine/collect_test.go:189`, `internal/plugin/logging_test.go:310`. No `//nolint` exemptions added.
- **unused (4 sites) — Policy: delete.** Truly dead code: the `fakePostHook` type in `internal/plugin/chain_test.go` (plus its `After` and `Name` methods) and the `newRequestIDFromReader` helper in `internal/plugin/request_id.go`. Verified with `grep -rn` that no caller, build-tag-guarded test, or reflection path references either. The `fakePostHook` type in `internal/engine/engine_test.go` is a different package and is unaffected. No `//nolint` exemptions added.
- **revive redefines-builtin-id (3 sites, this plan only) — Policy: rename.** Shadowing built-ins (`min`, `cap`) makes the code harder to grep and risks accidental misuse in Go 1.23+. Function-scope renames: `min`→`minVal` in `internal/acp/client_test.go:944`; `cap`→`capacity` in `internal/admin/tail.go:77` and `internal/admin/tail_test.go:123`. The struct field `r.cap` in `RingBuffer` is not flagged by revive and was left untouched (no exported signature changes). Wave 3 covers the remaining revive subcategories.
- **gosec G301 (2 sites) — Policy: tighten to 0o750.** Two `os.MkdirAll` callsites: `cmd/otto-gateway/main.go:989` (log-file parent) and `internal/config/config.go:493` (chat-trace parent). Owner rwx + group rx is sufficient for these operator-side directories. No `//nolint:gosec` exemptions added. Side-effect: gosec G703 surfaces on the same sites once G301 is resolved (logged in `deferred-items.md`).
- **noctx (4 sites + 1 post-baseline twin) — Policy: thread `context.Background()`.** Three baseline test sites in `internal/admin/sse_test.go` (216, 510, 608) switched to `httptest.NewRequestWithContext(context.Background(), …)`; the production site `tools/kiro-shim/main.go:77` switched to `exec.CommandContext(context.Background(), …)`. A fifth site (`internal/admin/sse_test.go:653`) appeared between baseline capture and execution; it was mechanically identical and required for the acceptance criterion `grep "noctx" | wc -l == 0`, so it was fixed under the same policy. Production-side `context.Background()` is justified — the kiro-shim is a process-spawn helper whose wrapper invocation does not yet thread a ctx; plumbing one in is beyond this debt-reduction phase's scope (CLAUDE.md "Don't refactor beyond what the task requires"). The Background ctx is still strictly better than the noctx-flagged plain `exec.Command` because it enables future cancellation plumbing without touching every call site.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 — bug-equivalent] Fixed a post-baseline noctx site (internal/admin/sse_test.go:653)**
- **Found during:** Task 5 verification
- **Issue:** A 5th `httptest.NewRequest` call site (line 653, added to the repo after the baseline snapshot) caused the acceptance criterion `grep "noctx" | wc -l == 0` to fail with count = 1.
- **Fix:** Applied the same mechanical `context.Background()` thread that the per-category policy mandates for the other 4 sites. Single 1-line diff, well under the 30-line scope_guard threshold, and consistent with the LINT-03 decision record.
- **Files modified:** `internal/admin/sse_test.go`
- **Commit:** included in `678911e`

### Out-of-scope discoveries (routed to deferred-items.md, NOT fixed)

**2. [Scope boundary] 2 new QF1001 sites in `internal/plugin/pii/*` not present in baseline**
- `internal/plugin/pii/luhn.go:55:5` — QF1001 De Morgan
- `internal/plugin/pii/recognizers_test.go:866:26` — QF1001 De Morgan
- These are absent from 10-BASELINE.txt; likely a linter-rev delta since the baseline snapshot. Logged to `deferred-items.md`. Re-baseline at phase close, then re-decide whether Wave 2 or Wave 3 picks them up.

**3. [Scope boundary] 2 G703 path-traversal hits unmasked by the G301 fix in Task 4**
- `cmd/otto-gateway/main.go:989:24` — G703 path traversal via taint analysis
- `internal/config/config.go:493:27` — G703 path traversal via taint analysis
- Once G301 was tightened, gosec's secondary G703 rule surfaced on the same MkdirAll arg. Proper mitigation is a `filepath.Clean` + allowlist check (since the dir originates from env-supplied paths), which is Wave 2/3 territory. Logged to `deferred-items.md`.

## Verification Results

| Check | Expected | Actual | Status |
|-------|----------|--------|--------|
| `golangci-lint` QF1001 baseline sites | 0 at baseline lines 21-23 | 0 at all three baseline lines | PASS |
| `golangci-lint` unused (fakePostHook, newRequestIDFromReader) | 0 | 0 | PASS |
| `golangci-lint` redefines-builtin-id | 0 | 0 | PASS |
| `golangci-lint` G301 | 0 | 0 | PASS |
| `golangci-lint` noctx | 0 | 0 | PASS |
| `go vet ./...` | clean | clean | PASS |
| `go build ./...` | clean | clean | PASS |
| `go test -race ./...` | all packages green | 19 packages green | PASS |
| Total remaining baseline-style violations | 33 (49 - 16) | 37 (33 + 2 new QF1001 + 2 unmasked G703) | EXPECTED-DELTA (documented) |

## Threat Flags

None — the Wave 1 changes either reduce surface (Task 4 dir-perm tightening, T-10-01 mitigation) or are test-only / function-scoped renames with no inbound trust boundary affected.

## Self-Check: PASSED

- File `.planning/phases/10-golangci-lint-v2-cleanup-re-gate/10-01-SUMMARY.md`: FOUND (this file)
- File `.planning/phases/10-golangci-lint-v2-cleanup-re-gate/deferred-items.md`: FOUND
- Commit `9ddc55b` (Task 1): FOUND in git log
- Commit `e8b2861` (Task 2): FOUND in git log
- Commit `0186248` (Task 3): FOUND in git log
- Commit `18574ea` (Task 4): FOUND in git log
- Commit `678911e` (Task 5): FOUND in git log
