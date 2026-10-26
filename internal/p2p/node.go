package p2p

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// ChainView is the read-only view of the local chain the handshake needs.
type ChainView interface {
	Tip() (*domain.Block, error)
	BlockByHeight(h int) (*domain.Block, error)
}

// Option customises a Node.
type Option func(*Node)

// WithProtocolVersion overrides the protocol version; for tests of version
// incompatibility only.
func WithProtocolVersion(v int) Option { return func(n *Node) { n.version = v } }

// WithClient sets the HTTP client used to dial peers; nil keeps the default (5s timeout, no redirects).
func WithClient(c *http.Client) Option {
	return func(n *Node) {
		if c != nil {
			n.client = c
		}
	}
}

// WithInterval sets the time between dialer rounds; a value <= 0 keeps the default (30s).
func WithInterval(d time.Duration) Option {
	return func(n *Node) {
		if d > 0 {
			n.interval = d
		}
	}
}

// WithAddrFile makes every round save the known addresses to path; empty means no persistence.
func WithAddrFile(path string) Option { return func(n *Node) { n.addrFile = path } }

// WithLogger sets the logger of the dialer; nil keeps the default, which discards.
func WithLogger(l *slog.Logger) Option {
	return func(n *Node) {
		if l != nil {
			n.log = l
		}
	}
}

// Node answers handshakes on behalf of the local chain.
type Node struct {
	chain      ChainView
	book       *Book
	nodeID     string
	listenAddr string
	version    int

	client   *http.Client
	interval time.Duration
	addrFile string
	log      *slog.Logger
	now      func() time.Time
}

// NewNode validates nodeID and a non-empty listenAddr and builds a Node.
func NewNode(c ChainView, b *Book, nodeID, listenAddr string, opts ...Option) (*Node, error) {
	if !isHex(nodeID, 32) {
		return nil, ErrBadHello
	}

	if listenAddr != "" {
		a, err := NormalizeAddr(listenAddr)
		if err != nil {
			return nil, err
		}

		listenAddr = a
	}

	n := &Node{
		chain: c, book: b, nodeID: nodeID, listenAddr: listenAddr, version: ProtocolVersion,
		client: newClient(), interval: 30 * time.Second, log: slog.New(slog.DiscardHandler), now: time.Now,
	}
	for _, o := range opts {
		o(n)
	}

	return n, nil
}

// newClient returns the default client: a 5s timeout and no redirects, so a peer cannot bounce a dial elsewhere.
func newClient() *http.Client {
	return &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Hello builds the local hello from the current chain.
func (n *Node) Hello() (Hello, error) {
	genesis, err := n.chain.BlockByHeight(0)
	if err != nil {
		return Hello{}, err
	}

	tip, err := n.chain.Tip()
	if err != nil {
		return Hello{}, err
	}

	return Hello{
		Version:     n.version,
		NodeID:      n.nodeID,
		GenesisHash: blockHash(genesis),
		Height:      tip.Index,
		TipHash:     blockHash(tip),
		ListenAddr:  n.listenAddr,
	}, nil
}

// blockHash is the hash a block is announced by. The genesis block is never
// sealed, so its stored Hash may be empty: it is announced by the hash of what
// it commits to.
func blockHash(b *domain.Block) string {
	if b.Hash == "" {
		return b.CalculateHash()
	}

	return b.Hash
}

// Accept checks an inbound hello and, when it passes, registers the sender as
// an inbound peer. It returns the local hello.
func (n *Node) Accept(remote Hello) (Hello, error) {
	local, err := n.Hello()
	if err != nil {
		return Hello{}, err
	}

	err = Check(local, remote)
	if err != nil {
		return Hello{}, err
	}

	err = n.book.Connect(remote, true)
	if err != nil {
		return Hello{}, err
	}

	return local, nil
}

// Peers returns the connected peers.
func (n *Node) Peers() []PeerInfo { return n.book.Peers() }

// Addrs returns the known addresses.
func (n *Node) Addrs() []string { return n.book.Addrs() }
