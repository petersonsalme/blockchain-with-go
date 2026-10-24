# ADR-0004: Disk storage — bbolt adapter, UTXO set stays derived in memory

Status: Superseded in part by ADR-0011 (accepted by the board, 2026-10-19, ENG-27)

## Context
- All state is in memory and lost on restart (README trade-off 1). Port `storage.BlockRepository` (ENG-64, extended by ENG-86) has one adapter, `memory`.
- ADR-0001: the UTXO set is derived state; `NewService` rebuilds it from `repo.All()`.
- ENG-83 review: if `UTXOSet.Apply` fails after a successful `Append`, chain and set diverge.

## Options
A. bbolt: pure Go, single file, ACID transactions, concurrent readers. B. BadgerDB: heavy dependency tree, multi-file, value-log GC. C. Own append-only file: recovery and indexing must be built and proven by us.

## Decision
Option A, package `internal/storage/bolt`.
- Buckets: `blocks` (key = height as 8-byte big-endian, value = JSON of `domain.Block`) and `hashes` (hash → height key). One `Append` = one `db.Update`: tip check, both puts. Commit is fsynced, so a crash leaves either the whole block or nothing.
- The UTXO set is **not** persisted. It stays in memory and is rebuilt from the chain at start (ADR-0001). Persisting it is deferred until startup time is measured as a problem.
- `chain.Service` fails stop: once `UTXOSet.Apply` fails after `Append`, every later `Mine` returns `chain.ErrStateDiverged`. A restart rebuilds the set from the repository.
- `cmd/node` picks the adapter: `DATA_DIR` empty → `memory`, otherwise bolt at `$DATA_DIR/chain.db`.

## Consequences
- Startup is O(chain length): every block is decoded and its delta re-applied.
- One process per data file: bbolt takes an exclusive file lock; a second node on the same `DATA_DIR` fails at start.
- The on-disk format is JSON of `domain.Block`; a schema change of `Block` needs a migration or a wipe (no versioning in this ADR).
