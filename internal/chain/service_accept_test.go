package chain_test

import (
	"context"
	"errors"
	"strings"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"

	. "github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/mempool"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
	"github.com/petersonsalme/blockchain-with-go/internal/vm"
)

var errBoom = errors.New("boom")

var _ = Describe("Service.AcceptBlock", func() {
	ctx := context.Background()
	miner := recipient().Address()

	for _, be := range backends {
		Describe(be.name, func() {
			var (
				eng   consensus.Consensus
				repoA storage.IndexedRepository
				repoB storage.IndexedRepository
				poolB *mempool.Pool
				a, b  *Service
				f     *funding
			)

			// sibling returns a service with the same genesis as a, on a fresh repository.
			BeforeEach(func() {
				newRepo = be.open
				eng = pow.NewProofOfWork(1)
				repoA = newRepo()
				a, f = funded(repoA, eng, WithMinerAddress(miner))

				all, err := repoA.All()
				Expect(err).NotTo(HaveOccurred())

				genesis := all[0]

				repoB = newRepo()
				delta, err := domain.BlockDelta(repoB, genesis, true)
				Expect(err).NotTo(HaveOccurred())
				Expect(repoB.Commit(genesis, delta)).To(Succeed())

				poolB = mempool.New(0)
				b, err = NewService(repoB, eng, WithMempool(poolB), WithMinerAddress(miner))
				Expect(err).NotTo(HaveOccurred())
			})

			snapshot := func() ([]string, domain.UTXOSet, int) {
				blocks, err := repoB.All()
				Expect(err).NotTo(HaveOccurred())

				return hashes(blocks), utxoSnapshot(repoB), poolB.Len()
			}

			// untouched runs fn and checks it fails with want and changes nothing.
			untouched := func(fn func() error, want types.GomegaMatcher) {
				h, u, n := snapshot()

				err := fn()
				Expect(err).To(want)

				h2, u2, n2 := snapshot()
				Expect(h2).To(Equal(h))
				Expect(u2).To(Equal(u))
				Expect(n2).To(Equal(n))
			}

			Describe("a block that extends the tip", func() {
				It("is accepted and leaves B equal to A", func() {
					mined, err := a.Mine(ctx, f.tx())
					Expect(err).NotTo(HaveOccurred())
					Expect(b.HasBlock(mined.Hash)).To(BeFalse())

					Expect(b.AcceptBlock(ctx, mined)).To(Succeed())

					tip, err := b.Tip()
					Expect(err).NotTo(HaveOccurred())
					Expect(tip.Hash).To(Equal(mined.Hash))
					Expect(b.HasBlock(mined.Hash)).To(BeTrue())

					balA, _ := a.Balance(miner)
					balB, _ := b.Balance(miner)
					Expect(balB).To(Equal(balA))
					Expect(balB).NotTo(BeZero())
					Expect(utxoSnapshot(repoB)).To(Equal(utxoSnapshot(repoA)))
				})

				It("removes from the pool the transactions it confirms", func() {
					confirmed := f.tx()[0]
					unrelated := f.tx()[0]

					Expect(b.Submit(unrelated)).To(Succeed())
					Expect(b.Submit(confirmed)).To(Succeed())
					Expect(poolB.Len()).To(Equal(2))

					mined, err := a.Mine(ctx, []domain.Transaction{confirmed})
					Expect(err).NotTo(HaveOccurred())
					Expect(b.AcceptBlock(ctx, mined)).To(Succeed())

					Expect(poolB.Has(confirmed.ID)).To(BeFalse())
					Expect(poolB.Has(unrelated.ID)).To(BeTrue())
					Expect(poolB.Len()).To(Equal(1))
				})

				It("drops a pool transaction that spends what the block spends", func() {
					inPool := f.tx()[0]
					rival := spend(f.owner, f.ops[f.next-1], 8) // same outpoint, different tx

					Expect(b.Submit(inPool)).To(Succeed())

					mined, err := a.Mine(ctx, []domain.Transaction{rival})
					Expect(err).NotTo(HaveOccurred())
					Expect(b.AcceptBlock(ctx, mined)).To(Succeed())

					Expect(poolB.Has(inPool.ID)).To(BeFalse())
					Expect(poolB.Len()).To(BeZero())
				})

				It("applies the contract operations of the block", func() {
					dep := withContract(f.owner, f.ops[0], 9, deployOp(vm.CounterCode))
					mined, err := a.Mine(ctx, []domain.Transaction{dep})
					Expect(err).NotTo(HaveOccurred())
					Expect(b.AcceptBlock(ctx, mined)).To(Succeed())

					_, _, ok := b.Contract(dep.CalculateHash())
					Expect(ok).To(BeTrue())
				})
			})

			Describe("a block that does not extend the tip", func() {
				var mined *domain.Block

				BeforeEach(func() {
					var err error

					mined, err = a.Mine(ctx, f.tx())
					Expect(err).NotTo(HaveOccurred())
				})

				It("rejects nil with ErrInvalidBlock", func() {
					untouched(func() error { return b.AcceptBlock(ctx, nil) }, MatchError(ErrInvalidBlock))
				})

				It("rejects a wrong PrevHash", func() {
					bad := *mined
					bad.PrevHash = strings.Repeat("a", 64)

					untouched(func() error { return b.AcceptBlock(ctx, &bad) }, MatchError(ErrNotOnTip))
				})

				It("rejects a wrong height", func() {
					bad := *mined
					bad.Index++

					untouched(func() error { return b.AcceptBlock(ctx, &bad) }, MatchError(ErrNotOnTip))
				})

				It("rejects the same block twice", func() {
					Expect(b.AcceptBlock(ctx, mined)).To(Succeed())
					untouched(func() error { return b.AcceptBlock(ctx, mined) }, MatchError(ErrNotOnTip))
				})
			})

			Describe("a tampered block", func() {
				history := func() []*domain.Block {
					all, err := repoB.All()
					Expect(err).NotTo(HaveOccurred())

					return all
				}
				// reseal rebuilds the seal of bad on top of the chain of B.
				reseal := func(bad *domain.Block) *domain.Block {
					Expect(eng.Seal(ctx, history(), bad)).To(Succeed())
					return bad
				}
				block := func(txs ...domain.Transaction) *domain.Block {
					return grow(eng, history(), txs)[len(history())]
				}

				It("fails consensus on a broken seal", func() {
					bad := *block(f.tx()...)

					for bad.Hash[0] < '8' {
						bad.Nonce++
						bad.Hash = bad.CalculateHash()
					}

					untouched(func() error { return b.AcceptBlock(ctx, &bad) }, MatchError(consensus.ErrInvalidProofOfWork))
				})

				It("fails the block rules on a wrong merkle root", func() {
					bad := *block(f.tx()...)
					bad.MerkleRoot = strings.Repeat("f", 64)
					reseal(&bad)

					untouched(func() error { return b.AcceptBlock(ctx, &bad) }, MatchError(ContainSubstring(string(domain.CodeMerkleRootMismatch))))
				})

				It("fails the UTXO rules on a double spend", func() {
					first := f.tx()[0]
					second := spend(f.owner, f.ops[f.next-1], 8)

					untouched(func() error { return b.AcceptBlock(ctx, block(first, second)) }, MatchError(domain.ErrDoubleSpend))
				})

				It("fails on an invalid signature", func() {
					tx := f.tx()[0]
					tx.Inputs[0].Signature = strings.Repeat("0", 128)

					untouched(func() error { return b.AcceptBlock(ctx, block(tx)) }, MatchError(domain.ErrInvalidSignature))
				})

				It("fails on an invalid contract operation", func() {
					tx := withContract(f.owner, f.ops[f.next], 9, callOp(strings.Repeat("0", 64), 100))

					untouched(func() error { return b.AcceptBlock(ctx, block(tx)) }, MatchError(vm.ErrUnknownContract))
				})
			})

			Describe("a failing write", func() {
				It("stops the service on an unknown commit error, like Mine", func() {
					mined, err := a.Mine(ctx, f.tx())
					Expect(err).NotTo(HaveOccurred())

					fw := &failingWrites{IndexedRepository: repoB, commitErr: storage.ErrConflict}
					svc, err := NewService(fw, eng)
					Expect(err).NotTo(HaveOccurred())

					Expect(svc.AcceptBlock(ctx, mined)).To(MatchError(storage.ErrConflict))
					Expect(svc.AcceptBlock(ctx, mined)).To(MatchError(storage.ErrConflict))

					fw.commitErr = errBoom

					Expect(svc.AcceptBlock(ctx, mined)).To(MatchError(ErrStateDiverged))
					Expect(svc.AcceptBlock(ctx, mined)).To(MatchError(ErrStateDiverged))
				})
			})

			Describe("concurrent with Mine", func() {
				It("lets at most one of them write", func() {
					mined, err := a.Mine(ctx, f.tx())
					Expect(err).NotTo(HaveOccurred())

					var (
						wg                 sync.WaitGroup
						acceptErr, mineErr error
					)

					wg.Add(2)

					go func() {
						defer GinkgoRecover()
						defer wg.Done()

						acceptErr = b.AcceptBlock(ctx, mined)
					}()
					go func() {
						defer GinkgoRecover()
						defer wg.Done()

						_, mineErr = b.Mine(ctx, f.tx())
					}()

					wg.Wait()

					Expect(mineErr).NotTo(HaveOccurred())

					want := 2
					if acceptErr == nil {
						want = 3
					} else {
						Expect(acceptErr).To(MatchError(ErrNotOnTip))
					}

					Expect(mustAll(repoB)).To(HaveLen(want))
					Expect(utxoSnapshot(repoB)).To(Equal(replayRepo(repoB)))
				})
			})
		})
	}
})

var _ = Describe("Service.HasBlock and HasTx", func() {
	ctx := context.Background()

	for _, be := range backends {
		Describe(be.name, func() {
			It("answers for blocks, pool and confirmed transactions", func() {
				newRepo = be.open
				svc, f := funded(newRepo(), pow.NewProofOfWork(1))

				pending := f.tx()[0]
				Expect(svc.Submit(pending)).To(Succeed())

				done := f.tx()[0]
				mined, err := svc.Mine(ctx, []domain.Transaction{done})
				Expect(err).NotTo(HaveOccurred())

				Expect(svc.HasBlock(mined.Hash)).To(BeTrue())
				Expect(svc.HasBlock(strings.Repeat("e", 64))).To(BeFalse())
				Expect(svc.HasTx(pending.ID)).To(BeTrue())
				Expect(svc.HasTx(done.ID)).To(BeTrue())
				Expect(svc.HasTx(strings.Repeat("e", 64))).To(BeFalse())
			})
		})
	}
})
