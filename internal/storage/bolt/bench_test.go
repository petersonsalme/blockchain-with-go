package bolt_test

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/bolt"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

// benchRepo returns a repository holding n blocks, each with one transaction
// that spends the previous one, and the id of each transaction.
func benchRepo(b *testing.B, n int) (*bolt.Repository, []string) {
	b.Helper()

	w, err := wallet.New()
	if err != nil {
		b.Fatal(err)
	}

	repo, err := bolt.Open(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}

	b.Cleanup(func() { _ = repo.Close() })

	blocks := make([]*domain.Block, 0, n)
	txids := make([]string, 0, n)

	var prevTx *domain.Transaction

	for i := range n {
		tx := domain.Transaction{
			Timestamp: time.Unix(1_700_000_000+int64(i), 0).UTC(),
			Outputs:   []domain.TxOut{{Amount: 1000, Address: w.Address()}},
		}
		if prevTx != nil {
			tx.Inputs = []domain.TxIn{{PrevTxID: prevTx.CalculateHash()}}
		}

		blk := &domain.Block{Index: i, Hash: fmt.Sprintf("h%d", i), Transactions: []domain.Transaction{tx}}
		if i > 0 {
			blk.PrevHash = blocks[i-1].Hash
		}

		blocks = append(blocks, blk)
		txids = append(txids, tx.CalculateHash())
		prevTx = &tx
	}
	// One transaction for the whole chain: Commit would fsync n times.
	err = repo.Append(blocks[0])
	if err != nil {
		b.Fatal(err)
	}

	if n > 1 {
		err := repo.ReplaceFrom(1, blocks[1:])
		if err != nil {
			b.Fatal(err)
		}
	}

	err = repo.Reindex()
	if err != nil {
		b.Fatal(err)
	}

	return repo, txids
}

func BenchmarkLookups(b *testing.B) {
	for _, n := range []int{1_000, 20_000} {
		repo, txids := benchRepo(b, n)
		b.Run(fmt.Sprintf("ByHash/%d", n), func(b *testing.B) {
			for i := range b.N {
				_, err := repo.ByHash(fmt.Sprintf("h%d", i%n))
				if err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("ByHeight/%d", n), func(b *testing.B) {
			for i := range b.N {
				_, err := repo.ByHeight(i % n)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("TxHeight/%d", n), func(b *testing.B) {
			for i := range b.N {
				_, err := repo.TxHeight(txids[i%n])
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
