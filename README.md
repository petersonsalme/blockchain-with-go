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
- **Cryptographic Hashing**: Uses SHA-256 to ensure data integrity across all block attributes (including nonce, difficulty, and an aggregate hash of all transactions). Transaction content hashing is deterministic and independent of transaction ID, signature, and public key.
- **Proof of Work & Context-Aware Mining**: Nonce search satisfying leading-zero difficulty, supporting graceful context cancellation and mining timeouts.
- **Chain Validation**: Validates new blocks by checking index sequentiality, cryptographic linkage (`PrevHash`), SHA-256 hash integrity, and Proof of Work against each block's recorded difficulty.
- **HTTP API**: A RESTful API built with `gorilla/mux` to view the blockchain, append new blocks with transactions, and report structured errors with appropriate HTTP status codes.

## Consensus Mechanism
This implementation utilizes a simplified **Longest Chain Rule** (represented by the `replaceChain` function). When confronted with multiple valid chains, the node will accept the longest chain as the source of truth. However, since this iteration does not implement peer-to-peer networking or block broadcasting, chain forks do not occur naturally. Block validation checks cryptographic linkage, index sequentiality, Proof of Work against the block's recorded difficulty, and transaction data integrity.

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

## Architectural Trade-offs
1. **In-Memory Storage vs. Persistence**: The node supports both in-memory and persistent disk storage. When `DATA_DIR` is set, blocks and hash indexes are persisted to `$DATA_DIR/chain.db` via the bbolt adapter (`internal/storage/bolt`). When `DATA_DIR` is empty, state lives in memory (`internal/storage/memory`) and is lost on restart. The UTXO set remains derived in memory and is rebuilt from the chain at startup (ADR-0001, ADR-0004).
2. **Centralization vs. P2P Network**: Currently, the blockchain runs as a single centralized HTTP server rather than a decentralized peer-to-peer network. This eliminates the complexity of network discovery, peer management, and gossip protocols, but makes the system centralized.
3. **Sybil Protection (Proof of Work)**: A Proof of Work (PoW) mechanism is implemented to require computational effort for block creation and mitigate spam. The difficulty (number of leading zeros required in the block hash) is configured via the `POW_DIFFICULTY` environment variable, defaulting to `2`. The logic lives in the `consensus` package behind the `Consensus` interface (`Seal`/`Verify`); `consensus.ProofOfWork` is the default implementation, and `POW_DIFFICULTY` is read once at startup (`cmd/node`).
   - **Difficulty Decoupled from Validation**: Each block records its difficulty in `b.Difficulty` at mining time, and validation checks the block's proof of work against its own recorded difficulty (which is included in the SHA-256 hash). Dynamic difficulty adjustment (e.g. retargeting) across nodes is not implemented.
   - **Context Cancellation & Timeouts**: Mining supports cooperative cancellation via `context.Context` (`consensus.MineWithContext`, `GenerateBlockWithContext`). The HTTP handler applies a configurable mining timeout via `MINING_TIMEOUT` (defaulting to 10 seconds) and aborts immediately if the client disconnects or cancels the request, preventing request processing from blocking indefinitely.
4. **Transaction List Payload**: Blocks store a slice of transactions committed via `TransactionsHash`. Full Merkle tree support with inclusion proofs is deferred to a subsequent milestone (ENG-20).

## Architecture Decision Records

Design decisions live in [`docs/adr/`](docs/adr):

| ADR | Decision |
|---|---|
| [0001](docs/adr/0001-utxo-model.md) | Transaction model: UTXO replaces account/nonce |
| [0002](docs/adr/0002-genesis-allocations-env-var.md) | Genesis funding comes from `GENESIS_ALLOCATIONS` |
| [0003](docs/adr/0003-learning-project-positioning.md) | Personal learning project with an AI-generated-changes disclaimer |
| [0004](docs/adr/0004-disk-storage.md) | Disk storage: bbolt adapter with in-memory derived UTXO set |

## Configuration & Environment Variables

The application can be configured via environment variables (e.g., in a `.env` file):

| Variable | Description | Default |
|---|---|---|
| `PORT` | HTTP server port | `8080` |
| `POW_DIFFICULTY` | Number of leading zeros required for block hashes | `2` |
| `MINING_TIMEOUT` | Maximum duration allowed for mining a block (e.g., `10s`, `500ms`, or integer seconds) | `10s` |
| `GENESIS_ALLOCATIONS` | Genesis outputs as `address:amount[,address:amount...]`. Each address must be a valid wallet address and each amount an integer from 1 to 2^64−1; an invalid item stops the node at startup. Only used when the chain is empty (the genesis is created); ignored otherwise | empty (genesis without transactions) |
| `DATA_DIR` | Directory for persistent disk storage (stores `chain.db`). When empty, the in-memory repository is used | empty (in-memory storage) |
| `MAX_BLOCK_BYTES` | Maximum JSON-serialized block size in bytes (positive integer; invalid values fall back to the default) | `1048576` |

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
│   └── server/        # HTTP REST API server, handlers and DTOs (gorilla/mux)
├── pkg/
│   └── wallet/        # Cryptographic wallet (Ed25519), addresses, and keystore
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
| Adapter (inbound) | `internal/server` (`Server`) | HTTP (gorilla/mux); maps `Service` errors to HTTP status codes. |
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

## API Calls

### GET /
Retrieve the full blockchain.

```shell
$ curl http://localhost:8080/
```

**Response (`200 OK`):**
```json
[
  {
    "Index": 0,
    "Timestamp": "2026-10-16T12:00:00Z",
    "Transactions": [],
    "Hash": "00a1b2c3...",
    "PrevHash": "",
    "Nonce": 42,
    "Difficulty": 2
  }
]
```

### GET /blocks

Paginated read of the chain. Query: `from` (default `0`) and `limit` (default `20`, maximum `100`; a larger value is reduced to `100`).

```json
{"blocks": [...], "from": 0, "limit": 20, "next": 20}
```

`next` (`from` + number of blocks returned) is present only when a block exists at that height; follow it until it disappears to read the whole chain. A `from` past the tip returns `blocks: []` without `next`. A non-integer or negative `from`/`limit`, or `limit=0`, returns `400` with `"code": "invalid_query"`.

### GET /block/{ref}

`ref` is a block hash (64 lowercase hex characters) or a height (decimal digits, fewer than 64). Returns `200` with the block in the same JSON format as `POST /`. Any other form returns `400` `invalid_block_ref`; a block outside the chain returns `404` `block_not_found`. The genesis block carries no hash, so look it up by height (`/block/0`).

### GET /health

`200` `{"status": "ok", "height": 12, "tip": "<hash>"}` with the current tip; `503` `{"error": "...", "code": "unhealthy"}` when the repository cannot be read.

None of these reads waits for a `POST /` that is mining.

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

### POST /legacy/bpm (Removed)
BPM payloads cannot be signed, so the endpoint answers `410 Gone` (with `Deprecation: true`) and `{"error": "endpoint removed; send signed transactions to POST /", "code": "endpoint_gone"}` for any body.

### HTTP Status Codes & Error Responses

| Status Code | Description | Example Response Body |
|---|---|---|
| `200 OK` | Blockchain retrieved successfully via `GET /` | JSON array of blocks |
| `200 OK` | `GET /blocks`, `GET /block/{ref}`, `GET /health` | page, block or health object (see above) |
| `400 Bad Request` | Malformed `from`/`limit` on `GET /blocks`, or malformed `ref` on `GET /block/{ref}` | `{"error": "...", "code": "invalid_query" \| "invalid_block_ref"}` |
| `404 Not Found` | `GET /block/{ref}` for a hash or height outside the chain | `{"error": "...", "code": "block_not_found"}` |
| `503 Service Unavailable` | `GET /health` cannot read the chain tip | `{"error": "...", "code": "unhealthy"}` |
| `201 Created` | Block mined and appended to the blockchain via `POST /` | JSON object of the new block |
| `400 Bad Request` | Malformed JSON body or invalid transaction payload (e.g. empty list) | `{"error": "invalid request body"}` |
| `400 Bad Request` | Transaction rejected before mining | `{"error": "...", "code": "missing_signature" \| "invalid_signature" \| "invalid_public_key" \| "input_owner_mismatch" \| "invalid_output" \| "missing_inputs" \| "missing_timestamp"}` |
| `409 Conflict` | Input already spent or unknown, or the same output spent twice | `{"error": "...", "code": "input_missing_or_spent" \| "double_spend"}` |
| `422 Unprocessable Entity` | Outputs exceed inputs | `{"error": "...", "code": "insufficient_inputs"}` |
| `410 Gone` | `POST /legacy/bpm` (removed) | `{"error": "...", "code": "endpoint_gone"}` |
| `405 Method Not Allowed` | HTTP method not allowed on endpoint (e.g. `PUT`, `DELETE`) | empty body (router-level response) |
| `408 Request Timeout` | Client disconnected or cancelled request during mining | `{"error": "mining cancelled"}` |
| `422 Unprocessable Entity` | Generated block failed chain validation | `{"error": "invalid block could not be added"}` |
| `422 Unprocessable Entity` | Block exceeds `MAX_BLOCK_BYTES` (checked before mining) | `{"error": "...", "code": "block_too_large", "details": {"size": 1200, "max": 1000}}` |
| `422 Unprocessable Entity` | Block timestamp earlier than its parent or more than 2h in the future | `{"error": "...", "code": "timestamp_before_parent" \| "timestamp_too_far_in_future", "details": {...}}` |
| `409 Conflict` | Another writer appended a block between reading the chain tip and appending (block no longer extends the tip) | `{"error": "...", "code": "chain_conflict"}` |
| `413 Request Entity Too Large` | `POST /` body larger than 2× `MAX_BLOCK_BYTES` | `{"error": "...", "code": "request_too_large", "details": {"max": 2000}}` |
| `504 Gateway Timeout` | Mining exceeded deadline set by `MINING_TIMEOUT` | `{"error": "mining timeout exceeded"}` |
| `500 Internal Server Error` | Unexpected internal server error | `{"error": "..."}` |

`POST /` requests are serialized: only one block is mined at a time, and the time a request spends waiting for its turn counts against `MINING_TIMEOUT` (so a long queue ends in `504`). `GET /` never waits for mining. The storage port appends a block only if it extends the current tip, which is what yields `409` when a second writer shares the repository.

## Wallet
The `wallet` package provides identities based on **Ed25519** keys (`wallet.New`, `wallet.FromSeed`, `Sign`, `wallet.Verify`).

**Address format**: `hex(version || SHA-256(pubkey)[:20] || checksum)` in lowercase (50 chars), where `version` is `0x00` and `checksum = SHA-256(SHA-256(version || hash))[:4]`. Use `wallet.ValidateAddress` to check length, encoding, version and checksum.

**Keystore**: `Wallet.Save(path, password)` writes a JSON file (permissions `0600`, atomic write) containing the Ed25519 seed encrypted with AES-256-GCM, using a key derived via scrypt (`N=32768, r=8, p=1`) with a random 32-byte salt and 12-byte nonce per save. The address is bound to the file as GCM additional data. `wallet.Load(path, password)` returns `ErrWrongPassword` on a bad password and `ErrInvalidKeystore` for malformed or tampered files.
