---
quick_id: 260528-d84
slug: phase-9-closeout-goleak-property-tests-e
description: Phase 9 close-out — goleak coverage gaps + property tests + Example_buildBlocks + CI workflow + ROADMAP housekeeping
date: 2026-05-28
status: in-progress
---

# Quick Task 260528-d84: Phase 9 Closeout

## Why

Phase 9 (Distribution + Trust-gate gating) has been over-delivered on the distribution side — `make package` produces darwin-{arm64,amd64} + linux-amd64 + windows-amd64 archives with SHA256SUMS, ad-hoc macOS codesign, daily log rotation, packaging layout, README, `/health/pool` endpoint, etc. — all of which exceed the original Phase 9 success-criteria scope.

What remains is the trust-gate completeness cluster (TRST-05/06/07) plus the CI merge-gate (SC3). Surgical work. Closing it out cleanly so v1.5 milestone can complete and Phase 9 is marked done.

## Tasks (atomic — one commit per)

### Task 1 — goleak.VerifyTestMain in three missing packages (TRST-05)

**Files:** create `internal/auth/testmain_test.go`, `internal/config/testmain_test.go`, `cmd/otto-gateway/testmain_test.go`

Mirror the canonical pattern from `internal/canonical/testmain_test.go`. Package suffix `_test` on auth and config (matches their existing `*_test` blackbox files); `package main` for cmd/otto-gateway. Add `go.uber.org/goleak` import; `TestMain` calls `goleak.VerifyTestMain(m)`.

**Verify:** `go test ./internal/auth/ ./internal/config/ ./cmd/otto-gateway/ -count=1` — all pass (no goroutine leaks revealed).

**Commit:** `test(trust): goleak.VerifyTestMain in auth/config/cmd (close TRST-05)`

### Task 2 — Property tests for buildBlocks + coerceToolCall (TRST-06)

**Files:** create `internal/engine/build_acp_property_test.go` (rapid-based property tests).

Add `pgregory.net/rapid` to go.mod (verify not already present).

**Invariants for buildBlocks:**
- Never panics on any valid `*canonical.ChatRequest` (random Roles, random text content, random message counts 0–10)
- For requests with `len(Messages) > 0` and no System role, output blocks > 0
- Every output Block has a valid (recognized) Role
- Image blocks NEVER appear in the output when no canonical Message had a `ContentKindImage` part
- Idempotent: same input → same output (run twice, compare)

**Invariants for coerceToolCall:**
- Never panics on any input
- Idempotent: `coerce(coerce(x)) == coerce(x)` for valid inputs
- Returns same shape (same type, same fields populated) on repeated calls with same input

**Adversarial cases (separate `TestCoerceToolCall_NeverPanics_Adversarial`):** zero values, empty slices, oversized strings (10K+ chars), deeply nested structures.

**Verify:** `go test ./internal/engine/ -run 'Property' -count=1 -race` — all pass.

**Commit:** `test(trust): property tests for buildBlocks + coerceToolCall (TRST-06)`

### Task 3 — Example_buildBlocks (TRST-07 closure)

**Files:** add Example function to `internal/engine/build_acp_test.go`.

Follow the same shape as the existing `ExampleCoerceToolCall` (in `internal/engine/coerce_test.go:399`) and `Example_pickCwd` (in `internal/engine/pickcwd_test.go:246`).

The Example must produce stable output verifiable by `go test -run Example`. Use a small fixture: 1 user message with text "hello", show the resulting block count + first block's role/content.

**Verify:** `go test ./internal/engine/ -run Example -count=1 -v` — Example_buildBlocks output matches.

**Commit:** `test(trust): Example_buildBlocks for go test -run Example (TRST-07)`

### Task 4 — .github/workflows/ci.yml (SC3)

**Files:** create `.github/workflows/ci.yml`.

Triggers: push to main, PR to main. Jobs:
1. **lint-test-arch:** Ubuntu runner. `actions/checkout@v4`, `actions/setup-go@v5` with `go-version-file: go.mod`. Install `golangci-lint`, `go-arch-lint@v1.15.0`, `govulncheck`. Run `make lint`, `make test-race`, `make arch-lint`, `govulncheck ./...`.
2. **cross-compile-smoke:** Ubuntu runner. Same setup. Run `make cross` — verify all four binaries (darwin-arm64, darwin-amd64, linux-amd64, windows-amd64.exe) build clean. Use matrix to keep concurrency, or single job sequential — single job is simpler.

Use caching (`actions/setup-go@v5` provides Go module + build cache automatically when `cache: true` is set, which is default).

**Verify:** YAML syntactically valid via `yamllint` or `python -c "import yaml; yaml.safe_load(open('.github/workflows/ci.yml'))"`.

**Commit:** `ci: GitHub Actions workflow gating lint+test+arch+vuln+cross (SC3)`

### Task 5 — ROADMAP + REQUIREMENTS housekeeping (Phase 9 closure)

**Files:** edit `.planning/ROADMAP.md`, `.planning/REQUIREMENTS.md`, `.planning/STATE.md`.

ROADMAP:
- Flip Phase 9 checkbox to `[x]` with `(completed 2026-05-28)`
- Add Phase 9's status table row showing 5/5 plans complete (or note "completed via /gsd-quick close-out, no separate phase plans")

REQUIREMENTS.md:
- Flip BLD-02, BLD-03, BLD-04, TRST-04, TRST-05, TRST-06, TRST-07 status from Pending → Complete in the mapping table at the bottom

STATE.md:
- Update `last_activity` to "2026-05-28 -- Phase 9 closed via /gsd-quick; v1.5 milestone complete"
- Update `completed_phases` counter (was 9; bump to 10)
- Update Current Focus / Position to reflect milestone-complete

**Verify:** `grep -E "Phase 9.*\[x\]" .planning/ROADMAP.md` matches; `grep "BLD-02.*Complete" .planning/REQUIREMENTS.md` matches.

**Commit:** `docs(09): close Phase 9 — Distribution + Trust-gate gating complete`

## Final close-out

After all 5 commits land:
- Write SUMMARY.md in this quick task directory
- Update STATE.md "Quick Tasks Completed" table

## What this does NOT include

- Pushing to remote / creating PR (user owns merge)
- Verifying CI workflow execution (workflow gates run when merged, not in local validation)
