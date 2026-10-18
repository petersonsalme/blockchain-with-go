package memory_test

import (
	"fmt"
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
				for i := 0; i < perWriter; {
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
})
