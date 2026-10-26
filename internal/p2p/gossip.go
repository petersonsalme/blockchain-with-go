package p2p

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// Gossip limits and item types of ADR-0018.
const (
	// InvBlock and InvTx are the item types of an inv.
	InvBlock = "block"
	InvTx    = "tx"

	// MaxInvItems is the most items an inv carries.
	MaxInvItems = 1000
	// DefaultMaxSeen is the size of the seen set when WithMaxSeen gives none.
	DefaultMaxSeen = 10000

	relayQueueSize = 1024
)

var (
	ErrUnknownPeer    = errors.New("p2p: sender is not a connected peer")
	ErrBadInv         = errors.New("p2p: malformed inv")
	ErrNotOnTip       = errors.New("p2p: block does not extend the tip")
	ErrInvalidObject  = errors.New("p2p: invalid object")
	ErrGossipDisabled = errors.New("p2p: gossip disabled")
)

// Ledger is the port gossip needs from the local chain and pool. AcceptBlock
// returns an error satisfying errors.Is(err, ErrNotOnTip) when the block does
// not extend the tip.
type Ledger interface {
	HasBlock(hash string) bool
	HasTx(id string) bool
	AcceptBlock(ctx context.Context, b *domain.Block) error
	SubmitTx(tx domain.Transaction) error
}

// InvItem names one object by type and hash.
type InvItem struct {
	Type string `json:"type"`
	Hash string `json:"hash"`
}

// WithLedger turns gossip on; nil keeps it off.
func WithLedger(l Ledger) Option { return func(n *Node) { n.ledger = l } }

// WithMaxSeen sets the size of the seen set; a value <= 0 keeps DefaultMaxSeen.
func WithMaxSeen(m int) Option {
	return func(n *Node) {
		if m > 0 {
			n.maxSeen = m
		}
	}
}

// seenSet is a bounded FIFO set of hashes; the oldest is evicted first.
type seenSet struct {
	mu    sync.Mutex
	limit int
	order *list.List
	items map[string]*list.Element
}

func newSeenSet(limit int) *seenSet {
	return &seenSet{limit: limit, order: list.New(), items: map[string]*list.Element{}}
}

func (s *seenSet) has(h string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, ok := s.items[h]

	return ok
}

// add inserts h and reports whether it was absent.
func (s *seenSet) add(h string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.items[h]; ok {
		return false
	}

	s.items[h] = s.order.PushBack(h)

	for s.order.Len() > s.limit {
		oldest := s.order.Front()
		h, _ := oldest.Value.(string)
		delete(s.items, h)
		s.order.Remove(oldest)
	}

	return true
}

func (s *seenSet) remove(h string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if e, ok := s.items[h]; ok {
		s.order.Remove(e)
		delete(s.items, h)
	}
}

// relay is one queued object with the peer it came from ("" for local ones).
type relay struct {
	item   InvItem
	block  *domain.Block
	tx     *domain.Transaction
	origin string
}

func (n *Node) connected(nodeID string) bool {
	for _, p := range n.book.Peers() {
		if p.NodeID == nodeID {
			return true
		}
	}

	return false
}

func (n *Node) known(it InvItem) bool {
	if n.seen.has(it.Hash) {
		return true
	}

	if it.Type == InvBlock {
		return n.ledger.HasBlock(it.Hash)
	}

	return n.ledger.HasTx(it.Hash)
}

// Inv answers an inv from a connected peer with the items it should send, in order.
func (n *Node) Inv(from string, items []InvItem) ([]InvItem, error) {
	if n.ledger == nil {
		return nil, ErrGossipDisabled
	}

	if !n.connected(from) {
		return nil, ErrUnknownPeer
	}

	if len(items) == 0 || len(items) > MaxInvItems {
		return nil, ErrBadInv
	}

	for _, it := range items {
		if (it.Type != InvBlock && it.Type != InvTx) || !isHex(it.Hash, 64) {
			return nil, ErrBadInv
		}
	}

	want := []InvItem{}

	for _, it := range items {
		if !n.known(it) {
			want = append(want, it)
		}
	}

	return want, nil
}

// ReceiveBlock validates a block sent by a connected peer through the ledger
// and queues it for relay to the other peers.
func (n *Node) ReceiveBlock(ctx context.Context, from string, b *domain.Block) (bool, error) {
	if b == nil {
		return false, fmt.Errorf("%w: no block", ErrInvalidObject)
	}

	return n.receive(from, relay{item: InvItem{Type: InvBlock, Hash: b.Hash}, block: b},
		func() error { return n.ledger.AcceptBlock(ctx, b) })
}

// ReceiveTx validates a transaction sent by a connected peer through the
// ledger and queues it for relay to the other peers.
func (n *Node) ReceiveTx(from string, tx domain.Transaction) (bool, error) {
	return n.receive(from, relay{item: InvItem{Type: InvTx, Hash: tx.ID}, tx: &tx},
		func() error { return n.ledger.SubmitTx(tx) })
}

func (n *Node) receive(from string, r relay, accept func() error) (bool, error) {
	if n.ledger == nil {
		return false, ErrGossipDisabled
	}

	if !n.connected(from) {
		return false, ErrUnknownPeer
	}

	if !isHex(r.item.Hash, 64) {
		return false, fmt.Errorf("%w: bad hash", ErrInvalidObject)
	}

	if n.known(r.item) || !n.seen.add(r.item.Hash) {
		return false, nil
	}

	err := accept()
	if errors.Is(err, ErrNotOnTip) {
		n.seen.remove(r.item.Hash)
		return false, ErrNotOnTip
	}

	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrInvalidObject, err)
	}

	r.origin = from
	n.enqueue(r)

	return true, nil
}

// AnnounceBlock queues a locally mined or admitted block for every peer; it never blocks.
func (n *Node) AnnounceBlock(b *domain.Block) {
	n.announce(relay{item: InvItem{Type: InvBlock, Hash: b.Hash}, block: b})
}

// AnnounceTx queues a locally admitted transaction for every peer; it never blocks.
func (n *Node) AnnounceTx(tx domain.Transaction) {
	n.announce(relay{item: InvItem{Type: InvTx, Hash: tx.ID}, tx: &tx})
}

func (n *Node) announce(r relay) {
	if n.ledger == nil {
		return
	}

	n.seen.add(r.item.Hash)
	n.enqueue(r)
}

func (n *Node) enqueue(r relay) {
	select {
	case n.queue <- r:
	default:
		n.log.Warn("p2p: relay queue full, dropping", "type", r.item.Type, "hash", r.item.Hash)
	}
}

// relayLoop drains the queue until ctx is done.
func (n *Node) relayLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case r := <-n.queue:
			n.relayOne(ctx, r)
		}
	}
}

// relayOne offers r to every connected peer with an address except its origin.
func (n *Node) relayOne(ctx context.Context, r relay) {
	for _, p := range n.book.Peers() {
		if ctx.Err() != nil {
			return
		}

		if p.Addr == "" || p.NodeID == r.origin {
			continue
		}

		err := n.offer(ctx, p.Addr, r)
		if err != nil {
			n.log.Warn("p2p: relay failed", "addr", p.Addr, "type", r.item.Type, "hash", r.item.Hash, "error", err)
		}
	}
}

// offer posts the inv for r to addr and, when the peer wants it, the object.
func (n *Node) offer(ctx context.Context, addr string, r relay) error {
	body, err := json.Marshal(map[string]any{"node_id": n.nodeID, "items": []InvItem{r.item}})
	if err != nil {
		return err
	}

	var resp struct {
		Want []InvItem `json:"want"`
	}

	err = n.do(ctx, http.MethodPost, addr+"/p2p/inv", body, &resp)
	if err != nil {
		return err
	}

	if !containsItem(resp.Want, r.item) {
		return nil
	}

	path, payload := "/p2p/tx", map[string]any{"node_id": n.nodeID}
	if r.block != nil {
		path, payload["block"] = "/p2p/block", r.block
	} else {
		payload["tx"] = r.tx
	}

	body, err = json.Marshal(payload)
	if err != nil {
		return err
	}

	var ack struct{}

	return n.do(ctx, http.MethodPost, addr+path, body, &ack)
}

func containsItem(items []InvItem, it InvItem) bool {
	return slices.Contains(items, it)
}
