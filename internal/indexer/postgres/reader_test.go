package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/petersonsalme/blockchain-with-go/internal/indexer"
	"github.com/petersonsalme/blockchain-with-go/internal/indexer/postgres"
)

func TestReaderQueriesAndInvariants(t *testing.T) {
	ctx := context.Background()
	s, url := openStore(t)
	chain := synth("a")[:4]
	applyAll(t, s, chain)

	r := postgres.NewReader(url)

	// 1. Health on populated DB, unreachable URL, and empty DB (checked last).
	tip, err := r.Health(ctx)
	if err != nil {
		t.Fatalf("health: %v", err)
	}

	if tip.Height != 3 || tip.Hash != chain[3].Hash {
		t.Fatalf("health got %+v, want height 3 hash %q", tip, chain[3].Hash)
	}

	badReader := postgres.NewReader("postgres://nobody@127.0.0.1:1/x?connect_timeout=1")

	_, err = badReader.Health(ctx)
	if err == nil {
		t.Fatal("expected error for unreachable URL")
	}

	// 2. SupplyTip and SupplyAt(0..3), plus SupplyAt(4) -> ErrHeightNotIndexed.
	wantSupply := []indexer.SupplyRow{
		{Height: 0, Hash: chain[0].Hash, Total: "150", Created: "150", Spent: "0", Coinbase: "150"},
		{Height: 1, Hash: chain[1].Hash, Total: "155", Created: "105", Spent: "100", Coinbase: "10"},
		{Height: 2, Hash: chain[2].Hash, Total: "153", Created: "57", Spent: "59", Coinbase: "0"},
		{Height: 3, Hash: chain[3].Hash, Total: "148", Created: "55", Spent: "60", Coinbase: "0"},
	}

	gotTipSupply, err := r.SupplyTip(ctx)
	if err != nil {
		t.Fatalf("supply tip: %v", err)
	}

	if gotTipSupply != wantSupply[3] {
		t.Fatalf("supply tip got %+v, want %+v", gotTipSupply, wantSupply[3])
	}

	for h, want := range wantSupply {
		got, err := r.SupplyAt(ctx, h)
		if err != nil {
			t.Fatalf("supply at %d: %v", h, err)
		}

		if got != want {
			t.Fatalf("supply at %d got %+v, want %+v", h, got, want)
		}
	}

	_, err = r.SupplyAt(ctx, 4)
	if !errors.Is(err, indexer.ErrHeightNotIndexed) {
		t.Fatalf("supply at 4 got err %v, want ErrHeightNotIndexed", err)
	}

	// 3. Richest(20) and Richest(1).
	richAll, err := r.Richest(ctx, 20)
	if err != nil {
		t.Fatalf("richest(20): %v", err)
	}

	if richAll.Height != 3 || richAll.Hash != chain[3].Hash || richAll.Total != "148" {
		t.Fatalf("richest(20) header got %+v", richAll)
	}

	wantAddrs := []indexer.RichAddress{
		{Address: "alice", Balance: "120", Share: indexer.ShareString("120", "148")},
		{Address: "erin", Balance: "28", Share: indexer.ShareString("28", "148")},
	}
	if fmt.Sprint(richAll.Addresses) != fmt.Sprint(wantAddrs) {
		t.Fatalf("richest(20) addresses got %+v, want %+v", richAll.Addresses, wantAddrs)
	}

	if sumBalances(t, richAll.Addresses).Cmp(mustBig(t, richAll.Total)) != 0 {
		t.Fatalf("richest(20) sum != total %s", richAll.Total)
	}

	richOne, err := r.Richest(ctx, 1)
	if err != nil {
		t.Fatalf("richest(1): %v", err)
	}

	if len(richOne.Addresses) != 1 || richOne.Addresses[0] != wantAddrs[0] {
		t.Fatalf("richest(1) got %+v, want [%+v]", richOne.Addresses, wantAddrs[0])
	}

	if sumBalances(t, richOne.Addresses).Cmp(mustBig(t, richOne.Total)) > 0 {
		t.Fatalf("richest(1) sum > total %s", richOne.Total)
	}

	// 4. Stats(0, 3), Stats(1, 2), Stats(1, 99), Stats(5, 10).
	statsCases := []struct {
		from int
		to   int
		want indexer.RangeStats
	}{
		{0, 3, indexer.RangeStats{From: 0, To: 3, Blocks: 4, Txs: 6, Created: "367", Spent: "219", Coinbase: "160"}},
		{1, 2, indexer.RangeStats{From: 1, To: 2, Blocks: 2, Txs: 4, Created: "162", Spent: "159", Coinbase: "10"}},
		{1, 99, indexer.RangeStats{From: 1, To: 3, Blocks: 3, Txs: 5, Created: "217", Spent: "219", Coinbase: "10"}},
		{5, 10, indexer.RangeStats{From: 5, To: 3, Blocks: 0, Txs: 0, Created: "0", Spent: "0", Coinbase: "0"}},
	}

	for _, tc := range statsCases {
		got, err := r.Stats(ctx, tc.from, tc.to)
		if err != nil {
			t.Fatalf("stats(%d, %d): %v", tc.from, tc.to, err)
		}

		if got != tc.want {
			t.Fatalf("stats(%d, %d) got %+v, want %+v", tc.from, tc.to, got, tc.want)
		}
	}

	// Empty database checks after Reset.
	err = s.Reset(ctx)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}

	_, err = r.Health(ctx)
	if !errors.Is(err, indexer.ErrHeightNotIndexed) {
		t.Fatalf("health on empty DB got %v, want ErrHeightNotIndexed", err)
	}

	_, err = r.SupplyTip(ctx)
	if !errors.Is(err, indexer.ErrHeightNotIndexed) {
		t.Fatalf("supply tip on empty DB got %v, want ErrHeightNotIndexed", err)
	}

	_, err = r.Richest(ctx, 10)
	if !errors.Is(err, indexer.ErrHeightNotIndexed) {
		t.Fatalf("richest on empty DB got %v, want ErrHeightNotIndexed", err)
	}

	_, err = r.Stats(ctx, 0, 3)
	if !errors.Is(err, indexer.ErrHeightNotIndexed) {
		t.Fatalf("stats on empty DB got %v, want ErrHeightNotIndexed", err)
	}
}

func TestReaderRepeatableReadAcrossConcurrentReorg(t *testing.T) {
	ctx := context.Background()
	s, url := openStore(t)

	base := synth("base")[:2]
	applyAll(t, s, base)

	branchA := synth("branchA")
	branchA[2].PrevHash = base[1].Hash
	branchA[2].Txs[0].Inputs[0].TxID = base[1].Txs[1].ID

	branchB := synth("branchB")
	branchB[2].PrevHash = base[1].Hash
	branchB[2].Txs[0].Inputs[0].TxID = base[1].Txs[1].ID
	branchB[2].Txs[0].Outputs[0].Amount = 25
	branchB[2].Txs[1].Outputs[0].Amount = 20
	branchB[3].Txs[0].Outputs[0].Amount = 40

	expectedSupply := map[string]indexer.SupplyRow{
		base[1].Hash:    {Height: 1, Hash: base[1].Hash, Total: "155", Created: "105", Spent: "100", Coinbase: "10"},
		branchA[2].Hash: {Height: 2, Hash: branchA[2].Hash, Total: "153", Created: "57", Spent: "59", Coinbase: "0"},
		branchA[3].Hash: {Height: 3, Hash: branchA[3].Hash, Total: "148", Created: "55", Spent: "60", Coinbase: "0"},
		branchA[4].Hash: {Height: 4, Hash: branchA[4].Hash, Total: "158", Created: "10", Spent: "0", Coinbase: "10"},
		branchB[2].Hash: {Height: 2, Hash: branchB[2].Hash, Total: "145", Created: "45", Spent: "55", Coinbase: "0"},
		branchB[3].Hash: {Height: 3, Hash: branchB[3].Hash, Total: "125", Created: "40", Spent: "60", Coinbase: "0"},
		branchB[4].Hash: {Height: 4, Hash: branchB[4].Hash, Total: "135", Created: "10", Spent: "0", Coinbase: "10"},
	}

	validStats := map[indexer.RangeStats]bool{
		{From: 0, To: 1, Blocks: 2, Txs: 3, Created: "255", Spent: "100", Coinbase: "160"}: true,
		{From: 0, To: 2, Blocks: 3, Txs: 5, Created: "312", Spent: "159", Coinbase: "160"}: true,
		{From: 0, To: 3, Blocks: 4, Txs: 6, Created: "367", Spent: "219", Coinbase: "160"}: true,
		{From: 0, To: 2, Blocks: 3, Txs: 5, Created: "300", Spent: "155", Coinbase: "160"}: true,
		{From: 0, To: 3, Blocks: 4, Txs: 6, Created: "340", Spent: "215", Coinbase: "160"}: true,
	}

	r := postgres.NewReader(url)

	var done atomic.Bool

	errCh := make(chan error, 16)

	var readersWG sync.WaitGroup
	for range 4 {
		readersWG.Go(func() {
			for !done.Load() {
				sup, err := r.SupplyTip(ctx)
				if err != nil {
					errCh <- fmt.Errorf("SupplyTip: %w", err)

					return
				}

				want, ok := expectedSupply[sup.Hash]
				if !ok || sup != want {
					errCh <- fmt.Errorf("SupplyTip inconsistent: got %+v, want %+v", sup, want)

					return
				}

				rich, err := r.Richest(ctx, 10)
				if err != nil {
					errCh <- fmt.Errorf("Richest: %w", err)

					return
				}

				wantRich, ok := expectedSupply[rich.Hash]
				if !ok || rich.Height != wantRich.Height || rich.Total != wantRich.Total {
					errCh <- fmt.Errorf("Richest header inconsistent: got %+v, want %+v", rich, wantRich)

					return
				}

				balSum := new(big.Int)
				for _, a := range rich.Addresses {
					v, ok := new(big.Int).SetString(a.Balance, 10)
					if !ok {
						errCh <- fmt.Errorf("invalid balance %q", a.Balance)

						return
					}

					balSum.Add(balSum, v)

					if a.Share != indexer.ShareString(a.Balance, rich.Total) {
						errCh <- fmt.Errorf("share mismatch for %+v total %s", a, rich.Total)

						return
					}
				}

				tot, _ := new(big.Int).SetString(rich.Total, 10)
				if balSum.Cmp(tot) != 0 {
					errCh <- fmt.Errorf("richest sum %s != total %s (hash %s)", balSum, rich.Total, rich.Hash)

					return
				}

				st, err := r.Stats(ctx, 0, 3)
				if err != nil {
					errCh <- fmt.Errorf("Stats: %w", err)

					return
				}

				if !validStats[st] {
					errCh <- fmt.Errorf("Stats inconsistent: got %+v", st)

					return
				}
			}
		})
	}

	for i := range 20 {
		branch := branchA[2:]
		if i%2 == 1 {
			branch = branchB[2:]
		}

		err := s.Rollback(ctx, 1)
		if err != nil {
			done.Store(true)
			readersWG.Wait()
			t.Fatalf("rollback %d: %v", i, err)
		}

		for _, b := range branch {
			err := s.Apply(ctx, b)
			if err != nil {
				done.Store(true)
				readersWG.Wait()
				t.Fatalf("apply %d h%d: %v", i, b.Height, err)
			}
		}
	}

	done.Store(true)
	readersWG.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatal(err)
	}
}

func mustBig(t *testing.T, s string) *big.Int {
	t.Helper()

	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("invalid integer %q", s)
	}

	return v
}

func sumBalances(t *testing.T, addrs []indexer.RichAddress) *big.Int {
	t.Helper()

	sum := new(big.Int)
	for _, a := range addrs {
		sum.Add(sum, mustBig(t, a.Balance))
	}

	return sum
}
