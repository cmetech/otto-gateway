---
quick_id: 260531-oax
title: Add -Trace switch to otto-gw.ps1 mirroring bash --trace
status: complete
date: 2026-05-31
commit: 1d68a7f
---

# Quick Task 260531-oax: Summary

## What changed

`scripts/otto-gw.ps1` gained a `-Trace` switch that sets both `$env:DEBUG=true`
and `$env:CHAT_TRACE=true` for `start | restart | run` — Windows parity with the
bash `--trace` flag from quick-260531-o4s.

Three insertions (commit `1d68a7f`):
1. `param()` (line 34): `[switch]$Trace`
2. `Apply-CliFlags` (line 142): `if ($Trace) { $env:DEBUG = 'true'; $env:CHAT_TRACE = 'true' }`
3. `Show-Usage` (line 768): `-Trace` doc line under Gateway config flags

## .bat: no change needed

`scripts/otto-gw.bat` is a pure pass-through
(`powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0otto-gw.ps1" %*`),
so `otto-gw.bat start -Trace` forwards `-Trace` to the `.ps1` automatically.

## Design notes

- `-Trace` is NOT a PowerShell reserved common parameter (unlike `-Debug`), so it
  is declared as a normal `[switch]` and tested directly — cleaner than the
  `$DebugRequested = $PSBoundParameters.ContainsKey('Debug')` workaround `-Debug`
  requires.
- No `ENABLED_HOOKS` plumbing: `internal/config/config.go` auto-prepends
  `ChatTraceHook` at runtime when `CHAT_TRACE=true`.

## Verification

- grep confirms all three insertions present.
- **Not verified:** PowerShell parse check — `pwsh` is not installed on the macOS
  dev box. The change is mechanical and low-risk, but a Windows operator should
  confirm `otto-gw.ps1 start -Trace` on first use.
