---
phase: 19-acp-stream-concurrency-fix
reviewed: 2026-06-12T00:00:00Z
depth: standard
files_reviewed: 3
files_reviewed_list:
  - internal/acp/stream.go
  - internal/acp/regression_rel_acp_01_test.go
  - internal/pool/regression_rel_pool_02_test.go
findings:
  critical: 0
  warning: 0
  info: 2
  total: 2
status: clean
---

# Phase 19: Code Review Report

**Reviewed:** 2026-06-12
**Depth:** standard
**Files Reviewed:** 3
**Status:** clean (2 informational notes only — no Blocker, no Warning)

## Summary

Phase 19 closes REL-ACP-01 via D-19-01 — `Stream.Result()` now copies
`*s.result` into a stack-local under `s.mu.Lock()` and returns `&cp`,
eliminating the close-vs-Result data race on `s.result.StopReason`. The
fix is three lines in `stream.go`, paired with a race-loop regression
test (`regression_rel_acp_01_test.go`, 156 lines) and a surgical revert
of the Phase 17 drainChunks workaround in
`regression_rel_pool_02_test.go` (20 deletions).

Reviewed against the phase intent (D-19-01 minimal copy-under-lock,
D-19-02 surgical revert, D-19-03 dual-invariant race-loop regression),
and the locked invariant from CONTEXT (`close(s.done)` must be ordered
BEFORE `s.mu.Lock()` in `close()`).

**Verdict:** the implementation is correct, minimal, and matches the
locked decisions. No bugs, no security issues, no quality defects worth
flagging at the Warning tier. Two Info items below capture small
observations a future maintainer may find useful — neither blocks ship.

### What I checked (and what passed)

`internal/acp/stream.go` Result() (lines 202–211):
- Lock acquisition (`s.mu.Lock()`) properly pairs with `defer
  s.mu.Unlock()`. The deferred unlock fires after `cp := *s.result`
  evaluates, so the copy is fully made under the lock.
- The nil branch returns `(nil, s.err)` — preserves pre-fix semantics
  exactly (callers that previously got `(nil, err)` still do).
- `cp := *s.result` is a value copy of the `FinalResult` struct
  (SessionID string, ChunkCount int, StopReason canonical.StopReason —
  all value types, no embedded pointers, no slices, no maps), so the
  shallow copy is also a deep copy. No aliasing footgun.
- `return &cp, s.err` — `cp` escapes to heap (standard Go), and `s.err`
  is read under the lock so any `s.err` write from `close()` is
  synchronized. Correct.
- Signature `(*FinalResult, error)` unchanged. All existing callers
  are byte-identical, matching D-19-01.

`internal/acp/regression_rel_acp_01_test.go`:
- The race scenario is genuinely exercised: 8 Result callers gate on
  `<-ready` for a deterministic happens-before edge, then race against
  a closer that does the same. `close(ready)` is the start gun. This
  is the canonical Go race-test pattern.
- Per-goroutine writes to `got[idx]` (array, not slice — no element
  aliasing) are synchronized to the main-goroutine reads via
  `wg.Wait()` — no false-positive race on the assertion path itself.
- `runtime.Gosched()` in the closer matches CONTEXT D-19-03 ("yield/
  Gosched scheduling"); with the test accepting BOTH outcomes
  (StopUnknown OR StopEndTurn), scheduling non-determinism is
  benign — it just shifts which arm wins each trial.
- The `sawEndTurn` sanity check catches the "CloseForTest stopped
  propagating StopReason" regression class: across 100 iter × 8
  callers = 800 trials per `go test` invocation, the closer must win
  `s.mu` at least once. Statistically robust at this trial count;
  there is no realistic false-fail risk under healthy scheduling.
- Default branch of the `switch got[j]` — anything outside
  `{StopUnknown, StopEndTurn}` triggers `t.Fatalf` with a torn-snapshot
  diagnostic. Correct backstop for the D-19-01 contract.
- `goleak.VerifyNone(t)` deferred at the top of the test function —
  catches any closer or Result-caller goroutine leak. Correct
  placement.

`internal/pool/regression_rel_pool_02_test.go`:
- Diff inspection (commit 899ce7a) confirms exactly the 20-line
  removal documented in 19-01-SUMMARY.md — the 15-line "Drain Chunks
  first, THEN call Result..." comment plus the 5-line `for range
  stream.Chunks()` loop, both inside the orphan goroutine.
- `resultWg` scaffolding preserved (Phase 17 D-17-04 — independent
  goleak tracking, correctly kept).
- WR-04 per-client cancel assertion preserved (lines 171–184). Phase
  15 invariant — correctly kept.
- Gate-close / wait ordering preserved at lines 194–197: gates →
  `wg.Wait()` → `resultWg.Wait()` → deferred `goleak.VerifyNone(t)`.
  Correct.
- The orphan goroutine at line 131 now reads:
  ```go
  go func() {
      defer resultWg.Done()
      _, _ = stream.Result()
  }()
  ```
  This is the minimal form: `Result()` blocks on `<-s.done`, which
  fires when `p.Close()` cancels the in-flight session. Post-D-19-01
  the discarded `*FinalResult` is a heap copy — no aliasing with the
  pool's internal `s.result`. Clean.

### Cross-cutting correctness

- The locked invariant (`close(s.done)` BEFORE `s.mu.Lock()` in
  `close()`) is preserved at stream.go:172 — the comment block at
  lines 169–171 even reasserts the rationale. D-19-01 does NOT touch
  `close()`, matching CONTEXT.
- The `nil result` defensive branch in `close()` (lines 176–180) and
  the `nil result` branch in `Result()` (lines 206–208) agree on
  semantics: both treat nil as "return what we have / nothing,
  no crash." Consistent.
- `SessionID()` (lines 219–226) takes `s.mu.Lock()` for the
  `s.result.SessionID` read — that ordering is unchanged by Phase 19
  and remains correct.

## Info

### IN-01: Result() doc comment line reference may drift

**File:** `internal/acp/stream.go:196-201`
**Issue:** The Result() doc comment refers to "the line below the
s.mu.Lock() inside close()" as the StopReason write site. This is a
prose hint that's correct today (stream.go:182) but unanchored — if
close()'s body is reordered in a future refactor, the comment risks
referring to the wrong line without breaking the build.
**Fix:** Optional — phrase as "the StopReason merge inside close()
(see line 181-183 today)" or drop the locator entirely and let the
reader find it via the symbol. Strictly cosmetic; do NOT block ship
on this.

### IN-02: Test header refers to stream.go:182 by line number

**File:** `internal/acp/regression_rel_acp_01_test.go:4-7`
**Issue:** Same drift risk as IN-01. The header narrates "BEFORE
close() reaches the StopReason write at stream.go:182" — accurate
today, fragile across refactors.
**Fix:** Optional — phrase as "the StopReason merge inside
acp.Stream.close()" without the line locator. Pure documentation
hygiene; not a defect.

### Items intentionally NOT flagged

For traceability — patterns I considered and rejected:

- **`runtime.Gosched()` is non-deterministic on GOMAXPROCS>1.** The
  test accepts both race outcomes (StopUnknown OR StopEndTurn) by
  design, and the `sawEndTurn` sanity check ensures the closer wins
  at least once across 800 trials. Adequate statistical safety; not a
  flake risk.
- **`got [resultCallers]canonical.StopReason` zero-initialized to
  StopUnknown.** If a goroutine panics before reaching the write,
  `got[idx]` stays StopUnknown — which is an accepted value, so a
  goroutine panic could be silently swallowed by the value
  assertion. BUT: a panic in a test goroutine without `recover`
  crashes the whole test binary in Go, which is the desired
  failure mode. No latent silent-fail.
- **Test uses package `acp` (not `acp_test`).** Necessary —
  `NewStreamForTest` is exported but the test directly references
  internal-package machinery via the helper. Conventional and
  correct for in-package regression suites in this codebase.
- **The phase intentionally relaxed the test assertion vs. the
  planner's PATTERNS.md skeleton.** The deviation is documented in
  19-01-SUMMARY.md and the test header at lines 17-30 explains why
  (the locked `close(s.done)` ordering invariant makes
  `StopReason == StopEndTurn` non-deterministic). Reviewing the
  reasoning: the dual invariant ("no race + value in
  {StopUnknown, StopEndTurn}") IS what REL-ACP-01 actually
  mandates per REQUIREMENTS.md, and the negative assertion (default
  branch t.Fatalf) catches the torn-snapshot regression class. The
  relaxation is the correct call; the planner's strict assertion
  would have produced a flaky test under the locked ordering.

---

_Reviewed: 2026-06-12_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
