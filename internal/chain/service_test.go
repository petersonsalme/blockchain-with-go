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

// errOnlyCtx reports Canceled through Err but never closes Done.
type errOnlyCtx struct{ context.Context }

func (errOnlyCtx) Err() error { return context.Canceled }

// failingRepo fails Append and Last on demand.
type failingRepo struct {
	storage.BlockRepository
	appendErr error
	lastErr   error
	allErr    error
	readErr   error
}

func (f *failingRepo) ByHeight(h int) (*domain.Block, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	return f.BlockRepository.ByHeight(h)
}

func (f *failingRepo) ByHash(hash string) (*domain.Block, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	return f.BlockRepository.ByHash(hash)
}

func (f *failingRepo) Range(from, limit int) ([]*domain.Block, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	return f.BlockRepository.Range(from, limit)
}

func (f *failingRepo) All() ([]*domain.Block, error) {
	if f.allErr != nil {
		return nil, f.allErr
	}
	return f.BlockRepository.All()
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

		It("fails when the chain cannot be scanned for nonces", func() {
			boom := errors.New("boom")
			_, err := NewService(&failingRepo{BlockRepository: repo, allErr: boom}, eng)
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
			svc, _ := funded(repo, eng)
			blocks, err := svc.Blocks()
			Expect(err).NotTo(HaveOccurred())
			Expect(blocks).To(HaveLen(1))
		})
	})

	Describe("block queries", func() {
		var (
			svc *Service
			f   *funding
		)

		BeforeEach(func() {
			svc, f = funded(repo, eng)
			_, err := svc.Mine(context.Background(), f.tx())
			Expect(err).NotTo(HaveOccurred())
		})

		It("returns the tip", func() {
			tip, err := svc.Tip()
			Expect(err).NotTo(HaveOccurred())
			Expect(tip.Index).To(Equal(1))
		})

		It("looks blocks up by height and hash and ranges over them", func() {
			all, _ := repo.All()
			for _, b := range all {
				byHeight, err := svc.BlockByHeight(b.Index)
				Expect(err).NotTo(HaveOccurred())
				Expect(byHeight).To(BeIdenticalTo(b))
				byHash, err := svc.BlockByHash(b.Hash)
				Expect(err).NotTo(HaveOccurred())
				Expect(byHash).To(BeIdenticalTo(b))
			}
			page, err := svc.BlocksRange(1, 5)
			Expect(err).NotTo(HaveOccurred())
			Expect(page).To(Equal(all[1:]))
		})

		It("reports ErrNotFound for unknown blocks", func() {
			_, err := svc.BlockByHeight(2)
			Expect(err).To(MatchError(storage.ErrNotFound))
			_, err = svc.BlockByHash("unknown")
			Expect(err).To(MatchError(storage.ErrNotFound))
		})

		It("propagates repository errors", func() {
			boom := errors.New("boom")
			failing := &failingRepo{BlockRepository: repo}
			bad, err := NewService(failing, eng)
			Expect(err).NotTo(HaveOccurred())
			failing.readErr, failing.lastErr = boom, boom
			_, err = bad.BlockByHeight(0)
			Expect(err).To(MatchError(boom))
			_, err = bad.BlockByHash("x")
			Expect(err).To(MatchError(boom))
			_, err = bad.BlocksRange(0, 1)
			Expect(err).To(MatchError(boom))
			_, err = bad.Tip()
			Expect(err).To(MatchError(boom))
		})

		It("finds transactions across blocks, including genesis allocation at height 0", func() {
			all, _ := repo.All()
			genesisTx := all[0].Transactions[0]
			tx, blockHash, height, err := svc.Transaction(genesisTx.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(tx.ID).To(Equal(genesisTx.ID))
			Expect(blockHash).To(Equal(all[0].Hash))
			Expect(height).To(Equal(0))

			minedTx := all[1].Transactions[0]
			tx, blockHash, height, err = svc.Transaction(minedTx.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(tx.ID).To(Equal(minedTx.ID))
			Expect(blockHash).To(Equal(all[1].Hash))
			Expect(height).To(Equal(1))
		})

		It("reports ErrNotFound for unknown transactions", func() {
			_, _, _, err := svc.Transaction("unknown")
			Expect(err).To(MatchError(storage.ErrNotFound))
		})

		It("propagates repository errors from Transaction", func() {
			boom := errors.New("boom")
			failing := &failingRepo{BlockRepository: repo}
			bad, err := NewService(failing, eng)
			Expect(err).NotTo(HaveOccurred())
			failing.allErr = boom
			_, _, _, err = bad.Transaction("any")
			Expect(err).To(MatchError(boom))
		})

		It("returns balance and utxos sorted by (TxID, Index)", func() {
			all, _ := repo.All()
			ownerAddr := all[0].Transactions[0].Outputs[0].Address
			balance, utxos := svc.Balance(ownerAddr)
			Expect(balance).To(Equal(uint64(110))) // 12 * 10 - 10 spent
			Expect(utxos).To(HaveLen(11))
			for i := 1; i < len(utxos); i++ {
				prev, curr := utxos[i-1], utxos[i]
				if prev.TxID == curr.TxID {
					Expect(prev.Index).To(BeNumerically("<", curr.Index))
				} else {
					Expect(prev.TxID < curr.TxID).To(BeTrue())
				}
			}

			// Mine another transaction paying recipient two outputs, so recipient has
			// UTXOs with both different TxIDs and identical TxID with different indices.
			recip := recipient()
			recipAddr := recip.Address()
			// Initial balance of recipient from BeforeEach (spent 1 output paying 9 to recipient)
			rBal, rUTXOs := svc.Balance(recipAddr)
			Expect(rBal).To(Equal(uint64(9)))
			Expect(rUTXOs).To(HaveLen(1))

			// Spend recipient's output into two outputs to self
			tx2 := domain.Transaction{
				Inputs: []domain.TxIn{{PrevTxID: rUTXOs[0].TxID, OutIndex: rUTXOs[0].Index}},
				Outputs: []domain.TxOut{
					{Amount: 3, Address: recipAddr},
					{Amount: 4, Address: recipAddr},
				},
				Timestamp: time.Now(),
			}
			tx2.ID = tx2.CalculateHash()
			Expect(domain.SignInput(&tx2, 0, recip)).To(Succeed())
			_, err := svc.Mine(context.Background(), []domain.Transaction{tx2})
			Expect(err).NotTo(HaveOccurred())

			// Now send another payment from owner to recipient so recipient has 2 different TxIDs
			_, err = svc.Mine(context.Background(), f.tx())
			Expect(err).NotTo(HaveOccurred())

			rBalFinal, rUTXOsFinal := svc.Balance(recipAddr)
			Expect(rBalFinal).To(Equal(uint64(3 + 4 + 9)))
			Expect(rUTXOsFinal).To(HaveLen(3))
			for i := 1; i < len(rUTXOsFinal); i++ {
				prev, curr := rUTXOsFinal[i-1], rUTXOsFinal[i]
				if prev.TxID == curr.TxID {
					Expect(prev.Index).To(BeNumerically("<", curr.Index))
				} else {
					Expect(prev.TxID < curr.TxID).To(BeTrue())
				}
			}

			// Empty/unknown address returns 0 and non-nil empty slice
			emptyBal, emptyUTXOs := svc.Balance("unknown")
			Expect(emptyBal).To(Equal(uint64(0)))
			Expect(emptyUTXOs).NotTo(BeNil())
			Expect(emptyUTXOs).To(BeEmpty())
		})
	})

	Describe("Mine", func() {
		It("seals, validates and appends a linked block", func() {
			svc, f := funded(repo, eng)
			blk, err := svc.Mine(context.Background(), f.tx())
			Expect(err).NotTo(HaveOccurred())
			Expect(blk.Index).To(Equal(1))

			all, _ := repo.All()
			Expect(all).To(HaveLen(2))
			Expect(all[1]).To(BeIdenticalTo(blk))
			Expect(all[1].PrevHash).To(Equal(all[0].Hash))
			Expect(eng.Verify(all[0], blk)).To(Succeed())
		})

		It("returns ErrInvalidBlock when Verify rejects, leaving the chain unchanged", func() {
			svc, f := funded(repo, rejectingConsensus{inner: eng})
			blk, err := svc.Mine(context.Background(), f.tx())
			Expect(err).To(MatchError(ErrInvalidBlock))
			Expect(blk).To(BeNil())

			all, _ := repo.All()
			Expect(all).To(HaveLen(1))
		})

		It("returns ErrInvalidBlock for a nil block", func() {
			svc, f := funded(repo, eng, WithGenerator(func(context.Context, consensus.Consensus, *domain.Block, []domain.Transaction) (*domain.Block, error) {
				return nil, nil
			}))
			_, err := svc.Mine(context.Background(), f.tx())
			Expect(err).To(MatchError(ErrInvalidBlock))
			all, _ := repo.All()
			Expect(all).To(HaveLen(1))
		})

		It("propagates a ValidationError from ValidateBlockRules, leaving the chain unchanged", func() {
			svc, f := funded(repo, eng, WithGenerator(func(ctx context.Context, c consensus.Consensus, prev *domain.Block, txs []domain.Transaction) (*domain.Block, error) {
				b, err := GenerateBlockWithContext(ctx, c, prev, txs)
				b.Timestamp = prev.Timestamp.Add(-time.Second)
				return b, err
			}))
			_, err := svc.Mine(context.Background(), f.tx())
			var ve *domain.ValidationError
			Expect(errors.As(err, &ve)).To(BeTrue())
			Expect(ve.Code).To(Equal("timestamp_before_parent"))
			all, _ := repo.All()
			Expect(all).To(HaveLen(1))
		})

		It("propagates a ValidationError from the size pre-check", func() {
			GinkgoT().Setenv(domain.MaxBlockBytesEnvVar, "100")
			svc, f := funded(repo, eng)
			_, err := svc.Mine(context.Background(), f.tx())
			var ve *domain.ValidationError
			Expect(errors.As(err, &ve)).To(BeTrue())
			Expect(ve.Code).To(Equal("block_too_large"))
		})

		DescribeTable("propagates context errors",
			func(e error) {
				svc, f := funded(repo, eng, WithGenerator(func(context.Context, consensus.Consensus, *domain.Block, []domain.Transaction) (*domain.Block, error) {
					return nil, e
				}))
				_, err := svc.Mine(context.Background(), f.tx())
				Expect(errors.Is(err, e)).To(BeTrue())
				all, _ := repo.All()
				Expect(all).To(HaveLen(1))
			},
			Entry("deadline", context.DeadlineExceeded),
			Entry("canceled", context.Canceled),
		)

		It("propagates a generic generator error unchanged", func() {
			boom := errors.New("boom")
			svc, f := funded(repo, eng, WithGenerator(func(context.Context, consensus.Consensus, *domain.Block, []domain.Transaction) (*domain.Block, error) {
				return nil, boom
			}))
			_, err := svc.Mine(context.Background(), f.tx())
			Expect(err).To(MatchError(boom))
			Expect(errors.Is(err, ErrInvalidBlock)).To(BeFalse())
		})

		DescribeTable("releases the write lock when Mine fails",
			func(failFirst func(context.Context, consensus.Consensus, *domain.Block, []domain.Transaction) (*domain.Block, error), recoverPanic bool) {
				var calls atomic.Int32
				svc, f := funded(repo, eng, WithGenerator(func(ctx context.Context, c consensus.Consensus, prev *domain.Block, txs []domain.Transaction) (*domain.Block, error) {
					if calls.Add(1) == 1 {
						return failFirst(ctx, c, prev, txs)
					}
					return GenerateBlockWithContext(ctx, c, prev, txs)
				}))

				func() {
					if recoverPanic {
						defer func() { Expect(recover()).NotTo(BeNil()) }()
					}
					_, err := svc.Mine(context.Background(), f.tx())
					Expect(recoverPanic).To(BeFalse())
					Expect(err).To(HaveOccurred())
				}()

				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				blk, err := svc.Mine(ctx, f.tx())
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
			svc, f := funded(repo, eng, WithGenerator(func(ctx context.Context, c consensus.Consensus, prev *domain.Block, txs []domain.Transaction) (*domain.Block, error) {
				called.Store(true)
				return GenerateBlockWithContext(ctx, c, prev, txs)
			}))
			// Done() never fires, so Mine's select always takes the free
			// semaphore and only the post-lock ctx.Err() check can stop it.
			blk, err := svc.Mine(errOnlyCtx{context.Background()}, f.tx())
			Expect(blk).To(BeNil())
			Expect(errors.Is(err, context.Canceled)).To(BeTrue())
			Expect(called.Load()).To(BeFalse())
			all, _ := repo.All()
			Expect(all).To(HaveLen(1))
		})

		It("propagates repository Append errors", func() {
			boom := errors.New("boom")
			fr := &failingRepo{BlockRepository: repo}
			svc, f := funded(fr, eng)
			fr.appendErr = boom
			_, err := svc.Mine(context.Background(), f.tx())
			Expect(err).To(MatchError(boom))
		})

		It("propagates repository Last errors", func() {
			boom := errors.New("boom")
			fr := &failingRepo{BlockRepository: repo}
			svc, f := funded(fr, eng)
			fr.lastErr = boom
			_, err := svc.Mine(context.Background(), f.tx())
			Expect(err).To(MatchError(boom))
		})

		It("keeps services over separate repositories independent", func() {
			a, fa := funded(memory.New(), eng)
			b, _ := funded(memory.New(), eng)
			_, err := a.Mine(context.Background(), fa.tx())
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
			a, f := funded(repo, eng, WithGenerator(barrier))
			b, err := NewService(repo, eng, WithGenerator(barrier))
			Expect(err).NotTo(HaveOccurred())
			Expect(a).NotTo(BeNil())
			Expect(b).NotTo(BeNil())

			errs := make(chan error, 2)
			txs := [][]domain.Transaction{f.tx(), f.tx()}
			for i, svc := range []*Service{a, b} {
				i, svc := i, svc
				go func() {
					defer GinkgoRecover()
					_, err := svc.Mine(context.Background(), txs[i])
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
			svc, f := funded(repo, eng)
			const n = 10
			var wg sync.WaitGroup
			for i := 0; i < n; i++ {
				txs := f.tx()
				wg.Add(1)
				go func() {
					defer GinkgoRecover()
					defer wg.Done()
					_, err := svc.Mine(context.Background(), txs)
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
				f        *funding
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
				svc, f = funded(repo, eng, WithGenerator(func(ctx context.Context, c consensus.Consensus, prev *domain.Block, txs []domain.Transaction) (*domain.Block, error) {
					if calls.Add(1) == 1 {
						close(entered)
						<-release
					}
					return GenerateBlockWithContext(ctx, c, prev, txs)
				}))
				go func() {
					defer GinkgoRecover()
					_, err := svc.Mine(context.Background(), f.tx())
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

				blk, err := svc.Mine(ctx, f.tx())
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

			It("does not block Transaction or Balance", func() {
				done := make(chan struct{}, 1)
				go func() {
					defer GinkgoRecover()
					_, _, _, _ = svc.Transaction("any")
					_, _ = svc.Balance("any")
					close(done)
				}()
				Eventually(done, time.Second).Should(BeClosed())
			})
		})
	})
})
