@echo off
setlocal
REM scripts/setup.bat - one-time setup for Windows operators after extracting otto_gateway.
REM Performs (a) recursive Unblock-File to strip MOTW Zone.Identifier streams from every
REM file in the package, and (b) Set-ExecutionPolicy RemoteSigned at CurrentUser scope so
REM subsequent .ps1 invocations work without -ExecutionPolicy Bypass.
REM
REM cmd.exe is NOT subject to PowerShell execution policy, so this bootstrap works even on
REM a machine where Set-ExecutionPolicy has never been called. We invoke powershell with
REM -NoProfile -ExecutionPolicy Bypass to insulate this run from any user profile or
REM machine-wide policy that would otherwise refuse the inline command.

echo Setting up otto-gateway for Windows...
echo.
echo [1/2] Stripping Mark-of-the-Web (MOTW) from extracted files...
powershell -NoProfile -ExecutionPolicy Bypass -Command "Get-ChildItem -Path '%~dp0..' -Recurse -File | Unblock-File"
if errorlevel 1 goto err

echo [2/2] Setting PowerShell ExecutionPolicy to RemoteSigned (CurrentUser)...
powershell -NoProfile -ExecutionPolicy Bypass -Command "Set-ExecutionPolicy -Scope CurrentUser -ExecutionPolicy RemoteSigned -Force"
if errorlevel 1 goto err

echo.
echo Done. You can now run:
echo   .\scripts\otto-gw.ps1 init    (or otto-gw.bat init)
echo   .\scripts\otto-gw.ps1 start   (or otto-gw.bat start / start.bat)
echo.
pause
exit /b 0

:err
echo.
echo Setup hit an error. If your environment locks CurrentUser ExecutionPolicy via
echo Group Policy (LocalMachine / MachinePolicy scope overrides CurrentUser), you can
echo still run the dispatcher with a per-invocation bypass:
echo   powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\otto-gw.ps1 ^<command^>
echo.
pause
exit /b 1
