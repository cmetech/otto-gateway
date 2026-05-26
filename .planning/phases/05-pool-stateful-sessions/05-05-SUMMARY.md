---
phase: 05-pool-stateful-sessions
plan: 05
subsystem: verification / perf
tags:
  - perf
  - rss
  - human-verification
  - claude-md-non-functional
  - gap-closure
  - in-progress
dependency_graph:
  requires:
    - 05-04   # SC3 + SC4 must be GREEN before Task 3 RSS measurement is meaningful
  provides:
    - "Phase 5 perf + RSS measurement skeleton (PHASE5-PERF.md) — file exists on disk; gap 3 from 05-VERIFICATION.md is now closeable by human operator at Tasks 2-4 checkpoints."
    - "tests/e2e/wrk/post.lua — committed wrk script reused by future perf runs."
  affects:
    - .planning/phases/05-pool-stateful-sessions/05-VERIFICATION.md  # will be re-stamped by Task 4 closure heading (after human measurement)
tech_stack:
  added:
    - wrk-lua  # operator-side load tool; not a project dependency
  patterns:
    - "Gitignored measurement artifact + tracked tooling: the report file is gitignored (regenerated per measurement run) but the wrk Lua script is committed for reproducibility — existence-on-disk gates closure rather than git history."
key_files:
  created:
    - tests/e2e/reports/PHASE5-PERF.md      # 222 lines, gitignored, exists-on-disk gate
    - tests/e2e/wrk/post.lua                # 23 lines, tracked
  modified: []
decisions:
  - "Manual perf + RSS gate has exactly two valid closure states: Satisfied (full measurement) OR Accepted-with-Notes (operator name + ISO date in heading; ≥ 1-paragraph rationale in PHASE5-PERF.md ## Sign-Off ## Notes). REJECTED keeps the gap open."
  - "PHASE5-PERF.md is gitignored under tests/e2e/reports/; the existence-on-disk gate from 05-VERIFICATION.md gap 3 closes when the file exists with the documented structure — not when it lands in git history."
  - "Plan 05-05 cannot complete (and Phase 5 status: field cannot flip to verified) while ANY placeholder token (TBD, AWAITING MANUAL MEASUREMENT, BLOCKED_ON_05-04, NODE_IMPL_UNAVAILABLE, pending, placeholder) remains in PHASE5-PERF.md."
metrics:
  duration: "Task 1 only — ~7 minutes elapsed at checkpoint"
  completed: in-progress (awaiting human-action checkpoints for Tasks 2-4)
---

# Phase 5 Plan 05: Manual Perf + RSS Verification — Summary

## One-Liner

PHASE5-PERF.md skeleton + wrk/post.lua landed on disk; the human-action checkpoints (Tasks 2-4) requiring side-by-side wrk measurements against the sibling Node implementation and per-kiro-cli RSS capture are awaited from the operator.

## Status

**IN PROGRESS — paused at the first human-action checkpoint (Task 2).** This SUMMARY records the state of the executor at the checkpoint boundary so the orchestrator and any continuation agent have a clean handoff. The plan is not complete until Tasks 2/3/4 are signed off by the operator and the placeholder-token audit passes.

## What Was Done

### Task 1 — autonomous skeleton — COMPLETE (commit `cc41472`)

Created two artifacts:

1. **`tests/e2e/reports/PHASE5-PERF.md`** (222 lines, gitignored per `.gitignore:43`).
   - 8 top-level sections in the plan-mandated order: header metadata, `## Acceptance Thresholds`, `## Test Environment`, `## Measurement Protocol — Latency`, `### Latency Results`, `## Measurement Protocol — Per-Session RSS`, `### Per-Session RSS Results` (+ Stats), `## Sign-Off` (+ closure-heading mapping).
   - Acceptance thresholds quoted verbatim from `CLAUDE.md` ("Must not be slower than the Node implementation under concurrent load. Tail latency should improve.") and `05-VERIFICATION.md` `human_verification` block (p99 ≤ Node + 10%; ±20% spread; 32 × avg ≤ 2 GB).
   - Every measurement cell is `TBD`; every threshold check carries the `AWAITING MANUAL MEASUREMENT` marker.
   - Closure-heading mapping table embedded in `## Sign-Off` shows how the verdict (`ACCEPTED` / `ACCEPTED-WITH-NOTES` / `REJECTED`) maps to one of the two valid 05-VERIFICATION.md re-stamp headings, and bakes in the placeholder-token audit + global `status:` flip preconditions.
   - The protocol includes the exact `wrk -t4 -c8 -d30s --latency -s tests/e2e/wrk/post.lua` invocation, `ps -o pid,rss -p $(pgrep -P $GATEWAY_PID kiro-cli)` for RSS, the `p99_delta_pct = (p99_go - p99_node) / p99_node * 100` formula, and `projected_32 = 32 * mean_kb / 1024 / 1024` for the 2 GB ceiling check.

2. **`tests/e2e/wrk/post.lua`** (23 lines, tracked).
   - Sets `wrk.method = "POST"`, `wrk.headers["Content-Type"] = "application/json"`, and reads `/tmp/req.json` as the body.
   - Includes an `assert(f ~= nil, ...)` nil-guard so the script fails loudly when the operator forgets to stage `/tmp/req.json` (silently sending an empty body would skew the measurement).
   - This file is the dependency that the `## Measurement Protocol — Latency` section in PHASE5-PERF.md cites.

**Task 1 verify (automated) — PASS:**

```
test -f tests/e2e/reports/PHASE5-PERF.md  → EXISTS
wc -l                                     → 222 lines  (≥ 80 required)
grep "AWAITING MANUAL MEASUREMENT"        → PRESENT
grep "p99(Go)"                            → PRESENT
test -f tests/e2e/wrk/post.lua            → EXISTS
```

### Tasks 2, 3, 4 — pending human-action checkpoints

Cannot be executed by Claude in this session:

- **Task 2 (latency):** requires the Node reference at `../gitlab.rosetta.ericssondevops.com/loop_24/acp_server` running on port 11435 side-by-side with the Go gateway on 11434, plus `wrk` installed on the host. Neither is hermetic; both are operator-provisioned.
- **Task 3 (RSS):** requires a long-running gateway with `POOL_SIZE=4 SESSION_MAX=8` populated by 8 curl requests, then `ps -o pid,rss` against the kiro-cli children. Platform-dependent and binary-version-dependent.
- **Task 4 (sign-off):** depends on Tasks 2 + 3 results, and includes the placeholder-token audit and the global `status: verified` flip (Plan 05-05's exclusive responsibility per LOW-2).

The checkpoint payload returned to the orchestrator carries the exact operator commands.

## Deviations from Plan

**None.** The skeleton was written exactly to the plan's `<action>` spec for Task 1 (8 sections in order, verbatim threshold quotes, post.lua with the 4 identifiers and nil-guard, file lengths above the verify gate).

No Rule 1/2/3 auto-fixes triggered. No Rule 4 architectural decisions surfaced.

## Auth / Environmental Gates

No auth gate. The Tasks 2-4 checkpoint is an environment gate (sibling Node repo + wrk + long-running gateway processes) that the operator must provision — categorized as `checkpoint:human-action` in the plan rather than an auth-gate per `<authentication_gates>`.

## Known Stubs

PHASE5-PERF.md contains the `AWAITING MANUAL MEASUREMENT` / `TBD` tokens by design — Task 1's purpose is to ship the skeleton with these markers so the operator (and the placeholder-token audit at Task 4) has unambiguous evidence that measurement has not yet been performed. These are NOT silent stubs: they are explicit, audited closure-blockers documented in PLAN.md `<verification>` items 5-7. They will be replaced with concrete numbers by Tasks 2-3 and audited away at Task 4.

## Self-Check

- `test -f tests/e2e/reports/PHASE5-PERF.md` → **FOUND**
- `wc -l tests/e2e/reports/PHASE5-PERF.md` → 222 lines (≥ 80) → **PASS**
- `grep -q "AWAITING MANUAL MEASUREMENT" tests/e2e/reports/PHASE5-PERF.md` → **FOUND**
- `grep -q "p99(Go)" tests/e2e/reports/PHASE5-PERF.md` → **FOUND**
- `test -f tests/e2e/wrk/post.lua` → **FOUND**
- `git log --oneline | grep cc41472` → **FOUND** (`feat(05-05): PHASE5-PERF.md skeleton + wrk/post.lua`)

## Self-Check: PASSED (for Task 1 — the autonomous half of the plan)

## Resumption Pointer

The operator (or any continuation agent) should resume at the Task 2 checkpoint payload returned by this executor. The full reproducible commands live in `tests/e2e/reports/PHASE5-PERF.md` `## Measurement Protocol — Latency` and `## Measurement Protocol — Per-Session RSS`. Resume signals: `latency-measured <delta-pct>` / `latency-deferred <reason>`, then `rss-measured <mean-mb> <spread-pct>` / `rss-deferred <reason>`, then `signed-off accepted` / `signed-off accepted-with-notes` / `signed-off rejected`.
