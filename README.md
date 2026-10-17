# GoBlockchain
[![Go CI](https://github.com/petersonsalme/blockchain-with-go/actions/workflows/go-ci.yml/badge.svg)](https://github.com/petersonsalme/blockchain-with-go/actions/workflows/go-ci.yml)

A simple blockchain implementation in Go, created as an exercise to demonstrate core blockchain concepts.

## Features Implemented
- **Basic Block Structure**: Blocks containing index, timestamp, data (BPM - Beats Per Minute), current hash, previous hash, nonce, and difficulty.
- **Cryptographic Hashing**: Uses SHA-256 to ensure data integrity across all block attributes (including nonce and difficulty).
- **Proof of Work & Context-Aware Mining**: Nonce search satisfying leading-zero difficulty, supporting graceful context cancellation and mining timeouts.
- **Chain Validation**: Validates new blocks by checking index sequentiality, cryptographic linkage (`PrevHash`), SHA-256 hash integrity, and Proof of Work against each block's recorded difficulty.
- **HTTP API**: A RESTful API built with `gorilla/mux` to view the blockchain, append new blocks, and report structured errors with appropriate HTTP status codes.

## Consensus Mechanism
This implementation utilizes a simplified **Longest Chain Rule** (represented by the `replaceChain` function). When confronted with multiple valid chains, the node will accept the longest chain as the source of truth. However, since this iteration does not implement peer-to-peer networking or block broadcasting, chain forks do not occur naturally. Block validation checks cryptographic linkage, index sequentiality, and Proof of Work against the block's recorded difficulty.

## Architectural Trade-offs
1. **In-Memory Storage vs. Persistence**: The blockchain state is kept entirely in memory. While this keeps the implementation simple, fast, and dependency-free, all data is lost when the server is restarted. A production system would require a persistent datastore (like LevelDB or RocksDB).
2. **Centralization vs. P2P Network**: Currently, the blockchain runs as a single centralized HTTP server rather than a decentralized peer-to-peer network. This eliminates the complexity of network discovery, peer management, and gossip protocols, but makes the system centralized.
3. **Sybil Protection (Proof of Work)**: A Proof of Work (PoW) mechanism is implemented to require computational effort for block creation and mitigate spam. The difficulty (number of leading zeros required in the block hash) is configured via the `POW_DIFFICULTY` environment variable, defaulting to `2`.
   - **Difficulty Decoupled from Validation**: Each block records its difficulty in `b.Difficulty` at mining time, and validation checks the block's proof of work against its own recorded difficulty (which is included in the SHA-256 hash). Dynamic difficulty adjustment (e.g. retargeting) across nodes is not implemented.
   - **Context Cancellation & Timeouts**: Mining supports cooperative cancellation via `context.Context` (`MineWithContext`, `GenerateBlockWithContext`). The HTTP handler applies a configurable mining timeout via `MINING_TIMEOUT` (defaulting to 10 seconds) and aborts immediately if the client disconnects or cancels the request, preventing request processing from blocking indefinitely.
4. **Simplistic Payload (BPM)**: The block payload is hardcoded to store a simple integer (`BPM`). A real-world application would use a more generic payload structure (like Merkle trees of transactions).

## Configuration & Environment Variables

The application can be configured via environment variables (e.g., in a `.env` file):

| Variable | Description | Default |
|---|---|---|
| `PORT` | HTTP server port | `8080` |
| `POW_DIFFICULTY` | Number of leading zeros required for block hashes | `2` |
| `MINING_TIMEOUT` | Maximum duration allowed for mining a block (e.g., `10s`, `500ms`, or integer seconds) | `10s` |

## Running Locally

Install dependencies and run:
```shell
$ go run main.go
```

To run test suites:
```shell
$ go test -v ./...
$ go test -race ./...
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
    "BPM": 0,
    "Hash": "00a1b2c3...",
    "PrevHash": "",
    "Nonce": 42,
    "Difficulty": 2
  }
]
```

### POST /
Create and mine a new block with the provided BPM data.

```shell
$ curl -X POST -H "Content-Type: application/json" -d '{"BPM":10}' http://localhost:8080/
```

**Success Response (`201 Created`):**
```json
{
  "Index": 1,
  "Timestamp": "2026-10-16T12:05:00Z",
  "BPM": 10,
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
| `201 Created` | Block mined and appended to the blockchain via `POST /` | JSON object of the new block |
| `400 Bad Request` | Malformed JSON body or invalid payload structure | `{"error": "corpo da requisição inválido"}` |
| `405 Method Not Allowed` | HTTP method not allowed on endpoint (e.g. `PUT`, `DELETE`) | `{"error": "método não permitido"}` |
| `408 Request Timeout` | Client disconnected or cancelled request during mining | `{"error": "mineração cancelada"}` |
| `422 Unprocessable Entity` | Generated block failed chain validation | `{"error": "bloco inválido não pôde ser adicionado"}` |
| `504 Gateway Timeout` | Mining exceeded deadline set by `MINING_TIMEOUT` | `{"error": "tempo limite de mineração excedido"}` |
| `500 Internal Server Error` | Unexpected internal server error | `{"error": "..."}` |
