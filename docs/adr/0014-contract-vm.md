# ADR-0014: Stack-machine contract VM — contract operations ride on UTXO transactions, contract state is derived in memory

Status: Proposed (ENG-42) — option A

## Context
- The chain only moves value (ADR-0001, UTXO). ENG-42 asks for a minimal stack VM: opcodes, a gas (step) limit, per-contract state, deploy and call transactions, deterministic and isolated execution. Acceptance: a counter contract runs deterministically and respects the gas limit.
- Authentication and replay protection already exist structurally: every input is signed over `CalculateHash` and an outpoint is spent once (ADR-0001). A contract operation that is part of the hashed payload of a normal transaction inherits both, and pays its fee as `sum(inputs) − sum(outputs)` like any other transaction.
- `internal/domain` imports nothing internal; block and transaction validation, the chain write path, mempool and storage are core areas (AGENTS.md).
- Storage persists the UTXO set and indexes atomically with each block (ADR-0011); `ReplaceChain` already rebuilds the UTXO set at the fork by replaying `local[:fork]`.

## Options
A. **Contract op on the transaction, state derived in memory.** `Transaction` gains an optional `Contract *ContractOp` (hashed only when non-nil, so every existing ID is unchanged). Contract state (code + storage) is not persisted: `chain.Service` rebuilds it from `repo.All()` at start and replays to the fork on reorg. Cost: medium; no storage or on-disk change; start-up and reorg cost O(chain) replay (already the case for the UTXO set in `ReplaceChain`).
B. **Contract op on the transaction, state persisted in `IndexedRepository`.** Same schema as A; `Commit`/`Reorg`/`Reindex` also write a contract bucket and its undo. Cost: high; touches the storage contract and both adapters (core) for a learning extra, with no measured need.
C. **Separate `Block.Calls` list.** Contract calls outside transactions. Cost: high; changes the block hash and Merkle root, and calls need their own signature, fee and replay scheme. Rejected.

## Decision
Option A.

VM (`internal/vm`, pure, no I/O, no clock, no randomness; imports only `internal/domain`):
- Word: `uint64`; `ADD`, `SUB`, `MUL` wrap modulo 2^64. Stack depth at most `MaxStack = 64` (`ErrStackOverflow`); popping an empty stack is `ErrStackUnderflow`.
- Code: bytes, `1 ≤ len ≤ domain.MaxContractCodeBytes = 1024`. `ValidateCode` rejects an unknown opcode or a truncated `PUSH8` immediate (`ErrInvalidCode`) at deploy.
- Opcodes (byte: name — effect — gas):
  - `0x00 STOP` — halt, success — 0
  - `0x01 PUSH8` — push the next 8 bytes, big-endian — 1
  - `0x02 POP` — 1; `0x03 DUP` — duplicate top — 1; `0x04 SWAP` — swap the top two — 1
  - `0x10 ADD`, `0x11 SUB` (`a − b` where `b` is the top), `0x12 MUL`, `0x13 LT` (`a < b` → 1 else 0, `b` is the top), `0x14 EQ`, `0x15 ISZERO` — 1 each
  - `0x20 JUMP` (pop destination), `0x21 JUMPI` (pop destination, then condition; jump when condition ≠ 0) — 1 each. A destination that is not the offset of an opcode (out of range or inside a `PUSH8` immediate) is `ErrBadJump`.
  - `0x30 SLOAD` (pop key, push `storage[key]`, 0 when absent) — 5; `0x31 SSTORE` (pop key, then value) — 20
  - `0x40 ARG` (pop `i`, push `args[i]`; `i ≥ len(args)` is `ErrArgIndex`) — 1
  - `0xFF REVERT` — halt, failure (`ErrReverted`) — 0
  - Running past the last byte is `STOP`.
- Gas: each instruction's cost is charged before it runs; when `used + cost > gasLimit` execution stops with `ErrOutOfGas`. `1 ≤ gasLimit ≤ domain.MaxContractGas = 100000`.
- `Execute(code []byte, args []uint64, store Storage, gasLimit uint64) (Result, error)`; `Storage` is read-only (`Load(key uint64) uint64`); `Result{Writes map[uint64]uint64; GasUsed uint64}`. Writes are buffered: `Execute` never mutates `store`, and any error returns no writes (all-or-nothing).
- Counter example (`vm.CounterCode`): `PUSH8 0, SLOAD, PUSH8 1, ADD, PUSH8 0, SSTORE, STOP` — increments `storage[0]`, gas 29.

Transaction (`internal/domain`, schema change):
- `Transaction.Contract *ContractOp` with JSON tag `json:",omitempty"`; `ContractOp{Kind string; Code string; Target string; Args []uint64; GasLimit uint64}`. `Kind` is `"deploy"` (`Code` = canonical lower-case hex, `Target` empty) or `"call"` (`Target` = contract ID, `Code` empty).
- Hash payload gains `"contract"` with `omitempty`: `{kind, code, target, args, gas_limit}`. A transaction with `Contract == nil` hashes exactly as before.
- A contract transaction obeys every UTXO rule (≥ 1 input, ≥ 1 output, signatures); `ValidateContractOp` checks the structural rules above (`ErrInvalidContractOp`).
- Contract ID = the deploying transaction's ID. Contracts hold no coins; value transfer to or from contracts is out of scope.

State and chain:
- `vm.State` (contract ID → code and storage) and `vm.BlockDelta(view, block)`: transactions in block order; a deploy of an existing ID is `ErrContractExists`, a call to an unknown ID is `ErrUnknownContract`; a call sees the writes of earlier transactions of the same block. Any failing operation invalidates the block (no partially applied contract state, no "failed but included" transaction). `vm.BuildState(blocks)` replays a chain.
- `chain.Service` keeps the state in memory behind its own lock: built at `NewService`, delta computed with the UTXO delta before sealing, applied only after `Commit` succeeds; `ReplaceChain` replays `local[:fork]`, verifies the candidate's contract deltas with the other rules, and swaps the state only after `Reorg` succeeds. `Submit` dry-runs against the confirmed state; `MinePending` evaluates pool candidates cumulatively in selection order and drops the ones that fail.

Delivery (each story reviewed separately):
1. `internal/vm` interpreter + counter + this ADR + AGENTS.md (core area).
2. `domain.ContractOp`, hash payload, `ValidateContractOp`; `vm.State`, `vm.BlockDelta`, `vm.BuildState`.
3. `chain.Service` wiring (Mine, Submit, MinePending, ReplaceChain, `Contract` query).
4. HTTP: `POST /tx` accepts `contract`, `GET /contracts/{id}`, OpenAPI + client, README walkthrough.

## Consequences
- New consensus rules: a block is invalid when any contract operation in it fails. Execution cost is bounded by `MaxContractGas` per transaction and by `MAX_BLOCK_BYTES` per block.
- No gas price: the fee is the UTXO fee, unrelated to gas used. A heavy call pays the same as a light one; acceptable for a learning project, revisit with a gas-price ADR if needed.
- Contract state is not on disk: `-reindex` does not touch it, and node start-up replays the whole chain. If replay time becomes a problem, option B is the follow-up.
- `Block.Size()` is unchanged for blocks without contract operations (`omitempty`), so `MAX_BLOCK_BYTES` and `sealedSize` keep their meaning.

