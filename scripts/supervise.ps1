#requires -Version 5.1
<#
  守护进程：定期检查 /healthz，网关挂掉就调用 start.ps1 重新拉起。
  配合「登录时启动」的计划任务使用（见 install-autostart.ps1）。
#>
param(
  [int]$IntervalSeconds = 5,
  [int]$RetryDelaySeconds = 30
)
$ErrorActionPreference = 'Continue'
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$listen = ':8090'
if (Test-Path '.env') {
  $line = Get-Content '.env' -Encoding UTF8 | Where-Object { $_ -match '^LISTEN_ADDR\s*=' } | Select-Object -First 1
  if ($line) { $listen = ($line -replace '^LISTEN_ADDR\s*=\s*', '').Trim() }
}
$port = if ($listen -match ':(\d+)\s*$') { $Matches[1] } else { '8090' }
$health = "http://127.0.0.1:$port/healthz"
$startScript = Join-Path $PSScriptRoot 'start.ps1'

Write-Host "[watch] 守护启动，端口 $port，检查间隔 ${IntervalSeconds}s"
while ($true) {
  $ok = $false
  try {
    $r = Invoke-WebRequest -UseBasicParsing -Uri $health -TimeoutSec 2
    $ok = ($r.StatusCode -eq 200 -and $r.Content -match '"status"\s*:\s*"ok"|^\s*ok\s*$')
  } catch { }

  if (-not $ok) {
    Write-Host "[watch] $(Get-Date -Format 'HH:mm:ss') 网关不可用，尝试拉起..."
    & $startScript -NoBuild
    if ($LASTEXITCODE -and $LASTEXITCODE -ne 0) {
      Write-Host "[watch] 拉起失败，${RetryDelaySeconds}s 后重试"
      Start-Sleep -Seconds $RetryDelaySeconds
      continue
    }
  }
  Start-Sleep -Seconds $IntervalSeconds
}