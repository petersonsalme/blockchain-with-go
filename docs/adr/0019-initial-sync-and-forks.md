# ADR-0019: Initial block download and fork resolution over HTTP

Status: Accepted (ENG-32)

## Context

ADR-0013 connects nodes and ADR-0018 gossips blocks, but a block is accepted only when it extends the local tip. A new node (genesis only), a node that was offline, or a node on another branch never catches up: gossip ignores what it cannot append, and nothing calls `chain.Service.ReplaceChain` (ADR-0010) with a peer's chain. ENG-32 asks for headers first, then bodies in batches from the best peer; fork detection with a switch to the branch with more work; and tolerance of slow or malicious peers (timeout, simple ban). Constraints: `internal/p2p` depends on `internal/domain` and the standard library only (ADR-0013); peer requests are HTTP + JSON inside the OpenAPI contract (ADR-0007); a peer's height and tip hash are in its hello and refreshed every dialer round.

## Options

Transport for headers:
1. **New route `GET /headers?from=&limit=` (chosen).** Same page shape as `GET /blocks`, every block without transactions, up to 1000 per page. Cost: one route, one contract case set. A page of 1000 headers is ~0.5 MB, inside the 1 MiB response bound the dialer already uses.
2. **Bodies only through `GET /blocks`.** No new route, but the fork-choice decision needs the whole competing branch in full before it can compare work; a peer that lies about its height costs full blocks instead of headers. Not headers-first, which the roadmap item asks for.
3. **Block locator (Bitcoin `getheaders`).** One request finds the fork point; needs a POST route with a list of hashes and its own validation. Unneeded at this scale: a descending window of 1000 headers finds any fork shallower than 1000 blocks in one request.

Fork choice and apply:
- A. **Reuse `ReplaceChain` for the shortest winning prefix, then `AcceptBlock` (chosen).** Bodies are fetched only up to the first height where the peer's branch has strictly more work than the local tail; that prefix goes to `ReplaceChain`, the rest is appended block by block. Memory bounded by the reorg depth; progress of a long IBD is committed block by block and survives a crash or a timeout.
- B. Whole peer chain to `ReplaceChain`. One call, but the whole IBD is held in memory and validated before the first write; a timeout loses everything.

Recommendation: option 1 + A. It is headers-first with one new read-only route, keeps all validation in `chain.Service` (no second set of rules in `internal/p2p`) and bounds what a lying peer can make the node download.

## Decision

- **Route.** `GET /headers?from=&limit=` (`from` default 0, `limit` default and cap 1000; `limit=0` or non-integers → `400 invalid_query`): `200` with the `GET /blocks` page (`blocks`, `from`, `limit`, `next`), every block with `Transactions: null`. Served by every node, whether or not p2p is enabled; reads never wait for `Mine`.
- **Port.** `p2p.SyncLedger` (`Tip`, `BlocksRange(from, limit)`, `HasBlock`, `AcceptBlock`, `ReplaceChain(ctx, candidate) error`), set with `p2p.WithSyncLedger`; nil keeps sync off. The adapter in `cmd/bwg` maps `chain.ErrInsufficientWork` → `p2p.ErrInsufficientWork`, `chain.ErrNotOnTip` → `p2p.ErrNotOnTip`, and `chain.ErrStateDiverged`, `storage.ErrConflict`, `storage.ErrReorgTooDeep`, `storage.ErrEmpty` → `p2p.ErrLocalFailure`; it resubmits `Reorg.Orphaned` to the pool with `Submit`, ignoring refusals.
- **When.** `Node.Sync(ctx)` runs at the end of every dialer round (after `collect`, before `Expire`). Candidates: connected peers with a non-empty `Addr`, not banned, whose `TipHash` the ledger does not know and whose `Height >= local tip Index`, ordered by `Height` descending then `NodeID` ascending. They are tried in order until one attempt changes the local tip; each attempt is bounded by `SyncAttemptTimeout` (default 2 min) and every request by the dialer client's 5 s timeout.
- **Fork point.** With `h = min(local tip, peer height)`: fetch the peer headers `[max(0, h-999), h]` and the local ones at the same heights (`BlocksRange`); the highest height with equal hashes is the common ancestor `a`, fork `f = a+1`. No match and window start > 0 → repeat below the window; no match at height 0 → misbehaviour.
- **Header checks** (every page): heights consecutive from the requested `from`, at most `limit` items, `Hash == CalculateHash()`, `PrevHash` equal to the previous header's hash (the first one links to the header at `from-1`). Any failure → misbehaviour.
- **Fork (`f <= local tip`).** Fetch peer headers from `f` until their `domain.ChainWork` is strictly greater than that of the local headers `[f, tip]`, then fetch those bodies and call `ReplaceChain(local[:f] + bodies)`. Headers end first → no more work; the attempt stops, no ban.
- **Extension.** From `tip+1`: header page, then its bodies in batches of `BodyBatch` = 16 (`GET /blocks?from=&limit=16`, response bound `16 × 2 × MaxBlockBytes()`), `AcceptBlock` each in order; repeat until a short header page.
- **Body checks.** `body.Hash` equals the header at its height; `body.MerkleRoot != domain.MerkleRoot(body.Transactions)` with no transactions → the peer pruned that body (`p2p.ErrPeerPruned`, no ban, next peer); with transactions → misbehaviour.
- **Outcome of an attempt.** `ErrInsufficientWork`, `ErrNotOnTip` (the tip moved), `ErrLocalFailure`, `ErrPeerPruned` and context errors stop the attempt without penalty. A request error or timeout disconnects the peer (it is slow or gone), no ban. Misbehaviour (failed header or body check, a short or malformed page, any other error from `AcceptBlock`/`ReplaceChain`) bans the peer.
- **Ban.** `Book.Ban(nodeID, addr, until)` disconnects the peer and, until `until`, `Connect` refuses that `NodeID` or `Addr` with `p2p.ErrBanned` and `Candidates` skips the address. Duration `DefaultBanDuration` = 1 h (`WithBanDuration`). Bans live in memory only. `POST /p2p/hello` from a banned peer answers `403 peer_banned`.

## Consequences

- A new node reaches the network tip in at most one dialer round plus the download time; a fork shallower than 1000 blocks costs two header requests before any body.
- Headers are not checked by the consensus before the bodies arrive (`internal/p2p` cannot import it); a peer that claims fake work costs at most the bodies of the claimed branch, then a ban.
- A peer whose branch has more work but a lower height is never tried (accepted limitation, shared with height-ordered peer selection).
- Pruned peers (ADR-0014) cannot serve bodies below their pruned height; the node tries the next peer.
- A ban is keyed by `NodeID` and address and forgotten on restart; a hostile peer can return with a new id from another address.
- The pool is not revalidated after a reorg beyond the resubmission of orphaned transactions (as in ADR-0010).
- Gossip is unchanged: a block that does not extend the tip is still dropped and is picked up by the next round's sync.

