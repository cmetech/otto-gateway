---
quick_id: 260531-oax
title: Add -Trace switch to otto-gw.ps1 mirroring bash --trace
status: complete
date: 2026-05-31
---

# Quick Task 260531-oax: Add -Trace switch to otto-gw.ps1

## Objective

Bring the Windows PowerShell wrapper to parity with the bash `--trace` flag added
in quick-260531-o4s. A single `-Trace` switch must set both `$env:DEBUG=true` and
`$env:CHAT_TRACE=true` for `start | restart | run`.

## Scope

- **scripts/otto-gw.ps1** — the only file edited.
- **scripts/otto-gw.bat** — NO edit. It is a pure pass-through
  (`powershell ... -File otto-gw.ps1 %*`), so `otto-gw.bat start -Trace` forwards
  `-Trace` to the `.ps1` automatically.
- config.go, init flow, bash wrapper — out of scope.

## Tasks

### Task 1: Add -Trace switch to otto-gw.ps1

Three insertions, mirroring the existing `-Debug` handling:

1. `param()` block — add `[switch]$Trace` after `[string]$Auth,`.
2. `Apply-CliFlags` — add `if ($Trace) { $env:DEBUG = 'true'; $env:CHAT_TRACE = 'true' }`
   after the `$DebugRequested` branch.
3. `Show-Usage` — document `-Trace` under "Gateway config flags", aligned with `-Debug`.

**Note:** Unlike `-Debug` (a PowerShell reserved common parameter requiring the
`$DebugRequested = $PSBoundParameters.ContainsKey('Debug')` workaround), `-Trace`
is not reserved, so it is declared as a normal `[switch]` and tested directly.

**Why no ENABLED_HOOKS plumbing:** `internal/config/config.go` auto-prepends
`ChatTraceHook` to `ENABLED_HOOKS` at runtime when `CHAT_TRACE=true`, so setting
the two env vars is the complete implementation.

- `files`: scripts/otto-gw.ps1
- `verify`: grep confirms `[switch]$Trace`, the Apply-CliFlags branch, and the usage line
- `done`: `-Trace` sets DEBUG + CHAT_TRACE; `.bat` inherits via pass-through

## must_haves

- truths:
  - `otto-gw.ps1 start -Trace` exports DEBUG=true and CHAT_TRACE=true
  - `otto-gw.bat start -Trace` works with no .bat change (pass-through)
- artifacts:
  - scripts/otto-gw.ps1 with `-Trace` switch
- key_links:
  - scripts/otto-gw.ps1
  - scripts/otto-gw.bat
