---
phase: quick
plan: 260804-imt
status: complete
subsystem: worker-lifecycle-observability
tags: [pool, idle-recycle, admin, prometheus, adversarial-review]
dependency_graph:
  requires:
    - "quick-260804-ae3 idle-memory Kiro worker recycling"
  provides:
    - "Zero memory threshold disables idle recycling for direct pool.Config callers"
    - "Admin checked_out state renders acquisition activity instead of stale idle time"
    - "Prometheus idle gauge HELP documents busy and unused zero semantics"
  affects:
    - "v3.1.0 release readiness"
key_files:
  created:
    - "docs/reviews/2026-08-04-idle-memory-worker-recycling-adversarial-review-prompt.md"
  modified:
    - "internal/pool/idle_recycle.go"
    - "internal/pool/detail.go"
    - "internal/admin/snapshot.go"
    - "internal/admin/static/js/admin.js"
    - "internal/metrics/worker_collector.go"
decisions:
  - "Preserve Busy as active ACP-session state and expose CheckedOut separately; checkout also covers acquisition and maintenance ownership."
  - "Accept the instruction-scale receive-to-checkedOut gauge window because it cannot affect recycling or slot ownership and is limited to one transient scrape."
  - "Keep native Windows working-set and real-Kiro recycling as an explicit release evidence gap; cross-compilation does not prove Win32 runtime behavior."
metrics:
  completed: "2026-08-04T17:35:58Z"
  commits: 2
---

# Quick Task 260804-imt Summary

The approved adversarial-review follow-ups are complete. A zero-byte memory
threshold now disables the idle recycler, the admin dashboard distinguishes a
checked-out worker from an idle worker without redefining ACP-session `busy`,
and the `gw_worker_idle_seconds` HELP text explains that busy and never-used
workers emit zero.

## Changes

- Added a startup guard and regression test so a direct `pool.Config` caller
  cannot accidentally turn the conjunctive idle-and-memory policy into
  idle-only recycling with a zero threshold.
- Added additive `checked_out` fields through pool detail, the command adapter,
  and the admin snapshot. A checked-out worker without a registered ACP session
  now renders `Active` and `IDLE active`.
- Updated the Prometheus HELP text and locked it through real text-exposition
  testing.
- Saved the reusable external adversarial review prompt under `docs/reviews/`.

## TDD Evidence

- Zero-threshold test failed with two lifecycle-hook calls before the guard and
  passed after it.
- Checkout projection tests failed to compile before the additive field; the
  rendered card test showed `Idle` with a growing `IDLE` duration before the UI
  change.
- Metrics exposition test failed against the old HELP text before the wording
  update.

## Verification

- `go test -race ./internal/pool -count=1` — PASS, including goleak coverage.
- `go test -race ./internal/admin ./internal/metrics ./cmd/otto-gateway -count=1` — PASS.
- `node --test internal/admin/admin_js_test.js` — 4/4 PASS.
- `make ci` with a fresh isolated golangci-lint cache — PASS: vet, build,
  0 lint issues, full race suite, admin JS, architecture lint, examples, and
  govulncheck (0 called vulnerabilities).
- Windows amd64, Linux amd64, and Darwin arm64 cgo-free Gateway cross-builds —
  PASS.
- Grafana generator parity, bash syntax, bash support bundle (151/151), privacy
  documentation checks, and `git diff --check` — PASS.

The first `make ci` attempt encountered stale golangci-lint cache entries that
referenced removed worktrees. The fresh isolated cache run was clean and is the
authoritative result.

## Accepted Residuals

- There remains an instruction-scale interval between receiving a slot from
  `p.slots` and setting `checkedOut` under `p.mu`. A scrape in that interval can
  report one stale nonzero idle gauge. The slot is already absent from the free
  channel, so this cannot cause sampling-driven recycling, interruption, double
  ownership, or capacity loss. No channel-protocol redesign was made for this
  low-impact observability-only window.
- Native Windows runtime behavior remains unproven on this macOS host. Before
  broad rollout, run the real-Kiro walkthrough on Windows and confirm working
  set sampling plus the 15-minute/500-MiB replacement and PID/metric changes.

## Commits

- `b5f5753` — save the adversarial review prompt.
- `fde22ce` — implement and test the review follow-ups.
