#requires -Version 5.1
<#
  一键启动 llm-gateway。
  - 自动释放 8080（只结束网关/编译产物；加 -Force 可强制结束其它进程）
  - 编译到 bin\gateway.exe
  - 后台启动、等待 /healthz、打印免填 token 的看板地址
  用法:
    powershell -ExecutionPolicy Bypass -File scripts\start.ps1
    powershell -ExecutionPolicy Bypass -File scripts\start.ps1 -NoBuild   # 跳过编译，快速重启
    powershell -ExecutionPolicy Bypass -File scripts\start.ps1 -Force     # 强制结束占用 8080 的任意进程
#>
param(
  [switch]$NoBuild,
  [switch]$Force
)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

function Fail([string]$msg) { Write-Host $msg -ForegroundColor Red; exit 1 }

# 1) .env
$envFile = Join-Path $root '.env'
if (-not (Test-Path -LiteralPath $envFile)) {
  Copy-Item -LiteralPath (Join-Path $root 'configs\.env.example') -Destination $envFile
  Write-Host "已从 configs/.env.example 生成 .env。请填入 DASHSCOPE_API_KEY 和 GATEWAY_TOKEN 后重新运行。" -ForegroundColor Yellow
  exit 1
}
$tokenLine = Get-Content -LiteralPath $envFile -Encoding UTF8 | Where-Object { $_ -match '^\s*GATEWAY_TOKEN\s*=' } | Select-Object -First 1
$token = if ($tokenLine) { ($tokenLine -replace '^\s*GATEWAY_TOKEN\s*=\s*', '').Trim() } else { '' }
if (-not $token) { Fail ".env 里 GATEWAY_TOKEN 为空，请先设置。" }

$badLines = @(Get-Content -LiteralPath $envFile -Encoding UTF8 | Where-Object { $_ -match '^\s*#.*\b[A-Z][A-Z0-9_]*\s*=' })
if ($badLines.Count -gt 0) {
  Write-Host ".env 疑似换行损坏（注释行里出现了 KEY=VALUE）。请用编辑器修正换行后再运行。" -ForegroundColor Red
  exit 1
}
$hasProvider = (Get-Content -LiteralPath $envFile -Encoding UTF8 | Where-Object { $_ -match '^[ \t]*(DASHSCOPE|OPENAI|DEEPSEEK)_API_KEY[ \t]*=[ \t]*\S' }).Count -gt 0
if (-not $hasProvider) {
  Write-Host ".env 里没有可用的 provider API_KEY（DASHSCOPE/OPENAI/DEEPSEEK），聊天会 502。请填写后重试。" -ForegroundColor Red
  exit 1
}

# 2) 释放 8080
$pids = @(netstat -ano | Select-String ':8080\s+.*LISTENING' | ForEach-Object { ($_.ToString() -split '\s+')[-1] } | Sort-Object -Unique)
foreach ($procId in $pids) {
  $proc = Get-Process -Id $procId -ErrorAction SilentlyContinue
  if (-not $proc) { continue }
  if ($proc.ProcessName -in @('gateway', 'server', 'go')) {
    Write-Host "结束旧网关：PID $($proc.Id) ($($proc.ProcessName))" -ForegroundColor DarkGray
    Stop-Process -Id $proc.Id -Force
  } elseif ($Force) {
    Write-Host "强制结束占用 8080 的进程：PID $($proc.Id) ($($proc.ProcessName))" -ForegroundColor Yellow
    Stop-Process -Id $proc.Id -Force
  } else {
    Fail "端口 8080 被 $($proc.ProcessName) (PID $($proc.Id)) 占用。确认安全后可加 -Force 强制结束。"
  }
}
Start-Sleep -Milliseconds 400

# 3) 编译
$binDir = Join-Path $root 'bin'
New-Item -ItemType Directory -Force -Path $binDir | Out-Null
$exe = Join-Path $binDir 'gateway.exe'
if (-not $NoBuild) {
  Write-Host "编译中..." -ForegroundColor DarkGray
  & go build -o $exe ./cmd/server
  if ($LASTEXITCODE -ne 0) { Fail "编译失败。" }
}

# 4) 后台启动
$outLog = Join-Path $binDir 'gateway.out.log'
$errLog = Join-Path $binDir 'gateway.err.log'
Remove-Item -LiteralPath $outLog, $errLog -ErrorAction SilentlyContinue
$p = Start-Process -FilePath $exe -WorkingDirectory $root -PassThru -WindowStyle Hidden `
  -RedirectStandardOutput $outLog -RedirectStandardError $errLog
Set-Content -LiteralPath (Join-Path $binDir 'gateway.pid') -Value $p.Id

# 5) 等待健康检查
$healthy = $false
for ($i = 0; $i -lt 40; $i++) {
  Start-Sleep -Milliseconds 250
  try {
    $r = Invoke-WebRequest -UseBasicParsing -Uri 'http://localhost:8080/healthz' -TimeoutSec 2
    if ($r.StatusCode -eq 200) { $healthy = $true; break }
  } catch { }
}
if (-not $healthy) {
  Write-Host "启动失败，最近的日志：" -ForegroundColor Red
  Get-Content -LiteralPath $errLog -Tail 20 -ErrorAction SilentlyContinue
  exit 1
}

# 6) 状态
$auth = @{ Authorization = "Bearer $token" }
$pricingSource = 'unknown'
try {
  $pr = Invoke-RestMethod -Uri 'http://localhost:8080/v1/admin/pricing' -Headers $auth -TimeoutSec 5
  $pricingSource = $pr.source
} catch { }

Write-Host ""
Write-Host "网关已启动 (PID $($p.Id))" -ForegroundColor Green
Write-Host "看板(免填 token): http://localhost:8080/?token=$token"
Write-Host "健康检查:         http://localhost:8080/healthz"
Write-Host "价格表来源:       $pricingSource"
Write-Host "停止:             powershell -ExecutionPolicy Bypass -File scripts\stop.ps1"
Write-Host ""
Write-Host "现在可重启 opencode（默认模型 gateway/deepseek-v4.1-flash）。" -ForegroundColor Cyan