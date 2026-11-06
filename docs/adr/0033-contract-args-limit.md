# ADR-0033: Fixed limit on contract call arguments

Status: Accepted (ENG-272, board decision: option B; implemented by ENG-367)

## Context
- ADR-0014 bounds contract code (`MaxContractCodeBytes = 1024`), gas (`MaxContractGas = 100000`) and stack (`MaxStack`) but not `ContractOp.Args` (ENG-261 finding F1, pinned by a test in ENG-266).
- Today only the block size limit (`MaxBlockBytes`, 1 MiB, ADR-0032) bounds the number of args: a single transaction may carry hundreds of thousands of them. A contract reads args with `PUSH8 i; ARG`, so with `MaxContractGas` it can read only a small fraction of them; the rest is dead payload that every node stores, hashes and replays.
- `ValidateContractOp` runs in `vm.BlockDelta`, so a rule there applies both to new blocks (mining, `POST /tx`, blocks from peers) and to the start-up replay of the stored chain (`vm.BuildState`).

## Options
1. **Document only.** Keep no limit and amend ADR-0014. Cost: docs only. The VM inputs stay the only unbounded one, bounded only indirectly by the block size.
2. **Fixed constant `MaxContractArgs = 256` (chosen).** Cost: one constant, one check and one boundary test. It is a consensus rule (core).
3. **Intrinsic gas per argument.** Cost: changes gas semantics, `GasUsed` values, the counter example and fixtures. It is a pricing decision, deferred to a gas-price ADR (ADR-0014).

## Decision
- `domain.MaxContractArgs = 256`. `ValidateContractOp` rejects `len(Args) > MaxContractArgs` with `ErrInvalidContractOp`, for deploy and call alike. Nil or zero args stay valid.
- 256 × 8 bytes = 2 KiB, the same order as the code limit, and far above what the counter or any sample needs.
- No gas is charged per argument.
- `openapi.yaml` declares `maxItems: 256` on `contract.args`, and the README contract table states the limit.

## Consequences
- New consensus rule: a block with a contract operation carrying more than 256 args is invalid. Every node must run the same version; changing the value later is a hard fork, as for every other consensus constant (ADR-0032).
- A node whose stored chain holds such a block fails at start-up replay; reset `DATA_DIR`. No shipped fixture or compose network carries one.
- All VM input limits (code, gas, stack, args) are now fixed constants written in an ADR.
