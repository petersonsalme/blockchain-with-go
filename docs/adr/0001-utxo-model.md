# ADR-0001: Transaction model — UTXO replaces account/nonce

Status: Accepted (board, 2026-10-19, ENG-23)

## Context
- `domain.Transaction` is account-based: `From/To/Amount/Fee/Nonce`, signed over `txHashPayload` (ENG-71); `chain.Service` keeps `nonces map[string]uint64` for replay protection (ENG-72).
- There is no notion of balance and no coin issuance; ENG-24 (coinbase) depends on ENG-23, so after ENG-23 alone nothing would be spendable.
- Roadmap items ENG-25 (mempool: "conflicting with spent UTXO"), ENG-26 (reorg with state reversal) and ENG-28 (UTXO index) assume a UTXO set.
- Layering rule (AGENTS.md): pure rules in `internal/domain`, orchestration in `internal/chain`, persistence behind a port in `internal/storage`.

## Options
A. Full UTXO (Bitcoin-like). Cost: high. Breaks the ENG-71 canonical payload and the ENG-72 nonce guarantee (both replaced, not weakened), rewrites the server DTOs and most transaction tests. Needs an issuance source in the same milestone.
B. Account balances (keep `Transaction`, add `balances map[address]uint64`). Cost: low. Contradicts the issue title and ENG-25/26/28; reorg undo needs per-block balance diffs anyway.
C. Hybrid (account tx + derived UTXO set). Rejected: account transactions do not reference outputs, so the "set" would be balances under another name.

## Decision
Option A.

Types (`internal/domain`):
- `OutPoint{TxID string; Index uint32}`, `TxIn{PrevTxID string; OutIndex uint32; PublicKey string; Signature string}`, `TxOut{Amount uint64; Address string}`.
- `Transaction{ID, Inputs []TxIn, Outputs []TxOut, Timestamp}`; `ID` = SHA-256 of the canonical JSON of inputs **without** `PublicKey`/`Signature`, outputs and UTC RFC3339Nano timestamp (SIGHASH_ALL). Every input signs the decoded ID bytes with Ed25519. Fee = sum(inputs) − sum(outputs), never stored.
- Input authorisation: `AddressFromPublicKey(input.PublicKey)` must equal the spent output's `Address`; the ENG-80 weak-key rejection and the canonical-hex checks of ENG-71 stay.
- Pure state: `UTXOView` (read-only lookup), `UTXODelta{Spent, Created map[OutPoint]TxOut}` computed by `BlockDelta(view, block, isGenesis)` without mutating anything; `UTXOSet` (map) with `Apply`/`Revert` of a delta. The delta is also the undo record for reorg (ENG-26).
- Persistence: port `storage.UTXOSet` + memory adapter; `chain.Service` computes the delta, appends the block, then applies the delta; `NewService` rebuilds the set from `repo.All()`.

Issuance until ENG-24: only the genesis block (Index 0) may carry input-less transactions (allocations configured in `cmd/node`); an input-less transaction in any other block is rejected. ENG-24 later adds the coinbase rule.

Delivery (each story reviewed separately):
1. Domain types, canonical hash, per-input signatures, pure UTXO rules — additive, the old account fields remain until story 2.
2. Schema switch: remove `From/To/Amount/Fee/Nonce` and the nonce map; storage port + adapter; chain validation/apply/rebuild; server error mapping.
3. Genesis allocation config in `cmd/node` and README.

## Consequences
- ENG-71 canonicalisation and ENG-72 nonce replay protection are superseded; replay protection becomes structural: an outpoint is spent at most once on the chain.
- `POST /` request format changes (breaking); README must be updated.
- The UTXO set is derived state: `rebuild(All()) == incremental(apply…)` is the invariant ENG-26 (reorg) relies on.
- `Fee` is no longer a field; ENG-25 computes it from inputs/outputs.
- Error codes `nonce_reused`/`nonce_gap` disappear; new codes: `input_missing_or_spent`, `double_spend`, `insufficient_inputs`, `input_owner_mismatch`, `invalid_output`, `missing_inputs`.

