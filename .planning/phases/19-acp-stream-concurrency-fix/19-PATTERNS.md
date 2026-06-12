# Phase 19: acp.Stream concurrency fix — Pattern Map

**Mapped:** 2026-06-12
**Files analyzed:** 3 (1 production edit, 1 new test, 1 test revert)
**Analogs found:** 3 / 3 (all exact, named in CONTEXT/RESEARCH)

## File Classification

| Target File | Role | Data Flow | Closest Analog | Match Quality |
|-------------|------|-----------|----------------|---------------|
| `internal/acp/stream.go` (MODIFY lines 191-198) | production / concurrency primitive | request-response (blocking-then-snapshot read under mutex) | itself — `internal/acp/stream.go` `close()` body (lines 166-189) and `SessionID()` (lines 200-213) | exact (same file, same lock discipline) |
| `internal/acp/regression_rel_acp_01_test.go` (NEW) | whitebox regression test | event-driven (N readers + 1 closer racing on shared `*Stream`) | `internal/acp/stream_race_test.go` (race-loop scaffolding) + `internal/acp/regression_rel_obsv_03_test.go` (Phase 18 file-header + goleak discipline) | exact |
| `internal/pool/regression_rel_pool_02_test.go` (MODIFY lines 130-152) | regression test (surgical revert) | event-driven (multi-session pool teardown) | itself — preserve lines 100-129 + 145-147 + 153-218 (`resultWg`, per-instance `sids`, `_ = p.Close()` ordering) | exact (same file is its own pattern source) |

## Pattern Assignments

### 1. `internal/acp/stream.go` — D-19-01 Result() copy-under-lock

**Analog:** `internal/acp/stream.go` itself. The fix mirrors the existing `s.mu` discipline used by `close()` and `SessionID()`.

**Current Result() body to REPLACE** (`internal/acp/stream.go:191-198`):
```go
// Result blocks until the stream is closed and then returns the FinalResult
// and any terminal error. Safe to call from any goroutine.
func (s *Stream) Result() (*FinalResult, error) {
    <-s.done
    s.mu.Lock()
    defer s.mu.Unlock()
    return s.result, s.err
}
```

**Replacement (D-19-01 exact text per CONTEXT §D-19-01 / RESEARCH §"Replacement Result() body"):**
```go
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

**Surrounding invariants to PRESERVE (do not touch):**

- Channel close-order docstring at `internal/acp/stream.go:79-86`:
```go
// sendMu serializes close() against in-flight push(). push() holds
// RLock during the send; close() takes Lock to wait for in-flight
// pushes to drain or bail before it closes s.chunks. The closed flag
// is set under Lock before s.chunks is closed so a push that wins the
// RLock race after close() landed bails out without touching the
// channel. close() closes s.done BEFORE taking Lock so any push
// already blocked on a full chunks buffer can wake via the <-s.done
// arm and release its RLock.
```

- `close()` body at `internal/acp/stream.go:166-189` — DO NOT REORDER `close(s.done)` vs. `s.mu.Lock()`:
```go
func (s *Stream) close(result *FinalResult, err error) {
    s.closeOnce.Do(func() {
        close(s.done)                  // line 172 — load-bearing FIRST
        s.sendMu.Lock()
        s.closed = true
        s.mu.Lock()                    // line 175
        if s.result == nil {
            s.result = &FinalResult{}  // defensive — newStream always allocates
        }
        if result != nil && result.StopReason != canonical.StopUnknown {
            s.result.StopReason = result.StopReason  // line 182 — the racing write
        }
        s.err = err
        s.mu.Unlock()                  // line 185
        close(s.chunks)                // line 186
        s.sendMu.Unlock()
    })
}
```

- `SessionID()` at `internal/acp/stream.go:200-213` — UNCHANGED (per CONTEXT D-19-01 "Leave SessionID() alone").

**Lock-discipline pattern to copy from `SessionID()` (lines 206-213):**
```go
func (s *Stream) SessionID() string {
    s.mu.Lock()
    defer s.mu.Unlock()
    if s.result == nil {
        return ""
    }
    return s.result.SessionID
}
```
Note the `if s.result == nil` nil-guard idiom — Result()'s post-fix body mirrors it.

---

### 2. `internal/acp/regression_rel_acp_01_test.go` — D-19-03 NEW race-loop test

**Primary analog (race-loop scaffolding):** `internal/acp/stream_race_test.go` (whitebox `package acp`, `iterations = 200`, `ready := make(chan struct{})` happens-before edge, `time.Sleep(time.Microsecond)` yield).

**Secondary analog (file-header + goleak discipline):** `internal/acp/regression_rel_obsv_03_test.go` (Phase 18 D-18-04 pattern — package-doc comment naming REQ-ID + pre-fix/post-fix paragraphs + phase tag, `defer goleak.VerifyNone(t)` per sub-test, no `t.Skip`).

**Package-wide goleak gate already exists** at `internal/acp/testmain_test.go:18` (`goleak.VerifyTestMain(m)`) — new file inherits it for free; per-test `defer goleak.VerifyNone(t)` is belt-and-braces per project norm.

#### Imports pattern (copy from `regression_rel_obsv_03_test.go:1-30`):
```go
// Package acp — regression for REL-ACP-01 (D-19-03).
//
// Pre-fix: acp.Stream.Result() returned s.result directly as a pointer.
// close() closes s.done BEFORE acquiring s.mu, so a Result() caller can
// observe s.done closed, acquire s.mu, return the pointer, release s.mu —
// all BEFORE close() reaches the StopReason write at stream.go:182. The
// caller's downstream fr.StopReason read then races close()'s in-flight
// write. `go test -race` reports the data race.
//
// Post-fix: Result() copies *s.result into a stack-local under s.mu and
// returns &copy. close()'s later mutations target s.result; the caller
// dereferences a frozen snapshot.
//
// goleak.VerifyNone confirms no closer or Result-caller goroutine leaks.
//
// Phase 19 Plan 01 — REL-ACP-01.
package acp

import (
    "runtime"
    "sync"
    "testing"

    "go.uber.org/goleak"

    "otto-gateway/internal/canonical"
)
```

#### Race-loop pattern (copy from `stream_race_test.go:29-97`):

Key idioms to mirror:
- `const iterations = ...` at top of test (stream_race_test.go uses 200; planner picks ≥ 100 per CONTEXT — RESEARCH recommends 100 with bump to 200 if RED non-deterministic).
- Fresh `*Stream` per iteration via `newStream(context.Background(), "sess-...")` OR `NewStreamForTest("sess-acp-01")` (RESEARCH recommends `NewStreamForTest` — less invasive).
- `var wg sync.WaitGroup` + `ready := make(chan struct{})` for deterministic happens-before edge.
- Closer goroutine pattern (stream_race_test.go:64-70):
```go
go func() {
    defer wg.Done()
    <-ready
    time.Sleep(time.Microsecond)   // or runtime.Gosched()
    s.close(&FinalResult{StopReason: canonical.StopEndTurn}, nil)
}()
```
- `close(ready)` then `wg.Wait()` — start signal + drain.
- **NO `t.Parallel()`** on the top-level test (Phase 18 precedent — `regression_rel_obsv_03_test.go` omits it). stream_race_test.go DOES use it; either is valid but Phase 18 idiom recommended.

#### Test helpers available (do not re-implement):

From `internal/acp/stream_testhelpers.go:21,54`:
```go
func NewStreamForTest(sessionID string) *Stream
// returns newStream(nil, sessionID); nil ctx tolerated by newStream.

func (s *Stream) CloseForTest(result *FinalResult, err error)
// delegates to s.close(result, err); idempotent via sync.Once.
```

#### Skeleton (assembled from analogs — planner adapts as needed):
```go
func TestRegression_REL_ACP_01_ResultRacesCloseStopReason(t *testing.T) {
    defer goleak.VerifyNone(t)

    const iterations = 100        // CONTEXT D-19-03: "at least 100"
    const resultCallers = 8       // RESEARCH OQ#1 recommendation

    for i := 0; i < iterations; i++ {
        s := NewStreamForTest("sess-acp-01")

        var got [resultCallers]canonical.StopReason
        var wg sync.WaitGroup
        ready := make(chan struct{})

        for j := 0; j < resultCallers; j++ {
            wg.Add(1)
            go func(idx int) {
                defer wg.Done()
                <-ready
                fr, _ := s.Result()
                if fr != nil {
                    got[idx] = fr.StopReason   // pointer deref AFTER s.mu.Unlock
                }
            }(j)
        }

        wg.Add(1)
        go func() {
            defer wg.Done()
            <-ready
            runtime.Gosched()
            s.CloseForTest(&FinalResult{StopReason: canonical.StopEndTurn}, nil)
        }()

        close(ready)
        wg.Wait()

        // Positive assertion (CONTEXT D-19-03 acceptance criterion §2).
        for j := 0; j < resultCallers; j++ {
            if got[j] != canonical.StopEndTurn {
                t.Fatalf("iter %d caller %d: StopReason = %v, want StopEndTurn",
                    i, j, got[j])
            }
        }
    }
}
```

#### Positive-assertion idiom (REQUIRED — CONTEXT §D-19-03 criterion 2):

The assertion MUST check `Result().StopReason == canonical.StopEndTurn`. An absence-of-race-only assertion does NOT meet acceptance. `canonical.StopEndTurn` is the value passed to `close()`; `canonical.StopUnknown` (zero value) is the pre-fix racy observation.

---

### 3. `internal/pool/regression_rel_pool_02_test.go` — D-19-02 surgical revert

**Analog:** the file itself. Preserve everything EXCEPT the REL-ACP-01 workaround block.

#### Block to REMOVE (`internal/pool/regression_rel_pool_02_test.go:130-152`):
```go
// Drain Chunks first, THEN call Result. The drain blocks until
// acp.Stream.close's `close(s.chunks)` runs at stream.go:186 —
// which is AFTER the StopReason write at stream.go:182 and
// after s.mu.Unlock() at stream.go:185. Once Chunks observes
// channel close, the close() body has fully executed (write
// barrier on chan-close). Calling Result after the drain
// avoids the close-vs-read race that triggered when Result
// was called with only <-s.done synchronization (s.done is
// closed BEFORE s.mu, so Result waiters can return the
// FinalResult pointer before close writes StopReason —
// poolStreamWrapper.Result then reads StopReason at pool.go:959
// while close is still mutating it; `go test -race` flags this).
// Result still fires the wrapper's releaseOnce → cancelWatch
// → close(doneCh) so the ctx-watcher goroutine spawned at
// pool.go:859 exits cleanly. Plan 17-02 / D-17-04 iter 1.
resultWg.Add(1)
go func() {
    defer resultWg.Done()
    for range stream.Chunks() {
        // drain; producer closes on stream close. The for-range
        // exit is the synchronization edge with close()'s body
        // completion — StopReason is now safely readable.
    }
    _, _ = stream.Result()
}()
```

#### Block to KEEP (replacement — post-revert form per RESEARCH §"Post-revert form"):
```go
resultWg.Add(1)
go func() {
    defer resultWg.Done()
    _, _ = stream.Result()
}()
```

**Net diff:** ~23 lines deleted (entire REL-ACP-01 comment block + `for range stream.Chunks()` loop body), 0 lines added. `resultWg.Add(1)` / `defer resultWg.Done()` / `_, _ = stream.Result()` STAY.

#### Scaffolding to PRESERVE (DO NOT TOUCH):

- **`resultWg` declaration** at lines 101-108 (with its comment block about Plan 17-02 / D-17-04 iter 1 orphan-goroutine tracking).
- **`sessions` / `sessionsMu`** at lines 109-110 + 122-124 — independent of REL-ACP-01 (Phase 20 QUAL-05 will remove these; NOT this phase).
- **Per-instance `sid` collection** inside the session loop.
- **WR-04 fix block** at lines 191-206 (per-client cancel assertion).
- **Gate-close + wait ordering** at lines 214-217:
```go
close(bc0.gate)
close(bc1.gate)
wg.Wait()
resultWg.Wait()
```

**Anti-pattern guard (Pitfall 1 from RESEARCH):** If diff > 30 lines deleted, you've gone too far. If diff touches `resultWg`, `bc0Cancels`, `WR-04`, `fake-sess-bc0`/`fake-sess-bc1`, or gate-close ordering → wrong region.

---

## Shared Patterns

### Mutex + nil-guard read idiom

**Source:** `internal/acp/stream.go:206-213` (SessionID).

**Applies to:** New Result() body in stream.go.

```go
s.mu.Lock()
defer s.mu.Unlock()
if s.result == nil {
    return /* zero value */
}
// safe to deref s.result here
```

### Whitebox test file header (Phase 18 D-18-04 pattern)

**Source:** `internal/acp/regression_rel_obsv_03_test.go:1-30`.

**Applies to:** New `regression_rel_acp_01_test.go`.

- Package-doc comment naming REQ-ID, pre-fix observable, post-fix expectation, goleak rationale, phase tag.
- `package acp` (whitebox, NOT `acp_test`) — required to call `newStream` directly or to keep access to whitebox helpers; matches Phase 18 precedent.
- Imports grouped: stdlib, blank line, third-party (`go.uber.org/goleak`), blank line, internal (`otto-gateway/internal/canonical`).

### Race-loop happens-before edge

**Source:** `internal/acp/stream_race_test.go:51-70`.

**Applies to:** New regression test.

```go
ready := make(chan struct{})
// ...spawn goroutines that all do `<-ready` first...
close(ready)    // deterministic start gun
wg.Wait()       // deterministic drain
```

### Per-test goleak gate

**Source:** `internal/acp/regression_rel_obsv_03_test.go:122` + `internal/pool/regression_rel_pool_01_test.go:57`.

**Applies to:** New regression test.

```go
defer goleak.VerifyNone(t)
```

Package-wide gate already at `internal/acp/testmain_test.go:18`. Per-test `VerifyNone` is project belt-and-braces norm.

### Yield primitive choice

**Source:** `internal/acp/stream_race_test.go:68` uses `time.Sleep(time.Microsecond)`; CONTEXT D-19-03 says "yield/Gosched scheduling" generically.

**Applies to:** New regression test closer goroutine.

Either `runtime.Gosched()` or `time.Sleep(time.Microsecond)` is acceptable. RESEARCH treats this as planner's discretion.

---

## No Analog Found

None. All three files have exact analogs (the production file IS its own analog for stream.go; stream_race_test.go + regression_rel_obsv_03_test.go are exact analogs for the new test; regression_rel_pool_02_test.go IS its own analog for the revert).

## Metadata

**Analog search scope:**
- `internal/acp/` (whitebox test precedents, stream.go itself, testhelpers, testmain)
- `internal/pool/` (revert target + race-loop precedent regression_rel_pool_01_test.go)
- `internal/canonical/` (StopReason constants)

**Files scanned (concrete excerpts extracted):**
- `internal/acp/stream.go` (lines 75-213)
- `internal/acp/stream_race_test.go` (full, 112 lines)
- `internal/acp/regression_rel_obsv_03_test.go` (lines 1-140)
- `internal/acp/stream_testhelpers.go` (full, 57 lines)
- `internal/acp/testmain_test.go` (full, 21 lines)
- `internal/pool/regression_rel_pool_02_test.go` (lines 100-218)
- `internal/pool/regression_rel_pool_01_test.go` (full, 156 lines)

**Pattern extraction date:** 2026-06-12
