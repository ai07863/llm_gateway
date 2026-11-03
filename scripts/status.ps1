#requires -Version 5.1
<#
  查看 llm-gateway 运行状态：端口、健康检查、PID、最近错误日志。
#>
$ErrorActionPreference = 'Continue'
try { [Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false) } catch { }
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$lines = if (Test-Path '.env') { Get-Content '.env' -Encoding UTF8 } else { @() }
$listenLine = $lines | Where-Object { $_ -match '^LISTEN_ADDR\s*=' } | Select-Object -First 1
$listen = if ($listenLine) { ($listenLine -replace '^LISTEN_ADDR\s*=\s*', '').Trim() } else { ':8090' }
$port = if ($listen -match ':(\d+)\s*$') { $Matches[1] } else { '8090' }

$healthy = $false
try {
  $r = Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$port/healthz" -TimeoutSec 2
  $healthy = ($r.StatusCode -eq 200 -and $r.Content -match '"status"\s*:\s*"ok"|^\s*ok\s*$')
} catch { }

Write-Host "端口:     $port"
if ($healthy) {
  Write-Host "健康检查: ok" -ForegroundColor Green
} else {
  Write-Host "健康检查: DOWN" -ForegroundColor Red
}

$pidFile = Join-Path $root 'bin\gateway.pid'
$proc = $null
if (Test-Path -LiteralPath $pidFile) {
  $savedId = Get-Content -LiteralPath $pidFile -ErrorAction SilentlyContinue | Select-Object -First 1
  if ($savedId) { $proc = Get-Process -Id $savedId -ErrorAction SilentlyContinue }
}
if (-not $proc) {
  foreach ($c in Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue) {
    $p = Get-Process -Id $c.OwningProcess -ErrorAction SilentlyContinue
    if ($p -and $p.ProcessName -eq 'gateway') { $proc = $p; break }
  }
}
if ($proc) {
  Write-Host "进程:     PID $($proc.Id)，启动于 $($proc.StartTime.ToString('yyyy-MM-dd HH:mm:ss'))"
} else {
  Write-Host "进程:     未找到"
}

$log = Join-Path $root 'bin\gateway.err.log'
if (Test-Path -LiteralPath $log) {
  Write-Host "最近日志:"
  Get-Content -LiteralPath $log -Tail 5 -ErrorAction SilentlyContinue | ForEach-Object { "  $_" }
}

if (-not $healthy -and -not $proc) {
  $listener = Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue | Select-Object -First 1
  if ($listener) {
    $p = Get-Process -Id $listener.OwningProcess -ErrorAction SilentlyContinue
    if ($p) {
      Write-Host "警告: 端口 $port 被 $($p.ProcessName) (PID $($p.Id)) 占用，但它不是网关。" -ForegroundColor Yellow
    }
  }
}