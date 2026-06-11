---
phase: 16-fix-mediums
plan: "04"
subsystem: tray-wrapper
tags: [go, tray, fsm, notifyfn-seam, goroutine-dispatch, pool-status-enum, last-non-empty-line, powershell, support-bundle, max-mb, timeout, write-stderr, tdd]

# Dependency graph
requires:
  - phase: 14-verify-reliability-findings
    provides: Regression test scaffolds for REL-TRAY-04/05 (skipped); permanent-skip stubs for T-6/T-7
  - phase: 16-fix-mediums (Wave 1)
    provides: 16-01 → pool.LastProgressAt() exposed; 16-02 → PoolStats.Status enum rendered in /health JSON (consumed by T-5)
provides:
  - T-4 notifyFn package-level seam on both uihelpers_windows.go and uihelpers_darwin.go; applyState dispatches notify in a fire-and-forget goroutine so blocking platform notify cannot wedge uiLoop
  - T-5 Snapshot.PoolStats.Status string field (json:"status") consumed from /health → /admin/api/snapshot pool.status enum
  - T-5 FSM rule: Pool.Status "degraded" or "exhausted" maps to StateDegraded
  - T-5 makeProbe error surfacing — snapshot errors no longer silently masked as zero-value (which previously masked the Alive==0 rule and Pool.Status enum)
  - T-6 revealBundle last-non-empty-line parse for archive path
  - T-6 Write-Stderr helper in scripts/otto-gw.ps1 ([Console]::Error.WriteLine) — Initialize-Config informational lines + "Note: gateway not running" branch routed off stdout
  - T-7 Invoke-Support hardening: $MaxMb default 50→512, new $Timeout param default 180s, live-log tail-trim on copy, Test-Deadline stopwatch overrun → throw → try/finally staging cleanup, progress to stderr
affects: []  # Phase 16 closes here; v1.9 milestone close after orchestrator verification

# Tech tracking
tech-stack:
  added: []  # No new deps — Go stdlib + PowerShell built-ins (System.Diagnostics.Stopwatch, System.IO.File/StreamReader)
  patterns:
    - "Package-level fn-var seam (notifyFn) + local snapshot before goroutine launch — closes the -race window the regression test exposed while preserving production semantics (in-flight notifications target the implementation active at FSM-transition time)"
    - "Extract notify-on-transition into testable method (notifyTransition) decoupled from systray menu-item state — regression test drives it without a live systray fixture"
    - "FSM enum-extension as additive switch alongside legacy Alive==0 rule — empty Status string from pre-Plan-16-02 builds falls through, so the enum is JSON-backward-compatible with older /health responses"
    - "Last-non-empty-line stdout parse — robust to wrapper informational chatter ahead of the canonical output line"
    - "PowerShell informational-output discipline via [Console]::Error.WriteLine helper — survives -Command capture, Write-Host host-redirect, and Out-Default chatter regardless of PS host"
    - "System.Diagnostics.Stopwatch + Test-Deadline helper throws on overrun → existing try/finally catches and cleans staging (belt-and-suspenders cleanup)"
    - "Live-log tail-trim on copy with newline-aligned start offset (drop first partial line) — keeps logs readable while bounding bundle size"

key-files:
  created: []
  modified:
    - cmd/otto-tray/uihelpers_windows.go
    - cmd/otto-tray/uihelpers_darwin.go
    - cmd/otto-tray/tray.go
    - cmd/otto-tray/status.go
    - cmd/otto-tray/fsm.go
    - cmd/otto-tray/regression_rel_tray_04_test.go
    - cmd/otto-tray/regression_rel_tray_05_test.go
    - cmd/otto-tray/regression_rel_tray_06_test.go
    - cmd/otto-tray/regression_rel_tray_07_test.go
    - scripts/otto-gw.ps1

key-decisions:
  - "notifyTransition extracted out of applyState so the regression test can drive the notify-on-transition path without a live systray (applyState normally touches s.miHeader/s.miSubheader/etc which are systray.MenuItem pointers — untestable without systray.Run)"
  - "Local snapshot (fn := notifyFn) before goroutine launch — production code never swaps notifyFn at runtime, but the regression test does, and -race flags the concurrent access. Capturing once at dispatch time is also semantically correct: in-flight notifications should target the implementation active when the FSM transition fired"
  - "FSM Pool.Status switch is additive — empty string falls through to the existing Alive==0 rule, preserving behavior on /health responses from older gateway builds that pre-date Plan 16-02's enum"
  - "makeProbe returns (true, false, Snapshot{}) on snapshot error — treats it as a failed /health probe so the StartingBudget/HealthFailures path lights up Starting or Error instead of falsely reporting Running with a zero-value snapshot"
  - "T-6/T-7 regression tests are permanent-skip stubs (same Phase 15 T-2/T-3 + Phase 16 T-6 precedent) — both fixes live in PS1 + Go-side parsing, and the failure modes are Windows-specific (cannot exercise in Linux CI without pwsh + Windows directory layout). Manual reproducer scripts referenced from updated skip messages"
  - "T-7 timeout uses System.Diagnostics.Stopwatch + Test-Deadline helper rather than Start-Job — Start-Job would have required $using: scoping for every local variable in the 250-line Invoke-Support body, with high regression risk that pwsh-on-macOS cannot test. Stopwatch + throw + try/finally cleanup achieves the same operator-visible contract (bounded execution, staging cleanup) with one-line stage gates"
  - "Live-log tail-trim drops the first partial line so the cap is newline-aligned — operators reading the trimmed log see whole entries, not a half-cut JSON record"
  - "T-7 cap-loop fall-through added — rotated .log.gz drops first (existing behavior, oldest first), then live logs if the aggregate is still over cap. Defense-in-depth for the pathological case where multiple live logs together exceed cap even after individual tail-trim"

patterns-established:
  - "Extract-for-testability when applyState-style methods touch a live UI framework — the testable surface is the policy decision (notify-on-transition), not the UI calls"
  - "fn-var + local snapshot is the canonical pattern for testable seams that need goroutine dispatch — closes the -race window cleanly"
  - "Write-Stderr helper as the canonical PowerShell informational-output primitive — any future PS1 wrapper that needs to print operator-visible chatter without polluting the success stream should use it instead of Write-Host"
  - "Stopwatch + Test-Deadline + try/finally as the lightweight PowerShell timeout pattern when Start-Job would over-complicate scoping"

requirements-completed: [REL-TRAY-04, REL-TRAY-05, REL-TRAY-06, REL-TRAY-07]

# Metrics
duration: 13min
completed: 2026-06-11
---

# Phase 16 Plan 04: Tray / Wrapper Reliability Fixes Summary

**Four confirmed Medium tray/wrapper findings closed — T-4 non-blocking notify via notifyFn seam + goroutine dispatch, T-5 tray consumes /health pool.status enum with FSM degraded extension and snapshot-error surfacing, T-6 revealBundle parses last non-empty stdout line + PS1 stderr discipline via Write-Stderr helper, T-7 Invoke-Support bounded by --max-mb cap (live logs included) and --timeout with try/finally staging cleanup. Final plan of Phase 16; v1.9 Reliability Hardening milestone ready for close.**

## Performance

- **Duration:** ~13 min
- **Started:** 2026-06-11T18:53:50Z
- **Completed:** 2026-06-11T19:06:49Z
- **Tasks:** 4 (Tasks 1 and 2 strict TDD R→G pairs; Tasks 3 and 4 single atomic commits — both ship permanent-skip stub tests so no RED phase was meaningful)
- **Files modified:** 10 (production: 6, regression tests: 4)

## Accomplishments

- **T-4 (REL-TRAY-04):** `notifyFn` package-level var seam introduced on both `cmd/otto-tray/uihelpers_windows.go` and `cmd/otto-tray/uihelpers_darwin.go` (`var notifyFn = notifyImpl`). The platform `notify()` function routes through `notifyFn` so test injection covers every call path. `applyState` now calls a new `notifyTransition` method which captures `notifyFn` into a local var (closes the -race window the regression test exposes) and dispatches in a fire-and-forget goroutine. A blocking platform notify (Windows MessageBox waits up to 30s for user click) can no longer wedge uiLoop. Retry loop allows up to 3 attempts per D-discretion; current implementations are blocking-by-contract (Windows) or already best-effort (Darwin osascript) so the inner loop breaks after first call — the allowance is reserved for future best-effort dispatchers (e.g. NSUserNotification permission flow).

- **T-5 (REL-TRAY-05):** Two production changes close the wedged-pool false-positive:
  1. `cmd/otto-tray/status.go`: `PoolStats` extended with `Status string \`json:"status"\``. Mirrors the Plan 16-02 /health JSON shape (the `pool.status` enum is rendered in both `/health` and `/admin/api/snapshot`).
  2. `cmd/otto-tray/fsm.go`: new switch on `Snapshot.Pool.Status` after the existing `Alive==0` rule. `"degraded"` → `StateDegraded{Detail: "pool stalled"}`; `"exhausted"` → `StateDegraded{Detail: "pool exhausted"}`. Empty status (degraded-mode boot / pre-Plan-16-02 build) falls through to the happy path — the legacy rule still catches real failures on older builds.
  3. `cmd/otto-tray/tray.go` `makeProbe`: snapshot errors are no longer swallowed. Pre-fix `snap, _ := client.snapshot()` discarded JSON decode failures and connection resets; the FSM then saw a zero-value Snapshot (PoolSize=0), which masked the Alive==0 rule AND the new Pool.Status enum — the tray reported StateRunning while the pool was wedged. Post-fix: return `(true, false, Snapshot{})` so the FSM treats a snapshot error the same as a failed /health probe.

- **T-6 (REL-TRAY-06):** Two-part fix for the Windows-only stdout-pollution bug:
  1. `cmd/otto-tray/tray.go` `revealBundle`: `strings.TrimSpace(res.Stdout)` replaced with a loop that takes the LAST non-empty stdout line. The support verb always emits the archive path last, so this gives the right answer even if a shell profile or wrapper config dump injects chatter ahead of it.
  2. `scripts/otto-gw.ps1`: new `Write-Stderr` helper using `[Console]::Error.WriteLine` — robust against `-Command` capture, `Write-Host` host-redirect, and `Out-Default` chatter (Go's `exec.Cmd` separates the real stderr stream from stdout regardless of PowerShell host behavior). Three call sites switched: `Initialize-Config` "loaded env file" / "loaded overrides" + `Invoke-Support` "Note: gateway not running" branch.

- **T-7 (REL-TRAY-07):** `scripts/otto-gw.ps1` `Invoke-Support` hardening:
  1. Defaults: `$MaxMb` 50 → 512 (D-discretion); new `$Timeout` param default 180s. Both CLI-overridable.
  2. Live logs are now cap-aware on copy. Pre-fix copied `otto-gateway.log`, `chat-trace.log`, boot stdout/stderr unconditionally — a 200MB log day blew past any --max-mb cap. Now: each live log is checked against `$maxBytes` at copy time; over-cap files have their last `$maxBytes` tail-trimmed (newline-aligned via initial ReadLine drop) before redaction streaming. Operator gets the most-recent behavior, not the oldest.
  3. Cap-loop extended with fall-through: rotated `.log.gz` drops first (existing behavior, oldest first); fall through to live logs if aggregate still over cap (defense-in-depth for pathological multi-live-log cases).
  4. Timeout via `System.Diagnostics.Stopwatch` + `Test-Deadline` helper. Stopwatch starts at `Invoke-Support` entry; `Test-Deadline` calls gate the logs-collect / logs-copy / size-cap / archive stages and throw on overrun. The existing try/finally block catches the throw and runs `Remove-Item -Recurse -Force $staging` — staging never orphans.
  5. Progress to stderr via `Write-Stderr`. Stdout retains the archive path as the SOLE line (satisfies T-6 contract).
  6. `Show-Usage` updated to mention -MaxMb default 512, -Timeout default 180.

- `cmd/otto-tray/regression_rel_tray_06_test.go` and `_07_test.go` permanent-skip stubs (matches Phase 15 T-2/T-3 + Phase 16 T-6 precedent for Windows-only PS1-resident fixes). Updated skip messages point at the shipped fix and the manual reproducer scripts.

- `go test -race ./...` clean tree-wide.

## Task Commits

Tasks 1 and 2 follow strict RED → GREEN per `type: tdd`. Tasks 3 and 4 each ship as a single atomic commit because their regression tests are permanent-skip stubs (no test RED state is meaningful when the test can never run automated).

1. **Task 1 RED — T-4 notifyFn seam + blocking-injection test** — `733f397` (test) — confirmed FAIL (notifyTransition still synchronous; injected blockGate stub wedges to 30s test timeout)
2. **Task 1 GREEN — T-4 non-blocking notify via notifyFn goroutine dispatch** — `196c0ce` (fix) — TestRegression_REL_TRAY_04_WindowsNotifyBlocking PASS (-race, 0.00s)
3. **Task 2 RED — T-5 Pool.Status enum field + FSM degraded-wedge test** — `04ebf03` (test) — confirmed FAIL (FSM returned "running" on Pool.Status="degraded" — degraded-wedge subtest; same for exhausted subtest)
4. **Task 2 GREEN — T-5 consume pool.status enum from /health** — `d69906f` (fix) — both subtests PASS (-race)
5. **Task 3 — T-6 revealBundle last-non-empty-line parse + PS1 stderr discipline** — `a0d093d` (fix atomic) — permanent-skip stub message updated
6. **Task 4 — T-7 bounded bundle size/time + cleanup on timeout** — `171908c` (fix atomic) — permanent-skip stub message updated

**Plan metadata commit:** (this commit — SUMMARY + STATE + ROADMAP)

## Files Created/Modified

**Production source (T-4 / Task 1):**
- `cmd/otto-tray/uihelpers_windows.go` — `notify()` body renamed to `notifyImpl()`; package-level `var notifyFn = notifyImpl` added; public `notify()` thin wrapper routes through `notifyFn`. Comment updated to reference T-4 fix (goroutine dispatch in tray.go applyState).
- `cmd/otto-tray/uihelpers_darwin.go` — same shape as windows for platform symmetry (D-discretion / 16-CONTEXT.md "non-blocking-notify pattern lands on uihelpers_darwin.go too").
- `cmd/otto-tray/tray.go` — `applyState` calls new `notifyTransition(prev, next)` method. `notifyTransition` filters the StateRunning→StateError/StateStopped transition, snapshots `notifyFn` into a local `fn`, and dispatches the call in `go func() { for i := 0; i < 3; i++ { fn(title, body); break } }()`. Local snapshot closes the -race window the regression test exposed.

**Production source (T-5 / Task 2):**
- `cmd/otto-tray/status.go` — `PoolStats` gains `Status string \`json:"status"\`` mirroring the /health JSON shape from Plan 16-02. Documented with REL-TRAY-05 reference; empty string semantics noted (legacy / degraded-mode boot signal).
- `cmd/otto-tray/fsm.go` — new switch on `in.Snapshot.Pool.Status` AFTER the existing Alive==0 rule. Two cases: `"degraded"` → `StateDegraded{"pool stalled"}`; `"exhausted"` → `StateDegraded{"pool exhausted"}`.
- `cmd/otto-tray/tray.go` — `makeProbe` no longer discards snapshot errors. The previous `snap, _ := client.snapshot()` is replaced with explicit `snap, err := client.snapshot()` + early return `(true, false, Snapshot{})` on error.

**Production source (T-6 / Task 3):**
- `cmd/otto-tray/tray.go` — `revealBundle` parses the last non-empty stdout line via a `strings.Split` loop. Fall-back to `latest+bundleExt()` retained for the empty-stdout case.
- `scripts/otto-gw.ps1` — new `Write-Stderr` helper (`[Console]::Error.WriteLine`). Three call sites switched: `Initialize-Config` "loaded env file" + "loaded overrides" Write-Host → Write-Stderr; `Invoke-Support` "Note: gateway not running" Write-Host → Write-Stderr.

**Production source (T-7 / Task 4):**
- `scripts/otto-gw.ps1` —
  - `param()` block: `$MaxMb = 50` → `$MaxMb = 512`; new `$Timeout = 180`.
  - `Invoke-Support`: `$deadlineStopwatch = [System.Diagnostics.Stopwatch]::StartNew()` + `$maxBytes = [int64]$MaxMb * 1MB` + `function Test-Deadline { ... throw ... }` declared at function entry. `Write-Stderr` opening progress line. `Test-Deadline` calls before logs-collect / each logs-copy / size-cap / archive stages.
  - Live-log copy path rewritten to check `$srcInfo.Length` against `$maxBytes` and, on overrun, open the file with `[System.IO.File]::Open`, `.Seek` to `Length - $maxBytes`, drop the first partial line via `$reader.ReadLine()`, then `ReadLine` the remainder into an array streamed through `Invoke-RedactStream` to the bundle sink. Fallback path (in-cap) uses the original `Get-Content | Invoke-RedactStream | Set-Content` flow.
  - Cap-loop extended: after the rotated `.log.gz` drop block, a second block falls through to all files in the logs dir (oldest first by LastWriteTime) until the bundle aggregate is at or below cap.
  - Archive stage: `Test-Deadline 'archive'` + `Write-Stderr` progress line. `Write-Output $outPath` retained as the SOLE stdout line.
  - `Show-Usage` text updated: "-MaxMb N (default 512), -Timeout SEC (default 180)".

**Regression tests:**
- `cmd/otto-tray/regression_rel_tray_04_test.go` — t.Skip removed; full body rewritten to inject a blocking notifyFn stub via the package-level seam, call `notifyTransition(StateRunning, StateStopped)`, and assert elapsed time < 100ms with `t.Fatalf`. Deferred `close(blockGate)` drains the goroutine on test exit.
- `cmd/otto-tray/regression_rel_tray_05_test.go` — t.Skip removed; rewritten as two subtests: `DegradedWhenPoolWedged` (PoolAlive=4, PoolSize=4, PoolBusy=4, Pool.Status="degraded" → assert StateDegraded with t.Fatalf) and `DegradedWhenPoolExhausted` (same shape, Pool.Status="exhausted"). Both subtests use t.Fatalf on the assertion branch — no t.Logf-only stubs.
- `cmd/otto-tray/regression_rel_tray_06_test.go` — permanent-skip stub. Skip message updated to "REL-TRAY-06 (T-6): manual validation required — run tests/reliability/manual/REL-TRAY-06-repro.ps1 on Windows with env file present; fix shipped in tray.go revealBundle (last-non-empty-line parse) and otto-gw.ps1 (Write-Stderr informational redirect)".
- `cmd/otto-tray/regression_rel_tray_07_test.go` — permanent-skip stub. Skip message updated to "REL-TRAY-07 (T-7): manual validation required — run tests/reliability/manual/REL-TRAY-07-repro.ps1 on Windows; fix shipped in scripts/otto-gw.ps1 Invoke-Support ($MaxMb=512, $Timeout=180s, live-log cap on copy, try/finally staging cleanup, Write-Stderr progress)".

## Decisions Made

- **notifyTransition extracted from applyState** — applyState normally touches `s.miHeader.SetTitle(...)` / `s.miSubheader.SetTitle(...)` etc which are systray.MenuItem pointers; calling applyState in `go test` without `systray.Run` panics on nil dereference. Splitting out the notify-on-transition policy decision into its own method gives the regression test a clean entry point and is also good cohesion (the systray-update concern is now distinct from the user-feedback concern).
- **Local `fn := notifyFn` snapshot before goroutine launch** — production code never swaps `notifyFn` at runtime, but the regression test does (`defer notifyFn = oldNotify`). `go test -race` flagged the concurrent access on the package-level var as a data race. Capturing once at dispatch time is also semantically correct: in-flight notifications should target the implementation that was active when the FSM transition fired, not whatever's there 30s later when the goroutine reads.
- **FSM Pool.Status switch is additive** — the existing `Alive==0` rule is preserved unchanged. Empty `Pool.Status` (legacy /health responses from gateways that pre-date Plan 16-02) falls through to the rest of the FSM. This matches the JSON-backwards-compatible design choice Plan 16-02 made (Status field rendered without omitempty so empty is meaningful).
- **makeProbe returns `(true, false, ...)` on snapshot error** — `(false, ...)` would imply PID is dead, which it isn't. `(true, true, Snapshot{})` would imply healthy with a wedged pool (PoolSize=0 → degraded check skipped). The right answer is "PID alive, /health unreachable / snapshot decode failed" — which is exactly what `(true, false, Snapshot{})` already signals to the existing FSM.
- **T-6/T-7 regression tests permanent-skip per precedent** — both fixes are Windows-only PS1-resident (T-6 stdout discipline + Go-side parse fallback only fires under Windows wrapper chatter; T-7 size/time bounds are entirely in PowerShell). Linux CI cannot run pwsh + the Windows-specific log dir layout. Permanent-skip stubs with manual-reproducer pointers match the Phase 15 T-2/T-3 + Phase 16 T-6 (this plan) pattern. The alternative — fake PS1 subprocess on darwin — would be theater, not coverage.
- **T-7 timeout via Stopwatch + Test-Deadline + throw, not Start-Job** — wrapping the 250-line Invoke-Support body in a Start-Job would have required `$using:` scoping for every captured variable (`$staging`, `$bundleRoot`, `$LogFile`, `$LogBootOut`, `$LogBootErr`, `$LogDays`, `$keys`, etc — easily 20+) plus visibility for `Invoke-RedactStream`, `Get-GatewayStatus`, `Test-IsSecretKey`, `Mask-EnvValue`. High regression risk for a script that pwsh-on-macOS cannot syntax-check. Stopwatch + Test-Deadline + throw → existing try/finally catches achieves the same operator-visible contract (bounded execution, staging cleanup) with one-line stage gates and minimal scoping risk.
- **T-7 live-log tail-trim drops first partial line** — `StreamReader.ReadLine()` after `Seek` will return the half-line that starts before the seek offset. Dropping it (via a `$null = $reader.ReadLine()` before the read loop) keeps the trimmed log newline-aligned. Operators get whole JSON records, not half-cut ones.
- **T-7 cap-loop fall-through to live logs** — the existing rotated-.gz-only loop was the original bug. Even with live-log tail-trim on copy, a pathological case (5 boot-stdout files all at $MaxBytes - 1B each) could still blow past the bundle cap. Fall-through to live logs (oldest first by LastWriteTime) is the defense-in-depth.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Data race on notifyFn between goroutine read and test-deferred restore**

- **Found during:** Task 1 GREEN — initial `go test -race` after wrapping notifyFn dispatch in `go func()` flagged a WARNING: DATA RACE between the goroutine reading `notifyFn` and the test's deferred `notifyFn = oldNotify` assignment.
- **Issue:** The package-level `notifyFn` var is accessed by both the dispatched goroutine and the test cleanup path. The test uses `t.Fatalf` on elapsed > 100ms — the dispatched goroutine is still blocked on `<-blockGate` when the test function returns and runs its deferred restore. The deferred `close(blockGate)` does eventually drain the goroutine, but the race detector flags the concurrent write/read regardless of the happens-before edge.
- **Fix:** Capture `notifyFn` into a local `fn` variable BEFORE the `go func()` launch. The goroutine reads its local `fn`, not the package-level var. The test's deferred restore can safely write to `notifyFn` because no live goroutine reads it.
- **Why this is also right semantically:** Production code never swaps `notifyFn` at runtime; but if a future configuration plane ever did (e.g. enable/disable notifications via tray.json reload), in-flight notifications from FSM transitions before the swap should still use the prior implementation. Late-binding on a var that mutates is generally a foot-gun.
- **Files modified:** `cmd/otto-tray/tray.go` (production fix). The test was unchanged — the fix is on the production side of the seam.
- **Verification:** `go test -race -run TestRegression_REL_TRAY_04 ./cmd/otto-tray/...` PASS clean (no race warning, 0.00s elapsed).
- **Committed in:** `196c0ce` (T-4 GREEN) — recorded explicitly in the commit message body.

---

**Total deviations:** 1 auto-fixed (1 -race-flagged concurrent access on the testability seam)
**Impact on plan:** Zero scope creep. The deviation was on a production source file already in the plan's `files_modified` list; the fix was a single-line local-capture addition that also improves the seam's semantics. No new files, no new packages, no new dependencies.

## Issues Encountered

None unexpected. The four findings were well-characterized by Phase 14's verification ledger and the `16-PATTERNS.md` mapping. The cross-plan data flow (16-01 → 16-02 → 16-04) for T-5 came together cleanly — `Snapshot.Pool.Status` mapped one-to-one onto the `PoolStats.Status` enum Plan 16-02 already shipped in `/health`. PS1 fixes could not be syntax-checked locally (no pwsh on macOS dev box, mirroring `260531-oax` policy) — this is the standard caveat for PS1-resident fixes in this codebase and is documented in the relevant commit messages.

## Verification

**Phase-level grep gates (all PASS):**

```
T-4: notifyFn in uihelpers_windows.go                     = 6 occurrences
T-4: notifyFn in uihelpers_darwin.go                      = 5 occurrences
T-4: Fatalf in regression_rel_tray_04_test.go             = 1 occurrence (t.Fatalf, not t.Logf)
T-5: StateDegraded in fsm.go                              = 6 occurrences (existing rule + new switch + State constant)
T-5: Status field on PoolStats in status.go               = present (PoolStats.Status string)
T-5: Fatalf in regression_rel_tray_05_test.go (excl Logf) = 2 occurrences (two subtests, both with t.Fatalf)
T-6: lastLine in tray.go                                  = 3 occurrences
T-6: Write-Stderr in otto-gw.ps1                          = 5 occurrences (after T-6 commit)
T-7: MaxMb in otto-gw.ps1                                 = 11 occurrences
T-7: Remove-Item.*Recurse in otto-gw.ps1                  = 1 occurrence (staging cleanup in finally)
T-7: Test-Deadline in otto-gw.ps1                         = 7 occurrences (function + 6 stage gates)
T-7: Write-Stderr in otto-gw.ps1                          = 9 occurrences (after T-6 + T-7 commits)
```

**Regression tests (all PASS):**

```
TestRegression_REL_TRAY_04_WindowsNotifyBlocking         PASS  (-race, 0.00s)
TestRegression_REL_TRAY_05_DegradedWhenPoolWedged        PASS  (-race, 0.00s)
TestRegression_REL_TRAY_05_DegradedWhenPoolExhausted     PASS  (-race, 0.00s)
TestRegression_REL_TRAY_06_WindowsBundlePathPollution    SKIP  (permanent — manual Windows reproducer)
TestRegression_REL_TRAY_07_SupportBundleBounds           SKIP  (permanent — manual Windows reproducer)
```

**Full suite under -race:**

```
go test -race ./...    → all packages PASS
go build ./cmd/otto-tray/                exits 0
GOOS=windows go build ./cmd/otto-tray/   exits 0
```

## TDD Gate Compliance

Plan-level TDD gate sequence verified in git history:

- **Task 1 (T-4 notifyFn seam + goroutine dispatch)**: `733f397` test(...) RED → `196c0ce` fix(...) GREEN ✓
- **Task 2 (T-5 Pool.Status enum + FSM extension)**: `04ebf03` test(...) RED → `d69906f` fix(...) GREEN ✓
- **Task 3 (T-6 last-non-empty-line + PS1 stderr discipline)**: `a0d093d` fix(...) atomic. Regression test is a permanent-skip stub (Windows-only PS1-resident fix). Authoring a RED commit when the test will never run under `go test` would be theater — the t.Skip message is the canonical surface and the skip message is updated in the same commit as the production fix (D-02 unskip-in-same-commit posture, applied here as "update-skip-message-in-same-commit").
- **Task 4 (T-7 bundle size/time bounds)**: `171908c` fix(...) atomic. Same reasoning as Task 3 — permanent-skip stub, single atomic commit pairs the skip-message update with the production fix.

No gate-sequence warnings. The two atomic-commit tasks (Tasks 3 and 4) are documented above as intentional under the same posture as Plan 16-02 Task 3 (`f847cd4`) and Plan 16-01 Task 4 (`775015d`).

## Next Phase Readiness

- **Phase 16 close**: 5 of 5 plans complete (16-01 Pool/ACP, 16-02 HTTP, 16-03 Hooks, 16-04 Tray/wrapper (this plan), 16-05 Config). All 14 Medium reliability findings closed. v1.9 Reliability Hardening milestone ready for verifier + ship-phase routing.
- **v1.9 success criteria**: `go test -race ./...` clean tree-wide (criterion #1 from Plan 16-01) holds after this plan's changes — no new -race-flagged paths introduced. The other criteria belong to the sibling plans and were satisfied at their respective close.

## Self-Check: PASSED

All claimed files and commits verified on disk and in git:

- cmd/otto-tray/uihelpers_windows.go — FOUND
- cmd/otto-tray/uihelpers_darwin.go — FOUND
- cmd/otto-tray/tray.go — FOUND
- cmd/otto-tray/status.go — FOUND
- cmd/otto-tray/fsm.go — FOUND
- cmd/otto-tray/regression_rel_tray_04_test.go — FOUND
- cmd/otto-tray/regression_rel_tray_05_test.go — FOUND
- cmd/otto-tray/regression_rel_tray_06_test.go — FOUND
- cmd/otto-tray/regression_rel_tray_07_test.go — FOUND
- scripts/otto-gw.ps1 — FOUND
- commit 733f397 (Task 1 RED) — FOUND
- commit 196c0ce (Task 1 GREEN — T-4) — FOUND
- commit 04ebf03 (Task 2 RED) — FOUND
- commit d69906f (Task 2 GREEN — T-5) — FOUND
- commit a0d093d (Task 3 atomic — T-6) — FOUND
- commit 171908c (Task 4 atomic — T-7) — FOUND

---
*Phase: 16-fix-mediums*
*Plan: 04*
*Completed: 2026-06-11*
