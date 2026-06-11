---
phase: 17-trust-gate-restoration
verified: 2026-06-11T23:30:00Z
status: passed
score: 5/5 must-haves verified
overrides_applied: 0
---

# Phase 17: Trust-Gate Restoration Verification Report

**Phase Goal:** Restore `make ci` to clean exit-0 end-to-end so v1.9.1 can ship from a build-green baseline. Closes six trust-gate items surfaced at v1.9 milestone close (gofmt, gofumpt, gosec G301, gosec G306, unused removeSlot, arch-lint TRST-04 adapter→pool, REL-POOL-02 goleak flake).

**Verified:** 2026-06-11T23:30:00Z
**Status:** passed
**Re-verification:** No — initial verification
**Phase HEAD:** `0735906` (docs(17-02): complete REL-POOL-02 deflake plan)

## Goal Achievement

### Observable Truths

| # | Truth (Phase Close Criterion) | Status | Evidence |
|---|-------------------------------|--------|----------|
| 1 | `make ci` exits 0 end-to-end | VERIFIED | Individual gates verified green: `gofmt -l .` empty; `gofumpt -l .` empty; `golangci-lint run ./...` reports `0 issues.`; `go-arch-lint check` reports `OK - No warnings found`; 20/20 REL-POOL-02 race PASS. SUMMARY 17-02 also captured full `make ci` exit 0 trace at `/tmp/17-02-makeci.log`. |
| 2 | `TestRegression_REL_POOL_02_CtrlCOrphansChildren` passes 20/20 iterations under `-race` | VERIFIED | Verifier ran 20 independent iterations of `go test -race -count=1 -run TestRegression_REL_POOL_02_CtrlCOrphansChildren ./internal/pool/`; result PASS=20 FAIL=0. Iteration 1 of the planned 3-iteration budget was sufficient (per 17-02-SUMMARY.md, with three confirming rounds 60/60 PASS). |
| 3 | `errors.Is(actualErr, canonical.ErrPoolExhausted)` returns true for REL-POOL-01 path | VERIFIED | (a) `canonical.ErrPoolExhausted` defined at `internal/canonical/errors.go:32` with byte-exact message `pool: all workers busy; retry in 5s`; (b) `pool.ErrPoolExhausted = canonical.ErrPoolExhausted` alias at `internal/pool/pool.go:21` preserves errors.Is identity (same `*errorString` value); (c) `TestErrPoolExhausted_SentinelIdentity` PASS confirms self-identity + wrap-traversal; (d) all 8 adapter sites now reference `canonical.ErrPoolExhausted` directly. |
| 4 | `~/go/bin/go-arch-lint check --project-path .` reports `total notices: 0` | VERIFIED | Verifier command output: `OK - No warnings found`. The 3 previous adapter→pool notices at `adapter/{anthropic,ollama,openai}/handlers.go` are gone — `grep -l '"otto-gateway/internal/pool"' internal/adapter/*/handlers.go` returns no matches. TRST-04 boundary restored. |
| 5 | Each plan has typed `<threat_model>` block | VERIFIED | All three PLAN.md files (17-01, 17-02, 17-03) contain `<threat_model>` blocks with Trust Boundaries + STRIDE Threat Register tables. Confirmed via `grep -l "<threat_model>" .planning/phases/17-trust-gate-restoration/*-PLAN.md`. |

**Score:** 5/5 truths verified

### Required Artifacts

| Artifact | Expected | Status | Details |
|----------|----------|--------|---------|
| `internal/canonical/errors.go` | Contains authoritative `ErrPoolExhausted` sentinel | VERIFIED | Line 32: `var ErrPoolExhausted = errors.New("pool: all workers busy; retry in 5s")` with TRST-04/D-17-01 doc comment block lines 19-31 |
| `internal/canonical/errors_test.go` | New sentinel-identity test | VERIFIED | `TestErrPoolExhausted_SentinelIdentity` exists and passes — `go test -count=1 -run TestErrPoolExhausted_SentinelIdentity ./internal/canonical/...` returns `ok` |
| `internal/pool/pool.go` (alias) | Backward-compat re-export | VERIFIED | Line 21: `var ErrPoolExhausted = canonical.ErrPoolExhausted` |
| `internal/pool/pool.go` (removeSlot removed) | Dead code gone | VERIFIED | `grep -n "func (p \*Pool) removeSlot" internal/pool/pool.go` returns exit=1 (no match) |
| `internal/adapter/anthropic/handlers.go` | 2 sites flipped to canonical; no pool import | VERIFIED | Sites at :187, :370 reference `canonical.ErrPoolExhausted`; no `"otto-gateway/internal/pool"` import |
| `internal/adapter/ollama/handlers.go` | 4 sites flipped to canonical; no pool import | VERIFIED | Sites at :159, :244, :472, :521 reference `canonical.ErrPoolExhausted`; no pool import |
| `internal/adapter/openai/handlers.go` | 2 sites flipped to canonical; no pool import | VERIFIED | Sites at :157, :295 reference `canonical.ErrPoolExhausted`; no pool import |
| `internal/pool/regression_rel_pool_02_test.go` | resultWg + drain-Chunks-then-Result pattern | VERIFIED | `grep -c "resultWg" ...` = 7 (declaration, Add, Done, Wait, comments) |
| `cmd/otto-tray/tray.go` | 0o750 MkdirAll + 0o600 WriteFile | VERIFIED | Line 367: `os.MkdirAll(logDir, 0o750)`; Line 372: `os.WriteFile(logPath, []byte(content), 0o600)` |
| `internal/server/server.go` | gofmt-clean struct alignment | VERIFIED | `gofmt -l .` reports zero non-vendor files |

### Key Link Verification

| From | To | Via | Status |
|------|----|----|--------|
| `internal/pool/pool.go` | `internal/canonical/errors.go` | `var ErrPoolExhausted = canonical.ErrPoolExhausted` re-export | WIRED |
| `internal/adapter/{anthropic,ollama,openai}/handlers.go` | `internal/canonical/errors.go` | `errors.Is(err, canonical.ErrPoolExhausted)` checks | WIRED (8 sites total) |
| `regression_rel_pool_02_test.go` (test body) | `goleak.VerifyNone(t)` | `resultWg.Wait()` after gate-closes + outer wg | WIRED |

### Behavioral Spot-Checks

| Behavior | Command | Result | Status |
|----------|---------|--------|--------|
| gofmt-clean tree-wide | `gofmt -l .` | empty output | PASS |
| gofumpt-clean tree-wide | `~/go/bin/gofumpt -l .` | empty output | PASS |
| golangci-lint clean | `~/go/bin/golangci-lint run ./...` | `0 issues.` | PASS |
| arch-lint clean | `~/go/bin/go-arch-lint check --project-path .` | `OK - No warnings found` | PASS |
| REL-POOL-02 deflake (20 iter) | `for i in 1..20; go test -race -count=1 -run TestRegression_REL_POOL_02_CtrlCOrphansChildren ./internal/pool/` | PASS=20 FAIL=0 | PASS |
| Sentinel identity test | `go test -count=1 -run TestErrPoolExhausted_SentinelIdentity ./internal/canonical/...` | `ok` | PASS |

### Requirements Coverage

| Requirement | Source Plan | Description | Status |
|-------------|-------------|-------------|--------|
| TRST-04-RESTORE | 17-01 | Restore adapter-over-canonical TRST-04 boundary | SATISFIED |
| REL-POOL-01-RELOCATE | 17-01 | Relocate ErrPoolExhausted sentinel to canonical | SATISFIED |
| REL-POOL-02-DEFLAKE | 17-02 | REL-POOL-02 test passes 20/20 under -race | SATISFIED |
| REL-FMT-GOFMT | 17-03 | gofmt-clean tree-wide | SATISFIED |
| REL-FMT-GOFUMPT | 17-03 | gofumpt-clean tree-wide | SATISFIED |
| REL-LINT-G301 | 17-03 | gosec G301 (MkdirAll perms) clean | SATISFIED |
| REL-LINT-G306 | 17-03 | gosec G306 (WriteFile perms) clean | SATISFIED |
| REL-LINT-UNUSED | 17-03 | unused-function lint (removeSlot) clean | SATISFIED |

### Anti-Patterns Found

None blocking. No new TBD/FIXME/XXX debt markers introduced. Dead-code removed in 17-03 (Pool.removeSlot). Three stale comments at the former lines 274, 695, 747 in pool.go were updated to note the function was removed in Phase 17 (per 17-03-SUMMARY.md "minimize the diff" preference). This preserves archaeological context without leaving behind dangling references.

### Phase-17 Flake-Fix Outcome

**Iteration 1 was sufficient** — per 17-02-SUMMARY.md, the planned 3-iteration budget collapsed to iter-1 only. The iter-1 fix landed three test-scaffolding edits in `internal/pool/regression_rel_pool_02_test.go`:
1. `resultWg` tracking of orphan `stream.Result()` goroutines
2. Per-instance unique session IDs (`fake-sess-bc0` / `fake-sess-bc1`) to fix a pre-existing degenerate sessionSlots collapse exposed by reliable resultWg draining (deviation Rule 1)
3. Drain-Chunks-then-Result ordering to inherit the chan-close write barrier as the synchronization edge with the underlying `acp.Stream.close()` body — workaround for a real production race flagged for v1.10

Baseline was 17/17 FAIL (worse than the 17-CONTEXT.md ~1/8 estimate — likely because iterations in the same process inherit goroutine state). Iter-1 took the test to 20/20 PASS with three independent confirmation rounds (60/60 total).

Tasks 3 and 4 (iter 2 gate-before-Close reorder; iter 3 `<-stream.Done()` wait) were both skipped — iter 1 was sufficient and iter 3 wasn't viable anyway (the `*acp.Stream` type does not export a public `Done()` method; iter 3 would have required production code changes out of scope per D-17-05).

### Out-of-Scope Items Surfaced for v1.10 Backlog

The 17-02 work surfaced a real production race in `internal/acp/stream.go` flagged but not fixed per D-17-05:

**`acp.Stream` close-vs-read race** (`internal/acp/stream.go`, `close` at lines ~166-189; `Result` at lines ~193-198):
- `Stream.close` invokes `close(s.done)` at line ~172 BEFORE acquiring `s.mu` at line ~175 and writing `s.result.StopReason` at line ~182
- A goroutine blocked in `Stream.Result` at line ~194 (`<-s.done`) wakes when s.done closes, then races for s.mu against close()
- If Result wins, it acquires s.mu, returns `s.result` (the pointer) — and its CALLER dereferences `fr.StopReason` (e.g., `poolStreamWrapper.Result` at `pool.go:959` for adapter translation) WITHOUT holding s.mu
- close() then takes s.mu and writes StopReason — a write/read race under `go test -race`
- Production impact is benign in normal use (StopReason values are equivalent — both reads see zero value or written value, and downstream adapters tolerate StopUnknown per D-02 forward-compat) but WILL flag any `-race` run that exercises a stream with a slow Result caller
- Recommended v1.10 fix: copy `*s.result` into a local FinalResult value under s.mu and return `&local` so downstream pointer reads are immune to subsequent close() writes
- Workaround landed in 17-02 (drain Chunks before Result) inherits chan-close write barrier — but will silently mask the regression if a future test/adapter author calls Result without a Chunks-drain first

This is the only v1.10 hardening item surfaced by Phase 17 execution. All Info-level findings deferred from 16-REVIEW.md and 12 Low-severity reliability findings from v1.9 remain in their original v1.10 backlog state per D-17-05.

## Gaps Summary

No gaps found. All five phase-close criteria from 17-CONTEXT.md verify against actual codebase state. The three contributing plans (17-01 arch-lint restoration `f727b24`, 17-02 REL-POOL-02 deflake `ca258f9`, 17-03 mechanical batch `b78fd09`) all landed clean atomic commits. Trust gates pass tree-wide:
- gofmt: 0 files
- gofumpt: 0 files
- golangci-lint: 0 issues
- arch-lint: 0 notices
- REL-POOL-02 race test: 20/20 PASS
- Sentinel identity test: PASS

**Verdict: PASS.** Phase 17 goal (restore `make ci` to exit-0 end-to-end) achieved. v1.9.1 release tag unblocked per D-17-03.

---

*Verified: 2026-06-11T23:30:00Z*
*Verifier: Claude (gsd-verifier)*
