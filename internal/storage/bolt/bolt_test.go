package bolt_test

import (
	"context"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	bbolt "go.etcd.io/bbolt"

	"github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/bolt"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/storagetest"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

var _ = Describe("Bolt Repository", func() {
	storagetest.BlockRepositoryContract(func() storage.BlockRepository {
		path := filepath.Join(GinkgoT().TempDir(), "contract.db")
		repo, err := bolt.Open(path)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			_ = repo.Close()
		})
		return repo
	})

	storagetest.IndexedRepositoryContract(func() storage.IndexedRepository {
		path := filepath.Join(GinkgoT().TempDir(), "indexed.db")
		repo, err := bolt.Open(path)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			_ = repo.Close()
		})
		return repo
	})

	It("persists blocks and hash index across Close and Open", func() {
		dbPath := filepath.Join(GinkgoT().TempDir(), "persist.db")
		repo, err := bolt.Open(dbPath)
		Expect(err).NotTo(HaveOccurred())

		w, err := wallet.New()
		Expect(err).NotTo(HaveOccurred())

		b0 := &domain.Block{
			Index:     0,
			Timestamp: time.Now().UTC(),
			Transactions: []domain.Transaction{
				{
					Outputs: []domain.TxOut{{Amount: 100, Address: w.Address()}},
				},
			},
			Nonce:      10,
			Difficulty: 1,
		}
		b0.Hash = b0.CalculateHash()
		Expect(repo.Append(b0)).To(Succeed())

		b1 := &domain.Block{
			Index:        1,
			Timestamp:    time.Now().UTC(),
			PrevHash:     b0.Hash,
			Transactions: []domain.Transaction{},
			Nonce:        20,
			Difficulty:   1,
		}
		b1.Hash = b1.CalculateHash()
		Expect(repo.Append(b1)).To(Succeed())

		Expect(repo.Close()).To(Succeed())

		reopened, err := bolt.Open(dbPath)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = reopened.Close() })

		all, err := reopened.All()
		Expect(err).NotTo(HaveOccurred())
		Expect(all).To(HaveLen(2))

		for i, b := range all {
			Expect(b.Index).To(Equal(i))
			Expect(b.CalculateHash()).To(Equal(b.Hash))
		}
		Expect(all[0].Hash).To(Equal(b0.Hash))
		Expect(all[0].Transactions).To(HaveLen(1))
		Expect(all[0].Transactions[0].Outputs[0].Address).To(Equal(w.Address()))
		Expect(all[1].Hash).To(Equal(b1.Hash))
		Expect(all[1].PrevHash).To(Equal(b0.Hash))

		byHash0, err := reopened.ByHash(b0.Hash)
		Expect(err).NotTo(HaveOccurred())
		Expect(byHash0.Index).To(Equal(0))

		byHash1, err := reopened.ByHash(b1.Hash)
		Expect(err).NotTo(HaveOccurred())
		Expect(byHash1.Index).To(Equal(1))
	})

	It("persists a replaced tail across Close and Open", func() {
		dbPath := filepath.Join(GinkgoT().TempDir(), "replace.db")
		repo, err := bolt.Open(dbPath)
		Expect(err).NotTo(HaveOccurred())
		for _, b := range []*domain.Block{
			{Index: 0, Hash: "h0"},
			{Index: 1, PrevHash: "h0", Hash: "h1"},
			{Index: 2, PrevHash: "h1", Hash: "h2"},
		} {
			Expect(repo.Append(b)).To(Succeed())
		}
		Expect(repo.ReplaceFrom(1, []*domain.Block{{Index: 1, PrevHash: "h0", Hash: "n1"}})).To(Succeed())
		Expect(repo.Close()).To(Succeed())

		reopened, err := bolt.Open(dbPath)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = reopened.Close() })

		all, err := reopened.All()
		Expect(err).NotTo(HaveOccurred())
		Expect(all).To(HaveLen(2))
		Expect(all[1].Hash).To(Equal("n1"))
		for _, gone := range []string{"h1", "h2"} {
			_, err = reopened.ByHash(gone)
			Expect(err).To(MatchError(storage.ErrNotFound))
		}
		byHash, err := reopened.ByHash("n1")
		Expect(err).NotTo(HaveOccurred())
		Expect(byHash.Index).To(Equal(1))
	})

	It("replaces blocks that carry no hash, which the hash index skips", func() {
		repo, err := bolt.Open(filepath.Join(GinkgoT().TempDir(), "nohash.db"))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = repo.Close() })
		Expect(repo.Append(&domain.Block{Index: 0})).To(Succeed())
		Expect(repo.Append(&domain.Block{Index: 1})).To(Succeed())

		Expect(repo.ReplaceFrom(1, []*domain.Block{{Index: 1}, {Index: 2}})).To(Succeed())
		all, err := repo.All()
		Expect(err).NotTo(HaveOccurred())
		Expect(all).To(HaveLen(3))
	})

	It("returns ErrNilBlock from ReplaceFrom when a block is nil", func() {
		repo, err := bolt.Open(filepath.Join(GinkgoT().TempDir(), "nil.db"))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = repo.Close() })
		Expect(repo.Append(&domain.Block{Index: 0, Hash: "h0"})).To(Succeed())

		blocks := []*domain.Block{{Index: 1, PrevHash: "h0", Hash: "h1"}, nil}
		Expect(repo.ReplaceFrom(1, blocks)).To(MatchError(bolt.ErrNilBlock))
		_, err = repo.ByHash("h1")
		Expect(err).To(MatchError(storage.ErrNotFound))
	})

	It("rolls ReplaceFrom back when a stored block cannot be decoded", func() {
		dbPath := filepath.Join(GinkgoT().TempDir(), "corrupt-replace.db")
		repo, err := bolt.Open(dbPath)
		Expect(err).NotTo(HaveOccurred())
		for _, b := range []*domain.Block{{Index: 0, Hash: "h0"}, {Index: 1, PrevHash: "h0", Hash: "h1"}, {Index: 2, PrevHash: "h1", Hash: "h2"}} {
			Expect(repo.Append(b)).To(Succeed())
		}
		Expect(repo.Close()).To(Succeed())

		rawDB, err := bbolt.Open(dbPath, 0o600, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(rawDB.Update(func(tx *bbolt.Tx) error {
			key := make([]byte, 8)
			binary.BigEndian.PutUint64(key, 2)
			return tx.Bucket([]byte("blocks")).Put(key, []byte("invalid-json{["))
		})).To(Succeed())
		Expect(rawDB.Close()).To(Succeed())

		reopened, err := bolt.Open(dbPath)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = reopened.Close() })

		err = reopened.ReplaceFrom(1, []*domain.Block{{Index: 1, PrevHash: "h0", Hash: "n1"}})
		Expect(err).To(HaveOccurred())
		Expect(err).NotTo(MatchError(storage.ErrConflict))
		for _, kept := range []string{"h0", "h1"} {
			_, err = reopened.ByHash(kept)
			Expect(err).NotTo(HaveOccurred())
		}
		_, err = reopened.ByHash("n1")
		Expect(err).To(MatchError(storage.ErrNotFound))
	})

	It("fails a second Open on the same file within 1 second due to file lock", func() {
		dbPath := filepath.Join(GinkgoT().TempDir(), "locked.db")
		repo, err := bolt.Open(dbPath)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = repo.Close() })

		start := time.Now()
		second, err := bolt.Open(dbPath)
		elapsed := time.Since(start)

		Expect(second).To(BeNil())
		Expect(err).To(HaveOccurred())
		Expect(elapsed).To(BeNumerically("<=", 1500*time.Millisecond))
		Expect(elapsed).To(BeNumerically(">=", 900*time.Millisecond))
	})

	It("returns an error without panicking when a block in the bucket is corrupted", func() {
		dbPath := filepath.Join(GinkgoT().TempDir(), "corrupt.db")
		repo, err := bolt.Open(dbPath)
		Expect(err).NotTo(HaveOccurred())

		b := &domain.Block{Index: 0, Hash: "h0"}
		Expect(repo.Append(b)).To(Succeed())
		Expect(repo.Close()).To(Succeed())

		// Corrupt raw value in bbolt directly
		rawDB, err := bbolt.Open(dbPath, 0o600, nil)
		Expect(err).NotTo(HaveOccurred())
		err = rawDB.Update(func(tx *bbolt.Tx) error {
			key := make([]byte, 8)
			binary.BigEndian.PutUint64(key, 0)
			return tx.Bucket([]byte("blocks")).Put(key, []byte("invalid-json{["))
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(rawDB.Close()).To(Succeed())

		reopened, err := bolt.Open(dbPath)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = reopened.Close() })

		Expect(func() {
			_, err = reopened.All()
		}).NotTo(Panic())
		Expect(err).To(HaveOccurred())

		Expect(func() {
			_, err = chain.NewService(reopened, pow.New(1))
		}).NotTo(Panic())
		Expect(err).To(HaveOccurred())
	})

	It("preserves chain state, rebuilds UTXO set, and rejects double-spend after restart", func() {
		dbPath := filepath.Join(GinkgoT().TempDir(), "restart.db")
		repo, err := bolt.Open(dbPath)
		Expect(err).NotTo(HaveOccurred())

		w1, err := wallet.New()
		Expect(err).NotTo(HaveOccurred())
		w2, err := wallet.New()
		Expect(err).NotTo(HaveOccurred())

		engine := pow.New(1)
		svc, err := chain.NewService(repo, engine,
			chain.WithGenesisAllocations([]domain.TxOut{
				{Amount: 50, Address: w1.Address()},
			}),
		)
		Expect(err).NotTo(HaveOccurred())

		// Genesis block should exist
		blocks, err := svc.Blocks()
		Expect(err).NotTo(HaveOccurred())
		Expect(blocks).To(HaveLen(1))
		Expect(blocks[0].Transactions).To(HaveLen(1))

		genTxID := blocks[0].Transactions[0].ID

		// Create and mine a transaction spending genesis output
		tx := domain.Transaction{
			Timestamp: time.Now(),
			Inputs: []domain.TxIn{
				{PrevTxID: genTxID, OutIndex: 0},
			},
			Outputs: []domain.TxOut{
				{Amount: 40, Address: w2.Address()},
				{Amount: 10, Address: w1.Address()},
			},
		}
		tx.ID = tx.CalculateHash()
		Expect(domain.SignInput(&tx, 0, w1)).To(Succeed())

		mined, err := svc.Mine(context.Background(), []domain.Transaction{tx})
		Expect(err).NotTo(HaveOccurred())
		Expect(mined.Index).To(Equal(1))

		beforeBlocks, err := svc.Blocks()
		Expect(err).NotTo(HaveOccurred())
		Expect(beforeBlocks).To(HaveLen(2))
		w1Before, w1UTXOsBefore := svc.Balance(w1.Address())
		w2Before, w2UTXOsBefore := svc.Balance(w2.Address())

		// Close repository
		Expect(repo.Close()).To(Succeed())

		// Reopen repository and create new Service over it
		reopened, err := bolt.Open(dbPath)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = reopened.Close() })

		// When reopened, GENESIS_ALLOCATIONS must be ignored
		w3, err := wallet.New()
		Expect(err).NotTo(HaveOccurred())
		reopenedSvc, err := chain.NewService(reopened, engine,
			chain.WithGenesisAllocations([]domain.TxOut{
				{Amount: 1000, Address: w3.Address()},
			}),
		)
		Expect(err).NotTo(HaveOccurred())

		afterBlocks, err := reopenedSvc.Blocks()
		Expect(err).NotTo(HaveOccurred())
		Expect(afterBlocks).To(HaveLen(len(beforeBlocks)))
		for i := range beforeBlocks {
			Expect(afterBlocks[i].Index).To(Equal(beforeBlocks[i].Index))
			Expect(afterBlocks[i].Hash).To(Equal(beforeBlocks[i].Hash))
			Expect(afterBlocks[i].PrevHash).To(Equal(beforeBlocks[i].PrevHash))
			Expect(afterBlocks[i].Transactions).To(Equal(beforeBlocks[i].Transactions))
		}

		// The persisted UTXO set must exactly match the one before restart
		w1After, w1UTXOsAfter := reopenedSvc.Balance(w1.Address())
		w2After, w2UTXOsAfter := reopenedSvc.Balance(w2.Address())
		Expect(w1After).To(Equal(w1Before))
		Expect(w1UTXOsAfter).To(Equal(w1UTXOsBefore))
		Expect(w2After).To(Equal(w2Before))
		Expect(w2UTXOsAfter).To(Equal(w2UTXOsBefore))
		spentGenOp := domain.OutPoint{TxID: genTxID, Index: 0}
		_, spentFound := reopened.Lookup(spentGenOp)
		Expect(spentFound).To(BeFalse())

		// Attempt double spend of the genesis output (already spent before restart)
		doubleSpendTx := domain.Transaction{
			Timestamp: time.Now(),
			Inputs: []domain.TxIn{
				{PrevTxID: genTxID, OutIndex: 0},
			},
			Outputs: []domain.TxOut{
				{Amount: 50, Address: w2.Address()},
			},
		}
		doubleSpendTx.ID = doubleSpendTx.CalculateHash()
		Expect(domain.SignInput(&doubleSpendTx, 0, w1)).To(Succeed())

		_, err = reopenedSvc.Mine(context.Background(), []domain.Transaction{doubleSpendTx})
		Expect(err).To(MatchError(domain.ErrInputMissingOrSpent))
	})

	It("returns consistent snapshots during concurrent reads and appends", func() {
		dbPath := filepath.Join(GinkgoT().TempDir(), "concurrent.db")
		repo, err := bolt.Open(dbPath)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = repo.Close() })

		genesis := &domain.Block{Index: 0, Hash: "h0"}
		Expect(repo.Append(genesis)).To(Succeed())

		const blocksToMine = 50

		var wg sync.WaitGroup
		wg.Add(2)

		// Writer
		go func() {
			defer GinkgoRecover()
			defer wg.Done()
			for i := 1; i <= blocksToMine; i++ {
				last, err := repo.Last()
				Expect(err).NotTo(HaveOccurred())
				b := &domain.Block{
					Index:    last.Index + 1,
					PrevHash: last.Hash,
					Hash:     fmt.Sprintf("h%d", last.Index+1),
				}
				Expect(repo.Append(b)).To(Succeed())
			}
		}()

		// Reader
		go func() {
			defer GinkgoRecover()
			defer wg.Done()
			for i := 0; i < 50; i++ {
				all, err := repo.All()
				Expect(err).NotTo(HaveOccurred())
				Expect(len(all)).To(BeNumerically(">=", 1))
				for j, b := range all {
					Expect(b.Index).To(Equal(j))
					if j > 0 {
						Expect(b.PrevHash).To(Equal(all[j-1].Hash))
					}
				}
			}
		}()

		wg.Wait()
	})
})

var _ = Describe("Bolt Repository with pruning", func() {
	storagetest.PrunedRepositoryContract(func(p storage.Pruning) storage.IndexedRepository {
		path := filepath.Join(GinkgoT().TempDir(), "pruned.db")
		repo, err := bolt.OpenPruned(path, p)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			_ = repo.Close()
		})
		return repo
	})
})
