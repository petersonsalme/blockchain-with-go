# ADR-0010: Reorg by cumulative work with an atomic ReplaceFrom

Status: Accepted (Lead, ENG-26, 2026-10-21). Implemented in two stories: ENG-111 (storage `ReplaceFrom`) and the `chain.Service.ReplaceChain` story.

## Context
- ENG-26 asks `chain.Service` to accept a competing chain: validate it whole, choose by accumulated work, revert the old branch and apply the new one to the UTXO set, and hand the dropped transactions back to the mempool.
- `storage.BlockRepository` only grows (`Append`). Swapping the tail with `Append` calls cannot be atomic, so a crash or an error mid-swap would leave a mixed chain.
- No undo data is persisted. The delta of an old block needs the UTXO view before that block, which no longer exists in the live set.
- Difficulty is the count of leading hex zeros (`pow.solvesDifficulty`); PoW `Verify` pins `Difficulty` to `ExpectedDifficulty(history)` and PoA `Verify` pins it to 0.
- There is no mempool yet (ENG-25) and no peer transport. ADR-0009 already length-prefixes `PublicKey` and `Signature` in the Merkle leaf, which closes the `TransactionsHash` ambiguity raised in the ENG-83 review; the block path still has to revalidate input signatures.

## Options
Storage:
- S1. Add `ReplaceFrom(height int, blocks []*domain.Block) error` to `BlockRepository`: drop every block with `Index >= height` and append `blocks`, in one atomic step (one bbolt `Update`, one lock in memory). Cost: one port method, two adapters, a contract test, test fakes.
- S2. Optional interface `storage.Reorganizer` checked by type assertion. Cost: same code, plus a runtime path where a repository cannot reorg; nothing gains from it.
- S3. Truncate + Append loop in `chain`. Not atomic; rejected.

Fork choice:
- W1. Cumulative work, `work(b) = 16^b.Difficulty` (`math/big`), summed over the blocks after the fork point; switch only on strictly greater work. PoA blocks weigh 1, so PoA falls back to the longest chain.
- W2. Longest chain. Ignores difficulty; a long low-difficulty chain wins. Contradicts the issue.
- W3. `Consensus.Work(b)` port method. Moves the rule into each adapter; extra port change with no second formula today.

UTXO switch:
- U1. Replay the stored chain into a scratch `memory.UTXOSet` (old state), roll it back to the fork point and apply the validated candidate (new state), then apply the net difference to the live set with one `Apply`. O(chain length) per reorg, no persisted undo data, one atomic step on the live set.
- U2. Persist per-block undo deltas. Schema change; out of proportion for this project.

## Decision
S1 + W1 + U1.
- **Entry point:** `Service.ReplaceChain(ctx, candidate []*domain.Block) (*Reorg, error)`, serialized with `Mine` on the same `writeSem` and refused with `ErrStateDiverged` after a divergence. `candidate` is a full chain, genesis first.
- **Fork point:** the first height where candidate and local hashes differ. The genesis must be identical (same `Hash`, equal to `CalculateHash()`); fork point >= 1.
- **Work:** `domain.BlockWork(b) *big.Int = 16^Difficulty`; `domain.ChainWork(blocks) *big.Int` is the sum. Equal work keeps the local chain (first seen wins).
- **Validation of every candidate block from the fork point on, before any write:** non-nil; `consensus.Verify(candidate[:i], b)`; `domain.ValidateBlockRules(b, prev, now)`; `domain.BlockDelta` against the scratch set (no allocations outside the genesis); input signatures verified against the scratch set. The first failure returns its error and nothing changes.
- **Commit:** `repo.ReplaceFrom(fork, candidate[fork:])`, then `utxos.Apply(DiffUTXO(oldState, newState))`. A failure of that `Apply` sets `diverged`, as in `Mine`.
- **Mempool:** `Reorg.Orphaned` lists, in chain order, the transactions of the disconnected blocks whose ID is not in the connected blocks. ENG-25 will feed them to the mempool; this ADR does not revalidate them.

## Consequences
- `BlockRepository` gains a write path that removes blocks; `ByHash` of a dropped block returns `ErrNotFound` afterwards. Readers keep seeing either the old or the new chain from `All`, never a mix.
- Between `ReplaceFrom` and the UTXO `Apply`, a reader can see the new blocks with the old balances, the same window `Mine` already has.
- A reorg costs a replay of the local chain. Acceptable at this scale; revisit with persisted undo data if chains grow.
- `Difficulty` becomes part of the fork-choice rule, so every `Consensus.Verify` must keep pinning it. A new adapter that does not would let a peer inflate its work.
- The `NewService` replay still does not revalidate stored blocks and still panics on a nil stored block (ENG-83 finding 4 for the startup path): out of scope, tracked separately.
