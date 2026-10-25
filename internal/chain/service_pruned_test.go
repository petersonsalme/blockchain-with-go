package chain_test

import (
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	. "github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
)

// prunedTip is how many blocks the pruned fixtures mine: with prunedPolicy the
// bodies of heights 1..4 are gone and PrunedBelow is 5.
const prunedTip = 8

var _ = Describe("Service on a pruned repository", func() {
	for _, be := range backends {
		be := be
		if !be.pruned {
			continue
		}
		Describe(be.name, func() {
			var (
				repo storage.IndexedRepository
				svc  *Service
				f    *funding
				eng  = pow.New(1)
				ctx  = context.Background()
				// mined[i] is the block at height i+1.
				mined []*domain.Block
			)

			BeforeEach(func() {
				repo = be.open()
				svc, f = funded(repo, eng)
				mined = nil
				for range prunedTip {
					b, err := svc.Mine(ctx, f.tx())
					Expect(err).NotTo(HaveOccurred())
					mined = append(mined, b)
				}
				below, err := repo.PrunedBelow()
				Expect(err).NotTo(HaveOccurred())
				Expect(below).To(Equal(5))
			})

			Describe("ReplaceChain", func() {
				It("rejects a fork below the pruned history without writing or diverging", func() {
					local := mustAll(repo)
					before := utxoSnapshot(repo)
					var txss [][]domain.Transaction
					for j := 0; j < prunedTip; j++ {
						txss = append(txss, []domain.Transaction{spend(f.owner, f.ops[j], 8)})
					}
					cand := grow(eng, local[:3], txss...)

					_, err := svc.ReplaceChain(ctx, cand)

					Expect(errors.Is(err, storage.ErrReorgTooDeep)).To(BeTrue(), "%v", err)
					Expect(errors.Is(err, ErrStateDiverged)).To(BeFalse())
					Expect(hashes(mustAll(repo))).To(Equal(hashes(local)))
					Expect(utxoSnapshot(repo)).To(Equal(before))
					_, err = svc.Mine(ctx, f.tx())
					Expect(err).NotTo(HaveOccurred())
				})

				It("reorgs a fork at the first retained height", func() {
					local := mustAll(repo)
					var txss [][]domain.Transaction
					for j := 0; j < prunedTip-4+1; j++ {
						txss = append(txss, []domain.Transaction{spend(f.owner, f.ops[4+j], 8)})
					}
					cand := grow(eng, local[:5], txss...)

					reorg, err := svc.ReplaceChain(ctx, cand)

					Expect(err).NotTo(HaveOccurred())
					Expect(reorg.ForkHeight).To(Equal(5))
					Expect(reorg.Disconnected).To(HaveLen(4))
					tip, err := svc.Tip()
					Expect(err).NotTo(HaveOccurred())
					Expect(tip.Hash).To(Equal(cand[len(cand)-1].Hash))
					Expect(utxoSnapshot(repo)).To(Equal(replayRepo(repo)))
				})
			})

			Describe("Balance", func() {
				It("counts outputs created in pruned blocks", func() {
					// The four unspent genesis outputs live in a pruned body.
					sum, utxos := svc.Balance(f.owner.Address())
					Expect(sum).To(Equal(uint64(40)))
					Expect(utxos).To(HaveLen(4))
					for i := range utxos {
						Expect(utxos[i].TxID).To(Equal(f.ops[prunedTip+i].TxID))
						Expect(utxos[i].Index).To(Equal(f.ops[prunedTip+i].Index))
					}
					sum, utxos = svc.Balance(recipient().Address())
					Expect(sum).To(Equal(uint64(9 * prunedTip)))
					Expect(utxos).To(HaveLen(prunedTip))
				})
			})

			Describe("block queries", func() {
				It("answers ErrPruned for pruned heights and hashes", func() {
					blocks := mustAll(repo)
					for h := 1; h < 5; h++ {
						_, err := svc.BlockByHeight(h)
						Expect(err).To(MatchError(storage.ErrPruned))
						_, err = svc.BlockByHash(blocks[h].Hash)
						Expect(err).To(MatchError(storage.ErrPruned))
					}
					b, err := svc.BlockByHeight(5)
					Expect(err).NotTo(HaveOccurred())
					Expect(b.Transactions).NotTo(BeEmpty())
					b, err = svc.BlockByHash(blocks[5].Hash)
					Expect(err).NotTo(HaveOccurred())
					Expect(b.Index).To(Equal(5))
				})

				It("keeps ErrNotFound for a height that never existed", func() {
					_, err := svc.BlockByHeight(prunedTip + 1)
					Expect(err).To(MatchError(storage.ErrNotFound))
					_, err = svc.BlockByHash("nope")
					Expect(err).To(MatchError(storage.ErrNotFound))
				})

				It("hands out headers for the pruned part of Blocks and BlocksRange", func() {
					all, err := svc.Blocks()
					Expect(err).NotTo(HaveOccurred())
					Expect(all).To(HaveLen(prunedTip + 1))
					part, err := svc.BlocksRange(3, 4)
					Expect(err).NotTo(HaveOccurred())
					Expect(part).To(HaveLen(4))
					for _, b := range all[1:5] {
						Expect(b.Transactions).To(BeEmpty())
						Expect(b.Hash).NotTo(BeEmpty())
					}
					Expect(part[0].Transactions).To(BeEmpty())
					Expect(part[2].Transactions).NotTo(BeEmpty())
					tip, err := svc.Tip()
					Expect(err).NotTo(HaveOccurred())
					Expect(tip.Index).To(Equal(prunedTip))
				})
			})

			Describe("Transaction and TxProof", func() {
				It("do not find a transaction of a pruned block", func() {
					ids := []string{f.ops[0].TxID, mined[1].Transactions[0].CalculateHash()}
					for _, id := range ids {
						_, _, _, err := svc.Transaction(id)
						Expect(err).To(MatchError(storage.ErrNotFound))
						_, err = svc.TxProof(id)
						Expect(err).To(MatchError(ErrTxNotFound))
					}
				})

				It("keep serving a transaction of a retained block", func() {
					b, err := svc.BlockByHeight(6)
					Expect(err).NotTo(HaveOccurred())
					id := b.Transactions[0].CalculateHash()
					tx, hash, height, err := svc.Transaction(id)
					Expect(err).NotTo(HaveOccurred())
					Expect(tx.CalculateHash()).To(Equal(id))
					Expect(hash).To(Equal(b.Hash))
					Expect(height).To(Equal(6))
					proof, err := svc.TxProof(id)
					Expect(err).NotTo(HaveOccurred())
					Expect(proof.BlockIndex).To(Equal(6))
				})
			})
		})
	}

	// Mine plus ReplaceChain on a complete and on a pruned service end in the same state.
	for _, be := range backends {
		be := be
		if !be.pruned {
			continue
		}
		It("ends like a complete service after the same Mine and ReplaceChain ("+be.name+")", func() {
			eng := pow.New(1)
			ctx := context.Background()
			fullRepo := backends[0].open()
			full, f := funded(fullRepo, eng)

			prunedRepo := be.open()
			genesis := mustAll(fullRepo)[0]
			delta, err := domain.BlockDelta(prunedRepo, genesis, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(prunedRepo.Commit(genesis, delta)).To(Succeed())
			pruned, err := NewService(prunedRepo, eng)
			Expect(err).NotTo(HaveOccurred())

			for range prunedTip {
				_, err := full.Mine(ctx, f.tx())
				Expect(err).NotTo(HaveOccurred())
			}
			_, err = pruned.ReplaceChain(ctx, mustAll(fullRepo))
			Expect(err).NotTo(HaveOccurred())
			below, err := prunedRepo.PrunedBelow()
			Expect(err).NotTo(HaveOccurred())
			Expect(below).To(Equal(5))

			// A heavier branch from the first retained height, on both.
			local := mustAll(fullRepo)
			var txss [][]domain.Transaction
			for j := 0; j < prunedTip-below+2; j++ {
				txss = append(txss, []domain.Transaction{spend(f.owner, f.ops[below-1+j], 8)})
			}
			alt := grow(eng, local[:below], txss...)
			_, err = full.ReplaceChain(ctx, alt)
			Expect(err).NotTo(HaveOccurred())
			_, err = pruned.ReplaceChain(ctx, alt)
			Expect(err).NotTo(HaveOccurred())

			fullTip, err := full.Tip()
			Expect(err).NotTo(HaveOccurred())
			prunedTipBlock, err := pruned.Tip()
			Expect(err).NotTo(HaveOccurred())
			Expect(prunedTipBlock.Hash).To(Equal(fullTip.Hash))
			Expect(utxoSnapshot(prunedRepo)).To(Equal(utxoSnapshot(fullRepo)))
			for _, addr := range []string{f.owner.Address(), recipient().Address()} {
				wantSum, wantUTXOs := full.Balance(addr)
				gotSum, gotUTXOs := pruned.Balance(addr)
				Expect(gotSum).To(Equal(wantSum))
				Expect(gotUTXOs).To(Equal(wantUTXOs))
			}
		})
	}
})
