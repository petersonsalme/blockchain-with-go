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
