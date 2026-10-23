# Assembles the Windows solo-miner distributable zip.
#
#   powershell -ExecutionPolicy Bypass -File package-windows.ps1 [-NodeExe <path>]
#
# Produces: dist\peercash-solo-miner-windows.zip  (node + bridge + genesis +
# config + launcher + quickstart). XMRig is NOT bundled - the user downloads the
# official signed binary (see QUICKSTART.md).

param(
  [string]$NodeExe = "C:\Users\b_str\peercash-chain\build\bin\peercash-static.exe"
)

$ErrorActionPreference = "Stop"
$here    = Split-Path -Parent $MyInvocation.MyCommand.Path
$pool    = Split-Path -Parent $here
$bridge  = Join-Path $pool "bin\peercash-bridge-windows-amd64.exe"
$distRoot= Join-Path $here "dist"
$stage   = Join-Path $distRoot "peercash-solo-miner-windows"
$zip     = Join-Path $distRoot "peercash-solo-miner-windows.zip"

# sanity: inputs exist
foreach ($f in @($NodeExe, $bridge,
                 (Join-Path $here "genesis-mainnet.json"),
                 (Join-Path $here "xmrig-config.json"),
                 (Join-Path $here "START-PEERCASH-MINER.bat"),
                 (Join-Path $here "restore-snapshot.ps1"),
                 (Join-Path $here "QUICKSTART.md"))) {
  if (-not (Test-Path $f)) { throw "missing input: $f" }
}

# fresh staging dir
if (Test-Path $stage) { Remove-Item -Recurse -Force $stage }
New-Item -ItemType Directory -Force -Path $stage | Out-Null

# assemble
Copy-Item $NodeExe                                   (Join-Path $stage "peercash.exe")
Copy-Item $bridge                                    (Join-Path $stage "peercash-bridge.exe")
Copy-Item (Join-Path $here "genesis-mainnet.json")   $stage
Copy-Item (Join-Path $here "xmrig-config.json")      $stage
Copy-Item (Join-Path $here "START-PEERCASH-MINER.bat") $stage
Copy-Item (Join-Path $here "restore-snapshot.ps1")   $stage
Copy-Item (Join-Path $here "QUICKSTART.md")          $stage

# zip
if (Test-Path $zip) { Remove-Item -Force $zip }
Compress-Archive -Path (Join-Path $stage "*") -DestinationPath $zip

# report
Write-Host ""
Write-Host "Packaged: $zip"
Write-Host ""
Write-Host "Contents:"
Get-ChildItem $stage | ForEach-Object { "  {0,-28} {1,12:N0} bytes" -f $_.Name, $_.Length } | Write-Host
Write-Host ""
Write-Host "SHA256:"
Get-FileHash (Join-Path $stage "peercash.exe")        -Algorithm SHA256 | ForEach-Object { "  peercash.exe        $($_.Hash)" }
Get-FileHash (Join-Path $stage "peercash-bridge.exe") -Algorithm SHA256 | ForEach-Object { "  peercash-bridge.exe $($_.Hash)" }
Get-FileHash $zip                                     -Algorithm SHA256 | ForEach-Object { "  ZIP                 $($_.Hash)" }
