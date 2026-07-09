<#
    ai-search task runner (PowerShell) — 'make' substitute for Windows.

    Usage:
        .\tasks.ps1 up          # start infra services
        .\tasks.ps1 up-app      # start infra + crawler + ai-api
        .\tasks.ps1 up-gpu      # start infra + TEI (needs NVIDIA toolkit)
        .\tasks.ps1 down        # stop everything
        .\tasks.ps1 ps          # list services
        .\tasks.ps1 logs        # tail logs
        .\tasks.ps1 health      # curl health endpoints
        .\tasks.ps1 init-env    # create .env from .env.example
#>
param(
    [Parameter(Position = 0)]
    [ValidateSet("up", "up-app", "up-gpu", "down", "ps", "logs", "health", "init-env")]
    [string]$Task = "up"
)

$ErrorActionPreference = "Stop"
$root = $PSScriptRoot
$compose = @("compose", "--env-file", "$root\.env", "-f", "$root\deploy\docker-compose.yml")

function Ensure-Env {
    if (-not (Test-Path "$root\.env")) {
        Copy-Item "$root\.env.example" "$root\.env"
        Write-Host "Created .env from .env.example — review secrets before production." -ForegroundColor Yellow
    }
}

switch ($Task) {
    "init-env" { Ensure-Env; break }
    "up"       { Ensure-Env; & docker @compose up -d; break }
    "up-app"   { Ensure-Env; & docker @compose --profile app up -d --build; break }
    "up-gpu"   { Ensure-Env; & docker @compose --profile gpu up -d; break }
    "down"     { & docker @compose down; break }
    "ps"       { & docker @compose ps; break }
    "logs"     { & docker @compose logs -f --tail=100; break }
    "health"   {
        Write-Host "OpenSearch:" -ForegroundColor Cyan
        try { (Invoke-WebRequest "http://localhost:9200/_cluster/health").Content } catch { Write-Host $_ }
        Write-Host "`nQdrant:" -ForegroundColor Cyan
        try { (Invoke-WebRequest "http://localhost:6333/readyz").Content } catch { Write-Host $_ }
        Write-Host "`nMinIO:" -ForegroundColor Cyan
        try { (Invoke-WebRequest "http://localhost:9000/minio/health/live").StatusCode } catch { Write-Host $_ }
        Write-Host "`nNATS:" -ForegroundColor Cyan
        try { (Invoke-WebRequest "http://localhost:8222/healthz").Content } catch { Write-Host $_ }
        break
    }
}
