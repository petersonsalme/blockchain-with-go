# Learning Blockchain with Go
[![Go CI](https://github.com/petersonsalme/blockchain-with-go/actions/workflows/go-ci.yml/badge.svg)](https://github.com/petersonsalme/blockchain-with-go/actions/workflows/go-ci.yml)

> # ⚠️ IMPORTANT DISCLAIMER
>
> **This is a personal study project about blockchain, built with professional engineering practice.** The chain core (consensus, UTXO, validation, mempool, gossip, sync and reorg) is written by hand to learn it; the tooling around it uses market standards ([ADR-0021](docs/adr/0021-professional-grade-study-reference.md)). It started as an old learning project and it is still an educational exercise.
>
> **It is also a pilot project to evaluate the adoption of [Paperclip](https://github.com/paperclipai/paperclip) with AI agents.** Every improvement, refactor, test, document and architectural change made to this repository since then is **purely an AI-generated suggestion** and nothing more.
>
> - ❌ It is **NOT** production-ready, audited, or secure. **Do not use it to hold, move or represent real value.** "Professional-grade" describes how it is built (ADRs, tests, CI gates), not fitness for production.
> - ❌ It has **NO** TLS and **NO** authentication: the HTTP API and the peer-to-peer protocol are plain HTTP, rate limiting is off by default, and any client that reaches a node can call it (see [SECURITY.md](SECURITY.md)). For a worked example of a TLS reverse proxy in front of a node, with its limits, see [Running behind a reverse proxy](docs/playground.md#running-behind-a-reverse-proxy-composetlsyml).
> - ❌ It is **NOT** an official product, a reference implementation of any protocol, or a statement of any company.
> - ❌ It is **NOT** affiliated with, endorsed by, or related to the work, products, or views of the author's employer, clients, or any other organization.
> - ❌ The AI-suggested changes do **NOT** represent professional advice, engineering recommendations, or best practices vetted by the author's employer or clients.
>
> This repository is maintained by an individual, on personal time, for learning and experimentation purposes only.

---

A simple blockchain implementation in Go, created as an exercise to demonstrate core blockchain concepts.

## Features Implemented
- **Block & Transaction Structure**: Blocks containing index, timestamp, transaction list (`Transactions`), current hash, previous hash, nonce, and difficulty. Each transaction has `ID`, `Inputs` (spent outputs with key and signature), `Outputs` (amount and address) and `Timestamp`.
- **Cryptographic Hashing**: Uses SHA-256 to ensure data integrity across all block attributes (including nonce, difficulty, and the Merkle root of all transactions). Transaction content hashing is deterministic and independent of transaction ID, signature, and public key.
- **Proof of Work & Context-Aware Mining**: Nonce search satisfying leading-zero-bit difficulty, supporting graceful context cancellation and mining timeouts.
- **Chain Validation**: Validates new blocks by checking index sequentiality, cryptographic linkage (`PrevHash`), SHA-256 hash integrity, and Proof of Work against each block's recorded difficulty.
- **HTTP API**: A RESTful API to view the blockchain, append new blocks with transactions, and report structured errors with appropriate HTTP status codes.

## Consensus Mechanism
Block validation checks cryptographic linkage, index sequentiality, Proof of Work against the block's recorded difficulty, and transaction data integrity.

### Reorg and fork choice
`chain.Service.ReplaceChain(ctx, candidate)` takes a full candidate chain (genesis first) and switches to it when it carries **strictly more accumulated work** after the fork point, the first height where it differs from the local chain. A block weighs `2^Difficulty` (difficulty counts leading zero bits), so a PoA block weighs 1 and PoA falls back to the longest chain. Equal work keeps the local chain (first seen wins), and the genesis must be the local one.

- Every candidate block from the fork on is checked before anything is written: consensus, block rules, UTXO rules and input signatures, against the UTXO state of the candidate branch itself. The first failure is returned and the chain and the UTXO set stay as they were.
- The swap is one atomic `ReplaceFrom` on the repository, then one `Apply` of the net UTXO difference. If that `Apply` fails the service stops accepting writes (`ErrStateDiverged`), like `Mine`.
- The returned `Reorg` lists the fork height, the disconnected and connected blocks and `Orphaned`, the transactions of the dropped blocks that the new blocks do not hold, in chain order. They are not put back in the mempool or revalidated yet.
- It shares the write turn with `Mine`. There is no peer transport yet, so forks come only from a caller handing in a candidate.

## Transaction Model (UTXO)
Value lives in unspent transaction outputs (UTXOs). A transaction spends existing outputs and creates new ones:

```go
type Transaction struct {
    ID        string
    Inputs    []TxIn  // PrevTxID, OutIndex, PublicKey, Signature
    Outputs   []TxOut // Amount, Address
    Timestamp time.Time
}
```

- **Inputs**: each input names the output it spends by `(PrevTxID, OutIndex)`. The output must be unspent and owned by the address derived from the input's `PublicKey`.
- **Outputs**: each output assigns `Amount > 0` to a valid checksummed wallet address.
- **Fee**: `fee = sum(inputs) − sum(outputs)`. Outputs may not exceed inputs (`insufficient_inputs`); whatever is not assigned to an output is the fee, so send the change back to yourself.
- **ID**: SHA-256 over the canonical JSON of the inputs (`prev_txid`, `out_index`, without key or signature), the outputs and the UTC timestamp. A non-empty `ID` must match `CalculateHash()`.
- **Signature per input**: every input is signed with Ed25519 (`domain.SignInput`) over the raw 32-byte digest of `CalculateHash()`, so one signature commits to all inputs and outputs. `PublicKey` is 64 lowercase hex characters (non-weak key), `Signature` is 128.
- **Double spend**: an output can be spent once; spending it twice, in one block or across blocks, is rejected (`double_spend`, `input_missing_or_spent`).
- **TransactionsHash**: SHA-256 over each transaction's hash and its input signatures, binding them to the block header.
- **Genesis**: the genesis block of an empty chain carries one transaction without inputs that creates the outputs listed in `GENESIS_ALLOCATIONS`; besides the coinbase below, it is the only source of coins.
- **Coinbase**: with `MINER_ADDRESS` set, every block the node mines starts with one transaction without inputs that pays `Subsidy(height) + fees` to that address. `domain.InitialSubsidy` is 5,000,000,000 base units and halves every `domain.HalvingInterval` (210,000) blocks; both are consensus constants, not configuration (ADR-0015). Validation accepts a coinbase only at position 0 of a non-genesis block, paying at most `Subsidy(height)` plus the fees of the block (`coinbase_too_large`). Without `MINER_ADDRESS` blocks carry no coinbase.

### Proof of Authority

With `CONSENSUS=poa` the node seals blocks with an Ed25519 signature instead of mining them (see [ADR-0005](docs/adr/0005-poa-block-seal.md)).

- **Who seals:** the node's own key, loaded from `POA_KEYSTORE`. It must belong to the validator set `POA_VALIDATORS`, otherwise the node refuses to start (it would produce blocks it rejects itself).
- **What is signed:** the block hash (which covers `Validator`, index, timestamp, transactions, `PrevHash`). `Verify` accepts a block only if `Validator` is in the set and `Signature` is valid over its hash.
- **A chain is tied to one consensus:** a chain mined with PoW cannot be continued in PoA (or the reverse), nor with a different validator set; start from an empty `DATA_DIR`.

Generate a keystore and print the public key to put in `POA_VALIDATORS`:

```go
package main

import (
	"encoding/hex"
	"fmt"
	"log"

	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

func main() {
	w, err := wallet.New()
	if err != nil {
		log.Fatal(err)
	}
	if err := w.Save("validator.key", "choose-a-password"); err != nil {
		log.Fatal(err)
	}
	fmt.Println(hex.EncodeToString(w.PublicKey()))
}
```

```shell
$ CONSENSUS=poa POA_VALIDATORS=<hex> POA_KEYSTORE=validator.key POA_KEYSTORE_PASSWORD=choose-a-password go run ./cmd/bwg node
```

### HD wallet (BIP-39 + SLIP-0010)

The `wallet` package supports hierarchical deterministic (HD) wallets using BIP-39 English mnemonics (12 or 24 words) and SLIP-0010 ed25519 key derivation. A seed is generated with an optional passphrase and keys are derived along hardened-only paths such as `m/44'/1'/0'/0'/0'` (see [ADR-0017](docs/adr/0017-hd-wallet-slip10.md)). The derived key is a standard `*wallet.Wallet` that can be saved with `Save`, persisting the derived key rather than the mnemonic.

```go
m, err := wallet.NewMnemonic(24)
if err != nil {
	log.Fatal(err)
}
w, err := wallet.FromMnemonic(m, "optional passphrase", "m/44'/1'/0'/0'/0'")
if err != nil {
	log.Fatal(err)
}
fmt.Println(w.Address())
```

## Architectural Trade-offs
1. **In-Memory Storage vs. Persistence**: The node supports both in-memory and persistent disk storage. When `DATA_DIR` is set, blocks and hash indexes are persisted to `$DATA_DIR/chain.db` via the bbolt adapter (`internal/storage/bolt`). When `DATA_DIR` is empty, state lives in memory (`internal/storage/memory`) and is lost on restart. The UTXO set remains derived in memory and is rebuilt from the chain at startup (ADR-0001, ADR-0004).
2. **Centralization vs. P2P Network**: Nodes talk to each other over the same HTTP server, with no second port and no new dependency ([ADR-0013](docs/adr/0013-p2p-over-http.md)). A node says hello to its peers (`POST /p2p/hello`: protocol version, genesis hash, height, tip), refuses incompatible ones, exchanges peer addresses (`GET /p2p/peers`) and keeps a persisted list, starting from `SEED_PEERS`. The price is polling: a "connection" is a peer entry that a hello created and a later round refreshes every `PEER_INTERVAL`, so a peer's height is at most one round old. Blocks and transactions travel by gossip ([ADR-0018](docs/adr/0018-block-and-tx-gossip.md)): the announcer posts the hashes (`POST /p2p/inv`), the peer answers the ones it wants and the announcer then posts those objects (`POST /p2p/block`, `POST /p2p/tx`). The price is one extra round-trip before each object, which saves sending every object to every neighbour; a block reaches a node only if it extends that node's tip (a node that is behind learns it through the sync of a separate roadmap item) and a peer without a `P2P_LISTEN_ADDR` cannot receive pushes. There is no authentication between peers and a node dials the addresses its peers advertise (see [SECURITY.md](SECURITY.md)). Two independently started nodes only pair when their genesis blocks are identical.
3. **Sybil Protection (Proof of Work)**: A Proof of Work (PoW) mechanism is implemented to require computational effort for block creation and mitigate spam. The difficulty (number of leading zero **bits** required in the 256-bit block hash, so one step doubles the expected work; [ADR-0012](docs/adr/0012-difficulty-bits.md)) is configured via the `POW_DIFFICULTY` environment variable, defaulting to `8`. The logic lives in the `consensus` package behind the `Consensus` interface (`Seal`/`Verify`); `consensus.ProofOfWork` is the default implementation, and `POW_DIFFICULTY` is read once at startup (`cmd/bwg`).
   - **Difficulty Retargeting**: Each block records its difficulty in `b.Difficulty` (included in the SHA-256 hash), and validation requires it to equal the difficulty the rule expects for that position in the chain, then checks the proof of work against it. The child of genesis uses `POW_DIFFICULTY`; every `POW_RETARGET_INTERVAL` blocks the difficulty moves by one towards `TARGET_BLOCK_TIME` (up when the window took less than half the target, down when it took more than twice), within `1..32`; other blocks inherit the parent's difficulty. `Seal` and `Verify` receive the chain history for this ([ADR-0008](docs/adr/0008-difficulty-retargeting.md)). Miners still choose block timestamps within the ENG-18 bounds, so time-warp attacks are not prevented.
   - **Parallel Mining**: `Seal` searches the nonce space with `MINING_WORKERS` goroutines (worker `i` tries the nonces `i, i+w, i+2w, …` on its own copy of the block). The first valid nonce cancels the others, and `Seal` returns only after all workers stopped. Any nonce that solves the difficulty is valid, so the nonce found may differ between runs when there is more than one worker; validation is unchanged.
   - **Context Cancellation & Timeouts**: Mining supports cooperative cancellation via `context.Context` (`consensus.MineWithContext`, `GenerateBlockWithContext`). The HTTP handler applies a configurable mining timeout via `MINING_TIMEOUT` (defaulting to 10 seconds; keep it at least 5 × `TARGET_BLOCK_TIME`, so that block times inside the retarget band rarely hit it) and aborts immediately if the client disconnects or cancels the request, preventing request processing from blocking indefinitely.
4. **Transaction List Payload**: Each block header carries `MerkleRoot`, the root of a Merkle tree over its transactions, and the block hash covers the root instead of the transaction list. A leaf is `SHA-256(0x00 || tx hash || per-input public key and signature)`, an inner node is `SHA-256(0x01 || left || right)`, and an odd node without a pair moves up a level unchanged (RFC 6962), so `[a,b,c]` and `[a,b,c,c]` have different roots. A block with zero transactions has the root `SHA-256("")`. `Mine` rejects a block whose root does not match its transactions with the code `merkle_root_mismatch` ([ADR-0009](docs/adr/0009-merkle-tree.md)). Inclusion proofs over HTTP are a separate story of ENG-20.

## Architecture Decision Records

Design decisions live in [`docs/adr/`](docs/adr/0009-merkle-tree.md):

| ADR | Decision |
|---|---|
| [0001](docs/adr/0001-utxo-model.md) | Transaction model: UTXO replaces account/nonce |
| [0002](docs/adr/0002-genesis-allocations-env-var.md) | Genesis funding comes from `GENESIS_ALLOCATIONS` |
| [0003](docs/adr/0003-learning-project-positioning.md) | Personal learning project with an AI-generated-changes disclaimer (superseded by 0021) |
| [0004](docs/adr/0004-disk-storage.md) | Disk storage: bbolt adapter with in-memory derived UTXO set |
| [0005](docs/adr/0005-poa-block-seal.md) | Proof of Authority seal fields on Block |
| [0006](docs/adr/0006-stdlib-servemux-routing.md) | HTTP routing with the standard library `ServeMux` instead of gorilla/mux |
| [0007](docs/adr/0007-openapi-contract.md) | OpenAPI spec as the HTTP contract, with a generated Go client |
| [0008](docs/adr/0008-difficulty-retargeting.md) | Difficulty retargeting; the `Consensus` port receives the block history |
| [0009](docs/adr/0009-merkle-tree.md) | Transaction Merkle tree in the block header (RFC 6962 odd-node rule) |
| [0010](docs/adr/0010-prometheus-metrics.md) | Prometheus metrics written by hand, no client library (superseded by 0022) |
| [0012](docs/adr/0012-difficulty-bits.md) | Proof-of-work difficulty counts leading zero bits; defaults realigned with `MINING_TIMEOUT` |
| [0014](docs/adr/0014-snapshots-and-pruning.md) | UTXO snapshots every N blocks, pruning of old bodies to headers, and boot from a snapshot file |
| [0015](docs/adr/0015-coinbase-and-halving.md) | Coinbase reward capped at `Subsidy(height) + fees`, with halving every 210,000 blocks |
| [0016](docs/adr/0016-bwg-cli-composition-root.md) | One `bwg` binary is the composition root; `bwg node` replaces `cmd/node` |
| [0017](docs/adr/0017-hd-wallet-slip10.md) | HD wallet: BIP-39 mnemonics with SLIP-0010 ed25519 derivation (hardened only) |
| [0020](docs/adr/0020-genesis-time.md) | Deterministic genesis time from `GENESIS_TIME` |
| [0021](docs/adr/0021-professional-grade-study-reference.md) | Professional-grade study reference: hand-written chain core, market tools around it, never for real value |
| [0022](docs/adr/0022-prometheus-client-golang.md) | Prometheus metrics with client_golang (registry per server) and an observability compose profile; supersedes 0010 |
| [0023](docs/adr/0023-network-dashboard.md) | Read-only network dashboard as `bwg dashboard` |
| [0024](docs/adr/0024-caddy-tls-reverse-proxy.md) | Caddy as the TLS-terminating reverse proxy in `compose.tls.yml` (no module added) |
| [0025](docs/adr/0025-trusted-proxies-forwarded-for.md) | Client address from `X-Forwarded-For` only behind `TRUSTED_PROXIES` (right to left, `net/netip`) |

## Configuration & Environment Variables

The application can be configured via environment variables (e.g., in a `.env` file):

| Variable | Description | Default |
|---|---|---|
| `PORT` | HTTP server port | `8080` |
| `CONSENSUS` | Consensus engine: `pow` or `poa`. Any other value stops the node at startup | empty (`pow`) |
| `POW_DIFFICULTY` | Number of leading zero bits required for block hashes (`CONSENSUS=pow`) | `8` |
| `POW_RETARGET_INTERVAL` | Blocks between difficulty adjustments (`CONSENSUS=pow`); `0` disables retargeting. Invalid values use the default | `10` |
| `TARGET_BLOCK_TIME` | Intended time between blocks, as a Go duration (`CONSENSUS=pow`). Invalid or non-positive values use the default. `MINING_TIMEOUT` should be at least 5 × this value | `2s` |
| `MINING_WORKERS` | Goroutines that search for a nonce (`CONSENSUS=pow`). Integers `>= 1` only; unset or invalid values use the number of CPUs | `runtime.NumCPU()` |
| `POA_VALIDATORS` | Validator set for `CONSENSUS=poa`: Ed25519 public keys in hex, comma-separated (spaces around items ignored). Required; a non-hex, wrong-length or duplicate item stops the node, naming the item | empty |
| `POA_KEYSTORE` | Path of the encrypted keystore (`wallet.Save`) holding this node's signing key. Its public key must be in `POA_VALIDATORS`; required with `CONSENSUS=poa` | empty |
| `POA_KEYSTORE_PASSWORD` | Password that unlocks `POA_KEYSTORE`; required with `CONSENSUS=poa` | empty |
| `MINING_TIMEOUT` | Maximum duration allowed for mining a block (e.g., `10s`, `500ms`, or integer seconds) | `10s` |
| `GENESIS_ALLOCATIONS` | Genesis outputs as `address:amount[,address:amount...]`. Each address must be a valid wallet address and each amount an integer from 1 to 2^64−1; an invalid item stops the node at startup. Whitespace around each item and around `:` is ignored. Only used when the chain is empty (the genesis is created); ignored otherwise | empty (genesis without transactions) |
| `GENESIS_TIME` | Timestamp of the genesis block and of its allocation transaction, as RFC 3339 (e.g. `2026-01-01T00:00:00Z`). Nodes started with the same `GENESIS_TIME` and `GENESIS_ALLOCATIONS` on empty storage share the genesis and can pair ([ADR-0020](docs/adr/0020-genesis-time.md)). An invalid value or one later than the current time stops the node at startup. Only used when the chain is empty; ignored otherwise | empty (the clock at genesis creation) |
| `MINER_ADDRESS` | Wallet address that receives the coinbase (`Subsidy(height) + fees`) of every block this node mines. Blank or only spaces adds no coinbase; an invalid address stops the node at startup, naming the variable | empty (no coinbase) |
| `DATA_DIR` | Directory for persistent disk storage (stores `chain.db` and the known peer addresses in `peers.json`; a corrupt `peers.json` stops the node). When empty, the in-memory repository is used | empty (in-memory storage) |
| `SNAPSHOT_INTERVAL` | Blocks between stored UTXO snapshots (non-negative integer; `0` disables them). With `PRUNE_KEEP_BLOCKS=K`, the snapshot of height `t − K` is stored when block `t` is committed. An invalid value stops the node at startup | `1000` |
| `PRUNE_KEEP_BLOCKS` | `0` keeps every block body (full node). A value `>= 100` keeps the bodies of at least that many recent blocks and prunes older ones down to their headers; requires `SNAPSHOT_INTERVAL > 0`. Values from 1 to 99, or invalid ones, stop the node at startup. Pruning cannot be undone: a pruned `chain.db` refuses to open with `0` | `0` |
| `SEED_PEERS` | Peers to dial at start: base URLs `http(s)://host:port`, comma-separated (spaces ignored). An invalid item stops the node, naming it | empty |
| `P2P_LISTEN_ADDR` | Base URL other nodes use to reach this one, sent in every hello and advertised to peers. Empty means the node dials out but accepts no inbound peers; an invalid value stops the node | empty |
| `MAX_INBOUND_PEERS` | Maximum inbound peers (positive integer; invalid values use the default) | `16` |
| `MAX_OUTBOUND_PEERS` | Maximum outbound peers (positive integer; invalid values use the default) | `8` |
| `PEER_INTERVAL` | Time between dialer rounds, as a Go duration. Empty, invalid or non-positive values use the default; peers not seen for 3 intervals expire | `30s` |
| `MAX_BLOCK_BYTES` | Maximum JSON-serialized block size in bytes (positive integer; invalid values fall back to the default) | `1048576` |
| `RATE_LIMIT` | Maximum requests per minute per client address: `RemoteAddr`, or the rightmost untrusted `X-Forwarded-For` entry when the connection comes from `TRUSTED_PROXIES` (positive integer; empty, invalid, or non-positive disables rate limiting) | disabled (`0`) |
| `TRUSTED_PROXIES` | Comma-separated CIDRs or IPs of reverse proxies whose `X-Forwarded-For` names the client for the rate limiter ([ADR-0025](docs/adr/0025-trusted-proxies-forwarded-for.md)); `Forwarded` and `X-Real-IP` are ignored. Invalid entries stop the node. List only proxies that overwrite or append the header; trusting anything else lets clients pick their own bucket | empty (header ignored) |
| `NODE_URL` | Base URL of the node used by `bwg wallet` and `bwg miner` (`--node` overrides it) | `http://localhost:8080` |
| `WALLET_KEYSTORE` | Keystore path used by `bwg wallet new` and `bwg wallet send` (`--keystore` overrides it) | `wallet.key` |
| `WALLET_PASSWORD` | Password of the wallet keystore; required by `bwg wallet new` and `bwg wallet send`. There is no flag for it, so it stays out of the shell history | empty |

## Project Structure

```
blockchain-with-go/
├── cmd/
│   └── node/          # Node binary entrypoint (main.go)
├── internal/
│   ├── chain/         # Chain service (genesis, mining, validation) and block generation
│   ├── storage/       # BlockRepository port
│   │   ├── bolt/      # bbolt disk adapter
│   │   └── memory/    # In-memory adapter (sync.RWMutex)
│   ├── consensus/     # Consensus port interface and errors
│   │   └── pow/       # Proof of Work implementation
│   ├── domain/        # Domain entities (Block, Transaction, rules)
│   └── server/        # HTTP REST API server, handlers and DTOs
├── client/            # Go client generated from openapi.yaml (do not edit)
├── pkg/
│   └── wallet/        # Cryptographic wallet (Ed25519), addresses, and keystore
├── openapi.yaml       # HTTP API contract (OpenAPI 3.0.3)
├── go.mod             # Go module definition
└── README.md          # Project documentation
```

## Ports & Adapters

| Layer | Package | Role |
| --- | --- | --- |
| Domain | `internal/domain` | Pure entities and rules (`Block`, `Transaction`, `ValidateBlockRules`). Imports no other internal package. |
| Port (outbound) | `internal/storage` (`BlockRepository`) | `Last`, `Append`, `All`, `ByHeight`, `ByHash`, `Range` — where the chain is stored. |
| Port (outbound) | `internal/consensus` (`Consensus`) | `Seal`/`Verify` — how blocks are produced and accepted. |
| Application | `internal/chain` (`Service`) | `Blocks()`, `Tip()`, `BlockByHeight`, `BlockByHash`, `BlocksRange` and `Mine(ctx, txs)`: genesis, pre-validation, `Seal`, rules, `Verify`, `Append`. Rejection → `chain.ErrInvalidBlock`. |
| Adapter (inbound) | `internal/server` (`Server`) | HTTP; maps `Service` errors to HTTP status codes. |
| Adapters (outbound) | `internal/storage/bolt`, `internal/storage/memory`, `internal/consensus/pow` | Disk repository (bbolt), in-memory repository (`sync.RWMutex`) and Proof of Work. |
| Composition root | `cmd/bwg` | Reads `.env`/environment, wires repo → pow → service → server and handles graceful shutdown. |

**Dependency rule:** arrows point inward (adapters → application → domain). `domain` does not depend on `storage`, `chain`, `server`, `net/http` or `os/signal`; `chain` does not import `server` or `net/http`. There is no global state: each `Server` receives its `Service` by injection.

## Running Locally

Build the node binary:
```shell
$ go build -o /dev/null ./cmd/bwg
```

Run the node in-memory:
```shell
$ go run ./cmd/bwg node
```

Run the node with disk storage:
```shell
$ DATA_DIR=./data go run ./cmd/bwg node
```

To run test suites:
```shell
$ go test -v ./...
$ go test -race ./...
```

Mutation testing (gremlins v0.6.0) per package:
```shell
$ go clean -testcache
$ go run github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0 unleash --timeout-coefficient 20 ./internal/<pacote>
```

### Rebuilding the indexes

The bbolt file keeps the blocks plus derived indexes (UTXO set, undo data, transaction heights, transactions by address; ADR-0011). A `chain.db` written before ADR-0011 is reindexed by the node itself at startup, so nothing needs to be done. For manual repair (for example after editing the file or restoring a partial backup), stop the node and run:

```shell
$ DATA_DIR=./data go run ./cmd/bwg node --reindex
reindexed: tip height 42
```

`--reindex` opens `$DATA_DIR/chain.db`, drops and rebuilds the indexes from the blocks in one transaction, prints the tip height (`reindexed: empty chain` when there are no blocks) and exits with code 0 without opening an HTTP port. Running it twice leaves the file in the same state as running it once. It exits with a non-zero code and an error on stderr when `DATA_DIR` is empty (the in-memory repository has nothing to rebuild), when `DATA_DIR` does not exist, or when another process holds `chain.db` (the lock wait is 1 second). `DATA_DIR` is also read from `.env` when present.

### Snapshots and pruning

Every `SNAPSHOT_INTERVAL` blocks the node stores a snapshot of the UTXO set together with a SHA-256 hash over the block hash and the sorted unspent outputs ([ADR-0014](docs/adr/0014-snapshots-and-pruning.md)). With `PRUNE_KEEP_BLOCKS` above `0`, block bodies below the snapshot are replaced by their headers (the hash and Merkle root stay verifiable), so `chain.db` holds the headers, the UTXO set and only the recent bodies. A pruned node follows reorgs only down to the pruned height. `GET /block/{ref}` for a pruned block returns `410` `block_pruned`, `GET /` and `GET /blocks` list pruned blocks with `"Transactions": []`, and a transaction of a pruned block returns `404` `tx_not_found`.

A full node can export its stored snapshot, and a new node can start from it instead of replaying the whole chain:

```sh
$ DATA_DIR=./full go run ./cmd/bwg node --snapshot-export snapshot.json
snapshot: height 990 hash 3f5c...
$ DATA_DIR=./fresh PRUNE_KEEP_BLOCKS=100 go run ./cmd/bwg node --snapshot-import snapshot.json --snapshot-hash 3f5c...
restored: height 990
```

`--snapshot-import` needs an empty `DATA_DIR`. It checks the file's hash against `--snapshot-hash` and every header against the consensus (`CONSENSUS` and its settings must match the exporting node), then writes the headers and the UTXO set; blocks after the snapshot height arrive like any other blocks. Neither flag opens an HTTP port; both exit with a non-zero code and an error on stderr when a check fails, leaving `chain.db` empty on import.

**Trust model:** blocks do not commit to the UTXO set, so no consensus rule can prove a snapshot is honest. The hash only proves the file is the one a full node reported. Take `--snapshot-hash` from a full node you trust (its `--snapshot-export` output), never from the same place as the file. This is the same model as Bitcoin Core's `assumeutxo`.

### CLI (bwg)

`cmd/bwg` is a command-line client for a running node. Every command prints one line on stdout (the value only, so it can be used in scripts); errors go to stderr with exit code 1.

```shell
$ export WALLET_PASSWORD='choose-a-password'
$ go run ./cmd/bwg wallet new --keystore alice.key        # prints the new address; refuses an existing file
$ go run ./cmd/bwg wallet balance <address>                # prints the confirmed balance
$ go run ./cmd/bwg wallet send --keystore alice.key --to <address> --amount 30 --fee 1   # prints the transaction ID
$ go run ./cmd/bwg miner --count 1                         # calls POST /mine; prints "block <index> <hash> txs=<n>"
```

`wallet send` spends the confirmed outputs of the keystore address in the order the node lists them, until they cover `amount + fee`, sends any change back to the same address, signs every input and submits the transaction to `POST /tx` (it is then in the mempool; `miner` or `POST /mine` confirms it). `miner` retries when the chain changed during mining (`409`) and, with `--count 0` (the default), mines until interrupted (`Ctrl+C`). `--node` (or `NODE_URL`) selects the node; the default matches the node's default `PORT`.

### Logs and shutdown

The node writes one structured JSON log line (`log/slog`) to stderr for every HTTP request, including `429` answers from the rate limiter:

```json
{"time":"2026-10-21T12:00:00.000000000-03:00","level":"INFO","msg":"http request","method":"GET","route":"GET /block/{ref}","status":404,"duration_ms":0}
```

`route` is the registered `ServeMux` pattern, never the raw path; requests that match no pattern and rate-limited requests are logged as `unmatched`. `status` is the code the client received.

On `SIGINT`/`SIGTERM` the node cancels the context of every request: a block being mined answers `408` with `{"error":"mining cancelled"}` and no block is appended. The other in-flight requests are drained for up to 10 seconds before the server closes. A fatal error is logged at `ERROR` level and the process exits with code 1.

### Docker

The `Dockerfile` builds a static `bwg` in a Go stage and runs `bwg node` on `gcr.io/distroless/static-debian12:nonroot` (user `65532`, no shell). The image listens on `8080` and is configured only through environment variables: it ships an empty `/app/.env`, so `docker run -e` is the only source of settings and every variable keeps the meaning described in [Configuration & Environment Variables](#configuration--environment-variables).

```shell
$ docker build -t bwg .
$ docker run --rm -p 8080:8080 bwg                                   # in-memory chain
$ docker run --rm -p 8080:8080 -e DATA_DIR=/data -v bwg-data:/data bwg   # bbolt in a named volume
$ curl -s localhost:8080/health
{"status":"ok","height":0,"tip":"..."}
```

Pass any other variable with `-e` (for example `-e CONSENSUS=poa -e POA_VALIDATORS=...`). Other subcommands run with the same image: `docker run --rm -e DATA_DIR=/data -v bwg-data:/data bwg node --reindex`. `scripts/docker-smoke.sh` builds the image and checks the non-root user and `GET /health` in both modes; CI runs it in the `docker` job.

### Three-node network (docker compose)

`docker-compose.yml` starts `node1`, `node2` and `node3` from the `Dockerfile` image, each with its own volume at `/data`, the same `GENESIS_TIME`, `CONSENSUS=pow` with `POW_DIFFICULTY=1` and `PEER_INTERVAL=1s`. The seed peers are `node2` → `node1` and `node3` → `node2`; the ports are bound to `127.0.0.1` only (18081, 18082, 18083).

```shell
$ docker compose up -d --build                  # brings the network up
$ curl -s localhost:18081/health                # node1; use 18082 and 18083 for the others
$ curl -s -X POST localhost:18081/mine          # mines one block; the other two receive it
$ go test -tags e2e -count=1 -timeout 10m ./e2e/...   # e2e: propagation, restarted node and convergence
$ docker compose down -v                        # stops the network and deletes the volumes
```

The e2e test uses the `bwg-e2e` project and tears it down when it finishes; CI runs it in the `e2e` job.

To try the network by hand, starting from an empty chain funded by mining, see [docs/playground.md](docs/playground.md). The same page has the guide to the network dashboard (`--profile dashboard`, port 18080): [Network dashboard](docs/playground.md#network-dashboard---profile-dashboard).

### Releases

Pushing a tag `v*` (for example `git tag v0.1.0 && git push origin v0.1.0`) runs `.github/workflows/release.yml`, which uses [goreleaser](https://goreleaser.com) (`.goreleaser.yaml`) to build static `bwg` binaries for linux, darwin and windows on amd64 and arm64 and publish them, with `checksums.txt`, as a GitHub release. To try it locally without publishing:

```shell
$ go run github.com/goreleaser/goreleaser/v2@v2.19.0 check
$ go run github.com/goreleaser/goreleaser/v2@v2.19.0 release --snapshot --clean   # binaries in dist/
```

### Fuzzing

Native Go fuzz targets (`testing.F`) check that `POST /` decoding, block JSON/hash and the block rules never panic and keep their properties. Seeds live in `testdata/fuzz/<FuzzName>/` of each package and run with a plain `go test ./...`. To fuzz for 60 seconds each:

```shell
$ go test -run='^$' -fuzz='^FuzzWriteBlockBody$' -fuzztime=60s ./internal/server
$ go test -run='^$' -fuzz='^FuzzBlockJSONRoundTrip$' -fuzztime=60s ./internal/domain
$ go test -run='^$' -fuzz='^FuzzValidateBlockRules$' -fuzztime=60s ./internal/domain
$ go test -run='^$' -fuzz='^FuzzVerifyTamper$' -fuzztime=60s ./internal/consensus/pow
```

A failing input is written to `testdata/fuzz/<FuzzName>/`; commit it so it becomes a regression seed.

### Benchmarks

`BenchmarkMineParallel` in `internal/consensus/pow` mines one new block per iteration for each difficulty (8, 12, 16 leading zero bits) and worker count (1, 2, 4, 8).

```shell
$ go test -run='^$' -bench=BenchmarkMineParallel -benchtime=50x ./internal/consensus/pow/
```

`ns/op` is the average time to mine one block. Each extra bit of difficulty doubles the expected work, so +4 bits ≈ 16× the time. The speedup from more workers is bounded by the CPU count. Results vary between runs because the number of hashes per block is random (geometric); compare runs with `benchstat`.

Reference run:

- `goos`: linux
- `goarch`: amd64
- `cpu`: AMD Ryzen 7 8700F 8-Core Processor
- Go: go1.26.10
- Commit: 07af10e (task 1, `BenchmarkMineParallel`)

| Difficulty (bits) | Workers | ms/op |
|---|---|---|
| 8 | 1 | 0.2 |
| 8 | 2 | 0.1 |
| 8 | 4 | 0.1 |
| 8 | 8 | 0.1 |
| 12 | 1 | 3.9 |
| 12 | 2 | 2.1 |
| 12 | 4 | 1.2 |
| 12 | 8 | 0.8 |
| 16 | 1 | 44.1 |
| 16 | 2 | 25.2 |
| 16 | 4 | 14.1 |
| 16 | 8 | 8.5 |

## API Calls

The HTTP contract is [`openapi.yaml`](openapi.yaml) (OpenAPI 3.0.3, see [ADR-0007](docs/adr/0007-openapi-contract.md)). `internal/server/contract_test.go` checks the server against it, so a route, status or JSON field change must update the spec in the same PR.

The Go client in [`client/`](client) is generated from the spec. After editing `openapi.yaml`, regenerate it (CI fails when `client/` is out of date):

```shell
go generate ./client/...
```

```go
api, _ := client.NewClientWithResponses("http://localhost:8080")
resp, _ := api.GetHealthWithResponse(ctx)
if resp.JSON200 != nil {
    fmt.Println(resp.JSON200.Height, resp.JSON200.Tip)
}
```

### GET /
Retrieve the full blockchain.

```shell
$ curl http://localhost:8080/
```

**Response (`200 OK`):**
```json
{
  "Blocks": [
    {
      "Index": 1,
      "Timestamp": "2026-10-16T12:00:00Z",
      "Transactions": [],
      "MerkleRoot": "e3b0c442...",
      "Hash": "9f3a1c...",
      "PrevHash": "00a1b2c3...",
      "Nonce": 0,
      "Difficulty": 0,
      "Validator": "3b6a27bc...",
      "Signature": "e1f0c4d2..."
    }
  ]
}
```

`Validator` and `Signature` are filled only under `CONSENSUS=poa` (hex strings); under PoW they are empty and `Nonce`/`Difficulty` carry the proof of work.

### GET /blocks

Paginated read of the chain. Query: `from` (default `0`) and `limit` (default `20`, maximum `100`; a larger value is reduced to `100`).

```json
{"blocks": [...], "from": 0, "limit": 20, "next": 20}
```

`next` (`from` + number of blocks returned) is present only when a block exists at that height; follow it until it disappears to read the whole chain. A `from` past the tip returns `blocks: []` without `next`. A non-integer or negative `from`/`limit`, or `limit=0`, returns `400` with `"code": "invalid_query"`.

### GET /block/{ref}

`ref` is a block hash (64 lowercase hex characters) or a height (decimal digits, fewer than 64). Returns `200` with the block in the same JSON format as `POST /`. Any other form returns `400` `invalid_block_ref`; a block outside the chain returns `404` `block_not_found`. The genesis block carries no hash, so look it up by height (`/block/0`). On a pruned node, a block below the pruned height returns `410` `block_pruned` (see [Snapshots and pruning](#snapshots-and-pruning)).

### GET /tx/{id}

Lookup a mined transaction by its 64-character lowercase hex ID. Returns `200` with the transaction object, the block hash, and height:

```json
{"transaction": {...}, "block_hash": "...", "height": 1}
```

The genesis allocation transaction is found at height `0` with `"block_hash": ""`. An ID that is not 64 lowercase hex characters returns `400` with `"code": "invalid_tx_id"`. A non-existent transaction returns `404` with `"code": "tx_not_found"`.

### GET /tx/{id}/proof

Merkle inclusion proof of a mined transaction against the `MerkleRoot` in its block header. `id` follows the same rules as `GET /tx/{id}`.

```json
{
  "tx_id": "...", "block_index": 2, "block_hash": "...",
  "merkle_root": "...", "leaf_index": 1, "leaf_hash": "...",
  "proof": [{"hash": "...", "side": "left"}, {"hash": "...", "side": "right"}]
}
```

To verify, start with `cur = leaf_hash` and, for each step in order, hash with SHA-256 the bytes `0x01 || sibling || cur` when `side` is `"left"` (the sibling sits on the left) or `0x01 || cur || sibling` when `side` is `"right"`, using the raw 32-byte values of the hex hashes. The proof holds when the final `cur` equals `merkle_root`, which must also equal the `MerkleRoot` of block `block_index` in `GET /`. The leaf hash is SHA-256 of `0x00 || ascii(tx hash) || for each input: u32be(len(public key)) || public key || u32be(len(signature)) || signature` ( see [ADR-0009](docs/adr/0009-merkle-tree.md)). An odd node without a sibling moves up unchanged and adds no step, so `proof` is `[]` when the block has a single transaction. An ID that is not 64 lowercase hex characters returns `400` `invalid_tx_id`; an unknown transaction returns `404` `tx_not_found`. The search is linear and never waits for a `POST /` that is mining.

### GET /balance/{addr}

Returns the balance and unspent outputs (UTXOs) for `addr` from a single snapshot of the UTXO set:

```json
{"address": "...", "balance": 100, "utxos": [{"txid": "...", "index": 0, "amount": 100}]}
```

Outputs are sorted by `(TxID, Index)`. If `wallet.ValidateAddress` fails, returns `400` with `"code": "invalid_address"`. An address with no unspent outputs returns `balance: 0` and `utxos: []` (never `null`).

### GET /address/{addr}/txs

Returns the confirmed transaction history of `addr`, newest first (block height descending, then position in the block descending), one page at a time:

```
curl 'localhost:8080/address/<addr>/txs?limit=2'
{"address": "...", "txs": [{"transaction": {...}, "block_hash": "...", "height": 7, "timestamp": "2026-01-01T00:00:00Z"}, ...], "limit": 2, "next": "<tx id>", "pruned_below": 0}
curl 'localhost:8080/address/<addr>/txs?limit=2&after=<next>'
```

- `limit` is a positive integer (default 25; above 100 it is clamped to 100, and the response echoes the value used). A bad `limit` returns `400` `invalid_query`.
- `after` is the id of the last transaction of the previous page (64 lowercase hex characters, else `400` `invalid_query`); the page starts strictly after it. `next` is that id, present only while older transactions remain, so follow it until it is absent. The cursor needs no server state: blocks mined between two requests add transactions only at the top, so they show up on a new first page and never shift or repeat the pages being read.
- A transaction that both spends from and pays to the address is listed once. Pending (mempool) transactions are not listed; use `GET /mempool`.
- An invalid address returns `400` `invalid_address`; an unknown but valid address returns `200` with `"txs": []` (never `null`) and no `next`.
- An `after` that is well formed but not in the address history returns `422` `unknown_cursor`, never an empty page: it belongs to another address, to a block that a reorg removed, or to a block pruned since the previous page. Restart from the first page. A reorg at or below the height of the cursor can invalidate it; if the same transaction is included again at another height, paging continues from its new position.
- A pruned node (`pruned_below` above 0) lists only transactions at heights `>= pruned_below`; older transactions are not listed. `pruned_below` is `0` when nothing was pruned.
- Every item and `pruned_below` of one response come from one chain tip, and the read never waits for a `POST /` that is mining.

### GET /health

`200` `{"status": "ok", "height": 12, "tip": "<hash>"}` with the current tip; `503` `{"error": "...", "code": "unhealthy"}` when the repository cannot be read.

None of these reads waits for a `POST /` that is mining.

### GET /events

Server-sent events (`Content-Type: text/event-stream`), no polling. The node sends `event: block` after each block it appends and `event: tx` after each transaction it admits, whatever the source: its own mining (`POST /` or `POST /mine`), `POST /tx`, a peer's block or transaction through gossip (`POST /p2p/block` or `/p2p/tx` answered `accepted`; a `known` answer or a rejection sends nothing), sync extension (one event per block) and reorg (one event per block of the new branch, oldest first, after the switch). `data:` is the same compact one-line JSON as the `201`/`202` body of a local request. There is no `reorg` event: after a reorg a `block` event can carry an `Index` that is not above the last one seen, so compare `Index` and `PrevHash`. Transactions put back in the pool after a reorg send no `tx` event. The block is already readable at `GET /block/{hash}` when its event arrives.

```
curl -N localhost:8080/events
event: block
data: {"index":1,"hash":"...",...}
```

A client that stays more than 16 events behind is disconnected (reconnect to resume; missed events are not replayed). The stream ends when the node shuts down. Other methods answer `405` with `Allow: GET, HEAD`; the rate limiter applies as on every other route.

### Web explorer (/explorer/)

A read-only web page embedded in the binary (plain HTML, CSS and JavaScript, no build step, no CDN): open `http://localhost:8080/explorer/`. It lists the latest 20 blocks and adds each new one at the top through `GET /events`, and links blocks to transactions, transactions to addresses (balance and UTXOs) and inputs to the transaction they spend. Views use hash URLs: `#/`, `#/block/{ref}`, `#/tx/{id}`, `#/address/{addr}`. API errors (`400`, `404`, `410 block_pruned`) show as a message with their `code`. The page only sends `GET` requests to the same origin; the responses carry a strict `Content-Security-Policy` and `X-Content-Type-Options: nosniff`. It is not part of the JSON API: it is absent from `openapi.yaml`, and other methods answer `405` with `Allow: GET, HEAD`.

### POST /p2p/hello and GET /p2p/peers

Peer-to-peer over HTTP ([ADR-0013](docs/adr/0013-p2p-over-http.md)). A node whose `Config.P2P` is unset answers `404` with `code: "p2p_disabled"` on both routes.

`POST /p2p/hello` is the handshake. The body is the caller's hello and must hold exactly these fields (an unknown field is rejected); the answer is the local hello with the same fields.

```
curl -X POST localhost:8080/p2p/hello -d '{"version":1,"node_id":"<32 hex>","genesis_hash":"<64 hex>","height":7,"tip_hash":"<64 hex>","listen_addr":"http://10.0.0.2:8080"}'
```

| Status | `code` | Cause |
|---|---|---|
| `200` | | Accepted: the caller is now an inbound peer. |
| `400` | `invalid_hello` | Invalid JSON, unknown field or malformed hello. |
| `403` | `peer_banned` | The caller was banned for misbehaving during sync (ADR-0019). |
| `404` | `p2p_disabled` | The node has no peer-to-peer layer. |
| `409` | `incompatible_version` | Different protocol version. |
| `409` | `genesis_mismatch` | Different genesis block. |
| `409` | `self_connection` | The caller is this node. |
| `413` | `request_too_large` | Body over the limit, as on the other POST routes. |
| `503` | `peer_limit` | The inbound peer limit is reached. |

A refused hello leaves the peer list unchanged.

`GET /p2p/peers` answers `200` with the connected peers and the known addresses; empty lists are `[]`, and `last_seen` is RFC 3339 UTC.

```json
{"peers": [{"node_id": "<32 hex>", "addr": "http://10.0.0.2:8080", "height": 7, "tip_hash": "<64 hex>", "inbound": true, "last_seen": "2026-10-25T12:00:00Z"}],
 "addrs": ["http://10.0.0.2:8080"]}
```

Other methods answer `405` with `Allow: POST` and `Allow: GET, HEAD`; the rate limiter, the body limit, the log and the metrics apply as on every other route.

### Sync and ban

Initial block download and fork resolution ([ADR-0019](docs/adr/0019-initial-sync-and-forks.md)). At the end of every dialer round (`PEER_INTERVAL`) a node asks the connected peer with the highest tip it does not know for `GET /headers`, then for the bodies in batches of 16 through `GET /blocks`. A peer on a fork is followed only when its branch has strictly more accumulated work; with equal work the first branch seen stays. Blocks are applied only through `AcceptBlock` or `ReplaceChain`, and the transactions of the blocks dropped by a switch go back to the pool. A peer that sends invalid headers or blocks is banned for one hour: it is disconnected and its `POST /p2p/hello` answers `403 peer_banned`. A slow or unreachable peer is only disconnected. No new environment variable is needed.

### POST /p2p/inv, POST /p2p/block and POST /p2p/tx

Block and transaction gossip ([ADR-0018](docs/adr/0018-block-and-tx-gossip.md)). A node mines a block or admits a transaction, queues it, and a relay worker offers it to every connected peer that has an address, except the peer the object came from: `POST /p2p/inv` carries the hashes, the answer lists the ones the peer wants, and each wanted object is then posted to `/p2p/block` or `/p2p/tx`. A received object is relayed only after the chain appended it (it must extend the tip) or the pool admitted it. Every body carries the sender's `node_id`, which must be a connected peer; it is not authentication. Without the peer layer the three routes answer `404` `p2p_disabled`, and mining and submitting work without announcing.

```
curl -X POST localhost:8080/p2p/inv -d '{"node_id":"<32 hex>","items":[{"type":"block","hash":"<64 hex>"},{"type":"tx","hash":"<64 hex>"}]}'
curl -X POST localhost:8080/p2p/block -d '{"node_id":"<32 hex>","block":{...}}'
curl -X POST localhost:8080/p2p/tx -d '{"node_id":"<32 hex>","tx":{...}}'
```

| Status | `code` | Route | Cause |
|---|---|---|---|
| `200` | | inv | `{"want": [...]}`: the items the node wants, in request order (`[]` when it has them all). |
| `200` | | block, tx | `{"status": "accepted"}` or `{"status": "known"}`. |
| `400` | `invalid_inv` | inv | Invalid JSON, unknown field, 0 or more than 1000 items, unknown type or a hash that is not 64 lowercase hex. |
| `400` | `invalid_body` | block, tx | Invalid JSON, unknown field or trailing data. |
| `403` | `unknown_peer` | all | `node_id` is not a connected peer. |
| `404` | `p2p_disabled` | all | The node has no peer-to-peer layer. |
| `409` | `not_on_tip` | block | The block does not extend the current tip. |
| `413` | `request_too_large` | all | Body over the limit. |
| `422` | `invalid_block` / `invalid_tx` | block / tx | The object failed validation; it is not relayed. |

### GET /metrics

Prometheus text exposition (format 0.0.4, `Content-Type: text/plain; version=0.0.4; charset=utf-8; escaping=underscores`), served by `prometheus/client_golang` from a registry per `Server` ([ADR-0022](docs/adr/0022-prometheus-client-golang.md), which supersedes [ADR-0010](docs/adr/0010-prometheus-metrics.md)). Labels and families come out sorted by name. Other methods answer `405` with `Allow: GET, HEAD`; the rate limiter applies as on every other route.

| Metric | Type | Meaning |
|---|---|---|
| `http_requests_total{method,route,code}` | counter | Requests answered. `route` is the `ServeMux` pattern (same value as the log), `unmatched` otherwise; methods outside the standard set are folded into `OTHER`, so the series count stays bounded. |
| `blockchain_blocks` | gauge | `Tip().Index + 1`, read on each scrape. Omitted if the tip cannot be read. |
| `blockchain_mining_duration_seconds` | summary | `_sum` and `_count` of the `Mine` calls that returned a block (no quantiles). |
| `go_*`, `process_*` | mixed | Go runtime and process collectors of client_golang (`process_*` only where `/proc` exists, i.e. Linux). |

```
curl -s localhost:8080/metrics
```

Counters live inside each `Server` and reset when the node restarts; a scrape is counted after it is answered.

### POST /
Create and mine a new block with a list of **signed** transactions. The node never changes a signed field: `Timestamp` is required (`400 missing_timestamp`), and `ID` is filled in only when empty (a wrong `ID` is `400`).

Every input is signed with Ed25519 over the transaction hash (`domain.SignInput`). Each input spends one unspent output, and `sum(inputs) − sum(outputs)` is the fee. To have something to spend, start the node with `GENESIS_ALLOCATIONS`:

```shell
$ GENESIS_ALLOCATIONS=<sender address>:100 go run ./cmd/bwg node
```

The genesis transaction (see `GET /`) has an `ID`; spending its output 0 (100 coins) by sending 60 to a recipient, 39 back as change, and leaving 1 as the fee:

```shell
$ curl -X POST -H "Content-Type: application/json" \
  -d '{"Transactions":[{
        "Inputs":[{
          "PrevTxID":"<genesis transaction ID>","OutIndex":0,
          "PublicKey":"<64 hex chars>","Signature":"<128 hex chars>"}],
        "Outputs":[
          {"Amount":60,"Address":"<recipient address>"},
          {"Amount":39,"Address":"<sender address (change)>"}],
        "Timestamp":"2026-10-16T12:05:00Z"}]}' \
  http://localhost:8080/
```

**Success Response (`201 Created`):**
```json
{
  "Index": 1,
  "Timestamp": "2026-10-16T12:05:00Z",
  "Transactions": [
    {
      "ID": "3a7b8c...",
      "Inputs": [
        {"PrevTxID": "<genesis transaction ID>", "OutIndex": 0, "PublicKey": "<64 hex chars>", "Signature": "<128 hex chars>"}
      ],
      "Outputs": [
        {"Amount": 60, "Address": "<recipient address>"},
        {"Amount": 39, "Address": "<sender address (change)>"}
      ],
      "Timestamp": "2026-10-16T12:05:00Z"
    }
  ],
  "Hash": "00f4a8b1...",
  "PrevHash": "00a1b2c3...",
  "Nonce": 128,
  "Difficulty": 2
}
```

### POST /tx
Submit **one** signed transaction to the mempool instead of mining it. The body is a single transaction in the same format as the transactions of `POST /` (not `{"Transactions": [...]}`), with the same rules: `Timestamp` is required, and `ID` is filled in only when empty. Each input must spend a confirmed output; spending an output of another transaction still in the pool is rejected.

Mempool transactions are mined by `POST /mine`, ordered by fee rate (see below). Nothing is mined by `POST /tx` itself.

```shell
$ curl -X POST -H "Content-Type: application/json" \
  -d '{"Inputs":[{
        "PrevTxID":"<genesis transaction ID>","OutIndex":0,
        "PublicKey":"<64 hex chars>","Signature":"<128 hex chars>"}],
      "Outputs":[{"Amount":99,"Address":"<recipient address>"}],
      "Timestamp":"2026-10-16T12:05:00Z"}' \
  http://localhost:8080/tx
```

**Success Response (`202 Accepted`):**
```json
{"id": "3a7b8c..."}
```

A `409 Conflict` means the transaction was not admitted: `duplicate_tx` (already in the pool), `mempool_conflict` (it spends an output that another pool transaction already spends; first seen wins, there is no replace-by-fee) or `mempool_full` (the pool holds 5000 transactions and this one's fee rate does not beat the lowest-ranked entry, which would be evicted). Other errors are the same as for `POST /`.

### GET /mempool
List the pending transactions in selection order: fee rate per byte, descending, then `ID` ascending. The fee is the sum of the inputs minus the sum of the outputs; the size is the length of the transaction's JSON. This request never waits for mining.

```shell
$ curl http://localhost:8080/mempool
```

**Success Response (`200 OK`):**
```json
{
  "transactions": [{"ID": "...", "Inputs": [...], "Outputs": [...], "Timestamp": "..."}],
  "entries": [{"id": "...", "fee": 120, "size": 412}]
}
```

`entries[i]` describes `transactions[i]` (same length and order): `fee` is the fee and `size` the size in bytes the pool orders by. There is no fee rate field; compute `fee / size` to display one. An empty pool returns `{"transactions": [], "entries": []}`.

### POST /mine
Mine a new block from the mempool. The request has no body. The node takes the transactions with the highest fee rate per byte that fit in `MAX_BLOCK_BYTES`, in that order. An empty pool mines an empty block.

Using the same genesis allocations (`GENESIS_ALLOCATIONS=<sender address>:100,<sender address>:100`), two transactions spend different outputs of the genesis transaction: A spends output 0 and pays 99 (fee 1), B spends output 1 and pays 95 (fee 5). Submit A and then B:

```shell
$ curl -X POST -H "Content-Type: application/json" -d @tx-a.json http://localhost:8080/tx
{"id": "5e1d0a..."}
$ curl -X POST -H "Content-Type: application/json" -d @tx-b.json http://localhost:8080/tx
{"id": "9c4f2b..."}
```

`GET /mempool` lists B before A, because B's fee rate is higher:

```shell
$ curl http://localhost:8080/mempool
{"transactions": [{"ID": "9c4f2b...", ...}, {"ID": "5e1d0a...", ...}]}
```

`POST /mine` then returns the block with B first and A second:

```shell
$ curl -X POST http://localhost:8080/mine
```

**Success Response (`201 Created`):**
```json
{
  "Index": 1,
  "Timestamp": "2026-10-16T12:10:00Z",
  "Transactions": [
    {"ID": "9c4f2b...", "Inputs": [...], "Outputs": [...], "Timestamp": "..."},
    {"ID": "5e1d0a...", "Inputs": [...], "Outputs": [...], "Timestamp": "..."}
  ],
  "Hash": "00d4e5f6...",
  "PrevHash": "00a1b2c3...",
  "Nonce": 77,
  "Difficulty": 2
}
```

Transactions that are included leave the pool. A transaction that became invalid after `POST /tx` (for example, its inputs were spent by a block mined through `POST /`) is dropped and not mined. The same `MINING_TIMEOUT`, serialization and errors as `POST /` apply. The mempool lives only in memory: restarting the node empties it. `POST /` keeps mining exactly the transactions it receives and does not use the mempool.

### POST /legacy/bpm (Removed)
BPM payloads cannot be signed, so the endpoint answers `410 Gone` (with `Deprecation: true`) and `{"error": "endpoint removed; send signed transactions to POST /", "code": "endpoint_gone"}` for any body.

#### Contracts

`POST /tx` also accepts the optional lower-case `contract` object, a deploy or a call of the stack-machine VM ([ADR-0014](docs/adr/0014-contract-vm.md)). Without it the request and the response are exactly as above.

| Field | Deploy | Call |
|---|---|---|
| `kind` | `"deploy"` | `"call"` |
| `code` | bytecode, lowercase hex, 1 to 1024 bytes | absent |
| `target` | absent | contract ID, 64 lowercase hex characters |
| `args` | numbers the code reads with `ARG` (optional) | same |
| `gas_limit` | `1` to `100000` | same |

The contract is part of the transaction hash and of every input signature (`CalculateHash`), so compute `ID` and sign after filling it in. A deploy's contract ID is the `ID` of its transaction. `POST /tx` runs the operation against the confirmed contract state and refuses what would fail (`400` for a malformed operation, `422` for an execution error); state changes only when `POST /mine` mines the transaction. Counter walkthrough (`vm.CounterCode`, which costs 29 gas per call):

```shell
# 1. deploy the counter (ID = hash of the signed transaction; inputs and outputs as in the example above)
$ curl -X POST -H "Content-Type: application/json" -d '{"ID":"<deploy ID>","Inputs":[...],"Outputs":[...],
    "Timestamp":"2026-10-16T12:05:00Z",
    "contract":{"kind":"deploy","code":"01000000000000000030010000000000000001100100000000000000003100","gas_limit":100}}' http://localhost:8080/tx
$ curl -X POST http://localhost:8080/mine

# 2. call it 3 times: each call is a new transaction that spends a new output; mine after each one
$ curl -X POST -H "Content-Type: application/json" -d '{"ID":"<call ID>","Inputs":[...],"Outputs":[...],
    "Timestamp":"2026-10-16T12:06:00Z",
    "contract":{"kind":"call","target":"<deploy ID>","args":[],"gas_limit":29}}' http://localhost:8080/tx
$ curl -X POST http://localhost:8080/mine

# 3. read the state after the third call
$ curl http://localhost:8080/contracts/<deploy ID>
{
  "id": "<deploy ID>",
  "code": "01000000000000000030010000000000000001100100000000000000003100",
  "storage": {
    "0": 3
  }
}

# 4. a call with "gas_limit": 28 is rejected: the counter needs 29
$ curl -X POST -H "Content-Type: application/json" -d '{... "contract":{"kind":"call","target":"<deploy ID>","args":[],"gas_limit":28}}' http://localhost:8080/tx
{"error": "vm: out of gas", "code": "out_of_gas"}   # 422
```

Contract error codes: `400` `invalid_contract_op`, `invalid_code`; `422` `unknown_contract`, `contract_exists`, `out_of_gas`, `reverted`, `stack_overflow`, `stack_underflow`, `bad_jump`, `arg_index`, `invalid_gas`. A transaction that is already mined shows its operation in `GET /tx/{id}` and in blocks under the Go field names (`Contract`, `Kind`, `GasLimit`).

### GET /contracts/{id}

Returns the confirmed state of a deployed contract:

```json
{"id": "<deploy ID>", "code": "<bytecode, lowercase hex>", "storage": {"0": 3}}
```

`storage` maps the decimal key of every cell written so far to its value (`{}` for a contract nobody has called). `id` must be 64 lowercase hex characters, otherwise `400` with `"code": "invalid_contract_id"`; an ID that is not deployed returns `404` with `"code": "contract_not_found"`. A deploy or call still in the mempool is not visible until it is mined.

### HTTP Status Codes & Error Responses

| Status Code | Description | Example Response Body |
|---|---|---|
| `200 OK` | Blockchain retrieved successfully via `GET /` | JSON array of blocks |
| `200 OK` | `GET /blocks`, `GET /block/{ref}`, `GET /tx/{id}`, `GET /balance/{addr}`, `GET /address/{addr}/txs`, `GET /contracts/{id}`, `GET /health` | page, block, transaction, balance, address history or health object (see above) |
| `400 Bad Request` | Malformed query on `/blocks`, malformed `ref` on `/block/{ref}`, malformed `id` on `/tx/{id}` or `/tx/{id}/proof`, invalid address on `/balance/{addr}` or `/address/{addr}/txs`, malformed query on `/address/{addr}/txs`, or malformed `id` on `/contracts/{id}` | `{"error": "...", "code": "invalid_query" \| "invalid_block_ref" \| "invalid_tx_id" \| "invalid_address" \| "invalid_contract_id"}` |
| `404 Not Found` | `GET /block/{ref}` or `GET /tx/{id}` or `GET /tx/{id}/proof` outside the chain, or `GET /contracts/{id}` for a contract that is not deployed | `{"error": "...", "code": "block_not_found" \| "tx_not_found" \| "contract_not_found"}` |
| `503 Service Unavailable` | `GET /health` cannot read the chain tip | `{"error": "...", "code": "unhealthy"}` |
| `201 Created` | Block mined and appended to the blockchain via `POST /` or `POST /mine` | JSON object of the new block |
| `202 Accepted` | Transaction admitted to the mempool via `POST /tx` | `{"id": "..."}` |
| `400 Bad Request` | Malformed JSON body or invalid transaction payload (e.g. empty list) | `{"error": "invalid request body"}` |
| `400 Bad Request` | Transaction rejected before mining | `{"error": "...", "code": "missing_signature" \| "invalid_signature" \| "invalid_public_key" \| "input_owner_mismatch" \| "invalid_output" \| "missing_inputs" \| "coinbase_too_large" \| "missing_timestamp"}` |
| `409 Conflict` | Input already spent or unknown, or the same output spent twice; on `POST /tx`, also a transaction already in the pool, one that conflicts with a pool transaction, or a full pool it cannot displace | `{"error": "...", "code": "input_missing_or_spent" \| "double_spend" \| "duplicate_tx" \| "mempool_conflict" \| "mempool_full"}` |
| `400 Bad Request` | `POST /tx` with a malformed contract operation or undeployable code | `{"error": "...", "code": "invalid_contract_op" \| "invalid_code"}` |
| `422 Unprocessable Entity` | `GET /address/{addr}/txs` with an `after` cursor outside the address history | `{"error": "...", "code": "unknown_cursor"}` |
| `422 Unprocessable Entity` | Outputs exceed inputs | `{"error": "...", "code": "insufficient_inputs"}` |
| `422 Unprocessable Entity` | `POST /tx` with a contract operation that fails against the confirmed state | `{"error": "...", "code": "unknown_contract" \| "contract_exists" \| "out_of_gas" \| "reverted" \| "stack_overflow" \| "stack_underflow" \| "bad_jump" \| "arg_index" \| "invalid_gas"}` |
| `410 Gone` | `POST /legacy/bpm` (removed) | `{"error": "...", "code": "endpoint_gone"}` |
| `405 Method Not Allowed` | HTTP method not allowed on endpoint (e.g. `PUT`, `DELETE`) | empty body (router-level response) |
| `408 Request Timeout` | Client disconnected or cancelled request during mining | `{"error": "mining cancelled"}` |
| `422 Unprocessable Entity` | Generated block failed chain validation | `{"error": "invalid block could not be added"}` |
| `422 Unprocessable Entity` | Block exceeds `MAX_BLOCK_BYTES` (checked before mining) | `{"error": "...", "code": "block_too_large", "details": {"size": 1200, "max": 1000}}` |
| `422 Unprocessable Entity` | Block timestamp earlier than its parent or more than 2h in the future | `{"error": "...", "code": "timestamp_before_parent" \| "timestamp_too_far_in_future", "details": {...}}` |
| `409 Conflict` | Another writer appended a block between reading the chain tip and appending (block no longer extends the tip) | `{"error": "...", "code": "chain_conflict"}` |
| `413 Request Entity Too Large` | `POST /` body larger than 2× `MAX_BLOCK_BYTES` | `{"error": "...", "code": "request_too_large", "details": {"max": 2000}}` |
| `429 Too Many Requests` | Request rate limit exceeded for client IP; the `Retry-After` header gives the whole seconds until the next token | `{"error": "rate limit exceeded", "code": "rate_limited"}` |
| `504 Gateway Timeout` | Mining exceeded deadline set by `MINING_TIMEOUT` | `{"error": "mining timeout exceeded"}` |
| `500 Internal Server Error` | Unexpected internal server error | `{"error": "..."}` |

`POST /` requests are serialized: only one block is mined at a time, and the time a request spends waiting for its turn counts against `MINING_TIMEOUT` (so a long queue ends in `504`). `GET /` never waits for mining. The storage port appends a block only if it extends the current tip, which is what yields `409` when a second writer shares the repository.

## Wallet
The `wallet` package provides identities based on **Ed25519** keys (`wallet.New`, `wallet.FromSeed`, `Sign`, `wallet.Verify`).

**Address format**: `hex(version || SHA-256(pubkey)[:20] || checksum)` in lowercase (50 chars), where `version` is `0x00` and `checksum = SHA-256(SHA-256(version || hash))[:4]`. Use `wallet.ValidateAddress` to check length, encoding, version and checksum.

**Keystore**: `Wallet.Save(path, password)` writes a JSON file (permissions `0600`, atomic write) containing the Ed25519 seed encrypted with AES-256-GCM, using a key derived via scrypt (`N=32768, r=8, p=1`) with a random 32-byte salt and 12-byte nonce per save. The address is bound to the file as GCM additional data. `wallet.Load(path, password)` returns `ErrWrongPassword` on a bad password and `ErrInvalidKeystore` for malformed or tampered files.
