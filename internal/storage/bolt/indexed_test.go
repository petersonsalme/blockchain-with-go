package bolt_test

import (
	"fmt"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	bbolt "go.etcd.io/bbolt"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/bolt"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/storagetest"
)

var indexBucketNames = []string{"utxos", "undo", "txs", "addrtx", "meta"}

var _ = Describe("Bolt IndexedRepository", func() {
	var (
		dbPath               string
		mainChain, altChain  []*domain.Block
		mainDelta, altDeltas []domain.UTXODelta
	)

	BeforeEach(func() {
		dbPath = filepath.Join(GinkgoT().TempDir(), "indexed.db")
		mainChain, altChain, mainDelta, altDeltas = storagetest.IndexedChains()
	})

	open := func() *bolt.Repository {
		repo, err := bolt.Open(dbPath)
		Expect(err).NotTo(HaveOccurred())

		return repo
	}

	It("keeps the UTXO set, the indexes and the byte layout across Close and Open", func() {
		repo := open()
		for i, b := range mainChain {
			Expect(repo.Commit(b, mainDelta[i])).To(Succeed())
		}

		Expect(repo.Reorg(2, altChain[2:], altDeltas[2:])).To(Succeed())
		spent := altChain[3].Transactions[0]
		id := spent.CalculateHash()
		height, err := repo.TxHeight(id)
		Expect(err).NotTo(HaveOccurred())
		Expect(repo.Close()).To(Succeed())

		before := dumpBuckets(dbPath, indexBucketNames...)

		reopened := open()

		DeferCleanup(func() { _ = reopened.Close() })

		current, err := reopened.IndexCurrent()
		Expect(err).NotTo(HaveOccurred())
		Expect(current).To(BeTrue())

		got, err := reopened.TxHeight(id)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(height))

		for op, o := range altDeltas[4].Created {
			found, ok := reopened.Lookup(op)
			Expect(ok).To(BeTrue())
			Expect(found).To(Equal(o))
		}

		ids, err := reopened.TxsByAddress(spent.Outputs[0].Address)
		Expect(err).NotTo(HaveOccurred())
		Expect(ids).To(ContainElement(id))
		Expect(reopened.Close()).To(Succeed())
		Expect(dumpBuckets(dbPath, indexBucketNames...)).To(Equal(before))
	})

	It("rebuilds exactly the same bytes in the five index buckets with Reindex", func() {
		repo := open()
		for i, b := range mainChain {
			Expect(repo.Commit(b, mainDelta[i])).To(Succeed())
		}

		Expect(repo.Reorg(2, altChain[2:], altDeltas[2:])).To(Succeed())
		Expect(repo.Close()).To(Succeed())

		before := dumpBuckets(dbPath, indexBucketNames...)
		for _, name := range indexBucketNames {
			Expect(before[name]).NotTo(BeEmpty(), name)
		}

		repo = open()
		Expect(repo.Reindex()).To(Succeed())
		Expect(repo.Close()).To(Succeed())

		Expect(dumpBuckets(dbPath, indexBucketNames...)).To(Equal(before))
	})

	It("opens a database written before the indexes, reports it stale and fixes it with Reindex", func() {
		legacy, err := bbolt.Open(dbPath, 0o600, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(legacy.Update(func(tx *bbolt.Tx) error {
			for _, name := range []string{"blocks", "hashes"} {
				_, err := tx.CreateBucket([]byte(name))
				if err != nil {
					return err
				}
			}

			return nil
		})).To(Succeed())
		Expect(legacy.Close()).To(Succeed())

		repo := open()
		for _, b := range mainChain {
			Expect(repo.Append(b)).To(Succeed())
		}

		Expect(repo.Close()).To(Succeed())

		reopened := open()

		DeferCleanup(func() { _ = reopened.Close() })

		current, err := reopened.IndexCurrent()
		Expect(err).NotTo(HaveOccurred())
		Expect(current).To(BeFalse())

		_, err = reopened.TxHeight(mainChain[0].Transactions[0].CalculateHash())
		Expect(err).To(HaveOccurred())

		Expect(reopened.Reindex()).To(Succeed())
		current, err = reopened.IndexCurrent()
		Expect(err).NotTo(HaveOccurred())
		Expect(current).To(BeTrue())

		h, err := reopened.TxHeight(mainChain[3].Transactions[0].CalculateHash())
		Expect(err).NotTo(HaveOccurred())
		Expect(h).To(Equal(3))
	})

	It("rolls a rejected Commit back at the byte level", func() {
		repo := open()
		for i, b := range mainChain[:3] {
			Expect(repo.Commit(b, mainDelta[i])).To(Succeed())
		}

		Expect(repo.Close()).To(Succeed())

		before := dumpBuckets(dbPath, append([]string{"blocks", "hashes"}, indexBucketNames...)...)

		repo = open()
		bad := domain.UTXODelta{
			Spent:   map[domain.OutPoint]domain.TxOut{{TxID: "nope"}: {Amount: 1}},
			Created: mainDelta[3].Created,
		}
		Expect(repo.Commit(mainChain[3], bad)).To(HaveOccurred())
		Expect(repo.Close()).To(Succeed())

		Expect(dumpBuckets(dbPath, append([]string{"blocks", "hashes"}, indexBucketNames...)...)).To(Equal(before))
	})

	It("reports a corrupted undo record from Reorg without changing anything", func() {
		repo := open()
		for i, b := range mainChain {
			Expect(repo.Commit(b, mainDelta[i])).To(Succeed())
		}

		Expect(repo.Close()).To(Succeed())

		raw, err := bbolt.Open(dbPath, 0o600, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(raw.Update(func(tx *bbolt.Tx) error {
			return tx.Bucket([]byte("undo")).Put([]byte{0, 0, 0, 0, 0, 0, 0, 2}, []byte("{["))
		})).To(Succeed())
		Expect(raw.Close()).To(Succeed())

		before := dumpBuckets(dbPath, append([]string{"blocks", "hashes"}, indexBucketNames...)...)

		repo = open()
		Expect(repo.Reorg(2, altChain[2:], altDeltas[2:])).To(HaveOccurred())
		Expect(repo.Close()).To(Succeed())
		Expect(dumpBuckets(dbPath, append([]string{"blocks", "hashes"}, indexBucketNames...)...)).To(Equal(before))
	})

	It("stores the undo delta sorted by txid, then output index", func() {
		repo := open()
		outs := func(ops ...domain.OutPoint) map[domain.OutPoint]domain.TxOut {
			m := map[domain.OutPoint]domain.TxOut{}
			for _, op := range ops {
				m[op] = domain.TxOut{Amount: 1, Address: "x"}
			}

			return m
		}
		a0, a1, a2 := domain.OutPoint{TxID: "a", Index: 0}, domain.OutPoint{TxID: "a", Index: 1}, domain.OutPoint{TxID: "a", Index: 2}
		b0, c0 := domain.OutPoint{TxID: "b"}, domain.OutPoint{TxID: "c"}
		Expect(repo.Commit(&domain.Block{Index: 0, Hash: "h0"}, domain.UTXODelta{Created: outs(c0, a2, b0, a0, a1)})).To(Succeed())
		Expect(repo.Commit(&domain.Block{Index: 1, PrevHash: "h0", Hash: "h1"}, domain.UTXODelta{Spent: outs(c0, a1, b0, a0, a2)})).To(Succeed())
		Expect(repo.Close()).To(Succeed())

		o := func(txid string, i int) string {
			return fmt.Sprintf(`{"TxID":%q,"Index":%d,"Amount":1,"Address":"x"}`, txid, i)
		}
		list := "[" + o("a", 0) + "," + o("a", 1) + "," + o("a", 2) + "," + o("b", 0) + "," + o("c", 0) + "]"
		undo := dumpBuckets(dbPath, "undo")["undo"]
		Expect(undo).To(HaveLen(2))
		Expect(undo[0][1]).To(Equal(`{"Spent":[],"Created":` + list + `}`))
		Expect(undo[1][1]).To(Equal(`{"Spent":` + list + `,"Created":[]}`))
	})
})
