package p2p

import "github.com/petersonsalme/blockchain-with-go/internal/domain"

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

// Node answers handshakes on behalf of the local chain.
type Node struct {
	chain      ChainView
	book       *Book
	nodeID     string
	listenAddr string
	version    int
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
	n := &Node{chain: c, book: b, nodeID: nodeID, listenAddr: listenAddr, version: ProtocolVersion}
	for _, o := range opts {
		o(n)
	}
	return n, nil
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
		GenesisHash: genesis.Hash,
		Height:      tip.Index,
		TipHash:     tip.Hash,
		ListenAddr:  n.listenAddr,
	}, nil
}

// Accept checks an inbound hello and, when it passes, registers the sender as
// an inbound peer. It returns the local hello.
func (n *Node) Accept(remote Hello) (Hello, error) {
	local, err := n.Hello()
	if err != nil {
		return Hello{}, err
	}
	if err := Check(local, remote); err != nil {
		return Hello{}, err
	}
	if err := n.book.Connect(remote, true); err != nil {
		return Hello{}, err
	}
	return local, nil
}

// Peers returns the connected peers.
func (n *Node) Peers() []PeerInfo { return n.book.Peers() }

// Addrs returns the known addresses.
func (n *Node) Addrs() []string { return n.book.Addrs() }
