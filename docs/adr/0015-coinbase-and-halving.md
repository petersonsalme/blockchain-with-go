# ADR-0015: Coinbase reward and halving

Status: Proposed (Spec-Opus, 2026-10-24, ENG-24)

## Context
- Coins only exist through the genesis allocations (ADR-0002); `domain.BlockDelta` rejects every input-less transaction outside the genesis with `ErrMissingInputs` (ADR-0001: "ENG-24 later adds the coinbase rule").
- `POST /mine` hands client transactions straight to `chain.Service.Mine`, and `Submit` validates a transaction as a one-transaction pseudo-block (`Index` 0), so any rule keyed on "input-less transaction at position 0" must not be reachable from client input.
- Chains already on disk (`chain.db`) have non-genesis blocks without any coinbase; `NewService`/`Reindex` replay them through `BlockDelta`.
- Amounts are `uint64` base units with no stored fee: fee = sum(inputs) − sum(outputs) (ADR-0001).

## Options
A. Optional coinbase, no inputs, only at position 0 of a non-genesis block, capped at `Subsidy(Index) + fees`; schedule as constants in `internal/domain`; the node creates it only when `MINER_ADDRESS` is set. Cost: medium. Existing chains stay valid; no schema change.
B. Mandatory coinbase in every non-genesis block. Cost: high. Invalidates every stored chain and most chain/server tests, and the node must refuse to start without a miner address (PoA and test setups included).
C. Bitcoin-style coinbase with a null-outpoint input carrying the height (BIP-34) for a unique txid. Cost: high. Special-cases the input in signature checks, the mempool and the hashed payload; contradicts "sem entradas" in ENG-24.

## Decision
Option A.

- `domain.InitialSubsidy uint64 = 5_000_000_000` (50 coins of 10^8 base units) and `domain.HalvingInterval = 210_000` blocks are consensus constants, not configuration: nodes that disagree on them would fork.
- `domain.Subsidy(height int) uint64` = `InitialSubsidy >> (height / HalvingInterval)`; 0 for a negative height and once the shift reaches 64.
- In a non-genesis block, the transaction at position 0 without inputs is the coinbase. The sum of its outputs must be `<= Subsidy(b.Index) + fees`, where fees is the sum of (inputs − outputs) of the other transactions; otherwise `ErrCoinbaseTooLarge` (code `coinbase_too_large`). Claiming less is allowed (the difference is burned). An input-less transaction at any other position keeps failing with `ErrMissingInputs`. The genesis rule is unchanged.
- Client entry points never produce a coinbase: `Submit` and `Mine(ctx, txs)` reject an input-less transaction with `ErrMissingInputs`; only the Service itself prepends one. `mineLocked` validates the block with its real `Index` (`prev.Index+1`).
- The node pays the coinbase to `MINER_ADDRESS` (validated with `wallet.ValidateAddress`, read in `cmd/node`); when unset, blocks carry no coinbase, as today.

## Consequences
- Issuance becomes: genesis allocations + at most `Subsidy(h)` per block h; for any accepted non-genesis block, sum(created) − sum(spent) of its delta is `<= Subsidy(Index)`.
- Total supply converges to about `2 × InitialSubsidy × HalvingInterval`.
- Coinbase txid uniqueness rests on its timestamp (set by `time.Now()` under `writeSem`) and on the existing rule that rejects an output colliding with an unspent one. Two coinbases with the same timestamp and outputs, the first already spent, would share a txid in the transaction index; accepted for this educational chain, BIP-34 height commitment is a possible follow-up.
- No coinbase maturity: a coinbase output is spendable in the next block, and a reorg that drops it can invalidate its spenders (they are reported in `Reorg.Orphaned`, the coinbase included).
- `coinbase_too_large` is a new error code in the server mapping and README.
