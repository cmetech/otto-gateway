---
finding: T-2
severity: H
rel_id: REL-TRAY-02
status: confirmed
target_phase: 15
verified_at: 2026-06-11
---

# T-2: Windows Support Bundle Exits 1 When Gateway Is Down (REL-TRAY-02)

## Review Citation

> **[High] T-2: Windows support bundle fails exactly when the gateway is down — `exit 1` inside `Get-GatewayStatus` terminates the whole `support` run**
>
> Files: `scripts/otto-gw.ps1:584-593` (Get-GatewayStatus exits 1 on missing/stale PID), `scripts/otto-gw.ps1:1464` (Invoke-Support captures status).
>
> Failure scenario: `Invoke-Support` captures status via `try { Get-GatewayStatus 2>&1 | Out-String } catch {...}`, but PowerShell `exit` is script-terminating flow control — `try/catch` does not catch it. When the pidfile is missing (587) or stale (593), the whole script exits 1 mid-collection; the `finally` at 1645 deletes the staging tree. Gateway crashed → user opens tray → "Create Support Bundle…" to gather evidence → "Support Bundle Failed" with no useful stderr.

## Current-Source Check

Verified against current source (worktree at commit `3a72d03`):

- **`scripts/otto-gw.ps1:584-593` (Get-GatewayStatus):** Lines 584–598 show `Get-GatewayStatus` calls `exit 1` at lines 581 (no PID file: `Write-Host "otto-gateway: stopped" / exit 1`) and 593 (stale PID: `Write-Host "otto-gateway: stopped (stale PID)" / exit 1`). These are raw `exit` calls — not `throw` or `return` — and PowerShell `exit` is a process-level termination that `try/catch` cannot intercept. **Failure path intact.**

- **`scripts/otto-gw.ps1:1464` (Invoke-Support status capture):** Line 1464 shows:
  `$statusOut = try { Get-GatewayStatus 2>&1 | Out-String } catch { "(status failed: $($_.Exception.Message))" }`
  The `try/catch` wraps `Get-GatewayStatus` but only catches thrown exceptions — not `exit`. When `Get-GatewayStatus` calls `exit 1`, the entire `otto-gw.ps1` process exits before reaching the `catch` or `finally` block. **Failure path intact.**

The bash wrapper correctly solved this via a subshell capture (`otto-gw:1838-1840`) but the PowerShell port did not carry the fix forward.

## Evidence

Manual reproducer: `tests/reliability/manual/REL-TRAY-02-repro.ps1`

The PowerShell script invokes `scripts/otto-gw.ps1 support` with the gateway stopped, captures stdout/stderr and the exit code, and reports whether the pre-fix behavior (exit 1, no bundle) or post-fix behavior (exit 0, bundle with `unreachable:` sentinel) was observed.

Discoverability stub: `cmd/otto-tray/regression_rel_tray_02_test.go` — `TestRegression_REL_TRAY_02_WindowsBundleExitOne` skips with pointer to the manual script.

## Verdict

**confirmed** — `Get-GatewayStatus` uses `exit 1` (not `return` or `throw`) which terminates the entire script process regardless of the surrounding `try/catch`. The `Invoke-Support` wrapper at line 1464 cannot intercept it. The fix (replace `exit 1` with `return` in `Get-GatewayStatus`, or capture via a separate subprocess invocation) lands in Phase 15. Failure is guaranteed every time a user attempts support-bundle creation while the gateway is down — exactly the diagnostic scenario the feature exists for.
