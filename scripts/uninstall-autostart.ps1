#requires -Version 5.1
<#
  卸载「登录时自动启动」计划任务和启动文件夹入口（不会停止正在运行的网关）。
#>
$ErrorActionPreference = 'Continue'
$taskName = 'llm-gateway'
$startupEntry = Join-Path ([Environment]::GetFolderPath('Startup')) 'llm-gateway.cmd'
$removed = $false

$task = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
if ($task) {
  Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
  Unregister-ScheduledTask -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue
  Write-Host "已卸载计划任务 '$taskName'。" -ForegroundColor Green
  $removed = $true
}

if (Test-Path -LiteralPath $startupEntry) {
  Remove-Item -LiteralPath $startupEntry -Force
  Write-Host "已移除启动文件夹入口：$startupEntry" -ForegroundColor Green
  $removed = $true
}

if (-not $removed) { Write-Host "没有找到自启配置。" -ForegroundColor DarkGray }