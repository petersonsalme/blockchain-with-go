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

	events := server.NewEvents()

	node, err := newP2PNode(svc, "", discardLogger(), events)
	if err != nil {
		t.Fatal(err)
	}

	s, err := server.NewServer(svc, server.Config{P2P: node, Events: events})
	if err != nil {
		t.Fatal(err)
	}

	handler = s.Handler()
	n.node.p2p.node = node

	stop := startDialer(ctx, node)
	t.Cleanup(stop)

	return n
}

func (s *syncNode) mineBlocks(ctx context.Context, t *testing.T, count int) {
	t.Helper()

	for range count {
		_, err := s.svc.MinePending(ctx)
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
	a.mineBlocks(ctx, t, 40)

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

	a.mineBlocks(ctx, t, blocksA)
	b.mineBlocks(ctx, t, blocksB)

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

// chainHashes returns the hashes of the blocks of s from height from to its tip.
func (s *syncNode) chainHashes(t *testing.T, from int) []string {
	t.Helper()

	tip := s.tip(t)

	var hashes []string

	for h := from; h <= tip.Index; h++ {
		b, err := s.svc.BlockByHeight(h)
		if err != nil {
			t.Fatal(err)
		}

		hashes = append(hashes, b.Hash)
	}

	return hashes
}

func TestSyncExtensionEmitsOneEventPerBlockInOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	a := startSyncNode(t, ctx, nil, nil, "", true)
	a.mineBlocks(ctx, t, 5)

	c := startSyncNode(t, ctx, genesisOf(t, a), nil, a.url(), false) // A is closed
	sink := openEvents(t, c.url())

	a.closed.Store(false)

	want := a.chainHashes(t, 1)
	waitFor(t, "C at the tip of A", func() bool { return c.tip(t).Hash == want[len(want)-1] })
	settle()

	if got := sink.blockHashes(t); !slices.Equal(got, want) {
		t.Fatalf("block events at C = %v, want %v", got, want)
	}
}

func TestReorgEmitsTheConnectedBlocksOldestFirst(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	p := startForkedPair(t, ctx, 3, 2) // B dials A and loses
	sink := openEvents(t, p.b.url())

	p.a.closed.Store(false)

	want := p.a.chainHashes(t, 1)
	waitFor(t, "B on the tip of A", func() bool { return p.b.tip(t).Hash == want[len(want)-1] })
	settle()

	if got := sink.blockHashes(t); !slices.Equal(got, want) {
		t.Fatalf("block events at B = %v, want the connected %v", got, want)
	}
}

func TestSyncWithoutSwitchEmitsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	p := startForkedPair(t, ctx, 2, 2)
	p.a.closed.Store(false)
	waitFor(t, "A and B connected", func() bool {
		return len(getPeers(t, p.a.url()).Peers) == 1 && len(getPeers(t, p.b.url()).Peers) == 1
	})

	sinkA, sinkB := openEvents(t, p.a.url()), openEvents(t, p.b.url())

	for _, n := range []*syncNode{p.a, p.b} {
		n.node.p2p.node.Sync(ctx) // equal work: ErrInsufficientWork
	}

	settle()

	if a, b := sinkA.blockHashes(t), sinkB.blockHashes(t); len(a)+len(b) != 0 {
		t.Fatalf("events after a refused sync: A %v, B %v", a, b)
	}
}

// ledgerWithSink is a syncLedger over a fresh chain whose events are served at url.
func ledgerWithSink(t *testing.T, genesis *domain.Block) (syncLedger, *eventSink) {
	t.Helper()

	svc := newTestService(t, 0, genesis)
	events := server.NewEvents()

	srv, err := server.NewServer(svc, server.Config{Events: events})
	if err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	return syncLedger{chainLedger: chainLedger{svc}, events: events}, openEvents(t, ts.URL)
}

func TestSyncLedgerPublishesOnlyWhatItAppended(t *testing.T) {
	src := newTestService(t, 2, nil)

	genesis, err := src.BlockByHeight(0)
	if err != nil {
		t.Fatal(err)
	}

	b1, _ := src.BlockByHeight(1)
	b2, _ := src.BlockByHeight(2)
	ctx := context.Background()

	l, sink := ledgerWithSink(t, genesis)

	err = l.AcceptBlock(ctx, b2) // does not extend the tip
	if err == nil {
		t.Fatal("AcceptBlock of a block that skips one succeeded")
	}

	settle()

	if got := sink.blockHashes(t); len(got) != 0 {
		t.Fatalf("events after a failed AcceptBlock = %v", got)
	}

	err = l.AcceptBlock(ctx, b1)
	if err != nil {
		t.Fatal(err)
	}

	waitFor(t, "the event of b1", func() bool { return len(sink.blockHashes(t)) == 1 })

	if got := sink.blockHashes(t); got[0] != b1.Hash {
		t.Fatalf("event = %s, want %s", got[0], b1.Hash)
	}

	// A candidate with no more work than the chain is refused and publishes nothing.
	err = l.ReplaceChain(ctx, []*domain.Block{genesis, b1})
	if err == nil {
		t.Fatal("ReplaceChain with equal work succeeded")
	}

	settle()

	if got := sink.blockHashes(t); len(got) != 1 {
		t.Fatalf("events after a failed ReplaceChain = %v", got)
	}
}

// TestRunServesSyncedBlocksOnEvents checks that run hands one event stream to both the sync ledger
// and the server: a block a node syncs from a peer reaches its served GET /events.
func TestRunServesSyncedBlocksOnEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	a := startSyncNode(t, ctx, nil, nil, "", true)
	a.mineBlocks(ctx, t, 3)

	port := freePort(t)
	t.Setenv("PORT", port)
	t.Setenv("POW_DIFFICULTY", "1")
	t.Setenv("MINING_TIMEOUT", "5s")
	t.Setenv(genesisTimeEnvVar, genesisOf(t, a).Timestamp.Format(time.RFC3339Nano))
	t.Setenv(seedPeersEnvVar, a.url())
	t.Setenv(p2pListenAddrEnvVar, "http://127.0.0.1:"+port)
	t.Setenv(peerIntervalEnvVar, syncRound)

	done := make(chan error, 1)
	go func() { done <- run(ctx, discardLogger()) }()

	t.Cleanup(func() {
		cancel()

		err := <-done
		if err != nil {
			t.Errorf("run returned %v after shutdown", err)
		}
	})

	url := "http://127.0.0.1:" + port

	waitFor(t, "the node up", func() bool {
		resp, err := getURL(url + "/health")
		if err != nil {
			return false
		}

		_ = resp.Body.Close() // test response body

		return true
	})

	sink := openEvents(t, url)

	a.closed.Store(false)

	want := a.chainHashes(t, 1)
	waitFor(t, "the synced events at the node", func() bool { return len(sink.blockHashes(t)) >= len(want) })
	settle()

	if got := sink.blockHashes(t); !slices.Equal(got, want) {
		t.Fatalf("block events at the node = %v, want %v", got, want)
	}
}
