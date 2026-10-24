# Learning Blockchain with Go
[![Go CI](https://github.com/petersonsalme/blockchain-with-go/actions/workflows/go-ci.yml/badge.svg)](https://github.com/petersonsalme/blockchain-with-go/actions/workflows/go-ci.yml)

> # ⚠️ IMPORTANT DISCLAIMER
>
> **This is an old, personal learning project about blockchain.** It is an educational exercise only.
>
> **Today, it is being used as a pilot project to evaluate the adoption of [Paperclip](https://github.com/paperclipai/paperclip) with AI agents.** Every improvement, refactor, test, document and architectural change made to this repository since then is **purely an AI-generated suggestion** and nothing more.
>
> - ❌ It is **NOT** production-ready, audited, or secure. **Do not use it to hold, move or represent real value.**
> - ❌ It is **NOT** an official product, reference implementation, or statement of any company.
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
- **Genesis**: the genesis block of an empty chain carries one transaction without inputs that creates the outputs listed in `GENESIS_ALLOCATIONS`; that is the only source of coins for now (no coinbase yet).

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
$ CONSENSUS=poa POA_VALIDATORS=<hex> POA_KEYSTORE=validator.key POA_KEYSTORE_PASSWORD=choose-a-password go run ./cmd/node
```

## Architectural Trade-offs
1. **In-Memory Storage vs. Persistence**: The node supports both in-memory and persistent disk storage. When `DATA_DIR` is set, blocks and hash indexes are persisted to `$DATA_DIR/chain.db` via the bbolt adapter (`internal/storage/bolt`). When `DATA_DIR` is empty, state lives in memory (`internal/storage/memory`) and is lost on restart. The UTXO set remains derived in memory and is rebuilt from the chain at startup (ADR-0001, ADR-0004).
2. **Centralization vs. P2P Network**: Currently, the blockchain runs as a single centralized HTTP server rather than a decentralized peer-to-peer network. This eliminates the complexity of network discovery, peer management, and gossip protocols, but makes the system centralized.
3. **Sybil Protection (Proof of Work)**: A Proof of Work (PoW) mechanism is implemented to require computational effort for block creation and mitigate spam. The difficulty (number of leading zero **bits** required in the 256-bit block hash, so one step doubles the expected work; [ADR-0012](docs/adr/0012-difficulty-bits.md)) is configured via the `POW_DIFFICULTY` environment variable, defaulting to `8`. The logic lives in the `consensus` package behind the `Consensus` interface (`Seal`/`Verify`); `consensus.ProofOfWork` is the default implementation, and `POW_DIFFICULTY` is read once at startup (`cmd/node`).
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
| [0003](docs/adr/0003-learning-project-positioning.md) | Personal learning project with an AI-generated-changes disclaimer |
| [0004](docs/adr/0004-disk-storage.md) | Disk storage: bbolt adapter with in-memory derived UTXO set |
| [0005](docs/adr/0005-poa-block-seal.md) | Proof of Authority seal fields on Block |
| [0006](docs/adr/0006-stdlib-servemux-routing.md) | HTTP routing with the standard library `ServeMux` instead of gorilla/mux |
| [0007](docs/adr/0007-openapi-contract.md) | OpenAPI spec as the HTTP contract, with a generated Go client |
| [0008](docs/adr/0008-difficulty-retargeting.md) | Difficulty retargeting; the `Consensus` port receives the block history |
| [0009](docs/adr/0009-merkle-tree.md) | Transaction Merkle tree in the block header (RFC 6962 odd-node rule) |
| [0010](docs/adr/0010-prometheus-metrics.md) | Prometheus metrics written by hand, no client library |
| [0012](docs/adr/0012-difficulty-bits.md) | Proof-of-work difficulty counts leading zero bits; defaults realigned with `MINING_TIMEOUT` |

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
| `GENESIS_ALLOCATIONS` | Genesis outputs as `address:amount[,address:amount...]`. Each address must be a valid wallet address and each amount an integer from 1 to 2^64−1; an invalid item stops the node at startup. Only used when the chain is empty (the genesis is created); ignored otherwise | empty (genesis without transactions) |
| `DATA_DIR` | Directory for persistent disk storage (stores `chain.db`). When empty, the in-memory repository is used | empty (in-memory storage) |
| `MAX_BLOCK_BYTES` | Maximum JSON-serialized block size in bytes (positive integer; invalid values fall back to the default) | `1048576` |
| `RATE_LIMIT` | Maximum requests per minute per IP (positive integer; empty, invalid, or non-positive disables rate limiting) | disabled (`0`) |

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
| Composition root | `cmd/node` | Reads `.env`/environment, wires repo → pow → service → server and handles graceful shutdown. |

**Dependency rule:** arrows point inward (adapters → application → domain). `domain` does not depend on `storage`, `chain`, `server`, `net/http` or `os/signal`; `chain` does not import `server` or `net/http`. There is no global state: each `Server` receives its `Service` by injection.

## Running Locally

Build the node binary:
```shell
$ go build -o /dev/null ./cmd/node
```

Run the node in-memory:
```shell
$ go run ./cmd/node
```

Run the node with disk storage:
```shell
$ DATA_DIR=./data go run ./cmd/node
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
$ DATA_DIR=./data go run ./cmd/node -reindex
reindexed: tip height 42
```

`-reindex` opens `$DATA_DIR/chain.db`, drops and rebuilds the indexes from the blocks in one transaction, prints the tip height (`reindexed: empty chain` when there are no blocks) and exits with code 0 without opening an HTTP port. Running it twice leaves the file in the same state as running it once. It exits with a non-zero code and an error on stderr when `DATA_DIR` is empty (the in-memory repository has nothing to rebuild), when `DATA_DIR` does not exist, or when another process holds `chain.db` (the lock wait is 1 second). `DATA_DIR` is also read from `.env` when present.

### Logs and shutdown

The node writes one structured JSON log line (`log/slog`) to stderr for every HTTP request, including `429` answers from the rate limiter:

```json
{"time":"2026-10-21T12:00:00.000000000-03:00","level":"INFO","msg":"http request","method":"GET","route":"GET /block/{ref}","status":404,"duration_ms":0}
```

`route` is the registered `ServeMux` pattern, never the raw path; requests that match no pattern and rate-limited requests are logged as `unmatched`. `status` is the code the client received.

On `SIGINT`/`SIGTERM` the node cancels the context of every request: a block being mined answers `408` with `{"error":"mining cancelled"}` and no block is appended. The other in-flight requests are drained for up to 10 seconds before the server closes. A fatal error is logged at `ERROR` level and the process exits with code 1.

### Fuzzing

Native Go fuzz targets (`testing.F`) check that `POST /` decoding, block JSON/hash and the block rules never panic and keep their properties. Seeds live in `testdata/fuzz/<FuzzName>/` of each package and run with a plain `go test ./...`. To fuzz for 60 seconds each:

```shell
$ go test -run='^$' -fuzz='^FuzzWriteBlockBody$' -fuzztime=60s ./internal/server
$ go test -run='^$' -fuzz='^FuzzBlockJSONRoundTrip$' -fuzztime=60s ./internal/domain
$ go test -run='^$' -fuzz='^FuzzValidateBlockRules$' -fuzztime=60s ./internal/domain
$ go test -run='^$' -fuzz='^FuzzVerifyTamper$' -fuzztime=60s ./internal/consensus/pow
```

A failing input is written to `testdata/fuzz/<FuzzName>/`; commit it so it becomes a regression seed.

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

`ref` is a block hash (64 lowercase hex characters) or a height (decimal digits, fewer than 64). Returns `200` with the block in the same JSON format as `POST /`. Any other form returns `400` `invalid_block_ref`; a block outside the chain returns `404` `block_not_found`. The genesis block carries no hash, so look it up by height (`/block/0`).

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

### GET /health

`200` `{"status": "ok", "height": 12, "tip": "<hash>"}` with the current tip; `503` `{"error": "...", "code": "unhealthy"}` when the repository cannot be read.

None of these reads waits for a `POST /` that is mining.

### GET /metrics

Prometheus text exposition (format 0.0.4, `Content-Type: text/plain; version=0.0.4; charset=utf-8`), written by hand without a client library ([ADR-0010](docs/adr/0010-prometheus-metrics.md)). Other methods answer `405` with `Allow: GET, HEAD`; the rate limiter applies as on every other route.

| Metric | Type | Meaning |
|---|---|---|
| `http_requests_total{method,route,code}` | counter | Requests answered. `route` is the `ServeMux` pattern (same value as the log), `unmatched` otherwise; methods outside the standard set are folded into `OTHER`, so the series count stays bounded. |
| `blockchain_blocks` | gauge | `Tip().Index + 1`, read on each scrape. Omitted if the tip cannot be read. |
| `blockchain_mining_duration_seconds` | summary | `_sum` and `_count` of the `Mine` calls that returned a block (no quantiles). |

```
curl -s localhost:8080/metrics
```

Counters live inside each `Server` and reset when the node restarts; a scrape is counted after it is answered.

### POST /
Create and mine a new block with a list of **signed** transactions. The node never changes a signed field: `Timestamp` is required (`400 missing_timestamp`), and `ID` is filled in only when empty (a wrong `ID` is `400`).

Every input is signed with Ed25519 over the transaction hash (`domain.SignInput`). Each input spends one unspent output, and `sum(inputs) − sum(outputs)` is the fee. To have something to spend, start the node with `GENESIS_ALLOCATIONS`:

```shell
$ GENESIS_ALLOCATIONS=<sender address>:100 go run ./cmd/node
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
{"transactions": [{"ID": "...", "Inputs": [...], "Outputs": [...], "Timestamp": "..."}]}
```

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

### HTTP Status Codes & Error Responses

| Status Code | Description | Example Response Body |
|---|---|---|
| `200 OK` | Blockchain retrieved successfully via `GET /` | JSON array of blocks |
| `200 OK` | `GET /blocks`, `GET /block/{ref}`, `GET /tx/{id}`, `GET /balance/{addr}`, `GET /health` | page, block, transaction, balance or health object (see above) |
| `400 Bad Request` | Malformed query on `/blocks`, malformed `ref` on `/block/{ref}`, malformed `id` on `/tx/{id}` or `/tx/{id}/proof`, or invalid address on `/balance/{addr}` | `{"error": "...", "code": "invalid_query" \| "invalid_block_ref" \| "invalid_tx_id" \| "invalid_address"}` |
| `404 Not Found` | `GET /block/{ref}` or `GET /tx/{id}` or `GET /tx/{id}/proof` outside the chain | `{"error": "...", "code": "block_not_found" \| "tx_not_found"}` |
| `503 Service Unavailable` | `GET /health` cannot read the chain tip | `{"error": "...", "code": "unhealthy"}` |
| `201 Created` | Block mined and appended to the blockchain via `POST /` or `POST /mine` | JSON object of the new block |
| `202 Accepted` | Transaction admitted to the mempool via `POST /tx` | `{"id": "..."}` |
| `400 Bad Request` | Malformed JSON body or invalid transaction payload (e.g. empty list) | `{"error": "invalid request body"}` |
| `400 Bad Request` | Transaction rejected before mining | `{"error": "...", "code": "missing_signature" \| "invalid_signature" \| "invalid_public_key" \| "input_owner_mismatch" \| "invalid_output" \| "missing_inputs" \| "missing_timestamp"}` |
| `409 Conflict` | Input already spent or unknown, or the same output spent twice; on `POST /tx`, also a transaction already in the pool, one that conflicts with a pool transaction, or a full pool it cannot displace | `{"error": "...", "code": "input_missing_or_spent" \| "double_spend" \| "duplicate_tx" \| "mempool_conflict" \| "mempool_full"}` |
| `422 Unprocessable Entity` | Outputs exceed inputs | `{"error": "...", "code": "insufficient_inputs"}` |
| `410 Gone` | `POST /legacy/bpm` (removed) | `{"error": "...", "code": "endpoint_gone"}` |
| `405 Method Not Allowed` | HTTP method not allowed on endpoint (e.g. `PUT`, `DELETE`) | empty body (router-level response) |
| `408 Request Timeout` | Client disconnected or cancelled request during mining | `{"error": "mining cancelled"}` |
| `422 Unprocessable Entity` | Generated block failed chain validation | `{"error": "invalid block could not be added"}` |
| `422 Unprocessable Entity` | Block exceeds `MAX_BLOCK_BYTES` (checked before mining) | `{"error": "...", "code": "block_too_large", "details": {"size": 1200, "max": 1000}}` |
| `422 Unprocessable Entity` | Block timestamp earlier than its parent or more than 2h in the future | `{"error": "...", "code": "timestamp_before_parent" \| "timestamp_too_far_in_future", "details": {...}}` |
| `409 Conflict` | Another writer appended a block between reading the chain tip and appending (block no longer extends the tip) | `{"error": "...", "code": "chain_conflict"}` |
| `413 Request Entity Too Large` | `POST /` body larger than 2× `MAX_BLOCK_BYTES` | `{"error": "...", "code": "request_too_large", "details": {"max": 2000}}` |
| `429 Too Many Requests` | Request rate limit exceeded for client IP | `{"error": "rate limit exceeded", "code": "rate_limited"}` |
| `504 Gateway Timeout` | Mining exceeded deadline set by `MINING_TIMEOUT` | `{"error": "mining timeout exceeded"}` |
| `500 Internal Server Error` | Unexpected internal server error | `{"error": "..."}` |

`POST /` requests are serialized: only one block is mined at a time, and the time a request spends waiting for its turn counts against `MINING_TIMEOUT` (so a long queue ends in `504`). `GET /` never waits for mining. The storage port appends a block only if it extends the current tip, which is what yields `409` when a second writer shares the repository.

## Wallet
The `wallet` package provides identities based on **Ed25519** keys (`wallet.New`, `wallet.FromSeed`, `Sign`, `wallet.Verify`).

**Address format**: `hex(version || SHA-256(pubkey)[:20] || checksum)` in lowercase (50 chars), where `version` is `0x00` and `checksum = SHA-256(SHA-256(version || hash))[:4]`. Use `wallet.ValidateAddress` to check length, encoding, version and checksum.

**Keystore**: `Wallet.Save(path, password)` writes a JSON file (permissions `0600`, atomic write) containing the Ed25519 seed encrypted with AES-256-GCM, using a key derived via scrypt (`N=32768, r=8, p=1`) with a random 32-byte salt and 12-byte nonce per save. The address is bound to the file as GCM additional data. `wallet.Load(path, password)` returns `ErrWrongPassword` on a bad password and `ErrInvalidKeystore` for malformed or tampered files.
