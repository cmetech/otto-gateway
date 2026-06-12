---
phase: 19
slug: acp-stream-concurrency-fix
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-06-12
---

# Phase 19 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.
> Sourced from 19-RESEARCH.md §Validation Architecture and 19-CONTEXT.md §Verification.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | Go testing stdlib + `go.uber.org/goleak` + `-race` flag |
| **Config file** | None (Go convention) |
| **Quick run command** | `go test -race ./internal/acp/ -run REL_ACP_01` |
| **Full suite command** | `make ci` |
| **Phase-specific race-loop gate** | `go test -race -count=60 ./internal/acp/ -run REL_ACP_01` and `go test -race -count=60 ./internal/pool/ -run REL_POOL_02` |
| **Estimated runtime** | quick ~5s · race-loop gate ~30s · `make ci` ~3–5min |

---

## Sampling Rate

- **After every task commit:** `go test -race ./internal/acp/ -run REL_ACP_01` (once the test file exists) + `go test -race ./internal/acp/...` + `go test -race ./internal/pool/...`
- **After plan wave:** `go test -race -count=60 ./internal/acp/ -run REL_ACP_01` AND `go test -race -count=60 ./internal/pool/ -run REL_POOL_02` (both race-loop gates).
- **Before `/gsd-verify-work`:** `make ci` exit 0 end-to-end + both 60-count race-loop gates clean.
- **Max feedback latency:** 30 seconds (quick run + race-loop gate)

---

## Per-Task Verification Map

> Populated by the planner from PLAN.md tasks. Each plan task with `<verify>` becomes a row here. Pre-fills for the locked D-IDs:

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 19-01-RED (D-19-03) | 01 | 1 | REL-ACP-01 | — | Test FAILS on pre-fix `stream.go` (race report or wrong StopReason). | unit/race | `go test -race -count=60 ./internal/acp/ -run REL_ACP_01` | ❌ NEW `internal/acp/regression_rel_acp_01_test.go` | ⬜ pending |
| 19-01-GREEN (D-19-01) | 01 | 1 | REL-ACP-01 | — | `Result()` returns pointer to copy under `s.mu`; race report disappears; 60/60 GREEN. | unit/race | `go test -race -count=60 ./internal/acp/ -run REL_ACP_01` | ✅ `internal/acp/stream.go` (edit lines 191–198) | ⬜ pending |
| 19-01-REVERT (D-19-02) | 01 | 1 | REL-ACP-01 | — | Phase 17 drain-Chunks workaround removed from REL_POOL_02; 60/60 GREEN proves revert safe. | unit/race | `go test -race -count=60 ./internal/pool/ -run REL_POOL_02` | ✅ `internal/pool/regression_rel_pool_02_test.go` (edit lines 130–152) | ⬜ pending |
| 19-01-CI | 01 | 1 | REL-ACP-01 | — | Full trust-gate clean. | integration | `make ci` | ✅ Makefile:259 | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] `internal/acp/regression_rel_acp_01_test.go` — NEW file, covers REL-ACP-01 positive assertion. Created in 19-01-RED.
- [ ] Framework install: NONE — `goleak`, `-race`, `canonical` package all already in use.
- [ ] No new shared fixtures — `NewStreamForTest`, `PushForTest`, `CloseForTest` are pre-existing exported test helpers at `internal/acp/stream_testhelpers.go:21,38,54`.

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| (none) | — | All phase behaviors have automated race-loop verification. | — |

*All phase behaviors have automated verification.*

---

## Validation Dimensions (Go concurrency fix specifics)

- **Race-loop count:** ≥ 60 outer (`-count=60`) × ≥ 100 inner iterations = ≥ 6,000 trials per CI invocation. Both `./internal/acp/` (new test) and `./internal/pool/` (reverted test) must clear this gate.
- **`t.Parallel()` choice:** Sequential (no `t.Parallel()` on the new test). The race is intra-test, not inter-test; sequential simplifies the goleak boundary.
- **Race detector overhead:** Expected to complete in seconds on M1/M2. If the 60-count run exceeds ~30s, drop inner iterations from 100 → 50 (still ≥ 6,000 trials).
- **goleak scope:** Per-test `defer goleak.VerifyNone(t)` in the new file. Package-wide `goleak.VerifyTestMain(m)` already covers regressions (testmain_test.go:18).

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references (just the new regression test file)
- [ ] No watch-mode flags
- [ ] Feedback latency < 30s (quick run + race-loop gate)
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
