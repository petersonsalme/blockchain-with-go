# Local playground

A three-node `bwg` network (the `docker-compose.yml` of the repository) that starts from an empty chain and is funded by mining, so you can try every feature by hand. Everything the playground creates (binary, keystores, env files) lives in `.playground/`, which is ignored by git; the compose files add only `compose.playground.yml` and `compose.tuning.yml`.

Every command below was run on a real playground (Docker Compose, difficulty 1). Hashes, addresses, timestamps and node IDs differ on every run; the shape of the output is what matters. Things that cannot be done by hand say so and name the test that covers them. The `/explorer/` pages were not opened in a browser while writing this page: the "Look at" hints come from the explorer's documented views (README, "Web explorer").

## Ports and one network at a time

| Port | Service | Profile / file |
|---|---|---|
| 18081, 18082, 18083 | `node1` (mines, pays the coinbase), `node2`, `node3` | base |
| 18084 | `snap1` (snapshots and pruning) | `--profile snapshots` |
| 18091, 18092 | `poa1`, `poa2` (Proof of Authority) | `--profile poa` |
| 19090 | `prometheus` | `--profile observability` |
| 13000 | `grafana` | `--profile observability` |

The base network, the e2e test (`go test -tags e2e ./e2e/...`) and the playground all use 18081-18083, so **only one of them can run at a time**. Stop the others first (`docker compose down -v` for the base network; the e2e test tears down its own `bwg-e2e` project).

## Commands used in this page

The compose project is `bwg-playground`. These shell functions keep the commands short; define them once per terminal from the repository root:

```shell
dc() { docker compose -p bwg-playground --env-file .playground/compose.env -f docker-compose.yml -f compose.playground.yml "$@"; }
export WALLET_PASSWORD=choose-a-password   # unlocks the keystores; never printed or stored by the scripts
B=.playground/bwg                          # the bwg binary built by scripts/playground.sh
N=http://127.0.0.1
```

## Start and fund

```shell
$ scripts/playground.sh        # 3 blocks by default; scripts/playground.sh 10 mines 10
```

The script builds `.playground/bwg`, creates the miner keystore `.playground/miner.key` (only the first time), writes `.playground/compose.env` with `MINER_ADDRESS`, brings the three nodes up with `--build`, waits for `GET /health`, asks node1 to mine, and prints:

```
block 1 69da001582eb4051c6dfeb388a1d4eff336d9ef6385f4b96fca44fb5d0372f73 txs=1
block 2 32330b951d00fe0d078c3f9ce1b2f497496258bc4dc6b06c8f5168601e869b91 txs=1
block 3 399a9cc9ad7fb2dfa7f43c741331711ef490fbfbf420ae56a38333fdadf064bf txs=1
miner address: 006bf401843b7ac110767cdb07168f57d69b12e43f7c4ddbc4
balance on 127.0.0.1:18081: 15000000000
balance on 127.0.0.1:18082: 15000000000
balance on 127.0.0.1:18083: 15000000000
```

The genesis is empty (no `GENESIS_ALLOCATIONS`); the only coins come from the coinbase of the mined blocks: 3 × 5,000,000,000 base units. Re-running the script reuses the miner keystore and the existing volumes.

Look at: `http://127.0.0.1:18081/explorer/` lists the latest blocks; each mined block has one coinbase transaction without inputs paying the miner address.

## Health, convergence and peers

```shell
$ curl -s $N:18081/health
{
  "status": "ok",
  "height": 3,
  "tip": "399a9cc9ad7fb2dfa7f43c741331711ef490fbfbf420ae56a38333fdadf064bf"
}
```

Run it on 18082 and 18083: the three nodes converge when `height` and `tip` are equal. Peers:

```shell
$ curl -s $N:18081/p2p/peers
{
  "peers": [
    { "node_id": "2d92a12f1577be10737a798542ab3521", "addr": "http://node2:8080", "height": 3,
      "tip_hash": "399a9cc9...", "inbound": true, "last_seen": "2026-10-27T19:00:08Z" },
    { "node_id": "66f0e2cea951a86f13ca9b42f37e4bb4", "addr": "http://node3:8080", "height": 3,
      "tip_hash": "399a9cc9...", "inbound": true, "last_seen": "2026-10-27T19:00:08Z" }
  ],
  "addrs": [ "http://node2:8080", "http://node3:8080" ]
}
```

(Shortened here; the real output is indented JSON.) The seeds are node2 → node1 and node3 → node2, and gossip fills in the rest. `GET /peers` does not exist; the route is `/p2p/peers`.

## Mining and coinbase

The `Wallet: new, balance, send` section below shows a real `miner` run (a block with two transactions: the coinbase plus one transfer).

`miner` calls `POST /mine`; `--count 0` mines until `Ctrl+C`, `--interval 2s` pauses between blocks. `txs` counts the coinbase plus the mempool transactions the block took. Only node1 has `MINER_ADDRESS`: a block mined on node2 (`curl -s -X POST $N:18082/mine`) has no coinbase (its `Transactions` is `[]`) and all nodes accept it. Look at: the coinbase transaction at position 0 of the block page in the explorer.

## Wallet: new, balance, send

```shell
$ $B wallet new --keystore .playground/alice.key
0085c802430abc7af16c30ba24725a0567d435846557fae833
$ $B wallet new --keystore .playground/bob.key
00de6019807d7a7e6d9b424d3d101f1e5933e861e67f28ef4d
$ $B wallet send --node $N:18081 --keystore .playground/miner.key --to 0085c802430abc7af16c30ba24725a0567d435846557fae833 --amount 1000 --fee 10
e53e1a73a2f8d36e02cc011d987037f495067d5cdbc12fd1dec2d39cd6f73855
$ $B miner --node $N:18081 --count 1
block 4 1665dee5b1d6d13d75e0c38068c1fe679d446e4bf642cfc495486022587f3819 txs=2
$ $B wallet balance --node $N:18083 0085c802430abc7af16c30ba24725a0567d435846557fae833
1000
```

`wallet new` refuses to overwrite a keystore and needs `WALLET_PASSWORD`. `wallet send` prints the transaction ID once the node has put it in the mempool; the balance only changes after a block confirms it (the 4th block above holds the coinbase and this transaction, hence `txs=2`). The fee is mined into the coinbase of node1, so the miner address receives it back. Look at: the address page of Alice in the explorer (balance and UTXOs), and the transaction page linking to the output it spent.

## Mempool and fee-rate ordering

Send two transactions from two funded wallets (a wallet that holds one confirmed output can have one pending spend at a time, see the next section). Bob needs coins first:

```shell
$ $B wallet send --node $N:18081 --keystore .playground/miner.key --to 00de6019807d7a7e6d9b424d3d101f1e5933e861e67f28ef4d --amount 1000000 --fee 1
fc52ff400af1166f5d314a9d67374b2b2908980cba7ac6da0de60e0e819be571
$ $B miner --node $N:18081 --count 1
block 5 1358fad06ef4486d79dbdd6f2421e497fa2f7bb7dadd18e53c66fd81433d9d77 txs=2
$ $B wallet send --node $N:18081 --keystore .playground/alice.key --to 00de6019807d7a7e6d9b424d3d101f1e5933e861e67f28ef4d --amount 100 --fee 5
384eed3d1df76822ca2818b459590bfff937515add575f18401a6aaf63682f30
$ $B wallet send --node $N:18081 --keystore .playground/bob.key --to 0085c802430abc7af16c30ba24725a0567d435846557fae833 --amount 200 --fee 500
e4d1d332df8bdf3adec32758f5491b41a41e54d33e3b4f70f0b7260d3fcc5b67
$ curl -s $N:18081/mempool | grep -E '"ID"|"Amount"'
      "ID": "e4d1d332df8bdf3adec32758f5491b41a41e54d33e3b4f70f0b7260d3fcc5b67",
          "Amount": 200,
          "Amount": 999300,
      "ID": "384eed3d1df76822ca2818b459590bfff937515add575f18401a6aaf63682f30",
          "Amount": 100,
          "Amount": 895,
$ $B miner --node $N:18081 --count 1
block 6 35bdb53f1edffa20aea8ad4bd7c848504ec01d55c6405ee74388c837eeb9a272 txs=3
$ curl -s $N:18081/mempool
{
  "transactions": []
}
```

`GET /mempool` lists pending transactions by fee rate (fee per byte), highest first: Bob's fee 500 comes before Alice's fee 5 although Alice sent first. The block takes both (`txs=3` with the coinbase) and the pool empties. Balances after: Alice `1095`, Bob `999400`.

## Double-spend rejection

A second spend of the same output while the first is pending is refused. Right after the `wallet send` of the Wallet section, and before mining its block, send again from the same keystore:

```shell
$ $B wallet send --node $N:18081 --keystore .playground/miner.key --to 0085c802430abc7af16c30ba24725a0567d435846557fae833 --amount 2000 --fee 50
Error: node answered 409: mempool: transaction spends an outpoint already spent in the pool
```

`wallet send` picks the same first confirmed output both times, so the second transaction conflicts (`409 mempool_conflict`; first seen wins, there is no replace-by-fee). After a block confirms the first one, the same output is spent and a transaction using it would fail validation. Look at: `GET /mempool` still holds only the first transaction.

## Contracts (deploy and call)

**Not reproducible by hand.** A contract is deployed and called by a signed transaction with a contract payload, and `bwg` has no command that builds one (`wallet send` only builds transfers). What you can check by hand is that the route exists and an unknown contract is a `404`:

```shell
$ curl -s $N:18081/contracts/0000000000000000000000000000000000000000000000000000000000000000
{
  "error": "contract not found",
  "code": "contract_not_found"
}
```

Deploy and call are covered by the tests of `internal/vm` (`vm_test.go`, `state_test.go`, plus the fuzz tests) and the README section "Contracts".

## SSE events (`/events`)

```shell
$ curl -sN $N:18081/events        # terminal 1
$ $B miner --node $N:18081 --count 1   # terminal 2
```

Terminal 1 prints one `event: block` with a single-line `data:` JSON for the mined block (and, per the README, an `event: tx` after each accepted `POST /tx`; not run here). Every node sends the event for each block it appends, whatever the source: a block mined on node1 is also one `event: block` (same `Hash`) on node2 and node3, through gossip, sync or reorg. The explorer uses this stream to add new blocks at the top without polling.

## Metrics (`/metrics`)

```shell
$ curl -s $N:18081/metrics | grep -E '^(# (HELP|TYPE) )?(http_|blockchain_)'
# HELP blockchain_blocks Number of blocks in the chain.
# TYPE blockchain_blocks gauge
blockchain_blocks 11
# HELP blockchain_mining_duration_seconds Time spent mining blocks that were produced.
# TYPE blockchain_mining_duration_seconds summary
blockchain_mining_duration_seconds_sum 0.000848201
blockchain_mining_duration_seconds_count 1
# HELP http_requests_total HTTP requests served, by method, route pattern and status code.
# TYPE http_requests_total counter
http_requests_total{code="200",method="GET",route="GET /metrics"} 9
http_requests_total{code="200",method="POST",route="POST /p2p/inv"} 1
http_requests_total{code="201",method="POST",route="POST /mine"} 1
```

The counters are per node; a double spend, as above, shows up as `code="409"`. Families are sorted by name and labels by name, and the same endpoint also serves the Go runtime (`go_*`) and process (`process_*`) metrics, left out of the `grep` above ([ADR-0022](adr/0022-prometheus-client-golang.md)).

## Grafana and Prometheus (`--profile observability`)

Start Prometheus and Grafana next to the running playground (published on loopback only, no volumes: data is lost on `down`):

```shell
$ dc --profile observability up -d
```

Open `http://127.0.0.1:13000/d/bwg-network` (anonymous Viewer, no login). The dashboard `bwg network` refreshes every 5 s and has four panels:

- **Height per node**: `blockchain_blocks` per `instance`; the three lines should overlap once gossip has converged.
- **Block rate (blocks per minute)**: `delta(blockchain_blocks[1m])`.
- **Requests by route and status**: `sum by (route, code) (rate(http_requests_total[1m]))`.
- **Mining duration (average seconds per mined block)**: `rate(..._sum[5m]) / rate(..._count[5m])`. It is only non-empty on nodes that mined (node1 here); the others received their blocks by gossip and never call `Mine`.

Prometheus scrapes `node1:8080`, `node2:8080` and `node3:8080` every 5 s; the targets page is `http://127.0.0.1:19090/targets`. Command-line checks:

```shell
$ curl -s 127.0.0.1:19090/api/v1/targets | grep -o '"instance":"[^"]*"\|"health":"[^"]*"'
$ curl -s -o /dev/null -w '%{http_code}\n' 127.0.0.1:13000/api/dashboards/uid/bwg-network
200
$ curl -s '127.0.0.1:13000/api/datasources/proxy/uid/prometheus/api/v1/query?query=blockchain_blocks'   # 3 series
$ $B miner --node $N:18081 --count 1                  # all three values rise within 30 s
```

With `compose.tuning.yml`, node1 has `RATE_LIMIT=30` and a scrape every 5 s uses 12 of those requests per minute (scrapes are rate limited like any other request).

## Restart sync (stop and start node3)

```shell
$ dc stop node3
$ $B miner --node $N:18081 --count 3
block 9 61e15e8faffa85e45d8d2c56504ab224ba3edc4c1bbe63f7191daca8de227cf3 txs=1
block 10 00309c5146c1aaf8f62d97ed21dcab2d38dadce1c5a7a297b67ea71edfd84db5 txs=1
block 11 21f3c79b89a3774cfa594b6d34b421df069ce1101d10caaacbb12d1452f7cb46 txs=1
$ dc start node3
$ sleep 6; curl -s $N:18083/health
{
  "status": "ok",
  "height": 11,
  "tip": "21f3c79b89a3774cfa594b6d34b421df069ce1101d10caaacbb12d1452f7cb46"
}
```

While stopped, node3 misses three blocks; after the restart its volume still holds the old chain and initial block download (ADR-0019) fetches the missing ones from its seed. It reaches node1's tip within the 1 s peer interval plus the sync time (6 s was enough).

## Reorg and peer ban

**Not reproducible by hand.** A reorg needs two branches mined while the nodes cannot see each other, and a ban needs a peer that sends invalid data during sync; the compose network has no partition or misbehaving-peer tool. They are covered by tests:

- reorg: `cmd/bwg/sync_test.go:TestForkEndsOnTheBranchWithMoreWork` and `TestEqualWorkKeepsTheFirstSeenBranch`;
- peer ban and `403 peer_banned`: `internal/p2p/sync_test.go:TestSyncBansTheMisbehavingPeerAndTriesTheNext`, `TestSyncBansWhenTheLedgerRejectsTheBlocks` and the status table in `internal/server/p2p_test.go`;
- invalid blocks and transactions are neither accepted nor relayed: `cmd/bwg/gossip_test.go:TestInvalidBlockIsNeitherAcceptedNorRelayed`, `TestInvalidTxIsNeitherAcceptedNorRelayed`.

## Snapshots and pruning (`--profile snapshots`)

`snap1` is a fourth node with `SNAPSHOT_INTERVAL=10` and `PRUNE_KEEP_BLOCKS=100`. It is a separate profile because **pruning cannot be undone**: a pruned node only follows reorgs down to the pruned height, so the three main nodes keep every block and stay the reference.

```shell
$ dc --profile snapshots up -d snap1
$ curl -s $N:18084/health          # it syncs from node1 (height 11 in this run)
$ $B miner --node $N:18081 --count 120
$ curl -s $N:18084/health          # height 131, same tip as node1
$ curl -s $N:18084/block/1
{
  "error": "block body was pruned; only its header is kept",
  "code": "block_pruned"
}
$ curl -s $N:18081/block/1 | head -c 60     # the full node still has the body
{
  "Index": 1,
  "Timestamp": "2026-10-27T18:59:10.448735812Z",
```

`curl -s $N:18084/block/130` returns the full block: only the bodies older than the 100 most recent are replaced by headers. Known limitation seen while writing this page: once `snap1` has pruned, its log repeats `p2p: cannot build the local hello: storage: block body is pruned` every second (`docker logs bwg-playground-snap1-1`). It still received every block from node1 (height 131 above), only its own outbound handshake fails. This is a node behaviour, not something the playground changes.

Export and import of a snapshot (`--snapshot-export`, `--snapshot-import`) are not part of the playground; see the README, section "Snapshots and pruning".

## Reindex

`--reindex` rebuilds the derived indexes (UTXO set, undo data, indexes by transaction and address) from the stored blocks. Stop the node first: the command opens `chain.db`, which the running node holds.

```shell
$ dc stop node1
$ dc run --rm node1 node --reindex
reindexed: tip height 131
$ dc start node1
$ curl -s $N:18081/health
{
  "status": "ok",
  "height": 131,
  "tip": "2a5e64f450abd41c35340ce6b4060340ae89a527518fd615aa2060992f68a370"
}
```

## Proof of Authority (`--profile poa`)

PoA is a separate profile because a chain is tied to one consensus and one validator set (ADR-0005): a PoA chain cannot share a data directory or peers with the PoW nodes. `poa1` (port 18091) and `poa2` (18092, seeded from `poa1`) have their own volumes and two validators, one key each. Nothing about the keys is committed; you generate them into `.playground/`.

Two traps, both solved by the helper below:

1. **The public key is not printed by any CLI.** `wallet new` prints the address, but `POA_VALIDATORS` wants the Ed25519 public key in hex. The README shows a Go snippet; the helper below does the same for two validators and writes `.playground/poa.env`. It lives in `.playground/`, so it is outside `./...`: it adds nothing to the coverage gate or the linter and is never committed.
2. **File permissions.** `wallet.Save` writes the keystore `0600`, owned by the host user, but the distroless image runs as uid 65532 and could not read it. The helper `chmod`s the two keystores to `0644` (they are encrypted with the password) and the directory is bind-mounted read-only at `/poa`. `poa.env`, which holds the password, stays `0600`: Docker Compose reads it as the host user and passes it as environment.

```shell
$ mkdir -p .playground/poa-tool
$ cat > .playground/poa-tool/main.go <<'EOF'
// Generates two validator keystores and the poa.env file used by compose.playground.yml.
package main

import (
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

func main() {
	password := os.Getenv("POA_KEYSTORE_PASSWORD")
	if password == "" {
		log.Fatal("set POA_KEYSTORE_PASSWORD")
	}
	dir := filepath.Join(".playground", "poa")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Fatal(err)
	}
	var keys []string
	for _, name := range []string{"poa1", "poa2"} {
		w, err := wallet.New()
		if err != nil {
			log.Fatal(err)
		}
		path := filepath.Join(dir, name+".key")
		if err := w.Save(path, password); err != nil {
			log.Fatal(err)
		}
		// Save writes 0600 for the host user; the container runs as uid 65532.
		if err := os.Chmod(path, 0o644); err != nil {
			log.Fatal(err)
		}
		keys = append(keys, hex.EncodeToString(w.PublicKey()))
	}
	env := fmt.Sprintf("POA_VALIDATORS=%s,%s\nPOA_KEYSTORE_PASSWORD=%s\n", keys[0], keys[1], password)
	if err := os.WriteFile(filepath.Join(".playground", "poa.env"), []byte(env), 0o600); err != nil {
		log.Fatal(err)
	}
	fmt.Println(keys[0])
	fmt.Println(keys[1])
}
EOF
$ POA_KEYSTORE_PASSWORD=choose-a-password go run ./.playground/poa-tool
c119e67a9eb86883db8348275311e026e2a94f4e8311d40dd55a0aaf700d2b98
148f558eb0c669adb9b0faece060727be2d55bf8832733219eaf1b2096639694
```

The two lines are the public keys of `poa1` and `poa2` (they differ on every run). Start the validators and seal blocks:

```shell
$ dc --profile poa up -d poa1 poa2
$ curl -s $N:18091/health          # height 0 on both 18091 and 18092
$ $B miner --node $N:18091 --count 2
block 1 5a60d102f47a11a31d71103159469ffd5fe18809d862aaf52a6fd74a691c9c52 txs=0
block 2 ab5686ad86983c9fa1e3f0465389d58a549b24a1c1a52d9ac84d090d4676b135 txs=0
$ $B miner --node $N:18092 --count 1
block 3 dc1837882428cf35ddf4ff6e29a20111c7dba87d14a596fe2ee789c2acdf4f8d txs=0
$ curl -s $N:18091/health
{
  "status": "ok",
  "height": 3,
  "tip": "dc1837882428cf35ddf4ff6e29a20111c7dba87d14a596fe2ee789c2acdf4f8d"
}
```

`miner` still calls `POST /mine`; under PoA the node seals the block with its own key instead of searching a nonce, so there is no difficulty or waiting. Blocks carry no coinbase (the PoA nodes have no `MINER_ADDRESS`). Look at: `GET /blocks` on either node shows `Validator` and `Signature` filled and `Nonce`/`Difficulty` at 0; blocks 1 and 2 carry the validator of `poa1` and block 3 the validator of `poa2`. Both nodes accept blocks sealed by either key; a key outside `POA_VALIDATORS` makes the node refuse to start (`POA_KEYSTORE: key <hex> is not in POA_VALIDATORS`, from the node source; not run here).

## Rate limit and retargeting (`-f compose.tuning.yml`)

`compose.tuning.yml` sets `RATE_LIMIT=30` and `MINING_WORKERS=2` on node1 and a difficulty retarget every 10 blocks aiming at 2 s per block on **all three** nodes. It is a separate file because retargeting is a consensus rule: nodes that disagree on it reject each other's blocks, so it must be on from the first block. **Start from a clean chain** (see Reset below, keep `.playground/` if you want the same miner). Do not add the file to a chain that already has blocks.

```shell
$ docker compose -p bwg-playground --env-file .playground/compose.env \
    -f docker-compose.yml -f compose.playground.yml -f compose.tuning.yml up -d
$ $B miner --node $N:18081 --count 25
$ curl -s "$N:18081/blocks?limit=100" | grep '"Difficulty"' | sort | uniq -c
      1       "Difficulty": 0
     19       "Difficulty": 1
      6       "Difficulty": 2
```

The 25 blocks were mined far faster than 2 s each, so at the first retarget the difficulty rose from 1 to 2: blocks 1-19 are at difficulty 1 and blocks 20-25 at difficulty 2 (genesis is 0). The difficulty is part of each block (`Difficulty` in `GET /blocks`).

Rate limit: 30 requests per minute per IP. Burst past it:

```shell
$ for i in $(seq 40); do curl -s -o /dev/null -w "%{http_code} " $N:18081/health; done; echo
429 429 429 ...
$ curl -si $N:18081/health | head -5
HTTP/1.1 429 Too Many Requests
Content-Type: application/json
Retry-After: 2
```

The first answers can already be `200`s or `429`s depending on the requests the miner and the peers made in the same minute; once the budget is gone they are all `429`. The body is `{"error": "rate limit exceeded", "code": "rate_limited"}`. The limit counts every request from the same IP, including the peer traffic of the other nodes to node1 and the `miner` command, so a burst of your own requests can also slow the other nodes' sync for a moment; it recovers by itself when the minute window moves on.

## Reset

```shell
$ dc --profile '*' down -v --remove-orphans     # stops everything, deletes the containers, the volumes and the network
$ rm -rf .playground                            # deletes the binary, the env files and the keystores
```

> **Warning:** `rm -rf .playground` deletes `miner.key`, `alice.key`, `bob.key` and the PoA validator keystores. Without them the coins mined to the miner address, and any PoA chain, cannot be spent or continued. That is fine for a playground; do not keep real funds here.

`--profile '*'` is needed: without it `down` ignores the services of the `snapshots` and `poa` profiles and leaves `snap1`, `poa1`, `poa2` and their volumes behind. This also removes a network started with `compose.tuning.yml` (same project name). After a reset, `scripts/playground.sh` starts again from an empty chain with a new miner address.
