package blockchain_test

import (
	"context"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	. "github.com/petersonsalme/blockchain-with-go/blockchain"
	"github.com/petersonsalme/blockchain-with-go/models"
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
			newBlock = GenerateBlock(oldBlock, sampleTxs)
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
			Expect(newBlock.Difficulty).To(Equal(models.Difficulty()))
			Expect(newBlock.Hash).To(Equal(newBlock.CalculateHash()))
			Expect(newBlock.HasValidProofOfWork()).To(BeTrue())
			Expect(newBlock.IsValid(oldBlock)).To(BeTrue())
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
			newBlock, err := GenerateBlockWithContext(context.Background(), oldBlock, sampleTxs)
			Expect(err).NotTo(HaveOccurred())
			Expect(newBlock).NotTo(BeNil())
			Expect(newBlock.Index).To(Equal(oldBlock.Index + 1))
			Expect(newBlock.Transactions).To(Equal(sampleTxs))
			Expect(newBlock.PrevHash).To(Equal(oldBlock.Hash))
			Expect(newBlock.Hash).NotTo(BeEmpty())
			Expect(newBlock.IsValid(oldBlock)).To(BeTrue())
		})

		It("should return an error when context is canceled", func() {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			newBlock, err := GenerateBlockWithContext(ctx, oldBlock, sampleTxs)
			Expect(err).To(MatchError(context.Canceled))
			Expect(newBlock).To(BeNil())
		})
	})

	Describe("GenerateBlockWithContext size limit", func() {
		It("returns a ValidationError without mining when the candidate is too large", func() {
			GinkgoT().Setenv(models.MaxBlockBytesEnvVar, "50")
			GinkgoT().Setenv(models.DifficultyEnvVar, "8")

			parent := New().LastBlock()
			done := make(chan struct{})
			var (
				block *models.Block
				err   error
			)
			go func() {
				defer close(done)
				block, err = GenerateBlockWithContext(context.Background(), parent, []models.Transaction{{From: "a", To: "b", Amount: 1}})
			}()
			Eventually(done, time.Second).Should(BeClosed())

			Expect(block).To(BeNil())
			var ve *models.ValidationError
			Expect(errors.As(err, &ve)).To(BeTrue())
			Expect(ve.Code).To(Equal(models.CodeBlockTooLarge))
		})
	})
})
