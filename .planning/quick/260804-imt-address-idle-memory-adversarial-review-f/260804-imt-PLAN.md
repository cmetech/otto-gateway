---
quick_id: 260804-imt
status: complete
description: Address idle-memory adversarial review follow-ups before v3.1.0 release
---

# Quick Task 260804-imt Plan

Address the three user-approved follow-ups from the external adversarial review
without redesigning pool channel ownership or changing existing ACP-session
`busy` semantics.

## Task 1: Disable the idle recycler when the memory threshold is zero

**Files:** `internal/pool/idle_recycle.go`, `internal/pool/idle_recycle_test.go`

**Action:** Add a behavioral regression test proving a directly constructed
pool with a non-zero idle duration and zero memory threshold never starts or
executes the idle recycler. Observe RED, then add the minimal startup guard.

**Verify:** Focused pool test passes under the race detector.

**Done:** Zero memory cannot turn the policy into idle-only recycling, including
for future direct `pool.Config` callers that bypass environment validation.

## Task 2: Surface checkout state separately in the admin dashboard

**Files:** `internal/pool/detail.go`, pool detail tests,
`cmd/otto-gateway/main.go`, `cmd/otto-gateway/main_test.go`,
`internal/admin/snapshot.go`, admin snapshot tests,
`internal/admin/static/js/admin.js`, `internal/admin/admin_js_test.js`

**Action:** Add an additive per-slot `checked_out` projection while preserving
`busy` as active-ACP-session state. Write RED tests for pool detail, the main
projection, snapshot JSON, and a real rendered card. The UI must render a
checked-out but not-yet-registered slot as active instead of showing a growing
idle countdown.

**Verify:** Focused Go tests and the Node admin suite pass. Existing busy and
unsupported-platform states remain unchanged.

**Done:** Admin presentation agrees with the worker idle metric throughout the
post-checkout acquisition interval without conflating maintenance ownership
with an ACP session ID.

## Task 3: Clarify idle metric exposition and complete release gates

**Files:** `internal/metrics/worker_collector.go`,
`internal/metrics/worker_collector_test.go`, the saved adversarial review prompt,
quick-task summary, and `.planning/STATE.md`.

**Action:** Add a RED real-exposition assertion for the busy/unused zero-value
caveat, update the HELP string minimally, preserve the known instruction-scale
receive-to-checkout sampling window as an accepted low-severity follow-up, and
record verification evidence.

**Verify:** Focused metrics test, `go test ./...`, `make ci`, cross-platform
Gateway builds, dashboard/admin tests, diff checks, and clean worktree pass.

**Done:** The branch is committed, reviewed by its executable gates, and ready
for the explicitly authorized merge, push, and `v3.1.0` tag-triggered release.
