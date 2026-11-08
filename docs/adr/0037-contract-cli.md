# ADR-0037: Contract transactions from the CLI (`bwg contract deploy|call|show`)

Status: Accepted (board approval of the story that adds it)

## Context
- ADR-0014 made contract operations part of a normal UTXO transaction and `POST /tx` accepts them, but `bwg wallet send` only builds transfers. The only way to deploy or call a contract was Go test code, so the playground could not show contracts by hand.
- `wallet send` already owns the keystore, the UTXO selection, signing and the error report of the node (`nodeError`).

## Options
- **A. `bwg contract deploy|call|show` (chosen).** A new command group reuses the keystore, UTXO selection and signing of `wallet send`; the contract transaction pays its change back to the sender. Cost: one file in `cmd/bwg`, one builder next to `buildTransfer`.
- **B. Contract flags on `wallet send`.** Cost: lower, but one command with two mutually exclusive flag sets (`--to/--amount` vs `--code/--target`), and no place for `show`.
- **C. `bwg tx submit --file tx.json` (raw transaction).** Cost: the user hand-writes the JSON, the hash and the signatures; it teaches the wire format but makes the walkthrough unusable.

## Decision
Option A.
- `deploy` takes the code as hex (`--code`) or a file of hex text (`--code-file`), `--gas-limit` and optional `--args`, `--fee`; it prints the contract ID, which is the transaction ID (ADR-0014).
- `call` takes `--target`, `--gas-limit`, optional `--args`, `--fee`; it prints the transaction ID.
- `show ID` prints `GET /contracts/{id}` unchanged.
- Inputs are taken in `GET /balance` order until they cover `fee + 1`; the only output returns `sum − fee` to the sender.
- The CLI parses its flags but does not pre-check contract rules (code size, gas range, args count, ADR-0033): the node decides and the CLI reports the node's message.
- Example contracts live in `examples/contracts/` as hex files with a README; a test runs each through `vm.ValidateCode` and `vm.Execute`.

## Consequences
- Contracts are reproducible by hand in the playground and covered end to end by `e2e/contract_cli_test.go`.
- One confirmed output allows one pending spend at a time (as for `wallet send`): a deploy and its first call need a block between them, which the call also needs because `Submit` dry-runs against the confirmed contract state.
- `contract_exists` stays unreachable from the CLI: a resubmitted deploy fails first on its spent inputs.
- ADR-0014 becomes Accepted: its four delivery steps are implemented and now usable by hand.
