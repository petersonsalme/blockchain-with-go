package p2p

import (
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Limits caps the peer book. A value <= 0 selects the default (16, 8, 1000).
type Limits struct{ MaxInbound, MaxOutbound, MaxAddrs int }

// PeerInfo describes a connected peer.
type PeerInfo struct {
	NodeID, Addr string
	Height       int
	TipHash      string
	Inbound      bool
	LastSeen     time.Time
}

// Book holds the known addresses and the connected peers. It is safe for
// concurrent use.
type Book struct {
	mu     sync.Mutex
	limits Limits
	now    func() time.Time
	addrs  map[string]struct{}
	peers  map[string]*PeerInfo
}

// NewBook returns an empty book; a nil now means time.Now.
func NewBook(l Limits, now func() time.Time) *Book {
	if l.MaxInbound <= 0 {
		l.MaxInbound = 16
	}

	if l.MaxOutbound <= 0 {
		l.MaxOutbound = 8
	}

	if l.MaxAddrs <= 0 {
		l.MaxAddrs = 1000
	}

	if now == nil {
		now = time.Now
	}

	return &Book{limits: l, now: now, addrs: map[string]struct{}{}, peers: map[string]*PeerInfo{}}
}

// AddAddrs normalises and stores addresses, ignoring invalid and repeated
// ones and dropping new ones once the book is full. It returns how many entered.
func (b *Book) AddAddrs(addrs ...string) int {
	b.mu.Lock()
	defer b.mu.Unlock()

	n := 0

	for _, a := range addrs {
		if b.addLocked(a) {
			n++
		}
	}

	return n
}

func (b *Book) addLocked(raw string) bool {
	a, err := NormalizeAddr(raw)
	if err != nil {
		return false
	}

	if _, ok := b.addrs[a]; ok || len(b.addrs) >= b.limits.MaxAddrs {
		return false
	}

	b.addrs[a] = struct{}{}

	return true
}

// Addrs returns the known addresses, sorted.
func (b *Book) Addrs() []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.sortedAddrs(func(string) bool { return true })
}

// Candidates returns the known addresses no connected peer uses, sorted.
func (b *Book) Candidates() []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	used := map[string]bool{}
	for _, p := range b.peers {
		used[p.Addr] = true
	}

	return b.sortedAddrs(func(a string) bool { return !used[a] })
}

func (b *Book) sortedAddrs(keep func(string) bool) []string {
	out := []string{}

	for a := range b.addrs {
		if keep(a) {
			out = append(out, a)
		}
	}

	sort.Strings(out)

	return out
}

// Connect registers the sender of h as a peer. A NodeID that is already
// connected is only refreshed; a new one over the limit of its direction is
// refused with ErrPeerLimit. A refusal leaves the book unchanged.
func (b *Book) Connect(h Hello, inbound bool) error {
	addr := ""

	if h.ListenAddr != "" {
		a, err := NormalizeAddr(h.ListenAddr)
		if err != nil {
			return err
		}

		addr = a
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if p, ok := b.peers[h.NodeID]; ok {
		p.Height, p.TipHash, p.LastSeen = h.Height, h.TipHash, b.now()
		return nil
	}

	count, limit := 0, b.limits.MaxOutbound
	if inbound {
		limit = b.limits.MaxInbound
	}

	for _, p := range b.peers {
		if p.Inbound == inbound {
			count++
		}
	}

	if count >= limit {
		return ErrPeerLimit
	}

	b.peers[h.NodeID] = &PeerInfo{NodeID: h.NodeID, Addr: addr, Height: h.Height, TipHash: h.TipHash, Inbound: inbound, LastSeen: b.now()}
	if addr != "" {
		b.addLocked(addr)
	}

	return nil
}

// Disconnect removes a connected peer; its address stays known.
func (b *Book) Disconnect(nodeID string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	delete(b.peers, nodeID)
}

// Peers returns a copy of the connected peers, ordered by NodeID.
func (b *Book) Peers() []PeerInfo {
	b.mu.Lock()
	defer b.mu.Unlock()

	out := make([]PeerInfo, 0, len(b.peers))
	for _, p := range b.peers {
		out = append(out, *p)
	}

	slices.SortFunc(out, func(a, b PeerInfo) int { return strings.Compare(a.NodeID, b.NodeID) })

	return out
}

// Expire drops connected peers last seen before the cutoff and returns how
// many it dropped. Their addresses stay known.
func (b *Book) Expire(before time.Time) int {
	b.mu.Lock()
	defer b.mu.Unlock()

	n := 0

	for id, p := range b.peers {
		if p.LastSeen.Before(before) {
			delete(b.peers, id)

			n++
		}
	}

	return n
}

// MaxOutbound returns the effective limit of outbound peers.
func (b *Book) MaxOutbound() int { return b.limits.MaxOutbound }
