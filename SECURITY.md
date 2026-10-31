# Security Policy

This is an **educational, personal learning project**. It is not audited and is not production-ready. Do not use it to hold, move or represent real value, and do not use keys generated or stored by it for anything real.

Many recent changes are AI-generated suggestions (see the disclaimer in the [README](README.md)).

## Peer-to-peer limits

Nodes talk to each other over plain HTTP with no authentication or encryption ([ADR-0013](docs/adr/0013-p2p-over-http.md)): any client that can reach the HTTP port can say hello and be listed as a peer, and the rate limiter is the only protection. A node also dials the addresses its peers advertise, so a hostile peer can point it at internal URLs (SSRF). The dialer sends only its own hello and never follows redirects, but this is acceptable for a learning project only; do not expose a node with `SEED_PEERS` set to networks you do not trust.

Gossip ([ADR-0018](docs/adr/0018-block-and-tx-gossip.md)) ties each request to a connected peer by its `node_id`, but the id is not a secret: anyone who knows a connected peer's id can send gossip in its name (`node_id` is spoofable). The rate limiter and the validation before relay (a block must extend the tip and pass every check, a transaction must be admitted by the pool) are the protection.

Sync ([ADR-0019](docs/adr/0019-initial-sync-and-forks.md)) bans a peer that sends invalid headers or blocks, by `node_id` and address, for one hour. The ban lives in memory and is forgotten on restart; a hostile peer can return with a new id from another address. Headers are not checked by the consensus before the bodies arrive, so a peer that claims fake work can cost the bodies of the claimed branch before it is banned. A peer whose branch has more work but a lower height is never tried.

## Reverse proxy setup

`compose.tls.yml` shows a node behind a TLS-terminating Caddy that publishes only read routes and keeps `/p2p/*`, writes and `/metrics` internal ([ADR-0024](docs/adr/0024-caddy-tls-reverse-proxy.md), [guide](docs/playground.md#running-behind-a-reverse-proxy-composetlsyml)). It adds encryption in transit to the exposed routes and nothing else: there is still no authentication, `node_id` is still spoofable, peers still use plain HTTP on the internal network, the certificate comes from a self-signed local CA, and the rate limiter keys on the connection address, so behind the proxy `RATE_LIMIT` is one cap shared by all public clients. It is an example, not audited and not for public deployment.

## Reporting a vulnerability

If you find a security issue, you are welcome to report it privately through GitHub's
[private vulnerability reporting](https://github.com/petersonsalme/blockchain-with-go/security/advisories/new).
Please do not open a public issue for it.

This is maintained on personal time with no guaranteed response time and no bug bounty. Reports are appreciated as learning input, not handled under any SLA.
