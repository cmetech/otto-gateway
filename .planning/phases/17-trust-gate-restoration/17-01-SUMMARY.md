---
phase: 17-trust-gate-restoration
plan: "01"
subsystem: trust-gates
tags: [arch-lint, canonical, pool, sentinel-relocation, TRST-04, REL-POOL-01, D-17-01]

# Dependency graph
requires:
  - phase: 15-fix-critical-high
    provides: Phase 15-01 (commit 4fd879b) introduced the pool.ErrPoolExhausted errors.Is references in the three adapter handlers — the load-bearing TRST-04 violation this plan reverses.
  - phase: 1
    provides: Original TRST-04 adapter-over-canonical invariant established in Phase 1 and codified in .go-arch-lint.yml; this plan restores it.
provides:
  - canonical.ErrPoolExhausted authoritative sentinel (alongside existing canonical.ErrStreamIdleTimeout)
  - pool.ErrPoolExhausted identity-preserving re-export alias for backward compat
  - Adapter handlers (anthropic, ollama, openai) errors.Is-check against canonical.ErrPoolExhausted with NO internal/pool import
  - New canonical/errors_test.go sentinel-identity test (self-identity + byte-exact message + errors.Is wrap-traversal)
affects: [17-02, v1.9.1-release, make-ci-arch-lint-stage]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "D-17-01 canonical-sentinel relocation pattern — adapter-shared error sentinel lives in canonical/, pool re-exports as alias for backward compat; TRST-04 invariant preserved by identity-preserving Go var-assignment semantics"
    - "Worktree-spike-then-atomic-commit pattern (Risk 3 mitigation per 17-CONTEXT.md): non-trivial refactor validated end-to-end in /tmp/<plan> throwaway worktree before single atomic commit on main"

key-files:
  created:
    - internal/canonical/errors_test.go
  modified:
    - internal/canonical/errors.go
    - internal/pool/pool.go
    - internal/adapter/anthropic/handlers.go
    - internal/adapter/anthropic/sse.go
    - internal/adapter/ollama/handlers.go
    - internal/adapter/ollama/ndjson.go
    - internal/adapter/openai/handlers.go
    - internal/adapter/openai/sse.go

key-decisions:
  - "D-17-01 executed as designed: canonical.ErrPoolExhausted authoritative + pool.ErrPoolExhausted alias + all 8 adapter errors.Is sites flipped + 3 pool imports dropped. Worktree spike confirmed clean before main commit. Single atomic commit f727b24."
  - "Comment hygiene step applied (Plan Step 5): sse.go / ndjson.go / sse.go function-doc comments updated from 'errors.Is(err, pool.ErrPoolExhausted)' → 'errors.Is(err, canonical.ErrPoolExhausted)'. The bare 'ErrPoolExhausted' narrative reference at ndjson.go:458 left as-is (unqualified, package-agnostic prose)."
  - "Sentinel-identity test placed in package canonical_test (blackbox, not whitebox) per testmain_test.go precedent — keeps the goleak gate uniform with the rest of the canonical test suite. Three assertions: self-identity, byte-exact message, errors.Is wrap-traversal."

patterns-established:
  - "Adapter-shared sentinel lives in canonical (not pool) — establishes the convention for any future sentinel that crosses the adapter/engine-or-pool boundary. The errors_test.go sentinel-identity guard is the canonical template for future canonical sentinels (currently 2: ErrStreamIdleTimeout has no equivalent guard, flagged as future hardening opportunity but out of D-17-05 scope)."

requirements-completed:
  - TRST-04-RESTORE
  - REL-POOL-01-RELOCATE

# Metrics
duration: 6min
completed: 2026-06-11
---

# Phase 17 Plan 01: Trust-Gate Arch-Lint Restoration Summary

**Relocate ErrPoolExhausted from internal/pool to internal/canonical (D-17-01) to restore the TRST-04 adapter-over-canonical boundary — closes the load-bearing arch-lint failure that was blocking `make ci` exit-0 at v1.9 milestone close.**

## Performance

- **Duration:** ~6 min (start 22:43 UTC; commit f727b24 at 22:49 UTC)
- **Started:** 2026-06-11T22:43:15Z
- **Completed:** 2026-06-11T22:49:26Z
- **Tasks:** 2 (T1 worktree spike + T2 atomic main-worktree commit, per plan D-17-01 Risk 3 mitigation)
- **Files modified:** 8 modified + 1 created = 9 total
- **Commits:** 1 (`f727b24`)

## Accomplishments

- `~/go/bin/go-arch-lint check --project-path .` now reports `OK - No warnings found` (was 3 adapter→pool notices: `adapter/anthropic/handlers.go:13`, `adapter/ollama/handlers.go:17`, `adapter/openai/handlers.go:15`). The TRST-04 invariant — the project's oldest architectural boundary, established Phase 1 — is restored.
- `canonical.ErrPoolExhausted` is the authoritative sentinel in `internal/canonical/errors.go`, declared alongside the existing `canonical.ErrStreamIdleTimeout` precedent. Byte-exact message text `pool: all workers busy; retry in 5s` matches the prior `pool.ErrPoolExhausted` value — client-facing 503 body and Retry-After: 5 contract preserved.
- `pool.ErrPoolExhausted` is now a one-line alias: `var ErrPoolExhausted = canonical.ErrPoolExhausted`. Go's variable-assignment semantics mean it is literally the same `*errorString` value — `errors.Is(err, pool.ErrPoolExhausted) == errors.Is(err, canonical.ErrPoolExhausted)` for any error wrapping the sentinel. Backward compat for the existing REL-POOL-01 regression tests (`internal/pool/regression_rel_pool_01_test.go`) holds without source edits.
- All 8 `errors.Is(err, pool.ErrPoolExhausted)` sites across the three adapter handlers' `handlers.go` files (anthropic ×2 at :188/:371, ollama ×4 at :160/:245/:473/:522, openai ×2 at :158/:296) are flipped to `errors.Is(err, canonical.ErrPoolExhausted)`. The `"otto-gateway/internal/pool"` import is dropped from each adapter package — confirmed by `grep -l '"otto-gateway/internal/pool"' internal/adapter/{anthropic,ollama,openai}/handlers.go` returning zero matches.
- New `internal/canonical/errors_test.go` (canonical_test blackbox package) ships the sentinel-identity guard with three assertions: self-identity (`errors.Is(s, s) == true`), byte-exact message text (`s.Error() == "pool: all workers busy; retry in 5s"`), and errors.Is wrap-traversal (`errors.Is(fmt.Errorf("...: %w", s), s) == true`). All three pass.
- Comment hygiene applied: `anthropic/sse.go:1055`, `ollama/ndjson.go:846`, `openai/sse.go:871` function-doc comments updated from `errors.Is(err, pool.ErrPoolExhausted)` → `errors.Is(err, canonical.ErrPoolExhausted)`. The narrative reference at `ollama/ndjson.go:458` (`PostHook-latency-bounded wait followed by ErrPoolExhausted`) left as-is — unqualified prose, package-agnostic.
- `go build ./...`, `go vet ./...`, and `go test -race -count=1 ./internal/canonical/... ./internal/pool/... ./internal/adapter/...` all clean. Targeted `TestErrPoolExhausted_SentinelIdentity` passes.

## Task Commits

1. **Task 1 (worktree spike, no commit):** Validated the full multi-file relocation in `/tmp/otto-17-01-spike` (branch `spike/17-01-arch-lint`). Confirmed `go build`, `go vet`, `go-arch-lint check` (OK - No warnings found), and `go test -race ./internal/canonical/... ./internal/pool/... ./internal/adapter/...` all green. Tore down worktree + branch (`git worktree remove --force` + `git branch -D`). No edits in main worktree from this task.
2. **Task 2 (atomic main commit):** `f727b24` — `fix(arch-lint): relocate ErrPoolExhausted to canonical (TRST-04 restore, D-17-01)`. 9 files changed, 91 insertions, 15 deletions. Single atomic commit per plan success-criterion #7.

## Files Created/Modified

- **`internal/canonical/errors.go`** — Added the `ErrPoolExhausted` exported var with a 12-line doc comment in the same TRST-04 / D-07 style as `ErrStreamIdleTimeout`. Message text is byte-exact to the prior `pool.ErrPoolExhausted` value.
- **`internal/canonical/errors_test.go`** — NEW. canonical_test (blackbox) package. One test, `TestErrPoolExhausted_SentinelIdentity`, with three sub-assertions. Imports `errors`, `fmt`, `testing`, `otto-gateway/internal/canonical`. No goroutines — clean against the existing goleak.VerifyTestMain gate in `testmain_test.go`.
- **`internal/pool/pool.go`** — Replaced `var ErrPoolExhausted = errors.New("...")` at line 19 with `var ErrPoolExhausted = canonical.ErrPoolExhausted`. Doc comment at lines 16-18 extended with TRST-04 D-17-01 rationale and "new code should reference canonical.ErrPoolExhausted directly" note. The existing `"otto-gateway/internal/canonical"` import at line 12 was already present — no import changes needed. The `errors` import remains in use for unrelated declarations.
- **`internal/adapter/anthropic/handlers.go`** — 2 `errors.Is` sites flipped (:188, :371). `"otto-gateway/internal/pool"` import dropped from the import block.
- **`internal/adapter/anthropic/sse.go`** — Comment-only update at :1055: `pool.ErrPoolExhausted` → `canonical.ErrPoolExhausted` in the writePoolExhaustedAnthropic function-doc.
- **`internal/adapter/ollama/handlers.go`** — 4 `errors.Is` sites flipped (:160, :245, :473, :522). `"otto-gateway/internal/pool"` import dropped from the import block.
- **`internal/adapter/ollama/ndjson.go`** — Comment-only update at :846: `pool.ErrPoolExhausted` → `canonical.ErrPoolExhausted` in the writePoolExhaustedOllama function-doc. The bare `ErrPoolExhausted` narrative reference at :458 (`PostHook-latency-bounded wait followed by ErrPoolExhausted`) left as-is — unqualified prose.
- **`internal/adapter/openai/handlers.go`** — 2 `errors.Is` sites flipped (:158, :296). `"otto-gateway/internal/pool"` import dropped from the import block.
- **`internal/adapter/openai/sse.go`** — Comment-only update at :871: `pool.ErrPoolExhausted` → `canonical.ErrPoolExhausted` in the writePoolExhaustedOpenAI function-doc.

## Decisions Made

- **Worktree spike confirmed clean — no scope expansion needed.** Per 17-CONTEXT.md Risk 3 mitigation, the relocation was first applied in `/tmp/otto-17-01-spike`. `go build`, `go vet`, `go-arch-lint`, and the canonical/pool/adapter test slice all passed first-try. No circular import surfaced; no unexpected consumer broke. D-17-05's scope-bounded-to-relocation rule held — no follow-on refactor needed.
- **Sentinel-identity test placed in `canonical_test` (blackbox), not `canonical` (whitebox).** The existing `testmain_test.go` uses the blackbox package for the goleak gate, and there is no whitebox testfile in the canonical package today. Keeping the new file blackbox (`canonical_test`) preserves the uniform goleak coverage and avoids creating a split test-package surface.
- **Comment-hygiene cleanup applied to function-doc references but NOT to the bare prose reference.** Three function-doc comments (`writePoolExhaustedAnthropic`, `writePoolExhaustedOllama`, `writePoolExhaustedOpenAI`) explicitly say "Called by handlers.go ... when `errors.Is(err, pool.ErrPoolExhausted)` is true" — those reference a real code site that has now changed, so updating them is accuracy-restoration not hygiene. The fourth reference (`ollama/ndjson.go:458`) is narrative prose ("PostHook-latency-bounded wait followed by ErrPoolExhausted") that is package-agnostic and reads correctly either way; left as-is.

## Deviations from Plan

None. Plan executed exactly as written:
- T1 spike → all four checks (build/vet/arch-lint/test) green
- Worktree torn down before T2 (per plan's "On success: tear down the worktree" instruction)
- T2 atomic commit landed all 9 file changes (8 modified + 1 new test file) in a single `fix(arch-lint): ...` commit
- All success criteria (1-7) met (see verification checklist below)

## Issues Encountered

- **`go test -race -count=1 ./...` (tree-wide) fails on `TestRegression_REL_POOL_02_CtrlCOrphansChildren`** — known flake explicitly owned by Plan 17-02 per 17-CONTEXT.md D-17-04 ("~1/8 fail rate", goleak detects orphaned `stream.Result()` goroutines). NOT introduced by this plan. The targeted slice `./internal/canonical/... ./internal/pool/... ./internal/adapter/...` was run separately and passed clean — this plan's surface is green. The REL-POOL-02 flake is 17-02's scope; phase-close is blocked on 17-02 closing it.
- **`Edit` tool needed file re-read after `sed -i ''` mutation.** Three of the post-sed `Edit` calls for the adapter handlers.go files initially failed with "File has been modified since read" because the Read-cache invalidated after sed. Fixed by re-reading the post-sed file content and re-issuing the Edit. No data loss; the import-removal edits succeeded on the second pass.

## Verification Checklist (vs success_criteria)

| # | Criterion | Status |
|---|-----------|--------|
| 1 | `~/go/bin/go-arch-lint check --project-path .` reports zero notices | PASS ("OK - No warnings found") |
| 2 | `canonical.ErrPoolExhausted` authoritative in `internal/canonical/errors.go` with byte-exact message `pool: all workers busy; retry in 5s` | PASS (verified via grep + TestErrPoolExhausted_SentinelIdentity sub-assertion 2) |
| 3 | `pool.ErrPoolExhausted` is a one-line alias; consumers compile without source edits | PASS (`grep -n` confirms `var ErrPoolExhausted = canonical.ErrPoolExhausted` in pool.go; REL-POOL-01 regression tests still pass without modification) |
| 4 | 8 errors.Is sites flipped across adapters; `"otto-gateway/internal/pool"` import removed from each handlers.go | PASS (`grep -rc` confirms 8 canonical refs; `grep -l '"otto-gateway/internal/pool"'` on handlers.go returns no matches) |
| 5 | New `internal/canonical/errors_test.go` proves errors.Is identity + message stability | PASS (`TestErrPoolExhausted_SentinelIdentity` PASS verbose) |
| 6 | `go test -race -count=1 ./internal/canonical/... ./internal/pool/... ./internal/adapter/...` passes | PASS (all 5 packages green) |
| 7 | One atomic commit titled `fix(arch-lint): relocate ErrPoolExhausted to canonical (TRST-04 restore, D-17-01)` | PASS (`f727b24`) |

## Threat Surface Scan

No new security-relevant surface introduced. The relocation REDUCES the adapter blast radius — adapters no longer import `internal/pool` for this code path, shrinking their dependency surface to canonical only. STRIDE register entries (T-17-01-01 Tampering on sentinel value, T-17-01-02 Repudiation on 503 mapping, T-17-01-03 InfoDisclosure on doc comments) all hold their planned dispositions: T-17-01-01 is `mitigate`'d via the new `errors_test.go` sentinel-identity test (Task 2 Step 2 as planned); T-17-01-02 and -03 remain `accept`. No `threat_flag:` annotations needed — the relocation is identity-preserving by Go semantics.

## User Setup Required

None — pure in-tree Go edits, zero new dependencies (T-17-01-SC remains accept).

## Self-Check: PASSED

- `internal/canonical/errors.go` modified: FOUND (`grep -n 'ErrPoolExhausted = errors.New' internal/canonical/errors.go` returns line 32)
- `internal/canonical/errors_test.go` created: FOUND (`ls -la internal/canonical/errors_test.go` exists; TestErrPoolExhausted_SentinelIdentity PASS verbose)
- `internal/pool/pool.go` alias wired: FOUND (`grep -n 'ErrPoolExhausted = canonical.ErrPoolExhausted' internal/pool/pool.go` returns line 21)
- All three handlers.go modified with canonical refs + pool import removed: FOUND (`grep -rc` confirms 8 sites; `grep -l '"otto-gateway/internal/pool"'` returns none)
- Comment refs updated in 3 sse.go/ndjson.go files: FOUND (`grep -n 'pool.ErrPoolExhausted' internal/adapter/{anthropic,ollama,openai}/{sse,ndjson}.go` returns empty)
- Commit `f727b24` exists: FOUND (`git log --oneline -3` confirms)
- SUMMARY.md exists at expected path: FOUND (this file)

## Next Plan Readiness

- **Plan 17-02 (REL-POOL-02 deflake)** can proceed — touches `internal/pool/regression_rel_pool_02_test.go` (resultWg / Result-drain plumbing). 17-01 touched pool.go (alias-only) and the three handlers.go files; no overlap with 17-02's test-only scope. The 17-02 flake reproduced in this plan's tree-wide `go test -race ./...` run (single failure on iteration 1 of the tree-wide run) confirms it is still live and 17-02 work is needed.
- **Phase 17 close** is now blocked solely on Plan 17-02 (REL-POOL-02 flake closure). 17-03 contributed fmt + lint + dead-code clean; 17-01 contributes arch-lint clean; 17-02 will contribute test-race clean. Once 17-02 lands, `make ci` should exit 0 end-to-end and v1.9.1 can ship (per D-17-03).
- **v1.9.1 tag (D-17-03)** is ready for issuance once 17-02 closes — references this plan's commit f727b24 + 17-02's eventual commit + 17-03's commit b78fd09 as the trust-gate-restoration ship-set.

---
*Phase: 17-trust-gate-restoration*
*Completed: 2026-06-11*
