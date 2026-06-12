---
phase: 19-acp-stream-concurrency-fix
discussed: 2026-06-12
status: ready_for_planning
---

# Phase 19 — acp.Stream concurrency fix (CONTEXT)

## Phase Goal

Close `REL-ACP-01` — the production race in `acp.Stream.Result` surfaced
by Phase 17's `17-02-SUMMARY.md`. After this phase, the v1.10.3 milestone
(Reliability Closeout) has only Phase 20 (code-review backlog burn-down)
remaining and is one phase from done.

Single-plan phase. Three D-IDs, all in `internal/acp/stream.go` + one
test-file revert + one new regression test.

## The Race (already locked from prior phases)

Current `internal/acp/stream.go:166-198` shape:

```go
func (s *Stream) close(result *FinalResult, err error) {
    s.closeOnce.Do(func() {
        close(s.done)              // line 172 — NO LOCK YET
        s.sendMu.Lock()
        s.closed = true
        s.mu.Lock()                // line 175
        // ... writes s.result.StopReason ...
        s.mu.Unlock()              // line 185
        close(s.chunks)
        s.sendMu.Unlock()
    })
}

func (s *Stream) Result() (*FinalResult, error) {
    <-s.done                       // line 194 — unblocks BEFORE close acquires s.mu
    s.mu.Lock()
    defer s.mu.Unlock()
    return s.result, s.err         // returns POINTER under lock; caller deref AFTER unlock
}
```

**Race sequence:**

1. Goroutine A enters `close()`, executes `close(s.done)`.
2. Goroutine B is in `Result()`. `<-s.done` unblocks.
3. B acquires `s.mu` FIRST (A is still racing to `sendMu.Lock()` + `s.mu.Lock()`).
4. B returns `s.result, s.err` (pointer is stable, lock released by defer).
5. A acquires `s.mu`, writes `s.result.StopReason = stop`.
6. B's caller does `result.StopReason` — read while A is writing → `go test -race` report.

The Phase 17 test-side workaround (drain `stream.Chunks()` before calling
`Result()`) avoids the race because `close(s.chunks)` runs at stream.go:186,
AFTER the StopReason write at line 182 and AFTER `s.mu.Unlock()` at line 185.
Chan-close write barrier ensures all close-body mutations are visible before
Result returns. This is operationally correct but adds boilerplate to every
caller test and lies about the production contract.

## Locked Decisions

### D-19-01: `Result()` returns `*FinalResult` pointing to a fresh-allocated copy

Allocate a NEW `&FinalResult{...}` inside the lock, copy fields one-by-one
from `*s.result`, return the new pointer. Signature stays `(*FinalResult, error)` —
zero caller diff. Every existing `result, err := stream.Result(); _ = result.StopReason`
site keeps working byte-identical.

```go
// internal/acp/stream.go (replace lines 191-198)
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

**Why pointer-to-copy not value:** Caller-side API stays byte-identical.
`(FinalResult, error)` would force a touch of every call site under
`internal/pool/`, `internal/engine/`, `internal/adapter/ollama/ndjson.go`,
`internal/adapter/ollama/sse.go`, plus every test. Fix-phase blast radius
should stay surgical.

**Why not also fix the `close(s.done)` ordering:** That's a bigger surgery
(close-order invariants are documented at stream.go:84-86 and 168-171 for
unblocking blocked pushers via the `<-s.done` arm). Result()-side copy
solves REL-ACP-01 without disturbing that invariant. If a future audit
finds that the s.done-first ordering causes OTHER races, that's a separate
ADR. Out of scope for v1.10.3.

**Pre-existing `SessionID()` (lines 200-213):** Already takes `s.mu` and
reads from the pointer. Not affected by the race because `SessionID` is set
once at `newStream` time and never mutated after — the `if s.result == nil`
guard is the only defensive read. Leave SessionID() alone.

### D-19-02: Surgical revert of the Phase 17 test-side workaround

Only the `drainChunks(stream.Chunks())` calls in
`internal/pool/regression_rel_pool_02_test.go` that exist as the
REL-ACP-01 workaround get removed. Specifically the block documented at
lines 130-145 (comment "Drain Chunks first, THEN call Result.") in the
two-session Ctrl-C reproducer. The comment block referencing
"REL-ACP-01" / "drain Chunks first" gets removed in the same commit.

**Out of scope — leave these untouched:**

- `internal/pool/pool_test.go:481, 558` — `drainChunks` used for general
  test-harness cleanup, not REL-ACP-01 race avoidance.
- `internal/pool/regression_rel_cfg_04_test.go:158` — same.
- `internal/pool/regression_rel_pool_04_test.go:54, 111` — explicitly says
  "We do NOT call drainChunks" (the test asserts the opposite — see comment).
- `internal/engine/acp_adapter_test.go:306` — drain assertion for chunk count.
- `internal/acp/integration_test.go:371, 399, 537` — drain for documented
  D-24 step + sibling-goroutine pattern.

**Verification gate for the revert:** After the revert, run
`internal/pool/regression_rel_pool_02_test.go` 60 times under `-race`.
Must pass 60/60 with no data race report. This is the published bar from
ROADMAP.md for REL-ACP-01.

### D-19-03: Dedicated `regression_rel_acp_01_test.go` + 60-iteration race-loop

New test file at `internal/acp/regression_rel_acp_01_test.go`. The test:

1. Creates an `acp.Stream` via the package's existing `newStream` helper
   (use the same construction path the production code does).
2. Spawns N goroutines that block on `stream.Result()`.
3. Spawns one goroutine that calls `stream.close(&FinalResult{StopReason: canonical.StopEndTurn}, nil)` after a yield.
4. Asserts every `Result()` caller observes `StopReason == canonical.StopEndTurn` (NOT `canonical.StopUnknown`).
5. The whole test runs inside a loop that repeats the race scenario at
   least 100 times per `go test` invocation, plus the `go test -count=60`
   acceptance gate at CI time.

Test-file scaffolding mirrors the Phase 18 regression-test pattern:
`captureSlogDefault` helper if useful, no `t.Skip`, race-clean. The test
MUST fail on pre-fix `stream.go` (assert by stashing the fix and running
the test → expect race report or wrong StopReason). The same RED→GREEN
discipline Phase 18 used.

**Acceptance criteria for the test:**

- `go test -race -count=60 ./internal/acp/ -run REL_ACP_01` passes.
- Test contains a positive assertion that `Result().StopReason` matches the
  value passed to `close()`. Not just an absence-of-race assertion.
- Test exercises the close-vs-Result race specifically (multiple Result
  callers, single closer, yield/Gosched scheduling).

## Cross-Plan Wires

Single plan: `19-01-PLAN.md` covers all 3 D-IDs. No parallel split.

| File | Touch |
|------|-------|
| `internal/acp/stream.go` | D-19-01: Result() copy-under-lock |
| `internal/acp/regression_rel_acp_01_test.go` | D-19-03: NEW race-loop regression |
| `internal/pool/regression_rel_pool_02_test.go` | D-19-02: surgical revert of REL-ACP-01 workaround (drainChunks + comment) |

## Verification (phase close criteria)

1. `make ci` exit 0 end-to-end (v1.10.3 baseline must not regress).
2. `go test -race ./...` clean tree-wide.
3. `go test -race -count=60 ./internal/acp/ -run REL_ACP_01` passes (REL-ACP-01 acceptance bar).
4. `go test -race -count=60 ./internal/pool/ -run REL_POOL_02` passes (proves the workaround revert is safe).
5. `internal/acp/stream.go` Result() returns a copy (grep for `cp := *s.result` or equivalent).
6. `internal/pool/regression_rel_pool_02_test.go` no longer contains
   `drainChunks(stream.Chunks())` adjacent to `stream.Result()` AND no longer
   has the "REL-ACP-01" / "drain Chunks first" comment block.
7. Signature `Result() (*FinalResult, error)` unchanged — `git diff` shows
   only function-body changes on stream.go:Result().
8. No new env vars, no new public API surface outside the in-function copy.

## Risks

| Risk | Likelihood | Impact | Mitigation |
|------|------------|--------|------------|
| FinalResult gains new fields after v1.10.3 and the copy misses them | Medium | Medium — silent stale read | Use struct-value copy `cp := *s.result` (not field-by-field) so future fields auto-propagate. Tests will catch a SessionID/ChunkCount mismatch immediately. |
| Reverting drainChunks in 17-02 surfaces a DIFFERENT race | Low | Medium | 60-iteration race-loop on the reverted test as the gate. If it fails, restore the drainChunks calls and open a follow-up ADR. |
| `s.result` is nil at Result-time (close called with nil pointer paths) | Very Low | Low | Existing defensive `if s.result == nil` in close() means newStream always allocates. Add the same nil-guard in Result() so the copy never crashes. |
| The dedicated REL-ACP-01 test races inside the test harness | Low | Low | Test exercises one close + N Result callers — standard race-test pattern. goleak.VerifyNone at the test boundary. |

## Out of Scope (D-17-05 / D-18 style)

- Rethinking the `close(s.done) BEFORE s.mu` ordering at stream.go:172. Documented invariant for unblocking blocked pushers. Out of scope.
- Changing `Result()` signature to value-return `(FinalResult, error)`. Bigger blast radius, no functional gain.
- Auditing every `drainChunks → Result` pattern across the test suite. Only the Phase 17 REL-ACP-01 workaround gets reverted.
- WR-01 (deferred bounded-bufio-Reader from Phase 18 review) — separate v1.10.4 todo.
- Phase 20 (code-review backlog burn-down: QUAL-01..06) — own phase.

## Canonical References

Downstream agents MUST read these before planning or implementing:

### Race source-of-truth
- `internal/acp/stream.go:155-213` — current `close()` + `Result()` + `SessionID()` implementations
- `internal/acp/stream.go:80-86` — chunks/done channel close-order invariant docstring
- `internal/acp/stream.go:160-165` — D-07 merge semantics for close()

### Prior phase artifacts
- `.planning/phases/17-trust-gate-restoration/17-02-SUMMARY.md` — original flag of this race ("Production acp.Stream race... is OUT OF SCOPE per D-17-05. Worked around in the test by draining stream.Chunks() first")
- `.planning/phases/17-trust-gate-restoration/17-SECURITY.md` — v1.10 acp.Stream race flagged threat
- `.planning/REQUIREMENTS.md` — REL-ACP-01 entry locks fix shape (copy under s.mu) and verification bar (60 race-clean iterations)

### Workaround revert site
- `internal/pool/regression_rel_pool_02_test.go:80-145` — Phase 17 D-17-04 iter 1 workaround block

### Test-pattern analogs (mirror these in regression_rel_acp_01_test.go)
- `internal/pool/regression_rel_pool_01_test.go` — pool race-loop regression test shape
- `internal/acp/regression_rel_obsv_03_test.go` — Phase 18 D-18-04 regression test (slog captureSlogDefault + goleak.VerifyNone pattern)

## Next Steps

`/gsd-plan-phase 19` — single plan covering all 3 D-IDs. Recommended order:
D-19-01 (production fix) → D-19-03 (new regression test, RED then GREEN
relative to the fix) → D-19-02 (revert the workaround once D-19-01+D-19-03
prove the race is closed).
