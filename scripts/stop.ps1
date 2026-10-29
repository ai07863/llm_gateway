#requires -Version 5.1
<#
  停止由 scripts\start.ps1 启动的 llm-gateway。
#>
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$pidFile = Join-Path $root 'bin\gateway.pid'
$stopped = $false

if (Test-Path -LiteralPath $pidFile) {
  $procId = (Get-Content -LiteralPath $pidFile -ErrorAction SilentlyContinue | Select-Object -First 1)
  if ($procId) {
    $proc = Get-Process -Id $procId -ErrorAction SilentlyContinue
    if ($proc) {
      Stop-Process -Id $proc.Id -Force
      Write-Host "已停止网关 PID $($proc.Id)" -ForegroundColor Green
      $stopped = $true
    }
  }
  Remove-Item -LiteralPath $pidFile -ErrorAction SilentlyContinue
}

if (-not $stopped) {
  # 兜底：按进程名结束
  $procs = Get-Process -Name gateway -ErrorAction SilentlyContinue
  foreach ($proc in $procs) {
    Stop-Process -Id $proc.Id -Force
    Write-Host "已停止 gateway PID $($proc.Id)" -ForegroundColor Green
    $stopped = $true
  }
}

if (-not $stopped) { Write-Host "没有发现正在运行的网关。" -ForegroundColor DarkGray }