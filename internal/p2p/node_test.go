package p2p

import (
	"errors"
	"reflect"
	"testing"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

type fakeChain struct {
	blocks []*domain.Block
	err    error
}

func (f fakeChain) Tip() (*domain.Block, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.blocks[len(f.blocks)-1], nil
}

func (f fakeChain) BlockByHeight(h int) (*domain.Block, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.blocks[h], nil
}

func chain2() fakeChain {
	return fakeChain{blocks: []*domain.Block{
		{Index: 0, Hash: gen},
		{Index: 1, Hash: tip},
	}}
}

func TestNewNodeValidation(t *testing.T) {
	book := NewBook(Limits{}, nil)
	if _, err := NewNode(chain2(), book, "short", ""); !errors.Is(err, ErrBadHello) {
		t.Fatalf("err = %v", err)
	}
	if _, err := NewNode(chain2(), book, idA, "nope"); !errors.Is(err, ErrBadAddr) {
		t.Fatalf("err = %v", err)
	}
	if _, err := NewNode(chain2(), book, idA, ""); err != nil {
		t.Fatal(err)
	}
	n, err := NewNode(chain2(), book, idA, "HTTP://H:80/")
	if err != nil {
		t.Fatal(err)
	}
	h, _ := n.Hello()
	if h.ListenAddr != "http://h:80" {
		t.Fatalf("listen = %q", h.ListenAddr)
	}
}

func TestNodeHello(t *testing.T) {
	n, _ := NewNode(chain2(), NewBook(Limits{}, nil), idA, "http://h:80")
	h, err := n.Hello()
	want := Hello{Version: 1, NodeID: idA, GenesisHash: gen, Height: 1, TipHash: tip, ListenAddr: "http://h:80"}
	if err != nil || h != want {
		t.Fatalf("hello = %+v, %v", h, err)
	}
	n, _ = NewNode(chain2(), NewBook(Limits{}, nil), idA, "", WithProtocolVersion(7))
	if h, _ := n.Hello(); h.Version != 7 {
		t.Fatalf("version = %d", h.Version)
	}
	boom := errors.New("boom")
	n, _ = NewNode(fakeChain{err: boom}, NewBook(Limits{}, nil), idA, "")
	if _, err := n.Hello(); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if _, err := n.Accept(hello(idB)); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

// a chain whose genesis lookup fails but whose tip works
type noGenesis struct{ fakeChain }

func (noGenesis) BlockByHeight(int) (*domain.Block, error) { return nil, errors.New("no genesis") }

func TestNodeHelloGenesisError(t *testing.T) {
	n, _ := NewNode(noGenesis{chain2()}, NewBook(Limits{}, nil), idA, "")
	if _, err := n.Hello(); err == nil {
		t.Fatal("want error")
	}
}

func TestNodeAccept(t *testing.T) {
	book := NewBook(Limits{MaxInbound: 1}, newClock().now)
	n, _ := NewNode(chain2(), book, idA, "http://me:1")

	bad := hello(idB)
	bad.GenesisHash = tip
	if _, err := n.Accept(bad); !errors.Is(err, ErrGenesisMismatch) {
		t.Fatalf("err = %v", err)
	}
	if len(n.Peers()) != 0 || len(n.Addrs()) != 0 {
		t.Fatal("rejected hello changed the book")
	}
	if _, err := n.Accept(hello(idA)); !errors.Is(err, ErrSelf) {
		t.Fatalf("err = %v", err)
	}

	remote := hello(idB)
	remote.Height = 42
	got, err := n.Accept(remote)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 || got.GenesisHash != gen || got.Height != 1 || got.TipHash != tip || got.NodeID != idA {
		t.Fatalf("local hello = %+v", got)
	}
	ps := n.Peers()
	if len(ps) != 1 || !ps[0].Inbound || ps[0].Height != 42 || ps[0].NodeID != idB || ps[0].Addr != remote.ListenAddr {
		t.Fatalf("peers = %+v", ps)
	}
	if !reflect.DeepEqual(n.Addrs(), []string{remote.ListenAddr}) {
		t.Fatalf("addrs = %v", n.Addrs())
	}

	other := hello(nid(9))
	if _, err := n.Accept(other); !errors.Is(err, ErrPeerLimit) {
		t.Fatalf("err = %v", err)
	}
	if len(n.Peers()) != 1 {
		t.Fatal("peer limit let a peer in")
	}
}

// a chain whose tip lookup fails but whose genesis works
type noTip struct{ fakeChain }

func (noTip) Tip() (*domain.Block, error) { return nil, errors.New("no tip") }

func TestNodeHelloTipError(t *testing.T) {
	n, _ := NewNode(noTip{chain2()}, NewBook(Limits{}, nil), idA, "")
	if _, err := n.Hello(); err == nil {
		t.Fatal("want error")
	}
}
