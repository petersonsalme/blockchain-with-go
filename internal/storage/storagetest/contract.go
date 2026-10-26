package storagetest

import (
	"fmt"
	"maps"
	"math"
	"sort"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

type rejection int

const (
	conflict rejection = iota
	nilBlock
)

// BlockRepositoryContract registers Ginkgo specs that every storage.BlockRepository
// implementation must satisfy.
func BlockRepositoryContract(newRepo func() storage.BlockRepository) {
	var repo storage.BlockRepository

	BeforeEach(func() {
		repo = newRepo()
	})

	It("returns ErrEmpty from Last when empty", func() {
		b, err := repo.Last()
		Expect(b).To(BeNil())
		Expect(err).To(MatchError(storage.ErrEmpty))
	})

	It("returns an empty, non-nil snapshot from All when empty", func() {
		all, err := repo.All()
		Expect(err).NotTo(HaveOccurred())
		Expect(all).NotTo(BeNil())
		Expect(all).To(BeEmpty())
	})

	It("appends in order and returns the last block", func() {
		a := &domain.Block{Index: 0, Hash: "h0"}
		b := &domain.Block{Index: 1, PrevHash: "h0", Hash: "h1"}

		Expect(repo.Append(a)).To(Succeed())
		lastA, err := repo.Last()
		Expect(err).NotTo(HaveOccurred())
		Expect(lastA.Hash).To(Equal(a.Hash))

		Expect(repo.Append(b)).To(Succeed())

		last, err := repo.Last()
		Expect(err).NotTo(HaveOccurred())
		Expect(last.Index).To(Equal(b.Index))
		Expect(last.Hash).To(Equal(b.Hash))
		Expect(last.PrevHash).To(Equal(b.PrevHash))

		all, err := repo.All()
		Expect(err).NotTo(HaveOccurred())
		Expect(all).To(HaveLen(2))
		Expect(all[0].Index).To(Equal(a.Index))
		Expect(all[0].Hash).To(Equal(a.Hash))
		Expect(all[1].Index).To(Equal(b.Index))
		Expect(all[1].Hash).To(Equal(b.Hash))
	})

	It("rejects a nil block and stores nothing", func() {
		Expect(repo.Append(nil)).To(HaveOccurred())
		all, err := repo.All()
		Expect(err).NotTo(HaveOccurred())
		Expect(all).To(BeEmpty())
	})

	It("returns a snapshot that later appends and caller edits do not affect", func() {
		Expect(repo.Append(&domain.Block{Index: 0, Hash: "h0"})).To(Succeed())
		snap, err := repo.All()
		Expect(err).NotTo(HaveOccurred())
		Expect(snap).To(HaveLen(1))

		Expect(repo.Append(&domain.Block{Index: 1, PrevHash: "h0", Hash: "h1"})).To(Succeed())
		Expect(snap).To(HaveLen(1))

		snap[0] = nil
		again, err := repo.All()
		Expect(err).NotTo(HaveOccurred())
		Expect(again[0]).NotTo(BeNil())
	})

	DescribeTable("rejects a block that does not extend the last one with ErrConflict and keeps the chain",
		func(b *domain.Block) {
			genesis := &domain.Block{Index: 0, Hash: "h0"}
			Expect(repo.Append(genesis)).To(Succeed())

			Expect(repo.Append(b)).To(MatchError(storage.ErrConflict))

			all, err := repo.All()
			Expect(err).NotTo(HaveOccurred())
			Expect(all).To(HaveLen(1))
			Expect(all[0].Index).To(Equal(genesis.Index))
			Expect(all[0].Hash).To(Equal(genesis.Hash))
		},
		Entry("wrong PrevHash", &domain.Block{Index: 1, PrevHash: "other", Hash: "h1"}),
		Entry("duplicate Index", &domain.Block{Index: 0, PrevHash: "h0", Hash: "h1"}),
		Entry("skipped Index", &domain.Block{Index: 2, PrevHash: "h0", Hash: "h2"}),
	)

	It("accepts a block that extends the last one", func() {
		Expect(repo.Append(&domain.Block{Index: 0, Hash: "h0"})).To(Succeed())
		Expect(repo.Append(&domain.Block{Index: 1, PrevHash: "h0", Hash: "h1"})).To(Succeed())
		all, err := repo.All()
		Expect(err).NotTo(HaveOccurred())
		Expect(all).To(HaveLen(2))
	})

	It("keeps the chain linked under concurrent Append and All", func() {
		const writers, perWriter = 8, 30

		var wg sync.WaitGroup
		for range writers {
			wg.Add(2)

			go func() {
				defer GinkgoRecover()
				defer wg.Done()

				for i, attempts := 0, 0; i < perWriter; attempts++ {
					Expect(attempts).To(BeNumerically("<", 2000), "writer starved: valid appends keep being rejected")

					last, err := repo.Last()
					if err != nil {
						Expect(repo.Append(&domain.Block{Hash: "h0"})).To(Or(Succeed(), MatchError(storage.ErrConflict)))
						continue
					}

					next := &domain.Block{Index: last.Index + 1, PrevHash: last.Hash, Hash: fmt.Sprintf("h%d", last.Index+1)}

					err = repo.Append(next)
					if err == nil {
						i++
					} else {
						Expect(err).To(MatchError(storage.ErrConflict))
					}
				}
			}()
			go func() {
				defer GinkgoRecover()
				defer wg.Done()

				for range perWriter {
					_, err := repo.All()
					Expect(err).NotTo(HaveOccurred())

					_, _ = repo.Last()
				}
			}()
		}

		wg.Wait()

		all, err := repo.All()
		Expect(err).NotTo(HaveOccurred())
		Expect(all).To(HaveLen(writers*perWriter + 1))

		for i, b := range all {
			Expect(b.Index).To(Equal(i))

			if i > 0 {
				Expect(b.PrevHash).To(Equal(all[i-1].Hash))
			}
		}
	})

	Describe("lookups", func() {
		var blocks []*domain.Block

		BeforeEach(func() {
			blocks = nil

			for i := range 5 {
				b := &domain.Block{Index: i, Hash: fmt.Sprintf("h%d", i)}
				if i > 0 {
					b.PrevHash = blocks[i-1].Hash
				}

				Expect(repo.Append(b)).To(Succeed())
				blocks = append(blocks, b)
			}
		})

		It("finds every block by hash and by height", func() {
			for _, b := range blocks {
				byHash, err := repo.ByHash(b.Hash)
				Expect(err).NotTo(HaveOccurred())
				Expect(byHash.Index).To(Equal(b.Index))
				Expect(byHash.Hash).To(Equal(b.Hash))

				byHeight, err := repo.ByHeight(b.Index)
				Expect(err).NotTo(HaveOccurred())
				Expect(byHeight.Index).To(Equal(b.Index))
				Expect(byHeight.Hash).To(Equal(b.Hash))
			}
		})

		It("returns ErrNotFound for a hash or height outside the chain", func() {
			b, err := repo.ByHash("unknown")
			Expect(b).To(BeNil())
			Expect(err).To(MatchError(storage.ErrNotFound))

			for _, h := range []int{-1, 5, 100} {
				b, err := repo.ByHeight(h)
				Expect(b).To(BeNil())
				Expect(err).To(MatchError(storage.ErrNotFound))
			}
		})

		It("returns ErrNotFound on an empty repository", func() {
			empty := newRepo()
			_, err := empty.ByHeight(0)
			Expect(err).To(MatchError(storage.ErrNotFound))
			_, err = empty.ByHash("h0")
			Expect(err).To(MatchError(storage.ErrNotFound))
		})

		DescribeTable("Range returns a window of the chain in order",
			func(from, limit, first, count int) {
				got, err := repo.Range(from, limit)
				Expect(err).NotTo(HaveOccurred())
				Expect(got).To(HaveLen(count))

				for i, b := range got {
					Expect(b.Index).To(Equal(blocks[first+i].Index))
					Expect(b.Hash).To(Equal(blocks[first+i].Hash))
				}
			},
			Entry("from the start", 0, 2, 0, 2),
			Entry("from the middle", 2, 2, 2, 2),
			Entry("limit past the tip", 3, 10, 3, 2),
			Entry("whole chain", 0, 5, 0, 5),
			Entry("last block", 4, 1, 4, 1),
			Entry("huge limit", 1, math.MaxInt, 1, 4),
		)

		DescribeTable("Range returns an empty, non-nil slice when nothing matches",
			func(from, limit int) {
				got, err := repo.Range(from, limit)
				Expect(err).NotTo(HaveOccurred())
				Expect(got).NotTo(BeNil())
				Expect(got).To(BeEmpty())
			},
			Entry("from at the tip+1", 5, 3),
			Entry("from far past the tip", 99, 3),
			Entry("negative from", -1, 3),
			Entry("zero limit", 0, 0),
			Entry("negative limit", 0, -2),
		)

		It("returns a Range copy that caller edits do not affect", func() {
			got, err := repo.Range(0, 2)
			Expect(err).NotTo(HaveOccurred())

			got[0] = nil
			again, err := repo.Range(0, 2)
			Expect(err).NotTo(HaveOccurred())
			Expect(again[0]).NotTo(BeNil())
			Expect(again[0].Index).To(Equal(blocks[0].Index))
		})

		It("leaves the lookups unchanged after a rejected Append", func() {
			rejected := &domain.Block{Index: 9, PrevHash: "nope", Hash: "rejected"}
			Expect(repo.Append(rejected)).To(MatchError(storage.ErrConflict))
			Expect(repo.Append(nil)).To(HaveOccurred())

			_, err := repo.ByHash("rejected")
			Expect(err).To(MatchError(storage.ErrNotFound))
			_, err = repo.ByHeight(9)
			Expect(err).To(MatchError(storage.ErrNotFound))
			got, err := repo.Range(0, 100)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(HaveLen(len(blocks)))

			for i, b := range got {
				Expect(b.Index).To(Equal(blocks[i].Index))
				Expect(b.Hash).To(Equal(blocks[i].Hash))
			}
		})

		It("keeps the lookups consistent under concurrent Append", func() {
			var wg sync.WaitGroup
			wg.Add(2)

			go func() {
				defer GinkgoRecover()
				defer wg.Done()

				for i := 5; i < 100; i++ {
					prev, err := repo.Last()
					Expect(err).NotTo(HaveOccurred())
					Expect(repo.Append(&domain.Block{Index: i, PrevHash: prev.Hash, Hash: fmt.Sprintf("h%d", i)})).To(Succeed())
				}
			}()
			go func() {
				defer GinkgoRecover()
				defer wg.Done()

				for range 100 {
					page, err := repo.Range(0, 1000)
					Expect(err).NotTo(HaveOccurred())

					for j, b := range page {
						Expect(b.Index).To(Equal(j))
						byHash, err := repo.ByHash(b.Hash)
						Expect(err).NotTo(HaveOccurred())
						Expect(byHash.Index).To(Equal(b.Index))
						Expect(byHash.Hash).To(Equal(b.Hash))
					}
				}
			}()

			wg.Wait()
		})
	})
	Describe("ReplaceFrom", func() {
		// link builds n blocks from index `from`, chained to prev, with hashes tag0, tag1, ...
		link := func(tag string, from, n int, prev string) []*domain.Block {
			out := make([]*domain.Block, 0, n)
			for i := range n {
				b := &domain.Block{Index: from + i, PrevHash: prev, Hash: fmt.Sprintf("%s%d", tag, from+i)}
				out = append(out, b)
				prev = b.Hash
			}

			return out
		}

		var chain []*domain.Block

		BeforeEach(func() {
			chain = link("h", 0, 5, "")
			for _, b := range chain {
				Expect(repo.Append(b)).To(Succeed())
			}
		})

		expectChain := func(want []*domain.Block) {
			all, err := repo.All()
			Expect(err).NotTo(HaveOccurred())
			Expect(all).To(HaveLen(len(want)))

			for i, w := range want {
				Expect(all[i].Index).To(Equal(w.Index))
				Expect(all[i].PrevHash).To(Equal(w.PrevHash))
				Expect(all[i].Hash).To(Equal(w.Hash))

				byHeight, err := repo.ByHeight(i)
				Expect(err).NotTo(HaveOccurred())
				Expect(byHeight.Hash).To(Equal(w.Hash))
				byHash, err := repo.ByHash(w.Hash)
				Expect(err).NotTo(HaveOccurred())
				Expect(byHash.Index).To(Equal(w.Index))
			}

			last, err := repo.Last()
			Expect(err).NotTo(HaveOccurred())
			Expect(last.Hash).To(Equal(want[len(want)-1].Hash))
			_, err = repo.ByHeight(len(want))
			Expect(err).To(MatchError(storage.ErrNotFound))
			window, err := repo.Range(0, 1000)
			Expect(err).NotTo(HaveOccurred())
			Expect(window).To(HaveLen(len(want)))

			for i, w := range want {
				Expect(window[i].Hash).To(Equal(w.Hash))
			}
		}

		DescribeTable("replaces the tail and reflects chain[:height] ++ blocks everywhere",
			func(height, n int) {
				old := append([]*domain.Block(nil), chain...)
				fork := link("n", height, n, chain[height-1].Hash)

				Expect(repo.ReplaceFrom(height, fork)).To(Succeed())

				expectChain(append(append([]*domain.Block(nil), old[:height]...), fork...))

				for _, removed := range old[height:] {
					_, err := repo.ByHash(removed.Hash)
					Expect(err).To(MatchError(storage.ErrNotFound))
				}
			},
			Entry("everything but the genesis, longer", 1, 6),
			Entry("everything but the genesis, shorter", 1, 2),
			Entry("the last block only", 4, 1),
			Entry("the middle, longer", 2, 5),
			Entry("the middle, shorter", 3, 1),
			Entry("height == len extends the chain", 5, 3),
		)

		It("accepts the same blocks again (a no-op swap)", func() {
			Expect(repo.ReplaceFrom(2, chain[2:])).To(Succeed())
			expectChain(chain)
		})

		It("keeps accepting Append after a swap", func() {
			fork := link("n", 2, 1, chain[1].Hash)
			Expect(repo.ReplaceFrom(2, fork)).To(Succeed())
			next := link("m", 3, 1, fork[0].Hash)
			Expect(repo.Append(next[0])).To(Succeed())
			expectChain(append(append(append([]*domain.Block(nil), chain[:2]...), fork...), next...))
		})

		DescribeTable("rejects and leaves the repository as it was",
			func(height int, mk func() []*domain.Block, want rejection) {
				blocks := mk()

				err := repo.ReplaceFrom(height, blocks)
				if want == conflict {
					Expect(err).To(MatchError(storage.ErrConflict))
				} else {
					Expect(err).To(HaveOccurred())
					Expect(err).NotTo(MatchError(storage.ErrConflict))
				}

				expectChain(chain)

				for _, b := range blocks {
					if b != nil {
						_, err := repo.ByHash(b.Hash)
						Expect(err).To(MatchError(storage.ErrNotFound))
					}
				}
			},
			Entry("height 0 (the genesis never leaves)", 0, func() []*domain.Block { return link("n", 0, 3, "") }, conflict),
			Entry("negative height", -1, func() []*domain.Block { return link("n", -1, 3, "") }, conflict),
			Entry("height past len", 6, func() []*domain.Block { return link("n", 6, 2, "h5") }, conflict),
			Entry("empty blocks", 2, func() []*domain.Block { return []*domain.Block{} }, conflict),
			Entry("nil blocks", 2, func() []*domain.Block { return nil }, conflict),
			Entry("first Index below height", 2, func() []*domain.Block { return link("n", 1, 2, "h0") }, conflict),
			Entry("first Index above height", 2, func() []*domain.Block { return link("n", 3, 2, "h1") }, conflict),
			Entry("first PrevHash not chain[height-1]", 2, func() []*domain.Block { return link("n", 2, 2, "other") }, conflict),
			Entry("first PrevHash is the old tip instead of the fork parent", 2, func() []*domain.Block { return link("n", 2, 2, "h4") }, conflict),
			Entry("broken PrevHash in the middle", 1, func() []*domain.Block {
				bs := link("n", 1, 4, "h0")
				bs[2].PrevHash = "other"

				return bs
			}, conflict),
			Entry("broken PrevHash in the last block", 1, func() []*domain.Block {
				bs := link("n", 1, 3, "h0")
				bs[2].PrevHash = "other"

				return bs
			}, conflict),
			Entry("skipped Index in the last block", 1, func() []*domain.Block {
				bs := link("n", 1, 3, "h0")
				bs[2].Index = 4

				return bs
			}, conflict),
			Entry("repeated Index in the last block", 1, func() []*domain.Block {
				bs := link("n", 1, 3, "h0")
				bs[2].Index = 2

				return bs
			}, conflict),
			Entry("nil in the middle", 1, func() []*domain.Block {
				bs := link("n", 1, 3, "h0")
				bs[1] = nil

				return bs
			}, nilBlock),
			Entry("nil first", 1, func() []*domain.Block {
				bs := link("n", 1, 3, "h0")
				bs[0] = nil

				return bs
			}, nilBlock),
			Entry("nil last", 1, func() []*domain.Block {
				bs := link("n", 1, 3, "h0")
				bs[2] = nil

				return bs
			}, nilBlock),
			Entry("nil with an invalid height", 0, func() []*domain.Block { return []*domain.Block{nil} }, nilBlock),
			Entry("nil with a height past len", 6, func() []*domain.Block { return []*domain.Block{nil} }, nilBlock),
			Entry("nil after a broken link", 1, func() []*domain.Block {
				bs := link("n", 1, 1, "other")
				return append(bs, nil)
			}, nilBlock),
		)

		It("returns ErrConflict on an empty repository", func() {
			empty := newRepo()
			Expect(empty.ReplaceFrom(0, link("n", 0, 2, ""))).To(MatchError(storage.ErrConflict))
			Expect(empty.ReplaceFrom(1, link("n", 1, 2, "h0"))).To(MatchError(storage.ErrConflict))
			nilErr := empty.ReplaceFrom(1, []*domain.Block{nil})
			Expect(nilErr).To(HaveOccurred())
			Expect(nilErr).NotTo(MatchError(storage.ErrConflict))

			all, err := empty.All()
			Expect(err).NotTo(HaveOccurred())
			Expect(all).To(BeEmpty())
		})

		It("shows readers either the old or the new chain under concurrent ReplaceFrom and Append", func() {
			const rounds = 150

			stop := make(chan struct{})

			var readers, writers sync.WaitGroup
			for range 4 {
				readers.Add(1)

				go func() {
					defer GinkgoRecover()
					defer readers.Done()

					for {
						select {
						case <-stop:
							return
						default:
						}

						all, err := repo.All()
						Expect(err).NotTo(HaveOccurred())
						Expect(len(all)).To(BeNumerically(">=", 2))

						for i, b := range all {
							Expect(b.Index).To(Equal(i))

							if i > 0 {
								Expect(b.PrevHash).To(Equal(all[i-1].Hash))
							}
						}
					}
				}()
			}

			writers.Add(2)

			go func() {
				defer GinkgoRecover()
				defer writers.Done()

				for i := range rounds {
					tag := fmt.Sprintf("v%d-", i)
					Expect(repo.ReplaceFrom(2, link(tag, 2, 1+i%4, "h1"))).To(Succeed())
				}
			}()
			go func() {
				defer GinkgoRecover()
				defer writers.Done()

				for i := range rounds {
					last, err := repo.Last()
					Expect(err).NotTo(HaveOccurred())

					err = repo.Append(&domain.Block{Index: last.Index + 1, PrevHash: last.Hash, Hash: fmt.Sprintf("a%d-%d", i, last.Index+1)})
					if err != nil {
						Expect(err).To(MatchError(storage.ErrConflict))
					}
				}
			}()

			writers.Wait()
			close(stop)
			readers.Wait()
		})
	})
}

// indexedWorld builds chains of real transactions together with the delta of each block.
type indexedWorld struct {
	set    domain.UTXOSet
	blocks []*domain.Block
	deltas []domain.UTXODelta
}

func newIndexedWorld() *indexedWorld { return &indexedWorld{set: domain.UTXOSet{}} }

// forkAt returns a world holding the first height blocks of w.
func (w *indexedWorld) forkAt(height int) *indexedWorld {
	f := newIndexedWorld()
	for i := range height {
		f.blocks = append(f.blocks, w.blocks[i])
		f.deltas = append(f.deltas, w.deltas[i])
		f.set.Apply(w.deltas[i])
	}

	return f
}

// mine appends a block with txs, tagging its hash.
func (w *indexedWorld) mine(tag string, txs ...domain.Transaction) {
	b := &domain.Block{Index: len(w.blocks), Hash: fmt.Sprintf("%s%d", tag, len(w.blocks)), Transactions: txs}
	if len(w.blocks) > 0 {
		b.PrevHash = w.blocks[len(w.blocks)-1].Hash
	}

	d, err := domain.BlockDelta(w.set, b, b.Index == 0)
	Expect(err).NotTo(HaveOccurred())
	w.set.Apply(d)
	w.blocks = append(w.blocks, b)
	w.deltas = append(w.deltas, d)
}

func indexedTx(salt int64, ins []domain.OutPoint, outs ...domain.TxOut) domain.Transaction {
	tx := domain.Transaction{Timestamp: time.Unix(1_700_000_000+salt, 0).UTC(), Outputs: outs}
	for _, op := range ins {
		tx.Inputs = append(tx.Inputs, domain.TxIn{PrevTxID: op.TxID, OutIndex: op.Index})
	}

	tx.ID = tx.CalculateHash()

	return tx
}

func spend(tx domain.Transaction, i uint32) domain.OutPoint {
	return domain.OutPoint{TxID: tx.CalculateHash(), Index: i}
}

// indexedFixture holds two branches that share blocks 0 and 1.
type indexedFixture struct {
	addrA, addrB, addrC string
	main, alt           *indexedWorld
	txids               []string
	outpoints           []domain.OutPoint
}

func newIndexedFixture() *indexedFixture {
	mk := func() string {
		w, err := wallet.New()
		Expect(err).NotTo(HaveOccurred())

		return w.Address()
	}
	f := &indexedFixture{addrA: mk(), addrB: mk(), addrC: mk()}
	A, B, C := f.addrA, f.addrB, f.addrC

	f.main = newIndexedWorld()
	g := indexedTx(0, nil, domain.TxOut{Amount: 100, Address: A}, domain.TxOut{Amount: 50, Address: B})
	f.main.mine("m", g)
	t1 := indexedTx(1, []domain.OutPoint{spend(g, 0)}, domain.TxOut{Amount: 60, Address: B}, domain.TxOut{Amount: 40, Address: A})
	f.main.mine("m", t1)
	// t2 pays C, t3 spends an output t2 created in the same block.
	t2 := indexedTx(2, []domain.OutPoint{spend(t1, 0)}, domain.TxOut{Amount: 30, Address: C}, domain.TxOut{Amount: 30, Address: C})
	t3 := indexedTx(3, []domain.OutPoint{spend(t2, 0)}, domain.TxOut{Amount: 30, Address: A})
	f.main.mine("m", t2, t3)
	t4 := indexedTx(4, []domain.OutPoint{spend(t3, 0), spend(g, 1)}, domain.TxOut{Amount: 80, Address: B})
	f.main.mine("m", t4)

	f.alt = f.main.forkAt(2)
	a2 := indexedTx(12, []domain.OutPoint{spend(t1, 1)}, domain.TxOut{Amount: 40, Address: C})
	f.alt.mine("a", a2)
	a3 := indexedTx(13, []domain.OutPoint{spend(a2, 0)}, domain.TxOut{Amount: 25, Address: A}, domain.TxOut{Amount: 15, Address: B})
	f.alt.mine("a", a3)
	a4 := indexedTx(14, []domain.OutPoint{spend(a3, 1), spend(t1, 0)}, domain.TxOut{Amount: 70, Address: C})
	f.alt.mine("a", a4)

	for _, tx := range []domain.Transaction{g, t1, t2, t3, t4, a2, a3, a4} {
		f.txids = append(f.txids, tx.CalculateHash())
		for i := range tx.Outputs {
			f.outpoints = append(f.outpoints, spend(tx, uint32(i)))
		}
	}

	return f
}

// indexedState is everything observable through the IndexedRepository port.
type indexedState struct {
	hashes  []string
	utxos   map[domain.OutPoint]domain.TxOut
	heights map[string]int
	byAddr  map[string][]string
	current bool
}

func (f *indexedFixture) observe(repo storage.IndexedRepository) indexedState {
	st := indexedState{utxos: map[domain.OutPoint]domain.TxOut{}, heights: map[string]int{}, byAddr: map[string][]string{}}
	all, err := repo.All()
	Expect(err).NotTo(HaveOccurred())

	for _, b := range all {
		st.hashes = append(st.hashes, b.Hash)
	}

	for _, op := range f.outpoints {
		if o, ok := repo.Lookup(op); ok {
			st.utxos[op] = o
		}
	}

	for _, id := range f.txids {
		h, err := repo.TxHeight(id)
		if err != nil {
			Expect(err).To(MatchError(storage.ErrNotFound))

			h = -1
		}

		st.heights[id] = h
	}

	for _, a := range []string{f.addrA, f.addrB, f.addrC} {
		ids, err := repo.TxsByAddress(a)
		Expect(err).NotTo(HaveOccurred())
		Expect(ids).NotTo(BeNil())
		st.byAddr[a] = ids
	}

	st.current, err = repo.IndexCurrent()
	Expect(err).NotTo(HaveOccurred())

	return st
}

func commitAll(repo storage.IndexedRepository, blocks []*domain.Block, deltas []domain.UTXODelta) {
	for i, b := range blocks {
		Expect(repo.Commit(b, deltas[i])).To(Succeed())
	}
}

// IndexedRepositoryContract registers Ginkgo specs that every
// storage.IndexedRepository implementation must satisfy.
func IndexedRepositoryContract(newRepo func() storage.IndexedRepository) {
	var (
		repo storage.IndexedRepository
		fx   *indexedFixture
	)

	BeforeEach(func() {
		repo = newRepo()
		fx = newIndexedFixture()
	})

	// fresh returns a repository holding only Commits of w.
	fresh := func(w *indexedWorld) storage.IndexedRepository {
		r := newRepo()
		commitAll(r, w.blocks, w.deltas)

		return r
	}

	Describe("an empty repository", func() {
		It("is current and finds nothing", func() {
			st := fx.observe(repo)
			Expect(st.current).To(BeTrue())
			Expect(st.utxos).To(BeEmpty())

			for _, h := range st.heights {
				Expect(h).To(Equal(-1))
			}

			for _, ids := range st.byAddr {
				Expect(ids).To(BeEmpty())
			}
		})
	})

	Describe("Restore", func() {
		// headersOf returns n linked headers whose Hash is the CalculateHash of the fields they carry.
		headersOf := func(n int) []*domain.Block {
			out := make([]*domain.Block, n)
			for i := range out {
				b := &domain.Block{Index: i, Timestamp: time.Unix(1_700_000_000+int64(i), 0).UTC(), MerkleRoot: fmt.Sprintf("mr%d", i), Transactions: []domain.Transaction{}}
				if i > 0 {
					b.PrevHash = out[i-1].Hash
				}
				// The genesis of a real chain is never sealed: its Hash stays empty.
				if i > 0 {
					b.Hash = b.CalculateHash()
				}

				out[i] = b
			}

			return out
		}
		copySet := func(src domain.UTXOSet) domain.UTXOSet {
			set := domain.UTXOSet{}
			maps.Copy(set, src)

			return set
		}
		expectEmpty := func(r storage.IndexedRepository) {
			all, err := r.All()
			Expect(err).NotTo(HaveOccurred())
			Expect(all).To(BeEmpty())

			_, err = r.Snapshot()
			Expect(err).To(MatchError(storage.ErrNotFound))
			below, err := r.PrunedBelow()
			Expect(err).NotTo(HaveOccurred())
			Expect(below).To(BeZero())

			for _, op := range fx.outpoints {
				_, ok := r.Lookup(op)
				Expect(ok).To(BeFalse())
			}

			for _, a := range []string{fx.addrA, fx.addrB, fx.addrC} {
				owned, err := r.UTXOsByAddress(a)
				Expect(err).NotTo(HaveOccurred())
				Expect(owned).To(BeEmpty())
			}
		}

		It("stores the headers, the UTXO set, the snapshot and the pruning marks", func() {
			headers := headersOf(5)
			set := copySet(fx.main.set)
			Expect(repo.Restore(headers, set)).To(Succeed())

			all, err := repo.All()
			Expect(err).NotTo(HaveOccurred())
			Expect(all).To(HaveLen(5))

			for i, b := range all {
				Expect(b.Hash).To(Equal(headers[i].Hash))
				Expect(b.PrevHash).To(Equal(headers[i].PrevHash))
				Expect(b.MerkleRoot).To(Equal(headers[i].MerkleRoot))
				Expect(b.Transactions).To(BeEmpty())

				if i == 0 {
					continue
				}

				byHash, err := repo.ByHash(headers[i].Hash)
				Expect(err).NotTo(HaveOccurred())
				Expect(byHash.Index).To(Equal(i))
			}

			last, err := repo.Last()
			Expect(err).NotTo(HaveOccurred())
			Expect(last.Hash).To(Equal(headers[4].Hash))

			snap, err := repo.Snapshot()
			Expect(err).NotTo(HaveOccurred())
			Expect(snap.Height).To(Equal(4))
			Expect(snap.BlockHash).To(Equal(headers[4].Hash))
			Expect(snap.Hash).To(Equal(storage.SnapshotHash(headers[4].Hash, set)))
			Expect(snap.UTXOs).To(Equal(set))

			below, err := repo.PrunedBelow()
			Expect(err).NotTo(HaveOccurred())
			Expect(below).To(Equal(5))

			current, err := repo.IndexCurrent()
			Expect(err).NotTo(HaveOccurred())
			Expect(current).To(BeTrue())

			for op, o := range set {
				got, ok := repo.Lookup(op)
				Expect(ok).To(BeTrue())
				Expect(got).To(Equal(o))
			}

			for _, a := range []string{fx.addrA, fx.addrB, fx.addrC} {
				owned, err := repo.UTXOsByAddress(a)
				Expect(err).NotTo(HaveOccurred())
				Expect(owned).To(Equal(expectedByAddr(set, a)))
			}

			state, err := repo.ForkState(5)
			Expect(err).NotTo(HaveOccurred())
			Expect(state).To(Equal(set))
		})

		It("copies its inputs", func() {
			headers := headersOf(3)
			set := copySet(fx.main.set)
			want := len(set)
			Expect(repo.Restore(headers, set)).To(Succeed())
			headers[2].Hash = "changed"

			for op := range set {
				delete(set, op)
			}

			last, err := repo.Last()
			Expect(err).NotTo(HaveOccurred())
			Expect(last.Hash).NotTo(Equal("changed"))

			snap, err := repo.Snapshot()
			Expect(err).NotTo(HaveOccurred())
			Expect(snap.UTXOs).To(HaveLen(want))
		})

		It("accepts the next Commit and keeps the indexes current", func() {
			headers := headersOf(5)
			set := copySet(fx.main.set)
			Expect(repo.Restore(headers, set)).To(Succeed())

			var op domain.OutPoint
			for op = range set {
				break
			}

			tx := indexedTx(99, []domain.OutPoint{op}, domain.TxOut{Amount: set[op].Amount, Address: fx.addrC})
			next := &domain.Block{Index: 5, PrevHash: headers[4].Hash, Hash: "next", Transactions: []domain.Transaction{tx}}
			d, err := domain.BlockDelta(set, next, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(repo.Commit(next, d)).To(Succeed())

			_, ok := repo.Lookup(op)
			Expect(ok).To(BeFalse())
			_, ok = repo.Lookup(spend(tx, 0))
			Expect(ok).To(BeTrue())

			h, err := repo.TxHeight(tx.CalculateHash())
			Expect(err).NotTo(HaveOccurred())
			Expect(h).To(Equal(5))
		})

		It("rejects a repository that already holds a block", func() {
			Expect(repo.Append(&domain.Block{Index: 0, Hash: "h0"})).To(Succeed())
			Expect(repo.Restore(headersOf(3), copySet(fx.main.set))).To(MatchError(storage.ErrConflict))
			all, err := repo.All()
			Expect(err).NotTo(HaveOccurred())
			Expect(all).To(HaveLen(1))

			_, err = repo.Snapshot()
			Expect(err).To(MatchError(storage.ErrNotFound))

			for _, op := range fx.outpoints {
				_, ok := repo.Lookup(op)
				Expect(ok).To(BeFalse())
			}
		})

		It("rejects a second Restore", func() {
			Expect(repo.Restore(headersOf(3), copySet(fx.main.set))).To(Succeed())
			Expect(repo.Restore(headersOf(4), copySet(fx.main.set))).To(MatchError(storage.ErrConflict))
			all, err := repo.All()
			Expect(err).NotTo(HaveOccurred())
			Expect(all).To(HaveLen(3))
		})

		DescribeTable("rejects bad headers and leaves the repository empty",
			func(bad func([]*domain.Block) []*domain.Block) {
				Expect(repo.Restore(bad(headersOf(4)), copySet(fx.main.set))).To(MatchError(storage.ErrConflict))
				expectEmpty(repo)
			},
			Entry("no headers", func([]*domain.Block) []*domain.Block { return nil }),
			Entry("a nil header", func(h []*domain.Block) []*domain.Block { h[2] = nil; return h }),
			Entry("a wrong index", func(h []*domain.Block) []*domain.Block { h[2].Index = 7; return h }),
			Entry("a broken PrevHash", func(h []*domain.Block) []*domain.Block { h[3].PrevHash = h[1].Hash; return h }),
			Entry("a wrong hash", func(h []*domain.Block) []*domain.Block { h[3].Hash = "forged"; return h }),
			Entry("a field changed after hashing", func(h []*domain.Block) []*domain.Block { h[3].Nonce++; return h }),
			Entry("a wrong genesis hash", func(h []*domain.Block) []*domain.Block { h[0].Hash = "forged"; return h }),
			Entry("a genesis changed after hashing", func(h []*domain.Block) []*domain.Block {
				h[0].Hash = h[0].CalculateHash()
				h[1].PrevHash = h[0].Hash
				h[1].Hash = h[1].CalculateHash()
				h[0].Nonce++

				return h
			}),
		)
	})

	Describe("Commit", func() {
		It("stores the blocks, the UTXO set and the indexes", func() {
			commitAll(repo, fx.main.blocks, fx.main.deltas)

			st := fx.observe(repo)
			Expect(st.current).To(BeTrue())
			Expect(st.hashes).To(Equal([]string{"m0", "m1", "m2", "m3"}))
			Expect(st.utxos).To(Equal(map[domain.OutPoint]domain.TxOut(fx.main.set)))

			for i, b := range fx.main.blocks {
				for _, tx := range b.Transactions {
					Expect(st.heights[tx.CalculateHash()]).To(Equal(i))
				}
			}

			ids := func(i, j int) string { return fx.main.blocks[i].Transactions[j].CalculateHash() }
			// A: receives g, spends it in t1 (change), receives t3, which t4 spends.
			Expect(st.byAddr[fx.addrA]).To(Equal([]string{ids(0, 0), ids(1, 0), ids(2, 1), ids(3, 0)}))
			// B: receives g, t1, and t4; spends g:1 in t4. t2 spends t1:0 (B's), so t2 counts too.
			Expect(st.byAddr[fx.addrB]).To(Equal([]string{ids(0, 0), ids(1, 0), ids(2, 0), ids(3, 0)}))
			// C: receives t2 (two outputs, one entry); t3 spends one of them.
			Expect(st.byAddr[fx.addrC]).To(Equal([]string{ids(2, 0), ids(2, 1)}))
		})

		It("serves the block lookups of the committed chain", func() {
			commitAll(repo, fx.main.blocks, fx.main.deltas)
			last, err := repo.Last()
			Expect(err).NotTo(HaveOccurred())
			Expect(last.Hash).To(Equal("m3"))

			byHash, err := repo.ByHash("m1")
			Expect(err).NotTo(HaveOccurred())
			Expect(byHash.Index).To(Equal(1))

			byHeight, err := repo.ByHeight(2)
			Expect(err).NotTo(HaveOccurred())
			Expect(byHeight.Hash).To(Equal("m2"))
		})

		DescribeTable("rejects and leaves blocks, UTXOs and indexes as they were",
			func(mk func(fx *indexedFixture) (*domain.Block, domain.UTXODelta), want error) {
				commitAll(repo, fx.main.blocks[:3], fx.main.deltas[:3])
				before := fx.observe(repo)

				b, d := mk(fx)

				err := repo.Commit(b, d)
				if want == nil {
					Expect(err).To(HaveOccurred())
					Expect(err).NotTo(MatchError(storage.ErrConflict))
					Expect(err).NotTo(MatchError(storage.ErrUTXOConflict))
				} else {
					Expect(err).To(MatchError(want))
				}

				Expect(fx.observe(repo)).To(Equal(before))

				if b != nil {
					_, err := repo.ByHash(b.Hash)
					Expect(err).To(MatchError(storage.ErrNotFound))
				}
			},
			Entry("wrong PrevHash", func(fx *indexedFixture) (*domain.Block, domain.UTXODelta) {
				b := *fx.main.blocks[3]
				b.PrevHash = "other"

				return &b, fx.main.deltas[3]
			}, storage.ErrConflict),
			Entry("skipped Index", func(fx *indexedFixture) (*domain.Block, domain.UTXODelta) {
				b := *fx.main.blocks[3]
				b.Index = 4

				return &b, fx.main.deltas[3]
			}, storage.ErrConflict),
			Entry("a delta that spends an absent output", func(fx *indexedFixture) (*domain.Block, domain.UTXODelta) {
				d := domain.UTXODelta{
					Spent:   map[domain.OutPoint]domain.TxOut{{TxID: "nope", Index: 0}: {Amount: 1, Address: fx.addrA}},
					Created: map[domain.OutPoint]domain.TxOut{},
				}

				return fx.main.blocks[3], d
			}, storage.ErrUTXOConflict),
			Entry("a delta that spends an output with a different value", func(fx *indexedFixture) (*domain.Block, domain.UTXODelta) {
				d := domain.UTXODelta{Spent: map[domain.OutPoint]domain.TxOut{}, Created: map[domain.OutPoint]domain.TxOut{}}
				for op, o := range fx.main.deltas[3].Spent {
					o.Amount++
					d.Spent[op] = o
				}

				return fx.main.blocks[3], d
			}, storage.ErrUTXOConflict),
			Entry("a delta that creates an existing output", func(fx *indexedFixture) (*domain.Block, domain.UTXODelta) {
				d := domain.UTXODelta{Spent: map[domain.OutPoint]domain.TxOut{}, Created: map[domain.OutPoint]domain.TxOut{}}
				maps.Copy(d.Created, fx.main.deltas[2].Created)

				return fx.main.blocks[3], d
			}, storage.ErrUTXOConflict),
			Entry("a nil block", func(fx *indexedFixture) (*domain.Block, domain.UTXODelta) {
				return nil, fx.main.deltas[3]
			}, nil),
		)

		It("refuses to commit on top of stale indexes", func() {
			commitAll(repo, fx.main.blocks[:2], fx.main.deltas[:2])
			Expect(repo.Append(fx.main.blocks[2])).To(Succeed())

			Expect(repo.Commit(fx.main.blocks[3], fx.main.deltas[3])).To(MatchError(storage.ErrIndexStale))
			last, err := repo.Last()
			Expect(err).NotTo(HaveOccurred())
			Expect(last.Hash).To(Equal("m2"))
		})
	})

	Describe("Append and ReplaceFrom", func() {
		It("leave the indexes stale after an Append", func() {
			commitAll(repo, fx.main.blocks[:2], fx.main.deltas[:2])
			Expect(repo.Append(fx.main.blocks[2])).To(Succeed())
			st := fx.observe(repo)
			Expect(st.current).To(BeFalse())
			Expect(st.heights[fx.main.blocks[2].Transactions[0].CalculateHash()]).To(Equal(-1))
		})

		It("leave the indexes stale after a swap that keeps the chain length", func() {
			commitAll(repo, fx.main.blocks, fx.main.deltas)
			Expect(repo.ReplaceFrom(2, fx.alt.blocks[2:4])).To(Succeed())
			current, err := repo.IndexCurrent()
			Expect(err).NotTo(HaveOccurred())
			Expect(current).To(BeFalse())
		})
	})

	Describe("Reorg", func() {
		It("matches a chain built only with Commit", func() {
			commitAll(repo, fx.main.blocks, fx.main.deltas)
			Expect(repo.Reorg(2, fx.alt.blocks[2:], fx.alt.deltas[2:])).To(Succeed())

			Expect(fx.observe(repo)).To(Equal(fx.observe(fresh(fx.alt))))
			st := fx.observe(repo)
			Expect(st.current).To(BeTrue())
			Expect(st.hashes).To(Equal([]string{"m0", "m1", "a2", "a3", "a4"}))
			// t4 only existed in the removed branch.
			t4 := fx.main.blocks[3].Transactions[0].CalculateHash()
			_, err := repo.TxHeight(t4)
			Expect(err).To(MatchError(storage.ErrNotFound))
			_, err = repo.ByHash("m3")
			Expect(err).To(MatchError(storage.ErrNotFound))
		})

		It("shortens the chain when the new branch is shorter", func() {
			commitAll(repo, fx.main.blocks, fx.main.deltas)
			Expect(repo.Reorg(2, fx.alt.blocks[2:3], fx.alt.deltas[2:3])).To(Succeed())
			Expect(fx.observe(repo)).To(Equal(fx.observe(fresh(fx.alt.forkAt(3)))))
		})

		It("extends the chain when height equals its length", func() {
			commitAll(repo, fx.main.blocks[:2], fx.main.deltas[:2])
			Expect(repo.Reorg(2, fx.main.blocks[2:], fx.main.deltas[2:])).To(Succeed())
			Expect(fx.observe(repo)).To(Equal(fx.observe(fresh(fx.main))))
		})

		It("accepts the same blocks again", func() {
			commitAll(repo, fx.main.blocks, fx.main.deltas)
			before := fx.observe(repo)
			Expect(repo.Reorg(1, fx.main.blocks[1:], fx.main.deltas[1:])).To(Succeed())
			Expect(fx.observe(repo)).To(Equal(before))
		})

		It("keeps accepting Commit afterwards", func() {
			commitAll(repo, fx.main.blocks, fx.main.deltas)
			Expect(repo.Reorg(2, fx.alt.blocks[2:3], fx.alt.deltas[2:3])).To(Succeed())
			Expect(repo.Commit(fx.alt.blocks[3], fx.alt.deltas[3])).To(Succeed())
			Expect(fx.observe(repo)).To(Equal(fx.observe(fresh(fx.alt.forkAt(4)))))
		})

		DescribeTable("rejects and leaves blocks, UTXOs and indexes as they were",
			func(height int, mk func(fx *indexedFixture) ([]*domain.Block, []domain.UTXODelta), want error) {
				commitAll(repo, fx.main.blocks, fx.main.deltas)
				before := fx.observe(repo)

				blocks, deltas := mk(fx)

				err := repo.Reorg(height, blocks, deltas)
				if want == nil {
					Expect(err).To(HaveOccurred())
					Expect(err).NotTo(MatchError(storage.ErrConflict))
					Expect(err).NotTo(MatchError(storage.ErrUTXOConflict))
				} else {
					Expect(err).To(MatchError(want))
				}

				Expect(fx.observe(repo)).To(Equal(before))

				for _, b := range blocks {
					if b != nil {
						_, err := repo.ByHash(b.Hash)
						Expect(err).To(MatchError(storage.ErrNotFound))
					}
				}
			},
			Entry("height 0", 0, func(fx *indexedFixture) ([]*domain.Block, []domain.UTXODelta) {
				return fx.alt.blocks[2:], fx.alt.deltas[2:]
			}, storage.ErrConflict),
			Entry("negative height", -1, func(fx *indexedFixture) ([]*domain.Block, []domain.UTXODelta) {
				return fx.alt.blocks[2:], fx.alt.deltas[2:]
			}, storage.ErrConflict),
			Entry("height past len", 5, func(fx *indexedFixture) ([]*domain.Block, []domain.UTXODelta) {
				return fx.alt.blocks[2:], fx.alt.deltas[2:]
			}, storage.ErrConflict),
			Entry("no blocks", 2, func(fx *indexedFixture) ([]*domain.Block, []domain.UTXODelta) {
				return nil, nil
			}, storage.ErrConflict),
			Entry("fewer deltas than blocks", 2, func(fx *indexedFixture) ([]*domain.Block, []domain.UTXODelta) {
				return fx.alt.blocks[2:], fx.alt.deltas[2:3]
			}, storage.ErrConflict),
			Entry("more deltas than blocks", 2, func(fx *indexedFixture) ([]*domain.Block, []domain.UTXODelta) {
				return fx.alt.blocks[2:3], fx.alt.deltas[2:]
			}, storage.ErrConflict),
			Entry("first PrevHash is not chain[height-1]", 2, func(fx *indexedFixture) ([]*domain.Block, []domain.UTXODelta) {
				b := *fx.alt.blocks[2]
				b.PrevHash = "other"

				return []*domain.Block{&b}, fx.alt.deltas[2:3]
			}, storage.ErrConflict),
			Entry("broken link in the middle", 2, func(fx *indexedFixture) ([]*domain.Block, []domain.UTXODelta) {
				b := *fx.alt.blocks[3]
				b.PrevHash = "other"

				return []*domain.Block{fx.alt.blocks[2], &b}, fx.alt.deltas[2:]
			}, storage.ErrConflict),
			Entry("a nil block", 2, func(fx *indexedFixture) ([]*domain.Block, []domain.UTXODelta) {
				return []*domain.Block{fx.alt.blocks[2], nil}, fx.alt.deltas[2:]
			}, nil),
			Entry("a nil block with mismatching deltas", 2, func(fx *indexedFixture) ([]*domain.Block, []domain.UTXODelta) {
				return []*domain.Block{nil}, nil
			}, nil),
			Entry("a delta that does not match, after valid reverts and commits", 2, func(fx *indexedFixture) ([]*domain.Block, []domain.UTXODelta) {
				bad := domain.UTXODelta{
					Spent:   map[domain.OutPoint]domain.TxOut{{TxID: "nope"}: {Amount: 1, Address: fx.addrA}},
					Created: map[domain.OutPoint]domain.TxOut{},
				}

				return fx.alt.blocks[2:], []domain.UTXODelta{fx.alt.deltas[2], fx.alt.deltas[3], bad}
			}, storage.ErrUTXOConflict),
			Entry("a delta that creates an output the set keeps", 2, func(fx *indexedFixture) ([]*domain.Block, []domain.UTXODelta) {
				keep := domain.UTXODelta{Spent: map[domain.OutPoint]domain.TxOut{}, Created: map[domain.OutPoint]domain.TxOut{}}
				maps.Copy(keep.Created, fx.main.deltas[1].Created)

				return fx.alt.blocks[2:3], []domain.UTXODelta{keep}
			}, storage.ErrUTXOConflict),
		)

		It("returns ErrConflict on an empty repository", func() {
			Expect(repo.Reorg(1, fx.alt.blocks[1:], fx.alt.deltas[1:])).To(MatchError(storage.ErrConflict))
			Expect(fx.observe(repo).hashes).To(BeEmpty())
		})

		It("refuses to reorg stale indexes", func() {
			commitAll(repo, fx.main.blocks[:2], fx.main.deltas[:2])
			Expect(repo.Append(fx.main.blocks[2])).To(Succeed())
			Expect(repo.Reorg(2, fx.alt.blocks[2:], fx.alt.deltas[2:])).To(MatchError(storage.ErrIndexStale))
			last, err := repo.Last()
			Expect(err).NotTo(HaveOccurred())
			Expect(last.Hash).To(Equal("m2"))
		})
	})

	Describe("Reindex", func() {
		It("is a no-op on an empty repository", func() {
			Expect(repo.Reindex()).To(Succeed())
			Expect(fx.observe(repo).current).To(BeTrue())
		})

		It("leaves a chain built with Commit and Reorg as it was", func() {
			commitAll(repo, fx.main.blocks, fx.main.deltas)
			Expect(repo.Reorg(2, fx.alt.blocks[2:], fx.alt.deltas[2:])).To(Succeed())
			before := fx.observe(repo)

			Expect(repo.Reindex()).To(Succeed())
			Expect(fx.observe(repo)).To(Equal(before))
		})

		It("rebuilds the indexes of a chain written with Append", func() {
			for _, b := range fx.main.blocks {
				Expect(repo.Append(b)).To(Succeed())
			}

			Expect(fx.observe(repo).current).To(BeFalse())

			Expect(repo.Reindex()).To(Succeed())
			Expect(fx.observe(repo)).To(Equal(fx.observe(fresh(fx.main))))
			// The rebuilt undo data lets a Reorg run.
			Expect(repo.Reorg(2, fx.alt.blocks[2:], fx.alt.deltas[2:])).To(Succeed())
			Expect(fx.observe(repo)).To(Equal(fx.observe(fresh(fx.alt))))
		})

		It("rebuilds the indexes after ReplaceFrom", func() {
			commitAll(repo, fx.main.blocks, fx.main.deltas)
			Expect(repo.ReplaceFrom(2, fx.alt.blocks[2:])).To(Succeed())
			Expect(fx.observe(repo).current).To(BeFalse())

			Expect(repo.Reindex()).To(Succeed())
			Expect(fx.observe(repo)).To(Equal(fx.observe(fresh(fx.alt))))
		})

		It("fails on a chain that does not replay and changes nothing", func() {
			commitAll(repo, fx.main.blocks[:2], fx.main.deltas[:2])
			// These transactions spend outputs that this chain never created.
			Expect(repo.Append(&domain.Block{Index: 2, PrevHash: "m1", Hash: "bad2", Transactions: fx.main.blocks[3].Transactions})).To(Succeed())
			before := fx.observe(repo)

			Expect(repo.Reindex()).To(HaveOccurred())
			Expect(fx.observe(repo)).To(Equal(before))
		})
	})
}

// IndexedChains returns two chains of real transactions that share their first
// two blocks, each with the UTXO delta of every block, for adapter tests that
// need to drive Commit and Reorg directly.
func IndexedChains() (main, alt []*domain.Block, mainDeltas, altDeltas []domain.UTXODelta) {
	fx := newIndexedFixture()
	return fx.main.blocks, fx.alt.blocks, fx.main.deltas, fx.alt.deltas
}

// step mines one block on w: a transaction spending the smallest unspent
// output that can be split, and, on even salts, a second one spending an
// output the first just created.
func (w *indexedWorld) step(addrs []string, salt int64, tag string) {
	ops := make([]domain.OutPoint, 0, len(w.set))
	for op, o := range w.set {
		if o.Amount >= 2 {
			ops = append(ops, op)
		}
	}

	sort.Slice(ops, func(i, j int) bool {
		if ops[i].TxID != ops[j].TxID {
			return ops[i].TxID < ops[j].TxID
		}

		return ops[i].Index < ops[j].Index
	})
	Expect(ops).NotTo(BeEmpty())
	in := w.set[ops[0]]
	n := int64(len(addrs))
	half := in.Amount / 2
	t1 := indexedTx(salt, []domain.OutPoint{ops[0]},
		domain.TxOut{Amount: half, Address: addrs[salt%n]}, domain.TxOut{Amount: in.Amount - half, Address: addrs[(salt+1)%n]})

	txs := []domain.Transaction{t1}
	if salt%2 == 0 && half >= 2 {
		txs = append(txs, indexedTx(salt+500, []domain.OutPoint{spend(t1, 0)},
			domain.TxOut{Amount: half / 2, Address: addrs[(salt+2)%n]}, domain.TxOut{Amount: half - half/2, Address: addrs[salt%n]}))
	}

	w.mine(tag, txs...)
	last := w.blocks[len(w.blocks)-1]
	last.MerkleRoot = "mr" + last.Hash
}

// prunedWorld returns a chain of n blocks over addrs: a genesis paying every address, then steps.
func prunedWorld(addrs []string, n int, tag string, salt int64) *indexedWorld {
	w := newIndexedWorld()

	outs := make([]domain.TxOut, 0, 2*len(addrs))
	for i := range 2 * len(addrs) {
		outs = append(outs, domain.TxOut{Amount: 1_000_000_000, Address: addrs[i%len(addrs)]})
	}

	w.mine(tag, indexedTx(salt, nil, outs...))
	w.blocks[0].MerkleRoot = "mr" + w.blocks[0].Hash
	w.extend(addrs, n-1, tag, salt+1)

	return w
}

func (w *indexedWorld) extend(addrs []string, n int, tag string, salt int64) {
	for i := range n {
		w.step(addrs, salt+int64(i), tag)
	}
}

// prunedView is everything observable about a repository that pruning can touch.
type prunedView struct {
	hashes      []string
	bodies      []int
	snapshot    storage.Snapshot
	snapshotErr error
	prunedBelow int
	byAddr      map[string][]storage.AddressUTXO
	tipState    domain.UTXOSet
	current     bool
}

func viewOf(repo storage.IndexedRepository, addrs []string) prunedView {
	v := prunedView{byAddr: map[string][]storage.AddressUTXO{}}
	all, err := repo.All()
	Expect(err).NotTo(HaveOccurred())

	for _, b := range all {
		v.hashes = append(v.hashes, b.Hash)
		v.bodies = append(v.bodies, len(b.Transactions))
	}

	v.snapshot, v.snapshotErr = repo.Snapshot()
	v.prunedBelow, err = repo.PrunedBelow()
	Expect(err).NotTo(HaveOccurred())

	for _, a := range addrs {
		v.byAddr[a], err = repo.UTXOsByAddress(a)
		Expect(err).NotTo(HaveOccurred())
	}

	v.tipState, err = repo.ForkState(len(all))
	Expect(err).NotTo(HaveOccurred())
	v.current, err = repo.IndexCurrent()
	Expect(err).NotTo(HaveOccurred())

	return v
}

func expectedByAddr(set domain.UTXOSet, addr string) []storage.AddressUTXO {
	out := []storage.AddressUTXO{}

	for op, o := range set {
		if o.Address == addr {
			out = append(out, storage.AddressUTXO{OutPoint: op, Amount: o.Amount})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].OutPoint.TxID != out[j].OutPoint.TxID {
			return out[i].OutPoint.TxID < out[j].OutPoint.TxID
		}

		return out[i].OutPoint.Index < out[j].OutPoint.Index
	})

	return out
}

// checkAgainst asserts that repo describes the chain of w: headers and bodies
// around prunedBelow, the snapshot, the address index and every fork state.
func checkAgainst(repo storage.IndexedRepository, w *indexedWorld, addrs []string, p storage.Pruning) {
	chain := w.blocks
	pb, err := repo.PrunedBelow()
	Expect(err).NotTo(HaveOccurred())

	snap, err := repo.Snapshot()
	if err == nil {
		Expect(snap.Height).To(BeNumerically("<", len(chain)))
		set, berr := domain.BuildUTXOSet(chain[:snap.Height+1])
		Expect(berr).NotTo(HaveOccurred())
		Expect(snap.BlockHash).To(Equal(chain[snap.Height].Hash))
		Expect(snap.UTXOs).To(Equal(set))
		Expect(snap.Hash).To(Equal(storage.SnapshotHash(chain[snap.Height].Hash, set)))

		if p.KeepBlocks > 0 {
			Expect(pb).To(Equal(snap.Height + 1))
		}
	} else {
		Expect(err).To(MatchError(storage.ErrNotFound))
		Expect(pb).To(BeZero())
	}

	if p.KeepBlocks == 0 {
		Expect(pb).To(BeZero())
	}

	same := func(got, want *domain.Block, h int) {
		Expect(got.Hash).To(Equal(want.Hash))
		Expect(got.MerkleRoot).To(Equal(want.MerkleRoot))
		Expect(got.Index).To(Equal(want.Index))
		Expect(got.PrevHash).To(Equal(want.PrevHash))
		Expect(got.Transactions).NotTo(BeNil())

		if h < pb {
			Expect(got.Transactions).To(BeEmpty())
			return
		}

		Expect(got.Transactions).To(HaveLen(len(want.Transactions)))

		for i := range want.Transactions {
			Expect(got.Transactions[i].CalculateHash()).To(Equal(want.Transactions[i].CalculateHash()))
		}
	}
	all, err := repo.All()
	Expect(err).NotTo(HaveOccurred())
	Expect(all).To(HaveLen(len(chain)))
	ranged, err := repo.Range(0, len(chain)+1)
	Expect(err).NotTo(HaveOccurred())
	Expect(ranged).To(HaveLen(len(chain)))

	for h, want := range chain {
		same(all[h], want, h)
		same(ranged[h], want, h)
		byHeight, err := repo.ByHeight(h)
		Expect(err).NotTo(HaveOccurred())
		same(byHeight, want, h)
		byHash, err := repo.ByHash(want.Hash)
		Expect(err).NotTo(HaveOccurred())
		same(byHash, want, h)

		for _, tx := range want.Transactions {
			got, err := repo.TxHeight(tx.CalculateHash())
			if h < pb {
				Expect(err).To(MatchError(storage.ErrNotFound))
			} else {
				Expect(err).NotTo(HaveOccurred())
				Expect(got).To(Equal(h))
			}
		}
	}

	full, err := domain.BuildUTXOSet(chain)
	Expect(err).NotTo(HaveOccurred())

	for _, a := range addrs {
		got, err := repo.UTXOsByAddress(a)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(expectedByAddr(full, a)))
	}

	for h := 0; h <= len(chain); h++ {
		got, err := repo.ForkState(h)
		if h < pb {
			Expect(err).To(MatchError(storage.ErrReorgTooDeep))
			continue
		}

		Expect(err).NotTo(HaveOccurred())
		want, err := domain.BuildUTXOSet(chain[:h])
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(want))
	}
}

// PrunedRepositoryContract registers Ginkgo specs for the snapshot, pruning,
// address-UTXO and fork-state behaviour of an IndexedRepository built with a
// storage.Pruning.
func PrunedRepositoryContract(newRepo func(storage.Pruning) storage.IndexedRepository) {
	var addrs []string

	BeforeEach(func() {
		addrs = nil

		for range 3 {
			w, err := wallet.New()
			Expect(err).NotTo(HaveOccurred())

			addrs = append(addrs, w.Address())
		}
	})

	commit := func(repo storage.IndexedRepository, w *indexedWorld, from, to int) {
		for i := from; i < to; i++ {
			Expect(repo.Commit(w.blocks[i], w.deltas[i])).To(Succeed())
		}
	}
	// reorg replaces the tail of w from height with n new blocks and returns the new world.
	reorg := func(repo storage.IndexedRepository, w *indexedWorld, height, n int, tag string, salt int64) *indexedWorld {
		alt := w.forkAt(height)
		alt.extend(addrs, n, tag, salt)
		Expect(repo.Reorg(height, alt.blocks[height:], alt.deltas[height:])).To(Succeed())

		return alt
	}

	for _, p := range []storage.Pruning{{SnapshotInterval: 1}, {SnapshotInterval: 3}, {SnapshotInterval: 3, KeepBlocks: 2}, {SnapshotInterval: 5, KeepBlocks: 4}, {SnapshotInterval: 1, KeepBlocks: 1}} {
		Describe(fmt.Sprintf("with N=%d K=%d", p.SnapshotInterval, p.KeepBlocks), func() {
			It("stores the snapshot the rule gives after every Commit", func() {
				repo := newRepo(p)
				w := prunedWorld(addrs, 14, "m", 10)
				lastS, has := 0, false

				for t := range w.blocks {
					Expect(repo.Commit(w.blocks[t], w.deltas[t])).To(Succeed())

					if s := t - p.KeepBlocks; s >= 1 && s%p.SnapshotInterval == 0 {
						lastS, has = s, true
					}

					snap, err := repo.Snapshot()
					if !has {
						Expect(err).To(MatchError(storage.ErrNotFound))
						continue
					}

					Expect(err).NotTo(HaveOccurred())
					set, err := domain.BuildUTXOSet(w.blocks[:lastS+1])
					Expect(err).NotTo(HaveOccurred())
					Expect(snap.Height).To(Equal(lastS))
					Expect(snap.BlockHash).To(Equal(w.blocks[lastS].Hash))
					Expect(snap.Hash).To(Equal(storage.SnapshotHash(w.blocks[lastS].Hash, set)))
					Expect(snap.UTXOs).To(Equal(set))

					pb, err := repo.PrunedBelow()
					Expect(err).NotTo(HaveOccurred())

					if p.KeepBlocks > 0 {
						Expect(pb).To(Equal(lastS + 1))
					} else {
						Expect(pb).To(BeZero())
					}
				}

				checkAgainst(repo, w, addrs, p)
			})

			It("hands out copies of the snapshot and fork state sets", func() {
				repo := newRepo(p)
				w := prunedWorld(addrs, 14, "m", 10)
				commit(repo, w, 0, 14)
				snap, err := repo.Snapshot()
				Expect(err).NotTo(HaveOccurred())

				for op := range snap.UTXOs {
					delete(snap.UTXOs, op)
					break
				}

				snap.UTXOs[domain.OutPoint{TxID: "forged", Index: 7}] = domain.TxOut{Amount: 1}
				again, err := repo.Snapshot()
				Expect(err).NotTo(HaveOccurred())
				set, err := domain.BuildUTXOSet(w.blocks[:again.Height+1])
				Expect(err).NotTo(HaveOccurred())
				Expect(again.UTXOs).To(Equal(set))
				Expect(again.Hash).To(Equal(storage.SnapshotHash(again.BlockHash, again.UTXOs)))

				pb, err := repo.PrunedBelow()
				Expect(err).NotTo(HaveOccurred())
				fs, err := repo.ForkState(max(pb, 10))
				Expect(err).NotTo(HaveOccurred())

				for op := range fs {
					delete(fs, op)
					break
				}

				fs[domain.OutPoint{TxID: "forged", Index: 8}] = domain.TxOut{Amount: 1}
				fs2, err := repo.ForkState(max(pb, 10))
				Expect(err).NotTo(HaveOccurred())
				Expect(fs2).NotTo(HaveKey(domain.OutPoint{TxID: "forged", Index: 8}))
				Expect(repo.Reindex()).To(Succeed())
				checked, err := repo.Snapshot()
				Expect(err).NotTo(HaveOccurred())
				Expect(checked.UTXOs).To(Equal(set))
			})

			It("keeps the address index and fork states right across Commit and Reorg", func() {
				repo := newRepo(p)
				w := prunedWorld(addrs, 12, "m", 10)
				commit(repo, w, 0, 12)
				checkAgainst(repo, w, addrs, p)

				pb, err := repo.PrunedBelow()
				Expect(err).NotTo(HaveOccurred())

				w = reorg(repo, w, max(pb, 6), 4, "a", 200)
				checkAgainst(repo, w, addrs, p)

				w.extend(addrs, 3, "a", 300)
				commit(repo, w, len(w.blocks)-3, len(w.blocks))
				checkAgainst(repo, w, addrs, p)

				pb, err = repo.PrunedBelow()
				Expect(err).NotTo(HaveOccurred())

				w = reorg(repo, w, max(pb, 8), 2, "b", 400)
				checkAgainst(repo, w, addrs, p)
			})

			It("reproduces the same state with Reindex", func() {
				repo := newRepo(p)
				w := prunedWorld(addrs, 14, "m", 10)
				commit(repo, w, 0, 14)
				pb, err := repo.PrunedBelow()
				Expect(err).NotTo(HaveOccurred())

				w = reorg(repo, w, max(pb, 9), 3, "a", 200)
				before := viewOf(repo, addrs)

				Expect(repo.Reindex()).To(Succeed())
				Expect(viewOf(repo, addrs)).To(Equal(before))
				checkAgainst(repo, w, addrs, p)
				// The rebuilt undo data still lets a Reorg run.
				pb, err = repo.PrunedBelow()
				Expect(err).NotTo(HaveOccurred())

				w = reorg(repo, w, max(pb, 9), 2, "c", 600)
				checkAgainst(repo, w, addrs, p)
			})
		})
	}

	Describe("pruning with N=3 K=2", func() {
		p := storage.Pruning{SnapshotInterval: 3, KeepBlocks: 2}

		It("turns the bodies below the snapshot into headers", func() {
			repo := newRepo(p)
			w := prunedWorld(addrs, 12, "m", 10)
			commit(repo, w, 0, 12)
			pb, err := repo.PrunedBelow()
			Expect(err).NotTo(HaveOccurred())
			Expect(pb).To(Equal(10)) // the Commit of height 11 stores the snapshot of 9

			snap, err := repo.Snapshot()
			Expect(err).NotTo(HaveOccurred())
			Expect(snap.Height).To(Equal(9))

			b0, err := repo.ByHeight(0)
			Expect(err).NotTo(HaveOccurred())
			Expect(b0.Transactions).NotTo(BeNil())
			Expect(b0.Transactions).To(BeEmpty())
			Expect(b0.MerkleRoot).To(Equal(w.blocks[0].MerkleRoot))

			b9, err := repo.ByHeight(9)
			Expect(err).NotTo(HaveOccurred())
			Expect(b9.Transactions).To(BeEmpty())

			b10, err := repo.ByHeight(10)
			Expect(err).NotTo(HaveOccurred())
			Expect(b10.Transactions).NotTo(BeEmpty())
			_, err = repo.TxHeight(w.blocks[3].Transactions[0].CalculateHash())
			Expect(err).To(MatchError(storage.ErrNotFound))
			ids, err := repo.TxsByAddress(addrs[0])
			Expect(err).NotTo(HaveOccurred())

			for _, id := range ids {
				h, err := repo.TxHeight(id)
				Expect(err).NotTo(HaveOccurred())
				Expect(h).To(BeNumerically(">=", 10))
			}
		})

		It("refuses a Reorg, a ReplaceFrom and a ForkState below prunedBelow and changes nothing", func() {
			repo := newRepo(p)
			w := prunedWorld(addrs, 12, "m", 10)
			commit(repo, w, 0, 12)
			before := viewOf(repo, addrs)

			for _, h := range []int{1, 9} {
				alt := w.forkAt(h)
				alt.extend(addrs, 2, "a", 200)
				Expect(repo.Reorg(h, alt.blocks[h:], alt.deltas[h:])).To(MatchError(storage.ErrReorgTooDeep))
				Expect(repo.ReplaceFrom(h, alt.blocks[h:])).To(MatchError(storage.ErrReorgTooDeep))
				_, err := repo.ForkState(h)
				Expect(err).To(MatchError(storage.ErrReorgTooDeep))
			}

			Expect(viewOf(repo, addrs)).To(Equal(before))
			// The first retained height is still a valid fork.
			w = reorg(repo, w, 10, 2, "a", 200)
			checkAgainst(repo, w, addrs, p)
		})

		It("accepts a ReplaceFrom at prunedBelow and Reindex repairs the indexes", func() {
			repo := newRepo(p)
			w := prunedWorld(addrs, 12, "m", 10)
			commit(repo, w, 0, 12)
			alt := w.forkAt(10)
			alt.extend(addrs, 2, "a", 200)
			Expect(repo.ReplaceFrom(10, alt.blocks[10:])).To(Succeed())
			Expect(repo.Reindex()).To(Succeed())
			checkAgainst(repo, alt, addrs, p)
		})

		It("leaves everything as it was when a Reorg fails after it pruned", func() {
			p := storage.Pruning{SnapshotInterval: 2, KeepBlocks: 1}
			repo := newRepo(p)
			w := prunedWorld(addrs, 11, "m", 10)
			commit(repo, w, 0, 11)
			pb, err := repo.PrunedBelow()
			Expect(err).NotTo(HaveOccurred())
			Expect(pb).To(Equal(9))

			before := viewOf(repo, addrs)

			// Blocks 9..11 store the snapshot of 10 and prune; block 12 is rejected.
			alt := w.forkAt(9)
			alt.extend(addrs, 3, "a", 200)
			bad := &domain.Block{Index: 12, PrevHash: alt.blocks[11].Hash, Hash: "bad12"}
			ghost := domain.OutPoint{TxID: "ghost", Index: 0}
			badDelta := domain.UTXODelta{Spent: map[domain.OutPoint]domain.TxOut{ghost: {Amount: 1, Address: addrs[0]}}}

			blocks := append(append([]*domain.Block{}, alt.blocks[9:]...), bad)
			deltas := append(append([]domain.UTXODelta{}, alt.deltas[9:]...), badDelta)
			Expect(repo.Reorg(9, blocks, deltas)).To(MatchError(storage.ErrUTXOConflict))
			Expect(viewOf(repo, addrs)).To(Equal(before))
			checkAgainst(repo, w, addrs, p)
		})
	})

	Describe("snapshots without pruning", func() {
		p := storage.Pruning{SnapshotInterval: 3}

		It("drops the snapshot when a Reorg forks at or below it", func() {
			repo := newRepo(p)
			w := prunedWorld(addrs, 8, "m", 10)
			commit(repo, w, 0, 8)
			snap, err := repo.Snapshot()
			Expect(err).NotTo(HaveOccurred())
			Expect(snap.Height).To(Equal(6))

			// Forking above the snapshot keeps it.
			w = reorg(repo, w, 7, 1, "a", 200)
			kept, err := repo.Snapshot()
			Expect(err).NotTo(HaveOccurred())
			Expect(kept).To(Equal(snap))

			// Forking at 5 leaves a chain of 6 blocks, short of the next snapshot.
			w = reorg(repo, w, 5, 1, "b", 300)
			_, err = repo.Snapshot()
			Expect(err).To(MatchError(storage.ErrNotFound))
			checkAgainst(repo, w, addrs, p)
		})

		It("drops the snapshot when a ReplaceFrom removes its block", func() {
			repo := newRepo(p)
			w := prunedWorld(addrs, 8, "m", 10)
			commit(repo, w, 0, 8)
			alt := w.forkAt(7)
			alt.extend(addrs, 1, "a", 200)
			Expect(repo.ReplaceFrom(7, alt.blocks[7:])).To(Succeed())
			snap, err := repo.Snapshot()
			Expect(err).NotTo(HaveOccurred())
			Expect(snap.Height).To(Equal(6))

			alt = w.forkAt(6)
			alt.extend(addrs, 2, "b", 300)
			Expect(repo.ReplaceFrom(6, alt.blocks[6:])).To(Succeed())
			_, err = repo.Snapshot()
			Expect(err).To(MatchError(storage.ErrNotFound))
		})

		It("keeps the snapshot when a Reorg that would drop it is rejected", func() {
			repo := newRepo(storage.Pruning{SnapshotInterval: 2})
			w := prunedWorld(addrs, 7, "m", 10)
			commit(repo, w, 0, 7)
			before := viewOf(repo, addrs)
			Expect(before.snapshot.Height).To(Equal(6))

			alt := w.forkAt(5)
			alt.extend(addrs, 1, "a", 200)
			bad := &domain.Block{Index: 6, PrevHash: alt.blocks[5].Hash, Hash: "bad6"}
			ghost := domain.UTXODelta{Spent: map[domain.OutPoint]domain.TxOut{{TxID: "ghost"}: {Amount: 1, Address: addrs[0]}}}
			Expect(repo.Reorg(5, []*domain.Block{alt.blocks[5], bad}, []domain.UTXODelta{alt.deltas[5], ghost})).To(MatchError(storage.ErrUTXOConflict))
			Expect(viewOf(repo, addrs)).To(Equal(before))
		})

		It("leaves the snapshot alone when a Commit is rejected at a snapshot height", func() {
			repo := newRepo(storage.Pruning{SnapshotInterval: 1})
			w := prunedWorld(addrs, 4, "m", 10)
			commit(repo, w, 0, 3)
			before := viewOf(repo, addrs)
			ghost := domain.UTXODelta{Spent: map[domain.OutPoint]domain.TxOut{{TxID: "ghost"}: {Amount: 1, Address: addrs[0]}}}
			Expect(repo.Commit(w.blocks[3], ghost)).To(MatchError(storage.ErrUTXOConflict))
			Expect(viewOf(repo, addrs)).To(Equal(before))
		})
	})

	Describe("UTXOsByAddress and ForkState", func() {
		It("returns an empty, non-nil slice for an address without outputs", func() {
			repo := newRepo(storage.Pruning{})
			got, err := repo.UTXOsByAddress("nobody")
			Expect(err).NotTo(HaveOccurred())
			Expect(got).NotTo(BeNil())
			Expect(got).To(BeEmpty())
		})

		It("refuses a height outside the chain", func() {
			repo := newRepo(storage.Pruning{})
			w := prunedWorld(addrs, 3, "m", 10)
			commit(repo, w, 0, 3)

			for _, h := range []int{-1, 4} {
				_, err := repo.ForkState(h)
				Expect(err).To(MatchError(storage.ErrConflict))
			}
		})

		It("returns a set the caller may edit", func() {
			repo := newRepo(storage.Pruning{})
			w := prunedWorld(addrs, 3, "m", 10)
			commit(repo, w, 0, 3)
			set, err := repo.ForkState(3)
			Expect(err).NotTo(HaveOccurred())

			for op := range set {
				delete(set, op)
			}

			checkAgainst(repo, w, addrs, storage.Pruning{})
		})

		It("refuses ForkState on stale indexes", func() {
			repo := newRepo(storage.Pruning{})
			w := prunedWorld(addrs, 3, "m", 10)
			commit(repo, w, 0, 2)
			Expect(repo.Append(w.blocks[2])).To(Succeed())
			_, err := repo.ForkState(1)
			Expect(err).To(MatchError(storage.ErrIndexStale))
		})
	})
}

// PrunedChain returns a chain of n blocks (n >= 1) of real transactions with the
// delta of each block, for adapter tests that need to drive Commit directly.
func PrunedChain(n int) ([]*domain.Block, []domain.UTXODelta) {
	addrs := make([]string, 3)
	for i := range addrs {
		w, err := wallet.New()
		Expect(err).NotTo(HaveOccurred())

		addrs[i] = w.Address()
	}

	w := prunedWorld(addrs, n, "m", 10)

	return w.blocks, w.deltas
}
