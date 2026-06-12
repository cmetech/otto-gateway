---
phase: 20-code-review-backlog-burn-down
fixed_at: 2026-06-12T13:37:19Z
review_path: .planning/phases/20-code-review-backlog-burn-down/20-REVIEW.md
iteration: 1
findings_in_scope: 3
fixed: 2
skipped: 1
status: partial
---

# Phase 20: Code Review Fix Report

**Fixed at:** 2026-06-12T13:37:19Z
**Source review:** `.planning/phases/20-code-review-backlog-burn-down/20-REVIEW.md`
**Iteration:** 1

**Summary:**
- Findings in scope (Critical + Warning): 3
- Fixed: 2
- Skipped: 1

`make ci` exit code after final fix: **0**

## Fixed Issues

### WR-01: `Server.forceCloseCh` is mutated after construction with no synchronization or single-call guard

**Files modified:** `internal/server/server.go`, `internal/server/run_direct_test.go`
**Commit:** `275def8`
**Applied fix:** Added `forceCloseOnce sync.Once` field to `Server` and a new
`ensureForceCloseCh()` helper that lazily allocates the channel exactly once.
Replaced the bare `s.forceCloseCh = make(chan struct{})` at server.go:537 with
`s.ensureForceCloseCh()`. Added `TestServer_EnsureForceCloseCh_Idempotent` that
calls the helper twice and asserts the same channel value is observed — so the
"do not call RunUntilSignal twice on the same *Server" contract is enforced in
code rather than only in comments, and a future supervisor pattern that
re-enters RunUntilSignal cannot leak the first channel or any goroutine parked
on it. The nil-channel-select-arm contract for direct `Run` callers (D-20-04)
is preserved verbatim. Took the reviewer's Option A (sync.Once-guarded
allocation) over Option B (pass channel into Run) per the REVIEW.md note that
Option B is out of scope for a refactor-only phase.

### WR-02: `TestServer_Run_DirectShutdown` time.Sleep race; missing readiness signal

**Files modified:** `internal/server/run_direct_test.go`
**Commit:** `cdb2fe5`
**Applied fix:** Dropped the `time.Sleep(50 * time.Millisecond)` between
`go s.Run(ctx)` and `cancel()`, along with the misleading "gives the server a
moment to bind and enter the serve loop" comment. Replaced the comment with a
Phase-20-WR-02-labelled block that documents the actual semantics (Run's outer
select fires on `ctx.Done()` regardless of whether `ListenAndServe` has reached
its blocking call). The 5s deadline on the `done` channel remains the load-
bearing liveness assertion. Verified under `go test -race -count=5
-run TestServer_Run_DirectShutdown ./internal/server/` — no flakes.

The reviewer offered two paths (drop the sleep, OR thread a real listener via
the `httptest`-style readiness pattern). Took the minimum-touch path per
Phase 20's refactor-only character — the test as written is really asserting
"Run unblocks on ctx.Done()", so removing the sleep aligns the code with the
real assertion and removes the anti-pattern without requiring a structural
listener-handoff refactor of the Server type.

## Skipped Issues

### WR-03: `regression_rel_pool_02_test.go` uses `time.Sleep` as a session-readiness signal

**File:** `internal/pool/regression_rel_pool_02_test.go:134`
**Reason:** Skipped per the phase-scope guidance in the spawning configuration:
the `time.Sleep(100ms)` at line 134 *predates* Phase 20 — it was introduced
under Plan 17-02 / D-17-04 (the same iteration that added the `resultWg`
WaitGroup at lines 100-108) — and the reviewer explicitly notes "the test
predates Phase 20" and "Phase 20 does not own the fix." The file is technically
in the changed-file set because QUAL-05/06 touched it, but the QUAL-05/06
edits were leak-tracking cleanups, not a rewrite of the readiness signalling.

The reviewer's two proposed fixes both expand Phase 20's blast radius beyond
its refactor-only contract:

1. **Option A (preferred per REVIEW.md):** add a `started` channel inside
   `blockingPromptClient.promptFn` so the test can `<-started` for each
   client. This requires modifying the `blockingPromptClient` test helper
   (a structural change to test plumbing that other regression tests may
   depend on) plus the goroutine bodies inside this test.
2. **Option B (poll):** replace the sleep with a `for p.Stats().Busy != 2`
   busy-loop. This requires verifying that `Pool.Stats()` exposes a `Busy`
   count with the right semantics and timing relative to the
   `Prompt`-blocked-on-`promptFn` state — a non-trivial cross-package
   inspection that goes beyond mechanical refactor.

Per the phase-scope guidance ("Skip if a fix would meaningfully change
runtime behavior or require structural changes beyond the reviewer's
specific recommendation"), this finding is deferred to a follow-up phase
that explicitly owns pool-test readiness signalling. The latent flake is
documented here and in REVIEW.md WR-03 so it can be picked up under a
dedicated test-hygiene phase.

**Original issue (verbatim from REVIEW.md):**
> `time.Sleep(100 * time.Millisecond)` after spawning two goroutines that each
> `NewSession` + `Prompt` is hoped to be "wait for both sessions to be
> established." Under CI load or with `-race`, this can fire before either
> session has reached the blocked `Prompt` state — the assertion that
> `pool.Close()` issued Cancel to BOTH clients then becomes timing-dependent.
> The test predates Phase 20 but is in the changed-file set per the config
> block, and Phase 20 marks it as having a QUAL-05/06 cleanup; this latent
> flake should be filed even if Phase 20 does not own the fix.

---

_Fixed: 2026-06-12T13:37:19Z_
_Fixer: Claude (gsd-code-fixer)_
_Iteration: 1_
