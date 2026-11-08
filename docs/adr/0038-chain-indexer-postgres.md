# ADR-0038: Reorg-safe chain indexer in Postgres

Status: Proposed (Spec-Opus, 2026-11-08, ENG-228)

## Context
- The explorer needs the coins in existence, the richest addresses and stats over long ranges. `GET /supply` (ADR-0027) answers only the scheduled issuance, an upper bound; ADR-0027 deferred the UTXO total to a new ADR. The node keeps an address index in bbolt (ADR-0011) but nothing ranks balances across addresses.
- Real explorers split the node from an indexer with a database (Blockscout: node -> indexer -> Postgres -> API; mempool.space: Bitcoin Core -> electrs -> MariaDB). The node validates and stores; the indexer derives query tables; heavy queries never reach the node.
- The node's `GET /events` (block and tx events) drops a client 16 events behind, has no replay, no sequence number and no reorg event. `GET /blocks` reads one page in one bbolt view; `GET /headers` pages 1000 headers.
- Fees only move coins; a coinbase may claim less than subsidy + fees, and blocks mined without `MINER_ADDRESS` have no coinbase, so their fees are burned (ADR-0015). Contracts hold no coins (ADR-0037).

## Options
1. Indexer inside the node, writing extra bbolt buckets in the append path. Cost: touches `internal/chain` and storage (core areas), adds write work under the chain lock, and the queries we want (rank, range sums) are what a key-value store does badly.
2. Separate service, push-driven from `/events`, with a new reorg event and per-message sequence numbers in the node. Cost: a core change to the node, and a replay buffer to survive disconnects; a missed message is still possible, so a pull-based check is needed anyway.
3. Separate service, pull-driven by height with hash-chain continuity, Postgres store (chosen). Cost: polling latency (1 s by default) and one extra service and database in compose; no node change.
4. Same as 3 with SQLite. Cost: no concurrent reader/writer server, not what the referenced explorers run; would teach less about operating a database service.

## Decision
- Option 3. `bwg indexer run` follows one node through its public API (`/blocks`, `/headers`) and writes Postgres. The stored tip (height, hash) is the cursor: a block is applied only when its PrevHash is the stored tip hash, rows and tip in one transaction.
- Reorg: on a broken link, walk back over `/headers` to the highest common height F, roll back F+1..H in one transaction, apply forward. A different genesis stops the indexer; `bwg indexer resync` rebuilds from height 0.
- Recovery after being offline or killed: the database is always a hash chain from the genesis, so a restart resumes at the stored tip; a block is never applied twice (primary keys, plus the PrevHash check) and no height is skipped (Index must be tip+1).
- One writer per database (Postgres advisory lock). The indexer needs a node that keeps every body (`PRUNE_KEEP_BLOCKS=0`); a 410 `block_pruned` stops it.
- Supply per height = sum of outputs created minus inputs spent up to that height; it equals the genesis allocations plus coinbase issuance net of fees and of burned fees, and the sum of balances.
- The genesis is never sealed: the node serves it with an empty `Hash`, and block 1 carries `PrevHash` `""`. The indexer names the genesis by what it commits to, the hash the peer-to-peer handshake compares (`Block.CalculateHash`, recomputed by the node client from the genesis the API serves), and presents block 1 as chained on it. That is what makes "a different genesis" detectable and `blocks` a hash chain from height 0.
- Tools, per ADR-0021: Postgres (separate service, PostgreSQL License); `github.com/jackc/pgx/v5` (MIT) as the adapter in `internal/indexer/postgres`; `github.com/fergusstrange/embedded-postgres` (MIT) in tests only, so `go test ./...` needs no docker. Modules added to go.mod: direct `github.com/jackc/pgx/v5` v5.12.0 and `github.com/fergusstrange/embedded-postgres` v1.34.0; indirect `github.com/jackc/pgpassfile` v1.0.0, `github.com/jackc/pgservicefile` (2024-06-06), `github.com/lib/pq` v1.10.9 and `github.com/xi2/xz` (2017-12-30), the last two pulled by embedded-postgres. Writing a Postgres driver by hand would teach nothing about blockchains. No Redis, no queue.

## Consequences
- The node is unchanged; `/events` keeps its contract. A wake-up from `/events` to cut polling latency is a possible follow-up.
- The indexer lags the node by up to one poll interval and may show a shorter prefix while a reorg is being applied, never a mix of branches.
- Postgres becomes a dependency of the indexer only; the base network, the node and its tests do not need it.
- First test run downloads the Postgres binaries embedded-postgres uses (cached afterwards); CI caches them.
