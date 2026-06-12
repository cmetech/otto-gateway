---
phase: 18-reliability-long-tail
fixed_at: 2026-06-12T00:00:00Z
review_path: .planning/phases/18-reliability-long-tail/18-REVIEW.md
iteration: 2
findings_in_scope: 2
fixed: 2
skipped: 0
status: all_fixed
---

# Phase 18: Code Review Fix Report (Iteration 2)

**Fixed at:** 2026-06-12T00:00:00Z
**Source review:** `.planning/phases/18-reliability-long-tail/18-REVIEW.md`
**Iteration:** 2

**Summary:**
- Findings in scope: 2 (WR-08, WR-09)
- Fixed: 2
- Skipped: 0
- Out of scope (INFO, severity-skipped): IN-01, IN-02, IN-03
- Deferred (severity-skipped: deferred-per-d-18-04): WR-01

## Fixed Issues

### WR-08: PowerShell sentinel write silently falls back to `$env:TEMP` when both `$HOME` and `$USERPROFILE` are unset

**Files modified:** `scripts/otto-gw.ps1`
**Commit:** `50c18e5`
**Applied fix:** Removed the `$env:TEMP` fallback from `Get-ConfigErrorSentinelPath`. Now returns `$null` when both `$env:HOME` and `$env:USERPROFILE` are unset, mirroring the bash sibling's WR-06 refusal (`config_error_sentinel_path` non-zero exit). Emits a stderr WARN via `[Console]::Error.WriteLine` so the operator's diagnostic path is preserved (parity with bash's existing stderr WARN). `Clear-ConfigErrorSentinel` and `Write-ConfigErrorSentinel` both short-circuit on `$null`. Comments updated to cite WR-08 and explain the cross-platform symmetry contract.

**Verification:**
- Tier 1 (re-read): confirmed fix text present, surrounding code intact. The `$env:TEMP` fallback is gone from the actual code path (only mentioned in explanatory comments and the WARN string).
- Tier 2 (syntax check): pwsh not installed on macOS dev box — regression-test gap documented as `skipped: needs-platform-runner`. Source-level pattern check (`grep -n 'env:TEMP'`) confirmed no remaining fallback code path. The symmetry assertion holds: `Get-ConfigErrorSentinelPath` returns `$null` in exactly the conditions where bash's `config_error_sentinel_path` returns 1, and both callers (clear/write) short-circuit equivalently.

### WR-09: WR-04 rune-boundary truncation may discard up to maxLineBytes-1 bytes silently

**Files modified:** `internal/acp/client.go`, `internal/acp/regression_rel_obsv_03_test.go`
**Commit:** `61aa985`
**Applied fix:** Compute `droppedBytes = originalLen - n` after the UTF-8 walk-back. When `droppedBytes > 0` (always true when the cap fires), emit the slog.Warn with two new fields: `truncated: true` and `dropped_bytes: <N>`. The base D-18-04 field set (`worker_pid`, `line`) is preserved byte-exact; the new fields are additive. Non-truncated lines keep the original 2-field shape so operators pattern-matching on field cardinality are unaffected.

**Test coverage added:**
- `A5_truncation_telemetry_wr_09`: pipes ~2MB of `X` (no newline) then a newline through the drain loop, asserts the resulting WARN has `truncated == true`, `dropped_bytes >= 1_000_000`, AND that the base `worker_pid`/`line` fields remain present (so D-18-04 contract is not regressed).
- `A6_no_truncation_no_telemetry_fields`: pipes a short line and asserts neither `truncated` nor `dropped_bytes` appear on the record.

**Verification:**
- Tier 1 (re-read): confirmed fix text present, walk-back loop intact, field set additive.
- Tier 2 (Go syntax/vet/test): `gofmt -l .` clean; `go vet ./...` clean; `go test -race -run TestRegression_REL_OBSV_03 ./internal/acp/` passes (2.0s); full `go test -race ./internal/acp/` passes (8.5s). No regression in A1..A4 (pre-existing sub-tests for D-18-04).

## Severity-Skipped Issues

### WR-01: bufio.Reader.ReadString unbounded accumulation in stderrDrainLoop

**File:** `internal/acp/client.go:392-439`
**Reason:** `severity-skipped: deferred-per-d-18-04`
**Original issue:** `ReadString('\n')` accumulates an unbounded internal buffer when stderr has no `\n` terminator. The 1MB cap and the WR-04/WR-09 UTF-8 truncation fire AFTER the ReadString returns — by which point the buffer has already grown to whatever the producer pushed.
**Disposition:** CONTEXT.md D-18-04 locks the `bufio.Reader.ReadString('\n')` pattern as the Wave 0 decision. Switching to a bounded reader (`io.LimitReader` wrapper or custom bounded-buffer reader) is a design change requiring an ADR, not a fix-pass mechanical change. Per re-review (and the iteration-1 disposition that surfaces this under `open_followups`), the deferral is preserved. The threat surface — a compromised kiro-cli flooding stderr to RAM-exhaust the gateway — is unchanged vs. pre-fix; this is not a regression introduced by iteration 1 or 2. Recommend a v1.10.4 dedicated phase + ADR.

### IN-01: `clear_config_error_sentinel` race window unchanged by WR-05

**File:** `scripts/otto-gw:226-230`
**Reason:** `severity-skipped: out-of-scope-fix_scope=critical_warning`
**Original issue:** Informational note that `rm -f` in `clear_config_error_sentinel` is already atomic on POSIX; flagged only to prevent later confusion about WR-05 asymmetry. No fix needed.

### IN-02: PowerShell Import-DotEnv successful parse of overrides file masks a parse error from a prior .env file

**File:** `scripts/otto-gw.ps1:321-327`
**Reason:** `severity-skipped: out-of-scope-fix_scope=critical_warning`
**Original issue:** When `.otto-gw.env` parse fails but `.otto-gw.overrides.env` parses cleanly, the sentinel from step 1 is cleared by step 2. Same shape on the bash side. May be intentional (overrides is the final word) but undocumented. Recommend docstring clarification OR cumulative-state tracking — both are design-change conversations, not fix-pass items.

### IN-03: WR-04 panics not gated by `cap`-aware guard if `len(trimmed) == maxLineBytes`

**File:** `internal/acp/client.go:406-422`
**Reason:** `severity-skipped: out-of-scope-fix_scope=critical_warning`
**Original issue:** Defense-in-depth recommendation to add `if n >= len(trimmed) { n = len(trimmed) - 1 }` before the walk-back loop. Current code is correct (gate is `> maxLineBytes` strictly greater); the invariant is only brittle to a future `>=` refactor. Not a present-day defect.

## Iteration 2 Verification Summary

- `gofmt -l .` — clean (no diffs)
- `go vet ./...` — clean (no warnings)
- `go test -race ./internal/acp/` — pass (8.5s, all sub-tests including A5/A6 telemetry coverage)
- Iteration-1 invariants spot-checked against the worktree:
  - CR-01 `t.running=false` + `t.cancelRun=nil` under `t.mu` in `internal/admin/tail.go` defer-recover — untouched
  - CR-02/CR-03 `$script:firstError` scoping in `scripts/otto-gw.ps1` — untouched
  - WR-02 `..` segment check in `internal/config/config.go` — untouched
  - WR-03 bind-probe `Close()` error surface in `internal/config/config.go` — untouched
  - WR-04 UTF-8 walk-back EXTENDED (not replaced) in `internal/acp/client.go` — new telemetry fields are additive; rune-boundary slice logic preserved
  - WR-05 atomic sentinel writes in both `scripts/otto-gw` and `scripts/otto-gw.ps1` — untouched
  - WR-06 bash `$HOME unset` refusal in `scripts/otto-gw` — untouched (WR-08 mirrors this on the PowerShell side)
  - WR-07 `previous_pid=0` doc-block in `internal/pool/pool.go` — untouched

---

_Fixed: 2026-06-12_
_Fixer: Claude (gsd-code-fixer)_
_Iteration: 2_
