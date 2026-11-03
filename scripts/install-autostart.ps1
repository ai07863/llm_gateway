#requires -Version 5.1
<#
  注册「登录时自动启动」：
    - 默认：注册计划任务 llm-gateway（需要管理员权限）
    - -StartupFolder：放入当前用户启动文件夹（无需管理员）
  两种方式都会运行 supervise.ps1 守护网关（挂了自动拉起）。
  卸载：scripts\uninstall-autostart.ps1
#>
param(
  [switch]$StartupFolder
)
$ErrorActionPreference = 'Stop'
try { [Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false) } catch { }

$root = Split-Path -Parent $PSScriptRoot
$script = Join-Path $root 'scripts\supervise.ps1'
if (-not (Test-Path -LiteralPath $script)) { throw "缺少 $script" }

$taskName = 'llm-gateway'
$startupEntry = Join-Path ([Environment]::GetFolderPath('Startup')) 'llm-gateway.cmd'

function Test-Admin {
  $id = [Security.Principal.WindowsIdentity]::GetCurrent()
  $principal = New-Object Security.Principal.WindowsPrincipal($id)
  return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Install-StartupFolder {
  $content = "@echo off`r`npowershell -NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -File `"$script`"`r`n"
  Set-Content -LiteralPath $startupEntry -Value $content -Encoding ASCII
  Write-Host "已创建登录启动项：$startupEntry" -ForegroundColor Green
  Write-Host "下次登录自动启动并守护网关；本机当前会话可运行：Start-Process -WindowStyle Hidden powershell -ArgumentList '-NoProfile','-ExecutionPolicy','Bypass','-File','$script'"
}

if ($StartupFolder) {
  Install-StartupFolder
  return
}

if (-not (Test-Admin)) {
  Write-Host "注册计划任务需要管理员权限。" -ForegroundColor Yellow
  Write-Host "二选一：" -ForegroundColor Yellow
  Write-Host "  A. 右键 PowerShell -> 以管理员身份运行，再执行："
  Write-Host "     powershell -ExecutionPolicy Bypass -File `"$PSScriptRoot\install-autostart.ps1`""
  Write-Host "  B. 用免管理员的启动文件夹方案（推荐，效果相同）："
  Write-Host "     powershell -ExecutionPolicy Bypass -File `"$PSScriptRoot\install-autostart.ps1`" -StartupFolder"
  exit 2
}

$action = New-ScheduledTaskAction -Execute 'powershell.exe' `
  -Argument "-NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -File `"$script`"" `
  -WorkingDirectory $root
$trigger = New-ScheduledTaskTrigger -AtLogOn
$settings = New-ScheduledTaskSettingsSet `
  -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
  -ExecutionTimeLimit ([TimeSpan]::Zero) `
  -MultipleInstances IgnoreNew `
  -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)

Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger `
  -Settings $settings -Description 'LLM Gateway：登录时启动并守护' -Force | Out-Null

if (Test-Path -LiteralPath $startupEntry) {
  Remove-Item -LiteralPath $startupEntry -Force
  Write-Host "已移除旧的启动文件夹入口，避免重复守护。" -ForegroundColor DarkGray
}

Write-Host "已注册计划任务 '$taskName'（登录时启动）。" -ForegroundColor Green
Write-Host "立即启动: Start-ScheduledTask -TaskName $taskName"
Write-Host "查看状态: Get-ScheduledTask -TaskName $taskName"
Write-Host "卸载:     .\scripts\uninstall-autostart.ps1"