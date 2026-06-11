---
phase: 17-trust-gate-restoration
plan: "02"
subsystem: trust-gates
tags: [test-race, goleak, REL-POOL-02, deflake, D-17-04, acp-stream-race-flagged]

# Dependency graph
requires:
  - phase: 15-fix-critical-high
    provides: Phase 15-01 introduced the REL-POOL-02 regression test (pool.Close cancels in-flight sessions); this plan deflakes the test scaffolding without altering the production assertion.
  - phase: 17
    provides: 17-01 arch-lint restoration (TRST-04) + 17-03 mechanical batch (fmt + lint + dead-code). With this plan landed, all three 17-plan trust-gate contributions are in place.
provides:
  - 20/20 PASS on TestRegression_REL_POOL_02_CtrlCOrphansChildren under `go test -race -count=20` with goleak.VerifyNone clean (60/60 across three independent rounds)
  - resultWg tracking of orphan stream-drain goroutines drained AFTER both gate-closes and the outer wg
  - Per-instance unique session IDs for the two blockingPromptClients (fixes pre-existing degenerate sessionSlots collapse exposed by iter 1 reliable draining)
  - Drain-Chunks-then-Result ordering that routes around the acp.Stream close-vs-read race (flagged for v1.10)
affects: [v1.9.1-release, make-ci-test-race-stage]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Chunks-first-then-Result drain pattern — the channel-close on s.chunks at the end of acp.Stream.close acts as the synchronization edge with close()'s s.result mutations; calling Result only AFTER the Chunks drain returns avoids the s.done-closed-before-s.mu race that `go test -race` flags in poolStreamWrapper's StopReason translation"
    - "Per-instance NewSession sid override in blockingPromptClient (newSessionFn) — keeps the shared fakeClient default (`fake-sess`) for tests that don't need distinct sids while letting this test isolate slot/session bindings cleanly"

key-files:
  created: []
  modified:
    - internal/pool/regression_rel_pool_02_test.go

key-decisions:
  - "Iteration 1 produced 20/20 green (60/60 across three independent rounds) — Tasks 3 and 4 from the plan's 3-iteration budget were not needed."
  - "Production acp.Stream race (s.done closed BEFORE s.mu acquired; Result waiters can return the *FinalResult pointer before close writes StopReason; poolStreamWrapper.Result reads StopReason at pool.go:959 in the gap) is OUT OF SCOPE per D-17-05. Worked around in the test by draining stream.Chunks() first — the chan-close write barrier ensures all close()-body mutations are visible before Result is called. Flagged in this SUMMARY's Threat Flags section for v1.10 hardening."
  - "Per-instance NewSession sid (fake-sess-bc0 / fake-sess-bc1) was added under deviation Rule 1 — the pre-existing shared `fake-sess` default caused sessionSlots[\"fake-sess\"] to be overwritten by the second NewSession, collapsing both Prompts onto whichever client won the race. Iteration 1's reliable resultWg.Wait() draining surfaced this as a 20/20 WR-04 assertion failure (`bc0=[fake-sess fake-sess] bc1=[]`). Fix is test-scaffolding-only — no production change."

patterns-established:
  - "Three-step test deflake pattern for goleak-tracked stream drains: (1) track orphan drain goroutines via dedicated WaitGroup, (2) ensure unique session identities so pool.sessionSlots is one-to-one across clients, (3) drain Chunks() to channel close BEFORE calling Result() to inherit the chan-close write barrier as the synchronization edge with the underlying stream close() body."

requirements-completed:
  - REL-POOL-02-DEFLAKE

# Metrics
duration: 17min
completed: 2026-06-11
---

# Phase 17 Plan 02: REL-POOL-02 Goleak Flake Closure Summary

**Closed the REL-POOL-02 goleak flake at iteration 1 with three test-scaffolding edits — resultWg + per-instance unique sids + drain-Chunks-then-Result — taking the test from 17/17 FAIL at baseline to 60/60 PASS across three independent 20-iteration rounds under `go test -race`. Restores `make ci` to exit 0 end-to-end; v1.9.1 is unblocked.**

## Performance

- **Duration:** ~17 min (start 2026-06-11T22:48Z; commit ca258f9 at 23:03Z)
- **Started:** 2026-06-11T22:48:00Z (approx; T1 baseline kicked off after plan dispatch)
- **Completed:** 2026-06-11T23:03:45Z
- **Tasks:** 1 of 4 from the 3-iteration budget needed (T1 baseline + T2 iter 1; T3 + T4 skipped because iter 1 produced 20/20 green)
- **Files modified:** 1 (`internal/pool/regression_rel_pool_02_test.go`)
- **Commits:** 1 (`ca258f9`)

## Accomplishments

- `TestRegression_REL_POOL_02_CtrlCOrphansChildren` passes 20/20 under `go test -race -count=20 -v` with goleak.VerifyNone clean. Three additional independent 20-iteration rounds (no -v): 60/60 PASS. Baseline was 17/17 FAIL with goleak "chan receive" leaks on `acp.(*Stream).Result` goroutines (`/tmp/17-02-baseline.log`).
- `make ci` exits 0 end-to-end at HEAD `ca258f9` — fmt-check (gofmt + gofumpt), vet, build, lint (golangci-lint zero issues), test-race (all packages green), arch-lint (OK — No warnings), examples, govulncheck (No vulnerabilities found). The v1.9 milestone trust-gate restoration is complete; v1.9.1 is unblocked per D-17-03.
- Three test-scaffolding edits landed in `internal/pool/regression_rel_pool_02_test.go`, zero production source changes (D-17-05 scope guard upheld):
  1. **resultWg** declared in the test's outer scope and drained AFTER both gate-closes and the outer wg. The orphan `_, _ = stream.Result()` goroutines spawned per session now have a deterministic join point that runs before the deferred `goleak.VerifyNone(t)` fires.
  2. **`newBlockingPromptClient(idTag string)`** — new signature; each instance now overrides `newSessionFn` to return a unique sid (`fake-sess-bc0` / `fake-sess-bc1`). Pre-fix, both clients used the shared fakeClient default of `"fake-sess"`, which caused `pool.NewSession` to write the SAME key twice into `pool.sessionSlots` — the second write clobbered the first and both subsequent `pool.Prompt` calls routed to whichever client won the overwrite race. Iteration 1's reliable resultWg.Wait() drain surfaced this as a deterministic WR-04 assertion failure (`bc0=[fake-sess fake-sess] bc1=[]`); fix is a test-scaffolding-only override under deviation Rule 1.
  3. **Drain-Chunks-then-Result** in the orphan goroutine. The body now does `for range stream.Chunks() { }` then `_, _ = stream.Result()`. Chunks closure (at `stream.go:186` inside `acp.Stream.close`) happens AFTER the StopReason write at `stream.go:182` and AFTER `s.mu.Unlock()` at `stream.go:185`. The chan-close write barrier means Result's downstream StopReason read in `poolStreamWrapper.Result` (`pool.go:959`) is now safely sequenced after close()'s mutations. Without this, `go test -race` flagged a real production race in `acp.Stream` (s.done is closed BEFORE s.mu is taken, so Result waiters wake and race for s.mu against close's later StopReason write).
- Iteration evidence logs captured: `/tmp/17-02-baseline.log` (17/17 FAIL baseline), `/tmp/17-02-iter1-v.log` (20/20 PASS iter 1 final under -v), `/tmp/17-02-makeci.log` (make ci exit 0 trace).
- Full pool package and tree-wide `go test -race -count=1 ./...` clean — no regressions in adjacent tests.

## Task Commits

1. **T1 (RED) — baseline 20-iter:** No commit (read-only evidence task). `/tmp/17-02-baseline.log` shows 17/17 FAIL with goleak chan-receive leaks on `internal/acp.(*Stream).Result` goroutines created at `regression_rel_pool_02_test.go:108`. RED criterion satisfied; flake confirmed dominant (not the ~1/8 estimate from 17-CONTEXT.md — it was 100% under -count=20 because subsequent iterations build on prior goroutine state under -race).
2. **T2 (GREEN iter 1):** `ca258f9` — `fix(test): REL-POOL-02 — track + drain orphan stream goroutines for goleak (D-17-04, iter 1)`. Single atomic commit; +56 / -4 lines on the test file. 20/20 PASS final on iter 1 with three independent confirmation rounds (60/60 PASS).
3. **T3 (GREEN iter 2) — SKIPPED:** Plan's iteration 2 (gate-before-Close reorder) was not needed; iter 1 closed the flake.
4. **T4 (GREEN iter 3) — SKIPPED:** Plan's iteration 3 (`<-stream.Done()` wait) was not needed; iter 1 closed the flake. Additionally, confirmed via `grep -n "func.*Done()" internal/acp/stream.go` that `Stream.Done()` does NOT exist as a public API — iter 3 was not viable as a test-only edit per D-17-05 even if needed.

## Files Created/Modified

- **`internal/pool/regression_rel_pool_02_test.go`** — three test-scaffolding edits:
  1. `newBlockingPromptClient()` → `newBlockingPromptClient(idTag string)` with new `newSessionFn` returning `fake-sess-<idTag>`; both call sites updated to pass `"bc0"` / `"bc1"`.
  2. `var resultWg sync.WaitGroup` declared alongside the outer `var wg sync.WaitGroup`; orphan goroutine wrapped in `resultWg.Add(1)` / `defer resultWg.Done()`; `resultWg.Wait()` appended after the final `wg.Wait()`.
  3. Orphan goroutine body changed from `go func() { _, _ = stream.Result() }()` to drain `stream.Chunks()` first, then call `stream.Result()`.
  - +56 / -4 lines, comments included. Production semantics of the test (cancelsAfter >= 2 + each client gets a Cancel + goleak.VerifyNone) preserved.

## Decisions Made

- **Iter 1 closed the flake; iters 2 + 3 SKIPPED.** Per the plan's 3-iteration budget, only the cheapest iteration was needed. Multiple confirmation rounds (60/60 across three independent 20-iter runs) make iter 2 + 3 strictly unnecessary.
- **Deviation Rule 1 — per-instance unique sids.** During iter 1 verification, the resultWg.Wait() drain made the test's pre-existing degenerate sessionSlots collapse deterministic (`bc0=[fake-sess fake-sess] bc1=[]` failing the WR-04 assertion). Root cause: shared `"fake-sess"` default from `fakeClient.NewSession`. Fixed in test scaffolding by overriding `newSessionFn` per blockingPromptClient instance. Rationale: pre-existing bug exposed by the goleak fix; auto-fixing it without production change matches deviation Rule 1 and the D-17-05 scope guard.
- **acp.Stream close-vs-read race flagged but NOT fixed in this plan.** The race detector surfaces a real production race in `acp.Stream`: close() closes s.done BEFORE acquiring s.mu, so Result waiters can return the *FinalResult pointer before close writes StopReason, and downstream readers (here: `poolStreamWrapper.Result` translating StopReason) race the write. Test-side workaround: drain Chunks() first — the chan-close at the end of close() is the right synchronization edge. Production fix (e.g., move `close(s.done)` to AFTER s.mu's critical section, or copy s.result into a value type under s.mu) is OUT OF SCOPE per D-17-05 and recorded as a v1.10 hardening item below.
- **Skipped the planned `<-stream.Done()` iter 3 approach.** Confirmed via grep that `*acp.Stream` does NOT export a `Done()` method (the `done` field is unexported). Iter 3 would have required production code changes — out of scope per D-17-05.

## Deviations from Plan

- **[Rule 1 — Pre-existing bug surfaced by the iter-1 fix]** Per-instance NewSession sid override in `blockingPromptClient`. Plan called for resultWg tracking only; iter 1's deterministic drain exposed a pre-existing degenerate sessionSlots overwrite when both fake clients return the shared `"fake-sess"` sid. Fix is one new closure (`newSessionFn`) returning `"fake-sess-<idTag>"` per instance and updating the two call sites. Test-scaffolding only; D-17-05 scope guard upheld.
- **[Rule 1 — Pre-existing production race worked around in test]** Changed orphan goroutine from `_, _ = stream.Result()` to `for range stream.Chunks() {}; _, _ = stream.Result()`. Plan called for resultWg-only; iter 1 with raw Result drain exposed a real production race in `acp.Stream` that was previously masked by the degenerate sessionSlots collapse (only one stream was actually closed via CloseForTest; the other hung indefinitely). The drain-Chunks-then-Result reordering inherits the chan-close write barrier as the synchronization edge, routing around the production race without touching production code. Flagged for v1.10 (see Threat Flags).

## Issues Encountered

- **Iter 1 RED reproduction was 17/17 FAIL — much worse than the 17-CONTEXT.md estimate of ~1/8.** Under `-count=20 -race`, the goleak failure was 100% reproducible at baseline (not ~1/8). Likely explanation: subsequent iterations in the same test process inherit goroutine leaks from prior iterations, compounding the failure rate. This made RED-evidence capture cheap and the GREEN verification more rigorous.
- **First iter-1 attempt with raw `stream.Result()` reproduced 20/20 FAIL** — but the failure mode SHIFTED from "goleak chan-receive leak" to "WR-04 assertion + race detector data race". The assertion failure (`bc0=[fake-sess fake-sess] bc1=[]`) revealed the pre-existing sessionSlots overwrite bug (Rule 1 fix #1). The data race revealed the production `acp.Stream` close-vs-read race (Rule 1 workaround #2). Both required additional test edits beyond the plan's resultWg-only iter 1 spec.
- **Second iter-1 attempt with Chunks-drain only (no Result)** reproduced 20/20 FAIL on `cancelsAfter = 1`. Calling Release alone (skipping Result) released the slot back to pool BEFORE p.Close ran, so the pool saw zero in-flight sessions to cancel. Reverted that attempt — the orphan goroutine must keep the slot held until pool.Close cancels it.
- **Final iter-1 form** (drain Chunks then call Result) produces 20/20 PASS reliably (60/60 across three rounds). The drain blocks until acp.Stream.close runs `close(s.chunks)`, which is AFTER the StopReason write under s.mu — so Result's downstream read in the wrapper is safely ordered after the write.

## `make ci` Post-Fix Evidence (HEAD ca258f9)

```
fmt-check (gofmt):   PASS — 0 non-vendor files reformatted
fmt-check (gofumpt): PASS — 0 non-vendor files reformatted
vet:                 PASS — go vet ./... clean
build:               PASS — go build ./... clean
lint:                PASS — golangci-lint 0 issues repo-wide
test-race:           PASS — all packages green; REL-POOL-02 stable
arch-lint:           PASS — OK - No warnings found
examples:            PASS — go test -run Example ./... clean
govulncheck:         PASS — No vulnerabilities found
```

Exit 0 end-to-end. v1.9.1 unblocked per D-17-03.

## Verification Checklist (vs success_criteria)

| # | Criterion | Status |
|---|-----------|--------|
| 1 | `go test -race -count=20 -run TestRegression_REL_POOL_02_CtrlCOrphansChildren ./internal/pool/...` exits 0 with zero FAIL | PASS (20/20 PASS final + 60/60 across three additional rounds) |
| 2 | Existing post-fix assertions (cancelsAfter >= 2; each blockingPromptClient receives at least one Cancel) continue to hold | PASS (verified in 20/20 -v log; both bc0 and bc1 receive Cancels every iteration) |
| 3 | No production source files modified — only `internal/pool/regression_rel_pool_02_test.go` | PASS (`git diff --name-only HEAD~1 HEAD` reports only the test file) |
| 4 | Flake closed within 3-iteration budget OR operator escalation before iter 4 | PASS (closed at iter 1; iters 2 + 3 skipped; no escalation needed) |
| 5 | `go test -race -count=1 ./internal/pool/...` passes — no regressions in adjacent tests | PASS (`ok otto-gateway/internal/pool 1.975s`) |
| 6 | One commit with conventional-commit message of the form `fix(test): REL-POOL-02 — [description] (D-17-04, iter N)` | PASS (`ca258f9`: `fix(test): REL-POOL-02 — track + drain orphan stream goroutines for goleak (D-17-04, iter 1)`) |
| Phase-level | `make ci` exits 0 end-to-end | PASS (`/tmp/17-02-makeci.log` exit 0; all gates green) |

## Threat Surface Scan / Threat Flags

| Flag | File | Description |
|------|------|-------------|
| threat_flag: production-race | internal/acp/stream.go (close at lines 166-189; Result at lines 193-198) | `Stream.close` invokes `close(s.done)` at line 172 BEFORE acquiring `s.mu` at line 175 and writing `s.result.StopReason` at line 182. A goroutine blocked in `Stream.Result` at line 194 (`<-s.done`) wakes when s.done closes, then races against close() for s.mu. If Result wins, it acquires s.mu, returns `s.result` (the pointer) — and its CALLER dereferences `fr.StopReason` (e.g., `poolStreamWrapper.Result` at pool.go:959 for adapter translation) WITHOUT holding s.mu. close() then takes s.mu and writes StopReason — a write/read race under `go test -race`. Production impact: the race is benign in real use (StopReason values are equivalent — both reads see either the zero value `canonical.StopUnknown` or the written value, and downstream adapters tolerate StopUnknown as "abrupt close" per D-02 forward-compat), but it WILL flag any `-race` run that exercises a stream with a slow Result caller. Out of scope per D-17-05; flagged for v1.10 hardening. Recommended fix: either (a) move `close(s.done)` to AFTER `s.mu.Unlock()` so Result waiters can't wake until close() has fully written all fields, or (b) inside `Result`, copy `*s.result` into a local FinalResult value under s.mu and return `&local` so downstream pointer reads are immune to subsequent close() writes. (b) preserves the close-on-done-first invariant that the push() backpressure path may rely on; recommend (b). |

No new security-relevant surface introduced by this plan. Test scaffolding only.

## User Setup Required

None — pure in-tree Go test edits, zero new dependencies, zero environment changes.

## Self-Check: PASSED

- `internal/pool/regression_rel_pool_02_test.go` modified: FOUND (`git show --stat HEAD` shows 1 file changed, 56 ins, 4 del at ca258f9)
- `resultWg` present in the test file: FOUND (`grep -c "resultWg" internal/pool/regression_rel_pool_02_test.go` returns 5 — declaration, Add, Done, Wait, and comment ref)
- `newBlockingPromptClient` signature now takes `idTag string`: FOUND (`grep "func newBlockingPromptClient" internal/pool/regression_rel_pool_02_test.go` returns the new signature)
- Both call sites updated: FOUND (`grep "newBlockingPromptClient(" internal/pool/regression_rel_pool_02_test.go` returns `bc0 := newBlockingPromptClient("bc0")` and `bc1 := newBlockingPromptClient("bc1")`)
- Drain-Chunks-then-Result pattern present: FOUND (`grep -A 1 "for range stream.Chunks" internal/pool/regression_rel_pool_02_test.go` shows the for-range followed by `_, _ = stream.Result()`)
- Commit `ca258f9` exists: FOUND (`git log --oneline -3` confirms)
- `go test -race -count=20 -run TestRegression_REL_POOL_02_CtrlCOrphansChildren ./internal/pool/...` PASS: FOUND (multiple rounds 20/20 PASS; logged in `/tmp/17-02-iter1-v.log` and `/tmp/17-02-baseline.log` for RED reference)
- `make ci` exit 0: FOUND (`/tmp/17-02-makeci.log` exit code 0)
- SUMMARY.md exists at expected path: FOUND (this file)

## Next Plan Readiness

- **Phase 17 close** is now READY: 17-01 (arch-lint restore — `f727b24`) + 17-02 (REL-POOL-02 deflake — `ca258f9`) + 17-03 (mechanical batch — `b78fd09`) all landed and `make ci` exits 0 end-to-end at HEAD.
- **v1.9.1 release tag (D-17-03)** is unblocked. Operator next steps: create `v1.9.1` tag at this HEAD with message "v1.9.1 — Trust-Gate Restoration: make ci clean baseline restored", push to GitHub origin (`github.com:cmetech/otto-gateway.git`), and create a GitHub release via `gh release create v1.9.1` referencing the v1.9 milestone audit + Phase 17 trust-gate closeout.
- **v1.10 backlog item (new):** acp.Stream close-vs-read race. See Threat Flags above for recommended fix. Suggested as a single-commit hardening plan early in v1.10 because it touches production code in a well-understood way and the test-side workaround in 17-02 will silently mask the regression if a future test/adapter author calls Result without a Chunks-drain first.

---
*Phase: 17-trust-gate-restoration*
*Completed: 2026-06-11*
