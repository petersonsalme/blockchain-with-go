package domain_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

var _ = Describe("Block", func() {
	Describe("CalculateHash", func() {
		var b *domain.Block

		BeforeEach(func() {
			b = &domain.Block{
				Index:        0,
				Timestamp:    time.Date(2021, 8, 15, 14, 30, 45, 100, time.UTC),
				Transactions: []domain.Transaction{},
				PrevHash:     "123",
				Hash:         "",
			}
		})

		It("should calculate the block hash successfully", func() {
			Expect(b.CalculateHash()).To(Equal("88920d5c885bdbb82f32f116a8c34b320eabd362041084756e76e65639d5972c"))
		})

		It("should change the hash when the nonce changes", func() {
			original := b.CalculateHash()
			b.Nonce = 1
			Expect(b.CalculateHash()).NotTo(Equal(original))
		})

		It("should change the hash when the difficulty changes", func() {
			original := b.CalculateHash()
			b.Difficulty = 1
			Expect(b.CalculateHash()).NotTo(Equal(original))
		})
	})
})
