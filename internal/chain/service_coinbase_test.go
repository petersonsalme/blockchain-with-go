package chain_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	. "github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
)

// coinbaseTx is an unsigned, input-less transaction paying amount to the recipient.
func coinbaseTx(amount uint64) domain.Transaction {
	tx := domain.Transaction{
		Outputs:   []domain.TxOut{{Amount: amount, Address: recipient().Address()}},
		Timestamp: time.Now(),
	}
	tx.ID = tx.CalculateHash()

	return tx
}

var _ = Describe("Service coinbase rule", func() {
	for _, be := range backends {
		Describe(be.name, func() {
			var (
				repo storage.IndexedRepository
				svc  *Service
				f    *funding
			)

			BeforeEach(func() {
				newRepo = be.open
				repo = newRepo()
				svc, f = funded(repo, engineCases[0].new())
			})

			It("Submit rejects an input-less transaction and the pool stays empty", func() {
				Expect(svc.Submit(coinbaseTx(1))).To(MatchError(domain.ErrMissingInputs))
				Expect(svc.Pending()).To(BeEmpty())
			})

			DescribeTable("Mine rejects any input-less transaction, wherever it sits, before sealing",
				func(position int) {
					before, _ := repo.All()
					beforeUTXO := utxoSnapshot(repo)

					txs := append(f.tx(), f.tx()...)
					txs = append(txs[:position], append([]domain.Transaction{coinbaseTx(1)}, txs[position:]...)...)

					_, err := svc.Mine(context.Background(), txs)

					Expect(err).To(MatchError(domain.ErrMissingInputs))

					after, _ := repo.All()
					Expect(hashes(after)).To(Equal(hashes(before)))
					Expect(utxoSnapshot(repo)).To(Equal(beforeUTXO))
				},
				Entry("at position 0", 0),
				Entry("at position 1", 1),
				Entry("last", 2),
			)

			It("Mine rejects a lone input-less transaction", func() {
				_, err := svc.Mine(context.Background(), []domain.Transaction{coinbaseTx(1)})
				Expect(err).To(MatchError(domain.ErrMissingInputs))
			})

			It("mineLocked validates the block with the Index it will get", func() {
				// Sealing succeeds, so a rejection can only come from BlockDelta on the real Index:
				// a spend of a genesis output is valid at any Index, hence the block is accepted at 1.
				b, err := svc.Mine(context.Background(), f.tx())
				Expect(err).NotTo(HaveOccurred())
				Expect(b.Index).To(Equal(1))
			})

			Describe("ReplaceChain", func() {
				var base []*domain.Block

				BeforeEach(func() {
					base, _ = repo.All()
					_, err := svc.Mine(context.Background(), f.tx())
					Expect(err).NotTo(HaveOccurred())
				})

				It("rejects a candidate whose coinbase exceeds the subsidy and changes nothing", func() {
					beforeBlocks, _ := repo.All()
					beforeUTXO := utxoSnapshot(repo)
					eng := engineCases[0].new()
					cand := grow(eng, base,
						[]domain.Transaction{coinbaseTx(domain.InitialSubsidy + 2), spend(f.owner, f.ops[5], 9)},
						f.tx(), f.tx())

					_, err := svc.ReplaceChain(context.Background(), cand)

					Expect(err).To(MatchError(domain.ErrCoinbaseTooLarge))

					after, _ := repo.All()
					Expect(hashes(after)).To(Equal(hashes(beforeBlocks)))
					Expect(utxoSnapshot(repo)).To(Equal(beforeUTXO))
				})

				It("accepts a candidate with a valid coinbase and shows its output in Balance", func() {
					eng := engineCases[0].new()
					cb := coinbaseTx(domain.InitialSubsidy)
					cand := grow(eng, base,
						[]domain.Transaction{cb},
						f.tx(), f.tx())

					_, err := svc.ReplaceChain(context.Background(), cand)

					Expect(err).NotTo(HaveOccurred())

					balance, _ := svc.Balance(recipient().Address())
					Expect(balance).To(BeNumerically(">=", domain.InitialSubsidy))
					Expect(utxoSnapshot(repo)).To(Equal(replay(cand)))
				})
			})
		})
	}
})
