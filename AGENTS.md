# AGENTS.md — blockchain-with-go

Guidelines for AI agents working in the `blockchain-with-go` repository.

## Project Context

Educational blockchain implementation in Go with block validation, SHA-256 hashing and a simple HTTP server.

- For the full architecture, consensus concepts and trade-offs, see [README.md](README.md).

## Repository Structure

```
blockchain-with-go/
├── cmd/
│   └── node/          # Application entry point (main.go)
├── internal/
│   ├── chain/         # chain.Service (Blocks/Mine), block generation, ErrInvalidBlock
│   ├── consensus/     # Consensus port and errors
│   │   ├── consensustest/ # Shared acceptance suite for Consensus adapters
│   │   ├── poa/       # Proof of Authority adapter (fixed Ed25519 validator set)
│   │   └── pow/       # Proof of Work adapter
│   ├── domain/        # Domain models (Block, Transaction, rules) — no internal dependencies
│   ├── server/        # HTTP adapter: server.Server (DI), handlers, DTOs and error mapping
│   └── storage/       # BlockRepository port
│       ├── bolt/      # bbolt disk adapter
│       └── memory/    # In-memory adapter (sync.RWMutex)
├── pkg/
│   └── wallet/        # Ed25519 keys, addressing and keystore
├── go.mod             # Go module and dependencies
└── README.md          # Project documentation and run instructions
```

## Architecture (Ports & Adapters)

- Dependencies point inward: `cmd/node` → `server`/`chain`/adapters → `domain`. `internal/domain` never imports `storage`, `chain`, `server`, `net/http` or `os/signal`; `internal/chain` does not import `server` or `net/http`.
- No global state: `internal/server` and `internal/chain` only have `const` and `errors.New` sentinels at package level. Dependencies (repository, consensus, block generator) are injected through constructors (`chain.NewService(repo, consensus, opts...)`, `server.NewServer(svc, cfg)`).
- Composition root in `cmd/node/main.go`; it is the only place that triggers configuration reading (`PORT`, `MINING_TIMEOUT` and `DATA_DIR` directly; `CONSENSUS` (`pow`|`poa`) picks the engine, `POW_DIFFICULTY` is read via `pow.NewProofOfWorkFromEnv`, `POA_VALIDATORS`/`POA_KEYSTORE`/`POA_KEYSTORE_PASSWORD` are parsed in `cmd/node`; `GENESIS_ALLOCATIONS`, genesis outputs as `address:amount[,...]`, parsed in `cmd/node` and ignored when the repository is not empty).

## Core Areas (Risk: core)

A story is **Risk: core** when it changes any of the areas below. Core stories need the second review (see the Lead's routing); all others are **normal**.

- **Block and transaction validation:** `internal/domain/block.go`, `rules.go`, `transaction.go`, `utxo.go` (hashing, validation order, UTXO rules, double-spend).
- **Consensus:** `internal/consensus/` and `internal/consensus/pow/` (`Seal`/`Verify`, difficulty, error sentinels).
- **Chain write path and concurrency:** `internal/chain/service.go` (`Mine`, append, any lock or semaphore, future reorg and sync).
- **Storage guarantees:** `internal/storage/` contracts and adapters (`memory`, `bolt`), including atomicity of `Append`/`Apply`/`Revert` and the `storagetest` contract.
- **Keys and signatures:** `pkg/wallet/` (Ed25519, addressing, keystore).
- **Schema:** any change to the persisted or hashed shape of `Block`, `Transaction` or UTXO, and any ADR-level change.

Everything else (HTTP handlers and DTOs, logging, docs, README, CLI, build files, `cmd/node` wiring that does not change configuration semantics) is **normal**. When a story is borderline, mark it core.

## Rules and Commands

1. **Validation and Tests:**
   - Run tests and type checking: `go test ./...`
   - Standard Go formatting: `gofmt -s -w .` or `goimports`
   - Build check: `go build -o /dev/null ./cmd/node`
   - Run the node locally: `go run ./cmd/node`
   - Mutation testing (gremlins v0.6.0) — two supported forms:
     - Per package: `go run github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0 unleash --timeout-coefficient 20 ./internal/<package>` (or `./pkg/wallet`)
     - Per diff: `go run github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0 unleash --timeout-coefficient 20 --diff <base>` (run at the root and **without** a package)
     - **Execution rule:** never combine `--diff` with a package path (it produces a false green with every mutant marked `Skipped`). If `Skipped` equals the total, the result is invalid. Mutants with status `TIMED OUT` do **not** count as killed: gremlins did not classify them. Run `go clean -testcache` and repeat before accepting the result. With the test cache active, the coverage run takes milliseconds, the time limit becomes tiny and almost every mutant turns `TIMED OUT` (false green).
     - **Results are not recorded in the repository:** paste the gremlins output into the issue comment and the PR description. Do not create or update files in `docs/` to record it.
   - Mutation threshold: zero `LIVED` or `NOT COVERED` mutants on the lines changed by the diff. An equivalent mutant is only accepted if it is listed in the QA report with a one-line justification and the reviewer agrees. A surviving mutant remains blocking.
2. **Best Practices:**
   - Keep Go code idiomatic (explicit error handling, concise naming).
   - Do not add heavy dependencies without justifiable need.
   - Commit messages following Conventional Commits (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`).
   - Replies to the user in Brazilian Portuguese; code and commits in English.
