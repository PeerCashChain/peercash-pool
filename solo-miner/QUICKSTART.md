# PeerCash Solo Miner (XMRig) - Quick Start

Mine PeerCash (PEER) with **XMRig** on your own PC. This is **solo mining**:
you run your own node, you keep **100% of every block you find**, and rewards
go straight to **your** address. There is no pool, no operator, no fee, and
nobody else to trust.

```
   XMRig  ->  peercash-bridge (:3333)  ->  your PeerCash node  ->  the network
 (does the hashing)   (translates)        (builds + verifies blocks)
```

XMRig is much faster than the built-in miner in the PeerCash Wallet app, so if
you already run XMRig this is the way to get your full hashrate on PeerCash.

---

## What's in this folder

| File | What it is |
|------|------------|
| `peercash.exe` | the PeerCash node (self-contained, no install) |
| `peercash-bridge.exe` | the stratum bridge XMRig connects to |
| `genesis-mainnet.json` | the mainnet genesis (chainId 620156) |
| `xmrig-config.json` | XMRig config, already pointed at the bridge |
| `START-PEERCASH-MINER.bat` | starts everything in the right order |
| `restore-snapshot.ps1` | optional: fast first sync from a published snapshot (step 2b) |
| `miner-address.txt` | **you create this** - your payout address |

You supply **XMRig yourself** (see step 2). Everything else is here.

---

## Requirements

- Windows 10/11, 64-bit, a multi-core CPU.
- ~a few GB of free disk for the chain data (grows over time).
- A PeerCash address to be paid to. Easiest way to make one: install the
  **PeerCash Wallet** app and copy your address, or use any EVM wallet
  (MetaMask) - any `0x...` address works.
- Patience on first run: your node must **sync the whole chain** before your
  mining counts. That can take a while the first time.

---

## Steps

### 1. Set your payout address
Create a file named **`miner-address.txt`** in this folder containing just your
address on one line:
```
0xYourPeerCashAddressHere
```
Every block you mine pays this address. (The `user` field in XMRig's config is
cosmetic for solo mining - the node's address is what gets paid.)

### 2. Get XMRig
Download the official Windows build from:
**https://github.com/xmrig/xmrig/releases**
Unzip it and copy **`xmrig.exe`** into this folder.

> **Windows Defender will flag XMRig** as a mining tool (it flags all miners).
> That's expected. You'll need to allow it / add an exclusion for this folder:
> open PowerShell as admin and run
> `Add-MpPreference -ExclusionPath "<full path to this folder>"`
> then re-copy `xmrig.exe` if Defender already removed it. Only do this for
> software you downloaded yourself from the official source above.

### 2b. (Optional, recommended) Fast first sync - restore a snapshot
A fresh node otherwise validates the whole chain from block 0, which takes a
while. If a snapshot is published, restore it first so the node starts near the
tip and only syncs a tiny gap. With the node **not yet started**:
```
powershell -ExecutionPolicy Bypass -File restore-snapshot.ps1 `
    -Source  <SNAPSHOT_URL_OR_ZIP> `
    -Sha256  <SNAPSHOT_SHA256> `
    -Datadir .\data
```
It verifies the SHA256 before extracting (never trust a snapshot that fails the
check). After the node syncs, you can confirm it's on the real chain: the block
at the snapshot height should match the published head hash and `explorer.peercash.io`.
Skip this step to sync from genesis the slow-but-trustless way.

### 3. Start mining
Double-click **`START-PEERCASH-MINER.bat`**. It will:
1. initialize the node data directory (first run only),
2. start the node (it begins syncing the chain),
3. start the bridge on `127.0.0.1:3333`,
4. start XMRig pointed at the bridge.

Three windows open: **Node**, **Bridge**, **XMRig**.

### 4. Wait for the node to sync
In the **Node** window you'll see `Imported new chain segment ...` as it catches
up. **Until it's fully synced, your mined blocks won't count** (you're on a
stale head). Once it's caught up to the current height, your XMRig shares become
real blocks and the **Bridge** window logs `BLOCK ACCEPTED ...`.

### 5. Check your rewards
Look up your address on the explorer (**explorer.peercash.io**) and watch the
balance climb as you find blocks.

### To stop
Close the XMRig, Bridge, and Node windows (in any order).

---

## Troubleshooting

- **XMRig can't connect / "connection refused" on :3333** - the bridge isn't up
  yet or the node RPC isn't ready. Give the node a few seconds after launch; the
  `.bat` waits ~12s, but a slow first start can need more. Re-run the `.bat`
  (the node will still be running).
- **Bridge logs no jobs / `eth_getWork` errors** - the node isn't serving work
  yet. It needs a moment after startup and a valid `miner.etherbase` (your
  `miner-address.txt`). Confirm the address is a valid `0x...` line.
- **"peercash.exe is not recognized" / nothing happens** - run the `.bat` from
  inside this folder (double-click it; don't copy the command elsewhere).
- **No peers / not syncing** - your firewall may be blocking the node. Allow
  `peercash.exe` through Windows Firewall when prompted. The node dials the
  PeerCash bootnodes outbound; inbound isn't required.
- **Defender removed xmrig.exe** - see the exclusion note in step 2, then
  re-copy `xmrig.exe`.

---

## Advanced

- **CPU threads / usage:** edit `xmrig-config.json` (see XMRig docs). By default
  XMRig auto-tunes. Lower it if you want to keep the machine responsive.
- **Bridge options:** `peercash-bridge.exe -h` lists flags (`-node`, `-stratum`,
  `-poll`, connection limits, optional `-stats` endpoint).
- **Already run a PeerCash node?** Skip the bundled node: point the bridge at
  your existing node with `-node http://127.0.0.1:8545` (it must have
  `--miner.etherbase` set and the `eth` HTTP API enabled). The node does **not**
  need `--mine` - the bridge drives mining.
