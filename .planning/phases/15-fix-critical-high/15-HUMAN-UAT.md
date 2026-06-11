---
status: partial
phase: 15-fix-critical-high
source: [15-VERIFICATION.md]
started: 2026-06-11T00:00:00Z
updated: 2026-06-11T00:00:00Z
---

## Current Test

[awaiting human testing]

## Tests

### 1. REL-TRAY-02 — Windows Support Bundle When Gateway Is Stopped
expected: Running `scripts/otto-gw.ps1 support` on Windows with the gateway stopped produces a complete non-empty `.zip` support bundle. The bundle includes `health/status.txt` with a stopped / no-pid-file message. The command does NOT exit early or throw an uncaught exception. Confirms `Get-GatewayStatus` refactor (exit-1 → `[pscustomobject]`) end-to-end.
repro: `tests/reliability/manual/REL-TRAY-02-repro.ps1` (Windows only)
result: [pending]

### 2. REL-TRAY-03 — macOS Tray Gateway-Death Visibility
expected: With the tray running on a macOS GUI session, `kill -9 <gateway-pid>` causes the menu-bar icon to change state (stopped / error variant) and the tooltip to update within ~3–6 s (one poll interval). Change is visible without restarting the tray. Confirms `applyState` → `setIconForState` + `SetTooltip` wiring is reachable from the FSM.
repro: `tests/reliability/manual/REL-TRAY-03-repro.sh` (macOS GUI only)
result: [pending]

## Summary

total: 2
passed: 0
issues: 0
pending: 2
skipped: 0
blocked: 0

## Gaps
