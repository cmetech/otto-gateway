@echo off
REM scripts/otto-gw.bat - dispatcher for Windows operators who prefer cmd.exe over PowerShell.
REM Routes every argument through to scripts/otto-gw.ps1 with -ExecutionPolicy Bypass so it
REM works even on machines where Set-ExecutionPolicy has never been run (or where setup.bat
REM has not been executed yet).
REM Subcommand surface (unchanged from the .ps1): init|start|stop|status|restart|logs|run|env|version
REM Self-relocating: %~dp0 is the directory containing this .bat (with trailing backslash).
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0otto-gw.ps1" %*
