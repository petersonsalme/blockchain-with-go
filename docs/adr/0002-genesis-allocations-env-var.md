# ADR-0002: Genesis funding comes from the GENESIS_ALLOCATIONS environment variable

Status: Accepted (PR #29, ENG-84)

## Context
- ADR-0001 allows input-less transactions only in the genesis block and leaves the allocations to be configured in `cmd/node`.
- Without an initial balance nothing is spendable, so transactions cannot be exercised on a fresh node.

## Decision
- `cmd/node` reads `GENESIS_ALLOCATIONS`, formatted as `address:value[,address:value...]`, and passes the parsed outputs to `chain.WithGenesisAllocations`.
- The variable is read only in the composition root, like `PORT` and `MINING_TIMEOUT`.

## Consequences
- An invalid item (missing `:`, bad address, zero, non-numeric, negative or overflowing value) makes bootstrap fail, and the error names the offending item.
- When the repository is already populated, the variable is ignored.
- The variable is documented in the README.
