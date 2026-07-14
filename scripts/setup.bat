@echo off
setlocal
REM scripts/setup.bat - one-time setup for Windows operators after extracting otto_gateway.
REM Idempotent: safe to re-run. Only does work that actually needs doing, and reports
REM precisely what changed vs what was already in good shape.
REM
REM Two steps:
REM   1. Strip Mark-of-the-Web (Zone.Identifier ADS) from files that have it. Skip files
REM      already clean. Report how many were unblocked.
REM   2. Verify PowerShell ExecutionPolicy is already permissive (RemoteSigned, Unrestricted,
REM      or Bypass). If yes, skip the set entirely. If no, set CurrentUser to RemoteSigned
REM      and report the effective result. If effective policy stays restrictive even after
REM      setting (Group Policy override), surface the .bat dispatcher fallback.
REM
REM cmd.exe is NOT subject to PowerShell execution policy, so this bootstrap works on a
REM fresh machine. We do NOT pass -ExecutionPolicy Bypass to the spawned powershell
REM processes because that creates a Process-scope override that then warns about itself
REM during Set-ExecutionPolicy. Set-ExecutionPolicy and Unblock-File are built-in cmdlets
REM that run regardless of execution policy when invoked via -Command (not via .ps1).

echo Setting up otto-gateway for Windows...
echo.

echo [1/2] Checking Mark-of-the-Web on extracted files...
powershell -NoProfile -Command "$root = Split-Path -Parent '%~dp0'; $files = Get-ChildItem -Path $root -Recurse -File; $unblocked = 0; foreach ($f in $files) { if (Get-Item $f.FullName -Stream Zone.Identifier -ErrorAction SilentlyContinue) { Unblock-File -Path $f.FullName; $unblocked++ } }; if ($unblocked -gt 0) { Write-Host ('  Unblocked ' + $unblocked + ' file(s).') } else { Write-Host '  No files needed unblocking - Mark-of-the-Web already clean.' }"
if errorlevel 1 goto err

echo.
echo [2/2] Checking PowerShell ExecutionPolicy...
powershell -NoProfile -Command "$effective = Get-ExecutionPolicy; $permissive = @('RemoteSigned','Unrestricted','Bypass'); if ($permissive -contains $effective) { Write-Host ('  Effective ExecutionPolicy is already ' + $effective + ' - no action needed.'); exit 0 }; try { Set-ExecutionPolicy -Scope CurrentUser -ExecutionPolicy RemoteSigned -Force -ErrorAction Stop; $newEffective = Get-ExecutionPolicy; if ($permissive -contains $newEffective) { Write-Host ('  Set CurrentUser ExecutionPolicy to RemoteSigned (effective: ' + $newEffective + ').'); exit 0 }; Write-Host ('  Set CurrentUser to RemoteSigned but effective policy stays ' + $newEffective + ' (Group Policy override at a higher scope).'); Write-Host '  Use .\scripts\gw.bat or the per-command .bat shortcuts - they bypass per-invocation.'; exit 2 } catch { Write-Host ('  Could not set ExecutionPolicy: ' + $_.Exception.Message); Write-Host '  Use .\scripts\gw.bat or the per-command .bat shortcuts - they bypass per-invocation.'; exit 2 }"

REM PowerShell exit codes: 0 = ok or already-permissive, 2 = soft warning (GPO override
REM or set failed; bat surface still works), 1 = unexpected error.
if errorlevel 2 goto soft_warn
if errorlevel 1 goto err

echo.
echo Setup complete. You can now run:
echo    .\scripts\gw.ps1 status    (PowerShell wrapper)
echo    .\scripts\gw.bat status    (cmd dispatcher - works under any policy)
echo    .\scripts\start.bat             (Explorer-double-clickable shortcut)
echo.
pause
exit /b 0

:soft_warn
echo.
echo Setup advisory: the per-user PowerShell policy is overridden at a higher scope
echo (likely Group Policy). The .bat surface works without further intervention:
echo    .\scripts\gw.bat status
echo    .\scripts\start.bat              (double-click from Explorer)
echo.
pause
exit /b 0

:err
echo.
echo Setup hit an unexpected error. The .bat dispatcher should still work because
echo cmd.exe is not subject to PowerShell execution policy:
echo    .\scripts\gw.bat status
echo    .\scripts\start.bat              (double-click from Explorer)
echo.
pause
exit /b 1
