# ADR-0011: Persisted indexes — UTXO set, address → transactions, txid → height

Status: Accepted (board, 2026-10-22, ENG-28) — option B

## Context
- ENG-28 item 1 is **already delivered**: `BlockRepository.ByHeight`/`ByHash` exist; `memory` answers both from a slice/map (O(1)); `bolt` answers with one B+tree key lookup (`blocks` and `hashes` buckets, O(log n) pages, no chain scan). Strict O(1) is not possible on bbolt; the measurable criterion is "time independent of chain length" (benchmark at 1k vs 100k blocks).
- Full-chain scans that remain: `chain.Service.Transaction` and `TxProof` call `repo.All()`; `Balance` scans a full UTXO snapshot; there is no address → transactions query; startup re-applies every block (ADR-0004 consequence).
- ADR-0004 decided: the UTXO set is **not** persisted until startup time is measured as a problem. No such measurement exists. ENG-28 item 2 reverses that decision, so it needs this ADR.
- ADR-0004 also left a gap (ENG-83 review): `Append` succeeds, `UTXOSet.Apply` fails → `ErrStateDiverged` until restart.
- ENG-26 (ENG-111 `ReplaceFrom`, ENG-112 `ReplaceChain`) is changing the same port and the write path right now. Any persisted index must follow a reorg, so it needs the undo delta per block.

## Options
A. **Keep everything derived in memory** (ADR-0004 stands). Add in-memory `txid → height` and `address → []txid` indexes next to the UTXO set, rebuilt at start; "reindex" = the existing rebuild, proven by `rebuild(All()) == incremental`. Cost: low (1 story, chain + memory adapter). Does not persist anything; startup stays O(chain); the divergence gap stays.
B. **Persist in the same bbolt transaction as the block.** The write port takes the block and its `UTXODelta` together (e.g. `Commit(b, delta)`; `ReplaceFrom` takes the deltas of the new blocks and reads the stored undo deltas of the removed ones). Buckets: `utxos`, `undo` (height → delta), `txs` (txid → height), `addrtx` (address|height|txid → ∅), `meta` (`indexedTip`). Memory adapter mirrors it under its one mutex. Startup: if `indexedTip == tip`, no rebuild; otherwise reindex. Reindex command: `go run ./cmd/node -reindex` (drops and rebuilds the index buckets from `blocks`). Cost: high (3 core stories, port + chain write path + storagetest contract). Removes the `ErrStateDiverged` window for both adapters; reorg undo comes from disk.
C. **Persist in a second bbolt transaction after `Append`**, with `indexedTip` and automatic reindex at start when it lags. Cost: medium. Port change smaller, but the divergence window stays (only self-heals on restart) and reorg must coordinate two commits.

## Decision
Option B (chosen by the board). It is the only option where block and indexes cannot disagree on disk, which also closes the ENG-83 gap instead of only surviving it; the undo bucket is what ENG-26 needs for a persisted reorg. Sequence it after ENG-112 merges, so the port is changed once on top of `ReplaceFrom`.

## Consequences
- Supersedes ADR-0004's "UTXO set is not persisted" and the fail-stop rationale for bolt (fail-stop stays as a guard but becomes unreachable on a committed write).
- On-disk format grows four buckets; an old `chain.db` has no `meta` key → treated as "lagging" → reindexed on first start (no manual migration).
- Invariant to prove in tests: for any chain, `reindex(blocks)` produces byte-identical index buckets to incremental `Commit`/`ReplaceFrom`.
- `storage.UTXOSet` as a separate port either disappears or becomes a read view of the repository; decided in story 1.
- Startup becomes O(1) when the index is current; measured in story 2 (before/after at 10k blocks), recorded under "What we learned".

## Delivery (each a separate story)
1. Storage (core): new port `storage.IndexedRepository` (embeds `BlockRepository` + `domain.UTXOView`) with `Commit(b, delta)`, `Reorg(height, blocks, deltas)`, `TxHeight(txid)`, `TxsByAddress(addr)`, `IndexCurrent()`, `Reindex()`; buckets `utxos`, `undo`, `txs`, `addrtx`, `meta`; memory and bolt; `storagetest` contract incl. reindex equivalence. Additive: nothing in `internal/chain` changes.
2. Chain (core): `Mine` uses `Commit`, `ReplaceChain` uses `Reorg`; `verifyInputs`/`Balance`/`Transaction`/`TxProof` read the repository indexes (no `All()` scan for a tx); `TransactionsByAddress`; startup reindexes only when `IndexCurrent()` is false; `storage.UTXOSet`, `memory.UTXOSet` and `WithUTXOSet` removed; startup measured at 10k blocks before/after → "What we learned".
3. CLI (normal): `go run ./cmd/node -reindex` + README.
Follow-up outside ENG-28: `GET /address/{addr}/txs`; an address → unspent index for `Balance`.

