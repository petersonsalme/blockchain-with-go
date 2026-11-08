package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/petersonsalme/blockchain-with-go/internal/indexer"
	"github.com/petersonsalme/blockchain-with-go/internal/indexer/postgres"
	"github.com/petersonsalme/blockchain-with-go/internal/indexer/postgres/pgtest"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// synth builds a small chain whose transactions exercise every rule: allocations, a coinbase,
// a transfer with change and a fee, a spend of an output created in the same block, burned fees
// (a block without coinbase) and a txid reused by a coinbase after its outputs were spent.
func synth(tag string) []indexer.Block {
	blk := func(h int, prev string, txs ...indexer.Tx) indexer.Block {
		return indexer.Block{Height: h, Hash: fmt.Sprintf("%s-h%d", tag, h), PrevHash: prev, Time: t0.Add(time.Duration(h) * time.Second), Txs: txs}
	}
	out := func(a string, v uint64) indexer.Out { return indexer.Out{Address: a, Amount: v} }
	in := func(tx string, i uint32) indexer.OutRef { return indexer.OutRef{TxID: tx, Index: i} }

	g := blk(0, "", indexer.Tx{ID: "gen", Outputs: []indexer.Out{out("alice", 100), out("bob", 50)}})
	b1 := blk(1, g.Hash,
		indexer.Tx{ID: "cb1", Outputs: []indexer.Out{out("miner", 10)}},
		indexer.Tx{ID: tag + "t1", Inputs: []indexer.OutRef{in("gen", 0)}, Outputs: []indexer.Out{out("carol", 30), out("alice", 65)}}, // fee 5
	)
	b2 := blk(2, b1.Hash,
		indexer.Tx{ID: tag + "t2", Inputs: []indexer.OutRef{in(tag+"t1", 0)}, Outputs: []indexer.Out{out("dave", 29)}}, // fee 1
		indexer.Tx{ID: tag + "t3", Inputs: []indexer.OutRef{in(tag+"t2", 0)}, Outputs: []indexer.Out{out("erin", 28)}}, // spends t2 in the same block
	)
	b3 := blk(3, b2.Hash, indexer.Tx{ID: tag + "t4", Inputs: []indexer.OutRef{in("gen", 1), in("cb1", 0)}, Outputs: []indexer.Out{out("alice", 55)}}) // burned fee 5, no coinbase
	b4 := blk(4, b3.Hash, indexer.Tx{ID: "cb1", Outputs: []indexer.Out{out("miner", 10)}})                                                            // txid reused

	return []indexer.Block{g, b1, b2, b3, b4}
}

func applyAll(t *testing.T, s *postgres.Store, blocks []indexer.Block) {
	t.Helper()

	for _, b := range blocks {
		err := s.Apply(context.Background(), b)
		if err != nil {
			t.Fatalf("apply %d: %v", b.Height, err)
		}
	}
}

func TestApplyKeepsInvariantsAndTotals(t *testing.T) {
	s, url := openStore(t)
	applyAll(t, s, synth("a"))
	pgtest.CheckInvariants(t, url)

	conn := mustConn(t, url)

	rows, err := conn.Query(context.Background(), "SELECT height, created::text, spent::text, coinbase::text, total::text FROM supply ORDER BY height")
	if err != nil {
		t.Fatal(err)
	}

	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (string, error) {
		var (
			h              int
			c, sp, cb, tot string
		)

		err := r.Scan(&h, &c, &sp, &cb, &tot)

		return fmt.Sprintf("%d:%s/%s/%s=%s", h, c, sp, cb, tot), err
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"0:150/0/150=150", "1:105/100/10=155", "2:57/59/0=153", "3:55/60/0=148", "4:10/0/10=158"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("supply %v, want %v", got, want)
	}

	tip, ok, err := s.Tip(context.Background())
	if err != nil || !ok || tip.Height != 4 || tip.Hash != "a-h4" {
		t.Fatalf("tip %+v %v %v", tip, ok, err)
	}

	hashes, err := s.Hashes(context.Background(), 1, 3)
	if err != nil || fmt.Sprint(hashes) != "[a-h1 a-h2 a-h3]" {
		t.Fatalf("hashes %v %v", hashes, err)
	}
}

func mustConn(t *testing.T, url string) *pgx.Conn {
	t.Helper()

	c, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = c.Close(context.Background()) })

	return c
}

func TestAmountsAboveInt64(t *testing.T) {
	s, url := openStore(t)
	big := uint64(1<<64 - 1)
	g := indexer.Block{Height: 0, Hash: "g", Time: t0, Txs: []indexer.Tx{{ID: "gen", Outputs: []indexer.Out{{Address: "a", Amount: big}}}}}
	b1 := indexer.Block{Height: 1, Hash: "h1", PrevHash: "g", Time: t0, Txs: []indexer.Tx{{ID: "t", Inputs: []indexer.OutRef{{TxID: "gen"}}, Outputs: []indexer.Out{{Address: "b", Amount: big - 1}}}}}
	applyAll(t, s, []indexer.Block{g, b1})
	pgtest.CheckInvariants(t, url)

	var total string

	err := mustConn(t, url).QueryRow(context.Background(), "SELECT total::text FROM supply WHERE height = 1").Scan(&total)
	if err != nil || total != "18446744073709551614" {
		t.Fatalf("total %q %v", total, err)
	}
}

func TestApplyRejectsBlocksThatDoNotExtendTheTip(t *testing.T) {
	chain := synth("a")
	s, url := openStore(t)
	ctx := context.Background()

	err := s.Apply(ctx, chain[1])
	if !errors.Is(err, indexer.ErrNotNext) {
		t.Fatalf("height 1 on empty: %v", err)
	}

	applyAll(t, s, chain[:2])

	skip := chain[3]
	wrongPrev := chain[2]
	wrongPrev.PrevHash = "other"

	for name, b := range map[string]indexer.Block{"skipped height": skip, "wrong prev": wrongPrev, "again": chain[1], "genesis again": chain[0]} {
		err := s.Apply(ctx, b)
		if !errors.Is(err, indexer.ErrNotNext) {
			t.Fatalf("%s: %v", name, err)
		}
	}

	pgtest.CheckInvariants(t, url)

	tip, _, _ := s.Tip(ctx)
	if tip.Height != 1 {
		t.Fatalf("tip %d", tip.Height)
	}
}

func TestApplyOfUnknownOrSpentInputWritesNothing(t *testing.T) {
	chain := synth("a")
	s, url := openStore(t)
	applyAll(t, s, chain[:2])
	before := pgtest.Dump(t, url)

	bad := chain[2]

	bad.Txs = []indexer.Tx{{ID: "x", Inputs: []indexer.OutRef{{TxID: "gen", Index: 0}}, Outputs: []indexer.Out{{Address: "z", Amount: 1}}}} // already spent

	err := s.Apply(context.Background(), bad)
	if err == nil {
		t.Fatal("spent input accepted")
	}

	bad.Txs[0].Inputs[0] = indexer.OutRef{TxID: "nope"}

	err = s.Apply(context.Background(), bad)
	if err == nil {
		t.Fatal("unknown input accepted")
	}

	if pgtest.Dump(t, url) != before {
		t.Fatal("a failed apply changed the database")
	}
}

func TestRollbackRestoresEveryPriorState(t *testing.T) {
	chain := synth("a")

	// dumps[h] is the database after applying heights 0..h.
	s, url := openStore(t)
	dumps := map[int]string{}

	for _, b := range chain {
		applyAll(t, s, []indexer.Block{b})
		dumps[b.Height] = pgtest.Dump(t, url)
	}

	for to := range slices.Backward(chain) {
		err := s.Rollback(context.Background(), to)
		if err != nil {
			t.Fatal(err)
		}

		if got := pgtest.Dump(t, url); got != dumps[to] {
			t.Fatalf("rollback to %d:\n%s\nwant\n%s", to, got, dumps[to])
		}

		pgtest.CheckInvariants(t, url)
	}
}

func TestRollbackThenApplyOtherBranch(t *testing.T) {
	s, url := openStore(t)
	a, b := synth("a"), synth("b")
	applyAll(t, s, a)

	// b shares only the genesis with a.
	b[0] = a[0]
	b[1].PrevHash = a[0].Hash

	err := s.Rollback(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}

	applyAll(t, s, b[1:])
	pgtest.CheckInvariants(t, url)

	fresh, freshURL := openStore(t)
	applyAll(t, fresh, b)

	if pgtest.Dump(t, url) != pgtest.Dump(t, freshURL) {
		t.Fatal("rollback + apply differs from a fresh apply")
	}
}

func TestSecondOpenIsLockedUntilClose(t *testing.T) {
	s, url := openStore(t)

	_, err := postgres.Open(context.Background(), url)
	if !errors.Is(err, postgres.ErrLocked) {
		t.Fatalf("err %v, want ErrLocked", err)
	}

	if err.Error() == "" || !contains(err.Error(), "lock") {
		t.Fatalf("message %q does not name the lock", err)
	}

	err = s.Close(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	s2, err := postgres.Open(context.Background(), url)
	if err != nil {
		t.Fatalf("open after close: %v", err)
	}

	_ = s2.Close(context.Background())
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}

	return false
}

func TestResetDropsTablesAndKeepsSchema(t *testing.T) {
	s, url := openStore(t)
	applyAll(t, s, synth("a"))

	err := s.Reset(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	_, ok, err := s.Tip(context.Background())
	if err != nil || ok {
		t.Fatalf("tip after reset: %v %v", ok, err)
	}

	applyAll(t, s, synth("a"))
	pgtest.CheckInvariants(t, url)
}

func TestMigrationIsIdempotentAndRejectsNewerSchema(t *testing.T) {
	s, url := openStore(t)
	_ = s.Close(context.Background())

	s, err := postgres.Open(context.Background(), url) // second open re-reads the version, applies nothing
	if err != nil {
		t.Fatal(err)
	}

	_, err = mustConn(t, url).Exec(context.Background(), "INSERT INTO schema_version VALUES (99)")
	if err != nil {
		t.Fatal(err)
	}

	_ = s.Close(context.Background())

	_, err = postgres.Open(context.Background(), url)
	if err == nil {
		t.Fatal("a newer schema was accepted")
	}
}

func TestOpenFailsOnBadURL(t *testing.T) {
	_, err := postgres.Open(context.Background(), "postgres://nobody@127.0.0.1:1/x?connect_timeout=1")
	if err == nil {
		t.Fatal("expected an error")
	}
}
