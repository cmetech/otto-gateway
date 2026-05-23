---
phase: 01-foundations
plan: "01"
subsystem: scaffold
tags: [go, chi, slog, config, canonical, server, health, middleware, scripts, makefile]
dependency_graph:
  requires: []
  provides:
    - internal/canonical (Chunk + Block types)
    - internal/config (Config + Load())
    - internal/version (Version + Commit())
    - internal/testutil (Logger(t))
    - internal/server (Server, Run, RunUntilSignal, healthHandler, versionHandler, accessLog)
    - cmd/loop24-gateway/main.go (binary entrypoint)
    - scripts/loop24 (POSIX lifecycle)
    - scripts/loop24.ps1 (PowerShell lifecycle)
    - Makefile ci/start/stop/status targets
  affects: []
tech_stack:
  added:
    - github.com/go-chi/chi/v5 v5.3.0 (HTTP routing + middleware)
    - go.uber.org/goleak v1.3.0 (goroutine leak detection in tests)
  patterns:
    - slog explicit injection (D-15 no slog.SetDefault)
    - config struct constructor (D-05 pattern)
    - discriminated-union Chunk/Block types (D-04 leaf package)
    - Run(ctx) + RunUntilSignal(ctx) testability split (REVIEW FIX Codex MEDIUM)
    - stop() bounded wait loop for restart-race elimination (REVIEW FIX Codex MEDIUM)
    - PowerShell Get-Logs parallel background jobs (REVIEW FIX Gemini MEDIUM)
key_files:
  created:
    - internal/canonical/chunk.go
    - internal/config/config.go
    - internal/config/config_test.go
    - internal/version/version.go
    - internal/testutil/testutil.go
    - internal/server/server.go
    - internal/server/health.go
    - internal/server/middleware.go
    - internal/server/server_test.go
    - cmd/loop24-gateway/main.go
    - scripts/loop24
    - scripts/loop24.ps1
  modified:
    - Makefile
    - .gitignore
decisions:
  - "ReadHeaderTimeout: 10s added to http.Server (gosec G112 Slowloris mitigation)"
  - "httptest.NewRequestWithContext used in tests (noctx linter compliance)"
  - "getEnvInt removed from config (unused in Phase 1, flagged by unused linter)"
  - ".gitignore exception !/cmd/loop24-gateway/ added (loop24-gateway pattern was ignoring source dir)"
  - "ServeHTTP exposed on Server struct to enable direct handler testing without a live listener"
metrics:
  duration_minutes: 10
  completed_date: "2026-05-23"
  tasks_completed: 3
  tasks_total: 3
  files_created: 12
  files_modified: 2
---

# Phase 01 Plan 01: Walking Skeleton Summary

**One-liner:** Chi HTTP server with slog injection, D-12 health endpoint, config env-loading with error aggregation, POSIX+PowerShell lifecycle scripts, and testable Run(ctx)/RunUntilSignal(ctx) split.

## What Was Built

### Task 1: Foundational packages

- **internal/canonical/chunk.go** — Discriminated-union `Chunk` type (Text/Thought/ToolCall/Plan) for ACP output and `Block` type (Text/ResourceLink) for prompt input. Leaf package — imports nothing under `internal/`. All exported symbols have godoc.

- **internal/config/config.go** — `Config` struct + `Load() (Config, error)`. Error aggregation pattern: `getEnvBool` and `getEnvDuration` return `(T, error)`. `PING_INTERVAL=abc` causes `Load()` to return a non-nil error (fail-loud, not silent fallback). Node-compat env var names (`HTTP_ADDR`, `KIRO_CMD`, `KIRO_ARGS`, `KIRO_CWD`, `DEBUG`, `PING_INTERVAL`). `LogLevel()` method. `strings.TrimSpace` on all reads (Windows DEBUG trailing-space fix).

- **internal/version/version.go** — `Version` var settable via `-X loop24-gateway/internal/version.Version`. `Commit()` using `debug.ReadBuildInfo()`.

- **internal/testutil/testutil.go** — `Logger(t)` routes slog JSON to `t.Log` via a small `testWriter`. Zero external deps.

### Task 2: HTTP server

- **internal/server/server.go** — `Server` struct with `Run(ctx context.Context) error` (testable — cancel ctx to drive shutdown) and `RunUntilSignal(ctx)` thin signal wrapper. `ServeHTTP` exposed for direct handler testing. `ReadHeaderTimeout: 10s` (Slowloris mitigation). Middleware order: `RequestID → Recoverer → accessLog`.

- **internal/server/health.go** — `HealthResponse`, `PoolStats`, `SessionStats`, `EmbeddingStats` types with D-12 locked JSON field names. Phase 1 health handler returns 200 with zero sub-stats.

- **internal/server/middleware.go** — `accessLog(logger)` middleware; `loggerKey{}` private context key; `LoggerFromCtx()` exported helper. Uses `middleware.GetReqID` + `middleware.NewWrapResponseWriter`.

- **internal/server/server_test.go** — httptest-based handler tests for `/health` (all six D-12 keys), `/api/version`, middleware chain order, `Run(ctx)` context cancel. `goleak.VerifyNone(t)` on all tests. `httptest.NewRequestWithContext` used (`noctx` linter compliance).

### Task 3: Binary, Makefile, scripts

- **cmd/loop24-gateway/main.go** — config → logger → `server.New` → `RunUntilSignal`. No `slog.SetDefault` (D-15). No lifecycle subcommands (D-22).

- **Makefile** — LDFLAGS fixed to `-X loop24-gateway/internal/version.Version`. `ci`, `start`, `stop`, `status` targets added.

- **scripts/loop24** — POSIX lifecycle with `stop()` bounded wait loop (max 10s) before PID removal — eliminates the restart race. Passes shellcheck.

- **scripts/loop24.ps1** — PowerShell lifecycle. `Start-Process -NoNewWindow -PassThru`. `Get-Logs` tails both `$LogFile` and `$LogErrFile` via two `Start-Job` background jobs.

## Verification Results

| Check | Result |
|-------|--------|
| `make build` | PASS — bin/loop24-gateway -rwxr-xr-x |
| `GET /health` D-12 JSON shape | PASS — all 6 keys present, correct types |
| `GET /api/version` | PASS — version field present |
| `go test -race ./internal/server/...` | PASS |
| `go test -race ./internal/config/...` | PASS |
| `Run(ctx)` returns on cancel | PASS — 50ms, no hang |
| `config.Load()` error on PING_INTERVAL=abc | PASS |
| `golangci-lint` | PASS — zero findings |
| `shellcheck scripts/loop24` | PASS |
| Binary serves /health on custom port | PASS — D-12 JSON confirmed |

## Commits

| Task | Commit | Description |
|------|--------|-------------|
| 1 | 312fc98 | feat(01-01): scaffold foundational packages |
| 2 | 2f8f26e | feat(01-01): HTTP server with chi router, health handler, and middleware |
| 3 | c7d8227 | feat(01-01): wire main.go, extend Makefile, and write wrapper scripts |

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] .gitignore pattern `loop24-gateway` silently ignored cmd/loop24-gateway/**
- **Found during:** Task 3 (cmd/loop24-gateway/main.go not appearing in git status)
- **Issue:** The `.gitignore` entry `loop24-gateway` (intended to exclude the compiled binary) also matched the Go source directory `cmd/loop24-gateway/`, causing `git status` to silently ignore all source files there.
- **Fix:** Added `!/cmd/loop24-gateway/` negation rule to `.gitignore`
- **Files modified:** `.gitignore`
- **Commit:** c7d8227

**2. [Rule 2 - Missing Critical Functionality] ReadHeaderTimeout not set in http.Server**
- **Found during:** Task 2 (golangci-lint pre-commit hook flagged G112)
- **Issue:** `http.Server` without `ReadHeaderTimeout` is vulnerable to Slowloris attacks (gosec G112)
- **Fix:** Added `ReadHeaderTimeout: 10 * time.Second` to the `http.Server` struct in `server.Run()`
- **Files modified:** `internal/server/server.go`
- **Commit:** 2f8f26e

**3. [Rule 1 - Bug] noctx linter: httptest.NewRequest must use context variant**
- **Found during:** Task 2 (golangci-lint pre-commit hook)
- **Issue:** `httptest.NewRequest` without context is flagged by the `noctx` linter
- **Fix:** Replaced all `httptest.NewRequest` calls with `httptest.NewRequestWithContext(context.Background(), ...)`
- **Files modified:** `internal/server/server_test.go`
- **Commit:** 2f8f26e

**4. [Rule 1 - Bug] getEnvInt unused in Phase 1**
- **Found during:** Task 1 (golangci-lint pre-commit hook flagged `unused`)
- **Issue:** `getEnvInt` was written proactively for future phases but the `unused` linter flagged it since no Phase 1 code calls it
- **Fix:** Removed `getEnvInt` from `internal/config/config.go`. Will be added when a Phase 2+ caller needs it.
- **Files modified:** `internal/config/config.go`
- **Commit:** 312fc98

**5. [Rule 2 - Missing Critical Functionality] Server.ServeHTTP not exposed**
- **Found during:** Task 2 test writing
- **Issue:** Tests needed to call handlers directly via `httptest.NewRecorder` without starting a live listener. `Server` had no `ServeHTTP` method exposed.
- **Fix:** Added `ServeHTTP(w http.ResponseWriter, r *http.Request)` method that delegates to `s.router.ServeHTTP`
- **Files modified:** `internal/server/server.go`
- **Commit:** 2f8f26e

**6. [Rule 1 - Bug] t.Parallel() incompatible with t.Setenv in Go 1.26+**
- **Found during:** Task 1 first test run
- **Issue:** Go 1.26 enforces that tests using `t.Setenv` cannot also call `t.Parallel()`. Tests panicked at runtime.
- **Fix:** Removed `t.Parallel()` from all config tests that use `t.Setenv`. Tests that don't use `t.Setenv` (TestLoadDefaults, TestLogLevel) keep `t.Parallel()`.
- **Files modified:** `internal/config/config_test.go`
- **Commit:** 312fc98

## Known Stubs

None. All Phase 1 handlers return real data:
- `/health`: real version (ldflags), real uptime (`time.Since(s.start).Seconds()`), zero sub-stats are intentional per D-12 (pool/sessions/embeddings populated in Phases 5/7)
- `/api/version`: real version from `version.Version`, commit from `version.Commit()`

## Threat Flags

No new security surfaces beyond the plan's threat model. `ReadHeaderTimeout` was added per gosec G112 as a Rule 2 deviation (missing critical mitigation).

## Self-Check: PASSED

Files confirmed to exist:
- internal/canonical/chunk.go: FOUND
- internal/config/config.go: FOUND
- internal/config/config_test.go: FOUND
- internal/version/version.go: FOUND
- internal/testutil/testutil.go: FOUND
- internal/server/server.go: FOUND
- internal/server/health.go: FOUND
- internal/server/middleware.go: FOUND
- internal/server/server_test.go: FOUND
- cmd/loop24-gateway/main.go: FOUND
- scripts/loop24: FOUND
- scripts/loop24.ps1: FOUND

Commits confirmed to exist:
- 312fc98: FOUND
- 2f8f26e: FOUND
- c7d8227: FOUND
