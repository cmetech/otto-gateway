# Phase 19: acp.Stream concurrency fix — Research

**Researched:** 2026-06-12
**Domain:** Go concurrency / `sync.Mutex` ordering / chan-close write barriers
**Confidence:** HIGH

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions

- **D-19-01** — `Result()` returns `*FinalResult` pointing to a fresh-allocated copy. Allocate a NEW `&FinalResult{...}` inside the lock, struct-value copy from `*s.result` (`cp := *s.result`), return `&cp`. Signature stays `(*FinalResult, error)` — zero caller diff. Keep the `if s.result == nil` defensive guard. Do NOT change `SessionID()` (lines 206-213). Do NOT change the `close(s.done)` ordering at stream.go:172.

- **D-19-02** — Surgical revert of the Phase 17 test-side `drainChunks` workaround in **`internal/pool/regression_rel_pool_02_test.go` ONLY**. Remove the drain-Chunks-then-Result orphan-goroutine block and its REL-ACP-01 comment block. Leave the `resultWg` scaffolding (it tracks the orphan goroutines for goleak — independent of the drain reordering). Verification gate: regression_rel_pool_02 must pass 60/60 under `-race` after the revert.

- **D-19-03** — NEW `internal/acp/regression_rel_acp_01_test.go`. Construct stream via `newStream` (whitebox package), spawn N `Result()` callers + 1 `close(&FinalResult{StopReason: canonical.StopEndTurn}, nil)` closer per iteration. Inner race-loop ≥ 100 iterations per `go test` invocation. CI gate: `go test -race -count=60 ./internal/acp/ -run REL_ACP_01`. Test MUST positively assert `Result().StopReason == canonical.StopEndTurn` (not just absence-of-race). Test MUST fail on pre-fix stream.go (RED gate before the D-19-01 edit).

### Claude's Discretion

- Exact goroutine count `N` per iteration in the new race test (CONTEXT does not pin a number; the analog at `stream_race_test.go:32` uses `iterations = 200`; the goleak / runtime cost of 60 outer counts × N goroutines × inner loop is the bound).
- Inner-loop count per test invocation (CONTEXT says "at least 100"; final count is the planner's call).
- Whether to use `t.Parallel()` (none of the existing regression files in `internal/acp/` set it on the top-level test — `cancel_test.go` and `regression_rel_obsv_03_test.go` do not; `stream_race_test.go` does. Both are valid.).
- Yield primitive: `runtime.Gosched()` vs `time.Sleep(time.Microsecond)`. `stream_race_test.go:68` uses `time.Sleep(time.Microsecond)`; CONTEXT calls out "yield/Gosched scheduling" generically.
- Whether to add `goleak.VerifyNone(t)` per sub-test (Phase 18 D-18-04 pattern at `regression_rel_obsv_03_test.go:122` does this even though `testmain_test.go:18` already runs `goleak.VerifyTestMain(m)` — belt-and-braces is the project norm).

### Deferred Ideas (OUT OF SCOPE)

- Rethinking the `close(s.done)` BEFORE `s.mu` ordering at stream.go:172. Documented load-bearing invariant for unblocking blocked pushers (stream.go:80-86, 168-171). If a future audit finds OTHER races caused by it, that's a separate ADR.
- Changing `Result()` signature to value-return `(FinalResult, error)`. Forces a touch of every call site (8 production + many tests) — bigger blast radius, no functional gain.
- Auditing every `drainChunks → Result` pattern across the test suite. Only the Phase 17 REL-ACP-01 workaround in `regression_rel_pool_02_test.go` gets reverted; six other `drainChunks` call sites stay.
- WR-01 (deferred bounded-bufio-Reader from Phase 18 review) — separate v1.10.4 todo.
- Phase 20 (QUAL-01..06 code-review backlog burn-down) — own phase.
</user_constraints>

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| REL-ACP-01 | `acp.Stream.Result` copies `*s.result` into a local value under `s.mu` instead of returning a pointer-deref that races `close(s.done)` against the StopReason write. After this fix the Phase 17 drain-Chunks-then-Result workaround can be reverted. (REQUIREMENTS.md:31) | Production race fully characterized (stream.go:166-198 + 17-02-SUMMARY threat-flag). Fix shape locked: copy-under-lock at the existing `s.mu` critical section. Verification gate: `go test -race -count=60` clean on both `./internal/acp/` (new REL_ACP_01 test) and `./internal/pool/` (reverted REL_POOL_02 test). |
</phase_requirements>

## Project Constraints (from CLAUDE.md)

- Go 1.23+ (`log/slog`, post-1.22 `net/http` routing). `cgo` disabled for the main binary; this phase touches only `internal/acp/` and `internal/pool/` test files — cgo posture not at risk.
- **Trust gate**: `make ci` (`fmt-check → vet → build → lint → test-race → arch-lint → examples → govulncheck`) is non-negotiable. The fix must clear it end-to-end, including `golangci-lint` with the project's existing config. [VERIFIED: Makefile:259, .planning/STATE.md:132]
- "AI-assisted code without these guardrails generates plausible-looking-but-wrong async code patterns." Race-detector + 60-count repetition is the load-bearing acceptance signal — do not weaken it.
- No new env vars, no public API surface change. (CONTEXT.md Verification §8.)

## Summary

Phase 19 closes REL-ACP-01, the production race in `acp.Stream.Result` that was first flagged at Phase 17 close (`17-02-SUMMARY.md` Threat Flags) and worked around test-side at the time per the D-17-05 scope guard. The race is real but benign in production (StopReason values are equivalent under any interleaving — see "Why production never crashed" below); the practical problem is that `go test -race` reliably reports it, and the workaround (drain `stream.Chunks()` before calling `Result()`) lies about the production contract and inflates every caller test.

The fix is **one ~10-line edit** to `internal/acp/stream.go:193-198` (`Result()` body), **one ~25-line removal** from `internal/pool/regression_rel_pool_02_test.go:130-154`, and **one new test file** at `internal/acp/regression_rel_acp_01_test.go`. Single-plan phase; all three D-IDs land in one PR. Execution order is RED → GREEN → REFACTOR:

1. Write the new race test against unmodified stream.go → verify it **fails** under `-race` (RED gate).
2. Apply the `Result()` copy-under-lock edit → verify the new test passes 60/60 (GREEN gate).
3. Revert the Phase 17 drain workaround → verify regression_rel_pool_02 passes 60/60 (REFACTOR gate, proves the workaround was load-bearing only for the race we just closed).

**Primary recommendation:** Use the struct-value copy form `cp := *s.result; return &cp, s.err` (not field-by-field) so future `FinalResult` fields auto-propagate; risk-table item #1 names this explicitly. Mirror Phase 18's `regression_rel_obsv_03_test.go` scaffolding for the new test (whitebox `package acp`, per-test `defer goleak.VerifyNone(t)`, no `t.Skip`, single deterministic top-level test with subtests if useful). Construct the stream via `NewStreamForTest("sess-acp-01")` — the existing exported helper at `internal/acp/stream_testhelpers.go:21` already wraps `newStream(nil, sessionID)`. Use `s.CloseForTest(&FinalResult{StopReason: canonical.StopEndTurn}, nil)` for the closer goroutine (also already exported, at `stream_testhelpers.go:54`).

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| Stream lifecycle / close-vs-Result synchronization | `internal/acp/` (Stream) | — | Stream owns `s.mu`, `s.done`, `closeOnce`. The fix lives entirely inside `Result()` body. |
| Pool-level wrapper translating `*acp.FinalResult` → `*canonical.FinalResult` | `internal/pool/` (poolStreamWrapper.Result, pool.go:1066-1085) | — | Caller of `*acp.Stream.Result`. Reads `fr.StopReason` / `fr.SessionID` / `fr.ChunkCount` AFTER `s.mu.Unlock()`. This is the read site that races today. Fix shape (return `&copy`) makes it byte-identical at this tier — no change needed. |
| Per-prompt result delivery (4 production surfaces) | `internal/adapter/{anthropic,ollama,openai}` + `internal/engine/` + `internal/session/` | — | Each calls `stream.Result()` (or `run.Stream().Result()` via the wrapper). All 8 sites read `final.StopReason` / `final.SessionID` AFTER the lock releases — same hazard as poolStreamWrapper. Fix shape immunizes all of them. |
| Test harness | `internal/acp/regression_rel_acp_01_test.go` (NEW) | `internal/acp/stream_testhelpers.go` (existing) | New test sits in the acp package so it can use the package-private `newStream` if needed; the exported `NewStreamForTest` / `CloseForTest` are preferred (less invasive, matches existing patterns). |

## Standard Stack

No new dependencies introduced. All listed packages are already in `go.mod` / vendor:

### Core
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| Go stdlib `sync` | Go 1.23+ | `Mutex`, `RWMutex`, `Once`, `WaitGroup` | Already used throughout `stream.go`. The fix uses only `s.mu.Lock()` / `defer s.mu.Unlock()` (no new sync primitives). [VERIFIED: stream.go:7] |
| Go stdlib `context` | Go 1.23+ | `context.Background()` for test ctx | Already used; new test mirrors `stream_race_test.go:104` `newStream(context.Background(), ...)`. [VERIFIED: stream_race_test.go:104] |
| `go.uber.org/goleak` | Already vendored | Goroutine-leak gate | Package-wide gate at `internal/acp/testmain_test.go:18` (`goleak.VerifyTestMain(m)`). Per-test `goleak.VerifyNone(t)` is the project norm in `regression_rel_obsv_03_test.go` (5 sub-tests) and `cancel_test.go:50`. [VERIFIED: testmain_test.go:10-19] |

### Supporting
| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `otto-gateway/internal/canonical` | Internal | `StopReason`, `StopEndTurn`, `StopUnknown` constants | Test asserts `Result().StopReason == canonical.StopEndTurn` (the positive value passed to `close()`). [VERIFIED: canonical/stop_reason.go:11-34] |
| `sync/atomic` (stdlib) | Go 1.23+ | If the test needs an atomic counter for "Result callers that saw the wrong StopReason" | Optional. `stream_race_test.go:48` uses `atomic.Value` for push-error capture; new test can use `atomic.Int32` for mismatch count, or just `t.Errorf` inline. |

### Alternatives Considered
| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| Value-return `(FinalResult, error)` from `Result()` | — | Locked out by CONTEXT — touches every call site. Bigger blast radius, no functional gain. |
| Reordering `close(s.done)` to AFTER `s.mu.Unlock()` in `close()` | — | Locked out by CONTEXT — breaks the documented invariant at stream.go:80-86 / 168-171 (push() backpressure path unblocks via `<-s.done`). |
| Field-by-field copy | `cp := FinalResult{SessionID: s.result.SessionID, ChunkCount: ..., StopReason: ...}` | Risk-table #1 flags this: future `FinalResult` fields would silently propagate stale. Struct-value copy `cp := *s.result` is the lock-in. |

**Installation:** No new packages. No `go.mod` change.

**Version verification:** N/A (no new deps).

## Package Legitimacy Audit

No external packages installed in this phase. Skipped per protocol ("Required whenever this phase installs external packages").

## Architecture Patterns

### System Architecture Diagram (the race today)

```
                    Goroutine A: close()
                    ────────────────────
                    closeOnce.Do(func() {
                        close(s.done) ─────────┐  unblocks B's <-s.done
                        s.sendMu.Lock()        │
                        s.closed = true        │
                        s.mu.Lock()            │   ◄─── races B for s.mu
                        s.result.StopReason =  │       (B wins by milliseconds)
                            stop  ◄────────────┼───────┐
                        s.err = err            │       │ WRITE here
                        s.mu.Unlock()          │       │
                        close(s.chunks)        │       │
                        s.sendMu.Unlock()      │       │
                    })                         │       │
                                               ▼       │
                    Goroutine B: Result()             │
                    ───────────────────────            │
                    <-s.done  ◄─────────────────┐     │
                    s.mu.Lock()                 │     │
                    return s.result, s.err  ────┘     │
                    (RETURNS POINTER; lock released)  │
                                                      │
                    Caller: fr.StopReason  ◄──────────┘  READ here
                    (no lock held — RACE)
```

After the D-19-01 fix:

```
                    Goroutine B: Result()
                    ───────────────────────
                    <-s.done
                    s.mu.Lock()
                    defer s.mu.Unlock()
                    if s.result == nil { return nil, s.err }
                    cp := *s.result      ◄── snapshot read under lock
                    return &cp, s.err    ◄── caller dereferences a frozen copy
                                              (A's later writes hit s.result, not cp)
```

### Pattern 1: Copy-under-lock for handoff across goroutines

**What:** When a method returns a `*T` field of the receiver, and that field is mutated under a mutex by another goroutine, the caller's downstream pointer-deref races the writer unless either (a) the caller holds the lock during the deref, or (b) the method returns a `*T` to a fresh-allocated value that has been snapshot-copied under the lock.

**When to use:** Whenever the field can be mutated AFTER `Result()` returns (here: `close()` can race `Result()` because `close(s.done)` precedes `s.mu.Lock()` in close()).

**Example:**

```go
// Source: D-19-01 (CONTEXT.md), internal/acp/stream.go:193-198 replacement.
func (s *Stream) Result() (*FinalResult, error) {
    <-s.done
    s.mu.Lock()
    defer s.mu.Unlock()
    if s.result == nil {
        return nil, s.err
    }
    // Copy under lock so the caller's pointer-deref of StopReason / ChunkCount /
    // SessionID after Unlock cannot race with close()'s in-flight writes.
    cp := *s.result
    return &cp, s.err
}
```

### Pattern 2: Race-loop regression test (Go idiom)

**What:** A `for i := 0; i < N; i++` inner loop that reconstructs the racy invariant fresh each iteration, spawns the racing goroutines, and asserts the post-condition. Combined with `go test -race -count=K` for an outer multiplier (the inner loop ensures every CI invocation exercises the race many times; the outer `-count` provides headroom).

**When to use:** Closing a `go test -race`-flagged production race. The existing analog `internal/acp/stream_race_test.go:36-97` is the closest pattern in the repo and uses `iterations = 200`.

**Example skeleton (mirrors `stream_race_test.go`):**

```go
// Source: pattern from internal/acp/stream_race_test.go:29-97 (whitebox acp pkg).
func TestRegression_REL_ACP_01_ResultRacesCloseStopReason(t *testing.T) {
    defer goleak.VerifyNone(t)

    const iterations = 100  // CONTEXT.md: "at least 100 per go test invocation"
    const resultCallers = 8

    for i := 0; i < iterations; i++ {
        s := NewStreamForTest("sess-acp-01")  // exported helper at stream_testhelpers.go:21

        var got [resultCallers]canonical.StopReason
        var wg sync.WaitGroup

        // Spawn N Result() callers blocked on <-s.done.
        ready := make(chan struct{})
        for j := 0; j < resultCallers; j++ {
            wg.Add(1)
            go func(idx int) {
                defer wg.Done()
                <-ready
                fr, _ := s.Result()
                if fr != nil {
                    got[idx] = fr.StopReason  // pointer deref AFTER s.mu.Unlock()
                }
            }(j)
        }

        // Spawn the closer.
        wg.Add(1)
        go func() {
            defer wg.Done()
            <-ready
            // Tiny yield so Result callers are parked in <-s.done before close runs.
            runtime.Gosched()
            s.CloseForTest(&FinalResult{StopReason: canonical.StopEndTurn}, nil)
        }()

        close(ready)
        wg.Wait()

        // Positive assertion (NOT mere absence-of-race) per CONTEXT D-19-03 acceptance criterion.
        for j := 0; j < resultCallers; j++ {
            if got[j] != canonical.StopEndTurn {
                t.Fatalf("iter %d caller %d: StopReason = %v, want StopEndTurn (close passed StopEndTurn but Result observed a different value — race lost the write or returned a stale snapshot)",
                    i, j, got[j])
            }
        }
    }
}
```

### Anti-Patterns to Avoid

- **Drain-Chunks-then-Result as a permanent test-side pattern.** This is the Phase 17 workaround that D-19-02 is reverting. It works by inheriting the chan-close write barrier from `close(s.chunks)` (line 186 — after the StopReason write at line 182), but it lies about the production contract (production callers don't drain Chunks before reading Result — adapters like `internal/adapter/ollama/ndjson.go:553` call `run.Stream().Result()` directly). After D-19-01 lands, the workaround is no longer load-bearing.

- **Field-by-field copy.** `cp := FinalResult{SessionID: s.result.SessionID, ...}` silently drops any field added to `FinalResult` after this PR. Risk-table item #1 explicitly calls for struct-value copy `cp := *s.result` so future fields auto-propagate.

- **`sync.RWMutex` upgrade for the Result lock.** Tempting (Result is read-heavy), but `close()` already holds `s.mu` for a 4-line critical section — no contention benefit, and a separate type means a churnier diff. Out of scope. The existing `sync.Mutex` is correct.

- **Asserting absence-of-race only (no positive `StopReason` check).** CONTEXT.md acceptance criterion §2 explicitly requires "a positive assertion that `Result().StopReason` matches the value passed to `close()`. Not just an absence-of-race assertion."

- **`t.Skip` on RED.** TDD mode is enabled. The RED state for this test is a real failure (race report or wrong StopReason value), captured in a commit message or scratch note — NOT a `t.Skip` that gets removed later. Phase 17's `regression_rel_pool_02_test.go` was permanently `t.Skip`'d during Phase 14 then unskipped in Phase 15 — but that was the v1.9 pre-fix-test pattern, since superseded. Phase 18 (the live precedent — `regression_rel_obsv_03_test.go`) does not use `t.Skip`.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Synchronize test goroutines for a shared start signal | Custom `time.Sleep` to "let goroutines spin up" | `ready := make(chan struct{}); close(ready)` pattern from `stream_race_test.go:51-56` | The `chan close` is a deterministic happens-before edge; sleeps are flaky under CI load. |
| Construct a real `*acp.Stream` from external test code | Reach into private fields | `acp.NewStreamForTest(sessionID)` (stream_testhelpers.go:21) | Already exported; mirrors `newStream` initialization including the `&FinalResult{SessionID: ...}` allocation that `Result()` reads. |
| Drive `close()` from external test code | Reach into private `s.close` directly | `s.CloseForTest(result, err)` (stream_testhelpers.go:54) | Delegates to `s.close()`; idempotent via the existing `sync.Once`. |
| Goroutine-leak check | Manual `runtime.NumGoroutine` polling | `defer goleak.VerifyNone(t)` per test + package-wide `goleak.VerifyTestMain(m)` already at `testmain_test.go:18` | Project convention; failing builds when goroutines leak. |
| Stop-reason constants | String comparison | `canonical.StopEndTurn` / `canonical.StopUnknown` (canonical/stop_reason.go:15-17) | Typed `int` enum; iota-based. Equality is direct. |

**Key insight:** This whole phase is "do not hand-roll the synchronization" — the fix is to inherit the existing `s.mu` critical section's happens-before guarantee by performing the read (struct-value copy) inside the critical section, rather than handing a pointer across the boundary and forcing the caller to either also lock or accept the race.

## Runtime State Inventory

> N/A for this phase. Phase 19 is a pure source-code change: 1 production file edit, 1 test file edit, 1 new test file. No renames, no migrations, no datastore touches, no service config, no OS-registered state, no env vars (CONTEXT.md Verification §8 explicitly forbids new env vars), no build artifacts to rebuild beyond the standard `go build`.

| Category | Items Found | Action Required |
|----------|-------------|-----------------|
| Stored data | None — no datastore touched | None |
| Live service config | None — no external service config | None |
| OS-registered state | None — no OS registration | None |
| Secrets/env vars | None — CONTEXT.md §8 forbids new env vars | None |
| Build artifacts | None — `go build` is hermetic per-invocation | None |

## Common Pitfalls

### Pitfall 1: Reverting too much in regression_rel_pool_02_test.go

**What goes wrong:** D-19-02 names "the Phase 17 drainChunks workaround." A careless edit also removes the `resultWg` WaitGroup, the per-instance unique `sid` override, or the `_ = p.Close()` ordering around the gate closes — but those are independent fixes for goleak-tracked orphan goroutines and the pre-existing sessionSlots collapse (deviation Rule 1 in 17-02-SUMMARY). They are NOT REL-ACP-01 workarounds; they stay.

**Why it happens:** The CONTEXT block says "Surgical revert" but the test file has 218 lines of interleaved scaffolding for three independent fixes (resultWg goleak, sessionSlots collapse, drain-Chunks-then-Result).

**How to avoid:** Limit the diff to exactly **lines 130-145 plus the inline comment at lines 130-144 and the `for range stream.Chunks()` block at lines 148-152**. Specifically:
- The comment block at lines 130-144 ("Drain Chunks first, THEN call Result...") gets deleted.
- The `for range stream.Chunks() {}` loop at lines 148-152 inside the orphan goroutine gets deleted.
- Line 153 (`_, _ = stream.Result()`) STAYS — that's the orphan Result() drain that `resultWg` exists to track.
- Lines 145-147 (`resultWg.Add(1)` + the `go func() { defer resultWg.Done(); ... }()` wrapper) STAY.
- Line 217 `resultWg.Wait()` STAYS.

**Warning signs:** If the diff is > 30 lines deleted from `regression_rel_pool_02_test.go`, you've gone too far. If the diff also touches `resultWg`, `bc0Cancels`, `WR-04`, `fake-sess-bc0`/`fake-sess-bc1`, or the gate-close ordering at lines 214-217, you've touched the wrong region.

### Pitfall 2: Test passes deterministically with a tiny iteration count → RED gate not actually proven

**What goes wrong:** A race-loop test with `iterations = 5` might pass 60/60 even on pre-fix stream.go if the scheduler happens to schedule close() before any Result() caller reaches the lock-acquire on every iteration. RED gate (test fails on pre-fix code) goes unproven.

**Why it happens:** Go's scheduler is fast on M1/M2; tiny race windows close quickly without enough iterations.

**How to avoid:** The CONTEXT says ≥100 inner iterations PER `go test` invocation. Match `stream_race_test.go:32`'s 200. Combined with `-count=60`, that's 12,000+ race trials per CI gate run. Before merging, **deliberately stash the D-19-01 fix and run the test 3 times** — every run must fail (race report or wrong StopReason). If even one run passes pre-fix, increase iteration count until pre-fix failure is deterministic.

**Warning signs:** Pre-fix passes silently → the race window is too narrow. Increase `resultCallers` (e.g., from 4 to 16) so more goroutines contend for `s.mu` after `<-s.done` unblocks.

### Pitfall 3: `goleak.VerifyNone(t)` fails because closer goroutine outlives the test

**What goes wrong:** Test exits with `wg.Wait()` but the closer goroutine is still inside `s.CloseForTest`'s `closeOnce.Do(...)` body. `goleak` flags it as a leaked goroutine.

**Why it happens:** `s.close()` runs `close(s.chunks)` and `s.sendMu.Unlock()` AFTER releasing `s.mu`. The closer goroutine's `defer wg.Done()` fires when the function returns — which is after the full `close()` body executes — so `wg.Wait()` covers it. But if you `wg.Done()` before calling `CloseForTest`, you'll leak the closer.

**How to avoid:** `defer wg.Done()` at the top of the closer goroutine. `CloseForTest` is the last statement. (See the skeleton above.) Verified pattern: `stream_race_test.go:55-61` uses `defer wg.Done()` at top, `s.close(...)` at end.

**Warning signs:** Intermittent `goleak: leaked goroutine` reports from the new test only under high concurrency.

### Pitfall 4: `make ci` lint failure from the new test (golangci-lint defaults)

**What goes wrong:** The new file fails `gofumpt -d` (formatting), `errcheck` (unhandled `_ = s.Result()`), or `staticcheck` (unused loop variable `j`).

**Why it happens:** golangci-lint is part of the `ci` target (`Makefile:259`); the project enforces `gofumpt` (`Makefile:48-60`).

**How to avoid:** Match existing patterns. `stream_race_test.go` and `regression_rel_obsv_03_test.go` both pass `make ci` today — mirror their idioms. Run `make fmt-check vet lint` locally before commit.

### Pitfall 5: Test is in the wrong package

**What goes wrong:** Test placed in `package acp_test` (blackbox) cannot call `newStream(...)` directly. Or placed in `package acp` (whitebox) but tries to `_test.go` imports that conflict with the package's own imports.

**Why it happens:** The acp package has BOTH whitebox (`stream_race_test.go`, `regression_rel_obsv_03_test.go`, `testmain_test.go` — all `package acp`) and blackbox (`integration_test.go` is `package acp_test`) test files in the same directory.

**How to avoid:** Use **`package acp`** (whitebox), matching `regression_rel_obsv_03_test.go:18` (`package acp` per the file header) and `stream_race_test.go:1`. This is the Phase 18 D-18-04 precedent the CONTEXT names. The exported helpers `NewStreamForTest` / `CloseForTest` work from whitebox too — they don't require blackbox.

## Code Examples

### Current Result() body — the byte-precise edit target

```go
// internal/acp/stream.go:191-198 (CURRENT)
// Result blocks until the stream is closed and then returns the FinalResult
// and any terminal error. Safe to call from any goroutine.
func (s *Stream) Result() (*FinalResult, error) {
    <-s.done
    s.mu.Lock()
    defer s.mu.Unlock()
    return s.result, s.err
}
```

### Replacement Result() body — D-19-01 fix

```go
// internal/acp/stream.go:191-204 (POST-FIX)
// Result blocks until the stream is closed and then returns a snapshot of the
// FinalResult and any terminal error. Safe to call from any goroutine.
//
// REL-ACP-01 (Phase 19 D-19-01): the returned pointer references a freshly
// allocated copy made under s.mu, NOT s.result directly. close() can mutate
// s.result fields (e.g. StopReason at the line below the s.mu.Lock() inside
// close()) AFTER Result has returned its pointer, because close() acquires
// s.mu separately. Copying under the lock means the caller's downstream
// `fr.StopReason` / `fr.SessionID` / `fr.ChunkCount` reads see a frozen
// snapshot immune to close()'s in-flight writes. Signature stays
// (*FinalResult, error) so every existing caller is byte-identical.
func (s *Stream) Result() (*FinalResult, error) {
    <-s.done
    s.mu.Lock()
    defer s.mu.Unlock()
    if s.result == nil {
        return nil, s.err
    }
    cp := *s.result
    return &cp, s.err
}
```

### NewStreamForTest / CloseForTest signatures (already exist — no code to write)

```go
// internal/acp/stream_testhelpers.go:21
func NewStreamForTest(sessionID string) *Stream
// returns newStream(nil, sessionID); nil ctx is tolerated by newStream.

// internal/acp/stream_testhelpers.go:54
func (s *Stream) CloseForTest(result *FinalResult, err error)
// delegates to s.close(result, err); idempotent via the receiver's sync.Once.
```

### newStream signature (for whitebox use if preferred over the exported helper)

```go
// internal/acp/stream.go:104
func newStream(ctx context.Context, sessionID string) *Stream {
    ch := make(chan canonical.Chunk, 64)
    s := &Stream{
        Chunks: ch,
        chunks: ch,
        ctx:    ctx,
        done:   make(chan struct{}),
        result: &FinalResult{SessionID: sessionID},
    }
    return s
}
```

Both `stream_race_test.go:37` and `stream_race_test.go:104` use `newStream(context.Background(), "sess-...")` directly. Either form is acceptable for the new test.

### Phase 18 D-18-04 scaffolding to mirror (file header conventions)

From `internal/acp/regression_rel_obsv_03_test.go:1-30`:

```go
// Package acp — regression for REL-OBSV-03 (D-18-04).
//
// Pre-fix: <one-paragraph race description>
//
// Post-fix: <one-paragraph fix description>
//
// goleak.VerifyNone confirms the goroutine exits cleanly when ...
//
// Phase 18 Plan 02 — Task 1 Part A.
package acp

import (
    "bytes"
    "context"
    "encoding/json"
    "log/slog"
    "strings"
    "testing"
    "time"

    "go.uber.org/goleak"
)
```

The new file's header should follow the same shape:
- Package-doc comment naming the requirement ID, pre-fix observable, post-fix expectation, and phase tag (`Phase 19 — REL-ACP-01 D-19-03`).
- `package acp` (whitebox).
- Import block: `context`, `runtime` (for `Gosched`), `sync`, `testing`, plus `go.uber.org/goleak` and `otto-gateway/internal/canonical`.

### The drainChunks block to remove from regression_rel_pool_02_test.go

Exact lines to delete (per Read tool output of the file):

```go
// LINES 130-144 — entire comment block:
//     // Drain Chunks first, THEN call Result. The drain blocks until
//     // acp.Stream.close's `close(s.chunks)` runs at stream.go:186 —
//     // [... 13 more lines of comment ...]
//     // pool.go:859 exits cleanly. Plan 17-02 / D-17-04 iter 1.

// LINES 148-152 — the drain body of the orphan goroutine:
//     for range stream.Chunks() {
//         // drain; producer closes on stream close. The for-range
//         // exit is the synchronization edge with close()'s body
//         // completion — StopReason is now safely readable.
//     }
```

**Post-revert form of the orphan goroutine block (lines 145-154 collapsed):**

```go
resultWg.Add(1)
go func() {
    defer resultWg.Done()
    _, _ = stream.Result()
}()
```

That's it — the entire revert is removing the comment block and the for-range loop. Net diff: ~25 lines deleted, 0 added.

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| Phase 17: drain-Chunks-then-Result test workaround | Phase 19: production-side `Result()` copy-under-lock | This phase | Production test contract no longer lies; all 8 production `stream.Result()` callers are immune to the race without per-caller changes. |
| Pre-Phase 17: orphan `_, _ = stream.Result()` goroutine with no `WaitGroup` tracking | Phase 17 D-17-04: `resultWg` tracks orphan Result-drain goroutines; `wg.Wait()` then `resultWg.Wait()` | Phase 17 (2026-06-11) | goleak no longer flakes on the REL-POOL-02 reproducer. UNCHANGED by Phase 19 — `resultWg` stays. |
| Pre-D-07: `close()` replaced `s.result` with the caller's pointer | D-07 (Phase 1.1): `close()` merges only non-zero fields onto the in-place `s.result` | Phase 1.1 | Preserves the `SessionID` from `newStream` and the `ChunkCount` accumulated by `push()`. Documented at stream.go:160-165. UNCHANGED by Phase 19. |

**Deprecated/outdated:** None. The Phase 17 workaround is being reverted in this phase but it is not "deprecated" — it was correct under the D-17-05 scope guard at the time.

## Phase Close-Loop Brief (Phase 17 / Phase 18 context)

- **Phase 17 (3 plans, completed 2026-06-11).** Trust-gate restoration. Plan 17-02 deflaked the REL-POOL-02 goleak failure (`ca258f9`) with three test-scaffolding edits: `resultWg`, per-instance unique `sids`, and drain-Chunks-then-Result. The third edit is the workaround that Phase 19 D-19-02 is reverting. The 17-02-SUMMARY Threat Flags section explicitly recommends the Phase 19 fix shape (option (b): "inside `Result`, copy `*s.result` into a local `FinalResult` value under `s.mu` and return `&local`"). [VERIFIED: 17-02-SUMMARY.md:138]

- **Phase 18 (3 plans, completed 2026-06-12).** Reliability long-tail (REL-CFG-05/06/07, REL-HTTP-06/07, REL-OBSV-02/03/04, REL-TRAY-08/09). D-18-04 is the kiro-cli-stderr → structured-slog goroutine + WR-09 truncation telemetry. `regression_rel_obsv_03_test.go` is the directly applicable scaffolding precedent for Phase 19's new test (whitebox `package acp`, per-test `defer goleak.VerifyNone(t)`, no `t.Skip`, subtest table form).

- **Phase 20 (next, not in this phase).** QUAL-01..06 code-review backlog burn-down. Specifically QUAL-05 will REMOVE dead `sessions`/`sessionsMu` vars from `regression_rel_pool_02_test.go` (lines 109-110 + 122-124). Phase 19's surgical revert MUST NOT also remove these — leave them for QUAL-05 in Phase 20. (Avoid scope creep; small diffs.)

## Assumptions Log

All claims in this research are either VERIFIED against the source file (via Read or Bash grep) or CITED to the CONTEXT.md / REQUIREMENTS.md / ROADMAP.md / 17-02-SUMMARY.md. No `[ASSUMED]` claims remain.

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| — | (none) | — | — |

## Open Questions

1. **`resultCallers` count for the new race test.**
   - What we know: CONTEXT does not pin a number. `stream_race_test.go:32` uses `iterations = 200` outer loop, 1 push + 1 close goroutine (2 racers per iteration). The new test needs ≥ 2 Result callers (the wrapper at `pool.go:1071` reads `fr.StopReason` after `s.mu.Unlock()`, so 1 caller + 1 closer already reproduces the race).
   - What's unclear: Whether 4 or 8 or 16 callers maximizes race-window detection vs. CI runtime.
   - Recommendation: Planner picks `resultCallers = 8` (mid-range; matches the racy hot-loop nature without bloating goleak set). If the RED gate fails to deterministically reproduce on pre-fix code with 8 callers, bump to 16.

2. **Inner-loop iteration count.**
   - What we know: CONTEXT says "at least 100 per `go test` invocation"; the stream_race_test.go analog uses 200.
   - What's unclear: Whether 100 or 200 is sufficient for deterministic RED at `-count=1`.
   - Recommendation: Planner picks 100; if pre-fix RED is non-deterministic at `-count=1`, bump to 200. The `-count=60` outer multiplier provides headroom regardless.

3. **Whether the planner adds a sub-test that exercises the `s.result == nil` branch.**
   - What we know: D-19-01 retains the `if s.result == nil { return nil, s.err }` guard. This is dead code in production today (`newStream` always allocates at stream.go:111). 
   - What's unclear: Is a unit test of the nil-guard branch worth the file bloat?
   - Recommendation: No. The branch is defensive (per CONTEXT close()'s "Defensive — newStream always allocates" comment at stream.go:177). Mark for QUAL-burndown if ever needed; not a Phase 19 acceptance gate.

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Go toolchain | All `make` targets | Assumed (CLAUDE.md requires Go 1.23+) | — | — |
| `golangci-lint` | `make lint` / `make ci` | Assumed (project standard, Phase 17 trust gate) | — | — |
| `gofumpt` | `make fmt` / `make fmt-check` | Optional (`Makefile:43,48-56` falls back to `gofmt`) | — | `gofmt` |
| `govulncheck` | `make ci` | Assumed (`Makefile:260`) | — | — |
| `go-arch-lint` | `make arch-lint` / `make ci` | Assumed (`Makefile:251`) | v1.15.0 per Makefile comment | — |
| `go.uber.org/goleak` | New test + existing tests | Vendored (already imported across acp pkg) | — | — |

**Missing dependencies with no fallback:** None — this phase is pure-Go editing using packages already in the project.

**Missing dependencies with fallback:** `gofumpt` falls back to `gofmt` per Makefile. Not phase-blocking.

## Validation Architecture

`workflow.nyquist_validation: true` per `.planning/config.json`. Include this section.

### Test Framework

| Property | Value |
|----------|-------|
| Framework | Go testing stdlib + `go.uber.org/goleak` + `-race` flag |
| Config file | None (Go convention) |
| Quick run command | `go test -race ./internal/acp/ -run REL_ACP_01` |
| Full suite command | `make ci` |
| Phase-specific race-loop gate | `go test -race -count=60 ./internal/acp/ -run REL_ACP_01` and `go test -race -count=60 ./internal/pool/ -run REL_POOL_02` |

### Phase Requirements → Test Map

| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|--------------|
| REL-ACP-01 (positive: Result returns StopReason that close passed) | unit/race | `go test -race -count=60 ./internal/acp/ -run REL_ACP_01` | ❌ Wave 0 — new file `internal/acp/regression_rel_acp_01_test.go` |
| REL-ACP-01 (negative: workaround revert does not re-race REL-POOL-02) | unit/race | `go test -race -count=60 ./internal/pool/ -run REL_POOL_02` | ✅ exists at `internal/pool/regression_rel_pool_02_test.go` (test gets EDITED, not created) |
| REL-ACP-01 (existing acp race coverage stays clean) | unit/race | `go test -race ./internal/acp/` | ✅ `stream_race_test.go`, `cancel_test.go`, `client_test.go`, `integration_test.go`, `regression_rel_obsv_03_test.go` |
| REL-ACP-01 (full tree race-clean) | integration | `go test -race ./...` | ✅ existing trust-gate |
| Trust gate end-to-end | integration | `make ci` | ✅ existing — Makefile:259 |

### Sampling Rate

- **Per task commit:** `go test -race ./internal/acp/ -run REL_ACP_01` (after the test file exists) + `go test -race ./internal/acp/...` + `go test -race ./internal/pool/...`
- **Per wave merge:** `go test -race -count=60 ./internal/acp/ -run REL_ACP_01` AND `go test -race -count=60 ./internal/pool/ -run REL_POOL_02` (the two race-loop gates).
- **Phase gate:** `make ci` exit 0 end-to-end. Both 60-count gates above clean. (CONTEXT Verification §1-4.)

### Wave 0 Gaps

- [ ] `internal/acp/regression_rel_acp_01_test.go` — NEW file, covers REL-ACP-01 positive assertion.
- [ ] Framework install: NONE — `goleak`, `-race`, `canonical` package all already in use.
- [ ] No new shared fixtures needed — `NewStreamForTest` / `CloseForTest` are pre-existing exported test helpers (`internal/acp/stream_testhelpers.go`).

### Validation Dimensions (for a Go concurrency fix phase)

- **Race-loop count:** ≥ 60 outer (`-count=60`) × ≥ 100 inner iterations = 6,000+ trials per CI invocation. Both `./internal/acp/` (new test) and `./internal/pool/` (reverted test) must clear this gate.
- **`t.Parallel()` choice:** No strong recommendation; both forms are used in the codebase. The race test does not need `t.Parallel()` to detect the race — the race is intra-test, not inter-test. Leaving it sequential (no `t.Parallel()`) simplifies the goleak boundary.
- **Race detector overhead:** Acceptable — `internal/acp/` is a small package; `go test -race -count=60 ./internal/acp/` should complete in seconds on M1/M2. If it exceeds ~30s, drop inner iterations from 100 → 50 (still ≥ CONTEXT minimum × 60 = 6,000 trials).
- **goleak scope:** Per-test `defer goleak.VerifyNone(t)` in the new file. Package-wide `goleak.VerifyTestMain(m)` already exists at `testmain_test.go:18` (covers regressions where Phase 19's edit accidentally leaks a goroutine in any acp test).

## Security Domain

`security_enforcement` is not explicitly disabled in config (config has no `security_enforcement` key → enabled). However, this phase is a **pure concurrency fix in `internal/acp/`** with no auth, session, input-validation, or crypto surface change. ASVS-mapped controls:

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|------------------|
| V2 Authentication | no | unchanged |
| V3 Session Management | no | ACP `SessionID` is opaque routing key; not user-auth session. Field handling is unchanged (still `SessionID` set in `newStream`, never mutated). |
| V4 Access Control | no | unchanged |
| V5 Input Validation | no | unchanged |
| V6 Cryptography | no | unchanged |

### Known Threat Patterns for Go concurrency

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| Data race on shared struct field via pointer hand-off (this phase) | Tampering (under the writer's intent — silent stale read by another goroutine) | Copy-under-lock on read; `go test -race` regression gate. |
| TOCTOU on `s.result == nil` check vs. mutation | TID | The check + copy are both inside `s.mu` (one critical section) — no TOCTOU window. |
| Deadlock from re-acquiring `s.mu` | Denial of Service | `Result()` takes `s.mu` once; never calls a method that itself takes `s.mu`. No re-acquisition. |

**Note:** The pre-fix race is benign in production because `canonical.StopReason` is an `int` (atomic on the writer side at machine-word size on all supported platforms) AND the only racing values are `StopUnknown` (zero) and a non-zero stop reason — adapters tolerate `StopUnknown` as "abrupt close" per D-02 forward-compat (17-02-SUMMARY threat flag, line 138). The fix closes the `-race` report without changing observable production behavior; the security posture is unchanged.

## Sources

### Primary (HIGH confidence) — VERIFIED via Read or Bash grep against the repository

- `internal/acp/stream.go` (entire file, 213 lines) — `Result()` body at 191-198, `close()` body at 166-189, `newStream` constructor at 104-114, `SessionID()` at 200-213, channel close-order invariant docstring at 80-86.
- `internal/acp/stream_testhelpers.go` (entire file, 57 lines) — `NewStreamForTest`, `PushForTest`, `CloseForTest` signatures and bodies.
- `internal/acp/stream_race_test.go` (entire file, 111 lines) — race-loop scaffolding precedent (whitebox, `iterations = 200`, `ready := make(chan struct{})` synchronization).
- `internal/acp/regression_rel_obsv_03_test.go` (entire file, 267 lines) — Phase 18 D-18-04 scaffolding precedent (whitebox `package acp`, `defer goleak.VerifyNone(t)`, subtest table, no `t.Skip`).
- `internal/acp/testmain_test.go` — `goleak.VerifyTestMain(m)` package-wide gate at line 18.
- `internal/pool/regression_rel_pool_02_test.go` (entire file, 218 lines) — D-19-02 revert site at lines 130-152, plus the `resultWg` / per-instance-sids scaffolding to preserve.
- `internal/pool/regression_rel_pool_01_test.go` (entire file, 155 lines) — race-loop test analog (referenced in instructions; less directly applicable than `stream_race_test.go`).
- `internal/pool/pool.go:1066-1085` — `poolStreamWrapper.Result()` (the production caller that races today).
- `internal/canonical/stop_reason.go` (lines 1-34) — `StopReason` iota; `StopUnknown` = 0, `StopEndTurn` = 1.
- `Makefile` (lines 33-37, 259-260) — `test`, `test-race`, `ci` target definitions.
- Grep audit confirming all 8 production `stream.Result()` call sites: `internal/adapter/anthropic/collect.go:189`, `internal/adapter/anthropic/sse.go:783`, `internal/adapter/ollama/ndjson.go:553`, `internal/adapter/openai/sse.go:552`, `internal/engine/acp_adapter.go:81`, `internal/engine/collect.go:186`, `internal/pool/pool.go:1071`, `internal/session/entry_acp.go:125`.
- Grep audit confirming all 7 `drainChunks` test call sites: `pool_test.go:481`, `pool_test.go:558`, `regression_rel_pool_02_test.go:148` (D-19-02 target), `regression_rel_cfg_04_test.go:158`, plus the "we do NOT call drainChunks" guard comments at `regression_rel_pool_04_test.go:54` and `:111`. Note: `engine/acp_adapter_test.go:306` and `integration_test.go:371/399/537` from CONTEXT.md's out-of-scope list do not contain literal `drainChunks` calls per grep — those are general drain patterns (`for range stream.Chunks()`) not the named helper. Either way, all are out of scope per CONTEXT.

### Secondary (MEDIUM confidence) — CITED from prior phase artifacts

- `.planning/phases/19-acp-stream-concurrency-fix/19-CONTEXT.md` — the locked decisions, source-of-truth for D-19-01/02/03.
- `.planning/REQUIREMENTS.md:31` — REL-ACP-01 entry locks fix shape ("copies `*s.result` into a local value under `s.mu`") and verification bar ("60 race-clean iterations").
- `.planning/REQUIREMENTS.md:73` — REL-ACP-01 status open, assigned to Phase 19.
- `.planning/ROADMAP.md:109` — Phase 19 high-level goal.
- `.planning/phases/17-trust-gate-restoration/17-02-SUMMARY.md:138` — threat-flag entry with the recommended fix shape (option (b): "inside `Result`, copy `*s.result` into a local `FinalResult` value under s.mu and return `&local`").
- `.planning/STATE.md:131` — explicit recommendation log entry ("Recommended fix: copy `*s.result` into a local value inside Result's `s.mu` critical section").

### Tertiary (LOW confidence) — none

No web search performed; this phase is entirely an internal-repo concurrency fix against stable Go stdlib `sync` primitives. No external API uncertainty.

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH — no new packages; everything already vendored and in use.
- Architecture: HIGH — the race is fully characterized at the line level by CONTEXT.md and `17-02-SUMMARY.md` Threat Flags; both name the same fix shape.
- Pitfalls: HIGH — surgical revert scope is enumerated line-by-line in CONTEXT; race-loop iteration count and goleak scope are direct mirrors of existing in-repo tests (`stream_race_test.go`, `regression_rel_obsv_03_test.go`).
- Validation: HIGH — `make ci` gate is already proven (Phase 17 closed it at `ca258f9`; Phase 18 kept it green).

**Research date:** 2026-06-12
**Valid until:** 2026-07-12 (stable Go internal-only refactor; no external version drift to worry about). Re-validate if `FinalResult` gains a field or if `close()` body changes in the interim.
