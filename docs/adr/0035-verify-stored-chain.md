# ADR-0035: `bwg node --verify` replays the stored chain under the full block rules

Status: Accepted (ENG-352; closes the startup-replay gap noted out of scope in ADR-0009 and ADR-0029)

## Context
- `NewService` trusts `chain.db`: it reindexes only when `IndexCurrent()` is false and rebuilds the contract state; nothing re-runs `consensus.Verify`, `domain.ValidateBlockRules`, `domain.BlockDelta` or the input signatures over stored blocks. A block edited on disk is served and built upon.
- ADR-0011 measured an O(1) startup; a check that walks the chain at every boot would undo it.

## Options
- **A. Always verify in `NewService`.** Cost: O(chain) on every boot, undoes the O(1) startup measured in ADR-0011; every test that opens a service pays it.
- **B. Opt-in `--verify` flag on `bwg node`: read-only replay after `NewService`, before any port opens; a failure stops the node (chosen).** Cost: one method in `chain`, one flag in `cmd/bwg`; the default boot is unchanged.
- **C. `--verify` as a one-shot command that verifies and exits (like `--reindex`).** Cost: same as B, but the operator runs two commands and nothing stops the node from starting on a bad file afterwards.

B closes the gap without paying the replay on every boot, and "refuse to start" is a property of the start itself, which C does not give.

## Decision
Option B. `chain.(*Service).Verify(ctx)` replays the stored chain on scratch state (a `domain.UTXOSet` and a `vm.State` built from scratch) and writes nothing. It takes the write lock, so it never runs with `Mine`, and honours a cancelled context.

Replay rules, in this order per block:
- Height 0: `Index == 0`, `PrevHash == ""`, `Hash` empty or equal to `CalculateHash()` (as `storage.CheckHeaders` does); with a body also `domain.ValidateMerkleRoot` and `domain.BlockDelta(scratch, genesis, true)`.
- Every height h ≥ 1: `consensus.Verify(history[:h], b)`; with a body `domain.ValidateBlockRules` (size, Merkle root, timestamp with `now = time.Now()` taken once), `domain.BlockDelta(scratch, b, false)`, input signatures, `vm.BlockDelta`, then both deltas are applied to the scratch state: the sequence `ReplaceChain` runs per candidate block. A header only gets `domain.ValidateTimestamp`.
- Pruned repository (`PrunedBelow() > 0`): heights below it are headers and get the header rules only; the scratch UTXO set starts from `Snapshot().UTXOs`, whose `Height` must be `PrunedBelow()-1` and whose `BlockHash` the stored hash at that height. The contract state starts empty, as `NewService` builds it over headers.
- The first failing block stops the replay. The error wraps `chain.ErrInvalidStoredChain` and the rule's own error (`errors.Is` is true for both) and names the height: `chain: stored block 3: ...`.

`bwg node --verify` calls it after the service is built and before the HTTP or P2P listener exists; on success it logs `verified stored chain` with the tip height; on failure the node stops with the error and a non-zero exit. The flag cannot be combined with `--reindex` or any `--snapshot-*` flag (usage error, nothing touched).

## Consequences
- `--verify` costs O(chain) at boot. Measured: 144 µs for a 3-block chain (genesis plus two blocks, bolt, PoW difficulty 1); 10k blocks were not measured.
- It checks stored blocks against the **current** rules, so a `chain.db` written before a rule change (for example ADR-0034's median time past) may fail verification and must be resynced.
- The persisted UTXO set, undo data and indexes are not compared against the replay (out of scope; `--reindex` rebuilds them).
- The default boot is unchanged: a tampered `chain.db` still starts without the flag.
