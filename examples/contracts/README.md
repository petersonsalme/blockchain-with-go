# Example contracts

Four contracts for the stack-machine VM ([ADR-0014](../../docs/adr/0014-contract-vm.md)), one hex line each. Deploy one with `bwg contract deploy --code-file examples/contracts/<name>.hex --gas-limit 100` ([ADR-0037](../../docs/adr/0037-contract-cli.md)); `contracts_test.go` runs every case below through `vm.ValidateCode` and `vm.Execute`. Gas figures are for the success path.

## counter.hex

`storage[0] += 1`. Opcodes: `PUSH8 0, SLOAD, PUSH8 1, ADD, PUSH8 0, SSTORE, STOP` (equal to `vm.CounterCode`). Args: none. Gas: 29. Fails with out of gas when `--gas-limit` is below 29 (for example 28).

## adder.hex

`storage[0] += args[0]`. Opcodes: `PUSH8 0, SLOAD, PUSH8 0, ARG, ADD, PUSH8 0, SSTORE, STOP`. Args: one value. Gas: 30. Fails with `argument index out of range` when called without args.

## guard.hex

Reverts when `args[0] == 0`, otherwise `storage[1] = args[0]`. Opcodes: `PUSH8 0, ARG, ISZERO, PUSH8 42, JUMPI, PUSH8 0, ARG, PUSH8 1, SSTORE, STOP, REVERT` (the jump lands on `REVERT`). Args: one value. Gas: 28 (with `--args 42`). Fails with `reverted` for `--args 0`.

## capped.hex

A counter that stops at 3. Opcodes: `PUSH8 0, SLOAD, DUP, PUSH8 3, LT, ISZERO, PUSH8 53, JUMPI, PUSH8 1, ADD, PUSH8 0, SSTORE, STOP, REVERT`. Args: none. Gas: 35 (from `{0: 2}` to `{0: 3}`). Fails with `reverted` once `storage[0]` is 3.
