# GoBlockchain

A simple blockchain implementation in Go, created as an exercise to demonstrate core blockchain concepts.

## Features Implemented
- **Basic Block Structure**: Blocks containing index, timestamp, data (BPM - Beats Per Minute), current hash, and previous hash.
- **Cryptographic Hashing**: Uses SHA-256 to ensure data integrity.
- **Chain Validation**: Validates new blocks by checking the index, previous hash, and current hash computation.
- **HTTP API**: A simple RESTful API built with `gorilla/mux` to view the blockchain and append new blocks.

## Consensus Mechanism
This implementation utilizes a simplified **Longest Chain Rule** (represented by the `replaceChain` function). When confronted with multiple valid chains, the node will accept the longest chain as the source of truth. However, since this iteration does not implement peer-to-peer networking or block broadcasting, chain forks do not occur naturally. Block validation simply checks cryptographic linkage and index sequentiality. There is no Proof of Work (PoW) or Proof of Stake (PoS) implemented in this basic version.

## Architectural Trade-offs
1. **In-Memory Storage vs. Persistence**: The blockchain state is kept entirely in memory. While this keeps the implementation simple, fast, and dependency-free, all data is lost when the server is restarted. A production system would require a persistent datastore (like LevelDB or RocksDB).
2. **Centralization vs. P2P Network**: Currently, the blockchain runs as a single centralized HTTP server rather than a decentralized peer-to-peer network. This eliminates the complexity of network discovery, peer management, and gossip protocols, but makes the system centralized.
3. **Lack of Sybil Protection (No PoW/PoS)**: Block creation requires minimal computational effort. Without a Sybil resistance mechanism, a malicious actor could easily spam the network with blocks or rewrite the chain history.
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
