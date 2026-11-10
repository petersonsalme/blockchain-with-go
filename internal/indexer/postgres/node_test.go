package postgres_test

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/indexer"
	"github.com/petersonsalme/blockchain-with-go/internal/indexer/postgres"
	"github.com/petersonsalme/blockchain-with-go/internal/indexer/postgres/pgtest"
	"github.com/petersonsalme/blockchain-with-go/internal/server"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
	"github.com/petersonsalme/blockchain-with-go/internal/vm"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

// levelEngine seals every block at a fixed difficulty and accepts any difficulty, so a test
// chooses how much work a branch carries independently of its length.
type levelEngine struct{ d int }

func (e levelEngine) Seal(_ context.Context, _ []*domain.Block, b *domain.Block) error {
	b.Difficulty = e.d
	b.Hash = b.CalculateHash()

	return nil
}

func (levelEngine) Verify(history []*domain.Block, b *domain.Block) error {
	prev := history[len(history)-1]

	switch {
	case prev.Index+1 != b.Index:
		return consensus.ErrInvalidIndex
	case prev.Hash != b.PrevHash:
		return consensus.ErrInvalidPrevHash
	case b.CalculateHash() != b.Hash:
		return consensus.ErrInvalidHash
	}

	return nil
}

// env is a real node (chain.Service over the memory repository) behind httptest.
type env struct {
	t   *testing.T
	svc *chain.Service
	srv *httptest.Server
}

func wal(t *testing.T) *wallet.Wallet {
	t.Helper()

	w, err := wallet.New()
	if err != nil {
		t.Fatal(err)
	}

	return w
}

// newEnv starts a node at difficulty d paying its coinbases to miner. A non-nil genesis is
// committed first (a competing branch shares the genesis of the first node).
func newEnv(t *testing.T, genesis *domain.Block, allocs []domain.TxOut, miner string, d int) *env {
	t.Helper()

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

	svc, err := chain.NewService(repo, levelEngine{d}, chain.WithGenesisAllocations(allocs), chain.WithMinerAddress(miner))
	if err != nil {
		t.Fatal(err)
	}

	s, err := server.NewServer(svc, server.Config{})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	return &env{t: t, svc: svc, srv: srv}
}

func (e *env) height() int {
	tip, err := e.svc.Tip()
	if err != nil {
		e.t.Fatal(err)
	}

	return tip.Index
}

func (e *env) blocks() []*domain.Block {
	var out []*domain.Block

	for from := 0; ; from += 100 {
		page, err := e.svc.BlocksRange(from, 100)
		if err != nil {
			e.t.Fatal(err)
		}

		out = append(out, page...)

		if len(page) < 100 {
			return out
		}
	}
}

// mine submits txs and mines one block.
func (e *env) mine(txs ...domain.Transaction) {
	e.t.Helper()

	for _, tx := range txs {
		err := e.svc.Submit(tx)
		if err != nil {
			e.t.Fatal(err)
		}
	}

	_, err := e.svc.MinePending(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
}

// spend builds a transaction from w's first confirmed output that covers amount+fee: amount to
// `to`, the change back to w, the rest is the fee.
func (e *env) spend(w *wallet.Wallet, to string, amount, fee uint64) domain.Transaction {
	e.t.Helper()

	_, utxos := e.svc.Balance(w.Address())

	for _, u := range utxos {
		if u.Amount < amount+fee {
			continue
		}

		tx := domain.Transaction{
			Inputs:    []domain.TxIn{{PrevTxID: u.TxID, OutIndex: u.Index}},
			Outputs:   []domain.TxOut{{Amount: amount, Address: to}},
			Timestamp: time.Now(),
		}

		if change := u.Amount - amount - fee; change > 0 {
			tx.Outputs = append(tx.Outputs, domain.TxOut{Amount: change, Address: w.Address()})
		}

		return e.sign(tx, w)
	}

	e.t.Fatalf("no output of %s covers %d", w.Address(), amount+fee)

	return domain.Transaction{}
}

// contract builds a deploy (target "") or a call of the counter contract, paying fee.
func (e *env) contract(w *wallet.Wallet, target string, fee uint64) domain.Transaction {
	e.t.Helper()

	_, utxos := e.svc.Balance(w.Address())
	u := utxos[0]

	op := &domain.ContractOp{Kind: domain.ContractDeploy, Code: hex.EncodeToString(vm.CounterCode), GasLimit: 100}
	if target != "" {
		op = &domain.ContractOp{Kind: domain.ContractCall, Target: target, GasLimit: 100}
	}

	tx := domain.Transaction{
		Inputs:    []domain.TxIn{{PrevTxID: u.TxID, OutIndex: u.Index}},
		Outputs:   []domain.TxOut{{Amount: u.Amount - fee, Address: w.Address()}},
		Timestamp: time.Now(),
		Contract:  op,
	}

	return e.sign(tx, w)
}

func (e *env) sign(tx domain.Transaction, w *wallet.Wallet) domain.Transaction {
	e.t.Helper()

	tx.ID = tx.CalculateHash()

	for i := range tx.Inputs {
		err := domain.SignInput(&tx, i, w)
		if err != nil {
			e.t.Fatal(err)
		}
	}

	return tx
}

// sync runs the real follower over the node's HTTP API against the database until level.
func (e *env) sync(url string) {
	e.t.Helper()

	s, err := postgres.Open(context.Background(), url)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = s.Close(context.Background()) }()

	nc, err := indexer.NewNodeClient(e.srv.URL)
	if err != nil {
		e.t.Fatal(err)
	}

	err = follower(nc, s).Sync(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
}

// assertMatches checks the database against the node: a row per height with the node's hash,
// balances equal to Service.Balance for every address and no other, and supply at every height
// recomputed by walking the blocks.
func (e *env) assertMatches(url string) {
	e.t.Helper()

	t := e.t
	ctx := context.Background()
	blocks := e.blocks()
	conn := mustConn(t, url)

	rows, err := conn.Query(ctx, "SELECT hash FROM blocks ORDER BY height")
	if err != nil {
		t.Fatal(err)
	}

	hashes, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}

	// The genesis is never sealed: the indexer names it by what it commits to.
	if len(blocks) > 0 && hashes[0] != blocks[0].CalculateHash() {
		t.Fatalf("genesis stored as %q, want %q", hashes[0], blocks[0].CalculateHash())
	}

	if len(hashes) != len(blocks) {
		t.Fatalf("%d block rows, node has %d", len(hashes), len(blocks))
	}

	amounts := map[domain.OutPoint]uint64{}
	addrs := map[string]bool{}
	total := new(bigInt)

	for h, b := range blocks {
		if h > 0 && hashes[h] != b.Hash {
			t.Fatalf("height %d: %s, node has %s", h, hashes[h], b.Hash)
		}

		for _, tx := range b.Transactions {
			id := tx.CalculateHash()

			for _, in := range tx.Inputs {
				total.sub(amounts[domain.OutPoint{TxID: in.PrevTxID, Index: in.OutIndex}])
			}

			for i, o := range tx.Outputs {
				amounts[domain.OutPoint{TxID: id, Index: uint32(i)}] = o.Amount
				addrs[o.Address] = true

				total.add(o.Amount)
			}
		}

		var got string

		err := conn.QueryRow(ctx, "SELECT total::text FROM supply WHERE height = $1", h).Scan(&got)
		if err != nil {
			t.Fatalf("supply at %d: %v", h, err)
		}

		if got != total.String() {
			t.Fatalf("supply at %d: %s, recomputed %s", h, got, total)
		}
	}

	positive := 0

	for a := range addrs {
		want, _ := e.svc.Balance(a)

		var got string

		err := conn.QueryRow(ctx, "SELECT amount::text FROM balances WHERE address = $1", a).Scan(&got)

		switch {
		case want == 0 && errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			t.Fatalf("balance of %s: %v (node %d)", a, err, want)
		case got != strconv.FormatUint(want, 10):
			t.Fatalf("balance of %s: %s, node %d", a, got, want)
		default:
			positive++
		}
	}

	var n int

	err = conn.QueryRow(ctx, "SELECT count(*) FROM balances").Scan(&n)
	if err != nil || n != positive {
		t.Fatalf("%d balance rows (%v), want %d", n, err, positive)
	}

	pgtest.CheckInvariants(t, url)
}

// bigInt is a running total of uint64 amounts that may not wrap.
type bigInt struct{ v int64 }

func (b *bigInt) String() string { return strconv.FormatInt(b.v, 10) }

func (b *bigInt) add(x uint64) { b.v += int64(x) }

func (b *bigInt) sub(x uint64) { b.v -= int64(x) }

// fixed returns the address of the wallet derived from a one-byte seed.
func fixed(b byte) string {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = b
	}

	w, err := wallet.FromSeed(seed)
	if err != nil {
		panic(err)
	}

	return w.Address()
}

var (
	treasury  = fixed(1)
	elsewhere = fixed(2)
	newbranch = fixed(3)
)

var allocs = func(owner, owner2 string) []domain.TxOut {
	return []domain.TxOut{
		{Amount: 100, Address: owner},
		{Amount: 100, Address: owner},
		{Amount: 100, Address: owner},
		{Amount: 100, Address: owner},
		{Amount: 100, Address: owner2},
		{Amount: 100, Address: owner2},
		{Amount: 100, Address: owner2},
		{Amount: 25, Address: treasury},
	}
}

func TestFollowsANodeFromEmptyThroughTransfersFeesAndContracts(t *testing.T) {
	owner, owner2, miner := wal(t), wal(t), wal(t)
	n := newEnv(t, nil, allocs(owner.Address(), owner2.Address()), miner.Address(), 1)
	url := pgtest.NewDatabase(t)

	n.sync(url) // genesis only
	n.assertMatches(url)

	n.mine(n.spend(owner, owner2.Address(), 40, 2), n.spend(owner2, treasury, 10, 1))
	n.sync(url)
	n.assertMatches(url)

	dep := n.contract(owner, "", 3)
	n.mine(dep)
	n.mine(n.spend(owner2, owner.Address(), 5, 4))
	n.sync(url)
	n.assertMatches(url)

	n.mine(n.contract(owner, dep.CalculateHash(), 2))
	n.mine() // coinbase only
	n.sync(url)
	n.sync(url) // level: a no-op
	n.assertMatches(url)

	if h := n.height(); h < 5 {
		t.Fatalf("node at height %d", h)
	}
}

func TestFollowsANodeInOnePassAcrossPages(t *testing.T) {
	owner, owner2, miner := wal(t), wal(t), wal(t)
	n := newEnv(t, nil, allocs(owner.Address(), owner2.Address()), miner.Address(), 1)

	for range 105 { // more than one /blocks page of 100
		n.mine()
	}

	url := pgtest.NewDatabase(t)
	n.sync(url)
	n.assertMatches(url)
}

func hasSpentAbove(t *testing.T, url string, fork int) []string {
	t.Helper()

	rows, err := mustConn(t, url).Query(context.Background(), "SELECT txid || ':' || idx FROM outputs WHERE spent_height > $1 ORDER BY 1", fork)
	if err != nil {
		t.Fatal(err)
	}

	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}

	return got
}

func TestReorgLeavesTheDatabaseEqualToTheNode(t *testing.T) {
	cases := []struct {
		name     string
		aAfter   int // blocks the old branch has above the fork
		bBlocks  int // blocks of the new branch
		bDiff    int
		wantHigh int // new tip height minus old tip height: >0 higher, 0 equal, <0 lower
	}{
		{"depth 1, ends higher", 1, 2, 1, 1},
		{"depth 3, ends higher", 3, 5, 1, 2},
		{"depth 3, ends at the same height", 3, 3, 2, 0},
		{"depth 3, ends lower", 4, 2, 5, -2},
		{"depth 1, ends at the same height", 1, 1, 3, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owner, owner2, minerA, minerB := wal(t), wal(t), wal(t), wal(t)
			a := newEnv(t, nil, allocs(owner.Address(), owner2.Address()), minerA.Address(), 1)
			genesis := a.blocks()[0]

			// Common history: transfers, a contract deploy, a fee, up to the fork F.
			a.mine(a.spend(owner, owner2.Address(), 40, 2))
			dep := a.contract(owner, "", 3)
			a.mine(dep)
			a.mine(a.spend(owner2, treasury, 10, 1))
			fork := a.height()

			b := newEnv(t, genesis, nil, minerB.Address(), tc.bDiff)
			for _, blk := range a.blocks()[1:] {
				err := b.svc.AcceptBlock(context.Background(), blk)
				if err != nil {
					t.Fatal(err)
				}
			}

			// The old branch spends the owner's outputs and calls the contract; the new one
			// spends owner2's and mines empty blocks.
			for i := range tc.aAfter {
				if i == 0 {
					a.mine(a.contract(owner, dep.CalculateHash(), 2))
				} else {
					a.mine(a.spend(owner, elsewhere, 7, 1))
				}
			}

			url := pgtest.NewDatabase(t)
			a.sync(url)
			a.assertMatches(url)

			orphanSpent := hasSpentAbove(t, url, fork)
			if len(orphanSpent) == 0 {
				t.Fatal("test setup: the old branch spent nothing")
			}

			for i := range tc.bBlocks {
				if i == 0 {
					b.mine(b.spend(owner2, newbranch, 11, 3))
				} else {
					b.mine()
				}
			}

			_, err := a.svc.ReplaceChain(context.Background(), b.blocks())
			if err != nil {
				t.Fatal(err)
			}

			if got := a.height() - fork - tc.aAfter; got != tc.wantHigh {
				t.Fatalf("new tip is %d above the old one, want %d", got, tc.wantHigh)
			}

			a.sync(url)
			a.assertMatches(url)

			// The old branch's spends of outputs that survive the reorg are unspent again.
			conn := mustConn(t, url)

			for _, o := range orphanSpent {
				var spent *string

				err := conn.QueryRow(context.Background(), "SELECT spent_txid FROM outputs WHERE txid || ':' || idx = $1 AND height <= $2", o, fork).Scan(&spent)

				if err == nil && spent != nil {
					t.Fatalf("output %s of the common history is still spent by %s", o, *spent)
				}
			}

			var orphans int

			err = conn.QueryRow(context.Background(), "SELECT count(*) FROM outputs WHERE height > $1 AND address = $2", fork, elsewhere).Scan(&orphans)
			if err != nil || orphans != 0 {
				t.Fatalf("%d outputs of the orphaned branch remain (%v)", orphans, err)
			}

			// A second sync changes nothing.
			before := pgtest.Dump(t, url)
			a.sync(url)

			if pgtest.Dump(t, url) != before {
				t.Fatal("a level sync changed the database")
			}
		})
	}
}

func TestPrunedBodyStopsTheIndexerWithoutWriting(t *testing.T) {
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte(`{"error":"block body was pruned; only its header is kept","code":"block_pruned"}`))
	}))
	defer gone.Close()

	s, url := openStore(t)
	before := pgtest.Dump(t, url)

	nc, err := indexer.NewNodeClient(gone.URL)
	if err != nil {
		t.Fatal(err)
	}

	err = follower(nc, s).Sync(context.Background())
	if err == nil || err.Error() != "the indexer needs a node that keeps every block body" {
		t.Fatalf("err %v", err)
	}

	if pgtest.Dump(t, url) != before {
		t.Fatal("wrote after a pruned answer")
	}
}

func TestDifferentGenesisStopsTheIndexerWithoutWriting(t *testing.T) {
	owner, owner2, miner := wal(t), wal(t), wal(t)
	a := newEnv(t, nil, allocs(owner.Address(), owner2.Address()), miner.Address(), 1)
	a.mine()
	a.mine()

	url := pgtest.NewDatabase(t)
	a.sync(url)

	other := newEnv(t, nil, allocs(owner2.Address(), owner.Address()), miner.Address(), 1)
	for range 4 {
		other.mine()
	}

	before := pgtest.Dump(t, url)

	s, err := postgres.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(context.Background()) }()

	nc, _ := indexer.NewNodeClient(other.srv.URL)

	err = follower(nc, s).Sync(context.Background())
	if err == nil || err.Error() != "different chain; run bwg indexer resync" {
		t.Fatalf("err %v", err)
	}

	if pgtest.Dump(t, url) != before {
		t.Fatal("wrote although the genesis differs")
	}
}
