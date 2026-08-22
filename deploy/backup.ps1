<#
.SYNOPSIS
  Self-hosted backup of the ai-search datastores (Phase 4 — backups/runbooks).

.DESCRIPTION
  Backs up the data-bearing stores to a timestamped folder:
    - Postgres : logical `pg_dump` (custom format, restorable with pg_restore) -- hot & consistent.
    - Qdrant / OpenSearch / MinIO : tar of the named Docker volume.
  Redis/NATS are rebuildable (frontier hot-cache / transient) and skipped unless -IncludeExtras.
  No paid/cloud services (CLAUDE.md rule 2) -- everything lands on local disk the owner controls.

  Volume tars of a *running* store can be slightly inconsistent; pass -Cold to stop the datastores
  first for a guaranteed-consistent copy (the pg_dump always runs hot, before any stop). Restore
  steps are in docs/10-OPERATIONS.md.

.EXAMPLE
  ./deploy/backup.ps1
  ./deploy/backup.ps1 -Cold -IncludeExtras -BackupRoot D:\ai-search-backups
#>
[CmdletBinding()]
param(
  [string]$BackupRoot = (Join-Path $PSScriptRoot "..\backups"),
  [switch]$Cold,
  [switch]$IncludeExtras
)

$ErrorActionPreference = "Stop"
$project = "ai-search"                       # docker compose project name (volumes are <project>_<vol>)
$compose = @("compose", "--env-file", (Join-Path $PSScriptRoot "..\.env"), "-f", (Join-Path $PSScriptRoot "docker-compose.yml"))

function Fail($msg) { Write-Error $msg; exit 1 }

# --- preflight ---------------------------------------------------------------
try { docker info | Out-Null } catch { Fail "Docker is not available/running." }
if ($LASTEXITCODE -ne 0) { Fail "Docker is not available/running." }

# Read Postgres creds from .env (fall back to compose defaults).
$envFile = Join-Path $PSScriptRoot "..\.env"
$pgUser = "aisearch"; $pgDb = "aisearch"
if (Test-Path $envFile) {
  foreach ($line in Get-Content $envFile) {
    if ($line -match '^\s*POSTGRES_USER\s*=\s*(.+?)\s*$') { $pgUser = $Matches[1] }
    if ($line -match '^\s*POSTGRES_DB\s*=\s*(.+?)\s*$')   { $pgDb   = $Matches[1] }
  }
}

$stamp = Get-Date -Format "yyyyMMdd-HHmmss"
$dest = Join-Path (Resolve-Path $BackupRoot -ErrorAction SilentlyContinue).Path $stamp 2>$null
if (-not $dest) { $dest = Join-Path $BackupRoot $stamp }
New-Item -ItemType Directory -Force -Path $dest | Out-Null
$destAbs = (Resolve-Path $dest).Path
Write-Host "Backing up to $destAbs"

# --- 1. Postgres logical dump (hot, restorable) ------------------------------
Write-Host "==> Postgres pg_dump ($pgDb)"
$pgContainer = "$project-postgres-1"
docker exec $pgContainer pg_dump -U $pgUser -d $pgDb -Fc -f /tmp/ai-search.dump
if ($LASTEXITCODE -ne 0) { Fail "pg_dump failed." }
docker cp "${pgContainer}:/tmp/ai-search.dump" (Join-Path $destAbs "postgres.dump")
docker exec $pgContainer rm -f /tmp/ai-search.dump

# --- 2. Volume tars ----------------------------------------------------------
$volumes = @("qdrantdata", "osdata", "miniodata")   # the indexed-data stores
if ($IncludeExtras) { $volumes += @("redisdata", "sessionsdata", "promdata", "grafanadata") }
# pgdata is covered by the logical dump above; include it too for a raw fallback.
$volumes = @("pgdata") + $volumes

if ($Cold) {
  Write-Host "==> Stopping datastores for a consistent volume copy"
  docker @compose stop postgres qdrant opensearch minio | Out-Null
}

foreach ($vol in $volumes) {
  $full = "${project}_${vol}"
  Write-Host "==> Volume $full"
  docker run --rm -v "${full}:/src:ro" -v "${destAbs}:/backup" alpine `
    sh -c "tar czf /backup/${vol}.tgz -C /src ."
  if ($LASTEXITCODE -ne 0) { Write-Warning "volume $full backup failed (skipping)" }
}

if ($Cold) {
  Write-Host "==> Restarting datastores"
  docker @compose start postgres qdrant opensearch minio | Out-Null
}

# --- 3. Manifest -------------------------------------------------------------
$manifest = Join-Path $destAbs "MANIFEST.txt"
"ai-search backup $stamp" | Out-File -FilePath $manifest -Encoding utf8
"cold=$Cold extras=$IncludeExtras" | Out-File -FilePath $manifest -Append -Encoding utf8
Get-ChildItem $destAbs -File | ForEach-Object {
  "{0,-24} {1,12:N0} bytes" -f $_.Name, $_.Length | Out-File -FilePath $manifest -Append -Encoding utf8
}
Get-Content $manifest
Write-Host "Backup complete: $destAbs"
