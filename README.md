# GoBlockchain
[![Go CI](https://github.com/petersonsalme/blockchain-with-go/actions/workflows/go-ci.yml/badge.svg)](https://github.com/petersonsalme/blockchain-with-go/actions/workflows/go-ci.yml)

A simple blockchain implementation in Go, created as an exercise to demonstrate core blockchain concepts.

## Features Implemented
- **Basic Block Structure**: Blocks containing index, timestamp, data (BPM - Beats Per Minute), current hash, and previous hash.
- **Cryptographic Hashing**: Uses SHA-256 to ensure data integrity.
- **Chain Validation**: Validates new blocks by checking the index, previous hash, and current hash computation.
- **HTTP API**: A simple RESTful API built with `gorilla/mux` to view the blockchain and append new blocks.

## Consensus Mechanism
This implementation utilizes a simplified **Longest Chain Rule** (represented by the `replaceChain` function). When confronted with multiple valid chains, the node will accept the longest chain as the source of truth. However, since this iteration does not implement peer-to-peer networking or block broadcasting, chain forks do not occur naturally. Block validation checks cryptographic linkage, index sequentiality, and Proof of Work.

## Architectural Trade-offs
1. **In-Memory Storage vs. Persistence**: The blockchain state is kept entirely in memory. While this keeps the implementation simple, fast, and dependency-free, all data is lost when the server is restarted. A production system would require a persistent datastore (like LevelDB or RocksDB).
2. **Centralization vs. P2P Network**: Currently, the blockchain runs as a single centralized HTTP server rather than a decentralized peer-to-peer network. This eliminates the complexity of network discovery, peer management, and gossip protocols, but makes the system centralized.
3. **Sybil Protection (Proof of Work)**: A Proof of Work (PoW) mechanism is implemented to require computational effort for block creation and mitigate spam. The difficulty (number of leading zeros required in the block hash) is configured via the `POW_DIFFICULTY` environment variable, defaulting to `2`. However, there are known caveats:
   - The required difficulty is read directly from the environment during validation (`POW_DIFFICULTY`), rather than being negotiated across nodes or recorded/adjusted adaptively within the chain itself.
   - Block mining is performed synchronously inside the HTTP handler when receiving a new block request, without an upper limit on difficulty, which can block request handling indefinitely if the difficulty is set too high.
4. **Simplistic Payload (BPM)**: The block payload is hardcoded to store a simple integer (`BPM`). A real-world application would use a more generic payload structure (like Merkle trees of transactions).

## Running locally

Install dependencies and run:
```shell
$ go run main.go
```

## API Calls

### GET
To list all existent blocks:

```shell
$ curl http://localhost:8080
```

### POST
To create a new block:
```shell
$ curl -H "Content-Type: application/json" -d '{"BPM":10}' http://localhost:8080
```
