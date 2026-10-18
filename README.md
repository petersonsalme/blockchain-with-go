# GoBlockchain
[![Go CI](https://github.com/petersonsalme/blockchain-with-go/actions/workflows/go-ci.yml/badge.svg)](https://github.com/petersonsalme/blockchain-with-go/actions/workflows/go-ci.yml)

A simple blockchain implementation in Go, created as an exercise to demonstrate core blockchain concepts.

## Features Implemented
- **Block & Transaction Structure**: Blocks containing index, timestamp, transaction list (`Transactions`), current hash, previous hash, nonce, and difficulty. Each transaction tracks `ID`, `From`, `To`, `Amount`, `Fee`, `Nonce`, `Signature`, and `Timestamp`.
- **Cryptographic Hashing**: Uses SHA-256 to ensure data integrity across all block attributes (including nonce, difficulty, and an aggregate hash of all transactions). Transaction content hashing is deterministic and independent of transaction ID and signature.
- **Proof of Work & Context-Aware Mining**: Nonce search satisfying leading-zero difficulty, supporting graceful context cancellation and mining timeouts.
- **Chain Validation**: Validates new blocks by checking index sequentiality, cryptographic linkage (`PrevHash`), SHA-256 hash integrity, and Proof of Work against each block's recorded difficulty.
- **HTTP API**: A RESTful API built with `gorilla/mux` to view the blockchain, append new blocks with transactions, and report structured errors with appropriate HTTP status codes.

## Consensus Mechanism
This implementation utilizes a simplified **Longest Chain Rule** (represented by the `replaceChain` function). When confronted with multiple valid chains, the node will accept the longest chain as the source of truth. However, since this iteration does not implement peer-to-peer networking or block broadcasting, chain forks do not occur naturally. Block validation checks cryptographic linkage, index sequentiality, Proof of Work against the block's recorded difficulty, and transaction data integrity.

## Transaction Model
Transactions represent value transfers recorded in the blockchain:

```go
type Transaction struct {
    ID        string
    From      string
    To        string
    Amount    uint64
    Fee       uint64
    Nonce     uint64
    Signature string
    Timestamp time.Time
}
```

- **ID**: SHA-256 hash over deterministic JSON serialization of `From, To, Amount, Fee, Nonce, Timestamp.UTC()`.
- **Validation**: Requires non-empty `From` and `To`, `Amount > 0`, and ensures `ID == CalculateHash()` when `ID` is provided.
- **TransactionsHash**: Computes a SHA-256 digest covering each transaction's content hash and signature in order, binding all transactions to the block header.

## Architectural Trade-offs
1. **In-Memory Storage vs. Persistence**: The blockchain state is kept entirely in memory. While this keeps the implementation simple, fast, and dependency-free, all data is lost when the server is restarted. A production system would require a persistent datastore (like LevelDB or RocksDB).
2. **Centralization vs. P2P Network**: Currently, the blockchain runs as a single centralized HTTP server rather than a decentralized peer-to-peer network. This eliminates the complexity of network discovery, peer management, and gossip protocols, but makes the system centralized.
3. **Sybil Protection (Proof of Work)**: A Proof of Work (PoW) mechanism is implemented to require computational effort for block creation and mitigate spam. The difficulty (number of leading zeros required in the block hash) is configured via the `POW_DIFFICULTY` environment variable, defaulting to `2`. The logic lives in the `consensus` package behind the `Consensus` interface (`Seal`/`Verify`); `consensus.ProofOfWork` is the default implementation, and `POW_DIFFICULTY` is read once at startup (`cmd/node`).
   - **Difficulty Decoupled from Validation**: Each block records its difficulty in `b.Difficulty` at mining time, and validation checks the block's proof of work against its own recorded difficulty (which is included in the SHA-256 hash). Dynamic difficulty adjustment (e.g. retargeting) across nodes is not implemented.
   - **Context Cancellation & Timeouts**: Mining supports cooperative cancellation via `context.Context` (`consensus.MineWithContext`, `GenerateBlockWithContext`). The HTTP handler applies a configurable mining timeout via `MINING_TIMEOUT` (defaulting to 10 seconds) and aborts immediately if the client disconnects or cancels the request, preventing request processing from blocking indefinitely.
4. **Transaction List Payload**: Blocks store a slice of transactions committed via `TransactionsHash`. Full Merkle tree support with inclusion proofs is deferred to a subsequent milestone (ENG-20).

## Configuration & Environment Variables

The application can be configured via environment variables (e.g., in a `.env` file):

| Variable | Description | Default |
|---|---|---|
| `PORT` | HTTP server port | `8080` |
| `POW_DIFFICULTY` | Number of leading zeros required for block hashes | `2` |
| `MINING_TIMEOUT` | Maximum duration allowed for mining a block (e.g., `10s`, `500ms`, or integer seconds) | `10s` |
| `MAX_BLOCK_BYTES` | Maximum JSON-serialized block size in bytes (positive integer; invalid values fall back to the default) | `1048576` |

## Project Structure

```
blockchain-with-go/
├── cmd/
│   └── node/          # Node binary entrypoint (main.go)
├── internal/
│   ├── chain/         # Chain service (genesis, mining, validation) and block generation
│   ├── storage/       # BlockRepository port
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
| Port (outbound) | `internal/storage` (`BlockRepository`) | `Last`, `Append`, `All` — where the chain is stored. |
| Port (outbound) | `internal/consensus` (`Consensus`) | `Seal`/`Verify` — how blocks are produced and accepted. |
| Application | `internal/chain` (`Service`) | `Blocks()` and `Mine(ctx, txs)`: genesis, pre-validation, `Seal`, rules, `Verify`, `Append`. Rejection → `chain.ErrInvalidBlock`. |
| Adapter (inbound) | `internal/server` (`Server`) | HTTP (gorilla/mux); maps `Service` errors to HTTP status codes. |
| Adapters (outbound) | `internal/storage/memory`, `internal/consensus/pow` | In-memory repository (`sync.RWMutex`) and Proof of Work. |
| Composition root | `cmd/node` | Reads `.env`/environment, wires repo → pow → service → server and handles graceful shutdown. |

**Dependency rule:** arrows point inward (adapters → application → domain). `domain` does not depend on `storage`, `chain`, `server`, `net/http` or `os/signal`; `chain` does not import `server` or `net/http`. There is no global state: each `Server` receives its `Service` by injection.

## Running Locally

Build the node binary:
```shell
$ go build -o /dev/null ./cmd/node
```

Run the node:
```shell
$ go run ./cmd/node
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

### POST /
Create and mine a new block with a list of transactions. If `Timestamp` or `ID` are omitted on any transaction, the server populates them automatically before validation.

```shell
$ curl -X POST -H "Content-Type: application/json" \
  -d '{"Transactions":[{"From":"alice","To":"bob","Amount":10,"Fee":1,"Nonce":0}]}' \
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
      "From": "alice",
      "To": "bob",
      "Amount": 10,
      "Fee": 1,
      "Nonce": 0,
      "Signature": "",
      "Timestamp": "2026-10-16T12:05:00Z"
    }
  ],
  "Hash": "00f4a8b1...",
  "PrevHash": "00a1b2c3...",
  "Nonce": 128,
  "Difficulty": 2
}
```

### POST /legacy/bpm (Deprecated)
Compatibility endpoint that accepts legacy BPM payloads and converts them into a single transaction (`{From: "legacy", To: "legacy", Amount: BPM}`). Responses include the `Deprecation: true` HTTP header.

```shell
$ curl -X POST -H "Content-Type: application/json" \
  -d '{"BPM":10}' \
  http://localhost:8080/legacy/bpm
```

**Success Response (`201 Created`):**
```
Deprecation: true
Content-Type: application/json
```
```json
{
  "Index": 1,
  "Timestamp": "2026-10-16T12:05:00Z",
  "Transactions": [
    {
      "ID": "f1e2d3...",
      "From": "legacy",
      "To": "legacy",
      "Amount": 10,
      "Fee": 0,
      "Nonce": 0,
      "Signature": "",
      "Timestamp": "2026-10-16T12:05:00Z"
    }
  ],
  "Hash": "00f4a8b1...",
  "PrevHash": "00a1b2c3...",
  "Nonce": 128,
  "Difficulty": 2
}
```

### HTTP Status Codes & Error Responses

| Status Code | Description | Example Response Body |
|---|---|---|
| `200 OK` | Blockchain retrieved successfully via `GET /` | JSON array of blocks |
| `201 Created` | Block mined and appended to the blockchain via `POST /` or `POST /legacy/bpm` | JSON object of the new block |
| `400 Bad Request` | Malformed JSON body or invalid transaction payload (e.g. empty list, missing address, zero amount) | `{"error": "invalid request body"}` |
| `405 Method Not Allowed` | HTTP method not allowed on endpoint (e.g. `PUT`, `DELETE`) | empty body (router-level response) |
| `408 Request Timeout` | Client disconnected or cancelled request during mining | `{"error": "mining cancelled"}` |
| `422 Unprocessable Entity` | Generated block failed chain validation | `{"error": "invalid block could not be added"}` |
| `422 Unprocessable Entity` | Block exceeds `MAX_BLOCK_BYTES` (checked before mining) | `{"error": "...", "code": "block_too_large", "details": {"size": 1200, "max": 1000}}` |
| `422 Unprocessable Entity` | Block timestamp earlier than its parent or more than 2h in the future | `{"error": "...", "code": "timestamp_before_parent" \| "timestamp_too_far_in_future", "details": {...}}` |
| `413 Request Entity Too Large` | `POST /` body larger than 2× `MAX_BLOCK_BYTES` | `{"error": "...", "code": "request_too_large", "details": {"max": 2000}}` |
| `504 Gateway Timeout` | Mining exceeded deadline set by `MINING_TIMEOUT` | `{"error": "mining timeout exceeded"}` |
| `500 Internal Server Error` | Unexpected internal server error | `{"error": "..."}` |

## Wallet
The `wallet` package provides identities based on **Ed25519** keys (`wallet.New`, `wallet.FromSeed`, `Sign`, `wallet.Verify`).

**Address format**: `hex(version || SHA-256(pubkey)[:20] || checksum)` in lowercase (50 chars), where `version` is `0x00` and `checksum = SHA-256(SHA-256(version || hash))[:4]`. Use `wallet.ValidateAddress` to check length, encoding, version and checksum.

**Keystore**: `Wallet.Save(path, password)` writes a JSON file (permissions `0600`, atomic write) containing the Ed25519 seed encrypted with AES-256-GCM, using a key derived via scrypt (`N=32768, r=8, p=1`) with a random 32-byte salt and 12-byte nonce per save. The address is bound to the file as GCM additional data. `wallet.Load(path, password)` returns `ErrWrongPassword` on a bad password and `ErrInvalidKeystore` for malformed or tampered files.
