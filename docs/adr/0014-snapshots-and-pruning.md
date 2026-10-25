# ADR-0014: UTXO snapshots, body pruning and boot from a snapshot

Status: Accepted (board, 2026-10-24, ENG-29) — option B. Final number is taken at merge (ADR-0013 is the rate limiter; ENG-30 may also add one).

## Context
- After ADR-0011 (ENG-28) bbolt holds `blocks`, `hashes`, `utxos`, `undo` (height → delta), `txs`, `addrtx` and `meta.indexedTip`, all written in one transaction by `Commit`/`Reorg`. Nothing is ever deleted: `blocks` and `undo` grow with the chain.
- A block header is already self-verifying without its body: `Block.CalculateHash` covers `Index`, `Timestamp`, `MerkleRoot`, `PrevHash`, `Nonce`, `Difficulty`, `Validator`; `pow.Verify` (retarget included) and `poa.Verify` read only header fields of `history` and of `b`; `domain.ChainWork` reads only `Difficulty`.
- Paths that need **bodies** of old blocks and would break under pruning:
  1. `IndexedRepository.Reindex` replays every block from genesis (`domain.BlockDelta`).
  2. `chain.Service.ReplaceChain` builds the fork state with `domain.BuildUTXOSet(local[:fork])`.
  3. `chain.Service.Balance` walks `TxsByAddress` → block bodies → outputs still in `utxos`: an output created in a pruned (or restored) block would vanish from the balance.
  4. Readers: `GET /`, `GET /blocks`, `GET /block/{ref}`, `GET /tx/{id}`, `GET /tx/{id}/proof`.
- There is no network sync yet (ENG-30 P2P in progress, ENG-31 gossip in backlog). "Boot from a snapshot" means a local file.
- Blocks do not commit to the UTXO set, so no consensus rule can check a snapshot. The hash proves the file is intact and equals what a trusted full node reported for the same block hash (assumeutxo model).

## Options
A. **Snapshot file only, no pruning.** Export/import a file with headers + UTXO set + hash. Cost: low (1–2 stories). Does not stop growth, and the imported node still needs half of B's work to cope with missing bodies.
B. **Periodic snapshot in the database + pruning of bodies below it + boot from an exported snapshot.** Cost: high (4 stories, 3 core). Bounds disk to headers + UTXO set + the last K..K+N−1 bodies.
C. **UTXO commitment in the block header** plus B's pruning. Cost: very high (hashed `Block` shape, consensus, every fixture and `chain.db`). Only gain: trustless snapshots, worthless until untrusted peers exist.

## Decision
Option B (chosen by the board), with these rules:

**Configuration.** `storage.Pruning{SnapshotInterval, KeepBlocks int}` (N and K), given to `memory.New` and `bolt.Open` as an option; the zero value keeps today's behaviour (no snapshot, no pruning). `KeepBlocks > 0` requires `SnapshotInterval > 0`. Opening a pruned database (`prunedBelow > 0`) with `KeepBlocks == 0` is an error: pruning is irreversible. `cmd/node` reads `SNAPSHOT_INTERVAL` (default 1000, 0 = off) and `PRUNE_KEEP_BLOCKS` (default 0 = full node; when > 0 it must be ≥ 100).

**Snapshot (lagging by K).** In the `Commit` of height t, when N > 0, t − K ≥ 1 and (t − K) mod N = 0, the adapter stores the UTXO set **as of height S = t − K** in the `snapshot` bucket (replacing the previous one), computed from the current set by reverting the stored `undo` of heights t..S+1, plus `meta.snapshotHeight = S` and `meta.snapshotHash`. Lagging by K means every block above S still has its body and undo, so `Reindex` from the snapshot is exact and a reorg never drops the snapshot block when pruning is on. With K = 0 (snapshots without pruning) a `Reorg` whose fork is ≤ S discards the stored snapshot (it described a dropped branch); the next multiple of N stores a new one.

**Snapshot hash.** `storage.SnapshotHash(blockHash string, set domain.UTXOSet) string` = hex SHA-256 over: the 64-char block hash of S as ASCII, then each entry sorted by (`TxID`, `Index`): `TxID` (64 ASCII), `Index` (4 B big-endian), `len(Address)` (2 B BE), `Address`, `Amount` (8 B BE). One function, used by the adapters, the export and the import.

**Pruning.** With K > 0, the same `Commit` that stores a snapshot at S sets `meta.prunedBelow = S + 1` and, for every height in [old `prunedBelow`, S], replaces the stored block with its header (same key, same hash, `Transactions` empty) and deletes its `undo`, `txs` and `addrtx` entries. `hashes` and headers are never pruned. `ByHeight`, `ByHash`, `Range` and `All` return headers (`Transactions` = empty, non-nil) below `prunedBelow`.

**Address → unspent outputs.** New bucket `addrutxo` (key `address|txid|index 4B BE` → amount 8B BE), kept in step with `utxos` by `Commit`/`Reorg`/`Reindex`/`Restore`, read by `UTXOsByAddress(addr)`. `Balance` switches to it, so it no longer depends on bodies or on `addrtx`.

**Reorg and fork state.** `ForkState(height)` returns the UTXO set as before block `height` (current set reverted by the stored `undo` of tip..height). `Reorg`/`ForkState` with `height < prunedBelow` return the new `storage.ErrReorgTooDeep` and change nothing. `ReplaceChain` uses `ForkState(fork)` instead of `BuildUTXOSet`.

**Reindex.** When `prunedBelow > 0` it starts from the stored snapshot (S, its UTXO set) and replays the bodies S+1..tip; otherwise from genesis as today.

**Boot.** `Restore(headers []*domain.Block, set domain.UTXOSet) error` on an empty repository writes the headers 0..S (index, `PrevHash` link and `Hash == CalculateHash` checked), `utxos`, `addrutxo`, the snapshot of S, `prunedBelow = S + 1` and `indexedTip = S`. `go run ./cmd/node -snapshot-export FILE` writes the headers and the stored snapshot as JSON; `-snapshot-import FILE -snapshot-hash HEX` checks the hash with `SnapshotHash`, runs `consensus.Verify` over every header, then `Restore`s into an empty `DATA_DIR`.

**Readers.** `chain.Service.BlockByHeight`/`BlockByHash` return the new `storage.ErrPruned` below `prunedBelow` → HTTP `410 Gone`; a transaction of a pruned block is `404` (its `txs` entry is gone); `GET /` and `GET /blocks` list headers with `"Transactions": []` for pruned heights.

## Consequences
- Supersedes in part ADR-0011: `Reindex` rebuilds from the snapshot when pruned; `undo`, `txs`, `addrtx` exist only above `prunedBelow`, so `TxHeight`/`TxsByAddress` cover only retained history (`GET /tx/{id}` of a pruned tx is 404).
- A pruned node follows reorgs only down to `prunedBelow` (at least K blocks deep) and cannot serve bodies below it (future P2P must refuse those requests).
- On-disk format gains `snapshot`, `addrutxo` and the `meta` keys `snapshotHeight`, `snapshotHash`, `prunedBelow`. An old `chain.db` lacks `addrutxo` → `IndexCurrent()` is false → `Reindex` at start builds it (no manual migration).
- Every N blocks a `Commit` copies the UTXO set and reverts K deltas: O(|UTXO| + K) on that block only.
- Invariants to prove in tests:
  - For any chain and any N, K: `snapshotHash` equals `SnapshotHash(chain[S].Hash, BuildUTXOSet(chain[:S+1]))`.
  - For any chain, a node restored from the snapshot at S and then given blocks S+1..tip has the same tip hash, the same `utxos` and `addrutxo` contents and the same `Balance` for every address as a full node given the whole chain.
  - For any pruned repository, `Reindex` reproduces byte-identical `utxos`, `addrutxo`, `undo`, `txs`, `addrtx`, `snapshot` and `meta`.
  - For any reorg with fork ≥ `prunedBelow`, a pruned node and a full node end with the same tip, `utxos` and `addrutxo`; a fork below it is `ErrReorgTooDeep` and changes nothing.
- Measured in story 1: `chain.db` size and the `Commit` time of a snapshot block for 20k blocks, full vs `K=100, N=1000` (recorded under "What we learned").

## Delivery (each a separate story)
1. Storage (core): `storage.Pruning`, snapshot in `Commit`, `SnapshotHash`, pruning, `addrutxo` + `UTXOsByAddress`, `ForkState`, `ErrReorgTooDeep`, `PrunedBelow`, `Snapshot`, `Reindex` from snapshot, `storagetest` contract, memory + bolt, this ADR file.
2. Chain (core): `ReplaceChain` via `ForkState`, `Balance` via `UTXOsByAddress`, `ErrPruned` from `BlockByHeight`/`BlockByHash`.
3. Boot (core): `Restore`, snapshot file encoding, `cmd/node` flags and env, end-to-end test of the acceptance criterion.
4. HTTP + docs (normal): `410 Gone`, `openapi.yaml` + client, README (trust model, env, flags).

## Out of scope
- Fetching snapshots or headers from peers (ENG-30/ENG-31).
- Pruning `hashes` or headers; more than one stored snapshot; compression.
