---
phase: 1
slug: foundations
status: ready
nyquist_compliant: true
wave_0_complete: true
created: 2026-05-23
---

# Phase 1 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | stdlib `testing` + `net/http/httptest` + `go.uber.org/goleak` |
| **Config file** | none (standard Go test runner) |
| **Quick run command** | `go test ./...` |
| **Full suite command** | `go test -race ./...` |
| **Estimated runtime** | ~30 seconds (race build dominates) |

---

## Sampling Rate

- **After every task commit:** Run `go test ./...`
- **After every plan wave:** Run `go test -race ./...`
- **Before `/gsd:verify-work`:** `make lint && make test-race && make ci` all green
- **Max feedback latency:** ~30 seconds

---

## Per-Task Verification Map

> TDD-style tasks in plans 01-01 and 01-02 co-create test files alongside implementation (no separate Wave 0 phase required). Wave 0 markers updated to `✓ co-created` accordingly.

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 01-01 Task 1 | 01-01 | 1 | BLD-01 | — | `make build` produces runnable binary serving /health | smoke | `make build && ./bin/loop24-gateway & sleep 1; curl -sf localhost:11434/health; kill %1` | ✓ co-created | ⬜ pending |
| 01-01 Task 2 | 01-01 | 1 | BLD-01, TRST-01 | T-01-G204 | golangci-lint passes on scaffold; /health endpoint correct | lint + unit | `make lint` | ✓ co-created | ⬜ pending |
| 01-02 Task 1 | 01-02 | 2 | ACP-02, ACP-05 | — | id correlation under concurrent Prompt calls; session/update frames translate to canonical.Chunk | unit | `go test -race ./internal/acp/... -run "TestFramer\|TestDispatcher\|TestTranslate\|TestStream"` | ✓ co-created | ⬜ pending |
| 01-02 Task 2 | 01-02 | 2 | ACP-01, ACP-03, ACP-04, ACP-06, TRST-03 | T-02-01, T-02-02 | Subprocess spawns/terminates cleanly; initialize+session/new+session/set_model+ping implemented; auto-grant works; goroutine leak gate passes | unit + integration | `go test -race ./internal/acp/... -v` | ✓ co-created | ⬜ pending |
| 01-03 Task 1 | 01-03 | 3 | TRST-08 | T-03-01 | go-arch-lint package confirmed legitimate before install | checkpoint | `— (human verify)` | ✅ (checkpoint task) | ⬜ pending |
| 01-03 Task 2 | 01-03 | 3 | TRST-01, TRST-02, TRST-08 | T-03-02 | make lint exits 0; make ci (lint+test-race+govulncheck) exits 0; pre-commit hooks pass | lint + vuln + race | `make ci 2>&1 \| tail -40; echo "make ci exit: $?"` | ✓ co-created | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

**Cross-cutting goroutine-leak gate:**
- `internal/acp/testmain_test.go` → `goleak.VerifyTestMain` — catches ACP goroutine leaks across the entire `internal/acp` test package
- `internal/server` tests → `goleak.VerifyNone(t)` per-handler — catches server goroutine leaks

---

## Wave 0 Requirements

- [x] `internal/acp/testmain_test.go` — `goleak.VerifyTestMain` (covers ACP-01..06) — ✓ co-created in plan 01-02 Task 1
- [x] `internal/acp/framer_test.go` — NDJSON encode/decode correctness — ✓ co-created in plan 01-02 Task 1
- [x] `internal/acp/dispatcher_test.go` — id correlation + notification routing (ACP-02, ACP-04 unit) — ✓ co-created in plan 01-02 Task 1
- [x] `internal/acp/client_test.go` — spawn, Close(), Stream lifecycle (ACP-01, ACP-06) — ✓ co-created in plan 01-02 Task 2
- [x] `internal/acp/integration_test.go` — real `kiro-cli` round trip; auto-skip when binary not on PATH (ACP-03, ACP-04, ACP-05) — ✓ co-created in plan 01-02 Task 2
- [x] `internal/server/server_test.go` — `/health` JSON shape (D-12), middleware order, graceful shutdown — ✓ co-created in plan 01-01 Task 1
- [x] `internal/config/config_test.go` — `Load()` with env-var overrides — ✓ co-created in plan 01-01 Task 1
- [x] `internal/testutil/testutil.go` — `Logger(t)` helper (slog → t.Log) — ✓ co-created in plan 01-01 Task 1
- [x] `make ci` Makefile target — invokes `$(go env GOPATH)/bin/govulncheck ./...` (covers TRST-02) — ✓ in plan 01-01 Task 2
- [x] Framework install — `go get go.uber.org/goleak@v1.3.0 github.com/go-chi/chi/v5@v5.3.0` — ✓ in plan 01-02 Task 2

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Pre-commit hooks block bad commits | TRST-08 | Hook activation depends on local git state; `pre-commit run --all-files` can be automated but block-on-commit requires a real commit attempt | Stage a file with a hard-coded secret (test fixture string); attempt `git commit`; verify `gitleaks` blocks. Repeat with `go fmt`-broken file; verify `golangci-lint` blocks. |
| Wrapper scripts manage gateway lifecycle on macOS/Linux | D-20 | PID/log file paths are OS-specific; signal handling is shell-script driven | `./scripts/loop24 start`, `./scripts/loop24 status` (verify HTTP+PID), `./scripts/loop24 stop`, `./scripts/loop24 logs` |
| Wrapper scripts manage gateway lifecycle on Windows | D-20 | PowerShell `Start-Process` redirection cannot be tested from a macOS CI; needs Windows machine | `.\scripts\loop24.ps1 start`, `.\scripts\loop24.ps1 status`, `.\scripts\loop24.ps1 stop` — deferred to user on a Windows laptop |

---

## Validation Sign-Off

- [x] All tasks have automated verify or Wave 0 dependencies
- [x] Sampling continuity: no 3 consecutive tasks without automated verify
- [x] Wave 0 covers all MISSING references (covered by co-located TDD tasks in plans 01-01 and 01-02)
- [x] No watch-mode flags
- [x] Feedback latency < 60s
- [x] `nyquist_compliant: true` set in frontmatter

**Approval:** approved 2026-05-23
