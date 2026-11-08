# Local playground

A three-node `bwg` network (the `docker-compose.yml` of the repository) that starts from an empty chain and is funded by mining, so you can try every feature by hand. Everything the playground creates (binary, keystores, env files) lives in `.playground/`, which is ignored by git; the compose files add only `compose.playground.yml` and `compose.tuning.yml`.

Every command below was run on a real playground (Docker Compose, difficulty 1), in the order of this page, from a fresh start (`dc --profile '*' down -v --remove-orphans; rm -rf .playground; scripts/playground.sh`), so block heights continue from one section to the next. Hashes, addresses, timestamps and node IDs differ on every run; the shape, counts, heights and messages are what matters. Every section ends with one line: `Verified: <date>, commit <sha>.` (the run the output came from) or `Not reproducible by hand; covered by <test>.` for what cannot be done by hand.

The `/explorer/` views (`#/`, `#/block/{ref}`, `#/tx/{id}`, `#/address/{addr}` and the error view `#/block/1` on `snap1`) were opened in Firefox (headless, driven through Marionette so the page has loaded when it is read; `firefox --headless --screenshot` alone captures the "Loading..." state), and the "Look at" hints describe what those pages show.

## Ports and one network at a time

| Port | Service | Profile / file |
|---|---|---|
| 18081, 18082, 18083 | `node1` (mines, pays the coinbase), `node2`, `node3` | base |
| 18084 | `snap1` (snapshots and pruning) | `--profile snapshots` |
| 18091, 18092 | `poa1`, `poa2` (Proof of Authority) | `--profile poa` |
| 18080 | `dashboard` (read-only network view; `NODES` names the nodes it reads) | `--profile dashboard` |
| 18443, 18444 | `caddy` (TLS proxy for the node API and for the dashboard) | `compose.tls.yml`, separate project `bwg-tls` |
| 19090 | `prometheus` | `--profile observability` |
| 13000 | `grafana` | `--profile observability` |

Start the dashboard next to the network with `dc --profile dashboard up -d` (`dc` is defined below) and open `http://127.0.0.1:18080/`; see "Network dashboard" below.

The base network, the e2e test (`go test -tags e2e ./e2e/...`) and the playground all use 18081-18083, so **only one of them can run at a time**. Stop the others first (`docker compose down -v` for the base network; the e2e test tears down its own `bwg-e2e` project).

Verified: 2026-10-31, commit 9c41517.

## Commands used in this page

The compose project is `bwg-playground`. These shell functions keep the commands short; define them once per terminal from the repository root:

```shell
dc() { docker compose -p bwg-playground --env-file .playground/compose.env -f docker-compose.yml -f compose.playground.yml "$@"; }
export WALLET_PASSWORD=choose-a-password   # unlocks the keystores; never printed or stored by the scripts
B=.playground/bwg                          # the bwg binary built by scripts/playground.sh
N=http://127.0.0.1
```

Verified: 2026-10-31, commit 9c41517.

## Start and fund

```shell
$ scripts/playground.sh        # 3 blocks by default; scripts/playground.sh 10 mines 10; scripts/playground.sh 0 mines none
```

The script builds `.playground/bwg`, creates the miner keystore `.playground/miner.key` (only the first time), writes `.playground/compose.env` with `MINER_ADDRESS`, brings the three nodes up with `--build`, waits for `GET /health`, asks node1 to mine, and prints:

```
block 1 65a2adfce7d9c3aa3456304f877ac5fa0ee72374fc98eed341c5d5d512ab495a txs=1
block 2 4c8266933acf638462cacc893c07997f90ce1256b178065ab8b832a38ac7fb9f txs=1
block 3 37d7d7d20759f1aa99d8331923c53a7873b92918991f4919b7fb16a8aa3d40eb txs=1
miner address: 00f0b32cf1353ad2612aa9cbf84a107d1a8d20aa8f272df205
balance on 127.0.0.1:18081: 15000000000
balance on 127.0.0.1:18082: 15000000000
balance on 127.0.0.1:18083: 15000000000
```

The genesis is empty (no `GENESIS_ALLOCATIONS`); the only coins come from the coinbase of the mined blocks: 3 × 5,000,000,000 base units. Re-running the script reuses the miner keystore and the existing volumes.

Look at: `http://127.0.0.1:18081/explorer/` shows "Latest blocks" with the tip height and one row per block (height, short hash, time, number of transactions); `1` in the Txs column is the coinbase. Open a block to see its transactions: the coinbase has `0` inputs and `1` output paying the miner address.

Verified: 2026-10-31, commit 9c41517.

## Start from a truly empty chain

Reset first (see "Reset" below: `dc --profile '*' down -v --remove-orphans`), then:

```shell
$ scripts/playground.sh 0
miner address: 00c23b7d77761a011fdd6c61a0297f0fb8400a213eb887f7c1
balance on 127.0.0.1:18081: 0
balance on 127.0.0.1:18082: 0
balance on 127.0.0.1:18083: 0
```

With `0` the script does everything except mining: it builds `.playground/bwg`, creates the miner wallet, writes `.playground/compose.env`, starts the three nodes and waits for `/health`. It never calls `bwg miner`. An argument that is not a non-negative integer (`abc`, `-1`, `1.5`) prints a usage message on stderr and exits with status 2 before anything is built or started.

The script runs this compose command, which you can also run yourself once `.playground/compose.env` exists (the compose file refuses to start without `MINER_ADDRESS`):

```shell
$ docker compose -p bwg-playground --env-file .playground/compose.env -f docker-compose.yml -f compose.playground.yml up -d --build
```

The three nodes are at height 0 (the genesis block only, `tip` is empty):

```shell
$ curl -s $N:18081/health
{
  "status": "ok",
  "height": 0,
  "tip": ""
}
```

18082 and 18083 print the same. Mine the first block on node1 and look at the balance on all three nodes:

```shell
$ $B miner --node $N:18081 --count 1
block 1 458866542d238c908b14b02652a382124c67d84268b8ab4a073ac73b991270d5 txs=1
$ $B wallet balance --node $N:18081 $(cat .playground/miner.addr)
5000000000
```

After about 3 seconds (gossip) the balance is `5000000000` on 18082 and 18083 too. `txs=1` is the coinbase.

> **Do not use `--count 0` to mean "no blocks".** `bwg miner --count 0` mines until `Ctrl+C`. That is why `scripts/playground.sh` handles `0` itself and never passes it to the miner.

### Who pays the coinbase

Only node1 has `MINER_ADDRESS`, so only node1 pays a coinbase. Mining one block through node2 and one through node3 on the same network (after the block above) gave:

```shell
$ $B miner --node $N:18082 --count 1
block 2 4e6f5bf5373882cbb875ff79dcb9e0bc18263b44d7684b27711239010699ea49 txs=0
$ $B miner --node $N:18083 --count 1
block 3 6e1859f09f949a4f1193ea32897da3dc375e012f47e3df05074c475bf1ab3ef0 txs=0
$ $B wallet balance --node $N:18081 $(cat .playground/miner.addr)
5000000000
```

Both blocks have `txs=0` (their `Transactions` is `[]`, there is no coinbase) and the miner balance did not change (`5000000000` on all three nodes, height 3 everywhere). The other nodes accept these blocks.

Nodes that only receive blocks by gossip do not emit `/events` for them until roadmap item 2 lands.

## Health, convergence and peers

```shell
$ curl -s $N:18081/health
{
  "status": "ok",
  "height": 3,
  "tip": "37d7d7d20759f1aa99d8331923c53a7873b92918991f4919b7fb16a8aa3d40eb"
}
```

Run it on 18082 and 18083: the three nodes converge when `height` and `tip` are equal. Peers:

```shell
$ curl -s $N:18081/p2p/peers
{
  "peers": [
    { "node_id": "1cb9027ecaae31ad309e71e7f2050b49", "addr": "http://node2:8080", "height": 3,
      "tip_hash": "37d7d7d2...", "inbound": true, "last_seen": "2026-10-31T19:12:07Z" },
    { "node_id": "b2fe3e33a1acfdb436f017dff7b306b4", "addr": "http://node3:8080", "height": 3,
      "tip_hash": "37d7d7d2...", "inbound": true, "last_seen": "2026-10-31T19:12:07Z" }
  ],
  "addrs": [ "http://node2:8080", "http://node3:8080" ]
}
```

(Shortened here; the real output is indented JSON.) The seeds are node2 → node1 and node3 → node2, and gossip fills in the rest. `GET /peers` does not exist; the route is `/p2p/peers`.

Verified: 2026-10-31, commit 9c41517.

## Mining and coinbase

`miner` calls `POST /mine`; `--count 0` mines until `Ctrl+C`, `--interval 2s` pauses between blocks. `txs` counts the coinbase plus the mempool transactions the block took (the `Wallet` section below shows a block with two: the coinbase plus one transfer). Only node1 has `MINER_ADDRESS`: a block mined on node2 has no coinbase (its `Transactions` is `[]`) and all nodes accept it. This is block 4 of the run, mined on node2 right after the three of the start:

```shell
$ curl -s -X POST $N:18082/mine | head -c 300
{
  "Index": 4,
  "Timestamp": "2026-10-31T19:12:07.675370553Z",
  "Transactions": [],
  "MerkleRoot": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
  "Hash": "572e5364f99180b7bfe14784ddd0a8f27599aaaaef1c218f7fb65e3887a7040d",
  "PrevHash": "37d7d7d20759f1aa99d8331923c53a7873b9
$ curl -s $N:18081/health
{
  "status": "ok",
  "height": 4,
  "tip": "572e5364f99180b7bfe14784ddd0a8f27599aaaaef1c218f7fb65e3887a7040d"
}
```

(`PrevHash` is the tip of the previous section; node1 already reports the new tip.) Look at: the block page in the explorer; the coinbase is the transaction at position 0 (`0` inputs, `1` output) of a block mined on node1,.

Verified: 2026-10-31, commit 9c41517.

## Wallet: new, balance, send

```shell
$ $B wallet new --keystore .playground/alice.key
00dca668461fe49762d03394953b8bfaa07ef43ea7a7457cce
$ $B wallet new --keystore .playground/bob.key
0017f3b2a41a3507f96fcf632030b15413f55cc0fc0b6105f6
$ $B wallet send --node $N:18081 --keystore .playground/miner.key --to 00dca668461fe49762d03394953b8bfaa07ef43ea7a7457cce --amount 1000 --fee 10
35df45a6e35f385f40fc2109f84c14d9c3b9ce76db9f87fb88a9071e94c56a46
$ $B miner --node $N:18081 --count 1
block 5 2ce382d887a12e15c70f19780ed909780e926660831938c15cbb6b227084da43 txs=2
$ $B wallet balance --node $N:18083 00dca668461fe49762d03394953b8bfaa07ef43ea7a7457cce
1000
```

`wallet new` refuses to overwrite a keystore and needs `WALLET_PASSWORD`. `wallet send` prints the transaction ID once the node has put it in the mempool; the balance only changes after a block confirms it (the 5th block of the run, above, holds the coinbase and this transaction, hence `txs=2`). The fee is mined into the coinbase of node1, so the miner address receives it back. Look at: the address page of Alice in the explorer (`Balance` and the table "Unspent outputs" with transaction, index and amount), and a transaction page, whose "Inputs" table links each input to the transaction whose output it spends (the "Block" row links to the block).

Verified: 2026-10-31, commit 9c41517.

## Mempool and fee-rate ordering

Send two transactions from two funded wallets (a wallet that holds one confirmed output can have one pending spend at a time, see the next section). Bob needs coins first:

```shell
$ $B wallet send --node $N:18081 --keystore .playground/miner.key --to 0017f3b2a41a3507f96fcf632030b15413f55cc0fc0b6105f6 --amount 1000000 --fee 1
d14ddbec21fb7140420f6a93629e8552cef8c4e596e3f00f0880c888ad68269c
$ $B miner --node $N:18081 --count 1
block 6 09cfbd36d2d7743aab97ee4b9c18f2c0cfc189469ddf40cdd4c54df2a40c5045 txs=2
$ $B wallet send --node $N:18081 --keystore .playground/alice.key --to 0017f3b2a41a3507f96fcf632030b15413f55cc0fc0b6105f6 --amount 100 --fee 5
0f9836bbc1efe899a12559e800830a1b6b3e10ea77478b0e882234199be624cc
$ $B wallet send --node $N:18081 --keystore .playground/bob.key --to 00dca668461fe49762d03394953b8bfaa07ef43ea7a7457cce --amount 200 --fee 500
20510639b8540fc3e615a3db89d37f022386cac3d8e4edca27a7a91a28863aaf
$ curl -s $N:18081/mempool | grep -E '"ID"|"Amount"'
      "ID": "20510639b8540fc3e615a3db89d37f022386cac3d8e4edca27a7a91a28863aaf",
          "Amount": 200,
          "Amount": 999300,
      "ID": "0f9836bbc1efe899a12559e800830a1b6b3e10ea77478b0e882234199be624cc",
          "Amount": 100,
          "Amount": 895,
$ $B miner --node $N:18081 --count 1
block 7 2cbf371f943536f38df61cc85e0e675d2c2da3c5d356ac47ae39594a2ef481a7 txs=3
$ curl -s $N:18081/mempool
{
  "transactions": []
}
```

`GET /mempool` lists pending transactions by fee rate (fee per byte), highest first: Bob's fee 500 comes before Alice's fee 5 although Alice sent first. The block takes both (`txs=3` with the coinbase) and the pool empties. Balances after: Alice `1095`, Bob `999400` (`wallet balance` on the same node).

Verified: 2026-10-31, commit 9c41517.

## Double-spend rejection

A second spend of the same output while the first is pending is refused. Send from the miner keystore, and before mining its block send again from the same keystore (Alice's address is the one of the Wallet section):

```shell
$ $B wallet send --node $N:18081 --keystore .playground/miner.key --to 00dca668461fe49762d03394953b8bfaa07ef43ea7a7457cce --amount 1000 --fee 10
9e06b0ffa5bcb30e7c63cc4af31a123b03f3a21aa451ecab8abd654c144bb383
$ $B wallet send --node $N:18081 --keystore .playground/miner.key --to 00dca668461fe49762d03394953b8bfaa07ef43ea7a7457cce --amount 2000 --fee 50
Error: node answered 409: mempool: transaction spends an outpoint already spent in the pool
$ curl -s $N:18081/mempool | grep -E '"ID"'
      "ID": "9e06b0ffa5bcb30e7c63cc4af31a123b03f3a21aa451ecab8abd654c144bb383",
$ $B miner --node $N:18081 --count 1
block 8 239d8da789e1fc803a807d6cefe82cc0d6c3021fe3a8d863e825f43cc9dcd248 txs=2
```

`wallet send` picks the same first confirmed output both times, so the second transaction conflicts (`409 mempool_conflict`; first seen wins, there is no replace-by-fee). After a block confirms the first one, the same output is spent and a transaction using it would fail validation. Look at: `GET /mempool` still holds only the first transaction (above), and the block that mines it has `txs=2`: the coinbase and that one transfer.

Verified: 2026-10-31, commit 9c41517.

## Contracts (deploy and call)

`bwg contract deploy|call|show` builds, signs and sends the contract transactions ([ADR-0037](adr/0037-contract-cli.md)); the four example contracts are in [`examples/contracts/`](../examples/contracts/README.md). The signing wallet pays from its confirmed coins, so use the funded miner keystore. The command lines use the flags of `e2e/contract_cli_test.go:TestContractCLI`, with `--keystore` where the test sets `WALLET_KEYSTORE`.

Deploy `adder` (`storage[0] += args[0]`) on node1, mine it, call it twice with a block each, then read it on node3, a node that did not mine:

```shell
$ ADDER=$($B contract deploy --node=$N:18081 --code-file=examples/contracts/adder.hex --gas-limit=100 --keystore=.playground/miner.key)
$ echo $ADDER
4a098c86a9102e8a8dd0ce3a6744ab14cd2aaffbac5e28130169e2aff7ae4748
$ $B miner --node $N:18081 --count 1
block 9 3bc35f81228bda44ba06e8abb13249a020ed63119cf54687a12887a8e960356c txs=2
$ $B contract call --node=$N:18081 --target=$ADDER --gas-limit=100 --args=7 --keystore=.playground/miner.key
155aa9c5301a62c0302fc198c44dad95299a3f3002e194aa762868f605f487a0
$ $B miner --node $N:18081 --count 1
block 10 007233ac832b844e7c4357df2e093dae16dfaa2332ac4f80b0f4d3f6efa0b5c2 txs=2
$ $B contract call --node=$N:18081 --target=$ADDER --gas-limit=100 --args=5 --keystore=.playground/miner.key
e7c044478d95b1daa7b40af57bf308b84a5c5dc4cf4132927b26c4d869b82aad
$ $B miner --node $N:18081 --count 1
block 11 0fef34499c863931ff7ba93865a6df1bc78b09226692f3680fc4cbe3e291678e txs=2
$ $B contract show --node=$N:18083 $ADDER
{
  "id": "4a098c86a9102e8a8dd0ce3a6744ab14cd2aaffbac5e28130169e2aff7ae4748",
  "code": "0100000000000000003001000000000000000040100100000000000000003100",
  "storage": {
    "0": 12
  }
}
```

`deploy` and `call` print the transaction ID once the node has put it in the mempool; the contract ID is the ID of the deploy transaction. The state changes only when a block confirms the transaction (`txs=2`: the coinbase and the contract transaction), so `storage` is `{"0": 12}` (7 plus 5) after the second call. `GET /contracts/{id}` answers the same JSON on every node.

Deploy `counter` and `guard` (blocks 12 and 13), which the failure cases below need:

```shell
$ COUNTER=$($B contract deploy --node=$N:18081 --code-file=examples/contracts/counter.hex --gas-limit=100 --keystore=.playground/miner.key)
$ $B miner --node $N:18081 --count 1
block 12 0482ed454cf1f904e11bb4757284351c3d832d91fdc2e87fafeaea498a446815 txs=2
$ GUARD=$($B contract deploy --node=$N:18081 --code-file=examples/contracts/guard.hex --gas-limit=100 --keystore=.playground/miner.key)
$ $B miner --node $N:18081 --count 1
block 13 44f46fb1d713d2f2e360685250cbc990a8366e48009d4c4bd60b8741878704b5 txs=2
```

### Failure cases

A call that cannot run is refused when it is sent: `bwg` exits with status 1, prints the node's answer on stderr and nothing reaches the mempool. The HTTP code is in the node's JSON (`curl -s -X POST` shows it); `bwg` prints only the status and the message.

```shell
$ $B contract call --node=$N:18081 --target=$COUNTER --gas-limit=28 --keystore=.playground/miner.key
Error: node answered 422: transaction 0: vm: out of gas
$ $B contract call --node=$N:18081 --target=$GUARD --gas-limit=100 --args=0 --keystore=.playground/miner.key
Error: node answered 422: transaction 0: vm: reverted
$ $B contract call --node=$N:18081 --target=0000000000000000000000000000000000000000000000000000000000000000 --gas-limit=100 --keystore=.playground/miner.key
Error: node answered 422: transaction 0: vm: unknown contract
```

- `out_of_gas`: `counter` needs 29 gas and the limit is 28.
- `reverted`: `guard` reverts when `args[0]` is 0.
- `unknown_contract`: the ID was never deployed.

A deploy that is still in the mempool is not a contract yet. Deploy `capped` and call it before mining the deploy:

```shell
$ CAPPED=$($B contract deploy --node=$N:18081 --code-file=examples/contracts/capped.hex --gas-limit=100 --keystore=.playground/miner.key)
$ $B contract call --node=$N:18081 --target=$CAPPED --gas-limit=100 --keystore=.playground/miner.key
Error: node answered 422: transaction 0: vm: unknown contract
$ $B miner --node $N:18081 --count 1
block 14 3be40ef0c312c6f1b9914b43fe07bc94e473d380d78cff8657da27f5766ef31b txs=2
$ $B contract show --node=$N:18081 $CAPPED
{
  "id": "0964b9964d88afe7488c1acfeb2f808de265b1253787c7b7b1c53bf44b4f4079",
  "code": "0100000000000000003003010000000000000003131501000000000000003521010000000000000001100100000000000000003100ff",
  "storage": {}
}
```

The same call works after block 14. A call takes at most 256 `--args` (ADR-0033); with 257 values the node answers `400 invalid_contract_op` (`MANY` is `1,1,...,1`, 257 times; the output is cut here):

```shell
$ $B contract call --node=$N:18081 --target=$COUNTER --gas-limit=100 --args=$MANY --keystore=.playground/miner.key
Error: node answered 400: transaction 0: invalid contract operation
```

`contract show` of an ID that is not deployed is a `404`, and so is the `curl` of the same route:

```shell
$ $B contract show --node=$N:18081 0000000000000000000000000000000000000000000000000000000000000000
Error: node answered 404: contract not found
$ curl -s $N:18081/contracts/0000000000000000000000000000000000000000000000000000000000000000
{
  "error": "contract not found",
  "code": "contract_not_found"
}
```

`contract_exists` (a second deploy of a contract that exists) cannot be provoked: a resubmitted deploy fails on its spent inputs first, before the contract rules run. Not reproducible by hand; covered by internal/vm/state_test.go:"deploy of an existing contract in the view" (run by `TestVM` in `internal/vm/vm_suite_test.go`).

The contracts, their opcodes and gas are in [`examples/contracts/README.md`](../examples/contracts/README.md); the VM is ADR-0014. The block heights above (9 to 14) continue in the next sections.

Verified: 2026-11-08, commit f3bd815.

## Version (`bwg version`)

```shell
$ $B version
bwg dev (commit none, built unknown)
```

`scripts/playground.sh` builds with a plain `go build`, so the version, commit and date are the defaults. A release binary has them filled in by the build (ADR-0036: goreleaser sets them, and the release publishes SBOMs, signed checksums and a signed image).

Verified: 2026-11-08, commit f3bd815.

## Supply (`GET /supply`)

```shell
$ curl -s $N:18081/supply
{
  "height": 14,
  "issued": "70000000000",
  "initial_subsidy": "5000000000",
  "halving_interval": 210000
}
```

`issued` is the sum of the block subsidy over heights 1 to `height` (ADR-0027): 14 blocks of 5,000,000,000 is 70,000,000,000. It is what the rules allow the chain to issue, not what was paid: block 4 was mined on node2, which has no `MINER_ADDRESS`, so it paid no coinbase. The coinbase total of this run is 13 blocks (all but block 4) × 5,000,000,000 = 65,000,000,000 plus the fees of the transfers, 526; the miner has spent 1,002,021 (three transfers and their fees), so its balance is:

```shell
$ $B wallet balance --node $N:18081 $(cat .playground/miner.addr)
64998998505
```

65,000,000,526 - 1,002,021 = 64,998,998,505. Every node answers the same `/supply` at the same height.

Verified: 2026-11-08, commit f3bd815.

## Verify the stored chain (`bwg node --verify`)

`--verify` replays the stored chain under the full block rules (consensus, size, Merkle root, timestamps, UTXO rules, signatures, contracts) on scratch state, writes nothing, and runs before the node opens a port (ADR-0035). It needs the node's `chain.db`, so stop node3 and run it on node3's volume, with the port published to look at the node afterwards:

```shell
$ dc stop node3
$ dc run -d --service-ports node3 node --verify
$ docker logs <container> 2>&1
{"time":"2026-11-08T15:01:27.977844109Z","level":"INFO","msg":"verified stored chain","height":14}
{"time":"2026-11-08T15:01:27.978152207Z","level":"INFO","msg":"listening","addr":":8080"}
$ curl -s $N:18083/health
{
  "status": "ok",
  "height": 14,
  "tip": "3be40ef0c312c6f1b9914b43fe07bc94e473d380d78cff8657da27f5766ef31b"
}
$ docker rm -f <container>; dc start node3
```

`verified stored chain` carries the tip height (14) and comes before `listening`: the listener opens only after the replay passes, and a failing replay stops the node with a non-zero exit instead. At 15 blocks the replay takes 0.3 ms here, so by hand you cannot catch the node answering `/health` during it; the log order is the evidence. Without `--service-ports`, `dc run` publishes no port. The flag cannot be combined with `--reindex` or `--snapshot-*`. A tampered `chain.db` that fails the replay is covered by the tests of `chain.(*Service).Verify`, not run here.

Verified: 2026-11-08, commit f3bd815.

## `MAX_BLOCK_BYTES` is refused (ADR-0032)

The block size limit is the 1 MiB consensus constant `domain.MaxBlockBytes`, not a setting. A node started with `MAX_BLOCK_BYTES` set to a non-blank value stops at once instead of ignoring it (node3 is stopped first; it does not start):

```shell
$ dc stop node3
$ dc run --rm -e MAX_BLOCK_BYTES=512 node3 node
{"time":"2026-11-08T15:01:01.018539216Z","level":"ERROR","msg":"node stopped","error":"MAX_BLOCK_BYTES is no longer supported: the block size limit is the 1048576-byte consensus constant domain.MaxBlockBytes (ADR-0032); unset the variable"}
$ echo $?
1
$ dc start node3
```

Unset or blank, the variable has no effect.

Verified: 2026-11-08, commit f3bd815.

## Median time past (ADR-0034)

A block's timestamp must be strictly later than the median of the timestamps of the last 11 blocks (fewer when the chain is shorter) and at most 2 hours ahead of the validator's clock. It may be earlier than its parent: one block with a wrong clock cannot hold honest miners back, because the miner stamps `max(now, median + 1ns)`. A block that breaks the rule is refused with `timestamp_not_after_median_time_past`. The shipped commands always stamp the current time, so no command here can produce such a block.

Not reproducible by hand; covered by internal/domain/rules_test.go:"rejects a timestamp equal to the median time past" (run by `TestDomain` in `internal/domain/domain_suite_test.go`).

## SSE events (`/events`)

```shell
$ curl -sN $N:18081/events        # terminal 1
$ $B miner --node $N:18081 --count 1   # terminal 2
block 15 48851f270a13556016db8c22e7a6ea8fd1d56b0fdae8236ce1aedea5059c9767 txs=1
```

To see the `event: tx` too, send a transfer from a third terminal before mining (to Alice's address of the Wallet section; use yours):

```shell
$ $B wallet send --node $N:18081 --keystore .playground/miner.key --to 00c994aebc7e17520985e0eccdcb4d985bb3b0a4de81cd08cc --amount 1 --fee 1
587847028a7427cef96a8f6c67a3087a28c68f31d4889d3bbc30c2d938e5b6ac
$ $B miner --node $N:18081 --count 1
block 16 2b3de0fd6ae349834a6690ab22c35ebc10185516e176888f3e704bcacfbe9cb0 txs=2
```

Terminal 1 prints one `event: block` for block 15, then one `event: tx` with a single-line `data:` JSON when the transfer is accepted and one `event: block` for block 16 (the JSON lines are shortened here):

```
event: block
data: {"Index":15,"Timestamp":"2026-11-08T15:01:42.738344456Z","Transactions":[{"ID":"13484531f9d55ef5c8799a90077040601e8d7f7f239aca7d798e1be68f311e57","Inputs":null,"Outputs":[{"Amount":5000000000,"A...

event: tx
data: {"ID":"587847028a7427cef96a8f6c67a3087a28c68f31d4889d3bbc30c2d938e5b6ac","Inputs":[{"PrevTxID":"0964b9964d88afe7488c1acfeb2f808de265b1253787c7b7b1c53bf44b4f4079","OutIndex":0,"PublicKey":"b6ac2c...

event: block
data: {"Index":16,"Timestamp":"2026-11-08T15:01:42.819410546Z","Transactions":[{"ID":"20000062b35eda8710f7eff238c2a9a0a8c0f923b0d90685149afa91957fab99","Inputs":null,"Outputs":[{"Amount":5000000001,"A...
```

The coinbase of block 16 pays `5000000001`: 5,000,000,000 plus the fee of 1. Every node sends the event for each block it appends, whatever the source: a block mined on node1 is also one `event: block` (same `Hash`) on node2 and node3, through gossip, sync or reorg. The explorer uses this stream to add new blocks at the top without polling. Known limitation seen while writing this page: the node sends the response headers of `/events` together with the first event (`curl -siN $N:18081/events` prints nothing on an idle node), so the explorer's status label stays "connecting" until the first block or transaction arrives and only then changes. This is node behaviour, not something the playground changes.

Verified: 2026-11-08, commit f3bd815.

## Metrics (`/metrics`)

```shell
$ curl -s $N:18081/metrics | grep -E '^(# (HELP|TYPE) )?(http_|blockchain_)'
# HELP blockchain_blocks Number of blocks in the chain.
# TYPE blockchain_blocks gauge
blockchain_blocks 17
# HELP blockchain_mining_duration_seconds Time spent mining blocks that were produced.
# TYPE blockchain_mining_duration_seconds summary
blockchain_mining_duration_seconds_sum 0.03408702
blockchain_mining_duration_seconds_count 15
# HELP http_requests_total HTTP requests served, by method, route pattern and status code.
# TYPE http_requests_total counter
http_requests_total{code="200",method="GET",route="GET /balance/{addr}"} 22
http_requests_total{code="200",method="GET",route="GET /contracts/{id}"} 1
http_requests_total{code="200",method="GET",route="GET /headers"} 2
http_requests_total{code="200",method="GET",route="GET /health"} 6
http_requests_total{code="200",method="GET",route="GET /mempool"} 4
http_requests_total{code="200",method="GET",route="GET /p2p/peers"} 574
http_requests_total{code="200",method="GET",route="GET /supply"} 1
http_requests_total{code="200",method="POST",route="POST /p2p/block"} 2
http_requests_total{code="200",method="POST",route="POST /p2p/hello"} 574
http_requests_total{code="200",method="POST",route="POST /p2p/inv"} 13
http_requests_total{code="201",method="POST",route="POST /mine"} 15
http_requests_total{code="202",method="POST",route="POST /tx"} 12
http_requests_total{code="400",method="POST",route="POST /tx"} 1
http_requests_total{code="404",method="GET",route="GET /contracts/{id}"} 2
http_requests_total{code="409",method="POST",route="POST /tx"} 1
http_requests_total{code="422",method="POST",route="POST /tx"} 4
```

This is after the SSE run above: `blockchain_blocks` is 17 (genesis plus 16 blocks) and the 15 mined blocks are the ones node1 mined itself (block 4 came from node2). The counters are per node: the double spend shows up as `code="409"`; the unknown contract `GET`s as `code="404"` (the `curl` and `contract show` of the Contracts section); the refused contract calls as `code="422"` (out of gas, reverted and two unknown contracts) and `code="400"` (the 257 arguments). The `/p2p` counts depend on how long the run took. Families are sorted by name and labels by name, and the same endpoint also serves the Go runtime (`go_*`) and process (`process_*`) metrics, left out of the `grep` above ([ADR-0022](adr/0022-prometheus-client-golang.md)).

Verified: 2026-11-08, commit f3bd815.

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
"instance":"node2:8080"
"health":"up"
"instance":"node3:8080"
"health":"up"
"instance":"node1:8080"
"health":"up"
$ curl -s -o /dev/null -w '%{http_code}\n' 127.0.0.1:13000/api/dashboards/uid/bwg-network
200
$ curl -s '127.0.0.1:13000/api/datasources/proxy/uid/prometheus/api/v1/query?query=blockchain_blocks' | grep -o '"instance":"[^"]*"'   # 3 series
"instance":"node3:8080"
"instance":"node2:8080"
"instance":"node1:8080"
$ $B miner --node $N:18081 --count 1                  # all three values rise within 30 s
block 17 768c3aff28255f649c6fc07bae33d8160dea5691dfec44f07bde839d4047ce21 txs=1
$ curl -s '127.0.0.1:13000/api/datasources/proxy/uid/prometheus/api/v1/query?query=blockchain_blocks' | grep -o '"value":\[[^]]*\]'
"value":[1794150134.533,"18"]
"value":[1794150134.533,"18"]
"value":[1794150134.533,"18"]
```

With `compose.tuning.yml`, node1 has `RATE_LIMIT=30` and a scrape every 5 s uses 12 of those requests per minute (scrapes are rate limited like any other request).

Verified: 2026-11-08, commit f3bd815.

## Network dashboard (`--profile dashboard`)

A read-only service next to the three nodes (ADR-0023). It reads `node1..node3` by service name with a fixed list of `GET` requests, renders pages with `html/template` and refreshes panels with htmx; it holds no key, no volume and cannot mine or send. Each node still serves its own explorer at `/explorer/`; the dashboard is the view across nodes.

Start it with the network up (`scripts/playground.sh` first), then open `http://127.0.0.1:18080/`:

```shell
$ dc --profile dashboard up -d --build dashboard
$ curl -s -o /dev/null -w "%{http_code}\n" $N:18080/
200
```

Screens (the top bar links them; the search box takes a height, a block or transaction hash, or an address, and a node):

| Screen | Path | What it shows |
|---|---|---|
| Network | `/` | the banner with Height, Agree and Reachable, and a comparison matrix with one column per node (status, height, tip, peers, last block, peer addresses; a tip that differs from the majority is marked ≠). Below the matrix, **Chain per node** lines up the last 8 blocks of every node by height (see below). The sidebar lists the nodes with their state on every screen. The poller reads the nodes every 2 s; opening the page never reads a node |
| Peers | `/peers` | connected and known peers of every node |
| Metrics | `/metrics` | key figures per node and two charts, below |
| Node | `/n/<node>/` | links to Mempool, Blocks and Peers of one node |
| Mempool, Blocks | `/n/<node>/mempool`, `/n/<node>/blocks` | pending transactions by fee rate, with fee and fee rate per row; the chain, 20 blocks per page |
| Block, transaction, address, contract | `/n/<node>/block/<height or hash>`, `/tx/<id>`, `/address/<addr>`, `/contract/<id>` | detail views, with the Merkle proof of a transaction and the unspent outputs of an address |

The pages work in light and dark (`prefers-color-scheme`), use no inline script or style and run under the same content policy as the node explorer.

### Consensus screen

`/consensus` shows the consensus engine of every node with its parameters, and a "same configuration" check that says whether all the nodes run with the same settings. For each node it lists the last 10 blocks with their difficulty and block work (2^difficulty), then the chain work of each node and, when the nodes are on different chains, the heaviest node on the fork. The screen reads `GET /consensus` on each node (ADR-0026), so it needs nodes that serve that route. Under Proof of Authority there is no turn order: any validator of the set may seal the next block.

### The banner

| Banner | Meaning |
|---|---|
| `Network in agreement` | every reachable node reports the same height and tip |
| `Network syncing` | reachable nodes report different heights (the column of a node that trails says `behind by N`), or the same height with different tips for fewer than three polls |
| `Fork: nodes report different tips at height H` | the same height with different tips on three consecutive polls (a single poll is only a block in flight) |
| `No node reachable` | no node answered `/health` |
| `... · N of M unreachable` | added to any of the first three when some nodes did not answer; an unreachable node keeps its last known height in the matrix |

### Chain per node

One lane per node, in the order of `NODES`, over the same heights: the last 8 up to the highest reachable node, so a block sits under the blocks of the other nodes at the same height. Each block shows its height and the start of its hash and opens the block page of that node.

| Mark | Meaning |
|---|---|
| `●` green border | the node's tip, at the top height |
| `▲` amber border | the node's tip, below the top height: the node is behind |
| `⑂` red | the block differs from the chain of the majority tip at that height. A node's own tip at the top height turns red only once the banner says Fork; before that, two nodes may just have mined at once |
| dashed, empty | a height the node does not have yet |
| dashed, grey | the last known blocks of a node that does not answer |

In the walkthrough below, after `dc stop node3` node3's lane keeps its blocks up to 17, dashed, and shows the new heights as empty while the others move on.

### Metrics screen

The table reads each node's `/metrics`: `blockchain_blocks`, the mean mining time (`blockchain_mining_duration_seconds` sum divided by count, `n/a` while the node mined nothing, as on node3, which only receives blocks; node2 shows a value because it mined block 4 in the Mining section) and `http_requests_total` added up by status class (`4xx` leaves out `429`, which has its own column). A node that does not answer, or whose text does not parse, shows `n/a` on its row; the other rows are not affected. The page refreshes the table every 2 s. Compare it with the node itself (the request counters keep growing with the peer traffic and with the dashboard's own reads, so compare them at one instant; blocks and mining time only move when a block is mined):

```shell
$ curl -s $N:18081/metrics | grep -E '^(blockchain_blocks|blockchain_mining_duration_seconds_(sum|count)) '
blockchain_blocks 18
blockchain_mining_duration_seconds_sum 0.035229558
blockchain_mining_duration_seconds_count 16
$ curl -s $N:18080/fragments/metrics | grep '<td class="col-main">node1</td>'
<tr><td class="col-main">node1</td><td class="col-num">18</td><td class="col-num">0.002202 s</td><td class="col-num">1476</td><td class="col-num">8</td><td class="col-num">0</td><td class="col-num">0</td></tr>
```

(0.035229558 / 16 = 0.002202 s; the eight `4xx` are the `409`, the two `404`, the four `422` and the `400` of the sections above.) Below the table, every 10 s:

- **Block interval**: one bar per pair of consecutive blocks among the last 50 headers of the node chosen in the selector (one `GET /headers` per refresh); the scale is the largest bar, printed top left. In the playground the first bar is the time from the fixed genesis (`GENESIS_TIME` in `docker-compose.yml`) to block 1, which dwarfs the others until more blocks are mined.
- **Transactions per block**: one bar per block among the last 50 blocks of the node chosen in the selector, the same node as the block interval.
- **Difficulty**: one bar per block among the last 50 blocks of the same node, with the largest value printed on the chart. When the node has too few blocks, or does not answer, the chart shows a message in its place.
- **Height per node**: one line per node over the poller's last 60 polls (about two minutes), on one shared scale. The history lives in the dashboard process and starts empty on restart; reading it costs no request to the nodes.

For time series, retention and alerts use the `observability` profile (Prometheus and Grafana, below), not the dashboard.

### Node page, search and transactions

- **Node page** `/n/<node>/`: below the stats, the 10 latest blocks and up to 10 of their transactions, newest block first. This is one read of the node; nothing is read before the dashboard's first poll of that node.
- **Search**: case-insensitive, so `ABC...` and `abc...` find the same thing. The dashboard tries a height, then a transaction ID, then a block hash, then an address, and goes to the first one the node answers. A value the node does not have shows "Not found" (HTTP 404) on the search page, which stays open with the query in it.
- **Transaction page** `/n/<node>/tx/<id>`: the fee is the sum of the inputs minus the sum of the outputs, in coins. A coinbase or genesis transaction has no fee, and the page says so; if a spent output cannot be read, the page says the fee is unknown instead of showing a wrong number.
- **Pending transaction**: the transaction page shows its position in the selection order, for example `Pending: position 2 of 5`, linked to the mempool. The mempool lists `#1..#n` in that order, highest fee rate first, which is the order mining takes them.
- **Fee rate**: each mempool row shows its fee and its fee rate, the fee divided by the size of the transaction in bytes (the length of its JSON), in base units per byte, rounded down to two decimals. The node reports fee and size in the `entries` of `GET /mempool`; the dashboard divides. The page of a pending transaction shows the same rate; a confirmed one says the rate is shown only while pending. In the fee-rate walkthrough above, Bob's row comes first with the higher rate.
- **Not shown**: the fee rate of a confirmed transaction (the node reports sizes for its pool only); the total supply; and the richest addresses (they need data the node does not keep).

### Walkthrough: stop node3

The commands read the dashboard's network panel and keep the banner title and the status of each node (the matrix has one status cell per node, in the order node1, node2, node3):

```shell
$ curl -s $N:18080/fragments/network | grep -oE 'banner-title">[^<]*|</span>[^<]*</span></td>'
banner-title">Network in agreement
</span>up to date</span></td>
</span>up to date</span></td>
</span>up to date</span></td>
$ dc stop node3
 Container bwg-playground-node3-1 Stopped
$ curl -s $N:18080/fragments/network | grep -oE 'banner-title">[^<]*|</span>[^<]*</span></td>'
banner-title">Network in agreement · 1 of 3 unreachable
</span>up to date</span></td>
</span>up to date</span></td>
</span>unreachable, last known height 17</span></td>
$ $B miner --node $N:18081 --count 2
block 18 66737533767c30d0f209d026242f5669d16d9cf97746c1fce8942543b68aaff1 txs=1
block 19 1e5748edef5196218be37c0a4ebf88a733830481cad9a467ff352d2d359b2a62 txs=1
$ curl -s $N:18080/fragments/network | grep -oE 'banner-title">[^<]*|</span>[^<]*</span></td>'
banner-title">Network in agreement · 1 of 3 unreachable
</span>up to date</span></td>
</span>up to date</span></td>
</span>unreachable, last known height 17 (behind by 2)</span></td>
$ dc start node3
 Container bwg-playground-node3-1 Started
$ curl -s $N:18080/fragments/network | grep -oE 'banner-title">[^<]*|</span>[^<]*</span></td>'
banner-title">Network in agreement
</span>up to date</span></td>
</span>up to date</span></td>
</span>up to date</span></td>
```

(Each `curl` was run a few seconds after the previous command, the poller reads every 2 s; right after `dc start node3` it needs a few more seconds.) The agreement is among the nodes that answer; the banner says how many do not. The stopped node keeps its last known height in the matrix and shows how far behind it is once the others move on. After `dc start node3` it syncs the two blocks on its own (a few seconds) and the last poll above shows three nodes up to date.

Verified: 2026-11-08, commit f3bd815.

## Restart sync (stop and start node3)

```shell
$ dc stop node3
$ $B miner --node $N:18081 --count 3
block 20 02eb12d4f5357a45181d3bcb15662bffe2d04052b0c0ef5e931b3a41e7796b2b txs=1
block 21 26ec31dcb1686ed56b061f2d2d59a42ec7008722e8a4b39ef62177f1a2cc438c txs=1
block 22 564144911ea2820e15d9c073c378a7805e0545d096b0e3dfb1da7013a56f3669 txs=1
$ dc start node3
$ sleep 6; curl -s $N:18083/health
{
  "status": "ok",
  "height": 22,
  "tip": "564144911ea2820e15d9c073c378a7805e0545d096b0e3dfb1da7013a56f3669"
}
```

While stopped, node3 misses three blocks; after the restart its volume still holds the old chain and initial block download (ADR-0019) fetches the missing ones from its seed. It reaches node1's tip within the 1 s peer interval plus the sync time (6 s was enough). The tip is the hash of block 22 above, the same one node1 reports.

Verified: 2026-11-08, commit f3bd815.

## Reorg and peer ban

**Not reproducible by hand.** A reorg needs two branches mined while the nodes cannot see each other, and a ban needs a peer that sends invalid data during sync; the compose network has no partition or misbehaving-peer tool. They are covered by tests:

- reorg: `cmd/bwg/sync_test.go:TestForkEndsOnTheBranchWithMoreWork` and `TestEqualWorkKeepsTheFirstSeenBranch`;
- peer ban and `403 peer_banned`: `internal/p2p/sync_test.go:TestSyncBansTheMisbehavingPeerAndTriesTheNext`, `TestSyncBansWhenTheLedgerRejectsTheBlocks` and the status table in `internal/server/p2p_test.go`;
- invalid blocks and transactions are neither accepted nor relayed: `cmd/bwg/gossip_test.go:TestInvalidBlockIsNeitherAcceptedNorRelayed`, `TestInvalidTxIsNeitherAcceptedNorRelayed`.

Not reproducible by hand; covered by cmd/bwg/sync_test.go:TestForkEndsOnTheBranchWithMoreWork and internal/p2p/sync_test.go:TestSyncBansTheMisbehavingPeerAndTriesTheNext.

## Snapshots and pruning (`--profile snapshots`)

`snap1` is a fourth node with `SNAPSHOT_INTERVAL=10` and `PRUNE_KEEP_BLOCKS=100`. It is a separate profile because **pruning cannot be undone**: a pruned node only follows reorgs down to the pruned height, so the three main nodes keep every block and stay the reference.

```shell
$ dc --profile snapshots up -d snap1
$ curl -s $N:18084/health          # it syncs from node1 (height 22 in this run)
$ $B miner --node $N:18081 --count 120
$ curl -s $N:18084/health          # height 142, same tip as node1
$ curl -s $N:18084/block/1
{
  "error": "block body was pruned; only its header is kept",
  "code": "block_pruned"
}
$ curl -s $N:18081/block/1 | head -c 60     # the full node still has the body
{
  "Index": 1,
  "Timestamp": "2026-11-08T14:57:00.54562175
```

`curl -s $N:18084/block/130 | head -c 80` returns the full block (`{ "Index": 130, "Timestamp": ...`): only the bodies older than the 100 most recent are replaced by headers. Known limitation seen while writing this page: once `snap1` has pruned, its log repeats the line `{"time":"...","level":"ERROR","msg":"p2p: cannot build the local hello","error":"storage: block body is pruned"}` every second (`docker logs bwg-playground-snap1-1 2>&1 | grep 'cannot build the local hello'`). It still received every block from node1 (height 142 above), only its own outbound handshake fails. This is a node behaviour, not something the playground changes.

Export and import of a snapshot (`--snapshot-export`, `--snapshot-import`) are not part of the playground; see the README, section "Snapshots and pruning".

Look at: `http://127.0.0.1:18084/explorer/#/block/1` shows the error `block_pruned (HTTP 410): block body was pruned; only its header is kept` with a link back to the latest blocks; `#/block/130` on the same node opens normally.

Verified: 2026-11-08, commit f3bd815.

## Reindex

`--reindex` rebuilds the derived indexes (UTXO set, undo data, indexes by transaction and address) from the stored blocks. Stop the node first: the command opens `chain.db`, which the running node holds.

```shell
$ dc stop node1
$ dc run --rm node1 node --reindex
reindexed: tip height 142
$ dc start node1
$ curl -s $N:18081/health
{
  "status": "ok",
  "height": 142,
  "tip": "33c89735cb75f322ab82af5d434c2ca15b0d61496358e70f990f9da41b32cff4"
}
```

The tip is the one the nodes had before the reindex (the same as `snap1`'s in the section above).

Verified: 2026-11-08, commit f3bd815.

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
39ea1f74a205c12733e1a5d7e855dd411e4f49a7eb302890f5bc4de5274ec39c
44802a907c8376bd6c81345405e558dcd9aee8914c65bd3e9dbea44ddd946d38
```

The two lines are the public keys of `poa1` and `poa2` (they differ on every run). Start the validators and seal blocks:

```shell
$ dc --profile poa up -d poa1 poa2
$ curl -s $N:18091/health          # the same on 18092
{
  "status": "ok",
  "height": 0,
  "tip": ""
}
$ $B miner --node $N:18091 --count 2
block 1 139eaece2f7c6129839fa040c411b43def32eebe6cdb6662b1e5cc134c06d668 txs=0
block 2 44188b95b17bb82e45a0eac63bdab9b702fa6dea8e5d9cc80b369af21680d43b txs=0
$ $B miner --node $N:18092 --count 1
block 3 3f3ba1635b1c41fa12f4db120740e4989dc47c9d623d1327f31d5fc2eb688a02 txs=0
$ curl -s $N:18091/health          # the same on 18092
{
  "status": "ok",
  "height": 3,
  "tip": "3f3ba1635b1c41fa12f4db120740e4989dc47c9d623d1327f31d5fc2eb688a02"
}
```

`miner` still calls `POST /mine`; under PoA the node seals the block with its own key instead of searching a nonce, so there is no difficulty or waiting. Blocks carry no coinbase (the PoA nodes have no `MINER_ADDRESS`). Look at: `GET /blocks` on either node (`curl -s $N:18091/blocks | grep -E '"Index"|"Validator"|"Nonce"|"Difficulty"'`) shows `Validator` filled and `Nonce`/`Difficulty` at 0, and the genesis (`Index` 0) with an empty `Validator`; blocks 1 and 2 carry the public key of `poa1` (`39ea1f74...`) and block 3 the one of `poa2` (`44802a90...`). Both nodes accept blocks sealed by either key; a key outside `POA_VALIDATORS` makes the node refuse to start (`POA_KEYSTORE: key <hex> is not in POA_VALIDATORS`, from the node source; not run here).

Verified: 2026-10-31, commit 9c41517.

## Rate limit and retargeting (`-f compose.tuning.yml`)

`compose.tuning.yml` sets `RATE_LIMIT=30` and `MINING_WORKERS=2` on node1 and a difficulty retarget every 10 blocks aiming at 2 s per block on **all three** nodes. It is a separate file because retargeting is a consensus rule: nodes that disagree on it reject each other's blocks, so it must be on from the first block. **Start from a clean chain**: `dc --profile '*' down -v --remove-orphans` (see Reset below; it deletes the volumes and keeps `.playground/`, so you keep the same miner). Do not add the file to a chain that already has blocks.

```shell
$ docker compose -p bwg-playground --env-file .playground/compose.env \
    -f docker-compose.yml -f compose.playground.yml -f compose.tuning.yml up -d
$ $B miner --node $N:18081 --count 25
block 1 2df012da7f0683db8f1a940a736321b0205f12aeece3b81b7f8df3e4611072ac txs=1
...
block 25 1c37efc5408e97f4397427268206a000e529aab503a22de030582abb301ee5a5 txs=1
$ curl -s "$N:18081/blocks?limit=100" | grep '"Difficulty"' | sort | uniq -c
      1       "Difficulty": 0,
     19       "Difficulty": 1,
      6       "Difficulty": 2,
```

The 25 blocks were mined far faster than 2 s each, so at the first retarget the difficulty rose from 1 to 2: blocks 1-19 are at difficulty 1 and blocks 20-25 at difficulty 2 (genesis is 0). The difficulty is part of each block (`Difficulty` in `GET /blocks`).

Rate limit: 30 requests per minute per IP. Burst past it:

```shell
$ for i in $(seq 40); do curl -s -o /dev/null -w "%{http_code} " $N:18081/health; done; echo
200 200 200 200 429 429 429 429 429 429 429 429 429 429 429 429 429 429 429 429 429 429 429 429 429 429 429 429 429 429 429 429 429 429 429 429 429 429 429 429
$ curl -si $N:18081/health | head -5
HTTP/1.1 429 Too Many Requests
Content-Type: application/json
Retry-After: 2
Date: Sat, 31 Oct 2026 19:17:21 GMT
Content-Length: 62
```

The first answers can already be `200`s or `429`s depending on the requests the miner and the peers made in the same minute; once the budget is gone they are all `429`. The body is `{"error": "rate limit exceeded", "code": "rate_limited"}`. The limit counts every request from the same IP, including the peer traffic of the other nodes to node1 and the `miner` command, so a burst of your own requests can also slow the other nodes' sync for a moment; it recovers by itself when the minute window moves on.

Verified: 2026-10-31, commit 9c41517.

## Reset

```shell
$ dc --profile '*' down -v --remove-orphans     # stops everything, deletes the containers, the volumes and the network
$ rm -rf .playground                            # deletes the binary, the env files and the keystores
```

> **Warning:** `rm -rf .playground` deletes `miner.key`, `alice.key`, `bob.key` and the PoA validator keystores. Without them the coins mined to the miner address, and any PoA chain, cannot be spent or continued. That is fine for a playground; do not keep real funds here.

`--profile '*'` is needed: without it `down` ignores the services of the `snapshots` and `poa` profiles and leaves `snap1`, `poa1`, `poa2` and their volumes behind. This also removes a network started with `compose.tuning.yml` (same project name). After a reset, `scripts/playground.sh` starts again from an empty chain with a new miner address. `dc` reads `.playground/compose.env`, which only exists after `scripts/playground.sh` has run once: on a checkout without `.playground/` the `dc ... down` above stops with `couldn't find env file: .../.playground/compose.env`. There is then nothing to reset, unless containers of an earlier run are still there; in that case create the file with `mkdir -p .playground; echo MINER_ADDRESS=x > .playground/compose.env` and run the two commands above.

Verified: 2026-10-31, commit 9c41517.

## Running behind a reverse proxy (compose.tls.yml)

`compose.tls.yml` (ADR-0024) is a separate stack for showing how an operator exposes a node: only a TLS-terminating Caddy is published; `node1`, `node2` and the `dashboard` sit on an internal network with no published port. It does not use 18080-18084, so it can run next to the base network. `node2` seeds from `node1` on the internal network, so p2p works there while the proxy blocks it.

```shell
$ docker compose -p bwg-tls -f compose.tls.yml up -d --build
$ scripts/tls-smoke.sh          # brings it up, checks everything below, tears it down
```

Caddy makes its own local CA (`tls internal`); it lives only in the `caddy-data` volume and no key or certificate is in the repository. Pass the root certificate to curl, or the browser shows a warning for `https://localhost:18443/explorer/`:

```shell
$ docker compose -p bwg-tls -f compose.tls.yml cp caddy:/data/caddy/pki/authorities/local/root.crt /tmp/bwg-root.crt
$ curl --cacert /tmp/bwg-root.crt https://localhost:18443/health
$ curl --cacert /tmp/bwg-root.crt https://localhost:18444/
```

Never use `curl -k`: it hides exactly what this stack is meant to show. Stop it with `docker compose -p bwg-tls -f compose.tls.yml down -v`.

### Routes

| Through `https://localhost:18443` | Methods | Answer |
|---|---|---|
| `/health`, `/blocks`, `/headers`, `/mempool`, `/block/*`, `/tx/*` (also `/tx/{id}/proof`), `/balance/*`, `/address/*`, `/contracts/*`, `/events` (SSE), `/explorer`, `/explorer/*` | `GET`, `HEAD` | exposed: forwarded to `node1` |
| `/p2p/*`, `POST /tx`, `POST /mine`, `GET /` and `POST /`, `/legacy/*`, `/metrics`, any other path or method | any | internal: `403` with the plain-text body `blocked by proxy` |
| `https://localhost:18444` (dashboard), any path | `GET`, `HEAD` | exposed |
| `https://localhost:18444`, other methods | any | `403 blocked by proxy` |

`/metrics` stays internal because Prometheus scrapes the node inside the network (ADR-0022). The proxy's `403` is plain text, so it is told apart from a node `403` such as `unknown_peer`, which is JSON.

Both sites add `Strict-Transport-Security: max-age=31536000`, `X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer`, and remove `Server`. There is no CSP on the dashboard site: the dashboard sets its own.

### Rate limiter behind the proxy

Without `TRUSTED_PROXIES`, the limiter keys each bucket on the connection's `RemoteAddr` and ignores `X-Forwarded-For`, `Forwarded` and `X-Real-IP` (pinned by `internal/server/ratelimit_test.go`). Behind a proxy every public client then reaches the node from the proxy's address, so `RATE_LIMIT` becomes **one shared cap for all public clients**: one noisy client can use it all. `compose.tls.yml` sets `TRUSTED_PROXIES=10.59.0.10/32` (caddy's fixed address), so each public client gets its own bucket, keyed on the rightmost untrusted `X-Forwarded-For` entry. Caddy overwrites an incoming `X-Forwarded-For` from clients, so a client cannot pick its own key. `Forwarded` and `X-Real-IP` stay ignored. The dashboard and node2 reach the node directly on the internal network and keep their own buckets. In `compose.tls.yml` node1 sets `RATE_LIMIT=1200` (requests per minute per public client, about 20 per second for each) as a starting value; lower it to see `429` answers through the proxy, and leave it off when only trusted clients use the proxy. The design is in [ADR-0025](adr/0025-trusted-proxies-forwarded-for.md).

### What this does not protect

- No authentication on any route: whoever reaches port 18443 can read everything on the exposed list.
- `node_id` is spoofable (ADR-0018); the proxy blocks `/p2p/*` from outside, but anything on the internal network can still send gossip.
- Peers still speak plain HTTP on the internal network.
- The certificate comes from a self-signed local CA, not from a public authority.
- Not audited and not for public deployment.

`scripts/tls-smoke.sh` (the command above) was run on 2026-10-31 and ended with `tls-smoke: OK` (exposed routes `200`, internal routes `403 blocked by proxy`, no published port on `node1`, `node2` and the dashboard, `node2` listing `http://node1:8080` as a peer).

Verified: 2026-10-31, commit 9c41517.
