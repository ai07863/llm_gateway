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

# 单实例锁：重复启动的守护进程直接退出，避免两个守护抢着拉起网关。
$mutex = New-Object System.Threading.Mutex($false, 'Local\llm-gateway-supervisor')
if (-not $mutex.WaitOne(0)) {
  Write-Host "[watch] 已有守护进程在运行，本实例退出"
  exit 0
}

$binDir = Join-Path $root 'bin'
New-Item -ItemType Directory -Force -Path $binDir | Out-Null
$logFile = Join-Path $binDir 'supervisor.log'

function Write-Log([string]$msg) {
  $line = "[watch] $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss') $msg"
  Write-Host $line
  try {
    if ((Test-Path -LiteralPath $logFile) -and (Get-Item -LiteralPath $logFile).Length -gt 1MB) {
      Move-Item -LiteralPath $logFile -Destination "$logFile.old" -Force
    }
    Add-Content -LiteralPath $logFile -Value $line -Encoding UTF8
  } catch { }
}

$listen = ':8090'
if (Test-Path '.env') {
  $line = Get-Content '.env' -Encoding UTF8 | Where-Object { $_ -match '^LISTEN_ADDR\s*=' } | Select-Object -First 1
  if ($line) { $listen = ($line -replace '^LISTEN_ADDR\s*=\s*', '').Trim() }
}
$port = if ($listen -match ':(\d+)\s*$') { $Matches[1] } else { '8090' }
$health = "http://127.0.0.1:$port/healthz"
$startScript = Join-Path $PSScriptRoot 'start.ps1'

Write-Log "守护启动，端口 $port，检查间隔 ${IntervalSeconds}s"
while ($true) {
  $ok = $false
  try {
    $r = Invoke-WebRequest -UseBasicParsing -Uri $health -TimeoutSec 2
    $ok = ($r.StatusCode -eq 200 -and $r.Content -match '"status"\s*:\s*"ok"|^\s*ok\s*$')
  } catch { }

  if (-not $ok) {
    Write-Log "网关不可用，尝试拉起..."
    & $startScript -NoBuild
    if ($LASTEXITCODE -and $LASTEXITCODE -ne 0) {
      Write-Log "拉起失败，${RetryDelaySeconds}s 后重试"
      Start-Sleep -Seconds $RetryDelaySeconds
      continue
    }
  }
  Start-Sleep -Seconds $IntervalSeconds
}