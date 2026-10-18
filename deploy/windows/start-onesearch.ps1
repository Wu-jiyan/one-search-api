# 启动 one-search-api（Windows）
# 用法：.\deploy\windows\start-onesearch.ps1 [-Config <path>]
param(
    [string]$Config = ""
)

$root = Resolve-Path (Join-Path $PSScriptRoot "..\..")
$exe = Join-Path $root "onesearch.exe"
if (-not (Test-Path $exe)) {
    Write-Error "找不到 $exe，请先执行: go build -o onesearch.exe ./cmd/onesearch"
    exit 1
}
if ($Config -eq "") { $Config = Join-Path $root "config.yaml" }
if (-not (Test-Path $Config)) {
    Write-Error "找不到配置文件 $Config（可复制 config.example.yaml 后修改）"
    exit 1
}

$running = Get-Process onesearch -ErrorAction SilentlyContinue
if ($running) {
    Write-Host "one-search-api 已在运行 (pid=$($running[0].Id))"
    exit 0
}

Start-Process -FilePath $exe `
    -ArgumentList "-config", $Config `
    -RedirectStandardOutput (Join-Path $root "onesearch.log") `
    -RedirectStandardError (Join-Path $root "onesearch.err.log") `
    -WindowStyle Hidden

Start-Sleep -Milliseconds 800
try {
    $health = Invoke-RestMethod -Uri "http://127.0.0.1:8080/healthz" -TimeoutSec 5
    Write-Host "已启动：$($health.status) providers=$($health.providers.Count)"
} catch {
    Write-Warning "进程已启动但 /healthz 未响应，请查看 onesearch.err.log"
}
