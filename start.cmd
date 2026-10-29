@echo off
chcp 65001 >nul
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0scripts\start.ps1" %*
echo.
echo If you see the PORT and dashboard URL above, the gateway started.
echo Press any key to close this window...
pause >nul
