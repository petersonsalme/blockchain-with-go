package bolt_test

import (
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	bbolt "go.etcd.io/bbolt"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/bolt"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/storagetest"
)

var allBucketNames = []string{"blocks", "hashes", "utxos", "undo", "txs", "addrtx", "addrutxo", "snapshot", "meta"}

var _ = Describe("Bolt pruned database", func() {
	var dbPath string

	pruning := storage.Pruning{SnapshotInterval: 3, KeepBlocks: 2}

	BeforeEach(func() {
		dbPath = filepath.Join(GinkgoT().TempDir(), "pruned.db")
	})

	populate := func(p storage.Pruning, n int) []*domain.Block {
		blocks, deltas := storagetest.PrunedChain(n)
		repo, err := bolt.OpenPruned(dbPath, p)
		Expect(err).NotTo(HaveOccurred())

		for i, b := range blocks {
			Expect(repo.Commit(b, deltas[i])).To(Succeed())
		}

		Expect(repo.Close()).To(Succeed())

		return blocks
	}

	It("rebuilds every bucket byte for byte with Reindex from the snapshot", func() {
		populate(pruning, 14)

		before := dumpBuckets(dbPath, allBucketNames...)
		for _, name := range []string{"utxos", "undo", "txs", "addrtx", "addrutxo", "snapshot", "meta"} {
			Expect(before[name]).NotTo(BeEmpty(), name)
		}

		repo, err := bolt.OpenPruned(dbPath, pruning)
		Expect(err).NotTo(HaveOccurred())
		Expect(repo.Reindex()).To(Succeed())
		Expect(repo.Close()).To(Succeed())

		Expect(dumpBuckets(dbPath, allBucketNames...)).To(Equal(before))
	})

	It("rebuilds every bucket byte for byte when snapshots do not prune", func() {
		populate(storage.Pruning{SnapshotInterval: 3}, 10)

		before := dumpBuckets(dbPath, allBucketNames...)

		repo, err := bolt.Open(dbPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(repo.Reindex()).To(Succeed())
		Expect(repo.Close()).To(Succeed())

		Expect(dumpBuckets(dbPath, allBucketNames...)).To(Equal(before))
	})

	It("keeps everything across Close and OpenPruned with the same Pruning", func() {
		populate(pruning, 14)

		before := dumpBuckets(dbPath, allBucketNames...)

		repo, err := bolt.OpenPruned(dbPath, pruning)
		Expect(err).NotTo(HaveOccurred())
		pb, err := repo.PrunedBelow()
		Expect(err).NotTo(HaveOccurred())
		Expect(pb).To(Equal(10))

		snap, err := repo.Snapshot()
		Expect(err).NotTo(HaveOccurred())
		Expect(snap.Height).To(Equal(9))

		current, err := repo.IndexCurrent()
		Expect(err).NotTo(HaveOccurred())
		Expect(current).To(BeTrue())
		Expect(repo.Close()).To(Succeed())

		Expect(dumpBuckets(dbPath, allBucketNames...)).To(Equal(before))
	})

	It("refuses to open a pruned database with KeepBlocks 0 and an invalid Pruning", func() {
		populate(pruning, 14)

		for _, p := range []storage.Pruning{{}, {SnapshotInterval: 3}} {
			_, err := bolt.OpenPruned(dbPath, p)
			Expect(err).To(MatchError(storage.ErrInvalidPruning))
		}

		_, err := bolt.OpenPruned(dbPath, storage.Pruning{KeepBlocks: 1})
		Expect(err).To(MatchError(storage.ErrInvalidPruning))
		// The refusals released the file lock.
		repo, err := bolt.OpenPruned(dbPath, pruning)
		Expect(err).NotTo(HaveOccurred())
		Expect(repo.Close()).To(Succeed())
	})

	It("reports a database without addrutxo as stale and builds it with Reindex", func() {
		blocks := populate(storage.Pruning{}, 6)
		raw, err := bbolt.Open(dbPath, 0o600, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(raw.Update(func(tx *bbolt.Tx) error {
			err := tx.DeleteBucket([]byte("addrutxo"))
			if err != nil {
				return err
			}

			return tx.DeleteBucket([]byte("snapshot"))
		})).To(Succeed())
		Expect(raw.Close()).To(Succeed())

		repo, err := bolt.Open(dbPath)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = repo.Close() })

		current, err := repo.IndexCurrent()
		Expect(err).NotTo(HaveOccurred())
		Expect(current).To(BeFalse())

		Expect(repo.Reindex()).To(Succeed())
		current, err = repo.IndexCurrent()
		Expect(err).NotTo(HaveOccurred())
		Expect(current).To(BeTrue())

		addr := blocks[0].Transactions[0].Outputs[0].Address
		got, err := repo.UTXOsByAddress(addr)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).NotTo(BeEmpty())
	})

	It("rolls back a rejected Reorg byte for byte", func() {
		populate(storage.Pruning{SnapshotInterval: 2, KeepBlocks: 1}, 11)

		before := dumpBuckets(dbPath, allBucketNames...)
		repo, err := bolt.OpenPruned(dbPath, storage.Pruning{SnapshotInterval: 2, KeepBlocks: 1})
		Expect(err).NotTo(HaveOccurred())

		blocks, deltas := storagetest.PrunedChain(11)
		// Same fork point as the original chain, so block 9 (retained) would be replaced and fail.
		Expect(repo.Reorg(9, blocks[9:], deltas[9:])).To(HaveOccurred())
		Expect(repo.Close()).To(Succeed())
		Expect(dumpBuckets(dbPath, allBucketNames...)).To(Equal(before))
	})

	It("refuses a pruned database with KeepBlocks 0 even when it predates addrutxo", func() {
		populate(pruning, 14)

		raw, err := bbolt.Open(dbPath, 0o600, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(raw.Update(func(tx *bbolt.Tx) error { return tx.DeleteBucket([]byte("addrutxo")) })).To(Succeed())
		Expect(raw.Close()).To(Succeed())

		_, err = bolt.Open(dbPath)
		Expect(err).To(MatchError(storage.ErrInvalidPruning))
	})

	It("removes every trace of a discarded snapshot", func() {
		p := storage.Pruning{SnapshotInterval: 3}
		blocks, deltas := storagetest.PrunedChain(8)
		repo, err := bolt.OpenPruned(dbPath, p)
		Expect(err).NotTo(HaveOccurred())

		for i, b := range blocks {
			Expect(repo.Commit(b, deltas[i])).To(Succeed())
		}

		Expect(repo.ReplaceFrom(6, blocks[6:7])).To(Succeed())
		Expect(repo.Close()).To(Succeed())

		dump := dumpBuckets(dbPath, "snapshot", "meta")
		Expect(dump["snapshot"]).To(BeEmpty())

		for _, kv := range dump["meta"] {
			Expect(kv[0]).NotTo(HavePrefix("snapshot"))
		}
	})
})
