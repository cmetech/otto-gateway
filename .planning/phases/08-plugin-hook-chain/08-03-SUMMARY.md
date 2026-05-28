---
phase: 08-plugin-hook-chain
plan: 03
subsystem: infra
tags: [plugin, logging, slog, pii-summary, ctx, request-id-correlation]

# Dependency graph
requires:
  - phase: 08-plugin-hook-chain (slice 1)
    provides: engine.PreHook/PostHook seam, plugin.RequestIDFromContext, goleak gate pattern, arch-lint plugin + plugin_pii components
provides:
  - plugin.LoggingHook (Pre+Post) — slog-correlated request observation with ms-precision timing and optional PII redaction summary emission
  - pii.Summary + pii.RedactionCount + pii.WithSummary + pii.SummaryFromContext + pii.NewSummary (D-04 API seam; race-safe via sync.Mutex; nil-receiver-safe)
  - pii.Summary.MarshalJSON (compact object form for slog)
  - goleak gate for internal/plugin/pii subpackage (T-8-GO-LEAK enforcement)
  - sync.Map-keyed-by-request_id Pre→Post timing bridge pattern (works around engine ctx-propagation quirk OQ-1)
  - Source-level T-8-PII audit test (stripGoComments + regex; rejects raw req.Messages in slog calls)
  - .go-arch-lint.yml plugin → plugin_pii edge (load-bearing for D-04 seam)
affects: [08-04-pii-redaction-hook, 08-05-main-wiring, 08-06-health-hooks-handler]

# Tech tracking
tech-stack:
  added: []  # no new vendor deps; sync, encoding/json, log/slog, time all stdlib
  patterns:
    - "Pattern B reuse: typed-key ctx + accessor pair (summaryKey struct → WithSummary/SummaryFromContext) — mirrors slice 1's RequestIDHook ctx surface"
    - "sync.Map-keyed-by-request_id for Pre→Post state (load+delete in After prevents map growth without spawning a janitor goroutine)"
    - "Source-audit test pattern (read .go file, strip comments, regex-reject forbidden patterns) for source-level threat enforcement"
    - "Nil-receiver-safe accessor: SummaryFromContext caller can chain .Add(...) without ok-check"
    - "Logger fallback per-call (h.Logger or slog.Default()) instead of cached field — never call slog.SetDefault (T-8-LEAK-3)"

key-files:
  created:
    - internal/plugin/logging.go (LoggingHook: Pre+Post + Describe + Name + sync.Map start times + nil-Logger fallback)
    - internal/plugin/logging_test.go (9 tests + captureSlog/decodeRecords helpers + stripGoComments source-audit helper)
    - internal/plugin/pii/summary.go (Summary + RedactionCount + WithSummary + SummaryFromContext + MarshalJSON)
    - internal/plugin/pii/summary_test.go (6 tests covering Add accumulator, race-safety, ctx round-trip, absent-zero, empty-JSON, nil-Add)
    - internal/plugin/pii/testmain_test.go (goleak gate)
  modified:
    - .go-arch-lint.yml (added plugin_pii to plugin.mayDependOn so D-04 seam compiles)

key-decisions:
  - "Retained TestSummary_AddIsRaceSafe with mutex implementation — even though v1 walker is single-threaded, the lock costs ~zero and lets a future async hook (e.g., content-moderation parallel walker) reuse the API without renegotiation"
  - "Adopted sync.Map keyed by request_id for Pre→Post timing bridge instead of ctx-stamping — engine.Run does not propagate the ctx returned from PreHook (08-RESEARCH OQ-1 finding from slice 1), so a context.WithValue stash in Before would not be visible to After. request_id IS propagated by the adapter's WithRequestID stamp before engine entry, making it a safe shared key"
  - "LoadAndDelete (not just Load) in After prevents unbounded sync.Map growth — every Before-Stored entry reclaimed by its paired After call without spawning a janitor goroutine (T-8-GO-LEAK)"
  - "Emit redacted attr ONLY when SummaryFromContext returns ok=true — graceful degradation so slice 3 is fully runnable before slice 4 lands (and so the no-PII-hook deployment doesn't leak 'no PII this request' on every request)"
  - "slog.Any on Summary.Counts() map (not on Summary struct) — avoids accidental serialization of internal mu/counts fields and gives operators the compact {Email:2} form directly"
  - "kind='Pre,Post' in Describe — LoggingHook is the only Pre+Post entity in v1; the combined kind lets /health/hooks de-duplicate the row at presentation time"
  - "logLevelString probes via slog.Logger.Enabled at each canonical level — Go's slog doesn't expose the active level directly, so the probe is the supported path; falls back to 'INFO' on nil"

patterns-established:
  - "D-04 API seam consumer pattern: a Pre/Post hook can read state populated by an EARLIER Pre hook by reading from ctx with an ok-check fallback. Slice 4's PIIRedactionHook will populate; slice 3's LoggingHook consumes. Future hooks (audit log, content moderation) follow the same shape."
  - "sync.Map keyed by request_id is the supported Pre→Post state bridge — documented here so slice 4/5 don't reinvent it"
  - "Source-level threat enforcement via in-test grep: a Go-aware comment stripper (stripGoComments) prevents false positives from threat-model docstrings that legitimately mention forbidden API names. Reusable for any future source-audit test."

requirements-completed: [PLUG-04, OBSV-03]

# Metrics
duration: ~25 min
completed: 2026-05-28
---

# Phase 8 Plan 03: LoggingHook Vertical Slice Summary

**Shipped `LoggingHook` (Pre+Post) emitting `plugin.before`/`plugin.after` slog records correlated by `request_id` with ms-precision timing via a `sync.Map`-keyed-by-request-id Pre→Post bridge, plus the load-bearing `pii.SummaryFromContext` API seam (D-04 contract) so slice 4's `PIIRedactionHook` has a concrete consumer to target — `redacted={Email:2, SSN:1}` is shipped today, graceful-degraded to omitted when no Summary is on ctx.**

## Performance

- **Duration:** ~25 min (single executor pass, no checkpoints)
- **Started:** 2026-05-28T00:59Z (approx; after orchestrator spawn)
- **Completed:** 2026-05-28T01:24:49Z
- **Tasks:** 4 (2 Wave-0 RED scaffolds + 2 GREEN implementations)
- **Files created:** 5 (2 source, 3 test)
- **Files modified:** 1 (.go-arch-lint.yml)
- **Commits:** 4 atomic task commits

## Accomplishments

- **`LoggingHook` (Pre+Post) ships** — the canonical-layer observation seam for every request that survives auth. `plugin.before` carries `request_id, model, message_count`; `plugin.after` carries `request_id, duration_ms, stop_reason` and the optional `redacted={Email:2, SSN:1}` attr.
- **D-04 API seam (`pii.SummaryFromContext`) ships and is consumed end-to-end in this slice** — `TestLoggingHook_EmitsRedactedSummary_WhenPresent` proves the round-trip from `pii.NewSummary().Add(...)` → `WithSummary(ctx, s)` → LoggingHook's `After` emitting the `redacted` slog attr. Slice 4's `PIIRedactionHook` just plugs into the populator side.
- **`sync.Map`-keyed-by-`request_id` Pre→Post timing bridge** documented and tested. Works around the engine ctx-propagation quirk (08-RESEARCH OQ-1: PreHook's returned ctx is not threaded to subsequent hooks); `LoadAndDelete` in After prevents unbounded growth without spawning a janitor goroutine (T-8-GO-LEAK).
- **T-8-PII source-level guard active** — `TestLoggingHook_SourceAudit_NoRawContent` reads `logging.go`, strips Go comments, and regex-rejects any `slog.Any("messages", req...)`/`slog.Any("request", req)`/raw `.Content` slog argument. Regression-proofs the "raw PII leaks to slog" failure mode at the source level.
- **`pii.Summary` is race-safe AND nil-receiver-safe** — sync.Mutex-guarded Add (so a future async hook can call concurrently); nil receiver Add is a no-op so LoggingHook can chain `s.Add(...)` without ok-checking when both PII and Logging are present.
- **goleak gate established for `internal/plugin/pii`** — mirrors slice 1's `internal/plugin/testmain_test.go`; T-8-GO-LEAK enforced for the pii subpackage from day one.

## Task Commits

Each task was committed atomically:

1. **Task 1: Wave 0 pii scaffold (6 failing tests + goleak gate)** — `9138609` (test)
2. **Task 2: pii.Summary implementation (D-04 API seam)** — `4aa6a1b` (feat)
3. **Task 3: Wave 0 LoggingHook scaffold (9 failing tests)** — `335351c` (test)
4. **Task 4: LoggingHook implementation + arch-lint edge** — `077018a` (feat)

## Files Created/Modified

### Created

- `internal/plugin/logging.go` — `LoggingHook` Pre+Post hook with sync.Map timing bridge, nil-Logger fallback (`slog.Default()` per call, never `SetDefault`), `pii.SummaryFromContext` consumer, and compile-time `engine.PreHook + engine.PostHook` assertions. Includes `logLevelString` probe via `slog.Logger.Enabled`.
- `internal/plugin/logging_test.go` — 9 LoggingHook tests + `captureSlog`/`decodeRecords` helpers + `stripGoComments` Go-aware comment stripper (defensive for the source-audit test).
- `internal/plugin/pii/summary.go` — `Summary` struct (sync.Mutex + counts map), `NewSummary`, `Add` (nil-safe + race-safe), `Counts` (snapshot returner), `MarshalJSON` (compact + nil-safe), `RedactionCount` exported wire row, `summaryKey` unexported struct, `WithSummary`/`SummaryFromContext`.
- `internal/plugin/pii/summary_test.go` — 6 Summary tests: Add accumulator, 100-goroutine race-safety property, ctx round-trip pointer identity, absent-returns-zero, empty-Counts JSON shape ("{}"), nil-receiver Add no-op.
- `internal/plugin/pii/testmain_test.go` — `goleak.VerifyTestMain` gate for the pii subpackage.

### Modified

- `.go-arch-lint.yml` — Added `plugin_pii` to `plugin.mayDependOn` with a doc comment explaining the D-04 seam direction. Slice 1 declared `plugin → {canonical, engine}` only; D-04 explicitly designs LoggingHook (in `plugin/`) to import `pii.SummaryFromContext`, requiring the new edge. No cycle today (slice 3's `summary.go` is stdlib-only); slice 4 may add the reverse direction for redaction-event logging — D-04 explicitly contemplates both directions.

## Decisions Made

1. **Retained `TestSummary_AddIsRaceSafe`** — even though v1 PII walker is single-threaded, the mutex is overhead-only and locking the safety contract NOW means future parallel hooks (e.g., content-moderation walker over multiple tool outputs) don't have to renegotiate the API.
2. **`sync.Map` keyed by `request_id` for Pre→Post timing** — `context.WithValue` in `Before` would not reach `After` because `engine.Run` does not propagate the PreHook's returned ctx (08-RESEARCH OQ-1 finding documented in slice 1). `request_id` is propagated by the adapter's `WithRequestID(ctx, id)` stamp BEFORE engine entry, making it the correct shared key. `LoadAndDelete` in `After` provides leak protection without a janitor goroutine.
3. **Emit `redacted` only when `SummaryFromContext` returns `ok=true`** — graceful degradation pre-slice-4; no false "empty redacted" attr on every request in deployments without PII hook (the latter would itself be a mild info-disclosure per T-8-PII-2, though T-8-PII-2 was already accepted in the threat register).
4. **`slog.Any("redacted", s.Counts())` not `slog.Any("redacted", s)`** — passing the Counts map (a `map[string]int`) to slog gives the operator the compact `{Email:2,SSN:1}` form directly and avoids any accidental serialization of `Summary`'s internal `mu`/`counts` fields. `MarshalJSON` exists on `*Summary` for callers who want the full nil-safe path, but slog default encoding doesn't call it for raw struct args.
5. **`kind="Pre,Post"` in Describe** — LoggingHook is the only Pre+Post entity in v1. Combined kind lets `/health/hooks` (slice 6) de-duplicate the row at presentation time. Slice 1's `Chain.Describe` currently emits one row per slice membership; planner discretion at slice 6 to either de-dup based on this kind value or accept two rows for diagnostic clarity.
6. **`logLevelString` probes via `slog.Logger.Enabled`** — Go's slog doesn't expose the active level directly. Probing each canonical level (DEBUG → INFO → WARN → ERROR) is the supported path; falls back to "INFO" on nil. Cheap (O(1) each).
7. **Logger fallback per-call, never cached** — `h.logger()` returns `h.Logger` if set else `slog.Default()`. Per-call so changes to `slog.Default` at boot (e.g., in main.go before chain construction) are seen. **Never `slog.SetDefault`** in LoggingHook itself (T-8-LEAK-3).

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Added `plugin_pii` to `plugin.mayDependOn` in `.go-arch-lint.yml`**

- **Found during:** Task 4 (LoggingHook implementation)
- **Issue:** Slice 1's `.go-arch-lint.yml` declared `plugin.mayDependOn: [canonical, engine]` and `plugin_pii.mayDependOn: [canonical, plugin]` — a one-direction edge. But D-04 explicitly designs LoggingHook (in `plugin/`) to read `pii.SummaryFromContext` from the `plugin/pii` subpackage; the plan's Task 4 imports `pii` from `logging.go` per `<read_first>` / `<action>` instructions. Running `make arch-lint` after Task 4 surfaced `Component plugin shouldn't depend on otto-gateway/internal/plugin/pii in /…/internal/plugin/logging.go:64`.
- **Fix:** Added `plugin_pii` to `plugin.mayDependOn` with a doc comment explaining D-04's bidirectional intent. Slice 3's `summary.go` is stdlib-only (no `plugin` import), so no cycle today. Slice 4 may import `plugin` (for `RequestIDFromContext` to tag redaction events) — D-04's chain-order narrative explicitly anticipates both directions of the seam.
- **Files modified:** `.go-arch-lint.yml`
- **Verification:** `make arch-lint` returns "OK - No warnings found"; `go build ./...` clean; `go test ./...` green across all packages.
- **Committed in:** `077018a` (Task 4 commit)

**2. [Rule 1 - Bug] Source-audit test false-positive on threat-model docstring**

- **Found during:** Task 4 (first run of LoggingHook tests after implementation)
- **Issue:** `TestLoggingHook_SourceAudit_NoRawContent` (from Task 3 scaffold) ran a substring `bytes.Contains(src, []byte("slog.SetDefault"))` check. The Task 4 implementation's threat-model package docstring legitimately mentions `slog.SetDefault` to declare "T-8-LEAK-3 (slog.SetDefault leaks Logger across handlers): never called" — the substring match triggered a false failure.
- **Fix:** Added a `stripGoComments` helper to the test file that removes Go line comments (`//…\n`) and block comments (`/* … */`) before applying both the regex audits and the SetDefault substring check. The helper preserves whitespace and newlines so byte offsets in error messages remain meaningful. The test is now bulletproof against threat-model docstrings while still catching real code-level violations.
- **Files modified:** `internal/plugin/logging_test.go`
- **Verification:** All 9 LoggingHook tests pass; `stripGoComments` is unit-tested implicitly via the audit test running against `logging.go`'s real (docstring-bearing) source.
- **Committed in:** `077018a` (Task 4 commit; fix landed in the same commit as the implementation so the test suite is self-consistent at the GREEN gate)

---

**Total deviations:** 2 auto-fixed (1 Rule 3 blocking — required for plan completion; 1 Rule 1 bug — defensive test improvement to prevent recurrence)
**Impact on plan:** Both deviations were structurally required to ship the slice as designed. No scope creep. The arch-lint edge is necessary for D-04's seam to compile; the test fix prevents the source-audit pattern from being brittle against future threat-model docstrings. Neither changed planned deliverables.

## Issues Encountered

- **`go vet ./...` shows pre-existing warnings in `internal/admin/tail_test.go`** — `testing.Context requires go1.24 or later (module is go1.23)`. Unrelated to slice 3; logged but not fixed per scope-boundary rule (would require a Go toolchain decision).

## Output Spec — Plan Output Requirements

Per `<output>` block in 08-03-PLAN.md:

- **Race-safety strategy in LoggingHook:** `sync.Map` keyed by `request_id`. Alternative considered: ctx-stamped start time via `context.WithValue(ctx, loggingStartKey{}, time.Now())`. **Rejected** because `engine.Run` does NOT propagate the ctx returned from a PreHook to subsequent hooks (08-RESEARCH OQ-1 finding inherited from slice 1) — `After` would receive the engine's pre-Pre ctx, not the ctx returned by `Before`. The `sync.Map` key (`request_id`) IS propagated independently via the adapter's `WithRequestID` stamp BEFORE engine entry, so the bridge is correlation-safe. `LoadAndDelete` in `After` prevents map growth without a janitor goroutine (T-8-GO-LEAK safe).
- **TestSummary_AddIsRaceSafe retention:** **Retained.** Rationale: even though v1 PII walker is single-threaded by contract, the mutex costs ~zero in the single-threaded path and locking the safety contract NOW means a future async hook (parallel content-moderation walker, audit-log async writer) does not need to renegotiate the Summary API to gain concurrency. The test runs under `-race` so the detector catches any unlocked map writes.
- **Exact slog attribute layout shipped (load-bearing for slice 5 e2e):**

  `plugin.before` record:
  ```
  msg            : "plugin.before"
  level          : INFO
  request_id     : string (ULID; empty when adapter hasn't stamped)
  model          : string (req.Model; empty for "auto")
  message_count  : int (len(req.Messages))
  ```

  `plugin.after` record:
  ```
  msg            : "plugin.after"
  level          : INFO
  request_id     : string (ULID; must match the matching plugin.before)
  duration_ms    : int64 (time.Since(Before-stamp).Milliseconds(); 0 if no Before paired)
  stop_reason    : canonical.StopReason int   (only present when resp != nil)
  redacted       : map[string]int             (only present when pii.SummaryFromContext ok=true)
  ```

- **`canonical.StopReason` rendering assumption:** `StopReason` is a `type StopReason int` (iota-positioned per `internal/canonical/stop_reason.go`). Shipped as `slog.Any("stop_reason", resp.StopReason)` — slog's default encoder produces the **integer form** (e.g., `1` for `StopEndTurn`, `0` for `StopUnknown`) via reflection. Slice 5's e2e tests should assert on the integer form, NOT a string name. If a future slice wants string names on the wire, the safest path is adding `func (s StopReason) String() string` to `canonical/stop_reason.go` and switching the slog attr to `slog.String("stop_reason", resp.StopReason.String())` — but that's a slice 5+ decision, not a slice 3 commitment.

## Threat Flags

No new security-relevant surface introduced beyond the planned ones in `<threat_model>`. The new edge `plugin → plugin_pii` in `.go-arch-lint.yml` is documented and aligns with D-04's design — not a new trust boundary, only a new compile-time dependency arrow.

## Self-Check

Verifying claimed artifacts and commits.

### Files exist on disk

```
[ -f internal/plugin/logging.go ]                  → FOUND
[ -f internal/plugin/logging_test.go ]             → FOUND
[ -f internal/plugin/pii/summary.go ]              → FOUND
[ -f internal/plugin/pii/summary_test.go ]         → FOUND
[ -f internal/plugin/pii/testmain_test.go ]        → FOUND
[ -f .go-arch-lint.yml ]                           → FOUND (modified)
```

### Commits exist in git log

```
git log --oneline | grep 9138609  → test(08-03): scaffold Wave 0 for plugin/pii/summary + goleak gate
git log --oneline | grep 4aa6a1b  → feat(08-03): add pii.Summary ctx seam (D-04 API contract)
git log --oneline | grep 335351c  → test(08-03): scaffold Wave 0 for LoggingHook
git log --oneline | grep 077018a  → feat(08-03): implement LoggingHook with slog correlation + PII summary emission
```

### Plan-level verification

- `go test ./internal/plugin/... -count=1 -race` → exit 0 (9 LoggingHook + 6 Summary tests pass; pre-existing slice 1 tests pass)
- `go test ./internal/plugin/pii/... -count=1 -race` → exit 0 (Summary tests + goleak gate clean)
- `go test ./... -count=1 -race` → all packages green (no regression elsewhere)
- `go build ./...` → exit 0
- `make arch-lint` → exit 0 (`OK - No warnings found`)
- `gosec` → not installed on dev host; deferred to CI gate
- Required exports verified via `go doc ./internal/plugin/pii`: `Summary`, `RedactionCount`, `NewSummary`, `WithSummary`, `SummaryFromContext` — all present
- Required exports verified via `go doc ./internal/plugin`: `LoggingHook` plus slice 1's exports — all present

## Self-Check: PASSED

All 5 created files exist on disk; all 4 task commits present in git log; all plan-level verification commands exit clean (modulo gosec not installed locally).

## Next Phase Readiness

- **Slice 3 complete.** `LoggingHook` + `pii.Summary` ctx surface are stable seams ready for slice 4 to plug into.
- **Ready for 08-04 (PIIRedactionHook + pii subpackage expansion).** Slice 4 will:
  1. Add `internal/plugin/pii/pii.go` (the `PIIRedactionHook` itself), `recognizers.go`, `walk.go`, `luhn.go`.
  2. Construct `pii.NewSummary()` per request in `PIIRedactionHook.Before`, mutate `req.Messages[*].Content` in place, call `WithSummary(ctx, s)` to stamp the ctx, and `s.Add("Email")` etc. per recognizer hit.
  3. Caveat: `WithSummary` returns a child ctx but `engine.Run` does NOT propagate it to subsequent Pre hooks (same OQ-1 finding). Slice 4 will need the same `sync.Map`-keyed-by-`request_id` bridge OR an equivalent — but since `Summary` is a `*Summary` (pointer), the cleanest path is for the adapter (slice 5) to call `WithSummary(ctx, pii.NewSummary())` BEFORE engine entry, so all three hook-side operations (PIIRedactionHook populating, LoggingHook reading) share the SAME `*Summary` value via ctx. **Slice 4 should NOT call `WithSummary` itself** — it should call `SummaryFromContext` and populate the existing pointer. Document this when planning slice 4.
- **Ready for 08-05 (main.go wiring).** LoggingHook is constructed `&plugin.LoggingHook{Logger: logger}` and placed LAST in `Chain.Pre` AND as the only entry in `Chain.Post` per D-04. Per slice 5 planning, the same instance can be reused in both slices (the `sync.Map startTimes` field is per-instance, so a single instance correctly bridges its own Pre→Post calls).
- **Ready for 08-06 (`/health/hooks` handler).** `LoggingHook.Describe()` returns `("Pre,Post", {"level": "INFO"|"DEBUG"|...})`. Slice 6 may choose to de-duplicate the Pre+Post rows when rendering using the combined kind value.
- **No blockers.** No deferred items. No outstanding architectural questions for slice 4/5/6.

---
*Phase: 08-plugin-hook-chain*
*Completed: 2026-05-28*
