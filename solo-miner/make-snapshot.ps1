# Produce a distributable PeerCash chaindata snapshot (fast-sync bootstrap).
#
# Two-pass, safe-by-design:
#   Pass 1 (node RUNNING):  records the head height+hash via RPC, then tells you
#                           to stop the node.
#   Pass 2 (node STOPPED):  archives chaindata+ancient, emits SHA256 + manifest.
#
#   powershell -ExecutionPolicy Bypass -File make-snapshot.ps1 -Datadir <datadir> [-RpcUrl http://127.0.0.1:8545] [-OutDir .\snapshot]
#
# -Datadir is the node's --datadir (the parent that contains the 'peercash'
# instance dir, i.e. <datadir>\peercash\chaindata).
#
# Why two passes: a live pebble/leveldb copied while the node is writing can be
# corrupt. We never archive a running node. RPC must be reachable in pass 1 only
# (to record the exact head the snapshot represents).

param(
  [Parameter(Mandatory=$true)][string]$Datadir,
  [string]$RpcUrl = "http://127.0.0.1:8545",
  [string]$OutDir = (Join-Path (Get-Location) "snapshot")
)

$ErrorActionPreference = "Stop"
$instance  = Join-Path $Datadir "peercash"
$chaindata = Join-Path $instance "chaindata"
$triedb    = Join-Path $instance "triedb"   # holds merkle.journal (PBSS head-state diffs)
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
$headFile = Join-Path $OutDir "head.json"

function Get-NodeUp {
  try {
    $body = '{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}'
    $r = Invoke-RestMethod -Uri $RpcUrl -Method Post -ContentType 'application/json' -Body $body -TimeoutSec 4
    return $r
  } catch { return $null }
}

$up = Get-NodeUp

if ($up -ne $null) {
  # -------- Pass 1: node is RUNNING -> record head, then require a stop --------
  $num = [Convert]::ToInt64($up.result,16)
  $blkBody = '{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByNumber","params":["' + $up.result + '",false]}'
  $blk = Invoke-RestMethod -Uri $RpcUrl -Method Post -ContentType 'application/json' -Body $blkBody -TimeoutSec 8
  $cidBody = '{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":[]}'
  $cid = (Invoke-RestMethod -Uri $RpcUrl -Method Post -ContentType 'application/json' -Body $cidBody -TimeoutSec 4).result
  $head = [ordered]@{ chainId = $cid; height = $num; headHashHex = $up.result; headHash = $blk.result.hash }
  $head | ConvertTo-Json | Set-Content -Encoding ascii $headFile
  Write-Host ""
  Write-Host "Pass 1 complete - head recorded:"
  Write-Host ("  chainId   : {0}" -f $cid)
  Write-Host ("  height    : {0}" -f $num)
  Write-Host ("  head hash : {0}" -f $blk.result.hash)
  Write-Host ""
  Write-Host "NOW STOP THE NODE (close its window / stop the service), then re-run"
  Write-Host "this exact command to archive the snapshot."
  exit 0
}

# -------- Pass 2: node is STOPPED -> archive --------
if (-not (Test-Path $headFile)) {
  throw "Node appears stopped but no head.json found. Run pass 1 first WITH the node running (so the head can be recorded)."
}
if (-not (Test-Path $chaindata)) { throw "chaindata not found at: $chaindata" }

$head = Get-Content $headFile -Raw | ConvertFrom-Json
if ($head.chainId -ne "0x9767c") {
  Write-Host ("WARNING: recorded chainId {0} is not mainnet 0x9767c" -f $head.chainId)
}

# CRITICAL: path-scheme (PBSS) keeps head state as in-memory diff layers that are
# journaled to peercash\triedb\merkle.journal ONLY on a clean shutdown. Without it,
# a restored node has no head state and rewinds ~90k blocks to the on-disk layer.
# So the snapshot MUST include triedb/ next to chaindata/.
$journal = Join-Path $triedb "merkle.journal"
if (-not (Test-Path $journal)) {
  Write-Host "WARNING: $journal not found."
  Write-Host "The node was NOT cleanly stopped, so head state is not persisted and a"
  Write-Host "restore will rewind ~90k blocks. Stop the node gracefully (it logs"
  Write-Host "'Persisted dirty state to file') and re-run pass 2."
}

$zip = Join-Path $OutDir ("peercash-mainnet-snapshot-{0}.zip" -f $head.height)
if (Test-Path $zip) { Remove-Item -Force $zip }
# Zip's top-level entries are the instance dirs chaindata/ and triedb/ (NOT their
# contents) so a restore extracts straight into <datadir>\peercash\.
$toArchive = @($chaindata)
if (Test-Path $triedb) { $toArchive += $triedb }
Write-Host "Archiving chaindata (incl. ancient freezer) + triedb (head-state journal) ..."
Compress-Archive -Path $toArchive -DestinationPath $zip

$sha = (Get-FileHash $zip -Algorithm SHA256).Hash
# stateScheme MUST be "path": the PeerCash Wallet and the solo-miner .bat both launch
# the node with geth's default (path/PBSS) scheme. Cut this snapshot from a node that
# ran WITHOUT --state.scheme hash. A hash-scheme snapshot (e.g. from the infra fleet)
# will NOT load in these consumers. The app reads: url, sha256, sizeBytes, height,
# headHash, chainId, stateScheme (camelCase) - keep these keys. Set `url` to the
# release-asset download URL when you publish (see SNAPSHOT-MANIFEST.md).
$manifest = [ordered]@{
  chainId     = $head.chainId
  height      = $head.height
  headHash    = $head.headHash
  file        = (Split-Path $zip -Leaf)
  url         = ""
  sha256      = $sha
  sizeBytes   = (Get-Item $zip).Length
  stateScheme = "path"
  note        = "Extract the zip's contents into <datadir>\peercash (yields chaindata/ + triedb/), then start the node. Verify: after sync, eth_getBlockByNumber at this height must return headHash."
}
$manifestFile = Join-Path $OutDir ("peercash-mainnet-snapshot-{0}.manifest.json" -f $head.height)
$manifest | ConvertTo-Json | Set-Content -Encoding ascii $manifestFile

Write-Host ""
Write-Host "Snapshot ready:"
Write-Host ("  zip      : {0}" -f $zip)
Write-Host ("  size     : {0:N0} bytes" -f (Get-Item $zip).Length)
Write-Host ("  sha256   : {0}" -f $sha)
Write-Host ("  height   : {0}" -f $head.height)
Write-Host ("  headHash : {0}" -f $head.headHash)
Write-Host ("  manifest : {0}" -f $manifestFile)
Write-Host ""
Write-Host "Publish the zip + sha256 + head hash (e.g. IPFS). Restore with restore-snapshot.ps1."
