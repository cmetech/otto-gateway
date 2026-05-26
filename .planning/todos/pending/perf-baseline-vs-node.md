---
id: perf-baseline-vs-node
status: pending
created: 2026-05-26
origin: phase 05-pool-stateful-sessions (Plan 05-05 Accepted-with-Notes sign-off)
type: perf-milestone
resolves_phase: null
tags:
  - perf
  - rss
  - claude-md-non-functional
  - milestone-deferral
---

# Performance baseline vs Node reference implementation

The CLAUDE.md non-functional constraint — "must not be slower than the Node
implementation" — was deferred at Phase 5 close (2026-05-26) in favour of
functional reliability coverage under multi-worker pool. See
`tests/e2e/reports/PHASE5-PERF.md` `## Sign-Off ## Notes` for the
Accepted-with-Notes rationale.

This todo tracks the two measurements that were deferred:

1. **Side-by-side wrk latency** — Go gateway vs Node reference
   (`../gitlab.rosetta.ericssondevops.com/loop_24/acp_server`) at
   4-thread × 8-conn × 30 s with `POOL_SIZE=4`. Threshold:
   `(p99_go - p99_node) / p99_node * 100 ≤ 10`.

2. **Per-session RSS sanity** — 8 simultaneously-live sessions, per-child
   RSS via `ps -o pid,rss`. Thresholds: per-session RSS within ±20% of
   mean; `32 × avg_rss_mb ≤ 2048` on an 8 GB host.

Reproducible commands are preserved in
`tests/e2e/reports/PHASE5-PERF.md` `## Future Measurement Run Protocol`,
and the wrk script lives at `tests/e2e/wrk/post.lua` (tracked in git).

**Resolution path:** when picked up, populate the two empty sections in
`tests/e2e/reports/PHASE5-PERF.md` with real numbers, then either replace
the `## Sign-Off` Closure line with `Satisfied` (if both thresholds pass)
or extend the Accepted-with-Notes rationale (if a threshold misses but
the result is still acceptable for the target milestone).
