# ADR-0020: Deterministic genesis time from the GENESIS_TIME environment variable

Status: Accepted (ENG-41)

## Context
- `chain.Service` creates the genesis block of an empty repository with `time.Now()` as the block timestamp and as the timestamp of the allocation transaction (`internal/chain/service.go`, `genesis()`).
- `Block.CalculateHash` covers the timestamp, so two nodes started independently always have different genesis blocks, and the hello check (ADR-0013) refuses them with `409 genesis_mismatch`.
- Today nodes only pair when they share storage or when a test copies the genesis between in-process repositories (`cmd/bwg/sync_test.go`). The three-node `docker compose` scenario of ENG-41 starts three separate processes on three empty volumes, so it cannot form a network.

## Options
1. **`GENESIS_TIME` environment variable (chosen).** An RFC 3339 instant passed to a new `chain.WithGenesisTime` option; same `GENESIS_TIME` + same `GENESIS_ALLOCATIONS` on empty storage gives the same genesis. Cost: one option, one parser, docs. Opt-in, current behaviour unchanged when unset.
2. **Fixed genesis time for every node (for example the Unix epoch).** No configuration, like a hard-coded Bitcoin genesis. Cost: changes the default for everyone; every network with the same allocations shares one genesis, so unrelated test networks would pair.
3. **Genesis file export/import (`bwg genesis export`, `GENESIS_FILE`).** Exact copy of any genesis, including the allocation transaction. Cost: a new file format, a new command and validation of an imported block; much larger than the problem.

## Decision
- `cmd/bwg` reads `GENESIS_TIME`, an RFC 3339 instant (for example `2026-01-01T00:00:00Z`), and passes it to `chain.WithGenesisTime`. The variable is read only in the composition root, like `GENESIS_ALLOCATIONS` (ADR-0002).
- `chain.WithGenesisTime(t)` makes the genesis block of an empty repository, and its allocation transaction, use `t` as timestamp. A zero `t` (the variable empty or only spaces) keeps `time.Now()`.
- A value that does not parse as RFC 3339, or that is later than the current clock, stops the node at startup and the error names the variable. A future genesis would make every child fail `ValidateTimestamp` (`timestamp_before_parent`) until the clock passes it.

## Consequences
- Nodes started with the same `GENESIS_TIME` and `GENESIS_ALLOCATIONS` (same items, same order) on empty storage have the same genesis hash and pair; this is what `docker-compose.yml` uses.
- When the repository is already populated, the variable is ignored, like `GENESIS_ALLOCATIONS`; changing it never rewrites an existing chain.
- The hashed shape of `Block` and `Transaction` does not change; only the value of one field of block 0 becomes configurable.
- Anyone who knows the two variables can create a node of the same network; there is still no authentication between peers (SECURITY.md).
