@echo off
setlocal enabledelayedexpansion
cd /d "%~dp0"

echo ============================================================
echo   PeerCash Solo Miner   (node + bridge + XMRig)
echo ============================================================
echo.

rem --- 1. your PEER payout address -----------------------------------------
if not exist miner-address.txt (
  echo   SETUP NEEDED:
  echo   Create a file called  miner-address.txt  in this folder containing
  echo   your PeerCash payout address on one line, e.g.
  echo       0x1234abcd........................
  echo.
  echo   All block rewards are paid to THIS address by your node.
  echo   Then run this script again.
  echo.
  pause
  exit /b 1
)
set /p ADDR=<miner-address.txt
echo   Payout address (node etherbase): !ADDR!
echo.

rem --- 2. first-run genesis init -------------------------------------------
if not exist data\peercash (
  echo   First run: initializing the PeerCash mainnet data directory...
  peercash.exe init --datadir data genesis-mainnet.json
  if errorlevel 1 (
    echo   Genesis init FAILED. Stopping.
    pause
    exit /b 1
  )
  echo.
)

rem --- 3. start the node ---------------------------------------------------
echo   Starting the PeerCash node (its window will sync the chain)...
start "PeerCash Node" peercash.exe --datadir data --networkid 620156 --bootnodes "enode://cc2782823bee8c12c127c0cdd1215ce00e6f0ed5d201900392a3c8942b1d368bdf42891289ecde9d87330caef83dda8b69c80d3e2da03ccb1a462e57ec730104@134.122.29.72:30303,enode://751d49ecf147088282bb0fa0b02f7d1fd683cdacfe08621deb2a8ada09bd1157496180d0a102751cd0384c3fadeba126325dbc6ac9577dbdc7f193cd49ab034d@167.71.85.230:30303" --miner.etherbase !ADDR! --http --http.addr 127.0.0.1 --http.port 8545 --http.api eth,net,web3

echo   Waiting ~12s for the node RPC to come up...
timeout /t 12 >nul

rem --- 4. start the bridge -------------------------------------------------
echo   Starting the stratum bridge on 127.0.0.1:3333 ...
start "PeerCash Bridge" peercash-bridge.exe -node http://127.0.0.1:8545 -stratum 127.0.0.1:3333
timeout /t 3 >nul

rem --- 5. start XMRig ------------------------------------------------------
if exist xmrig.exe (
  echo   Starting XMRig...
  start "XMRig" xmrig.exe -c xmrig-config.json
) else (
  echo.
  echo   xmrig.exe was not found in this folder.
  echo   Download the official XMRig for Windows from:
  echo       https://github.com/xmrig/xmrig/releases
  echo   Put xmrig.exe in THIS folder, then run this script again.
  echo   (The node and bridge are already running - leave them open.)
)

echo.
echo ------------------------------------------------------------
echo   Running. You should have windows open for: Node, Bridge, XMRig.
echo.
echo   IMPORTANT: let the NODE finish syncing before you expect
echo   rewards. Until the node is fully synced you are mining on a
echo   stale head and your blocks will not count. The node window
echo   shows "Imported new chain segment" as it catches up; it is
echo   synced when it stops racing to catch newer blocks.
echo.
echo   To stop mining: close the XMRig, Bridge, and Node windows.
echo ------------------------------------------------------------
echo.
pause
