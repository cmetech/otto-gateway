---
phase: 16-fix-mediums
plan: "02"
subsystem: http-surface
tags: [go, http, chi-middleware, time.AfterFunc, body-read-deadline, tailer, ring-buffer, sse, health-enum, atomic.Int64, tdd]

# Dependency graph
requires:
  - phase: 14-verify-reliability-findings
    provides: Regression test scaffolds for REL-HTTP-04 (skipped) and REL-HTTP-05 (skipped); pre-fix observable assertions
  - phase: 16-fix-mediums (Wave 1)
    provides: 16-01 → pool.LastProgressAt() + pool.IsExhausted() accessors consumed by healthHandler; 16-05 → server.Config.BodyReadTimeout populated by main.go from cfg.BodyReadTimeout
provides:
  - H-4 per-request body-read deadline (REL-HTTP-04) — withBodyReadDeadline chi middleware applied to chat-body POST sub-routers; SSE response writes unaffected
  - H-5 TailerMaxLineBytes cap enforced on newline-terminated lines too (REL-HTTP-05) — multi-MB chat-trace lines truncated to cap in ring buffer + SSE stream
  - D-05 PoolStats.Status enum (ok/degraded/exhausted) in /health JSON — cross-plan data plumbing for Plan 16-04 T-5 (Tray)
  - poolDegradedStallThreshold const (30s) — hardcoded per D-05a, NOT an env var; reserved POOL_DEGRADED_STALL_SEC for v1.10+
  - PoolStatsSource interface extended with IsExhausted() + LastProgressAt() — main.go poolStatsAdapter forwards from *pool.Pool
affects: [16-04 (Tray T-5 consumer reads /health pool.status enum)]

# Tech tracking
tech-stack:
  added: []  # No new deps — pure stdlib (time.AfterFunc, chi middleware)
  patterns:
    - "Per-request body-read deadline via time.AfterFunc + r.Body.Close() — scopes to body-read phase only; ResponseWriter and context untouched so long SSE writes are unbounded (D-04b)"
    - "Path-scoped chi middleware via static map[string]struct{} — admin POSTs and catalog GETs/POSTs are intentionally absent from the deadline path set"
    - "Method-scoped middleware — GET/HEAD/OPTIONS skip the wrapper because no body travels; only POST/PUT/PATCH/DELETE arm the timer"
    - "Cap enforcement on BOTH paths in readLines — the unterminated-fragment guard (pre-existing) plus a newline-terminated truncation step before broadcast (H-5 fix)"
    - "JSON-backward-compatible enum extension — PoolStats.Status is added without omitempty; the empty string is a meaningful 'pool not wired' signal that the tray probe can branch on"

key-files:
  created:
    - internal/server/body_deadline.go — withBodyReadDeadline chi middleware + chatBodyDeadlinePaths set (Task 1)
    - internal/server/health_status_test.go — 5 PoolStats.Status enum coverage tests (Task 3 atomic w/ implementation)
  modified:
    - internal/server/server.go — PoolStatsSource interface extended; withBodyReadDeadline registered after auth.IPAllowlist in each per-prefix Route block
    - internal/server/health.go — PoolStats.Status field + poolDegradedStallThreshold const + healthHandler computation
    - internal/server/server_test.go — fakePoolSource gains IsExhausted/LastProgressAt nop methods (interface compliance)
    - internal/server/regression_rel_http_04_test.go — unskipped + rewritten for post-fix shape; 2 subtests (BodyReadDeadline + SSEWriteUnaffected)
    - internal/admin/tail.go — readLines cap enforcement also fires in the HasSuffix("\n") branch (H-5 fix)
    - internal/admin/regression_rel_http_05_test.go — unskipped + flipped assertion to post-fix; pre-create empty log file so tailer can position at byte 0
    - cmd/otto-gateway/main.go — poolStatsAdapter forwards IsExhausted() and LastProgressAt() from *pool.Pool

key-decisions:
  - "Body-read deadline implemented as a chi middleware registered AFTER auth.IPAllowlist in each per-prefix Route block — denied requests do not arm the timer; the deadline runs at the path-aware seam closest to the handler"
  - "Path-scoped via a static map[string]struct{} of full paths (not a regex) — exact-match table is fast, auditable, and the deadline set is enumerated explicitly per D-04a (no surprise inclusions on admin or catalog routes)"
  - "Method scope POST/PUT/PATCH/DELETE — GET/HEAD/OPTIONS never carry a meaningful body so the timer is not armed; saves a goroutine on every /v1/models GET"
  - "Only r.Body.Close() fires on timeout — the ResponseWriter and request context are untouched, which is the load-bearing property that keeps SSE writes unbounded (D-04b)"
  - "Status field rendered without omitempty — '' is the meaningful 'no pool wired' signal; tray probe can branch on it to distinguish degraded-mode boot (KIRO_CMD unset) from a wired-but-OK pool"
  - "PoolStatsSource interface extended (vs. adding a sibling interface) — main.go poolStatsAdapter naturally owns the bridge for both Stats and IsExhausted/LastProgressAt; one interface, one adapter"
  - "Task 3 (D-05 Status enum) shipped as a single atomic commit per D-02 — the new test file could not compile against the unmodified PoolStats struct (Status field absent), so a standalone RED commit was not meaningful; same pattern as Plan 16-05 Task 3 (be7abbc) and Plan 16-01 Task 4 (775015d)"
  - "Pre-existing test scaffold fix in regression_rel_http_05_test.go: pre-create the log file empty before Subscribe. Tailer's D-10 invariant (never backfill historical content) caused the test to silently fail to deliver any line on first reopen() against a not-yet-created file. The fix is documented in the test file's setup block."

patterns-established:
  - "Chat-body POST allowlist as a static map in body_deadline.go — adding/removing endpoints from the deadline scope is a single edit at the table, not a per-handler middleware annotation; the table also documents which paths are NOT under the deadline (admin + catalog)"
  - "Status-enum coverage via a richer fake (statusPoolSource) separate from fakePoolSource — the OBSV-01 test only needs static Stats; the Status test needs configurable IsExhausted + LastProgressAt. Splitting the fakes keeps each test file's intent crisp."
  - "Tailer test setup: pre-create empty log file BEFORE Subscribe so the tailer's reopen() can seek to byte 0 and observe subsequent appends. Future regression tests against the tailer should follow this pattern; relying on the implicit reopen-retry path produces flaky 'tailer did not deliver any line' failures."

requirements-completed: [REL-HTTP-04, REL-HTTP-05]

# Metrics
duration: 25min
completed: 2026-06-11
---

# Phase 16 Plan 02: HTTP Surface Reliability Fixes Summary

**Two confirmed Medium HTTP findings closed (H-4 / REL-HTTP-04: per-request body-read deadline with SSE-write-safe semantics; H-5 / REL-HTTP-05: TailerMaxLineBytes cap enforced on newline-terminated lines too) — plus D-05 PoolStats.Status enum (ok/degraded/exhausted) plumbed into GET /health for Plan 16-04 (Tray) to consume.**

## Performance

- **Duration:** ~25 min
- **Started:** 2026-06-11T14:35:00Z
- **Completed:** 2026-06-11T14:50:00Z
- **Tasks:** 3 (Tasks 1 and 2 strict TDD R→G pairs; Task 3 atomic per D-02 — test could not compile against the unmodified PoolStats)
- **Files created:** 2 (body_deadline.go, health_status_test.go)
- **Files modified:** 7

## Accomplishments

- **H-4 (REL-HTTP-04):** Per-request body-read deadline via a `withBodyReadDeadline` chi middleware. The wrapper arms a `time.AfterFunc` timer scoped to a static set of chat-body POST paths (`/v1/chat/completions`, `/v1/messages`, `/v1/completions`, `/v1/embeddings`, `/api/chat`, `/api/generate`, `/api/embed`, `/api/embeddings`) and write methods (`POST`/`PUT`/`PATCH`/`DELETE`). On timeout the timer calls `r.Body.Close()` — the next `Read` on the body returns an error, unblocking `io.ReadAll` / `json.Decode` on the handler goroutine. Critical property: the timer NEVER touches the `ResponseWriter` or the request context, so long SSE response writes are unbounded by the deadline (D-04b / must_haves bullet 2).
- **H-5 (REL-HTTP-05):** The `readLines` cap bug at `tail.go:402` is closed. Previously the cap check fired only when `!strings.HasSuffix(current, "\n")` — a multi-MB chat-trace line arriving newline-terminated in a single `ReadString` call bypassed truncation and flowed unbounded through the ring buffer and SSE stream. The fix keeps the unterminated-fragment guard and adds a second cap check in the `HasSuffix("\n")` branch that truncates the trimmed line before broadcast. A new Debug log line `admin: tailer line truncated at cap` distinguishes the newline-terminated cap path from the fragment-cap path for operator diagnosis.
- **D-05 (cross-plan for Plan 16-04 T-5):** `PoolStats.Status` enum (`ok` | `degraded` | `exhausted`) is now rendered in GET /health JSON. Computation in `healthHandler` follows the priority order from `16-CONTEXT.md`:
  1. `exhausted` when `pool.IsExhausted()` returns true (D-05b — single boolean check, no escape hatch).
  2. `degraded` when `Size > 0 AND Busy == Alive == Size AND time.Since(LastProgressAt()) > poolDegradedStallThreshold` (D-05a, 30s threshold).
  3. `ok` otherwise.
  The `poolDegradedStallThreshold` is a compile-time `const` (30s); `POOL_DEGRADED_STALL_SEC` is reserved for v1.10+ if operators report false-positives on slow networks.
- `PoolStatsSource` interface extended with `IsExhausted() bool` and `LastProgressAt() time.Time`. `cmd/otto-gateway/main.go` `poolStatsAdapter` forwards both from `*pool.Pool` (accessors added by Plan 16-01 Task 4), keeping `internal/server` import-clean of `internal/pool` (TRST-04).
- Both Phase 14 regression tests (`TestRegression_REL_HTTP_04`, `TestRegression_REL_HTTP_05`) unskipped in their respective fix commits per D-02.
- `go test -race ./...` clean tree-wide.

## Task Commits

Each task followed RED → GREEN per `type: tdd` plan frontmatter, except Task 3 which shipped atomically per D-02 (the new behavior-coverage test could not compile against the unmodified PoolStats struct — a standalone RED commit would have been theater).

1. **Task 1 RED — Unskip REL-HTTP-04 + rewrite for post-fix** — `ae99d6c` (test) — confirmed FAIL (handler parked 3s, no deadline enforced)
2. **Task 1 GREEN — H-4 body-read deadline middleware** — `190a68a` (fix) — TestRegression_REL_HTTP_04_BodyReadDeadline + SSEWriteUnaffected both PASS
3. **Task 2 RED — Unskip REL-HTTP-05 + flip assertion + tailer file-pre-create fix** — `55f69fa` (test) — confirmed FAIL (5×TailerMaxLineBytes line delivered intact)
4. **Task 2 GREEN — H-5 readLines cap on newline-terminated lines** — `34fac0b` (fix) — line truncated to exactly TailerMaxLineBytes
5. **Task 3 — D-05 PoolStats.Status enum + interface extension + adapter forward (RED+GREEN atomic per D-02)** — `f847cd4` (feat) — 5/5 Status enum cases PASS

**Plan metadata commit:** (this commit — SUMMARY + STATE + ROADMAP)

## Files Created/Modified

**Production source (H-4 / Task 1):**
- `internal/server/body_deadline.go` (NEW) — `withBodyReadDeadline` middleware + `chatBodyDeadlinePaths` static set. The wrapper checks path and method, arms `time.AfterFunc(timeout, func() { _ = r.Body.Close() })`, defers `timer.Stop()`. Zero timeout disables the wrapper (defensive — Plan 16-05 already rejects ≤ 0 at boot).
- `internal/server/server.go` — `r.Use(withBodyReadDeadline(cfg.BodyReadTimeout))` registered after `auth.IPAllowlist` in each per-prefix `Route` block.

**Production source (H-5 / Task 2):**
- `internal/admin/tail.go` — `readLines` gains a second cap check in the `HasSuffix("\n")` branch: `if len(line) > TailerMaxLineBytes { ... line = line[:TailerMaxLineBytes] }` before broadcast. The pre-existing unterminated-fragment guard at the top of the loop is preserved.

**Production source (D-05 / Task 3):**
- `internal/server/health.go` — `PoolStats.Status string \`json:"status"\``; `const poolDegradedStallThreshold = 30 * time.Second`; healthHandler switch computes `exhausted` / `degraded` / `ok` from `s.pool.IsExhausted()` + `time.Since(s.pool.LastProgressAt())`.
- `internal/server/server.go` — `PoolStatsSource` interface extended with `IsExhausted() bool` and `LastProgressAt() time.Time` (docstring updates the JSON sub-tree to `{size, alive, busy, status}`).
- `cmd/otto-gateway/main.go` — `poolStatsAdapter` gains `IsExhausted()` and `LastProgressAt()` methods that forward to `*pool.Pool`'s accessors (added by Plan 16-01).

**Regression tests (unskipped per D-02):**
- `internal/server/regression_rel_http_04_test.go` — `t.Skip` removed; rewritten to drive through `NewFromConfig` with a stub `/v1/chat/completions` handler that calls `io.ReadAll`. Two subtests: `BodyReadDeadline` (handler returns within deadline + non-nil read error) and `SSEWriteUnaffected` (handler writes 10 SSE chunks across 1s with 200ms deadline; all chunks delivered).
- `internal/admin/regression_rel_http_05_test.go` — `t.Skip` removed; assertion flipped from "want > cap" to "want ≤ cap"; pre-create the log file empty so the tailer's D-10 reopen-at-EOF positions at byte 0 instead of past the just-written 5 MB blob.

**Supporting test updates (PoolStatsSource ripple):**
- `internal/server/server_test.go` — `fakePoolSource` gains `IsExhausted() bool` returning `false` and `LastProgressAt() time.Time` returning `time.Now()` (nop implementations for OBSV-01 coverage that doesn't care about status).
- `internal/server/health_status_test.go` (NEW) — richer `statusPoolSource` fake + 5 enum coverage cases (Exhausted, Degraded, OK_RecentProgress, OK_HasFreeSlot, NilPool).

## Decisions Made

- **`withBodyReadDeadline` registered after `auth.IPAllowlist`** — denied requests do not arm the timer. The middleware order in each per-prefix `Route` block is: `RequestID` (outer) → `Recoverer` (outer) → `accessLog` (outer) → `IPAllowlist` (per-prefix) → `withBodyReadDeadline` (per-prefix) → adapter handler. This puts the body deadline as close to the handler as possible while still respecting auth.
- **Static path map vs. regex / per-handler wrapper** — `chatBodyDeadlinePaths` is a `map[string]struct{}` literal. The set is small, exact-match is fast (O(1) hash lookup), and additions/removals are a single-line edit at the table. A regex would have been over-engineered; a per-handler wrapper would have spread the policy across three adapter packages.
- **Method scope `POST`/`PUT`/`PATCH`/`DELETE` only** — `GET`/`HEAD`/`OPTIONS` skip the timer because bodies on those methods are conventionally empty and parsers do not read them. Avoids arming a goroutine for every `/v1/models` GET.
- **`Status` field WITHOUT `omitempty`** — the empty string is a meaningful "no pool wired" signal. Plan 16-04's tray probe can distinguish degraded-mode boot (`status: ""`) from a wired-but-OK pool (`status: "ok"`); omitempty would have lost that distinction.
- **`PoolStatsSource` interface extended (vs. sibling interface)** — `main.go`'s `poolStatsAdapter` naturally owns the bridge for `Stats`, `IsExhausted`, and `LastProgressAt`. A sibling `PoolStatusSource` interface would have required a separate adapter for one extra method. The existing interface is the natural home for the new accessors.
- **Task 3 atomic per D-02** — the new `health_status_test.go` references `PoolStats.Status` which did not exist before this plan. Authoring a "RED" commit that wouldn't compile would have been theater; the test ships in the same atomic commit as the production change. This mirrors Plan 16-05 Task 3 (commit `be7abbc`) and Plan 16-01 Task 4 (commit `775015d`).

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Test Scaffold Bug] Pre-existing tailer test scaffold could not deliver multi-MB lines**

- **Found during:** Task 2 RED — after unskipping `TestRegression_REL_HTTP_05_AdminTailerLineCapBypass`, the test failed with `"tailer did not deliver any line within 3s"`. Not the post-fix invariant failure I was expecting.
- **Issue:** The Phase 14 regression test scaffold subscribed to the Tailer, slept 400 ms, then called `appendToFile` to create + write the 5 MB line. But the Tailer's D-10 invariant ("never backfill historical content") means `reopen()` seeks to EOF of whatever file exists at the moment. The first reopen (called immediately on Subscribe) fired against a non-existent file and silently logged DEBUG + returned with `f == nil`. The next poll tick (250 ms later) called `reopen()` again, this time against the now-existing 5 MB file — and seeked to byte 5 MB. All subsequent polls saw `st.Size() == lastSize` and the line was never read.
- **Fix:** Pre-create the log file empty (`os.WriteFile(logPath, nil, 0o644)`) BEFORE constructing the Tailer. The first reopen positions at byte 0 of an empty file; the subsequent append at byte 0 is read on the next poll tick. Documented inline in the test setup with the rationale.
- **Files modified:** `internal/admin/regression_rel_http_05_test.go` (test-only)
- **Verification:** Test correctly reports the pre-fix invariant violation (line of 5 × TailerMaxLineBytes delivered intact) → confirms RED. After the production fix, the test correctly reports the post-fix invariant satisfied (line truncated at exactly TailerMaxLineBytes).
- **Committed in:** `55f69fa` (Task 2 RED) — bundled with the unskip + assertion flip, all test-only changes in one atomic step.

**2. [Rule 3 - Blocking] Existing `fakePoolSource` does not satisfy the extended `PoolStatsSource` interface**

- **Found during:** Task 3 — after extending the `PoolStatsSource` interface with `IsExhausted()` and `LastProgressAt()`, the build broke at `internal/server/server_test.go:417` because `fakePoolSource` (the OBSV-01 fixture) only implements `Stats()`.
- **Issue:** Interface extension is a breaking change for all implementers; the test fixture had to gain the two new methods.
- **Fix:** Added `func (f fakePoolSource) IsExhausted() bool { return false }` and `func (f fakePoolSource) LastProgressAt() time.Time { return time.Now() }`. Both are nop / always-safe values appropriate for the OBSV-01 tests that exercise only the `Stats` shape, not the new status enum.
- **Files modified:** `internal/server/server_test.go` (test-only)
- **Verification:** `go build ./...` clean; all pre-existing OBSV-01 tests still pass.
- **Committed in:** `f847cd4` (Task 3 atomic — production interface extension + test fixture compliance ship together).

---

**Total deviations:** 2 auto-fixed (1 pre-existing test scaffold bug, 1 mandatory ripple from interface extension)
**Impact on plan:** Zero scope creep — both deviations were required for the plan's stated success criteria. The tailer test scaffold fix was necessary to make REL-HTTP-05's regression test actually exercise the bug (without it, the assertion never ran). The fakePoolSource ripple was the natural consequence of extending the PoolStatsSource interface per D-05's required healthHandler integration.

## Issues Encountered

None unexpected. The H-4 and H-5 findings were well-characterized by Phase 14's verification ledger and the `16-PATTERNS.md` mapping. The D-05 cross-plan plumbing was straightforward because Plan 16-01 had already landed the `pool.IsExhausted()` and `pool.LastProgressAt()` accessors with the documented semantics.

## Verification

**Phase-level grep gates (all PASS):**

```
H-4: BodyReadTimeout in internal/server/server.go         = 3 occurrences
H-5: TailerMaxLineBytes in internal/admin/tail.go         = 11 occurrences (constant + 2 enforcement sites + Debug log + docstrings)
D-05: "status" in internal/server/health.go               = 2 occurrences (struct tag + docstring)
D-05: poolDegradedStallThreshold in internal/server/...   = 4 occurrences (const + healthHandler call + docstrings)
D-05: IsExhausted in internal/server/health.go            = 1 occurrence (healthHandler switch)
```

**Regression tests (all PASS):**

```
TestRegression_REL_HTTP_04_BodyReadDeadline       PASS  (-race, 0.21s — 200ms deadline, handler returns at 201ms with 408)
TestRegression_REL_HTTP_04_SSEWriteUnaffected     PASS  (-race, 1.02s — 1s write phase delivers all 10 SSE chunks)
TestRegression_REL_HTTP_05_AdminTailerLineCapBypass  PASS  (-race, 0.52s — 5×cap line truncated to exactly cap)
TestHealth_PoolStatus_Exhausted                   PASS  (-race)
TestHealth_PoolStatus_Degraded                    PASS  (-race)
TestHealth_PoolStatus_OK_RecentProgress           PASS  (-race)
TestHealth_PoolStatus_OK_HasFreeSlot              PASS  (-race)
TestHealth_PoolStatus_NilPool                     PASS  (-race)
```

**Full suite under -race:**

```
go test -race ./...   → all packages PASS
go build ./...        → exit 0
go vet ./...          → clean
```

## TDD Gate Compliance

Plan-level TDD gate sequence verified in git history:

- **Task 1 (H-4 body-read deadline)**: `ae99d6c` test(...) RED → `190a68a` fix(...) GREEN ✓
- **Task 2 (H-5 tailer line cap)**: `55f69fa` test(...) RED → `34fac0b` fix(...) GREEN ✓
- **Task 3 (D-05 PoolStats.Status enum)**: `f847cd4` feat(...) atomic per D-02. The new `health_status_test.go` references `PoolStats.Status` which did not exist before this plan, so authoring a "RED" commit that wouldn't compile would have been theater. This pattern is the explicit D-02 "unskip-in-same-commit" intent: the production source edit and the test land together. Same posture as Plan 16-05 Task 3 (`be7abbc`) and Plan 16-01 Task 4 (`775015d`).

No gate-sequence warnings.

## Next Phase Readiness

- **Plan 16-04 (Tray T-5)** can now proceed: GET /health JSON renders `pool.status` as one of `"ok"`, `"degraded"`, `"exhausted"`, or `""` (no pool wired). The tray probe at `cmd/otto-tray/tray.go` can branch on this field directly without re-deriving from Busy/Alive/Size. The empty-string case maps to the same `"unknown"` UX as a JSON decode failure.
- **Phase 16 Wave 1**: 4 of 4 plans complete (16-01 Pool/ACP, 16-02 HTTP, 16-03 Hooks, 16-05 Config). Wave 2 (Plan 16-04 Tray) can now start — all its dependencies (`pool.LastProgressAt`, `PoolStats.Status`) are in place.
- **v1.9 Reliability Hardening milestone**: 4 of 5 Phase 16 plans complete. Plan 16-04 remaining for milestone close.

## Self-Check: PASSED

All claimed files and commits verified on disk and in git:

- internal/server/body_deadline.go — FOUND
- internal/server/server.go — FOUND
- internal/server/health.go — FOUND
- internal/server/server_test.go — FOUND
- internal/server/health_status_test.go — FOUND
- internal/server/regression_rel_http_04_test.go — FOUND
- internal/admin/tail.go — FOUND
- internal/admin/regression_rel_http_05_test.go — FOUND
- cmd/otto-gateway/main.go — FOUND
- commit ae99d6c (Task 1 RED) — FOUND
- commit 190a68a (Task 1 GREEN — H-4) — FOUND
- commit 55f69fa (Task 2 RED) — FOUND
- commit 34fac0b (Task 2 GREEN — H-5) — FOUND
- commit f847cd4 (Task 3 atomic — D-05) — FOUND

---
*Phase: 16-fix-mediums*
*Plan: 02*
*Completed: 2026-06-11*
