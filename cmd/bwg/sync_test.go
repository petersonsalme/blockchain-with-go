package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/server"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

// syncRound is the dialer interval of the sync tests: short, so a round follows the connection quickly.
const syncRound = "100ms"

// syncNode is a real node whose HTTP port can be closed to keep it isolated.
type syncNode struct {
	svc    *chain.Service
	node   *gossipNode
	closed atomic.Bool
	miner  string
}

func (s *syncNode) url() string { return s.node.url() }

// startSyncNode starts a node over genesis (a fresh funded one when nil) that pays its coinbase to a
// new address. A closed node answers 503 to every request; seed may be the URL of a closed node.
func startSyncNode(t *testing.T, ctx context.Context, genesis *domain.Block, allocs []domain.TxOut, seed string, closed bool) *syncNode {
	t.Helper()

	miner, err := wallet.New()
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("POW_DIFFICULTY", "1")
	t.Setenv(seedPeersEnvVar, seed)
	t.Setenv(peerIntervalEnvVar, syncRound)

	repo := memory.New()
	if genesis != nil {
		delta, err := domain.BlockDelta(repo, genesis, true)
		if err != nil {
			t.Fatal(err)
		}

		err = repo.Commit(genesis, delta)
		if err != nil {
			t.Fatal(err)
		}
	}

	svc, err := chain.NewService(repo, pow.NewProofOfWorkFromEnv(), chain.WithGenesisAllocations(allocs), chain.WithMinerAddress(miner.Address()))
	if err != nil {
		t.Fatal(err)
	}

	n := &syncNode{svc: svc, miner: miner.Address(), node: &gossipNode{svc: svc, p2p: &p2pNode{}}}
	n.closed.Store(closed)

	var handler http.Handler

	n.node.p2p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.closed.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}

		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(n.node.p2p.srv.Close)
	t.Setenv(p2pListenAddrEnvVar, n.url())

	node, err := newP2PNode(svc, "", discardLogger())
	if err != nil {
		t.Fatal(err)
	}

	s, err := server.NewServer(svc, server.Config{P2P: node})
	if err != nil {
		t.Fatal(err)
	}

	handler = s.Handler()
	n.node.p2p.node = node

	stop := startDialer(ctx, node)
	t.Cleanup(stop)

	return n
}

func (s *syncNode) mineBlocks(t *testing.T, count int) {
	t.Helper()

	for range count {
		_, err := s.svc.MinePending(context.Background())
		if err != nil {
			t.Fatal(err)
		}
	}
}

func (s *syncNode) tip(t *testing.T) *domain.Block {
	t.Helper()

	tip, err := s.svc.Tip()
	if err != nil {
		t.Fatal(err)
	}

	return tip
}

func genesisOf(t *testing.T, s *syncNode) *domain.Block {
	t.Helper()

	g, err := s.svc.BlockByHeight(0)
	if err != nil {
		t.Fatal(err)
	}

	return g
}

func TestEmptyNodeSyncsToTheTipOfTheNetwork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	a := startSyncNode(t, ctx, nil, nil, "", false)
	a.mineBlocks(t, 40)

	c := startSyncNode(t, ctx, genesisOf(t, a), nil, a.url(), false)

	want := a.tip(t).Hash
	waitFor(t, "C at the tip of A", func() bool { return c.tip(t).Hash == want })

	balanceA, _ := a.svc.Balance(a.miner)
	if balanceC, _ := c.svc.Balance(a.miner); balanceC != balanceA || balanceA == 0 {
		t.Fatalf("balance of the miner at C = %d, at A = %d", balanceC, balanceA)
	}
}

// forkedPair is A and B on the same genesis and isolated from each other; spend is in the blocks of A.
type forkedPair struct {
	a, b  *syncNode
	spend domain.Transaction
}

func startForkedPair(t *testing.T, ctx context.Context, blocksA, blocksB int) *forkedPair {
	t.Helper()

	owner, err := wallet.New()
	if err != nil {
		t.Fatal(err)
	}

	allocs := []domain.TxOut{{Amount: 100, Address: owner.Address()}}
	a := startSyncNode(t, ctx, nil, allocs, "", true)
	genesis := genesisOf(t, a)
	b := startSyncNode(t, ctx, genesis, allocs, a.url(), false) // B dials A, which is closed

	spend := domain.Transaction{
		Inputs:    []domain.TxIn{{PrevTxID: genesis.Transactions[0].CalculateHash(), OutIndex: 0}},
		Outputs:   []domain.TxOut{{Amount: 90, Address: owner.Address()}},
		Timestamp: time.Now(),
	}
	spend.ID = spend.CalculateHash()

	err = domain.SignInput(&spend, 0, owner)
	if err != nil {
		t.Fatal(err)
	}

	err = a.svc.Submit(spend)
	if err != nil {
		t.Fatal(err)
	}

	a.mineBlocks(t, blocksA)
	b.mineBlocks(t, blocksB)

	return &forkedPair{a: a, b: b, spend: spend}
}

func TestForkEndsOnTheBranchWithMoreWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	p := startForkedPair(t, ctx, 2, 3)
	if slices.ContainsFunc(p.a.svc.Pending(), func(tx domain.Transaction) bool { return tx.ID == p.spend.ID }) {
		t.Fatal("the transaction is still in the pool of A before the fork is resolved")
	}

	p.a.closed.Store(false)

	want := p.b.tip(t).Hash
	waitFor(t, "A and B on the tip of B", func() bool { return p.a.tip(t).Hash == want && p.b.tip(t).Hash == want })
	waitFor(t, "the orphaned transaction back in the pool of A", func() bool {
		return slices.ContainsFunc(p.a.svc.Pending(), func(tx domain.Transaction) bool { return tx.ID == p.spend.ID })
	})
}

func TestEqualWorkKeepsTheFirstSeenBranch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	p := startForkedPair(t, ctx, 2, 2)
	tipA, tipB := p.a.tip(t).Hash, p.b.tip(t).Hash

	if tipA == tipB {
		t.Fatal("the branches are the same")
	}

	p.a.closed.Store(false)
	waitFor(t, "A and B connected with the other's height", func() bool {
		return len(getPeers(t, p.a.url()).Peers) == 1 && len(getPeers(t, p.b.url()).Peers) == 1
	})

	// Both nodes run a sync with the other connected: neither switches.
	for _, n := range []*syncNode{p.a, p.b} {
		if n.node.p2p.node.Sync(ctx) {
			t.Fatalf("%s changed its tip with equal work", n.url())
		}
	}

	if p.a.tip(t).Hash != tipA || p.b.tip(t).Hash != tipB {
		t.Fatal("a node switched to a branch with equal work")
	}
}
