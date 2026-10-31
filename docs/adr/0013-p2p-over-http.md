# ADR-0013: Peer-to-peer handshake and peer discovery over HTTP

Status: Accepted (board, <merge date>, ENG-30)

## Context

The node is a single HTTP server (README, trade-off 2). Blocks and transactions do not travel between nodes. ENG-30 adds the base layer: a message carrying protocol version, height and tip hash, a handshake that refuses incompatible peers, a persisted peer list with connection limits, seed peers from `SEED_PEERS`, and address exchange. Gossip (ENG-31) and initial sync and forks (ENG-32) build on it. The transport (TCP or HTTP) is open in the roadmap item.

## Options

1. **HTTP + JSON on the existing server (chosen).** Peers call `POST /p2p/hello` and `GET /p2p/peers` on each other's HTTP port. A "connection" is a peer entry that a successful hello created and a later hello refreshes; it expires when the peer is not seen for 3 rounds. Cost: 2 routes in `openapi.yaml`, an outbound `net/http` client and one background loop. Reuses the ServeMux (ADR-0006), the contract test (ADR-0007), rate limiting, body limits, request logs and metrics. ENG-32 can fetch blocks with the existing `GET /blocks?from=&limit=`. No new dependency.
2. **Raw TCP, length-prefixed JSON frames, own port (`P2P_PORT`)** (Bitcoin-style `version`/`verack`/`ping`/`addr`). Cost: a second listener and its shutdown, framing and read deadlines, one reader and one writer goroutine per connection, a hand-written protocol outside the OpenAPI contract, and a new way to fetch blocks for ENG-32. It gives long-lived connections and push without polling, which only gossip needs.
3. **libp2p.** Discovery, multiplexing and encryption ready-made, but dozens of module requirements, against the AGENTS.md rule on heavy dependencies, and it hides exactly what this educational project wants to show (ADR-0003).

Recommendation: option 1. It meets both ENG-30 criteria with the parts the node already has, keeps every peer message inside the tested HTTP contract and leaves the read path for ENG-32 free. The price is polling: a peer's height is at most one round old, which ENG-31 shortens by pushing announcements with `POST`.

## Decision

Option 1, with these rules. New package `internal/p2p`; it depends on `internal/domain` and the standard library only, never on `internal/server`, `internal/chain` or `internal/storage`.

- **Message.** `p2p.Hello{Version int, NodeID string, GenesisHash string, Height int, TipHash string, ListenAddr string}`, JSON `version`, `node_id`, `genesis_hash`, `height`, `tip_hash`, `listen_addr`. `p2p.ProtocolVersion = 1`. `NodeID` is 16 random bytes in lowercase hex (32 chars), drawn once per process start. `ListenAddr` is the base URL other nodes dial (`http://10.0.0.2:8080`); empty means the node accepts no inbound peers and is never advertised.
- **Address form.** `p2p.NormalizeAddr` accepts only `http` or `https` URLs with a host and an explicit port, no user info, no query, no fragment and an empty path or `/`; it returns `scheme://host:port` with scheme and host in lower case. Anything else is `p2p.ErrBadAddr`.
- **Handshake checks**, in this order, by `p2p.Check(local, remote Hello) error`:
  1. malformed hello (negative height, `NodeID` not 32 lowercase hex, `GenesisHash` or `TipHash` not 64 lowercase hex, `ListenAddr` not empty and not valid for `NormalizeAddr`) → `p2p.ErrBadHello`;
  2. `remote.Version != local.Version` → `p2p.ErrIncompatibleVersion` (exact match: there is one version; a supported range is a follow-up when version 2 exists);
  3. `remote.GenesisHash != local.GenesisHash` → `p2p.ErrGenesisMismatch` (a PoA node and a PoW node, or two chains with different `GENESIS_ALLOCATIONS`, never pair);
  4. `remote.NodeID == local.NodeID` → `p2p.ErrSelf`.
  The initiator runs the same `Check` on the responder's hello, so both sides refuse an incompatible peer.
- **Peer book** (`p2p.Book`, safe for concurrent use, one mutex). It keeps known addresses (normalised, deduplicated, at most `MaxAddrs`, default 1000; when full, new addresses are dropped, never old ones) and connected peers keyed by `NodeID` with `Addr`, `Height`, `TipHash`, `Inbound` and `LastSeen`. Limits `MaxInbound` (default 16) and `MaxOutbound` (default 8) count connected peers by direction; a new peer over its limit is refused with `p2p.ErrPeerLimit`, while a hello from an already connected `NodeID` only refreshes it. `Expire(before time.Time)` drops connected peers with `LastSeen` before the cutoff; their addresses stay known.
- **Routes.** `POST /p2p/hello`: body is the caller's hello; `200` with the local hello when accepted (the caller becomes an inbound peer); `400 invalid_hello`, `409 incompatible_version`, `409 genesis_mismatch`, `409 self_connection`, `503 peer_limit`. `GET /p2p/peers`: `200` with the connected peers and the known addresses (addr exchange).
- **Dialer** (`Node.Run(ctx)` in `internal/p2p`). Rounds every `PEER_INTERVAL` (default 30s), the first one at start: say hello again to every outbound peer (a failed request or `Check` error disconnects it), dial known addresses that are not connected until `MaxOutbound` (never its own `ListenAddr`), fetch `GET /p2p/peers` from each outbound peer and add its addresses to the book, expire peers not seen for 3 intervals, then save the known addresses. One HTTP client with a 5s timeout. `Run` returns when `ctx` is done and leaves no goroutine behind.
- **Configuration** (read in `cmd/bwg`): `SEED_PEERS` (comma-separated base URLs; an invalid item fails startup, as `GENESIS_ALLOCATIONS` does), `P2P_LISTEN_ADDR` (the advertised `ListenAddr`), `MAX_INBOUND_PEERS`, `MAX_OUTBOUND_PEERS`, `PEER_INTERVAL` (blank, invalid or non-positive → default).
- **Persistence.** Known addresses go to `$DATA_DIR/peers.json` (JSON array of strings, mode `0o600`, written to a temporary file in the same directory and renamed). Without `DATA_DIR` nothing is persisted. At start the node loads that file plus `SEED_PEERS`; a missing file is an empty list, a corrupt file fails startup.

## Consequences

- Peer state lives outside `chain.db`: the storage contracts and adapters do not change.
- Heights are refreshed once per round; nothing is pushed yet. Gossip (ENG-31) adds announcements; sync (ENG-32) uses the existing block routes.
- No authentication or encryption between peers: any client that can reach the HTTP port can say hello. The rate limiter is the only protection. A node dials addresses that peers advertise, so a hostile peer can point it at internal URLs (SSRF); acceptable for a learning project, noted in SECURITY.md.
- A node behind NAT can dial out but cannot be reached; it leaves `P2P_LISTEN_ADDR` empty.
- `internal/p2p` joins the core areas in AGENTS.md (handshake rules, peer book lock and limits, dialer loop).

