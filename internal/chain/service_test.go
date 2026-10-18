package chain_test

import (
	"context"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	. "github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
)

// rejectingConsensus seals with PoW but never accepts a block.
type rejectingConsensus struct{ inner *pow.ProofOfWork }

func (r rejectingConsensus) Seal(ctx context.Context, b *domain.Block) error {
	return r.inner.Seal(ctx, b)
}

func (rejectingConsensus) Verify(prev, b *domain.Block) error { return errors.New("rejected") }

// failingRepo fails Append and Last on demand.
type failingRepo struct {
	storage.BlockRepository
	appendErr error
	lastErr   error
}

func (f *failingRepo) Append(b *domain.Block) error {
	if f.appendErr != nil {
		return f.appendErr
	}
	return f.BlockRepository.Append(b)
}

func (f *failingRepo) Last() (*domain.Block, error) {
	if f.lastErr != nil {
		return nil, f.lastErr
	}
	return f.BlockRepository.Last()
}

var sampleTx = []domain.Transaction{{From: "alice", To: "bob", Amount: 1}}

var _ = Describe("Service", func() {
	var (
		repo *memory.Repository
		eng  *pow.ProofOfWork
	)

	BeforeEach(func() {
		repo = memory.New()
		eng = pow.NewProofOfWork(1)
	})

	Describe("NewService", func() {
		It("creates the genesis block in an empty repository", func() {
			_, err := NewService(repo, eng)
			Expect(err).NotTo(HaveOccurred())

			all, _ := repo.All()
			Expect(all).To(HaveLen(1))
			g := all[0]
			Expect(g.Index).To(Equal(0))
			Expect(g.Timestamp).To(BeTemporally("~", time.Now(), time.Second))
			Expect(g.Transactions).To(BeEmpty())
			Expect(g.PrevHash).To(BeEmpty())
			Expect(g.Hash).To(BeEmpty())
		})

		It("keeps an existing chain untouched", func() {
			existing := &domain.Block{Index: 0}
			Expect(repo.Append(existing)).To(Succeed())

			_, err := NewService(repo, eng)
			Expect(err).NotTo(HaveOccurred())

			all, _ := repo.All()
			Expect(all).To(HaveLen(1))
			Expect(all[0]).To(BeIdenticalTo(existing))
		})

		It("fails when the repository cannot be read", func() {
			boom := errors.New("boom")
			_, err := NewService(&failingRepo{BlockRepository: repo, lastErr: boom}, eng)
			Expect(err).To(MatchError(boom))
		})

		It("fails when the genesis cannot be stored", func() {
			boom := errors.New("boom")
			_, err := NewService(&failingRepo{BlockRepository: repo, appendErr: boom}, eng)
			Expect(err).To(MatchError(boom))
		})
	})

	Describe("Blocks", func() {
		It("returns the repository chain", func() {
			svc, _ := NewService(repo, eng)
			blocks, err := svc.Blocks()
			Expect(err).NotTo(HaveOccurred())
			Expect(blocks).To(HaveLen(1))
		})
	})

	Describe("Mine", func() {
		It("seals, validates and appends a linked block", func() {
			svc, _ := NewService(repo, eng)
			blk, err := svc.Mine(context.Background(), sampleTx)
			Expect(err).NotTo(HaveOccurred())
			Expect(blk.Index).To(Equal(1))

			all, _ := repo.All()
			Expect(all).To(HaveLen(2))
			Expect(all[1]).To(BeIdenticalTo(blk))
			Expect(all[1].PrevHash).To(Equal(all[0].Hash))
			Expect(eng.Verify(all[0], blk)).To(Succeed())
		})

		It("returns ErrInvalidBlock when Verify rejects, leaving the chain unchanged", func() {
			svc, _ := NewService(repo, rejectingConsensus{inner: eng})
			blk, err := svc.Mine(context.Background(), sampleTx)
			Expect(err).To(MatchError(ErrInvalidBlock))
			Expect(blk).To(BeNil())

			all, _ := repo.All()
			Expect(all).To(HaveLen(1))
		})

		It("returns ErrInvalidBlock for a nil block", func() {
			svc, _ := NewService(repo, eng, WithGenerator(func(context.Context, consensus.Consensus, *domain.Block, []domain.Transaction) (*domain.Block, error) {
				return nil, nil
			}))
			_, err := svc.Mine(context.Background(), sampleTx)
			Expect(err).To(MatchError(ErrInvalidBlock))
			all, _ := repo.All()
			Expect(all).To(HaveLen(1))
		})

		It("propagates a ValidationError from ValidateBlockRules, leaving the chain unchanged", func() {
			svc, _ := NewService(repo, eng, WithGenerator(func(ctx context.Context, c consensus.Consensus, prev *domain.Block, txs []domain.Transaction) (*domain.Block, error) {
				b, err := GenerateBlockWithContext(ctx, c, prev, txs)
				b.Timestamp = prev.Timestamp.Add(-time.Second)
				return b, err
			}))
			_, err := svc.Mine(context.Background(), sampleTx)
			var ve *domain.ValidationError
			Expect(errors.As(err, &ve)).To(BeTrue())
			Expect(ve.Code).To(Equal("timestamp_before_parent"))
			all, _ := repo.All()
			Expect(all).To(HaveLen(1))
		})

		It("propagates a ValidationError from the size pre-check", func() {
			GinkgoT().Setenv(domain.MaxBlockBytesEnvVar, "100")
			svc, _ := NewService(repo, eng)
			_, err := svc.Mine(context.Background(), sampleTx)
			var ve *domain.ValidationError
			Expect(errors.As(err, &ve)).To(BeTrue())
			Expect(ve.Code).To(Equal("block_too_large"))
		})

		DescribeTable("propagates context errors",
			func(e error) {
				svc, _ := NewService(repo, eng, WithGenerator(func(context.Context, consensus.Consensus, *domain.Block, []domain.Transaction) (*domain.Block, error) {
					return nil, e
				}))
				_, err := svc.Mine(context.Background(), sampleTx)
				Expect(errors.Is(err, e)).To(BeTrue())
				all, _ := repo.All()
				Expect(all).To(HaveLen(1))
			},
			Entry("deadline", context.DeadlineExceeded),
			Entry("canceled", context.Canceled),
		)

		It("propagates a generic generator error unchanged", func() {
			boom := errors.New("boom")
			svc, _ := NewService(repo, eng, WithGenerator(func(context.Context, consensus.Consensus, *domain.Block, []domain.Transaction) (*domain.Block, error) {
				return nil, boom
			}))
			_, err := svc.Mine(context.Background(), sampleTx)
			Expect(err).To(MatchError(boom))
			Expect(errors.Is(err, ErrInvalidBlock)).To(BeFalse())
		})

		It("propagates repository Append errors", func() {
			boom := errors.New("boom")
			fr := &failingRepo{BlockRepository: repo}
			svc, _ := NewService(fr, eng)
			fr.appendErr = boom
			_, err := svc.Mine(context.Background(), sampleTx)
			Expect(err).To(MatchError(boom))
		})

		It("propagates repository Last errors", func() {
			boom := errors.New("boom")
			fr := &failingRepo{BlockRepository: repo}
			svc, _ := NewService(fr, eng)
			fr.lastErr = boom
			_, err := svc.Mine(context.Background(), sampleTx)
			Expect(err).To(MatchError(boom))
		})

		It("keeps services over separate repositories independent", func() {
			a, _ := NewService(memory.New(), eng)
			b, _ := NewService(memory.New(), eng)
			_, err := a.Mine(context.Background(), sampleTx)
			Expect(err).NotTo(HaveOccurred())

			ab, _ := a.Blocks()
			bb, _ := b.Blocks()
			Expect(ab).To(HaveLen(2))
			Expect(bb).To(HaveLen(1))
		})
	})
})
