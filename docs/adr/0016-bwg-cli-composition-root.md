# ADR-0016: one `bwg` binary is the composition root

Status: Accepted

## Context
ENG-37 adds `wallet` and `miner` subcommands (cobra) in `cmd/bwg`. The node wiring (repository, consensus, service, server, `-reindex`) lived in `cmd/node/main.go`, a `main` package no other binary can import. Keeping both entry points means duplicating the wiring or moving it into an importable package that reads configuration outside a composition root.

## Decision
`cmd/bwg` is the only binary and the only composition root. `bwg node` runs the node with the same environment variables, `.env` handling, logs and shutdown as the former `cmd/node`; `bwg node --reindex` replaces `node -reindex`. `cmd/node` is removed.

Rejected: a shared `cmd/internal/nodeapp` package used by a `cmd/node` shim and by `bwg node` (two entry points to keep in sync, configuration read outside `main`).

## Consequences
- `go run ./cmd/node` and `-reindex` (single dash) stop working; the docs use `go run ./cmd/bwg node` and `--reindex`.
- Configuration reading stays in one `main` package (`cmd/bwg`); `MAX_BLOCK_BYTES` remains the only on-demand read in `internal/domain`.
- New client commands (wallet, miner, future sync tools) are added as subcommands, not new binaries.
- Earlier ADRs that mention `cmd/node` are historical; read it as `cmd/bwg node`.
