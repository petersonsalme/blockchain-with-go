# Study guide

Walkthroughs of the code, one flow at a time. Each step names the
Go function that does it (file and name) and the ADR that decided it, and
`guide_test.go` fails when a name or a link stops being true.

- [Life of a transaction](transaction.md): from `bwg wallet send` signing the
  inputs to the inclusion proof of the mined block.
- [Life of a reorg](reorg.md): from the dialer's sync detecting a heavier
  branch to the orphaned transactions back in the mempool.

Decisions live in [`docs/adr/`](../adr/); the index is in the
[root README](../../README.md).
