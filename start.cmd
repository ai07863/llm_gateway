@echo off
chcp 65001 >nul
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0scripts\start.ps1" %*
echo.
echo 上面就是端口和看板地址。按任意键关闭此窗口...
pause >nul