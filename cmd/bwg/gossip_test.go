package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/p2p"
	"github.com/petersonsalme/blockchain-with-go/internal/server"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

const gossipDeadline = 10 * time.Second

// gossipNet is three real nodes in a line A <- B <- C: B has A as its seed, C has B.
type gossipNet struct {
	a, b, c *gossipNode
	owner   *wallet.Wallet // owns the genesis outputs
	txid    string         // ID of the genesis transaction
}

// gossipNode is a real node whose inbound p2p requests are recorded.
type gossipNode struct {
	p2p *p2pNode
	svc *chain.Service

	mu   sync.Mutex
	invs []string // hashes of the items of every POST /p2p/inv received
}

func (g *gossipNode) url() string { return g.p2p.srv.URL }

func (g *gossipNode) invHashes() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	return slices.Clone(g.invs)
}

func (g *gossipNode) record(r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/p2p/inv" {
		return
	}

	raw, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(raw))

	var inv struct {
		Items []p2p.InvItem `json:"items"`
	}

	_ = json.Unmarshal(raw, &inv)

	g.mu.Lock()
	defer g.mu.Unlock()

	for _, it := range inv.Items {
		g.invs = append(g.invs, it.Hash)
	}
}

// startGossipNode starts a node over a repository that holds genesis (or a fresh funded one when nil).
func startGossipNode(t *testing.T, ctx context.Context, genesis *domain.Block, allocs []domain.TxOut, seed string) *gossipNode {
	t.Helper()
	t.Setenv("POW_DIFFICULTY", "1")
	t.Setenv(seedPeersEnvVar, seed)
	// The first round can sync a block that gossip is also delivering (the gossip copy then answers
	// not_on_tip and is not relayed, ADR-0019); a short interval lets the next round catch the rest up.
	t.Setenv(peerIntervalEnvVar, "200ms")

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

	svc, err := chain.NewService(repo, pow.NewProofOfWorkFromEnv(), chain.WithGenesisAllocations(allocs))
	if err != nil {
		t.Fatal(err)
	}

	g := &gossipNode{svc: svc, p2p: &p2pNode{}}

	var handler http.Handler

	g.p2p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.record(r)
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(g.p2p.srv.Close)
	t.Setenv(p2pListenAddrEnvVar, g.url())

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
	g.p2p.node = node

	stop := startDialer(ctx, node)
	t.Cleanup(stop)

	return g
}

// startGossipNet starts A, then B (seed A), then C (seed B) and waits until the line is connected.
func startGossipNet(t *testing.T) *gossipNet {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	owner, err := wallet.New()
	if err != nil {
		t.Fatal(err)
	}

	allocs := []domain.TxOut{{Amount: 100, Address: owner.Address()}, {Amount: 100, Address: owner.Address()}, {Amount: 100, Address: owner.Address()}}
	a := startGossipNode(t, ctx, nil, allocs, "")

	genesis, err := a.svc.BlockByHeight(0)
	if err != nil {
		t.Fatal(err)
	}

	b := startGossipNode(t, ctx, genesis, allocs, a.url())
	c := startGossipNode(t, ctx, genesis, allocs, b.url())

	waitFor(t, "the line A <- B <- C is connected", func() bool {
		return len(getPeers(t, a.url()).Peers) == 1 && len(getPeers(t, b.url()).Peers) == 2 && len(getPeers(t, c.url()).Peers) == 1
	})

	return &gossipNet{a: a, b: b, c: c, owner: owner, txid: genesis.Transactions[0].CalculateHash()}
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(gossipDeadline)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(10 * time.Millisecond)
	}
}

// spend returns a transaction signed by the owner that spends genesis output idx.
func (n *gossipNet) spend(t *testing.T, idx uint32) domain.Transaction {
	t.Helper()

	tx := domain.Transaction{
		Inputs:    []domain.TxIn{{PrevTxID: n.txid, OutIndex: idx}},
		Outputs:   []domain.TxOut{{Amount: 90, Address: n.owner.Address()}},
		Timestamp: time.Now(),
	}
	tx.ID = tx.CalculateHash()

	err := domain.SignInput(&tx, 0, n.owner)
	if err != nil {
		t.Fatal(err)
	}

	return tx
}

func send(t *testing.T, method, url string, body any) (int, []byte) {
	t.Helper()

	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(context.Background(), method, url, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = resp.Body.Close() }() // test response body

	out, _ := io.ReadAll(resp.Body)

	return resp.StatusCode, out
}

func mine(t *testing.T, g *gossipNode) domain.Block {
	t.Helper()

	status, out := send(t, http.MethodPost, g.url()+"/mine", struct{}{})
	if status != http.StatusCreated {
		t.Fatalf("POST /mine = %d %s", status, out)
	}

	var b domain.Block

	err := json.Unmarshal(out, &b)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func submit(t *testing.T, g *gossipNode, tx domain.Transaction) {
	t.Helper()

	status, out := send(t, http.MethodPost, g.url()+"/tx", tx)
	if status != http.StatusAccepted {
		t.Fatalf("POST /tx = %d %s", status, out)
	}
}

func tipHash(t *testing.T, g *gossipNode) string {
	t.Helper()

	status, out := getBody(t, g.url()+"/blocks")
	if status != http.StatusOK {
		t.Fatalf("GET /blocks = %d %s", status, out)
	}

	var page struct {
		Blocks []domain.Block `json:"blocks"`
	}

	err := json.Unmarshal(out, &page)
	if err != nil || len(page.Blocks) == 0 {
		t.Fatalf("GET /blocks body %s (%v)", out, err)
	}

	return page.Blocks[len(page.Blocks)-1].Hash
}

func getBody(t *testing.T, url string) (int, []byte) {
	t.Helper()

	resp, err := getURL(url)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = resp.Body.Close() }() // test response body

	out, _ := io.ReadAll(resp.Body)

	return resp.StatusCode, out
}

func mempoolIDs(t *testing.T, g *gossipNode) []string {
	t.Helper()

	_, out := getBody(t, g.url()+"/mempool")

	var pool struct {
		Transactions []domain.Transaction `json:"transactions"`
	}

	err := json.Unmarshal(out, &pool)
	if err != nil {
		t.Fatal(err)
	}

	ids := make([]string, len(pool.Transactions))
	for i, tx := range pool.Transactions {
		ids[i] = tx.ID
	}

	return ids
}

func waitForTx(t *testing.T, g *gossipNode, id string) {
	t.Helper()
	waitFor(t, "tx "+id[:8]+" in the mempool of "+g.url(), func() bool { return slices.Contains(mempoolIDs(t, g), id) })
}

func TestMinedBlockReachesTheWholeLine(t *testing.T) {
	n := startGossipNet(t)

	mined := mine(t, n.a)

	for _, g := range []*gossipNode{n.b, n.c} {
		waitFor(t, "the block of A at "+g.url(), func() bool { return tipHash(t, g) == mined.Hash })
	}
}

func TestSubmittedTxReachesTheWholeLine(t *testing.T) {
	n := startGossipNet(t)
	tx := n.spend(t, 0)

	submit(t, n.a, tx)

	waitForTx(t, n.b, tx.ID)
	waitForTx(t, n.c, tx.ID)
}

func TestAnnouncementIsNotEchoedToTheOrigin(t *testing.T) {
	n := startGossipNet(t)
	mined := mine(t, n.a)
	probe := n.spend(t, 0)

	submit(t, n.a, probe)
	// B relays in order: once the probe reached C, B is done with the block.
	waitForTx(t, n.c, probe.ID)
	waitFor(t, "the block at C", func() bool { return tipHash(t, n.c) == mined.Hash })

	if slices.Contains(n.a.invHashes(), mined.Hash) {
		t.Fatalf("A received an inv of its own block %s", mined.Hash)
	}

	if slices.Contains(n.a.invHashes(), probe.ID) {
		t.Fatalf("A received an inv of its own tx %s", probe.ID)
	}
}

func TestInvalidTxIsNeitherAcceptedNorRelayed(t *testing.T) {
	n := startGossipNet(t)

	// B knows A by the id it announced at the handshake.
	var aID string

	for _, p := range getPeers(t, n.b.url()).Peers {
		if p.Addr == n.a.url() {
			aID = p.NodeID
		}
	}

	if aID == "" {
		t.Fatal("B has no peer A")
	}

	bad := domain.Transaction{
		Inputs:    []domain.TxIn{{PrevTxID: strings.Repeat("c", 64), OutIndex: 0}},
		Outputs:   []domain.TxOut{{Amount: 1, Address: n.owner.Address()}},
		Timestamp: time.Now(),
	}
	bad.ID = bad.CalculateHash()

	status, out := send(t, http.MethodPost, n.b.url()+"/p2p/tx", map[string]any{"node_id": aID, "tx": bad})
	if status != http.StatusUnprocessableEntity || !strings.Contains(string(out), "invalid_tx") {
		t.Fatalf("POST /p2p/tx = %d %s, want 422 invalid_tx", status, out)
	}

	// B's relay queue is in order: once the probe reached C, the bad tx would have too.
	probe := n.spend(t, 0)

	submit(t, n.a, probe)
	waitForTx(t, n.c, probe.ID)

	if slices.Contains(n.c.invHashes(), bad.ID) {
		t.Fatalf("C received an inv of the invalid tx %s", bad.ID)
	}

	if slices.Contains(mempoolIDs(t, n.b), bad.ID) {
		t.Fatal("B pooled the invalid tx")
	}
}

func TestInvalidBlockIsNeitherAcceptedNorRelayed(t *testing.T) {
	n := startGossipNet(t)

	var aID string

	for _, p := range getPeers(t, n.b.url()).Peers {
		if p.Addr == n.a.url() {
			aID = p.NodeID
		}
	}

	tip, err := n.b.svc.Tip()
	if err != nil {
		t.Fatal(err)
	}

	bad := domain.Block{Index: tip.Index + 1, PrevHash: tip.Hash, Timestamp: time.Now(), Hash: strings.Repeat("d", 64)}

	status, out := send(t, http.MethodPost, n.b.url()+"/p2p/block", map[string]any{"node_id": aID, "block": bad})
	if status != http.StatusUnprocessableEntity || !strings.Contains(string(out), "invalid_block") {
		t.Fatalf("POST /p2p/block = %d %s, want 422 invalid_block", status, out)
	}

	probe := n.spend(t, 0)

	submit(t, n.a, probe)
	waitForTx(t, n.c, probe.ID)

	if slices.Contains(n.c.invHashes(), bad.Hash) {
		t.Fatalf("C received an inv of the invalid block %s", bad.Hash)
	}
}

func TestChainLedger(t *testing.T) {
	svc := newTestService(t, 1, nil)
	l := chainLedger{svc}
	ctx := context.Background()

	tip, _ := svc.Tip()
	if !l.HasBlock(tip.Hash) || l.HasBlock(strings.Repeat("e", 64)) {
		t.Fatal("HasBlock does not reflect the chain")
	}

	err := l.AcceptBlock(ctx, &domain.Block{Index: tip.Index + 5, PrevHash: strings.Repeat("f", 64)})
	if !errors.Is(err, p2p.ErrNotOnTip) || !errors.Is(err, chain.ErrNotOnTip) {
		t.Fatalf("AcceptBlock off the tip: %v, want both ErrNotOnTip", err)
	}

	err = l.AcceptBlock(ctx, &domain.Block{Index: tip.Index + 1, PrevHash: tip.Hash})
	if err == nil || errors.Is(err, p2p.ErrNotOnTip) {
		t.Fatalf("AcceptBlock of an unsealed block: %v, want a validation error", err)
	}

	bad := domain.Transaction{Inputs: []domain.TxIn{{PrevTxID: strings.Repeat("c", 64)}}, Timestamp: time.Now()}
	bad.ID = bad.CalculateHash()

	err = l.SubmitTx(bad)
	if err == nil {
		t.Fatal("SubmitTx admitted a transaction that spends nothing")
	}

	if l.HasTx(bad.ID) {
		t.Fatal("HasTx reports a rejected transaction")
	}
}

// eventSink collects the events of an open GET /events stream.
type eventSink struct {
	mu     sync.Mutex
	events []sinkEvent
}

type sinkEvent struct{ name, data string }

// openEvents opens GET /events at url; the node has registered the subscriber when it returns.
func openEvents(t *testing.T, url string) *eventSink {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = resp.Body.Close() }) // test response body

	sink := &eventSink{}

	go func() {
		sc := bufio.NewScanner(resp.Body)

		var cur sinkEvent

		for sc.Scan() {
			line := sc.Text()

			switch {
			case strings.HasPrefix(line, "event: "):
				cur.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				cur.data = strings.TrimPrefix(line, "data: ")
			case line == "":
				sink.mu.Lock()
				sink.events = append(sink.events, cur)
				sink.mu.Unlock()

				cur = sinkEvent{}
			}
		}
	}()

	return sink
}

// blockHashes returns the Hash of every block event received so far, in arrival order.
func (s *eventSink) blockHashes(t *testing.T) []string {
	t.Helper()

	s.mu.Lock()
	defer s.mu.Unlock()

	var hashes []string

	for _, ev := range s.events {
		if ev.name != "block" {
			continue
		}

		var b domain.Block

		err := json.Unmarshal([]byte(ev.data), &b)
		if err != nil {
			t.Fatalf("block event data %q: %v", ev.data, err)
		}

		hashes = append(hashes, b.Hash)
	}

	return hashes
}

// settle gives a stray event time to arrive before a test asserts there is none.
func settle() { time.Sleep(300 * time.Millisecond) }

func TestGossipedBlockEmitsOneEventOnThePeer(t *testing.T) {
	n := startGossipNet(t)
	sink := openEvents(t, n.b.url())

	mined := mine(t, n.a)

	waitFor(t, "the block event at B", func() bool { return len(sink.blockHashes(t)) > 0 })
	settle()

	if got := sink.blockHashes(t); !slices.Equal(got, []string{mined.Hash}) {
		t.Fatalf("block events at B = %v, want [%s]", got, mined.Hash)
	}

	status, out := send(t, http.MethodPost, n.b.url()+"/p2p/block", map[string]any{"node_id": getPeers(t, n.b.url()).Peers[0].NodeID, "block": mined})
	if status != http.StatusOK || !strings.Contains(string(out), "known") {
		t.Fatalf("POST /p2p/block again = %d %s, want 200 known", status, out)
	}

	settle()

	if got := sink.blockHashes(t); len(got) != 1 {
		t.Fatalf("block events at B after a duplicate = %v, want one", got)
	}
}
