package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/indexer"
	"github.com/petersonsalme/blockchain-with-go/internal/indexer/postgres"
	"github.com/petersonsalme/blockchain-with-go/internal/indexer/postgres/pgtest"
)

// sliceNode serves a chain held in memory; swapping chain simulates a reorg of the node.
type sliceNode struct{ chain []indexer.Block }

func (n *sliceNode) Blocks(_ context.Context, from, limit int) ([]indexer.Block, error) {
	if from >= len(n.chain) {
		return nil, nil
	}

	return n.chain[from:min(from+limit, len(n.chain))], nil
}

func (n *sliceNode) Headers(_ context.Context, from, limit int) ([]indexer.Header, error) {
	var hs []indexer.Header

	for _, b := range n.chain[min(from, len(n.chain)):min(from+limit, len(n.chain))] {
		hs = append(hs, indexer.Header{Height: b.Height, Hash: b.Hash})
	}

	return hs, nil
}

func follower(n indexer.Node, s indexer.Store) *indexer.Follower {
	return indexer.NewFollower(n, s, slog.New(slog.DiscardHandler), indexer.WithPollInterval(time.Millisecond))
}

var errCrash = errors.New("crash")

// crashAt runs Sync on a new store with its hook failing at event number k (cancelling the
// context instead when cancel is true) and reports whether the crash happened. The store is
// closed afterwards, as the process exit would.
func crashAt(t *testing.T, url string, node indexer.Node, k int, cancel bool) bool {
	t.Helper()

	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	s, err := postgres.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}

	events := 0

	s.SetHook(func(bool) error {
		defer func() { events++ }()

		if events != k {
			return nil
		}

		if cancel {
			stop()

			return nil
		}

		return errCrash
	})

	err = follower(node, s).Sync(ctx)
	_ = s.Close(context.Background())

	crashed := events > k
	if crashed && err == nil {
		t.Fatalf("event %d: crash was swallowed", k)
	}

	return crashed
}

func syncOnce(t *testing.T, url string, node indexer.Node) {
	t.Helper()

	s, err := postgres.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(context.Background()) }()

	err = follower(node, s).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
}

func resetTo(t *testing.T, url string, node indexer.Node) {
	t.Helper()

	s, err := postgres.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}

	err = s.Reset(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	_ = s.Close(context.Background())

	syncOnce(t, url, node)
}

// prefixOf checks that the stored blocks are a prefix of chain: the database is at the last
// fully applied block.
func prefixOf(t *testing.T, url string, chain []indexer.Block, label string) {
	t.Helper()

	s, err := postgres.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(context.Background()) }()

	hashes, err := s.Hashes(context.Background(), 0, len(chain)+1)
	if err != nil {
		t.Fatal(err)
	}

	for h, hash := range hashes {
		if h >= len(chain) || chain[h].Hash != hash {
			t.Fatalf("%s: stored height %d is %s, not on the chain", label, h, hash)
		}
	}
}

// reorgChains returns the old chain and a heavier-by-the-node's-view new one forking at 2.
func reorgChains() (old, newer []indexer.Block) {
	old = synth("a")

	b := synth("b")
	b[0], b[1], b[2] = old[0], old[1], old[2]
	b[3].PrevHash, b[3].Txs = old[2].Hash, []indexer.Tx{{ID: "b-cb", Outputs: []indexer.Out{{Address: "frank", Amount: 7}}}}
	b[4].PrevHash = b[3].Hash
	b[4].Txs = []indexer.Tx{{ID: "b-t", Inputs: []indexer.OutRef{{TxID: "bt1", Index: 1}}, Outputs: []indexer.Out{{Address: "gina", Amount: 64}}}}

	return old, b
}

// TestCrashAtAnyPointRecoversToTheUninterruptedDump kills a sync before and after each statement
// of every block's apply and of the rollback (by an error and by cancelling the context) and
// checks the database stays at the last applied block and ends identical to an uninterrupted run.
func TestCrashAtAnyPointRecoversToTheUninterruptedDump(t *testing.T) {
	old, newer := reorgChains()
	// newer's last transaction spends the coinbase of height 1, which both branches share.
	newer[4].Txs[0].Inputs[0] = indexer.OutRef{TxID: "cb1", Index: 0}
	newer[4].Txs[0].Outputs[0].Amount = 10

	scenarios := []struct {
		name  string
		setup []indexer.Block // chain applied before the crashing sync
		sync  []indexer.Block // chain the node serves during the crashing sync
	}{
		{"fresh sync", nil, old},
		{"reorg", old, newer},
	}

	for _, sc := range scenarios {
		for _, cancel := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cancel=%v", sc.name, cancel), func(t *testing.T) {
				goldURL := pgtest.NewDatabase(t)
				syncOnce(t, goldURL, &sliceNode{chain: sc.sync})
				golden := pgtest.Dump(t, goldURL)

				url := pgtest.NewDatabase(t)
				node := &sliceNode{chain: sc.sync}
				hit := 0

				for k := 0; ; k++ {
					resetTo(t, url, &sliceNode{chain: sc.setup})

					if !crashAt(t, url, node, k, cancel) {
						break
					}

					hit++

					pgtest.CheckInvariants(t, url)
					// Whatever the crash left is the setup chain's prefix or the new chain's prefix.
					s, err := postgres.Open(context.Background(), url)
					if err != nil {
						t.Fatal(err)
					}

					tip, _, _ := s.Tip(context.Background())
					hashes, _ := s.Hashes(context.Background(), 0, tip.Height+1)
					_ = s.Close(context.Background())

					if !onEither(hashes, sc.setup, sc.sync) {
						t.Fatalf("event %d: state is not a prefix of either chain: %v", k, hashes)
					}

					syncOnce(t, url, node)

					if got := pgtest.Dump(t, url); got != golden {
						t.Fatalf("event %d: after recovery\n%s\nwant\n%s", k, got, golden)
					}

					pgtest.CheckInvariants(t, url)
					prefixOf(t, url, sc.sync, fmt.Sprintf("event %d", k))
				}

				if hit < 20 {
					t.Fatalf("only %d crash points exercised", hit)
				}

				t.Logf("%d crash points", hit)
			})
		}
	}
}

func onEither(hashes []string, chains ...[]indexer.Block) bool {
	for _, c := range chains {
		ok := len(hashes) <= len(c)

		for h := 0; ok && h < len(hashes); h++ {
			ok = c[h].Hash == hashes[h]
		}

		if ok {
			return true
		}
	}

	return false
}
