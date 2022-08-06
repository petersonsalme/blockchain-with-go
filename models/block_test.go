package models_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/go-blockchain/blockchain"
	"github.com/petersonsalme/go-blockchain/models"
)

var _ = Describe("Block", func() {
	Describe("CalculateHash", func() {
		var b *models.Block

		BeforeEach(func() {
			b = &models.Block{
				Index:     0,
				Timestamp: time.Date(2021, 8, 15, 14, 30, 45, 100, time.Local),
				BPM:       0,
				PrevHash:  "123",
				Hash:      "",
			}
		})

		It("should calculate the block hash successfully", func() {
			Expect(b.CalculateHash()).To(Equal("b2f69408a72ef7c200b7485d9534c54a7463ac31d83f0f803e4723f787a85c8c"))
		})
	})
	Describe("IsValid", func() {
		var oldBlock, newBlock *models.Block

		BeforeEach(func() {
			oldBlock = &models.Block{
				Index:     0,
				Timestamp: time.Date(2021, 8, 15, 14, 30, 45, 100, time.Local),
				BPM:       0,
				PrevHash:  "123",
				Hash:      "",
			}

			newBlock = blockchain.GenerateBlock(oldBlock, 10)
		})

		When("everything is fine", func() {
			It("should return true", func() {
				Expect(newBlock.IsValid(oldBlock)).To(BeTrue())
			})
		})

		When("index doesn't match", func() {
			It("should return false", func() {
				newBlock.Index = 50
				Expect(newBlock.IsValid(oldBlock)).To(BeFalse())
			})
		})

		When("previous hash doesn't match", func() {
			It("should return false", func() {
				newBlock.PrevHash = "abc"
				Expect(newBlock.IsValid(oldBlock)).To(BeFalse())
			})
		})

		When("hash doesn't match", func() {
			It("should return false", func() {
				newBlock.Hash = "123"
				Expect(newBlock.IsValid(oldBlock)).To(BeFalse())
			})
		})
	})
})
