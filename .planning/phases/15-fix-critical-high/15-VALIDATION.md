---
phase: 15
slug: fix-critical-high
status: draft
nyquist_compliant: true
wave_0_complete: false
created: 2026-06-11
---

# Phase 15 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | `go test` + `-race` (goroutine race detector) |
| **Config file** | none — `go test ./...` via Makefile or direct invocation |
| **Quick run command** | `go test -race ./internal/pool/... ./internal/server/... ./internal/adapter/openai/... ./internal/adapter/ollama/... ./cmd/otto-tray/...` |
| **Full suite command** | `go test -race ./...` |
| **Estimated runtime** | ~15–30 seconds |

---

## Sampling Rate

- **After every task commit:** Run quick run command above
- **After every plan wave:** Run full suite command (`go test -race ./...`)
- **Before `/gsd-verify-work`:** Full suite must be green AND REL-TRAY-02/REL-TRAY-03 operator validation recorded in SUMMARY
- **Max feedback latency:** 15–30 seconds

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 15-01-T1 | 01 | 1 | REL-POOL-01 | T-15-01 | ErrPoolExhausted typed 503 bounds wait; no indefinite hang | unit (race) | `go test -race ./internal/pool/ -run TestRegression_REL_POOL_01 -v -count=1 2>&1 \| tail -20` | ✅ `internal/pool/regression_rel_pool_01_test.go` | ⬜ pending |
| 15-01-T2a | 01 | 1 | REL-POOL-02 | T-15-03 | shutdownCh close prevents long-lived SSE from blocking 30s drain | unit (race) | `go test -race ./internal/pool/ -run TestRegression_REL_POOL_02 -v -count=1 2>&1 \| tail -10` | ✅ `internal/pool/regression_rel_pool_02_test.go` | ⬜ pending |
| 15-01-T2b | 01 | 1 | REL-HTTP-01 | T-15-03 | Admin SSE unwinds in < 1s on Ctrl-C; no blocking shutdown | unit (real HTTP, race) | `go test -race ./internal/server/ -run TestRegression_REL_HTTP_01 -v -count=1 2>&1 \| tail -10` | ✅ `internal/server/regression_rel_http_01_test.go` | ⬜ pending |
| 15-01-T3 | 01 | 1 | REL-POOL-03 | T-15-02 | CAS guard prevents stale goroutine from wiping newer session's stream pointer | unit (race) | `go test -race ./internal/pool/ -run TestRegression_REL_POOL_03 -v -count=1 2>&1 \| tail -10` | ✅ `internal/pool/regression_rel_pool_03_test.go` | ⬜ pending |
| 15-02-T1 | 02 | 1 | REL-HTTP-02 | T-15-05 | Idle-timeout no longer suppresses ACP Cancel watchdog | unit | `go test -race ./internal/adapter/openai/ -run TestRegression_REL_HTTP_02 -v -count=1 2>&1 \| tail -10` | ✅ `internal/adapter/openai/regression_rel_http_02_test.go` | ⬜ pending |
| 15-02-T2a | 02 | 1 | REL-HTTP-03 | T-15-04 | Error message is static string; no internal detail exposed to client (OpenAI) | unit | `go test -race ./internal/adapter/openai/ -run TestRegression_REL_HTTP_03 -v -count=1 2>&1 \| tail -10` | ✅ `internal/adapter/openai/regression_rel_http_03_test.go` | ⬜ pending |
| 15-02-T2b | 02 | 1 | REL-HTTP-03 | T-15-04 | Error message is static string; no internal detail exposed to client (Ollama) | unit | `go test -race ./internal/adapter/ollama/ -run TestRegression_REL_HTTP_03 -v -count=1 2>&1 \| tail -10` | ✅ `internal/adapter/ollama/regression_rel_http_03_test.go` | ⬜ pending |
| 15-03-T1 | 03 | 1 | REL-TRAY-01 | T-15-06 / T-15-08 | verifyGatewayIdentity prevents killing unrelated process that recycled the PID | unit (`//go:build darwin\|\|windows`) | `go test -race ./cmd/otto-tray/ -run TestRegression_REL_TRAY_01 -v -count=1 2>&1 \| tail -10` | ✅ `cmd/otto-tray/regression_rel_tray_01_test.go` | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

*None — all 9 regression test files already exist from Phase 14. No new test scaffolding needed. Existing infrastructure covers all phase requirements.*

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Get-GatewayStatus returns object; Invoke-Support completes when gateway is down; support bundle zip created | REL-TRAY-02 | Requires PowerShell on Windows; Go test is a permanently-skipped discoverability stub (cannot run PowerShell on macOS CI) | Run `tests/reliability/manual/REL-TRAY-02-repro.ps1` on Windows with gateway stopped. Post-fix: script completes, zip file created, exit 0. Operator records pass/fail in 15-SUMMARY.md. |
| applyState calls setIcon + SetTooltip; tray menu-bar icon changes state within poll interval after `kill -9 <gw_pid>` | REL-TRAY-03 | Requires macOS GUI session with tray running; Go test is a permanently-skipped discoverability stub (systray is not automatable in CI) | Run `tests/reliability/manual/REL-TRAY-03-repro.sh` on macOS GUI session with tray running. Kill gateway with `kill -9 <gw_pid>`; within ~6s (next poller tick) the menu-bar icon must change to the error/stopped state. Operator records pass/fail in 15-SUMMARY.md. |

---

## Validation Sign-Off

- [x] All tasks have `<automated>` verify or Wave 0 dependencies
- [x] Sampling continuity: no 3 consecutive tasks without automated verify
- [x] Wave 0 covers all MISSING references (none — all test files exist)
- [x] No watch-mode flags
- [x] Feedback latency < 15–30s
- [x] `nyquist_compliant: true` set in frontmatter

**Approval:** pending — flip to approved YYYY-MM-DD at phase close
