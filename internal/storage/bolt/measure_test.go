package bolt_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/bolt"
)

// TestMeasureSnapshotCost prints the chain.db size and the Commit time of the
// blocks that store a snapshot (t-K = 0 mod N, K = 100, N = 1000) for 20k
// blocks, with and without pruning. It takes minutes (one fsync per Commit),
// so it only runs with MEASURE_SNAPSHOT=1.
func TestMeasureSnapshotCost(t *testing.T) {
	if os.Getenv("MEASURE_SNAPSHOT") == "" {
		t.Skip("set MEASURE_SNAPSHOT=1 to run")
	}

	const blocks = 20_000

	for _, p := range []storage.Pruning{{}, {SnapshotInterval: 1000, KeepBlocks: 100}} {
		path := filepath.Join(t.TempDir(), "chain.db")

		repo, err := bolt.OpenPruned(path, p)
		if err != nil {
			t.Fatal(err)
		}

		queue := []domain.OutPoint{}
		amounts := map[domain.OutPoint]uint64{}

		var plain, snap []time.Duration

		prev := ""

		for i := range blocks {
			tx := domain.Transaction{Timestamp: time.Unix(1_700_000_000+int64(i), 0).UTC()}
			d := domain.UTXODelta{Spent: map[domain.OutPoint]domain.TxOut{}, Created: map[domain.OutPoint]domain.TxOut{}}
			total := uint64(1) << 50

			if i > 0 {
				in := queue[0]
				queue = queue[1:]
				total = amounts[in]
				tx.Inputs = []domain.TxIn{{PrevTxID: in.TxID, OutIndex: in.Index}}
				d.Spent[in] = domain.TxOut{Amount: total, Address: "addr" + strconv.FormatUint(uint64(in.Index), 10)}
			}

			tx.Outputs = []domain.TxOut{{Amount: total / 2, Address: "addr0"}, {Amount: total - total/2, Address: "addr1"}}

			id := tx.CalculateHash()
			for j, o := range tx.Outputs {
				op := domain.OutPoint{TxID: id, Index: uint32(j)}
				d.Created[op] = o
				queue = append(queue, op)
				amounts[op] = o.Amount
			}

			b := &domain.Block{Index: i, Hash: fmt.Sprintf("%064d", i), PrevHash: prev, Transactions: []domain.Transaction{tx}}
			prev = b.Hash
			start := time.Now()

			if err := repo.Commit(b, d); err != nil {
				t.Fatal(err)
			}

			took := time.Since(start)
			if s := i - 100; s >= 1 && s%1000 == 0 {
				snap = append(snap, took)
			} else {
				plain = append(plain, took)
			}
		}

		if err := repo.Close(); err != nil {
			t.Fatal(err)
		}

		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}

		mean := func(ds []time.Duration) time.Duration {
			var s time.Duration
			for _, d := range ds {
				s += d
			}

			return s / time.Duration(len(ds))
		}
		t.Logf("%+v: chain.db %.1f MiB; Commit mean %v (other blocks), %v (blocks t=1100,2100,...); max snapshot-height Commit %v",
			p, float64(fi.Size())/(1<<20), mean(plain), mean(snap), maxOf(snap))
	}
}

func maxOf(ds []time.Duration) time.Duration {
	var m time.Duration
	for _, d := range ds {
		m = max(m, d)
	}

	return m
}
