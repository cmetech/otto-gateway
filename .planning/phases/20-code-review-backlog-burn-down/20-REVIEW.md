---
phase: 20-code-review-backlog-burn-down
reviewed: 2026-06-12T00:00:00Z
depth: standard
files_reviewed: 9
files_reviewed_list:
  - cmd/otto-tray/escapeApplescript_darwin_test.go
  - cmd/otto-tray/tooltip.go
  - internal/server/run_direct_test.go
  - cmd/otto-tray/uihelpers_darwin.go
  - cmd/otto-tray/uihelpers_windows.go
  - cmd/otto-tray/tray.go
  - internal/server/server.go
  - internal/pool/regression_rel_pool_02_test.go
  - internal/pool/respawn_ctx_cancel_test.go
findings:
  critical: 0
  warning: 3
  info: 5
  total: 8
status: issues_found
---

# Phase 20: Code Review Report

**Reviewed:** 2026-06-12
**Depth:** standard
**Files Reviewed:** 9
**Status:** issues_found

## Summary

Phase 20 is a refactor-only batch closing six Info-level findings (QUAL-01..06).
Reviewed against the stated contract: behavior-preserving for QUAL-02..06, and
strictly additive defense-in-depth for QUAL-01.

The QUAL-01 escape-set expansion in `escapeApplescript` is correctly
implemented and tested. The QUAL-02 `tooltipForState` extraction is clean and
build-tag-correct. The QUAL-04 `tailLines` algorithm swap preserves output
semantics. The QUAL-05/06 test cleanups look benign.

The QUAL-03 relocation of `forceCloseCh` allocation from `New()` /
`NewWithCommit()` into `RunUntilSignal` is the highest-risk change in the
batch — it introduces a non-obvious receiver-field mutation in a method that
has no explicit single-call contract, and the new test exercises the
direct-`Run` path but does not exercise the
`RunUntilSignal`-with-second-signal path that this relocation is actually
load-bearing for. There is also a latent ordering quirk: `RunUntilSignal`
mutates `s.forceCloseCh` AFTER spawning the signal-watching goroutine, but
BEFORE spawning the `Run` goroutine — safe today, fragile to anyone who
reorders these blocks. See WR-01.

No Critical findings. Three Warnings, five Info items.

## Narrative Findings (AI reviewer)

## Warnings

### WR-01: `Server.forceCloseCh` is mutated after construction with no synchronization or single-call guard

**File:** `internal/server/server.go:537`
**Issue:** `RunUntilSignal` writes to the receiver field `s.forceCloseCh`
mid-method without sync.Once, mutex, or even a doc comment declaring that
`RunUntilSignal` must not be called concurrently with itself or with
`Run(ctx)`. The Phase 20 phase context calls QUAL-03 a "pure refactor", but
relocating the allocation from the constructor into a method changes the
field's lifetime invariant from "non-nil after New() returns" to "non-nil only
after RunUntilSignal has executed line 537". Consequences:

  1. If `RunUntilSignal` is called twice on the same `*Server` (e.g., a
     supervisor that restarts the inner loop) the second call leaks the
     first `forceCloseCh` and any goroutine still parked on it.
  2. If a caller invokes `s.Run(ctx)` directly on the same instance that
     `RunUntilSignal` is currently running on, the two methods race on
     `s.forceCloseCh` with no happens-before relation other than
     goroutine spawn — `-race` would flag this.
  3. The new `internal/server/run_direct_test.go` does not cover the
     `RunUntilSignal` path at all, so the QUAL-03 invariant is asserted
     only for the constructor (which is trivially true now that the
     allocation moved out) and never end-to-end.

The field comment at server.go:182-193 documents the contract correctly, but
the contract is enforced by convention, not by code.

**Fix:** Pick one:

```go
// Option A: sync.Once-guarded allocation, callable from Run too.
type Server struct {
    // ...
    forceCloseOnce sync.Once
    forceCloseCh   chan struct{}
}

func (s *Server) ensureForceCloseCh() {
    s.forceCloseOnce.Do(func() {
        s.forceCloseCh = make(chan struct{})
    })
}

// In RunUntilSignal, replace line 537 with:
s.ensureForceCloseCh()
```

```go
// Option B: pass the channel into Run explicitly instead of stashing on the
// receiver — eliminates the cross-method shared-state pattern entirely.
func (s *Server) Run(ctx context.Context, forceClose <-chan struct{}) error { ... }
```

Option B is the cleaner design but is out of scope for a refactor-only phase.
Option A is a 6-line change and matches the existing nil-channel-select-arm
contract.

### WR-02: `TestServer_Run_DirectShutdown` time.Sleep race; missing readiness signal

**File:** `internal/server/run_direct_test.go:48`
**Issue:** The test calls `s.Run(ctx)` in a goroutine, then sleeps 50ms before
cancelling the context. On a loaded CI runner, 50ms is not enough for
`http.Server.ListenAndServe` to actually bind and enter the serve loop —
`cancel()` may fire before Run has wired its select. The test currently
passes because `Run`'s outer select on `ctx.Done()` fires regardless of
whether `ListenAndServe` has reached its blocking call, but the test
intent ("server enters serve loop, then we cancel") is not what is being
exercised. Worse, the comment at lines 44-47 explicitly claims the sleep
"gives the server a moment to bind and enter the serve loop" — readers will
believe this is what is happening. This is the classic
`time.Sleep`-as-synchronization anti-pattern.

**Fix:** Use the standard `httptest`-style readiness pattern — bind a real
listener first, hand it to the server, and signal readiness via a channel.
Or accept that the test is really only asserting "Run unblocks on ctx.Done"
and drop the sleep + the misleading comment:

```go
// Drop the sleep — Run's outer select fires on ctx.Done() regardless of
// whether ListenAndServe has reached its blocking call.
done := make(chan error, 1)
go func() { done <- s.Run(ctx) }()
cancel()
select {
case err := <-done:
    if err != nil {
        t.Fatalf("Run returned error on ctx.Done(): %v", err)
    }
case <-time.After(5 * time.Second):
    t.Fatal("Run did not return within 5s after context cancel")
}
```

### WR-03: `regression_rel_pool_02_test.go` uses `time.Sleep` as a session-readiness signal

**File:** `internal/pool/regression_rel_pool_02_test.go:134`
**Issue:** `time.Sleep(100 * time.Millisecond)` after spawning two goroutines
that each `NewSession` + `Prompt` is hoped to be "wait for both sessions to
be established." Under CI load or with `-race`, this can fire before either
session has reached the blocked `Prompt` state — the assertion that
`pool.Close()` issued Cancel to BOTH clients then becomes timing-dependent.
The test predates Phase 20 but is in the changed-file set per the config
block, and Phase 20 marks it as having a QUAL-05/06 cleanup; this latent
flake should be filed even if Phase 20 does not own the fix.

**Fix:** Replace the 100ms sleep with a positive readiness signal — both
`blockingPromptClient`s should expose a `started` channel that is closed
inside their `promptFn` so the test can `<-started` for each before
calling `p.Close()`. Alternatively, poll on the pool's `Busy()` count
until it equals 2:

```go
deadline := time.Now().Add(2 * time.Second)
for p.Stats().Busy != 2 {
    if time.Now().After(deadline) {
        t.Fatal("two prompts never reached busy state within 2s")
    }
    time.Sleep(5 * time.Millisecond)
}
```

## Info

### IN-01: `tooltipForState` duplicates the header-formatting code that lives inline in `applyState`

**File:** `cmd/otto-tray/tooltip.go:8` (and `cmd/otto-tray/tray.go:202-205`)
**Issue:** QUAL-02 extracted `tooltipForState` to share the
`"OTTO Gateway · {state} ({detail})"` format string. But `applyState` at
tray.go:202-205 still open-codes the SAME format for the `miHeader.SetTitle`
call:

```go
header := fmt.Sprintf("OTTO Gateway · %s", out.State)
if out.Detail != "" {
    header += " (" + out.Detail + ")"
}
s.miHeader.SetTitle(header)
```

This is the same string `tooltipForState(out.State, out.Detail)` produces.
The dedup opportunity QUAL-02 was supposed to capture is half-done.

**Fix:**
```go
header := tooltipForState(out.State, out.Detail)
s.miHeader.SetTitle(header)
systray.SetTooltip(header)
```

Or rename `tooltipForState` → `statusLine` since both tooltip and header now
consume it.

### IN-02: `escapeApplescript` test missing a CRLF-pair case and a "trailing-stripped-byte" edge

**File:** `cmd/otto-tray/escapeApplescript_darwin_test.go:18`
**Issue:** The table covers `\n`, `\r`, `\t` individually but not `\r\n`
(Windows-style line endings that an operator might paste into a body). It
also does not assert behavior on an input that ends with a stripped byte
(`"trailing\x00"`) or starts with one (`"\x00leading"`) — the strip path is
the new defense-in-depth behavior and deserves boundary cases. Current
coverage is sufficient to prove the function works but not to lock the
contract against accidental regressions in `len(s)` arithmetic.

**Fix:** Add cases:
```go
{name: "crlf_pair", in: "a\r\nb", want: `a\r\nb`},
{name: "strip_leading", in: "\x00a", want: "a"},
{name: "strip_trailing", in: "a\x00", want: "a"},
{name: "strip_only", in: "\x00\x01\x02", want: ""},
```

### IN-03: `for range []*blockingPromptClient{bc0, bc1}` allocates an unused 2-element slice each call

**File:** `internal/pool/regression_rel_pool_02_test.go:110`
**Issue:** `for range []*blockingPromptClient{bc0, bc1}` constructs a slice
solely to iterate twice. The loop body does not use the value. This is a
Go idiom smell — `for i := 0; i < 2; i++` (or `for range 2` on Go 1.22+,
which the project mandates per CLAUDE.md tech stack) is the clearer spelling
and avoids the heap allocation.

**Fix:**
```go
for range 2 {
    wg.Add(1)
    go func() { ... }()
}
```

### IN-04: `RunUntilSignal` close-then-check idempotency dance is one writer; the select is dead code

**File:** `internal/server/server.go:559-563`
**Issue:** The pattern

```go
select {
case <-s.forceCloseCh:
default:
    close(s.forceCloseCh)
}
```

defends against double-close, but `s.forceCloseCh` has exactly ONE writer
(this block) — the field is freshly allocated 26 lines earlier on the same
goroutine. The select can never take the `case <-s.forceCloseCh:` arm
unless `RunUntilSignal` is called twice on the same `*Server` (see WR-01).
If WR-01 is fixed by introducing `sync.Once`, this select becomes
unambiguously dead. Either remove the defensive select or document it as
guarding the WR-01 reentrancy case explicitly.

The same pattern at lines 551-555 for `s.shutdownCh` IS load-bearing —
`shutdownCh` is also closed by the `RegisterOnShutdown` callback in Run,
so double-close defense is warranted there. The asymmetry between the
two close blocks is what makes this confusing.

**Fix:** Drop the select around `close(s.forceCloseCh)` and add a short
comment:
```go
// forceCloseCh has a single writer (this goroutine) and a single allocator
// (RunUntilSignal entry), so a bare close is safe.
close(s.forceCloseCh)
```

### IN-05: `tooltip.go` lives under `cmd/otto-tray/` but the package is `main` — unit testing requires darwin/windows GOOS

**File:** `cmd/otto-tray/tooltip.go:1`
**Issue:** `//go:build darwin || windows` correctly mirrors the build tag of
the platform-specific files that previously held `tooltipForState`. But the
function is now in its own file with no companion `tooltip_test.go`. The
extraction was justified as dedup; cementing it with a small test
(`tooltip_test.go` under `//go:build darwin || windows`) would catch any
future format-string churn:

**Fix:**
```go
//go:build darwin || windows

package main

import "testing"

func TestTooltipForState(t *testing.T) {
    cases := []struct {
        state  State
        detail string
        want   string
    }{
        {StateRunning, "", "OTTO Gateway · running"},
        {StateError, "boom", "OTTO Gateway · error (boom)"},
    }
    for _, tc := range cases {
        if got := tooltipForState(tc.state, tc.detail); got != tc.want {
            t.Errorf("tooltipForState(%v, %q) = %q; want %q",
                tc.state, tc.detail, got, tc.want)
        }
    }
}
```

This also covers IN-01's dedup if `applyState` is rewritten to call
`tooltipForState`.

---

_Reviewed: 2026-06-12_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
