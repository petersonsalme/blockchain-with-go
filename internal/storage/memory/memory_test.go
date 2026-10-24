package memory_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/storagetest"
)

var _ = Describe("Repository", func() {
	storagetest.BlockRepositoryContract(func() storage.BlockRepository {
		return memory.New()
	})

	storagetest.IndexedRepositoryContract(func() storage.IndexedRepository {
		return memory.New()
	})

	It("rebuilds the exact index structures with Reindex", func() {
		mainChain, altChain, mainDeltas, altDeltas := storagetest.IndexedChains()
		repo := memory.New()
		for i, b := range mainChain {
			Expect(repo.Commit(b, mainDeltas[i])).To(Succeed())
		}
		Expect(repo.Reorg(2, altChain[2:], altDeltas[2:])).To(Succeed())
		before := repo.IndexState()

		Expect(repo.Reindex()).To(Succeed())
		Expect(repo.IndexState()).To(Equal(before))
	})

	It("does not let the caller edit the stored undo data", func() {
		mainChain, _, mainDeltas, _ := storagetest.IndexedChains()
		repo := memory.New()
		d := mainDeltas[0]
		Expect(repo.Commit(mainChain[0], d)).To(Succeed())
		before := repo.IndexState()
		for op := range d.Created {
			delete(d.Created, op)
		}
		Expect(repo.IndexState()).To(Equal(before))
	})

	It("returns ErrNilBlock from Commit and Reorg when a block is nil", func() {
		repo := memory.New()
		Expect(repo.Commit(nil, domain.UTXODelta{})).To(MatchError(memory.ErrNilBlock))
		Expect(repo.Reorg(1, []*domain.Block{nil}, nil)).To(MatchError(memory.ErrNilBlock))
	})

	It("returns ErrNilBlock when given a nil block", func() {
		repo := memory.New()
		Expect(repo.Append(nil)).To(MatchError(memory.ErrNilBlock))
	})

	It("returns ErrNilBlock from ReplaceFrom when a block is nil", func() {
		repo := memory.New()
		Expect(repo.Append(&domain.Block{Index: 0, Hash: "h0"})).To(Succeed())
		blocks := []*domain.Block{{Index: 1, PrevHash: "h0", Hash: "h1"}, nil}
		Expect(repo.ReplaceFrom(1, blocks)).To(MatchError(memory.ErrNilBlock))
	})
})
