# peercash-pool

Mining infrastructure for the PeerCash chain (a go-ethereum / RandomX fork).

Built in two phases:

- **Phase 1 (this repo, now): `peercash-bridge`** — a solo stratum bridge that
  lets a stock **XMRig** mine against a single PeerCash node over the node's
  `eth_getWork` / `eth_submitWork` RPC. Block rewards go to the node's own
  `--miner.etherbase`.
- **Phase 2 (later): a public pool** — fork of
  [sammy007/open-ethereum-pool](https://github.com/sammy007/open-ethereum-pool)
  (Redis, payouts, unlocker, API, policy), with its Ethash stratum replaced by
  the Phase-1 stratum, RandomX share verification via
  `peercash-chain/consensus/randomx` (`NewHasher`), vardiff, and an unlocker
  fixed for the PeerCash reward schedule (1 PEER, halving, no uncles,
  coinbase-balance-delta accounting). **Not implemented yet.**

## How it works (Phase 1)

```
  XMRig  <--stratum/TCP :3333-->  peercash-bridge  <--JSON-RPC HTTP :8545-->  peercash node
 (RandomX rx/0)                   (this program)        (eth_getWork /         (verifies + inserts
                                                          eth_submitWork)        the block)
```

- The bridge polls `eth_getWork` every ~500 ms. On a change it pushes a fresh
  job to every connected XMRig client.
- The bridge needs **no RandomX itself**: the node verifies every submission.
  XMRig only ever submits real block solutions (the job target is the block
  target), so a submit is a block.

### The job blob (43 bytes)

Matches `peercash-chain/consensus/randomx` `sealInput`:

| bytes   | contents                                                        |
|---------|-----------------------------------------------------------------|
| `0:32`  | `sealHash` (from `eth_getWork[0]`)                              |
| `32:35` | zero padding                                                    |
| `35:39` | per-worker **extranonce** (so workers don't grind the same space) |
| `39:43` | zero — XMRig writes its 4-byte nonce here                       |

The `39:43` bytes are sent as zero so XMRig stays in **standard** mode (a
non-zero nonce field there flips XMRig into nicehash mode). XMRig's RandomX
nonce offset is 39, which is why the extranonce lives in `35:38`.

- **target**: the last 8 bytes of `eth_getWork[2]` (big-endian value
  `maxUint64/difficulty`), sent to XMRig as an 8-byte **little-endian** hex
  string. This makes XMRig's own share check identical to the node's
  `meetsTarget` rule, so XMRig only submits genuine block solutions.
- **seed_hash**: `eth_getWork[1]` (the RandomX epoch seed), hex without `0x`.

### On submit

XMRig sends `{job_id, nonce (4 bytes), result (32-byte RandomX hash)}`. The
bridge:

1. splices XMRig's 4 nonce bytes into `blob[39:43]`,
2. reads `blob[35:43]` as the 8-byte big-endian **header nonce**,
3. calls `eth_submitWork(nonce = blob[35:43], sealHash, mixDigest = result)`.

The node recomputes `RandomX(sealInput(sealHash, nonce))`, checks it equals
`mixDigest` and meets the target, then inserts and propagates the block.

## Build

Pure Go, no cgo — trivial to cross-compile.

```bash
make            # test + build bin/peercash-bridge-{windows-amd64.exe,linux-amd64}
make windows
make linux
make test
```

Or directly:

```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o bin/peercash-bridge.exe ./cmd/peercash-bridge
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -o bin/peercash-bridge     ./cmd/peercash-bridge
```

## Run

```
peercash-bridge [flags]

  -node     node JSON-RPC HTTP endpoint   (default http://127.0.0.1:8545)
  -stratum  stratum listen address        (default :3333)
  -poll     eth_getWork poll interval      (default 500ms)
```

The node must expose the `eth` namespace over HTTP and have an etherbase set
(it does **not** need `--mine`; the bridge drives mining, and the reward goes to
`--miner.etherbase`):

```bash
peercash --datadir <dir> --networkid <id> --bootnodes <enodes> \
         --miner.etherbase 0xYOUR_ADDRESS \
         --http --http.addr 127.0.0.1 --http.port 8545 --http.api eth,net,web3
```

Then point XMRig at the bridge using `xmrig/config.json` (edit nothing for a
local setup):

```bash
xmrig -c xmrig/config.json
```

## End-to-end test (testnet, chainId 563321)

1. **Init + start a testnet node** (synced to the tip, etherbase set):

   ```bash
   peercash init --datadir ./tnet <testnet-genesis.json>
   peercash --datadir ./tnet --networkid 563321 --bootnodes <testnet-enodes> \
            --miner.etherbase 0xYOUR_TESTNET_ADDRESS \
            --http --http.addr 127.0.0.1 --http.port 8545 --http.api eth,net,web3
   ```

   Wait until it is fully synced (otherwise work is built on a stale head and
   solutions are rejected by peers).

2. **Start the bridge:**

   ```bash
   ./peercash-bridge -node http://127.0.0.1:8545 -stratum :3333
   ```

   You should see `new job <id> seal=0x... height=N workers=0` lines as work
   rotates.

3. **Start XMRig:**

   ```bash
   xmrig -c xmrig/config.json
   ```

   XMRig logs `new job from 127.0.0.1:3333` and begins hashing rx/0.

4. **Confirm a block.** When XMRig finds one, the bridge logs
   `BLOCK ACCEPTED job=... worker=... nonce=0x...`, the node logs
   `Successfully sealed new RandomX block via remote sealer`, and the testnet
   explorer shows the new block mined by `0xYOUR_TESTNET_ADDRESS` (the node's
   etherbase).

> Testnet difficulty is low, so a single machine should find a block quickly.

## Tests

```bash
go test ./...
```

- `internal/stratum/job_test.go` — blob layout, target conversion, and the
  nonce round-trip (asserts the node's `sealInput` reproduces exactly the blob
  XMRig hashed).
- `internal/stratum/server_test.go` — drives the server like a real XMRig
  (login -> job -> submit) and asserts the exact `eth_submitWork` arguments.

## Layout

```
cmd/peercash-bridge/   main: flags, work poller, wiring
internal/node/         JSON-RPC client (eth_getWork/eth_submitWork/eth_blockNumber)
internal/stratum/      stratum server, protocol types, blob/target/nonce math
xmrig/config.json      sample XMRig config for the local bridge
```

## Reference

- `peercash-chain/consensus/randomx/hasher.go` — `sealInput`, `meetsTarget`
- `peercash-chain/miner/remote_sealer*.go`, `eth/api_mining.go` — the
  `eth_getWork` / `eth_submitWork` contract this bridge speaks to
