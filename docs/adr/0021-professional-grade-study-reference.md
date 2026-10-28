# ADR-0021: Position the repository as a professional-grade study reference

Status: Accepted (ENG-218). Supersedes [ADR-0003](0003-learning-project-positioning.md).

## Context
- [ADR-0003](0003-learning-project-positioning.md) positions the repository as an old personal learning project and a pilot for Paperclip adoption with AI agents. Both stay true.
- The goal is now a professional-grade study project: the chain core is written by hand to learn it, and the work around it uses the tools the industry uses, so the project shows how a real system is built and operated.
- The next roadmap items (a metrics library, dashboards, TLS, indexing, a UI) reverse earlier "write it by hand" choices: [ADR-0010](0010-prometheus-metrics.md) rejected `client_golang`, and AGENTS.md said "Do not add heavy dependencies without justifiable need". Without a written rule, every item reopens the same argument.
- The code has no TLS and no authentication (`internal/server`, `cmd/bwg`), p2p is plain HTTP and a peer's `node_id` is spoofable ([ADR-0013](0013-p2p-over-http.md), [SECURITY.md](../../SECURITY.md)), and `RATE_LIMIT` is off by default. "Professional-grade" must not be read as "fit for real value".

## Options
1. **Keep ADR-0003 and decide every tool case by case.** Cost: nothing now; every roadmap item repeats the dependency argument, and AGENTS.md keeps a rule that the roadmap contradicts.
2. **Professional-grade study reference with a written dependency rule (chosen).** Cost: this ADR, a reworded README disclaimer and AGENTS.md rule; every new tool still needs its own ADR.
3. **Aim at production readiness.** Cost: a security audit, key custody, authenticated network identity, an upgrade and migration policy and operational support, which a personal project cannot provide; the disclaimer would become false.

## Decision
- **Meaning.** "Professional-grade" describes the engineering practice: written decisions (ADRs), contract tests, CI gates (race detector, coverage floor, lint, govulncheck, mutation testing), reproducible builds and releases, and observability. It does not describe fitness for use. The project is **not for production and must not hold, move or represent real value.**
- **Out of scope.** What production readiness needs beyond that practice: a security audit, key custody, network identity and authentication between peers, protocol upgrades and data migrations across releases, and operational support.
- **Write by hand what we want to learn:** consensus, the UTXO model, block and transaction validation, the mempool, gossip semantics, sync and reorg, and the persisted schema. A library that would do this work for us is not adopted.
- **Use market tools around it:** observability (metrics, logs, traces), dashboards, TLS, indexing and query, UI, and build, release and test tooling.
- **When a market tool is justified.** All of the following hold, and the ADR that adopts the tool shows each one:
  1. It works around the hand-written core, never inside it.
  2. It is a widely used standard for its concern and is actively maintained; a linked Go module has a license compatible with GPL-3.0 (a tool that runs as a separate service only needs a license that allows that use).
  3. It enters through an adapter or the composition root (`cmd/bwg`), or runs as a separate service (for example in `docker-compose.yml`); `internal/domain` never imports it and no global state is added.
  4. `govulncheck` stays clean, and the ADR lists the modules the tool adds to `go.mod`, direct and indirect.
  5. Writing it by hand would teach nothing about blockchains and would cost more than the dependency.
- **Cryptography** comes from the Go standard library and `golang.org/x/crypto`; primitives are never written by hand.
- **Carried over from ADR-0003.** The README opens with a prominent English disclaimer stating that the project is a personal study project; that it is a pilot for Paperclip adoption with AI agents and its recent changes are AI-generated suggestions; that it is not production-ready, not audited, has no TLS or authentication and speaks plain HTTP between peers; that it must not hold or move real value; and that it is not affiliated with the author's employer, clients or any other organization. The README title stays `Learning Blockchain with Go`. The GitHub About text uses the same positioning.

## Consequences
- AGENTS.md replaces "Do not add heavy dependencies without justifiable need" with a rule that points to the criteria above.
- An earlier ADR that chose to write a tool by hand is superseded only by the ADR that adopts the tool: [ADR-0010](0010-prometheus-metrics.md) stays Accepted until `client_golang` is actually adopted.
- The README, SECURITY.md and About text keep the not-audited, no-TLS, no-authentication, plain-HTTP p2p and no-real-value warnings; a change that adds TLS or authentication updates them in the same PR.
- The limits are written down instead of implied, as Geth does when it states that its RPC endpoints are "not designed to withstand attacks by hostile clients".
- Changing the project's relationship to the author's employer, ending the pilot or targeting production requires a new ADR that supersedes this one.
