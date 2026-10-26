package p2p

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// Sync limits of ADR-0019.
const (
	// DefaultBanDuration is how long a misbehaving peer stays banned when WithBanDuration gives none.
	DefaultBanDuration = time.Hour
	// SyncAttemptTimeout bounds one sync attempt against one peer.
	SyncAttemptTimeout = time.Duration(120e9) // 2 minutes
	// HeaderBatch is the most headers asked for in one request.
	HeaderBatch = 1000
	// BodyBatch is the most block bodies asked for in one request.
	BodyBatch = 16

	// maxDifficulty is the most leading zero bits a SHA-256 hash can have; a header claiming more is a lie.
	maxDifficulty = 256
)

var (
	ErrInsufficientWork = errors.New("p2p: candidate chain has no more work than the local one")
	ErrLocalFailure     = errors.New("p2p: local chain cannot apply the blocks")
	ErrPeerPruned       = errors.New("p2p: peer pruned the requested blocks")
	ErrBanned           = errors.New("p2p: peer is banned")

	errMisbehaved = errors.New("p2p: peer misbehaved")
	errRequest    = errors.New("p2p: peer request failed")
)

// SyncLedger is the port sync needs from the local chain. AcceptBlock and
// ReplaceChain are the only writes; they return errors satisfying
// errors.Is(err, ErrNotOnTip), ErrInsufficientWork or ErrLocalFailure for the
// outcomes that are not the peer's fault, and any other error for an invalid chain.
type SyncLedger interface {
	Tip() (*domain.Block, error)
	BlocksRange(from, limit int) ([]*domain.Block, error)
	HasBlock(hash string) bool
	AcceptBlock(ctx context.Context, b *domain.Block) error
	ReplaceChain(ctx context.Context, candidate []*domain.Block) error
}

// Sync brings the local chain to the best connected peer's, headers before
// bodies, switching to the branch with more work (ADR-0019). It tries the
// candidates in order until one attempt changes the local tip and reports
// whether the tip changed. It writes only through the SyncLedger.
func (n *Node) Sync(ctx context.Context) bool {
	if n.sync == nil {
		return false
	}

	tip, err := n.sync.Tip()
	if err != nil {
		n.log.Error("p2p: sync cannot read the local tip", "error", err)
		return false
	}

	for _, p := range n.syncCandidates(tip) {
		if ctx.Err() != nil {
			return false
		}

		if n.book.Banned(p.NodeID, p.Addr) { // an earlier attempt banned the address
			continue
		}

		if n.syncAttempt(ctx, p) {
			return true
		}
	}

	return false
}

// syncCandidates lists the peers worth syncing from: with an address, not
// banned, on a tip the ledger lacks and at least as high as ours; highest first.
func (n *Node) syncCandidates(tip *domain.Block) []PeerInfo {
	var out []PeerInfo

	for _, p := range n.book.Peers() {
		if p.Addr != "" && p.Height >= tip.Index && !n.book.Banned(p.NodeID, p.Addr) && !n.sync.HasBlock(p.TipHash) {
			out = append(out, p)
		}
	}

	slices.SortFunc(out, func(a, b PeerInfo) int {
		if a.Height != b.Height {
			return b.Height - a.Height
		}

		return strings.Compare(a.NodeID, b.NodeID)
	})

	return out
}

// syncAttempt syncs from one peer, settles the outcome and reports whether the local tip changed.
func (n *Node) syncAttempt(ctx context.Context, p PeerInfo) bool {
	before, err := n.sync.Tip()
	if err != nil {
		return false
	}

	actx, cancel := context.WithTimeout(ctx, n.syncTimeout)
	defer cancel()

	err = n.syncFrom(actx, p)
	n.settle(actx, p, err)

	after, err := n.sync.Tip()

	return err == nil && blockHash(after) != blockHash(before)
}

// settle applies the outcome of an attempt: misbehaviour (and any error the
// ledger gives for the peer's blocks) bans, a failed request disconnects, the rest is no one's fault.
func (n *Node) settle(ctx context.Context, p PeerInfo, err error) {
	if err == nil {
		return
	}

	if errors.Is(err, errMisbehaved) {
		n.ban(p, err)
		return
	}

	if ctx.Err() != nil {
		return
	}

	if errors.Is(err, errRequest) {
		n.log.Warn("p2p: sync request failed", "addr", p.Addr, "error", err)
		n.book.Disconnect(p.NodeID)

		return
	}

	if errors.Is(err, ErrInsufficientWork) || errors.Is(err, ErrNotOnTip) || errors.Is(err, ErrLocalFailure) || errors.Is(err, ErrPeerPruned) {
		n.log.Info("p2p: sync stopped", "addr", p.Addr, "reason", err)
		return
	}

	n.ban(p, err)
}

func (n *Node) ban(p PeerInfo, reason error) {
	n.log.Warn("p2p: peer banned", "addr", p.Addr, "node", p.NodeID, "reason", reason)
	n.book.Ban(p.NodeID, p.Addr, n.now().Add(n.banFor))
}

func misbehaved(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errMisbehaved, fmt.Sprintf(format, args...))
}

// syncFrom runs one attempt: resolve a fork if the peer's chain diverges below our tip, then extend.
func (n *Node) syncFrom(ctx context.Context, p PeerInfo) error {
	tip, err := n.localTip()
	if err != nil {
		return err
	}

	ancestor, err := n.forkPoint(ctx, p, tip)
	if err != nil {
		return err
	}

	if ancestor.Index < tip.Index {
		err = n.reorg(ctx, p, tip, ancestor)
		if err != nil {
			return err
		}
	}

	return n.extend(ctx, p)
}

func (n *Node) localTip() (*domain.Block, error) {
	tip, err := n.sync.Tip()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLocalFailure, err)
	}

	return tip, nil
}

// localRange returns count local blocks from height from, however many pages that takes.
func (n *Node) localRange(from, count int) ([]*domain.Block, error) {
	out := make([]*domain.Block, 0, count)

	for len(out) < count {
		page, err := n.sync.BlocksRange(from+len(out), min(count-len(out), HeaderBatch))
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrLocalFailure, err)
		}

		if len(page) == 0 {
			return nil, fmt.Errorf("%w: short range from height %d", ErrLocalFailure, from)
		}

		out = append(out, page...)
	}

	return out[:count], nil
}

// forkPoint returns the peer's header at the highest height both chains share,
// looking at most min(tip, peer height) down in windows of HeaderBatch headers.
func (n *Node) forkPoint(ctx context.Context, p PeerInfo, tip *domain.Block) (*domain.Block, error) {
	for hi := min(tip.Index, p.Height); ; {
		lo := max(0, hi-HeaderBatch+1)
		count := hi - lo + 1

		peer, err := n.fetchHeaders(ctx, p.Addr, lo, count, nil)
		if err != nil {
			return nil, err
		}

		if len(peer) != count {
			return nil, misbehaved("%d headers from height %d, want %d", len(peer), lo, count)
		}

		local, err := n.localRange(lo, count)
		if err != nil {
			return nil, err
		}

		for i := count - 1; i >= 0; i-- {
			if blockHash(peer[i]) == blockHash(local[i]) {
				return peer[i], nil
			}
		}

		if lo == 0 {
			return nil, misbehaved("no common block, not even the genesis")
		}

		hi = lo - 1
	}
}

// reorg replaces the local chain above ancestor by the shortest prefix of the
// peer's branch that has strictly more work than the local tail.
func (n *Node) reorg(ctx context.Context, p PeerInfo, tip, ancestor *domain.Block) error {
	fork := ancestor.Index + 1

	tail, err := n.localRange(fork, tip.Index-ancestor.Index)
	if err != nil {
		return err
	}

	headers, err := n.winningHeaders(ctx, p, ancestor, domain.ChainWork(tail))
	if err != nil {
		return err
	}

	var bodies []*domain.Block

	for batch := range slices.Chunk(headers, BodyBatch) {
		page, err := n.fetchBodies(ctx, p.Addr, batch)
		if err != nil {
			return err
		}

		bodies = append(bodies, page...)
	}

	prefix, err := n.localRange(0, fork)
	if err != nil {
		return err
	}

	return n.sync.ReplaceChain(ctx, append(prefix, bodies...))
}

// winningHeaders fetches the peer's headers after ancestor up to the first
// height where their work exceeds need; ErrInsufficientWork when they end first.
func (n *Node) winningHeaders(ctx context.Context, p PeerInfo, ancestor *domain.Block, need *big.Int) ([]*domain.Block, error) {
	var (
		headers []*domain.Block
		work    = new(big.Int)
		prev    = ancestor
	)

	for {
		page, err := n.fetchHeaders(ctx, p.Addr, prev.Index+1, HeaderBatch, prev)
		if err != nil {
			return nil, err
		}

		for _, h := range page {
			headers = append(headers, h)

			work.Add(work, domain.BlockWork(h))

			if work.Cmp(need) > 0 {
				return headers, nil
			}
		}

		if len(page) < HeaderBatch {
			return nil, ErrInsufficientWork
		}

		prev = page[len(page)-1]
	}
}

// extend appends the peer's blocks after the local tip, one header page at a time.
func (n *Node) extend(ctx context.Context, p PeerInfo) error {
	for {
		tip, err := n.localTip()
		if err != nil {
			return err
		}

		headers, err := n.fetchHeaders(ctx, p.Addr, tip.Index+1, HeaderBatch, tip)
		if err != nil {
			return err
		}

		for batch := range slices.Chunk(headers, BodyBatch) {
			bodies, err := n.fetchBodies(ctx, p.Addr, batch)
			if err != nil {
				return err
			}

			for _, b := range bodies {
				err = n.sync.AcceptBlock(ctx, b)
				if err != nil {
					return err
				}
			}
		}

		if len(headers) < HeaderBatch {
			return nil
		}
	}
}

// blockPage is the body of GET /headers and GET /blocks.
type blockPage struct {
	Blocks []*domain.Block `json:"blocks"`
}

// get fetches one page; a failed request is tagged errRequest, except a
// pruned range (410), which is the peer's limit and not its failure.
func (n *Node) get(ctx context.Context, addr, route string, from, limit, maxBytes int) ([]*domain.Block, error) {
	var resp blockPage

	err := n.doMax(ctx, http.MethodGet, fmt.Sprintf("%s/%s?from=%d&limit=%d", addr, route, from, limit), nil, &resp, maxBytes)
	if err != nil {
		var se *statusError
		if errors.As(err, &se) && se.code == http.StatusGone {
			return nil, fmt.Errorf("%w: %w", ErrPeerPruned, err)
		}

		return nil, fmt.Errorf("%w: %w", errRequest, err)
	}

	if len(resp.Blocks) > limit {
		return nil, misbehaved("%d blocks, asked for %d", len(resp.Blocks), limit)
	}

	return resp.Blocks, nil
}

// fetchHeaders returns up to limit headers from height from, checked: heights
// consecutive, hash right, difficulty possible and each linked to the one
// before (the first to prev, when known).
func (n *Node) fetchHeaders(ctx context.Context, addr string, from, limit int, prev *domain.Block) ([]*domain.Block, error) {
	headers, err := n.get(ctx, addr, "headers", from, limit, maxResponseBytes)
	if err != nil {
		return nil, err
	}

	for i, h := range headers {
		err = checkHeader(h, i, from, prev)
		if err != nil {
			return nil, err
		}

		prev = h
	}

	return headers, nil
}

// checkHeader checks the i-th header of a page asked from height from: height, hash, difficulty and link to prev (when known).
func checkHeader(h *domain.Block, i, from int, prev *domain.Block) error {
	if h == nil || h.Index != from+i {
		return misbehaved("header out of order at position %d from height %d", i, from)
	}

	if h.Index > 0 && h.Hash != h.CalculateHash() {
		return misbehaved("header %d has a wrong hash", h.Index)
	}

	if h.Difficulty < 0 || h.Difficulty > maxDifficulty {
		return misbehaved("header %d has difficulty %d", h.Index, h.Difficulty)
	}

	if prev != nil && h.PrevHash != blockHash(prev) {
		return misbehaved("header %d does not link to its parent", h.Index)
	}

	return nil
}

// fetchBodies returns the blocks for headers, checked against them.
func (n *Node) fetchBodies(ctx context.Context, addr string, headers []*domain.Block) ([]*domain.Block, error) {
	bodies, err := n.get(ctx, addr, "blocks", headers[0].Index, len(headers), len(headers)*2*domain.MaxBlockBytes())
	if err != nil {
		return nil, err
	}

	if len(bodies) != len(headers) {
		return nil, misbehaved("%d blocks from height %d, want %d", len(bodies), headers[0].Index, len(headers))
	}

	for i, b := range bodies {
		if b == nil || b.Index != headers[i].Index || b.Hash != headers[i].Hash {
			return nil, misbehaved("block at height %d does not match its header", headers[i].Index)
		}

		if b.MerkleRoot != domain.MerkleRoot(b.Transactions) {
			if len(b.Transactions) == 0 {
				return nil, fmt.Errorf("%w: block %d has no body", ErrPeerPruned, b.Index)
			}

			return nil, misbehaved("block %d has a wrong merkle root", b.Index)
		}
	}

	return bodies, nil
}
