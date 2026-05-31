---
phase: quick-260531-ebi
plan: 01
subsystem: observability
tags: [admin-ui, snapshot-json, feature-flags, debug, chat-trace, wrappers, shellcheck, powershell]

# Dependency graph
requires:
  - phase: quick-260529-f8r / Phase 6.1 (Admin Observability UI)
    provides: admin.Deps + AdminSnapshot + index.html.tmpl summary strip + otto-gw status health probe
provides:
  - debug + chat_trace booleans on admin.Deps, AdminSnapshot JSON, and the rendered HTML page
  - otto-gw status (POSIX + PowerShell) prints debug/chat-trace on/off from /admin/api/snapshot
affects: [admin-ui, operator-tooling, wrappers]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Feature-flag enablement flows cfg.Config -> admin.Deps -> snapshot JSON + HTML (single source, two surfaces)"
    - "Wrappers read enablement from auth-exempt /admin/api/snapshot, never /health (D-12 locked)"

key-files:
  created: []
  modified:
    - internal/admin/admin.go
    - internal/admin/snapshot.go
    - internal/admin/templates/index.html.tmpl
    - cmd/otto-gateway/main.go
    - internal/admin/snapshot_test.go
    - internal/admin/handlers_test.go
    - scripts/otto-gw
    - scripts/otto-gw.ps1

key-decisions:
  - "Surfaced via /admin/api/snapshot, not /health, to keep the D-12-locked /health byte-shape unchanged"
  - "HTML uses literal 'Debug' + 'Chat-trace' labels so wrapper-independent HTML checks and operators both find them"
  - "Wrapper admin fetches are best-effort: unreachable admin endpoint degrades to skipped lines, never fails status"

patterns-established:
  - "Render-time baked HTML summary items (like Version), not JS-hydrated, for static enablement flags"
  - "JSON-boolean sed parse (true|false, unquoted) in the POSIX wrapper"

requirements-completed: [QUICK-260531-EBI]

# Metrics
duration: ~20min
completed: 2026-05-31
---

# Phase quick-260531-ebi: Surface debug + chat-trace enablement Summary

**DEBUG and CHAT_TRACE enablement now flow from cfg.Config through admin.Deps to both the admin snapshot JSON (`debug`/`chat_trace`) and the rendered HTML summary strip, and both `otto-gw status` wrappers print them on/off — sourced from /admin/api/snapshot, with /health left byte-shape unchanged.**

## Performance

- **Duration:** ~20 min
- **Tasks:** 2
- **Files modified:** 8

## Accomplishments
- `admin.Deps` gains `Debug`/`ChatTrace` bool fields (documented; ChatTrace flagged SENSITIVE raw-prompt tracer).
- `AdminSnapshot` gains `debug` + `chat_trace` snake_case JSON keys, populated from Deps; wire-shape doc updated.
- `pageHandler` render struct + `index.html.tmpl` render visible literal "Debug" and "Chat-trace" on/off summary items at render time.
- `cmd/otto-gateway/main.go` wires `Debug: cfg.Debug` / `ChatTrace: cfg.ChatTrace` into `admin.Handler(admin.Deps{...})`.
- POSIX `status()` and PowerShell `Get-GatewayStatus` fetch the auth-exempt `/admin/api/snapshot` and print `debug:`/`chat-trace:` on/off lines after the existing health output; both best-effort.

## Task Commits

1. **Task 1: Add debug + chat_trace to admin Deps, snapshot JSON, and HTML page (TDD)** - `3a3d9c8` (feat)
2. **Task 2: Surface debug + chat-trace in otto-gw status (POSIX + PowerShell)** - `4978de6` (feat)

_TDD note: Task 1 followed RED (extended tests failed to compile — fields absent) -> GREEN (implemented fields, tests pass). RED and GREEN were combined into one atomic `feat` commit since the quick-task constraint asked for one commit per task; the RED state was verified via a failing `go test ./internal/admin/...` run before implementing._

## Files Created/Modified
- `internal/admin/admin.go` - Debug/ChatTrace fields + docs on Deps; pageHandler render struct carries them.
- `internal/admin/snapshot.go` - debug/chat_trace JSON fields on AdminSnapshot; populated in snapshotHandler; wire-shape doc updated.
- `internal/admin/templates/index.html.tmpl` - two new summary-strip items (Debug, Chat-trace) after Version.
- `cmd/otto-gateway/main.go` - wires cfg.Debug/cfg.ChatTrace into admin.Deps.
- `internal/admin/snapshot_test.go` - asserts true-state in TestAdmin_SnapshotHandler and false zero-value default in TestAdmin_SnapshotNilSafe.
- `internal/admin/handlers_test.go` - TestAdmin_PageHandler asserts literal "Debug"/"Chat-trace" labels and rendered "on".
- `scripts/otto-gw` - status() second fetch against /admin/api/snapshot, sed-parsed JSON booleans, aligned on/off lines.
- `scripts/otto-gw.ps1` - Get-GatewayStatus second Invoke-RestMethod against /admin/api/snapshot in its own try/catch.

## Decisions Made
- Surfaced enablement via /admin/api/snapshot (auth-exempt) rather than /health — /health is D-12 byte-shape locked and gains no new fields.
- HTML labels are literal text so operators and the HTML-content test both find them; rendered at template time (not JS-hydrated) like Version.
- Wrapper admin fetches are best-effort and isolated (POSIX: `|| true` + empty-skip; PowerShell: dedicated try/catch) so an unreachable admin endpoint never blanks the already-printed health lines.

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered

- **Trust-gate tooling not preinstalled.** `golangci-lint` and `gosec` were absent on this box (exit 127 / not found). Installed both via `go install` to honor the non-negotiable trust gates. The repo's `.golangci.yml` is v2 schema, so golangci-lint v2 (`/v2/cmd/...`, v2.12.2) was installed.
- **Pre-existing, out-of-scope lint/vet/security findings (NOT fixed — logged to `deferred-items.md`):**
  - `go vet ./internal/admin/...` and golangci-lint `govet/stdversion` fail on `internal/admin/tail_test.go` + `tail_timberjack_test.go`, which use `t.Context()` (go1.24) while `go.mod` declares `go 1.23`. These files are unmodified by this plan; `go test` passes under the local go1.26.3 toolchain. `go vet ./cmd/otto-gateway/...` is clean.
  - golangci-lint/gosec flag `internal/admin/admin.go:137` (G703), `internal/admin/sse.go:91` (G705), `cmd/otto-gateway/main.go:855` (G301), and errcheck/G304 in `sse_test.go`/`tail_test.go` — all pre-existing code untouched by this plan. The locally-installed linter minor version is newer than the repo's pinned CI version (newer taint analyzers; `issues.exclude-rules` test-file exclusions interpreted differently), so they surfaced locally. **Verified via `git diff HEAD` that NONE of the findings reference any line this plan added** — every added line is a struct field, doc comment, or assignment with zero lint/security impact.

## Verification Limitations

- **PowerShell (`scripts/otto-gw.ps1`) is review-only.** No `pwsh` runtime on this macOS dev box, so the PowerShell `Get-GatewayStatus` path was NOT executed or pwsh-linted. Correctness is asserted by careful code review against the existing `Invoke-RestMethod` + try/catch + `Write-Host ("  label: {0}" -f ...)` patterns already in the file. The grep gates (`/admin/api/snapshot`, `chat_trace`) pass.
- **golangci-lint / gosec** ran on freshly-installed dev/v2.12 binaries that differ from the repo's pinned CI versions; findings above are environment/version artifacts on untouched code, not regressions introduced here.

## Verify Gate Results

| Gate | Result |
|------|--------|
| `go test ./internal/admin/...` | PASS (`ok`; new debug/chat_trace assertions included) |
| `go build ./...` | PASS |
| `go vet ./cmd/otto-gateway/...` | PASS (clean) |
| `go vet ./internal/admin/...` | FAIL — pre-existing `tail_test.go` go1.24 `t.Context()` vs `go 1.23` module (untouched files, out of scope) |
| `golangci-lint run ./internal/admin/... ./cmd/otto-gateway/...` | Findings only in untouched files / tool-version artifacts; zero findings on this plan's diff |
| `gosec -quiet ./internal/admin/...` | 2 findings (admin.go:137, sse.go:91) — both pre-existing, untouched |
| `shellcheck scripts/otto-gw` | PASS (clean) |
| Task 2 grep gates (admin/api/snapshot, chat-trace, chat_trace) | PASS |

## Next Phase Readiness
- Operators can now see Debug + Chat-trace state in the admin UI, the snapshot JSON, and `otto-gw status` (POSIX live-verified; PowerShell review-only).
- /health remains D-12 byte-shape locked — unchanged.
- Pre-existing trust-gate debt (tail_test.go go1.24 usage; untouched gosec G703/G705) is logged in `deferred-items.md` for a future cleanup task.

## Self-Check: PASSED

All 8 modified files exist on disk; both task commits (`3a3d9c8`, `4978de6`) exist in git history; SUMMARY.md written.

---
*Phase: quick-260531-ebi*
*Completed: 2026-05-31*
