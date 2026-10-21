// Package consensustest holds the acceptance suite every consensus.Consensus
// adapter must pass.
package consensustest

import (
	"context"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// Run registers the shared acceptance specs for the Consensus built by newConsensus.
func Run(newConsensus func() consensus.Consensus) bool {
	return Describe("Consensus acceptance", func() {
		var (
			c    consensus.Consensus
			prev *domain.Block
			b    *domain.Block
		)

		BeforeEach(func() {
			c = newConsensus()

			prev = &domain.Block{
				Index:        0,
				Timestamp:    time.Date(2021, 8, 15, 14, 30, 45, 0, time.UTC),
				Transactions: []domain.Transaction{},
			}
			Expect(c.Seal(context.Background(), nil, prev)).To(Succeed())

			b = &domain.Block{
				Index:        1,
				Timestamp:    time.Date(2021, 8, 15, 14, 31, 45, 0, time.UTC),
				Transactions: []domain.Transaction{{Outputs: []domain.TxOut{{Amount: 1, Address: "b"}}}},
				PrevHash:     prev.Hash,
			}
			Expect(c.Seal(context.Background(), nil, b)).To(Succeed())
		})

		It("accepts a sealed block on a valid predecessor", func() {
			Expect(c.Verify([]*domain.Block{prev}, b)).To(Succeed())
		})

		DescribeTable("rejects a tampered block",
			func(tamper func(b *domain.Block), want error) {
				tamper(b)
				Expect(errors.Is(c.Verify([]*domain.Block{prev}, b), want)).To(BeTrue())
			},
			Entry("Index", func(b *domain.Block) { b.Index = 99 }, consensus.ErrInvalidIndex),
			Entry("PrevHash", func(b *domain.Block) { b.PrevHash = "bad" }, consensus.ErrInvalidPrevHash),
			Entry("Timestamp", func(b *domain.Block) { b.Timestamp = b.Timestamp.Add(time.Second) }, consensus.ErrInvalidHash),
			Entry("Transactions", func(b *domain.Block) { b.Transactions[0].Outputs[0].Amount = 999 }, consensus.ErrInvalidHash),
			Entry("Hash", func(b *domain.Block) { b.Hash = "bad" }, consensus.ErrInvalidHash),
		)

		It("returns the context error when sealing with a canceled context", func() {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			err := c.Seal(ctx, nil, &domain.Block{Index: 2, PrevHash: b.Hash})
			Expect(errors.Is(err, context.Canceled)).To(BeTrue())
		})
	})
}
