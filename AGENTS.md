# AGENTS.md — blockchain-with-go

Guidelines for AI agents working in the `blockchain-with-go` repository.

## Project Context

Educational blockchain implementation in Go with block validation, SHA-256 hashing and a simple HTTP server.

- For the full architecture, consensus concepts and trade-offs, see [README.md](README.md).

## Repository Structure

```
blockchain-with-go/
├── cmd/
│   └── bwg/           # The only binary and composition root (cobra): node, wallet new/balance/send, miner
├── client/            # Go client generated from openapi.yaml by oapi-codegen (do not edit by hand)
├── internal/
│   ├── chain/         # chain.Service (Blocks/Mine), block generation, ErrInvalidBlock
│   ├── consensus/     # Consensus port and errors
│   │   ├── consensustest/ # Shared acceptance suite for Consensus adapters
│   │   ├── poa/       # Proof of Authority adapter (fixed Ed25519 validator set)
│   │   └── pow/       # Proof of Work adapter
│   ├── domain/        # Domain models (Block, Transaction, rules) — no internal dependencies
│   ├── mempool/       # mempool.Pool: admitted transactions, fee-rate selection (ADR-0012); depends only on domain
│   ├── p2p/           # Hello, handshake checks, peer book, persisted peer addresses, the dialer loop (ADR-0013) and block/tx gossip: inv/want, seen-set, relay worker (ADR-0018); depends only on domain
│   ├── server/        # HTTP adapter: server.Server (DI), handlers, DTOs and error mapping
│   ├── storage/       # BlockRepository port
│   │   ├── bolt/      # bbolt disk adapter
│   │   └── memory/    # In-memory adapter (sync.RWMutex)
│   └── vm/            # Contract VM: pure stack-machine interpreter with gas (ADR-0014); depends only on domain
├── pkg/
│   └── wallet/        # Ed25519 keys, addressing and keystore
├── Dockerfile         # Multi-stage image: static bwg on distroless nonroot, runs `bwg node` on 8080
├── .goreleaser.yaml   # Release build of bwg (linux/darwin/windows × amd64/arm64); run on `v*` tags by release.yml
├── openapi.yaml       # HTTP API contract (OpenAPI 3.0.3); internal/server/contract_test.go checks the server against it
├── go.mod             # Go module and dependencies
└── README.md          # Project documentation and run instructions
```

## Architecture (Ports & Adapters)

- Dependencies point inward: `cmd/bwg` → `server`/`chain`/adapters → `domain`. `internal/domain` never imports `storage`, `chain`, `server`, `net/http` or `os/signal`; `internal/chain` does not import `server` or `net/http`.
- No global state: `internal/server` and `internal/chain` only have `const` and `errors.New` sentinels at package level. Dependencies (repository, consensus, block generator) are injected through constructors (`chain.NewService(repo, consensus, opts...)`, `server.NewServer(svc, cfg)`).
- Composition root in `cmd/bwg` (the only binary; `bwg node` runs the node); it is the only place that triggers configuration reading (`PORT`, `MINING_TIMEOUT` and `DATA_DIR` directly; `CONSENSUS` (`pow`|`poa`) picks the engine, `POW_DIFFICULTY`, `POW_RETARGET_INTERVAL`, `TARGET_BLOCK_TIME` and `MINING_WORKERS` are read via `pow.NewProofOfWorkFromEnv`, `POA_VALIDATORS`/`POA_KEYSTORE`/`POA_KEYSTORE_PASSWORD` are parsed in `cmd/bwg`; `GENESIS_ALLOCATIONS`, genesis outputs as `address:amount[,...]`, parsed in `cmd/bwg` and ignored when the repository is not empty; `MINER_ADDRESS`, the address that receives the coinbase of every mined block, validated by `wallet.ValidateAddress` at startup and blank for no coinbase); the peer-to-peer variables `SEED_PEERS`, `P2P_LISTEN_ADDR`, `MAX_INBOUND_PEERS`, `MAX_OUTBOUND_PEERS` and `PEER_INTERVAL` are parsed in `cmd/bwg/p2p.go` (`DATA_DIR/peers.json` holds the known peer addresses); the one exception is `MAX_BLOCK_BYTES`, read on demand by `domain.MaxBlockBytes()` in `internal/domain/rules.go` (falls back to `DefaultMaxBlockBytes`, 1 MiB, when unset or not a positive integer).

## Core Areas (Risk: core)

A story is **Risk: core** when it changes any of the areas below. Core stories need the second review (see the Lead's routing); all others are **normal**.

- **Block and transaction validation:** `internal/domain/block.go`, `rules.go`, `transaction.go`, `utxo.go` (hashing, validation order, UTXO rules, double-spend).
- **Consensus:** `internal/consensus/` and `internal/consensus/pow/` (`Seal`/`Verify`, difficulty, error sentinels).
- **Chain write path and concurrency:** `internal/chain/service.go` (`Mine`, append, any lock or semaphore, future reorg and sync) and `internal/mempool/` (pool lock, admission and eviction order).
- **Storage guarantees:** `internal/storage/` contracts and adapters (`memory`, `bolt`), including atomicity of `Append`/`Apply`/`Revert` and the `storagetest` contract.
- **Contract VM:** `internal/vm/` (opcodes, gas costs, error sentinels, `Execute`/`ValidateCode`; from ADR-0014 on these are consensus rules) and the limits in `internal/domain/contract.go`.
- **Peer-to-peer:** `internal/p2p/` (handshake checks, peer book lock and limits, dialer loop; ADR-0013).
- **Keys and signatures:** `pkg/wallet/` (Ed25519, addressing, keystore).
- **Schema:** any change to the persisted or hashed shape of `Block`, `Transaction` or UTXO, and any ADR-level change.

Everything else (HTTP handlers and DTOs, logging, docs, README, CLI, build files, `cmd/bwg` wiring that does not change configuration semantics) is **normal**. When a story is borderline, mark it core.

## Rules and Commands

1. **Validation and Tests:**
   - Run tests and type checking: `go test ./...`
   - Go formatting (gofumpt + goimports): `golangci-lint fmt`; auto-fix style findings (wsl, nlreturn, godot): `golangci-lint run --fix ./...`
   - Build check: `go build -o /dev/null ./cmd/bwg`
   - Run the node locally: `go run ./cmd/bwg node`
   - Docker smoke (build, non-root user, `GET /health` in memory and with `DATA_DIR=/data`): `scripts/docker-smoke.sh`
   - Release config check and local snapshot build: `go run github.com/goreleaser/goreleaser/v2@v2.19.0 check` and `go run github.com/goreleaser/goreleaser/v2@v2.19.0 release --snapshot --clean` (output in `dist/`, ignored)
   - Regenerate the client after editing `openapi.yaml`: `go generate ./client/...` (CI fails on `git diff --exit-code client/`)
   - CI quality gates (`.github/workflows/go-ci.yml`), local equivalents:
     - Tests with race detector and coverage profile: `go test -race -covermode=atomic -coverprofile=coverage.out ./...`
     - Coverage floor: `scripts/check-coverage.sh coverage.out 90.0` (fails when total statement coverage is below **90.0%**; excludes, by explicit import path, the contract suites `internal/consensus/consensustest` and `internal/storage/storagetest`, which only run from adapter tests, and the generated `client`, exercised by `internal/server/contract_test.go`). Reference measurement: 94,6% at `450f42e`, without the two contract suites.
     - Lint: `golangci-lint run ./...` (golangci-lint v2.15.0; the linter set, formatters and exclusions live in `.golangci.yml`, every exclusion carries its reason; a new `//nolint` needs one too)
     - Vulnerabilities: `go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...`
   - Mutation testing (gremlins v0.6.0) — two supported forms:
     - Per package: `go run github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0 unleash --timeout-coefficient 20 ./internal/<package>` (or `./pkg/wallet`)
     - Per diff: `go run github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0 unleash --timeout-coefficient 20 -E 'storagetest|consensustest' --diff <base>` (run at the root and **without** a package; `-E` leaves out the shared contract suites, which are test code and have no coverage of their own)
     - **Execution rule:** never combine `--diff` with a package path (it produces a false green with every mutant marked `Skipped`). If `Skipped` equals the total, the result is invalid. Mutants with status `TIMED OUT` do **not** count as killed: gremlins did not classify them. Run `go clean -testcache` and repeat before accepting the result. With the test cache active, the coverage run takes milliseconds, the time limit becomes tiny and almost every mutant turns `TIMED OUT` (false green).
     - **Results are not recorded in the repository:** paste the gremlins output into the issue comment and the PR description. Do not create or update files in `docs/` to record it.
   - Mutation threshold: zero `LIVED` or `NOT COVERED` mutants on the lines changed by the diff. An equivalent mutant is only accepted if it is listed in the QA report with a one-line justification and the reviewer agrees. A surviving mutant remains blocking.
2. **Best Practices:**
   - Keep Go code idiomatic (explicit error handling, concise naming).
   - Do not add heavy dependencies without justifiable need.
   - Commit messages following Conventional Commits (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`).
   - Replies to the user in Brazilian Portuguese; code and commits in English.
