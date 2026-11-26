# blockchain-with-go

An educational blockchain node: blocks of UTXO transactions and contract operations, agreed on by proof of work or proof of authority, and exchanged between peers over HTTP.

## Blocks and the chain

**Block**:
A sealed batch of **Transactions** that extends the chain by one height.
_Avoid_: page, entry

**Transaction**:
A signed transfer that spends outputs of the **UTXO set** and creates new ones, optionally carrying a contract operation.
_Avoid_: tx (in prose), payment

**Mempool**:
The **Transactions** a node has admitted but no **Block** has confirmed yet.
_Avoid_: pending pool, queue

**Genesis**:
The **Block** at height 0. It is never sealed, so it carries no stored hash.
_Avoid_: block zero, root block

**Block identity**:
The hash that names a **Block**, the **Genesis** included. It is computed from the header alone.
_Avoid_: block id (for the stored hash field), genesis hash (as a special case)

**Tip**:
The last **Block** of the chain a node currently follows.
_Avoid_: head, top

**Connect**:
Check a **Block** against the chain below it and produce its effect on the **UTXO set** and the **Contract state**, without writing it. A **Block** that connects is valid on that chain.
_Avoid_: validate (too narrow), append, apply

**Chain work**:
The total work behind a chain, summed from the difficulty of each of its **Blocks**. The node follows the chain with the most of it.
_Avoid_: length, height (as the fork-choice measure)

**Median time past**:
The median timestamp of the recent **Blocks** before a given height. A new **Block** must be later than it.

## State

**UTXO set**:
The unspent transaction outputs at the **Tip**: who owns which coins.
_Avoid_: balances, ledger state

**Contract state**:
The deployed contracts and their stored values at the **Tip**. Together with the **UTXO set** it forms the chain state.
_Avoid_: VM state, storage (for the contract values)

**Supply**:
The total of all subsidies paid up to a given height.
_Avoid_: issuance, minted, circulating

## Reorg

**Reorg**:
Replacing the **Blocks** above a **Fork point** with a competing branch that has more **Chain work**.
_Avoid_: rollback, chain swap

**Fork point**:
The highest height two chains share.
_Avoid_: common ancestor (when a height is meant)

**Orphaned transaction**:
A **Transaction** confirmed only in the **Blocks** a **Reorg** disconnected. It goes back to the **Mempool**.
_Avoid_: dropped transaction

**Chain change**:
One ordered fact about the chain a node follows: a **Block** connected, a **Block** disconnected, or a **Transaction** admitted to the **Mempool**.
_Avoid_: event (for the fact itself; an event is one way of announcing it)

## Peers

**Ledger**:
What the peer-to-peer side of a node sees of its own chain: the **Tip**, which **Blocks** and **Transactions** it holds, and how it accepts new ones.
_Avoid_: chain view, sync ledger

## Storage

**Snapshot**:
The **UTXO set** at one height, with a hash that commits to it, used to start a node without replaying every **Block**. It does not carry the **Contract state**.
_Avoid_: checkpoint, dump

**Pruning**:
Dropping the bodies of old **Blocks** and keeping their headers, once a **Snapshot** covers them.
_Avoid_: compaction, trimming
