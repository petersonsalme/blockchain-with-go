package storagetest

import (
	"fmt"
	"math"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
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
		for w := 0; w < writers; w++ {
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
				for i := 0; i < perWriter; i++ {
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
			for i := 0; i < 5; i++ {
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
				for i := 0; i < 100; i++ {
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
			for i := 0; i < n; i++ {
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
			for r := 0; r < 4; r++ {
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
				for i := 0; i < rounds; i++ {
					tag := fmt.Sprintf("v%d-", i)
					Expect(repo.ReplaceFrom(2, link(tag, 2, 1+i%4, "h1"))).To(Succeed())
				}
			}()
			go func() {
				defer GinkgoRecover()
				defer writers.Done()
				for i := 0; i < rounds; i++ {
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
