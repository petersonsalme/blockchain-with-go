# ADR-0026: read-only consensus status route `GET /consensus`

Status: Accepted (extends ADR-0023)

## Context
The dashboard (ADR-0023) has no consensus screen. The engine (`CONSENSUS`) and its parameters (`POW_DIFFICULTY`, `POW_RETARGET_INTERVAL`, `TARGET_BLOCK_TIME`, `POA_VALIDATORS`) exist only as environment configuration, so nobody can see over HTTP whether the nodes of a network are configured the same. The reorg rule (ADR-0010, ENG-26) compares accumulated work, which no route reports. Options: (A) a new route `GET /consensus`; (B) more fields in `/health`. B is cheaper for the dashboard (the poller already reads `/health`) but turns a liveness probe that Docker, the e2e test and the poller call every 2 s into an O(height) read, and mixes configuration with liveness.

## Decision
- A: `GET /consensus` (and `HEAD`) answers `{engine, height, tip, chain_work, pow?: {initial_difficulty, retarget_interval, target_block_time_ms}, poa?: {validators}}`. Exactly one of `pow` and `poa` is present. `chain_work` is a decimal string (2^Difficulty sums overflow int64).
- `cmd/bwg` builds the parameters from the configuration it already parsed and passes them to `server.Config.Consensus`; `internal/consensus` gains no accessor. The handler reads the tip, then the headers 0..tip in pages of `BlocksRange`, checks that they link by `PrevHash` up to the tip and sums `domain.ChainWork`. A chain that changed during the read answers 503 `chain_changed`; the caller retries. No cache: the cost is O(height) header reads per request, paid only by whoever asks.
- PoA has no turn order (ADR-0005): any validator may seal. The route reports the set, not a turn.
- `bwg dashboard` adds `/consensus` to its allowlist and a Consensus screen that reads it on demand, not from the background poller.

## Consequences
- The HTTP contract grows by one read-only route; no node state, schema or consensus rule changes.
- A request costs O(height) header reads; acceptable for the playground's chains. If a measurement shows it is not, cache the work per tip in `chain.Service` in a new ADR.
- The parameters shown are what the node was configured with, not values proved by the chain; a node lying about them would still be caught by `Verify`.
