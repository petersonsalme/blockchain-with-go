package memory_test

import (
	"fmt"
	"math"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
	. "github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
)

var _ = Describe("Repository", func() {
	var repo *Repository

	BeforeEach(func() { repo = New() })

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
		a, b := &domain.Block{Index: 0}, &domain.Block{Index: 1}
		Expect(repo.Append(a)).To(Succeed())
		Expect(repo.Append(b)).To(Succeed())

		last, err := repo.Last()
		Expect(err).NotTo(HaveOccurred())
		Expect(last).To(BeIdenticalTo(b))

		all, err := repo.All()
		Expect(err).NotTo(HaveOccurred())
		Expect(all).To(HaveLen(2))
		Expect(all[0]).To(BeIdenticalTo(a))
		Expect(all[1]).To(BeIdenticalTo(b))
	})

	It("rejects a nil block and stores nothing", func() {
		Expect(repo.Append(nil)).To(MatchError(ErrNilBlock))
		all, _ := repo.All()
		Expect(all).To(BeEmpty())
	})

	It("returns a snapshot that later appends and caller edits do not affect", func() {
		Expect(repo.Append(&domain.Block{Index: 0})).To(Succeed())
		snap, _ := repo.All()

		Expect(repo.Append(&domain.Block{Index: 1})).To(Succeed())
		Expect(snap).To(HaveLen(1))

		snap[0] = nil
		again, _ := repo.All()
		Expect(again[0]).NotTo(BeNil())
	})

	DescribeTable("rejects a block that does not extend the last one with ErrConflict and keeps the chain",
		func(b *domain.Block) {
			genesis := &domain.Block{Index: 0, Hash: "h0"}
			Expect(repo.Append(genesis)).To(Succeed())

			Expect(repo.Append(b)).To(MatchError(storage.ErrConflict))

			all, _ := repo.All()
			Expect(all).To(HaveLen(1))
			Expect(all[0]).To(BeIdenticalTo(genesis))
		},
		Entry("wrong PrevHash", &domain.Block{Index: 1, PrevHash: "other"}),
		Entry("duplicate Index", &domain.Block{Index: 0, PrevHash: "h0"}),
		Entry("skipped Index", &domain.Block{Index: 2, PrevHash: "h0"}),
	)

	It("accepts a block that extends the last one", func() {
		Expect(repo.Append(&domain.Block{Index: 0, Hash: "h0"})).To(Succeed())
		Expect(repo.Append(&domain.Block{Index: 1, PrevHash: "h0", Hash: "h1"})).To(Succeed())
		all, _ := repo.All()
		Expect(all).To(HaveLen(2))
	})

	It("keeps the chain linked under concurrent Append and All", func() {
		const writers, perWriter = 8, 50
		var wg sync.WaitGroup
		for w := 0; w < writers; w++ {
			wg.Add(2)
			go func() {
				defer GinkgoRecover()
				defer wg.Done()
				// Bounded so a repository that rejects valid appends fails the test instead of hanging it.
				for i, attempts := 0, 0; i < perWriter; attempts++ {
					Expect(attempts).To(BeNumerically("<", 100000), "writer starved: valid appends keep being rejected")
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

		all, _ := repo.All()
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
				Expect(byHash).To(BeIdenticalTo(b))
				byHeight, err := repo.ByHeight(b.Index)
				Expect(err).NotTo(HaveOccurred())
				Expect(byHeight).To(BeIdenticalTo(b))
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
			empty := New()
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
					Expect(b).To(BeIdenticalTo(blocks[first+i]))
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
			got, _ := repo.Range(0, 2)
			got[0] = nil
			again, _ := repo.Range(0, 2)
			Expect(again[0]).To(BeIdenticalTo(blocks[0]))
		})

		It("leaves the lookups unchanged after a rejected Append", func() {
			rejected := &domain.Block{Index: 9, PrevHash: "nope", Hash: "rejected"}
			Expect(repo.Append(rejected)).To(MatchError(storage.ErrConflict))
			Expect(repo.Append(nil)).To(MatchError(ErrNilBlock))

			_, err := repo.ByHash("rejected")
			Expect(err).To(MatchError(storage.ErrNotFound))
			_, err = repo.ByHeight(9)
			Expect(err).To(MatchError(storage.ErrNotFound))
			got, _ := repo.Range(0, 100)
			Expect(got).To(Equal(blocks))
		})

		It("keeps the lookups consistent under concurrent Append", func() {
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer GinkgoRecover()
				defer wg.Done()
				for i := 5; i < 200; i++ {
					prev, _ := repo.Last()
					Expect(repo.Append(&domain.Block{Index: i, PrevHash: prev.Hash, Hash: fmt.Sprintf("h%d", i)})).To(Succeed())
				}
			}()
			go func() {
				defer GinkgoRecover()
				defer wg.Done()
				for i := 0; i < 200; i++ {
					page, err := repo.Range(0, 1000)
					Expect(err).NotTo(HaveOccurred())
					for j, b := range page {
						Expect(b.Index).To(Equal(j))
						byHash, err := repo.ByHash(b.Hash)
						Expect(err).NotTo(HaveOccurred())
						Expect(byHash).To(BeIdenticalTo(b))
					}
				}
			}()
			wg.Wait()
		})
	})
})
