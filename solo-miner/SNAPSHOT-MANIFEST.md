# PeerCash fast-sync snapshot manifest

A **snapshot** is a cold copy of a synced node's `chaindata` so a fresh node starts
near the chain tip instead of validating every block from genesis. The PeerCash
Wallet app and the solo-miner both consume snapshots; this file is the contract.

## Hard requirement: PATH state scheme

Both consumers launch the node with geth's **default `path` (PBSS) scheme** - they do
NOT pass `--state.scheme hash`. A snapshot therefore MUST be cut from a node that also
ran in `path` scheme. A `hash`-scheme snapshot (e.g. from the infra fleet, which runs
`--state.scheme hash`) will **not load** in the wallet / solo-miner and must not be
published here.

Cut snapshots from a fully-synced default-scheme node (e.g. the wallet's own datadir,
or the solo-miner `data/` dir) with `make-snapshot.ps1`.

## Manifest schema (`peercash-mainnet-snapshot.json`)

JSON, camelCase keys. The app deserializes exactly these fields (extra keys ignored):

```json
{
  "chainId":     "0x9767c",
  "height":      139000,
  "headHash":    "0x2cb279558bb4b887ad5eda5cbd89a61dc28e27c4acab9609284076fad76df505",
  "file":        "peercash-mainnet-snapshot-139000.zip",
  "url":         "https://github.com/PeerCashChain/peercash-snapshots/releases/download/mainnet-latest/peercash-mainnet-snapshot-139000.zip",
  "sha256":      "5BEED286...A36B772C8",
  "sizeBytes":   341000000,
  "stateScheme": "path"
}
```

| Field | Used by app | Meaning |
|-------|-------------|---------|
| `url` | ✅ download | Direct download URL of the snapshot zip (a GitHub release asset). |
| `sha256` | ✅ verify | Expected SHA256 of the zip; the app refuses a mismatch. |
| `sizeBytes` | ✅ progress | Zip size, for the download progress bar (falls back to the HTTP `Content-Length`). |
| `height` | ✅ UI + cache | Block height the snapshot represents; seeds the sync-cache so the UI shows it immediately. |
| `headHash` | cross-check | Block hash at `height`; verify against `explorer.peercash.io` after sync. |
| `chainId` | sanity | Must be mainnet `0x9767c`. |
| `stateScheme` | sanity | Must be `path` (see above). |

The **zip's top-level entries are the CONTENTS of `chaindata`** (incl. the `ancient/`
freezer), i.e. produced by `Compress-Archive -Path <datadir>\peercash\chaindata\*`.
The app extracts them straight into `<datadir>\peercash\chaindata`.

## Where it's published (stable URL)

The app's `snapshot_manifest_url` points at a **long-lived release tagged
`mainnet-latest`** on the dedicated `PeerCashChain/peercash-snapshots` repo, so the
URL is stable as snapshots are refreshed (assets are replaced in place):

```
https://github.com/PeerCashChain/peercash-snapshots/releases/download/mainnet-latest/peercash-mainnet-snapshot.json
```

Until a snapshot is published there the fetch 404s and the app simply syncs normally
(the feature is fail-open). To publish a refresh:

1. `make-snapshot.ps1 -Datadir <synced path-scheme datadir>` (two passes; node stopped
   for pass 2). Produces the zip + a manifest with `sha256`/`sizeBytes`/`height`.
2. Upload the zip as an asset on the `mainnet-latest` release (delete the old zip
   first); set `url` in the manifest to that asset's download URL.
3. Upload the manifest as `peercash-mainnet-snapshot.json` on the same release
   (replace the old asset). `publish-snapshot.ps1` automates steps 1-3.

## Staleness

A snapshot ages ~6,600 blocks/day (~10s blocks). The app only offers it when a node is
more than **25,000 blocks** behind tip (`SNAPSHOT_GAP_THRESHOLD`), so refresh the
published snapshot roughly weekly to keep the post-restore gap small.
