package chain_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
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

func sampleTx() []domain.Transaction {
	return []domain.Transaction{{From: "alice", To: "bob", Amount: 1}}
}

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
			blk, err := svc.Mine(context.Background(), sampleTx())
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
			blk, err := svc.Mine(context.Background(), sampleTx())
			Expect(err).To(MatchError(ErrInvalidBlock))
			Expect(blk).To(BeNil())

			all, _ := repo.All()
			Expect(all).To(HaveLen(1))
		})

		It("returns ErrInvalidBlock for a nil block", func() {
			svc, _ := NewService(repo, eng, WithGenerator(func(context.Context, consensus.Consensus, *domain.Block, []domain.Transaction) (*domain.Block, error) {
				return nil, nil
			}))
			_, err := svc.Mine(context.Background(), sampleTx())
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
			_, err := svc.Mine(context.Background(), sampleTx())
			var ve *domain.ValidationError
			Expect(errors.As(err, &ve)).To(BeTrue())
			Expect(ve.Code).To(Equal("timestamp_before_parent"))
			all, _ := repo.All()
			Expect(all).To(HaveLen(1))
		})

		It("propagates a ValidationError from the size pre-check", func() {
			GinkgoT().Setenv(domain.MaxBlockBytesEnvVar, "100")
			svc, _ := NewService(repo, eng)
			_, err := svc.Mine(context.Background(), sampleTx())
			var ve *domain.ValidationError
			Expect(errors.As(err, &ve)).To(BeTrue())
			Expect(ve.Code).To(Equal("block_too_large"))
		})

		DescribeTable("propagates context errors",
			func(e error) {
				svc, _ := NewService(repo, eng, WithGenerator(func(context.Context, consensus.Consensus, *domain.Block, []domain.Transaction) (*domain.Block, error) {
					return nil, e
				}))
				_, err := svc.Mine(context.Background(), sampleTx())
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
			_, err := svc.Mine(context.Background(), sampleTx())
			Expect(err).To(MatchError(boom))
			Expect(errors.Is(err, ErrInvalidBlock)).To(BeFalse())
		})

		DescribeTable("releases the write lock when Mine fails",
			func(failFirst func(context.Context, consensus.Consensus, *domain.Block, []domain.Transaction) (*domain.Block, error), recoverPanic bool) {
				var calls atomic.Int32
				svc, _ := NewService(repo, eng, WithGenerator(func(ctx context.Context, c consensus.Consensus, prev *domain.Block, txs []domain.Transaction) (*domain.Block, error) {
					if calls.Add(1) == 1 {
						return failFirst(ctx, c, prev, txs)
					}
					return GenerateBlockWithContext(ctx, c, prev, txs)
				}))

				func() {
					if recoverPanic {
						defer func() { Expect(recover()).NotTo(BeNil()) }()
					}
					_, err := svc.Mine(context.Background(), sampleTx())
					Expect(recoverPanic).To(BeFalse())
					Expect(err).To(HaveOccurred())
				}()

				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				blk, err := svc.Mine(ctx, sampleTx())
				Expect(err).NotTo(HaveOccurred())
				Expect(blk.Index).To(Equal(1))

				all, _ := repo.All()
				Expect(all).To(HaveLen(2))
			},
			Entry("generator error", func(context.Context, consensus.Consensus, *domain.Block, []domain.Transaction) (*domain.Block, error) {
				return nil, errors.New("boom")
			}, false),
			Entry("generator panic", func(context.Context, consensus.Consensus, *domain.Block, []domain.Transaction) (*domain.Block, error) {
				panic("boom")
			}, true),
		)

		It("returns the context error without generating or appending when ctx is already done and the lock is free", func() {
			var called atomic.Bool
			svc, _ := NewService(repo, eng, WithGenerator(func(ctx context.Context, c consensus.Consensus, prev *domain.Block, txs []domain.Transaction) (*domain.Block, error) {
				called.Store(true)
				return GenerateBlockWithContext(ctx, c, prev, txs)
			}))
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			blk, err := svc.Mine(ctx, sampleTx())
			Expect(blk).To(BeNil())
			Expect(errors.Is(err, context.Canceled)).To(BeTrue())
			Expect(called.Load()).To(BeFalse())
			all, _ := repo.All()
			Expect(all).To(HaveLen(1))
		})

		It("propagates repository Append errors", func() {
			boom := errors.New("boom")
			fr := &failingRepo{BlockRepository: repo}
			svc, _ := NewService(fr, eng)
			fr.appendErr = boom
			_, err := svc.Mine(context.Background(), sampleTx())
			Expect(err).To(MatchError(boom))
		})

		It("propagates repository Last errors", func() {
			boom := errors.New("boom")
			fr := &failingRepo{BlockRepository: repo}
			svc, _ := NewService(fr, eng)
			fr.lastErr = boom
			_, err := svc.Mine(context.Background(), sampleTx())
			Expect(err).To(MatchError(boom))
		})

		It("keeps services over separate repositories independent", func() {
			a, _ := NewService(memory.New(), eng)
			b, _ := NewService(memory.New(), eng)
			_, err := a.Mine(context.Background(), sampleTx())
			Expect(err).NotTo(HaveOccurred())

			ab, _ := a.Blocks()
			bb, _ := b.Blocks()
			Expect(ab).To(HaveLen(2))
			Expect(bb).To(HaveLen(1))
		})

		It("lets exactly one of two services mining on the same tip append, the other gets ErrConflict", func() {
			var read sync.WaitGroup
			read.Add(2)
			barrier := func(ctx context.Context, c consensus.Consensus, prev *domain.Block, txs []domain.Transaction) (*domain.Block, error) {
				read.Done()
				read.Wait()
				return GenerateBlockWithContext(ctx, c, prev, txs)
			}
			a, err := NewService(repo, eng, WithGenerator(barrier))
			Expect(err).NotTo(HaveOccurred())
			b, err := NewService(repo, eng, WithGenerator(barrier))
			Expect(err).NotTo(HaveOccurred())

			errs := make(chan error, 2)
			for _, svc := range []*Service{a, b} {
				go func() {
					defer GinkgoRecover()
					_, err := svc.Mine(context.Background(), sampleTx())
					errs <- err
				}()
			}
			results := []error{<-errs, <-errs}

			failed := 0
			for _, e := range results {
				if e != nil {
					failed++
					Expect(errors.Is(e, storage.ErrConflict)).To(BeTrue())
				}
			}
			Expect(failed).To(Equal(1))

			all, _ := repo.All()
			Expect(all).To(HaveLen(2))
			Expect(eng.Verify(all[0], all[1])).To(Succeed())
		})

		It("serializes concurrent Mine calls into a valid chain", func() {
			svc, _ := NewService(repo, eng)
			const n = 10
			var wg sync.WaitGroup
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func() {
					defer GinkgoRecover()
					defer wg.Done()
					_, err := svc.Mine(context.Background(), sampleTx())
					Expect(err).NotTo(HaveOccurred())
				}()
			}
			wg.Wait()

			all, _ := repo.All()
			Expect(all).To(HaveLen(n + 1))
			for i := 1; i < len(all); i++ {
				Expect(all[i].Index).To(Equal(i))
				Expect(eng.Verify(all[i-1], all[i])).To(Succeed())
			}
		})

		Context("while another Mine holds the write lock", func() {
			var (
				svc      *Service
				entered  chan struct{}
				release  chan struct{}
				holderCh chan error
				calls    atomic.Int32
			)

			BeforeEach(func() {
				entered = make(chan struct{})
				release = make(chan struct{})
				holderCh = make(chan error, 1)
				calls.Store(0)
				svc, _ = NewService(repo, eng, WithGenerator(func(ctx context.Context, c consensus.Consensus, prev *domain.Block, txs []domain.Transaction) (*domain.Block, error) {
					if calls.Add(1) == 1 {
						close(entered)
						<-release
					}
					return GenerateBlockWithContext(ctx, c, prev, txs)
				}))
				go func() {
					defer GinkgoRecover()
					_, err := svc.Mine(context.Background(), sampleTx())
					holderCh <- err
				}()
				Eventually(entered).Should(BeClosed())
			})

			AfterEach(func() {
				select {
				case <-release:
				default:
					close(release)
				}
				Eventually(holderCh).Should(Receive())
			})

			It("returns the context error to a queued Mine without appending", func() {
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				defer cancel()

				blk, err := svc.Mine(ctx, sampleTx())
				Expect(blk).To(BeNil())
				Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())
				Expect(calls.Load()).To(Equal(int32(1)))

				all, _ := repo.All()
				Expect(all).To(HaveLen(1))
			})

			It("does not block Blocks", func() {
				done := make(chan []*domain.Block, 1)
				go func() {
					defer GinkgoRecover()
					bs, err := svc.Blocks()
					Expect(err).NotTo(HaveOccurred())
					done <- bs
				}()
				Eventually(done, time.Second).Should(Receive(HaveLen(1)))
			})
		})
	})
})
