package blockchain_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	. "github.com/petersonsalme/blockchain-with-go/blockchain"
	"github.com/petersonsalme/blockchain-with-go/consensus"
	"github.com/petersonsalme/blockchain-with-go/models"
)

var _ = Describe("Blockchain", Ordered, func() {
	pow := consensus.NewProofOfWork(2)

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
				Expect(block.Transactions).To(BeEmpty())
				Expect(block.PrevHash).To(BeEmpty())
				Expect(block.Hash).To(BeEmpty())
			})
		})
	})
	Describe("GenerateBlock", func() {
		var oldBlock *models.Block
		var newBlock *models.Block
		var sampleTxs []models.Transaction

		BeforeAll(func() {
			oldBlock = &models.Block{
				Index:        0,
				Timestamp:    time.Now(),
				Transactions: []models.Transaction{},
				PrevHash:     "",
				Hash:         "",
			}
			sampleTxs = []models.Transaction{
				{From: "alice", To: "bob", Amount: 10, Fee: 1, Nonce: 0},
			}
		})

		It("should generate new block successfully", func() {
			newBlock = GenerateBlock(pow, oldBlock, sampleTxs)
			Expect(newBlock).NotTo(BeNil())
		})

		It("Should be a valid block", func() {
			Expect(newBlock.Index).To(Equal(oldBlock.Index + 1))
			Expect(newBlock.Timestamp).Should(BeTemporally("~", time.Now(), time.Second))
			Expect(newBlock.Transactions).To(Equal(sampleTxs))
			Expect(newBlock.PrevHash).To(Equal(oldBlock.Hash))
			Expect(newBlock.Hash).NotTo(BeEmpty())
		})

		It("Should be mined with the configured difficulty", func() {
			Expect(newBlock.Difficulty).To(Equal(pow.Difficulty))
			Expect(newBlock.Hash).To(Equal(newBlock.CalculateHash()))
			Expect(pow.Verify(oldBlock, newBlock)).To(Succeed())
		})
	})
	Describe("GenerateBlockWithContext", func() {
		var oldBlock *models.Block
		var sampleTxs []models.Transaction

		BeforeAll(func() {
			oldBlock = &models.Block{
				Index:        0,
				Timestamp:    time.Now(),
				Transactions: []models.Transaction{},
				PrevHash:     "",
				Hash:         "",
			}
			sampleTxs = []models.Transaction{
				{From: "alice", To: "bob", Amount: 10, Fee: 1, Nonce: 0},
			}
		})

		It("should generate new block successfully with valid context", func() {
			newBlock, err := GenerateBlockWithContext(context.Background(), pow, oldBlock, sampleTxs)
			Expect(err).NotTo(HaveOccurred())
			Expect(newBlock).NotTo(BeNil())
			Expect(newBlock.Index).To(Equal(oldBlock.Index + 1))
			Expect(newBlock.Transactions).To(Equal(sampleTxs))
			Expect(newBlock.PrevHash).To(Equal(oldBlock.Hash))
			Expect(newBlock.Hash).NotTo(BeEmpty())
			Expect(pow.Verify(oldBlock, newBlock)).To(Succeed())
		})

		It("should return an error when context is canceled", func() {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			newBlock, err := GenerateBlockWithContext(ctx, pow, oldBlock, sampleTxs)
			Expect(err).To(MatchError(context.Canceled))
			Expect(newBlock).To(BeNil())
		})
	})
})
