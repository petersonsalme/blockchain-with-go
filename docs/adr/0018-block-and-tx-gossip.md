# ADR-0018: Block and transaction gossip over HTTP

Status: Accepted (board, pending merge, ENG-31)

## Context

ADR-0013 connects nodes (handshake, peer book, dialer) but nothing travels between them: a block mined on A never reaches B. ENG-31 asks for announcements (inv) and fetches (get) of blocks and transactions, deduplication by hash, no echo to the peer the object came from, and validation before relaying. ADR-0013 left a door open: "ENG-31 shortens [polling] by pushing announcements with `POST`". Initial sync and forks are ENG-32. Constraint from ADR-0013: `internal/p2p` depends on `internal/domain` and the standard library only.

## Options

1. **Announcer-driven inv/want over HTTP (chosen).** The node that has an object posts `POST /p2p/inv` with the hashes; the peer answers with the hashes it wants; the announcer then posts each wanted object (`POST /p2p/block`, `POST /p2p/tx`). Cost: 3 routes in `openapi.yaml`, one relay goroutine, a bounded seen-set. Everything runs in requests the announcer opens, so it needs only the peer's address, which the book already holds; reuses the contract test, rate limiter, body limits and logs.
2. **Receiver-driven get (Bitcoin `getdata`).** Receiver of an inv fetches the object from the announcer (`GET /blocks/{hash}`, a new `GET /p2p/tx/{id}` for pool transactions). Cost: the receiver must dial back the announcer, which fails for inbound peers without `ListenAddr`; needs a new route for unconfirmed transactions; two request directions per object.
3. **Push the full object without inv.** One route per kind, simplest code, but every peer receives every object from every neighbour: N-fold bandwidth on blocks up to `MAX_BLOCK_BYTES`, and the roadmap item asks for inv/get explicitly.

Recommendation: option 1. It meets the inv/get ask with one request direction, works for every peer the node can reach, and keeps the protocol inside the tested HTTP contract. Its price, a want round-trip before each object, is what option 3 saves at the cost of bandwidth.

## Decision

Option 1, with these rules.

- **Items.** `p2p.InvItem{Type string, Hash string}`, JSON `type`, `hash`; `Type` is `block` (hash = `Block.Hash`) or `tx` (hash = `Transaction.ID`); `Hash` is 64 lowercase hex. An inv carries 1 to `p2p.MaxInvItems` (1000) items.
- **Sender.** Every gossip request carries the sender's `node_id`; it must be a connected peer in the book (either direction), otherwise `p2p.ErrUnknownPeer`. This is not authentication (ADR-0013 has none); it only ties gossip to the handshake.
- **Seen set.** One bounded set of hashes per node (`DefaultMaxSeen` = 10000, oldest evicted first), shared by blocks and transactions. A hash enters it when the node mines or admits the object locally, and when an object with that hash is received, before validation, by an atomic add-if-absent: two concurrent deliveries of one object are processed once. An object rejected as invalid stays in the set (it is never fetched again); a block rejected because it does not extend the tip is removed from it (it may be valid later; ENG-32).
- **Want.** The answer to an inv is the items whose hash is neither in the seen set nor known to the ledger (block in the chain, transaction in the pool or the chain), in request order.
- **Validation before relay.** A received block is relayed only after `chain.Service.AcceptBlock` appended it: it must extend the current tip and pass the same checks as `ReplaceChain` (consensus `Verify`, block rules, UTXO delta, input signatures, contract delta); the pool drops what it confirms or conflicts with. A received transaction is relayed only after `Submit` admitted it. Anything else is answered with an error and never relayed.
- **No echo.** Relay sends the inv to every connected peer with a non-empty address except the one the object came from. Objects mined or submitted locally have no origin and go to every such peer.
- **Relay worker.** `Announce*` never blocks: it puts the item on a queue of 1024 entries (full → dropped and logged) drained by one goroutine that `Node.Run` starts and stops with `ctx`. Per peer: `POST /p2p/inv`, then one `POST` per wanted object. Failures are logged and never disconnect the peer (the dialer round does that).
- **Routes.** `POST /p2p/inv` `{"node_id", "items"}` → `200 {"want": [...]}`, `400 invalid_inv`, `403 unknown_peer`. `POST /p2p/block` `{"node_id", "block"}` and `POST /p2p/tx` `{"node_id", "tx"}` → `200 {"status": "accepted"|"known"}`, `403 unknown_peer`, `409 not_on_tip` (block only), `422 invalid_block` / `422 invalid_tx`. Without the peer layer, `404 p2p_disabled` as the other `/p2p` routes.
- **Ports.** `internal/p2p` declares `p2p.Ledger` (`HasBlock`, `HasTx`, `AcceptBlock`, `SubmitTx`); `cmd/bwg` adapts `*chain.Service` to it and maps `chain.ErrNotOnTip` to `p2p.ErrNotOnTip`. `internal/p2p` still imports neither `chain` nor `server`.

## Consequences

- A block reaches a node only if it extends that node's tip. A node that is behind, or on another branch, ignores it until ENG-32 syncs or reorganises; gossip never calls `ReplaceChain`.
- A peer without a `ListenAddr` (behind NAT) cannot receive pushes; it learns new blocks only through ENG-32 sync.
- `node_id` can be spoofed by anyone who knows a connected peer's id; the rate limiter and the validation before relay are the protection. Noted in SECURITY.md.
- The seen set forgets after 10000 hashes; a very old object could then be fetched and validated again, never accepted twice (the ledger rejects it).
- Superseded by the ENG-219 amendment below.

## Amendment (ENG-219): events for received blocks and transactions

Status: Accepted (board, pending merge, ENG-219)

Context: only the mining node emitted `GET /events`; on the three-node playground node1 mined block 8, all nodes reached height 8, and node2 and node3 sent no event.

Decision: a node publishes `event: block` for every block it appends, whatever the source: `POST /p2p/block` answered `accepted` (published by the handler), sync extension (one per `AcceptBlock`) and reorg (one per `Reorg.Connected` block, oldest first, after `ReplaceChain`), the last two in the `cmd/bwg` sync ledger through an injected `server.Events`. `POST /p2p/tx` answered `accepted` publishes `event: tx`. `known` answers and rejected objects publish nothing. Payloads are the same as for local events. Rejected: gossip-only (a synced or reorganised node stays silent) and an append observer in `chain.Service` (core write path, double publish with `Mine`).

Consequences:
- A client sees a `block` event whose `Index` is not above the last one it saw only after a reorg; there is no `reorg` event, and clients that need one must compare `Index` and `PrevHash`.
- An initial sync of many blocks emits one event per block; a subscriber more than 16 events behind is disconnected as before (reconnect, no replay).
- Orphaned transactions resubmitted after a reorg emit no `tx` event.
