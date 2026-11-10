package indexer_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/petersonsalme/blockchain-with-go/client"
	"github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/indexer"
	"github.com/petersonsalme/blockchain-with-go/internal/server"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

func stub(t *testing.T, status int, body string) *indexer.NodeClient {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c, err := indexer.NewNodeClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	return c
}

func TestNodeClientMapsAnswers(t *testing.T) {
	ctx := context.Background()

	c := stub(t, http.StatusGone, `{"error":"pruned","code":"block_pruned"}`)

	_, err := c.Blocks(ctx, 0, 10)
	if !errors.Is(err, indexer.ErrPruned) {
		t.Fatalf("410 block_pruned: %v", err)
	}

	// A 410 with another code is not the pruned case.
	c = stub(t, http.StatusGone, `{"error":"endpoint removed","code":"endpoint_gone"}`)

	_, err = c.Blocks(ctx, 0, 10)
	if err == nil || errors.Is(err, indexer.ErrPruned) || !strings.Contains(err.Error(), "endpoint removed") {
		t.Fatalf("410 other: %v", err)
	}

	c = stub(t, http.StatusInternalServerError, `{"error":"boom"}`)

	_, err = c.Blocks(ctx, 0, 10)
	if err == nil || !strings.Contains(err.Error(), "GET /blocks") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("500: %v", err)
	}

	_, err = c.Headers(ctx, 0, 10)
	if err == nil || !strings.Contains(err.Error(), "GET /headers") {
		t.Fatalf("headers 500: %v", err)
	}

	c = stub(t, http.StatusServiceUnavailable, ``)

	_, err = c.Blocks(ctx, 0, 10)
	if err == nil || !strings.Contains(err.Error(), "Service Unavailable") {
		t.Fatalf("503 without a body: %v", err)
	}

	c = stub(t, http.StatusInternalServerError, `not json`)

	_, err = c.Blocks(ctx, 0, 10)
	if err == nil {
		t.Fatal("malformed body accepted")
	}

	c = stub(t, http.StatusOK, `{"blocks":[{"Index":3,"Hash":"h","PrevHash":"p","Transactions":[{"Inputs":[],"Outputs":[],"Timestamp":"2026-01-01T00:00:00Z"}]}],"from":3,"limit":1}`)

	_, err = c.Blocks(ctx, 3, 1)
	if err == nil || !strings.Contains(err.Error(), "without ID") {
		t.Fatalf("tx without ID: %v", err)
	}

	dead, err := indexer.NewNodeClient("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}

	_, err = dead.Blocks(ctx, 0, 1)
	if err == nil {
		t.Fatal("unreachable node: no error")
	}

	_, err = dead.Headers(ctx, 0, 1)
	if err == nil {
		t.Fatal("unreachable node: no error")
	}

	boom := errors.New("boom")

	_, err = indexer.NewNodeClient("http://x", func(*client.Client) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("option error: %v", err)
	}
}

func TestNodeClientReadsARealNode(t *testing.T) {
	owner, to := mustWallet(t), mustWallet(t)

	svc, err := chain.NewService(memory.New(), pow.New(1),
		chain.WithGenesisAllocations([]domain.TxOut{{Amount: 50, Address: owner.Address()}}),
		chain.WithMinerAddress(to.Address()))
	if err != nil {
		t.Fatal(err)
	}

	for range 3 {
		_, err := svc.MinePending(t.Context())
		if err != nil {
			t.Fatal(err)
		}
	}

	s, err := server.NewServer(svc, server.Config{})
	if err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	c, err := indexer.NewNodeClient(ts.URL)
	if err != nil {
		t.Fatal(err)
	}

	want, _ := svc.Blocks()
	genesisID := want[0].CalculateHash()

	for _, from := range []int{0, 1, 2} { // pages that start at the genesis, at block 1 and later
		blocks, err := c.Blocks(t.Context(), from, 100)
		if err != nil {
			t.Fatal(err)
		}

		if len(blocks) != len(want)-from {
			t.Fatalf("from %d: %d blocks", from, len(blocks))
		}

		for i, b := range blocks {
			h := from + i
			if b.Height != h || len(b.Txs) != len(want[h].Transactions) {
				t.Fatalf("height %d: %+v", h, b)
			}

			switch h {
			case 0:
				if b.Hash != genesisID || len(b.Txs) != 1 || b.Txs[0].Outputs[0].Amount != 50 || len(b.Txs[0].Inputs) != 0 {
					t.Fatalf("genesis %+v, want hash %s", b, genesisID)
				}
			case 1:
				if b.PrevHash != genesisID || b.Hash != want[1].Hash {
					t.Fatalf("block 1 %+v", b)
				}
			default:
				if b.Hash != want[h].Hash || b.PrevHash != want[h].PrevHash {
					t.Fatalf("block %d %+v", h, b)
				}
			}
		}
	}

	// A mined block spends nothing but carries a coinbase; check a spend converts its inputs.
	hs, err := c.Headers(t.Context(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}

	if len(hs) != 4 || hs[0].Hash != genesisID || hs[3].Hash != want[3].Hash {
		t.Fatalf("headers %+v", hs)
	}

	hs, err = c.Headers(t.Context(), 2, 1)
	if err != nil || len(hs) != 1 || hs[0].Height != 2 {
		t.Fatalf("headers page %+v %v", hs, err)
	}
}

func mustWallet(t *testing.T) *wallet.Wallet {
	t.Helper()

	w, err := wallet.New()
	if err != nil {
		t.Fatal(err)
	}

	return w
}

func TestNodeClientNamesAnEmptyGenesisAndConvertsSpends(t *testing.T) {
	owner, to := mustWallet(t), mustWallet(t)

	svc, err := chain.NewService(memory.New(), pow.New(1), chain.WithGenesisAllocations([]domain.TxOut{{Amount: 50, Address: owner.Address()}}))
	if err != nil {
		t.Fatal(err)
	}

	_, utxos := svc.Balance(owner.Address())
	tx := domain.Transaction{
		Inputs:  []domain.TxIn{{PrevTxID: utxos[0].TxID, OutIndex: utxos[0].Index}},
		Outputs: []domain.TxOut{{Amount: 45, Address: to.Address()}},
	}
	tx.ID = tx.CalculateHash()

	err = domain.SignInput(&tx, 0, owner)
	if err != nil {
		t.Fatal(err)
	}

	err = svc.Submit(tx)
	if err != nil {
		t.Fatal(err)
	}

	_, err = svc.MinePending(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	s, _ := server.NewServer(svc, server.Config{})

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	c, _ := indexer.NewNodeClient(ts.URL)

	blocks, err := c.Blocks(t.Context(), 1, 10)
	if err != nil || len(blocks) != 1 || len(blocks[0].Txs) != 1 {
		t.Fatalf("%+v %v", blocks, err)
	}

	in := blocks[0].Txs[0].Inputs
	if len(in) != 1 || in[0].TxID != utxos[0].TxID || in[0].Index != utxos[0].Index || blocks[0].Txs[0].Outputs[0].Amount != 45 {
		t.Fatalf("converted spend %+v", blocks[0].Txs[0])
	}

	// An empty genesis (no allocation transaction) is still named by what it commits to.
	empty, err := chain.NewService(memory.New(), pow.New(1))
	if err != nil {
		t.Fatal(err)
	}

	es, _ := server.NewServer(empty, server.Config{})

	ets := httptest.NewServer(es.Handler())
	defer ets.Close()

	ec, _ := indexer.NewNodeClient(ets.URL)

	blocks, err = ec.Blocks(t.Context(), 0, 1)
	g, _ := empty.BlockByHeight(0)

	if err != nil || len(blocks) != 1 || blocks[0].Hash != g.CalculateHash() {
		t.Fatalf("%+v %v", blocks, err)
	}
}
