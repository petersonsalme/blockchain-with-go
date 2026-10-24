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
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

// stubRepo returns a fixed chain whose indexes are always stale, bypassing the
// append-order checks of the in-memory repository. Reindex replays the chain.
type stubRepo struct {
	storage.IndexedRepository
	blocks []*domain.Block
}

func (s *stubRepo) Last() (*domain.Block, error)  { return s.blocks[len(s.blocks)-1], nil }
func (s *stubRepo) All() ([]*domain.Block, error) { return s.blocks, nil }
func (s *stubRepo) IndexCurrent() (bool, error)   { return false, nil }
func (s *stubRepo) Reindex() error {
	_, err := domain.BuildUTXOSet(s.blocks)
	return err
}

var _ = Describe("Service UTXO rules", func() {
	var (
		repo  *memory.Repository
		eng   *pow.ProofOfWork
		svc   *Service
		f     *funding
		other *wallet.Wallet
	)

	mine := func(txs ...domain.Transaction) (*domain.Block, error) {
		return svc.Mine(context.Background(), txs)
	}
	chainLen := func() int {
		all, err := repo.All()
		Expect(err).NotTo(HaveOccurred())
		return len(all)
	}
	// expectUntouched runs fn and asserts that the chain and the UTXO set did not change.
	expectUntouched := func(fn func()) {
		before, err := repo.All()
		Expect(err).NotTo(HaveOccurred())
		snapshot := utxoSnapshot(repo)
		fn()
		after, err := repo.All()
		Expect(err).NotTo(HaveOccurred())
		Expect(after).To(Equal(before))
		Expect(utxoSnapshot(repo)).To(Equal(snapshot))
	}
	expectRebuildMatches := func() {
		all, err := repo.All()
		Expect(err).NotTo(HaveOccurred())
		built, err := domain.BuildUTXOSet(all)
		Expect(err).NotTo(HaveOccurred())
		Expect(utxoSnapshot(repo)).To(Equal(built))
	}

	BeforeEach(func() {
		var err error
		repo = memory.New()
		eng = pow.NewProofOfWork(1)
		svc, f = funded(repo, eng)
		other, err = wallet.New()
		Expect(err).NotTo(HaveOccurred())
	})

	Describe("NewService", func() {
		It("allocates the genesis outputs in an empty repository", func() {
			Expect(utxoSnapshot(repo)).To(HaveLen(fundedOutputs))
			all, _ := repo.All()
			Expect(all).To(HaveLen(1))
			genesis := all[0]
			Expect(genesis.Transactions).To(HaveLen(1))
			alloc := genesis.Transactions[0]
			Expect(alloc.Inputs).To(BeEmpty())
			Expect(alloc.Outputs).To(HaveLen(fundedOutputs))
			Expect(alloc.Timestamp).To(Equal(genesis.Timestamp))
			Expect(alloc.ID).To(Equal(alloc.CalculateHash()))
			for i, op := range f.ops {
				Expect(op).To(Equal(domain.OutPoint{TxID: alloc.CalculateHash(), Index: uint32(i)}))
				got, found := repo.Lookup(op)
				Expect(found).To(BeTrue())
				Expect(got).To(Equal(alloc.Outputs[i]))
			}
		})

		It("creates an empty genesis without allocations", func() {
			emptyRepo := memory.New()
			_, err := NewService(emptyRepo, eng)
			Expect(err).NotTo(HaveOccurred())
			all, _ := emptyRepo.All()
			Expect(all[0].Transactions).To(BeEmpty())
			Expect(utxoSnapshot(emptyRepo)).To(BeEmpty())
		})

		It("creates an empty genesis for an empty allocation list", func() {
			emptyRepo := memory.New()
			_, err := NewService(emptyRepo, eng, WithGenesisAllocations([]domain.TxOut{}))
			Expect(err).NotTo(HaveOccurred())
			all, _ := emptyRepo.All()
			Expect(all[0].Transactions).To(BeEmpty())
		})

		It("ignores the allocations when the repository already has a chain", func() {
			again, err := NewService(repo, eng, WithGenesisAllocations([]domain.TxOut{{Amount: 99, Address: other.Address()}}))
			Expect(err).NotTo(HaveOccurred())
			Expect(chainLen()).To(Equal(1))
			blocks, _ := again.Blocks()
			Expect(blocks[0].Transactions[0].Outputs).To(HaveLen(fundedOutputs))
		})

		DescribeTable("rejects invalid allocations without storing a genesis",
			func(allocs []domain.TxOut) {
				bad := memory.New()
				_, err := NewService(bad, eng, WithGenesisAllocations(allocs))
				Expect(err).To(MatchError(domain.ErrInvalidOutput))
				_, lastErr := bad.Last()
				Expect(lastErr).To(MatchError(storage.ErrEmpty))
			},
			Entry("zero amount", []domain.TxOut{{Amount: 0, Address: wallet.AddressFromPublicKey(make([]byte, 32))}}),
			Entry("bad address", []domain.TxOut{{Amount: 1, Address: "nope"}}),
		)

		It("rebuilds an equal set from a populated repository", func() {
			_, err := mine(f.tx()[0])
			Expect(err).NotTo(HaveOccurred())
			_, err = mine(f.tx()[0])
			Expect(err).NotTo(HaveOccurred())

			before := utxoSnapshot(repo)
			Expect(repo.Reindex()).To(Succeed())
			_, err = NewService(repo, eng)
			Expect(err).NotTo(HaveOccurred())
			Expect(utxoSnapshot(repo)).To(Equal(before))
		})

		It("keeps rejecting an already mined spend after the rebuild", func() {
			spent := f.tx()[0]
			_, err := mine(spent)
			Expect(err).NotTo(HaveOccurred())

			again, err := NewService(repo, eng)
			Expect(err).NotTo(HaveOccurred())
			_, err = again.Mine(context.Background(), []domain.Transaction{spent})
			Expect(err).To(MatchError(domain.ErrInputMissingOrSpent))
			Expect(chainLen()).To(Equal(2))
		})

		It("fails when the stored chain is not a valid UTXO history", func() {
			bad := memory.New()
			Expect(bad.Append(&domain.Block{Index: 0})).To(Succeed())
			Expect(bad.Append(&domain.Block{Index: 1, Transactions: []domain.Transaction{
				{Outputs: []domain.TxOut{{Amount: 1, Address: other.Address()}}},
			}})).To(Succeed())
			_, err := NewService(bad, eng)
			Expect(err).To(MatchError(domain.ErrMissingInputs))
		})

		It("treats a first block with Index 5 as a regular block, not the genesis", func() {
			bad := memory.New()
			Expect(bad.Append(&domain.Block{Index: 5, Transactions: []domain.Transaction{
				{Outputs: []domain.TxOut{{Amount: 1, Address: other.Address()}}},
			}})).To(Succeed())
			_, err := NewService(bad, eng)
			Expect(err).To(MatchError(domain.ErrMissingInputs))
		})

		It("treats an Index 0 block at position 1 as a regular block, not the genesis", func() {
			stub := &stubRepo{blocks: []*domain.Block{
				{Index: 0},
				{Index: 0, Transactions: []domain.Transaction{
					{Outputs: []domain.TxOut{{Amount: 1, Address: other.Address()}}},
				}},
			}}
			_, err := NewService(stub, eng)
			Expect(err).To(MatchError(domain.ErrMissingInputs))
		})

		It("rebuilds the indexes of a repository written without them", func() {
			legacy := memory.New()
			genesis := &domain.Block{Index: 0, Transactions: []domain.Transaction{}}
			Expect(legacy.Append(genesis)).To(Succeed())
			current, err := legacy.IndexCurrent()
			Expect(err).NotTo(HaveOccurred())
			Expect(current).To(BeFalse())

			_, err = NewService(legacy, eng)
			Expect(err).NotTo(HaveOccurred())
			current, err = legacy.IndexCurrent()
			Expect(err).NotTo(HaveOccurred())
			Expect(current).To(BeTrue())
		})
	})

	Describe("Mine", func() {
		It("seals the merkle root of the mined transactions into the block", func() {
			blk, err := mine(f.tx()...)
			Expect(err).NotTo(HaveOccurred())
			Expect(blk.MerkleRoot).To(Equal(domain.MerkleRoot(blk.Transactions)))
			Expect(blk.Hash).To(Equal(blk.CalculateHash()))
		})

		It("rejects a block whose merkle root does not match and changes nothing", func() {
			wrong := domain.MerkleRoot(nil)
			bad, err := NewService(repo, eng, WithGenerator(func(ctx context.Context, c consensus.Consensus, history []*domain.Block, txs []domain.Transaction) (*domain.Block, error) {
				b, err := GenerateBlockWithContext(ctx, c, history, txs)
				if err != nil {
					return nil, err
				}
				b.MerkleRoot = wrong
				b.Hash = b.CalculateHash()
				return b, nil
			}))
			Expect(err).NotTo(HaveOccurred())

			before, err := repo.All()
			Expect(err).NotTo(HaveOccurred())
			snapshot := utxoSnapshot(repo)

			blk, err := bad.Mine(context.Background(), f.tx())
			Expect(blk).To(BeNil())
			var ve *domain.ValidationError
			Expect(errors.As(err, &ve)).To(BeTrue())
			Expect(ve.Code).To(Equal(domain.CodeMerkleRootMismatch))
			Expect(ve.Details).To(HaveKeyWithValue("got", wrong))

			after, err := repo.All()
			Expect(err).NotTo(HaveOccurred())
			Expect(after).To(Equal(before))
			Expect(utxoSnapshot(repo)).To(Equal(snapshot))
		})

		It("applies the delta of a mined block to the set", func() {
			tx := f.tx()[0]
			blk, err := mine(tx)
			Expect(err).NotTo(HaveOccurred())
			Expect(blk.Transactions).To(Equal([]domain.Transaction{tx}))

			snap := utxoSnapshot(repo)
			Expect(snap).To(HaveLen(fundedOutputs))
			_, stillThere := snap[f.ops[0]]
			Expect(stillThere).To(BeFalse())
			Expect(snap).To(HaveKeyWithValue(domain.OutPoint{TxID: tx.CalculateHash()}, tx.Outputs[0]))
			expectRebuildMatches()
		})

		It("accepts a transaction that spends an output created earlier in the same request", func() {
			first := f.tx()[0]
			// first pays recipient(), so the chained spend must be signed by that key.
			second := spend(recipient(), domain.OutPoint{TxID: first.CalculateHash()}, 5)

			_, err := mine(first, second)
			Expect(err).NotTo(HaveOccurred())
			Expect(utxoSnapshot(repo)).To(HaveKey(domain.OutPoint{TxID: second.CalculateHash()}))
			Expect(utxoSnapshot(repo)).NotTo(HaveKey(domain.OutPoint{TxID: first.CalculateHash()}))
			expectRebuildMatches()
		})

		It("rejects a chained spend signed by the wrong key", func() {
			first := f.tx()[0]
			second := spend(other, domain.OutPoint{TxID: first.CalculateHash()}, 5)
			expectUntouched(func() {
				_, err := mine(first, second)
				Expect(err).To(MatchError(domain.ErrInputOwnerMismatch))
			})
		})

		It("rejects two transactions spending the same outpoint in one request", func() {
			a := spend(f.owner, f.ops[0], 9)
			b := spend(f.owner, f.ops[0], 8)
			expectUntouched(func() {
				_, err := mine(a, b)
				Expect(err).To(MatchError(domain.ErrDoubleSpend))
			})
		})

		It("rejects a resend of an already mined transaction", func() {
			tx := f.tx()[0]
			_, err := mine(tx)
			Expect(err).NotTo(HaveOccurred())
			expectUntouched(func() {
				_, err := mine(tx)
				Expect(err).To(MatchError(domain.ErrInputMissingOrSpent))
			})
		})

		It("rejects another transaction spending an outpoint spent in an earlier block", func() {
			_, err := mine(spend(f.owner, f.ops[0], 9))
			Expect(err).NotTo(HaveOccurred())
			expectUntouched(func() {
				_, err := mine(spend(f.owner, f.ops[0], 8))
				Expect(err).To(MatchError(domain.ErrInputMissingOrSpent))
			})
		})

		It("reports a missing input before checking its signature", func() {
			tx := spend(other, domain.OutPoint{TxID: "unknown"}, 1)
			tx.Inputs[0].Signature = ""
			expectUntouched(func() {
				_, err := mine(tx)
				Expect(err).To(MatchError(domain.ErrInputMissingOrSpent))
			})
		})

		DescribeTable("rejects an invalid transaction without changing the chain or the set",
			func(build func(f *funding, other *wallet.Wallet) domain.Transaction, want error) {
				tx := build(f, other)
				expectUntouched(func() {
					_, err := mine(tx)
					Expect(err).To(MatchError(want))
				})
				// the funds were not consumed
				_, err := mine(spend(f.owner, f.ops[0], 1))
				Expect(err).NotTo(HaveOccurred())
			},
			Entry("outputs exceed inputs", func(f *funding, _ *wallet.Wallet) domain.Transaction {
				return spend(f.owner, f.ops[0], 11)
			}, domain.ErrInsufficientInputs),
			Entry("input owned by another key", func(f *funding, other *wallet.Wallet) domain.Transaction {
				return spend(other, f.ops[0], 1)
			}, domain.ErrInputOwnerMismatch),
			Entry("zero-amount output", func(f *funding, _ *wallet.Wallet) domain.Transaction {
				return spend(f.owner, f.ops[0], 0)
			}, domain.ErrInvalidOutput),
			Entry("no inputs", func(f *funding, _ *wallet.Wallet) domain.Transaction {
				tx := spend(f.owner, f.ops[0], 1)
				tx.Inputs = nil
				return tx
			}, domain.ErrMissingInputs),
			Entry("unsigned", func(f *funding, _ *wallet.Wallet) domain.Transaction {
				tx := spend(f.owner, f.ops[0], 1)
				tx.Inputs[0].Signature = ""
				return tx
			}, domain.ErrMissingSignature),
			Entry("bad signature", func(f *funding, _ *wallet.Wallet) domain.Transaction {
				tx := spend(f.owner, f.ops[0], 1)
				tx.Outputs[0].Amount = 2
				return tx
			}, domain.ErrInvalidSignature),
			Entry("bad public key", func(f *funding, _ *wallet.Wallet) domain.Transaction {
				tx := spend(f.owner, f.ops[0], 1)
				tx.Inputs[0].PublicKey = "abcd"
				return tx
			}, domain.ErrInvalidPublicKey),
			Entry("signature on the second input missing", func(f *funding, _ *wallet.Wallet) domain.Transaction {
				tx := spend(f.owner, f.ops[0], 1)
				tx.Inputs = append(tx.Inputs, domain.TxIn{PrevTxID: f.ops[1].TxID, OutIndex: f.ops[1].Index})
				Expect(domain.SignInput(&tx, 0, f.owner)).To(Succeed())
				return tx
			}, domain.ErrMissingSignature),
		)

		It("checks every input of a multi-input transaction", func() {
			tx := domain.Transaction{
				Inputs:    []domain.TxIn{{PrevTxID: f.ops[0].TxID, OutIndex: 0}, {PrevTxID: f.ops[1].TxID, OutIndex: 1}},
				Outputs:   []domain.TxOut{{Amount: 20, Address: other.Address()}},
				Timestamp: time.Now(),
			}
			Expect(domain.SignInput(&tx, 0, f.owner)).To(Succeed())
			Expect(domain.SignInput(&tx, 1, f.owner)).To(Succeed())
			_, err := mine(tx)
			Expect(err).NotTo(HaveOccurred())
			Expect(utxoSnapshot(repo)).To(HaveLen(fundedOutputs - 1))
			expectRebuildMatches()
		})

		It("leaves the chain and the set untouched when consensus rejects the block", func() {
			rejRepo := memory.New()
			rej, rf := funded(rejRepo, rejectingConsensus{inner: eng})
			before := utxoSnapshot(rejRepo)

			_, err := rej.Mine(context.Background(), rf.tx())
			Expect(err).To(MatchError(ErrInvalidBlock))
			all, _ := rejRepo.All()
			Expect(all).To(HaveLen(1))
			Expect(utxoSnapshot(rejRepo)).To(Equal(before))
		})

		It("returns a tip conflict from the commit as is and keeps accepting writes", func() {
			failing := &failingWrites{IndexedRepository: memory.New()}
			s, ff := funded(failing, eng)
			failing.commitErr = storage.ErrConflict

			_, err := s.Mine(context.Background(), ff.tx())
			Expect(err).To(MatchError(storage.ErrConflict))
			Expect(errors.Is(err, ErrStateDiverged)).To(BeFalse())

			failing.commitErr = nil
			_, err = s.Mine(context.Background(), ff.tx())
			Expect(err).NotTo(HaveOccurred())
		})

		It("stops the service when the commit fails after the seal", func() {
			boom := errors.New("boom")
			memRepo := memory.New()
			failing := &failingWrites{IndexedRepository: memRepo}
			var genCalls atomic.Int32
			s, ff := funded(failing, eng, WithGenerator(func(ctx context.Context, c consensus.Consensus, history []*domain.Block, txs []domain.Transaction) (*domain.Block, error) {
				genCalls.Add(1)
				return GenerateBlockWithContext(ctx, c, history, txs)
			}))
			failing.commitErr = boom

			_, err := s.Mine(context.Background(), ff.tx())
			Expect(err).To(MatchError(ContainSubstring("boom")))
			Expect(errors.Is(err, ErrStateDiverged)).To(BeTrue())

			// The failed commit changed nothing
			all, err := memRepo.All()
			Expect(err).NotTo(HaveOccurred())
			Expect(all).To(HaveLen(1))
			callsBefore := genCalls.Load()

			// Every subsequent Mine returns ErrStateDiverged without calling Generator or Append
			_, err = s.Mine(context.Background(), ff.tx())
			Expect(errors.Is(err, ErrStateDiverged)).To(BeTrue())
			Expect(genCalls.Load()).To(Equal(callsBefore))

			allAfter, err := memRepo.All()
			Expect(err).NotTo(HaveOccurred())
			Expect(allAfter).To(HaveLen(1))

			// Blocks() continues to respond
			blocks, err := s.Blocks()
			Expect(err).NotTo(HaveOccurred())
			Expect(blocks).To(HaveLen(1))
		})

		It("keeps the set equal to a rebuild from the chain after many blocks", func() {
			for i := 0; i < 5; i++ {
				_, err := mine(f.tx()[0])
				Expect(err).NotTo(HaveOccurred())
			}
			expectRebuildMatches()
		})

		It("lets exactly one of 50 concurrent requests spend the same outpoint", func() {
			const n = 50
			var wg sync.WaitGroup
			var ok, spent atomic.Int32
			for i := 0; i < n; i++ {
				tx := spend(f.owner, f.ops[0], uint64(1+i%9))
				tx.Timestamp = tx.Timestamp.Add(time.Duration(i) * time.Nanosecond)
				tx.ID = tx.CalculateHash()
				Expect(domain.SignInput(&tx, 0, f.owner)).To(Succeed())
				wg.Add(1)
				go func() {
					defer GinkgoRecover()
					defer wg.Done()
					_, err := mine(tx)
					switch {
					case err == nil:
						ok.Add(1)
					case errors.Is(err, domain.ErrInputMissingOrSpent):
						spent.Add(1)
					default:
						Fail("unexpected error: " + err.Error())
					}
				}()
			}
			wg.Wait()

			Expect(ok.Load()).To(Equal(int32(1)))
			Expect(spent.Load()).To(Equal(int32(n - 1)))
			Expect(chainLen()).To(Equal(2))
			expectRebuildMatches()
		})
	})
})
