package pow_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	powpkg "github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

var _ = Describe("ProofOfWork.Verify with PoA seal fields", func() {
	var (
		work    *powpkg.ProofOfWork
		prev, b *domain.Block
	)

	BeforeEach(func() {
		work = powpkg.New(1)
		prev = &domain.Block{Index: 0, Timestamp: time.Date(2021, 8, 15, 14, 30, 45, 0, time.UTC)}
		Expect(work.Seal(context.Background(), prev)).To(Succeed())
		b = &domain.Block{Index: 1, Timestamp: time.Date(2021, 8, 15, 14, 31, 45, 0, time.UTC), PrevHash: prev.Hash}
	})

	It("rejects a block with a Validator", func() {
		b.Validator = "ab"
		Expect(work.Seal(context.Background(), b)).To(Succeed())
		Expect(work.Verify(prev, b)).To(MatchError(consensus.ErrInvalidSeal))
	})

	It("rejects a block with a Signature", func() {
		Expect(work.Seal(context.Background(), b)).To(Succeed())
		b.Signature = "cd"
		Expect(work.Verify(prev, b)).To(MatchError(consensus.ErrInvalidSeal))
	})

	It("still accepts a block without seal fields", func() {
		Expect(work.Seal(context.Background(), b)).To(Succeed())
		Expect(work.Verify(prev, b)).To(Succeed())
	})
})
