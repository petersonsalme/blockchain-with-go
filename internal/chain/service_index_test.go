package chain_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	. "github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/bolt"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
)

// indexSpy counts the full scans and fails the index reads on demand.
type indexSpy struct {
	storage.IndexedRepository
	all     atomic.Int32
	byTxErr error
	// heightErrAfter fails TxHeight from its n-th call on (0 = never).
	heightErrAfter int32
	heights        atomic.Int32
	byHeightErr    error
	lastErrAfter   int32
	lasts          atomic.Int32
	// onTxs runs after every TxsByAddress read.
	onTxs func()
	txs   atomic.Int32
	empty bool
}

func (s *indexSpy) All() ([]*domain.Block, error) {
	s.all.Add(1)
	return s.IndexedRepository.All()
}

func (s *indexSpy) Last() (*domain.Block, error) {
	if n := s.lasts.Add(1); s.lastErrAfter > 0 && n >= s.lastErrAfter {
		return nil, errors.New("last failed")
	}
	return s.IndexedRepository.Last()
}

func (s *indexSpy) TxHeight(id string) (int, error) {
	if n := s.heights.Add(1); s.heightErrAfter > 0 && n >= s.heightErrAfter {
		return 0, errors.New("height failed")
	}
	return s.IndexedRepository.TxHeight(id)
}

func (s *indexSpy) TxsByAddress(addr string) ([]string, error) {
	if s.byTxErr != nil {
		return nil, s.byTxErr
	}
	ids, err := s.IndexedRepository.TxsByAddress(addr)
	s.txs.Add(1)
	if s.onTxs != nil {
		s.onTxs()
	}
	return ids, err
}

func (s *indexSpy) ByHeight(h int) (*domain.Block, error) {
	if s.byHeightErr != nil {
		return nil, s.byHeightErr
	}
	b, err := s.IndexedRepository.ByHeight(h)
	if err == nil && s.empty {
		stripped := *b
		stripped.Transactions = nil
		return &stripped, nil
	}
	return b, err
}

func ids(txs []domain.Transaction) []string {
	out := make([]string, 0, len(txs))
	for _, tx := range txs {
		out = append(out, tx.ID)
	}
	return out
}

var _ = Describe("Service persisted indexes", func() {
	repos := map[string]func() storage.IndexedRepository{
		"memory": func() storage.IndexedRepository { return memory.New() },
		"bolt": func() storage.IndexedRepository {
			r, err := bolt.Open(filepath.Join(GinkgoT().TempDir(), "chain.db"))
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = r.Close() })
			return r
		},
	}

	for name, open := range repos {
		open := open
		Describe(name, func() {
			var (
				repo      storage.IndexedRepository
				spy       *indexSpy
				svc       *Service
				f         *funding
				eng       *pow.ProofOfWork
				genesisTx domain.Transaction
				tx0       domain.Transaction
				shared    domain.Transaction
				onlyA     domain.Transaction
			)

			BeforeEach(func() {
				repo = open()
				spy = &indexSpy{IndexedRepository: repo}
				eng = pow.New(1)
				svc, f = funded(spy, eng)
				genesisTx = mustAll(repo)[0].Transactions[0]

				tx0 = f.tx()[0]
				_, err := svc.Mine(context.Background(), []domain.Transaction{tx0})
				Expect(err).NotTo(HaveOccurred())
				shared = spend(f.owner, f.ops[1], 9)
				onlyA = spend(f.owner, f.ops[2], 8)
				_, err = svc.Mine(context.Background(), []domain.Transaction{shared, onlyA})
				Expect(err).NotTo(HaveOccurred())
			})

			// reorg switches to a heavier branch that keeps shared and drops onlyA.
			reorg := func() []domain.Transaction {
				blocks := mustAll(repo)
				b3 := []domain.Transaction{
					spend(recipient(), outpointOf(shared, 0), 5),
					spend(f.owner, f.ops[3], 7),
				}
				cand := grow(eng, blocks[:2], []domain.Transaction{shared}, b3)
				_, err := svc.ReplaceChain(context.Background(), cand)
				Expect(err).NotTo(HaveOccurred())
				return b3
			}

			Describe("Transaction and TxProof", func() {
				It("find every transaction at its height without scanning the chain", func() {
					reorg()
					spy.all.Store(0)
					for _, b := range mustAll(repo) {
						for _, want := range b.Transactions {
							tx, hash, height, err := svc.Transaction(want.ID)
							Expect(err).NotTo(HaveOccurred())
							Expect(tx.ID).To(Equal(want.ID))
							Expect(hash).To(Equal(b.Hash))
							Expect(height).To(Equal(b.Index))

							proof, err := svc.TxProof(want.ID)
							Expect(err).NotTo(HaveOccurred())
							Expect(proof.BlockIndex).To(Equal(b.Index))
							Expect(proof.BlockHash).To(Equal(b.Hash))
						}
					}
					Expect(spy.all.Load()).To(BeZero())
				})

				It("reports a transaction of the removed branch as not found", func() {
					reorg()
					_, _, _, err := svc.Transaction(onlyA.ID)
					Expect(err).To(MatchError(storage.ErrNotFound))
					_, err = svc.TxProof(onlyA.ID)
					Expect(err).To(MatchError(ErrTxNotFound))
				})

				It("returns the read error when the block of an indexed transaction cannot be read", func() {
					boom := errors.New("boom")
					spy.byHeightErr = boom
					_, _, _, err := svc.Transaction(shared.ID)
					Expect(err).To(MatchError(boom))
				})

				It("reports not found when the indexed block no longer holds the transaction", func() {
					spy.empty = true
					_, _, _, err := svc.Transaction(shared.ID)
					Expect(err).To(MatchError(storage.ErrNotFound))
				})
			})

			Describe("TransactionsByAddress", func() {
				It("lists the transactions that create or spend an output, in chain order", func() {
					got, err := svc.TransactionsByAddress(f.owner.Address())
					Expect(err).NotTo(HaveOccurred())
					Expect(ids(got)).To(Equal([]string{genesisTx.ID, tx0.ID, shared.ID, onlyA.ID}))

					got, err = svc.TransactionsByAddress(recipient().Address())
					Expect(err).NotTo(HaveOccurred())
					Expect(ids(got)).To(Equal([]string{tx0.ID, shared.ID, onlyA.ID}))
				})

				It("returns an empty, non-nil slice for an unknown address", func() {
					got, err := svc.TransactionsByAddress("unknown")
					Expect(err).NotTo(HaveOccurred())
					Expect(got).NotTo(BeNil())
					Expect(got).To(BeEmpty())
				})

				It("drops the transactions of the removed branch and adds those of the new one", func() {
					b3 := reorg()

					got, err := svc.TransactionsByAddress(f.owner.Address())
					Expect(err).NotTo(HaveOccurred())
					Expect(ids(got)).To(Equal([]string{genesisTx.ID, tx0.ID, shared.ID, b3[1].ID}))

					got, err = svc.TransactionsByAddress(recipient().Address())
					Expect(err).NotTo(HaveOccurred())
					Expect(ids(got)).To(Equal([]string{tx0.ID, shared.ID, b3[0].ID, b3[1].ID}))
				})

				It("returns the index error", func() {
					spy.byTxErr = errors.New("boom")
					_, err := svc.TransactionsByAddress(f.owner.Address())
					Expect(err).To(MatchError("boom"))
				})

				It("returns the error of a transaction that cannot be located", func() {
					spy.heightErrAfter = 1
					_, err := svc.TransactionsByAddress(f.owner.Address())
					Expect(err).To(MatchError("height failed"))
				})

				It("returns the error of a block that cannot be read", func() {
					spy.byHeightErr = errors.New("boom")
					_, err := svc.TransactionsByAddress(f.owner.Address())
					Expect(err).To(MatchError("boom"))
				})

				It("reports not found when an indexed block no longer holds the transaction", func() {
					spy.empty = true
					_, err := svc.TransactionsByAddress(f.owner.Address())
					Expect(err).To(MatchError(storage.ErrNotFound))
				})

				It("returns the error of reading the tip", func() {
					spy.lastErrAfter = 1
					_, err := svc.TransactionsByAddress(f.owner.Address())
					Expect(err).To(MatchError("last failed"))
				})

				It("returns the error of reading the tip after the read", func() {
					spy.lastErrAfter = 2
					_, err := svc.TransactionsByAddress(f.owner.Address())
					Expect(err).To(MatchError("last failed"))
				})

				It("reads again when a block lands during the read", func() {
					next := 4
					spy.onTxs = func() {
						if spy.txs.Load() > 1 {
							return
						}
						_, err := svc.Mine(context.Background(), []domain.Transaction{spend(f.owner, f.ops[next], 9)})
						Expect(err).NotTo(HaveOccurred())
					}
					got, err := svc.TransactionsByAddress(f.owner.Address())
					Expect(err).NotTo(HaveOccurred())
					Expect(got).To(HaveLen(5))
					Expect(spy.txs.Load()).To(Equal(int32(2)))
				})

				It("reads once when the tip stays put", func() {
					_, err := svc.TransactionsByAddress(f.owner.Address())
					Expect(err).NotTo(HaveOccurred())
					Expect(spy.txs.Load()).To(Equal(int32(1)))
				})

				It("gives up when blocks keep landing during the read", func() {
					next := 4
					spy.onTxs = func() {
						_, err := svc.Mine(context.Background(), []domain.Transaction{spend(f.owner, f.ops[next], 9)})
						Expect(err).NotTo(HaveOccurred())
						next++
					}
					_, err := svc.TransactionsByAddress(f.owner.Address())
					Expect(err).To(MatchError(ErrTipMoving))
					Expect(spy.txs.Load()).To(Equal(int32(5)))
				})
			})

			Describe("Balance", func() {
				It("reads the persisted UTXO set, before and after a reorg", func() {
					reorg()
					want := replay(mustAll(repo))
					for _, addr := range []string{f.owner.Address(), recipient().Address()} {
						var sum uint64
						var count int
						for _, out := range want {
							if out.Address == addr {
								sum += out.Amount
								count++
							}
						}
						got, utxos := svc.Balance(addr)
						Expect(got).To(Equal(sum), addr)
						Expect(utxos).To(HaveLen(count), addr)
					}
				})

				It("is empty when the repository cannot be read", func() {
					spy.byTxErr = errors.New("boom")
					got, utxos := svc.Balance(f.owner.Address())
					Expect(got).To(BeZero())
					Expect(utxos).NotTo(BeNil())
					Expect(utxos).To(BeEmpty())
				})
			})

			Describe("Reindex", func() {
				It("changes nothing after any sequence of writes", func() {
					reorg()
					_, err := svc.Mine(context.Background(), []domain.Transaction{spend(f.owner, f.ops[4], 9)})
					Expect(err).NotTo(HaveOccurred())

					before := utxoSnapshot(repo)
					Expect(before).To(Equal(replay(mustAll(repo))))
					owner, err := svc.TransactionsByAddress(f.owner.Address())
					Expect(err).NotTo(HaveOccurred())

					Expect(repo.Reindex()).To(Succeed())
					Expect(utxoSnapshot(repo)).To(Equal(before))
					again, err := svc.TransactionsByAddress(f.owner.Address())
					Expect(err).NotTo(HaveOccurred())
					Expect(ids(again)).To(Equal(ids(owner)))
				})

				It("survives a restart without scanning the chain", func() {
					reopened := &indexSpy{IndexedRepository: repo}
					again, err := NewService(reopened, eng)
					Expect(err).NotTo(HaveOccurred())
					Expect(reopened.all.Load()).To(BeZero())
					got, err := again.TransactionsByAddress(f.owner.Address())
					Expect(err).NotTo(HaveOccurred())
					Expect(ids(got)).To(Equal([]string{genesisTx.ID, tx0.ID, shared.ID, onlyA.ID}))
				})
			})
		})
	}
})
