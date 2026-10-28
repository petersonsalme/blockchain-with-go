package chain_test

import (
	"context"
	"errors"
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	. "github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
)

// pageIDs is the transaction ids of a page, in order.
func pageIDs(p AddressPage) []string {
	out := make([]string, 0, len(p.Txs))
	for _, t := range p.Txs {
		out = append(out, t.Tx.CalculateHash())
	}

	return out
}

var _ = Describe("Service.AddressHistory", func() {
	ctx := context.Background()

	for _, be := range backends {
		Describe(be.name, func() {
			var (
				repo storage.IndexedRepository
				spy  *indexSpy
				svc  *Service
				f    *funding
				eng  = pow.New(1)
			)

			BeforeEach(func() {
				repo = be.open()
				spy = &indexSpy{IndexedRepository: repo}
				svc, f = funded(spy, eng)
			})

			if be.pruned {
				It("lists only the retained heights and reports pruned_below", func() {
					var mined []*domain.Block

					for range prunedTip {
						b, err := svc.Mine(ctx, f.tx())
						Expect(err).NotTo(HaveOccurred())

						mined = append(mined, b)
					}

					page, err := svc.AddressHistory(f.owner.Address(), "", 100)
					Expect(err).NotTo(HaveOccurred())
					Expect(page.PrunedBelow).To(Equal(5))
					Expect(page.More).To(BeFalse())
					Expect(page.Txs).To(HaveLen(4))

					for i, t := range page.Txs {
						Expect(t.Height).To(Equal(prunedTip - i))
						Expect(t.Height).To(BeNumerically(">=", 5))
					}

					_, err = svc.AddressHistory(f.owner.Address(), mined[2].Transactions[0].CalculateHash(), 10)
					Expect(err).To(MatchError(ErrUnknownCursor))
				})

				return
			}

			Describe("on a chain with several blocks", func() {
				var (
					t0, t1, t2, t3, self domain.Transaction
					genesisID            string
					want                 []string
				)

				BeforeEach(func() {
					genesisID = mustAll(repo)[0].Transactions[0].CalculateHash()
					t0, t1 = spend(f.owner, f.ops[0], 9), spend(f.owner, f.ops[1], 9)
					t2, t3 = spend(f.owner, f.ops[2], 9), spend(f.owner, f.ops[3], 9)

					// self spends from and pays to the owner: one history entry.
					self = domain.Transaction{
						Inputs:  []domain.TxIn{{PrevTxID: f.ops[4].TxID, OutIndex: f.ops[4].Index}},
						Outputs: []domain.TxOut{{Amount: 10, Address: f.owner.Address()}},
					}
					self.ID = self.CalculateHash()
					Expect(domain.SignInput(&self, 0, f.owner)).To(Succeed())

					for _, txs := range [][]domain.Transaction{{t0, t1}, {t2}, {self, t3}} {
						_, err := svc.Mine(ctx, txs)
						Expect(err).NotTo(HaveOccurred())
					}

					want = []string{
						t3.CalculateHash(), self.CalculateHash(), t2.CalculateHash(),
						t1.CalculateHash(), t0.CalculateHash(), genesisID,
					}
				})

				It("lists newest first, by height then position, each transaction once, with its block", func() {
					page, err := svc.AddressHistory(f.owner.Address(), "", 100)
					Expect(err).NotTo(HaveOccurred())
					Expect(pageIDs(page)).To(Equal(want))
					Expect(page.More).To(BeFalse())
					Expect(page.PrunedBelow).To(BeZero())

					blocks := mustAll(repo)
					for _, it := range page.Txs {
						b := blocks[it.Height]
						Expect(it.BlockHash).To(Equal(b.Hash))
						Expect(it.Timestamp).To(BeTemporally("==", b.Timestamp))
					}
				})

				It("pages through the same list for every page size", func() {
					for limit := 1; limit <= len(want)+1; limit++ {
						var (
							got   []string
							after string
						)

						for range 2 * len(want) {
							page, err := svc.AddressHistory(f.owner.Address(), after, limit)
							Expect(err).NotTo(HaveOccurred())
							Expect(len(page.Txs)).To(BeNumerically("<=", limit))

							got = append(got, pageIDs(page)...)

							if !page.More {
								break
							}

							Expect(page.Txs).To(HaveLen(limit))

							after = pageIDs(page)[len(page.Txs)-1]
						}

						Expect(got).To(Equal(want), "limit %d", limit)
					}
				})

				It("flags More only while older transactions remain", func() {
					page, err := svc.AddressHistory(f.owner.Address(), "", len(want))
					Expect(err).NotTo(HaveOccurred())
					Expect(page.More).To(BeFalse())
					Expect(page.Txs).To(HaveLen(len(want)))

					page, err = svc.AddressHistory(f.owner.Address(), "", len(want)-1)
					Expect(err).NotTo(HaveOccurred())
					Expect(page.More).To(BeTrue())
					Expect(page.Txs).To(HaveLen(len(want) - 1))

					page, err = svc.AddressHistory(f.owner.Address(), want[len(want)-2], 1)
					Expect(err).NotTo(HaveOccurred())
					Expect(pageIDs(page)).To(Equal(want[len(want)-1:]))
					Expect(page.More).To(BeFalse())
				})

				It("lists an address that only received", func() {
					page, err := svc.AddressHistory(recipient().Address(), "", 100)
					Expect(err).NotTo(HaveOccurred())
					Expect(pageIDs(page)).To(Equal([]string{
						t3.CalculateHash(), t2.CalculateHash(), t1.CalculateHash(), t0.CalculateHash(),
					}))
				})

				It("returns an empty non-nil page for an unknown address", func() {
					page, err := svc.AddressHistory("nobody", "", 25)
					Expect(err).NotTo(HaveOccurred())
					Expect(page.Txs).NotTo(BeNil())
					Expect(page.Txs).To(BeEmpty())
					Expect(page.More).To(BeFalse())
				})

				It("rejects a cursor of another address", func() {
					other := spend(recipient(), outpointOf(t0, 0), 5)
					_, err := svc.Mine(ctx, []domain.Transaction{other})
					Expect(err).NotTo(HaveOccurred())

					_, err = svc.AddressHistory(f.owner.Address(), other.CalculateHash(), 10)
					Expect(err).To(MatchError(ErrUnknownCursor))

					page, err := svc.AddressHistory(f.owner.Address(), "", 10)
					Expect(err).NotTo(HaveOccurred())
					Expect(page.Txs).NotTo(BeEmpty())
				})

				It("rejects a cursor that a reorg removed", func() {
					blocks := mustAll(repo)
					cand := grow(eng, blocks[:2], []domain.Transaction{t2}, []domain.Transaction{t3}, nil, nil)
					_, err := svc.ReplaceChain(ctx, cand)
					Expect(err).NotTo(HaveOccurred())

					_, err = svc.AddressHistory(f.owner.Address(), self.CalculateHash(), 10)
					Expect(err).To(MatchError(ErrUnknownCursor))

					page, err := svc.AddressHistory(f.owner.Address(), t3.CalculateHash(), 10)
					Expect(err).NotTo(HaveOccurred())
					Expect(pageIDs(page)).To(Equal([]string{t2.CalculateHash(), t1.CalculateHash(), t0.CalculateHash(), genesisID}))
				})

				It("keeps a cursor valid while new blocks land", func() {
					first, err := svc.AddressHistory(f.owner.Address(), "", 2)
					Expect(err).NotTo(HaveOccurred())

					fresh := spend(f.owner, f.ops[5], 9)
					_, err = svc.Mine(ctx, []domain.Transaction{fresh})
					Expect(err).NotTo(HaveOccurred())

					rest, err := svc.AddressHistory(f.owner.Address(), pageIDs(first)[1], 100)
					Expect(err).NotTo(HaveOccurred())
					Expect(slices.Concat(pageIDs(first), pageIDs(rest))).To(Equal(want))

					top, err := svc.AddressHistory(f.owner.Address(), "", 1)
					Expect(err).NotTo(HaveOccurred())
					Expect(pageIDs(top)).To(Equal([]string{fresh.CalculateHash()}))
				})

				It("reads again when a block lands during the read", func() {
					fresh := spend(f.owner, f.ops[5], 9)
					spy.onTxs = func() {
						if spy.txs.Load() > 1 {
							return
						}

						_, err := svc.Mine(ctx, []domain.Transaction{fresh})
						Expect(err).NotTo(HaveOccurred())
					}

					page, err := svc.AddressHistory(f.owner.Address(), "", 100)
					Expect(err).NotTo(HaveOccurred())
					Expect(pageIDs(page)[0]).To(Equal(fresh.CalculateHash()))
					Expect(spy.txs.Load()).To(Equal(int32(2)))
				})

				It("gives up when blocks keep landing during the read", func() {
					next := 5
					spy.onTxs = func() {
						_, err := svc.Mine(ctx, []domain.Transaction{spend(f.owner, f.ops[next], 9)})
						Expect(err).NotTo(HaveOccurred())

						next++
					}

					page, err := svc.AddressHistory(f.owner.Address(), "", 10)
					Expect(err).To(MatchError(ErrTipMoving))
					Expect(page.Txs).NotTo(BeNil())
				})

				It("returns the errors of the repository", func() {
					spy.byTxErr = errors.New("boom")
					_, err := svc.AddressHistory(f.owner.Address(), "", 10)
					Expect(err).To(MatchError("boom"))
				})

				It("returns the error of a transaction that cannot be located", func() {
					spy.heightErrAfter = 1
					_, err := svc.AddressHistory(f.owner.Address(), "", 10)
					Expect(err).To(MatchError("height failed"))
				})

				It("keeps ErrNotFound when the block no longer holds the transaction", func() {
					spy.empty = true
					_, err := svc.AddressHistory(f.owner.Address(), "", 10)
					Expect(err).To(MatchError(storage.ErrNotFound))
				})

				It("returns the error of reading the pruned height", func() {
					spy.prunedErr = errors.New("pruned failed")
					_, err := svc.AddressHistory(f.owner.Address(), "", 10)
					Expect(err).To(MatchError("pruned failed"))
				})

				It("returns the error of reading the tip", func() {
					spy.lastErrAfter = 1
					_, err := svc.AddressHistory(f.owner.Address(), "", 10)
					Expect(err).To(MatchError("last failed"))
				})
			})
		})
	}
})
