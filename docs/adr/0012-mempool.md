# ADR-0012: In-memory mempool with fee-rate selection

Status: Accepted

## Context

`POST /` validates the posted transactions and mines them in the same request. There is no queue between submission and mining, so a node cannot collect transactions from several clients, rank them by fee or fill a block up to `MAX_BLOCK_BYTES`. The fee already exists implicitly (`fee = sum(inputs) - sum(outputs)`, ADR-0001) but nothing uses it.

## Options

1. **Mempool inside `chain.Service`, explicit mining endpoint (chosen).** New package `internal/mempool` (depends only on `domain`) holds admitted transactions. `chain.Service.Submit` validates a transaction against the confirmed UTXO set and admits it; `chain.Service.MinePending` builds the block from the pool. HTTP gets `POST /tx` (admit, 202) and `POST /mine` (mine from the pool, 201). `POST /` keeps its current behaviour. Cost: 3 stories, new routes in `openapi.yaml`, no background goroutine.
2. **Background miner loop.** A goroutine in `cmd/node` mines from the pool every `TARGET_BLOCK_TIME`. Cost: lifecycle and shutdown handling, non-deterministic tests, and PoA/PoW timing interactions; harder to test the selection order end to end.
3. **`POST /` admits then mines from the pool.** One endpoint, but the caller's own transactions may be left out of the block it gets back, which silently changes the meaning of an existing endpoint.

## Decision

Option 1.

- **Admission** (`Submit`): the transaction must pass the same checks `Mine` applies to a one-transaction block (`BlockDelta` against the confirmed UTXO set and every input signature). Inputs must be confirmed outputs: spending an output created by another pool transaction is rejected (no chained unconfirmed transactions).
- **Deduplication:** a transaction whose `ID` is already in the pool is rejected with `mempool.ErrDuplicate`.
- **Conflicts in the pool:** a transaction that spends an outpoint already spent by a pool transaction is rejected with `mempool.ErrConflict` (first seen wins, no replace-by-fee). So pool transactions never conflict with each other and any subset of them is a valid block body.
- **Fee rate:** `fee / size`, where `size` is the length of `json.Marshal(tx)` (the same encoding `Block.Size` measures). Compared by cross-multiplication in 128 bits (`math/bits.Mul64`), never by division or floats.
- **Order:** fee rate descending, then `ID` ascending. It is a total order, so selection is deterministic for any pool contents.
- **Limit and eviction:** the pool holds at most `mempool.DefaultMaxTxs` (5000) transactions, configurable through the constructor. When full, a new transaction is admitted only if it ranks strictly above the lowest-ranked entry, which is evicted; otherwise it is rejected with `mempool.ErrFull`.
- **Selection** (`MinePending`): walk the pool in order and take every transaction that still fits the byte budget (skip the ones that do not, keep going). The budget keeps the sealed block within `domain.MaxBlockBytes()`. Before generating the block, each selected transaction is checked again against the current UTXO set under the write lock; stale ones are dropped from the pool, never mined.
- **After a block** (either `Mine` or `MinePending`): the pool drops the included transactions and every transaction with an input that is no longer in the UTXO set.

## Consequences

- The pool lives in memory only: a restart empties it. Persistence is out of scope.
- Fees are ranked but not paid to anyone: there is no coinbase transaction yet.
- No replace-by-fee, no chained unconfirmed transactions, no minimum relay fee. Each is a possible follow-up.
- When chain reorganisation (ENG-26) lands, transactions of reverted blocks are not returned to the pool; that is a follow-up for ENG-26.
- `internal/mempool` joins the core areas (concurrency path of the chain write path).
