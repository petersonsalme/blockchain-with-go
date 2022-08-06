package blockchain_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	. "github.com/petersonsalme/go-blockchain/blockchain"
	"github.com/petersonsalme/go-blockchain/models"
)

var _ = Describe("Blockchain", Ordered, func() {
	Describe("New", func() {
		var b *models.Blockchain

		BeforeAll(func() {
			b = New()
		})

		It("should create a new instance successfully", func() {
			Expect(b).NotTo(BeNil())
		})

		Context("Blocks validation", func() {
			It("should contains only one block", func() {
				Expect(b.Blocks).NotTo(BeNil())
				Expect(b.Blocks).Should(HaveLen(1))
			})

			It("should be the genesis block", func() {
				block := b.Blocks[0]
				Expect(block.Index).To(Equal(0))
				Expect(block.Timestamp).Should(BeTemporally("~", time.Now(), time.Second))
				Expect(block.BPM).To(Equal(0))
				Expect(block.PrevHash).To(BeEmpty())
				Expect(block.Hash).To(BeEmpty())
			})
		})
	})
	Describe("GenerateBlock", func() {
		var oldBlock *models.Block
		var newBlock *models.Block

		BeforeAll(func() {
			oldBlock = &models.Block{
				Index:     0,
				Timestamp: time.Now(),
				BPM:       0,
				PrevHash:  "",
				Hash:      "",
			}
		})

		It("should generate new block successfully", func() {
			newBlock = GenerateBlock(oldBlock, 10)
			Expect(newBlock).NotTo(BeNil())
		})

		It("Should be a valid block", func() {
			Expect(newBlock.Index).To(Equal(oldBlock.Index + 1))
			Expect(newBlock.Timestamp).Should(BeTemporally("~", time.Now(), time.Second))
			Expect(newBlock.BPM).To(Equal(10))
			Expect(newBlock.PrevHash).To(Equal(oldBlock.Hash))
			Expect(newBlock.Hash).NotTo(BeEmpty())
		})
	})
})
