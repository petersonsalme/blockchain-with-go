# ADR-0027: read-only supply route `GET /supply`

Status: Accepted (extends ADR-0015 and ADR-0026)

## Context
The dashboard and the indexer want the total supply without scanning blocks. Issuance follows ADR-0015: the genesis allocations, then at most one coinbase per non-genesis block paying at most `Subsidy(h)` plus the fees of the block; fees only move coins, a coinbase may claim less (the rest is burned) and a node without `MINER_ADDRESS` mines blocks without a coinbase. Options:
- (A) the schedule: the sum of `domain.Subsidy(h)` for h = 1..height, computed from the tip height in closed form, one halving era at a time. One `Tip()` read, at most 33 steps.
- (B) the actual coinbase issuance: read every block, add the coinbase outputs and subtract the fees, which needs the value of every spent input. O(height) block reads, and a pruned node no longer has the bodies.
- (C) the coins in existence: the total of the UTXO set. It needs a new accessor in `chain.Service` and storage (core areas) and costs O(UTXO) per request or a maintained total.

## Decision
- A: `GET /supply` (and `HEAD`) answers `{height, issued, initial_subsidy, halving_interval}`. `issued` is the sum of `Subsidy(h)` for h = 1..`height` as a decimal string; the genesis (h = 0) has no coinbase and adds nothing. `initial_subsidy` is `domain.InitialSubsidy` as a decimal string, `halving_interval` is `domain.HalvingInterval`.
- The handler reads the tip once and computes `issued` from its index, so it never answers `chain_changed`. A chain that cannot be read answers 503 `unhealthy`. There is no configuration, so there is no 404.
- `bwg dashboard` adds `/supply` to its allowlist, like `/consensus` (ADR-0026).

## Consequences
- The HTTP contract grows by one read-only route; no node state, schema or consensus rule changes.
- `issued` is an upper bound on coinbase issuance: it is exact only when every non-genesis block claimed its full subsidy, and it excludes the genesis allocations. Clients that need the coins in existence need option C, in a new ADR.
- The value never exceeds 2,099,994,997,690,000 base units, reached at height 6,929,999; it fits in `uint64`.
