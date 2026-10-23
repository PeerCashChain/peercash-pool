# Restore a PeerCash chaindata snapshot so a node starts near tip instead of
# syncing from genesis.
#
#   powershell -ExecutionPolicy Bypass -File restore-snapshot.ps1 `
#       -Source <url-or-zip-path> -Sha256 <expected> -Datadir <datadir> [-Force]
#
# Run this with the node STOPPED, BEFORE the first start. It extracts the
# snapshot into <datadir>\peercash\chaindata.

param(
  [Parameter(Mandatory=$true)][string]$Source,     # http(s) URL or local .zip path
  [Parameter(Mandatory=$true)][string]$Sha256,     # expected zip SHA256 (from the manifest)
  [Parameter(Mandatory=$true)][string]$Datadir,    # node --datadir
  [switch]$Force                                    # overwrite existing chaindata
)

$ErrorActionPreference = "Stop"
$chaindata = Join-Path $Datadir "peercash\chaindata"

if ((Test-Path $chaindata) -and -not $Force) {
  throw "chaindata already exists at $chaindata. Use -Force to overwrite (this REPLACES local chain data)."
}

# 1. obtain the zip
$work = Join-Path $env:TEMP ("peercash-snap-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force -Path $work | Out-Null
if ($Source -match '^https?://') {
  $zip = Join-Path $work "snapshot.zip"
  Write-Host "Downloading snapshot..."
  Invoke-WebRequest -Uri $Source -OutFile $zip
} else {
  if (-not (Test-Path $Source)) { throw "snapshot file not found: $Source" }
  $zip = $Source
}

# 2. verify integrity
Write-Host "Verifying SHA256..."
$got = (Get-FileHash $zip -Algorithm SHA256).Hash
if ($got -ne $Sha256.ToUpper()) {
  throw "SHA256 MISMATCH`n  expected $($Sha256.ToUpper())`n  got      $got`nAborting - do NOT trust this file."
}
Write-Host "SHA256 OK."

# 3. extract into chaindata
if (Test-Path $chaindata) { Remove-Item -Recurse -Force $chaindata }
New-Item -ItemType Directory -Force -Path $chaindata | Out-Null
Write-Host "Extracting into $chaindata ..."
Expand-Archive -Path $zip -DestinationPath $chaindata -Force

Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
Write-Host ""
Write-Host "Snapshot restored. Start the node; it will open at the snapshot height"
Write-Host "and sync only the small gap to tip."
Write-Host "VERIFY (optional): once synced, eth_getBlockByNumber at the snapshot"
Write-Host "height should return the manifest's headHash (and match explorer.peercash.io)."
