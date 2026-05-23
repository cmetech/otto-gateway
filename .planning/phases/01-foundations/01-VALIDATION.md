---
phase: 1
slug: foundations
status: draft
nyquist_compliant: false
wave_0_complete: false
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

> Filled by planner once tasks exist (each PLAN.md task references back here via `<acceptance_criteria>`).

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| TBD     | TBD  | TBD  | ACP-01      | —          | Subprocess spawns and terminates cleanly | unit | `go test -race ./internal/acp/... -run TestNew` | ❌ W0 | ⬜ pending |
| TBD     | TBD  | TBD  | ACP-02      | —          | id correlation under concurrent Prompt calls | unit | `go test -race ./internal/acp/... -run TestDispatcher` | ❌ W0 | ⬜ pending |
| TBD     | TBD  | TBD  | ACP-03      | —          | initialize + session/new + ping over real kiro-cli | integration | `go test -race ./internal/acp/... -run TestIntegration` | ❌ W0 | ⬜ pending |
| TBD     | TBD  | TBD  | ACP-04      | —          | session/request_permission auto-granted; kiro-cli unblocks | integration | `go test -race ./internal/acp/... -run TestAutoGrant` | ❌ W0 | ⬜ pending |
| TBD     | TBD  | TBD  | ACP-05      | —          | session/update frames translate to canonical.Chunk | unit | `go test ./internal/acp/... -run TestTranslateUpdate` | ❌ W0 | ⬜ pending |
| TBD     | TBD  | TBD  | ACP-06      | —          | Ping heartbeat goroutine exits cleanly on Close() | unit | `go test -race ./internal/acp/... -run TestPingShutdown` | ❌ W0 | ⬜ pending |
| TBD     | TBD  | TBD  | BLD-01      | —          | `make build` produces runnable binary serving /health | smoke | `make build && ./bin/loop24-gateway &; sleep 1; curl -sf localhost:11434/health; kill %1` | ❌ W0 | ⬜ pending |
| TBD     | TBD  | TBD  | TRST-01     | T-01-G204  | golangci-lint passes on scaffold | lint | `make lint` | ✅ (.golangci.yml exists) | ⬜ pending |
| TBD     | TBD  | TBD  | TRST-02     | —          | govulncheck passes | vuln | `make ci` | ❌ W0 (ci target missing) | ⬜ pending |
| TBD     | TBD  | TBD  | TRST-03     | —          | `go test -race ./...` passes | race | `make test-race` | ✅ (target exists; tests TBD) | ⬜ pending |
| TBD     | TBD  | TBD  | TRST-08     | —          | Pre-commit hooks block bad commits | manual | `pre-commit run --all-files` | ✅ (.pre-commit-config.yaml exists) | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

**Cross-cutting goroutine-leak gate:**
- `internal/acp/testmain_test.go` → `goleak.VerifyTestMain` — catches ACP goroutine leaks across the entire `internal/acp` test package
- `internal/server` tests → `goleak.VerifyNone(t)` per-handler — catches server goroutine leaks

---

## Wave 0 Requirements

- [ ] `internal/acp/testmain_test.go` — `goleak.VerifyTestMain` (covers ACP-01..06)
- [ ] `internal/acp/framer_test.go` — NDJSON encode/decode correctness
- [ ] `internal/acp/dispatcher_test.go` — id correlation + notification routing (ACP-02, ACP-04 unit)
- [ ] `internal/acp/client_test.go` — spawn, Close(), Stream lifecycle (ACP-01, ACP-06)
- [ ] `internal/acp/integration_test.go` — real `kiro-cli` round trip; auto-skip when binary not on PATH (ACP-03, ACP-04, ACP-05)
- [ ] `internal/server/server_test.go` — `/health` JSON shape (D-12), middleware order, graceful shutdown
- [ ] `internal/config/config_test.go` — `Load()` with env-var overrides
- [ ] `internal/testutil/testutil.go` — `Logger(t)` helper (slog → t.Log)
- [ ] `make ci` Makefile target — invokes `$(go env GOPATH)/bin/govulncheck ./...` (covers TRST-02)
- [ ] Framework install — `go get go.uber.org/goleak@v1.3.0 github.com/go-chi/chi/v5@v5.3.0`

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Pre-commit hooks block bad commits | TRST-08 | Hook activation depends on local git state; `pre-commit run --all-files` can be automated but block-on-commit requires a real commit attempt | Stage a file with a hard-coded secret (test fixture string); attempt `git commit`; verify `gitleaks` blocks. Repeat with `go fmt`-broken file; verify `golangci-lint` blocks. |
| Wrapper scripts manage gateway lifecycle on macOS/Linux | D-20 | PID/log file paths are OS-specific; signal handling is shell-script driven | `./scripts/loop24 start`, `./scripts/loop24 status` (verify HTTP+PID), `./scripts/loop24 stop`, `./scripts/loop24 logs` |
| Wrapper scripts manage gateway lifecycle on Windows | D-20 | PowerShell `Start-Process` redirection cannot be tested from a macOS CI; needs Windows machine | `.\scripts\loop24.ps1 start`, `.\scripts\loop24.ps1 status`, `.\scripts\loop24.ps1 stop` — deferred to user on a Windows laptop |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 60s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
