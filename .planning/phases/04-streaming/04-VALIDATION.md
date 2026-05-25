---
phase: 4
slug: streaming
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-05-24
---

# Phase 4 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | `go test` (stdlib testing + `goleak` gate on engine/handler packages) |
| **Config file** | none — Go toolchain native |
| **Quick run command** | `go test ./internal/...` |
| **Full suite command** | `go test ./... && go vet ./... && gosec ./...` |
| **Estimated runtime** | ~30–90 seconds (incl. real-binary E2E) |

---

## Sampling Rate

- **After every task commit:** Run `go test ./internal/adapter/... ./internal/engine/...`
- **After every plan wave:** Run `go test ./...`
- **Before `/gsd:verify-work`:** Full suite + `go vet` + `gosec` must be green; `goleak` must report zero leaked goroutines.
- **Max feedback latency:** 90 seconds

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| TBD — planner populates from PLAN.md tasks (one row per task; STRM-01..05 mapped to unit / fake-ACP / real-binary E2E layers per RESEARCH.md "Validation Architecture") | | | | | | | | | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] Confirm `goleak.VerifyTestMain` is active on `internal/engine` and `internal/adapter/ollama` test packages (watchdog teardown guard for STRM-04).
- [ ] Existing `tests/e2e/` harness + `internal/acp/fakeacp_test.go` cover the surfaces; no new framework install needed.

*Existing Go test infrastructure covers all phase requirements — Wave 0 only verifies the `goleak` gate is wired.*

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|

*All phase behaviors have automated verification (D-09 automated E2E per surface; D-10 fake-ACP frame assertion + real-binary disconnect smoke). No HUMAN-UAT gate.*

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 90s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
