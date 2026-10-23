# Publish (or refresh) a PeerCash snapshot to the dedicated snapshots repo's
# long-lived release, replacing the previous assets in place so the app's
# manifest URL stays stable.
#
#   powershell -ExecutionPolicy Bypass -File publish-snapshot.ps1 `
#       -Zip      .\snapshot\peercash-mainnet-snapshot-140544.zip `
#       -Manifest .\snapshot\peercash-mainnet-snapshot.json
#
# Inputs come from make-snapshot.ps1 (cut from a SYNCED, STOPPED path-scheme node).
# -Manifest must be named peercash-mainnet-snapshot.json and have its `url` set to
# the release download URL for the zip (see SNAPSHOT-MANIFEST.md).
#
# Auth: a GitHub token with `repo` scope. Taken from $env:GITHUB_TOKEN if set,
# otherwise from Git Credential Manager (the credential used for github.com).

param(
  [Parameter(Mandatory=$true)][string]$Zip,
  [Parameter(Mandatory=$true)][string]$Manifest,
  [string]$Repo = "PeerCashChain/peercash-snapshots",
  [string]$Tag  = "mainnet-latest"
)

$ErrorActionPreference = "Stop"
foreach ($f in @($Zip, $Manifest)) { if (-not (Test-Path $f)) { throw "missing: $f" } }

# --- token ---
$token = $env:GITHUB_TOKEN
if (-not $token) {
  $cred = "protocol=https`nhost=github.com`n`n" | git credential fill 2>$null
  $token = ($cred | Select-String '^password=').ToString().Substring(9)
}
if (-not $token) { throw "No GitHub token (set GITHUB_TOKEN or sign in to git for github.com)." }
$auth = @{ Authorization = "Bearer $token"; Accept = "application/vnd.github+json" }

$api = "https://api.github.com/repos/$Repo"

# --- find or create the release for $Tag ---
try {
  $rel = Invoke-RestMethod -Uri "$api/releases/tags/$Tag" -Headers $auth
} catch {
  Write-Host "Release $Tag not found - creating it."
  $body = @{ tag_name = $Tag; target_commitish = "main"; name = "PeerCash mainnet snapshot (latest)"; prerelease = $false } | ConvertTo-Json
  $rel = Invoke-RestMethod -Uri "$api/releases" -Headers $auth -Method Post -Body $body -ContentType "application/json"
}
Write-Host ("Release id={0} tag={1}" -f $rel.id, $Tag)

# --- delete any existing assets with the names we're about to upload ---
$zipName = Split-Path $Zip -Leaf
$manName = Split-Path $Manifest -Leaf
foreach ($a in $rel.assets) {
  if ($a.name -eq $zipName -or $a.name -eq $manName -or $a.name -like "peercash-mainnet-snapshot-*.zip") {
    Write-Host ("deleting old asset: {0}" -f $a.name)
    Invoke-RestMethod -Uri "$api/releases/assets/$($a.id)" -Headers $auth -Method Delete | Out-Null
  }
}

# --- upload new assets ---
function Upload($path, $name, $ctype) {
  $url = "https://uploads.github.com/repos/$Repo/releases/$($rel.id)/assets?name=$name"
  $h = @{ Authorization = "Bearer $token"; "Content-Type" = $ctype }
  Write-Host ("uploading {0} ..." -f $name)
  $r = Invoke-RestMethod -Uri $url -Headers $h -Method Post -InFile $path
  Write-Host ("  -> {0}" -f $r.browser_download_url)
}
Upload $Zip      $zipName "application/zip"
Upload $Manifest $manName "application/json"

Write-Host ""
Write-Host "Published. Verify (no auth):"
Write-Host ("  https://github.com/$Repo/releases/download/$Tag/$manName")
