package blockchain_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
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
				Hash:         strings.Repeat("a", 64),
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

		It("should return nil when generation fails due to size limit", func() {
			GinkgoT().Setenv(models.MaxBlockBytesEnvVar, "50")
			result := GenerateBlock(pow, oldBlock, sampleTxs)
			Expect(result).To(BeNil())
		})

		It("should return nil when consensus seal fails", func() {
			failingConsensus := &mockConsensus{
				sealFn: func(ctx context.Context, b *models.Block) error {
					return errors.New("seal error")
				},
			}
			result := GenerateBlock(failingConsensus, oldBlock, sampleTxs)
			Expect(result).To(BeNil())
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
				Hash:         strings.Repeat("a", 64),
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

		It("should return an error when consensus seal fails", func() {
			failingConsensus := &mockConsensus{
				sealFn: func(ctx context.Context, b *models.Block) error {
					return errors.New("seal error")
				},
			}
			newBlock, err := GenerateBlockWithContext(context.Background(), failingConsensus, oldBlock, sampleTxs)
			Expect(err).To(MatchError("seal error"))
			Expect(newBlock).To(BeNil())
		})
	})

	Describe("GenerateBlockWithContext size limit", func() {
		It("returns a ValidationError without mining when the candidate is too large", func() {
			GinkgoT().Setenv(models.MaxBlockBytesEnvVar, "50")
			slowPoW := consensus.NewProofOfWork(8)

			parent := New().LastBlock()
			done := make(chan struct{})
			var (
				block *models.Block
				err   error
			)
			go func() {
				defer close(done)
				block, err = GenerateBlockWithContext(context.Background(), slowPoW, parent, []models.Transaction{{From: "a", To: "b", Amount: 1}})
			}()
			Eventually(done, time.Second).Should(BeClosed())

			Expect(block).To(BeNil())
			var ve *models.ValidationError
			Expect(errors.As(err, &ve)).To(BeTrue())
			Expect(ve.Code).To(Equal(models.CodeBlockTooLarge))
		})

		It("accounts for the 64-character hash placeholder in size estimation before mining", func() {
			parent := New().LastBlock()
			txs := []models.Transaction{{From: "alice", To: "bob", Amount: 1}}

			spyConsensus := &mockConsensus{}

			refTimestamp := time.Now().Truncate(time.Second).Add(123456789 * time.Nanosecond)
			fullTimestampLen := timestampJSONLen(refTimestamp)

			tested := false
			for attempt := 0; attempt < 100; attempt++ {
				candidate := models.Block{
					Index:        parent.Index + 1,
					Timestamp:    time.Now(),
					Transactions: txs,
					PrevHash:     parent.Hash,
					Hash:         strings.Repeat("0", 64),
				}
				if timestampJSONLen(candidate.Timestamp) != fullTimestampLen {
					time.Sleep(50 * time.Microsecond)
					continue
				}
				exactSize, err := candidate.Size()
				Expect(err).NotTo(HaveOccurred())

				// 1) Exactly 1 byte below required size: must reject candidate without mining
				GinkgoT().Setenv(models.MaxBlockBytesEnvVar, strconv.Itoa(exactSize-1))
				miningAttempted := false
				var capturedBlock models.Block
				spyConsensus.sealFn = func(ctx context.Context, b *models.Block) error {
					miningAttempted = true
					capturedBlock = *b
					return nil
				}

				block, err := GenerateBlockWithContext(context.Background(), spyConsensus, parent, txs)
				if miningAttempted {
					// Check if timestamp in GenerateBlockWithContext had trailing zero (shorter JSON length)
					if timestampJSONLen(capturedBlock.Timestamp) < fullTimestampLen {
						time.Sleep(50 * time.Microsecond)
						continue
					}
					// Under mutant producing fewer than 64 chars (e.g. Repeat("0", 63)), mining is incorrectly attempted
					Expect(miningAttempted).To(BeFalse())
				}

				Expect(block).To(BeNil())
				var ve *models.ValidationError
				Expect(errors.As(err, &ve)).To(BeTrue())

				// If estimate size is smaller than exactSize, timestamp had trailing zeroes: retry
				if ve.Details["size"].(int) < exactSize {
					time.Sleep(50 * time.Microsecond)
					continue
				}

				// If estimate size is greater than exactSize, mutant produced > 64 chars: must fail
				Expect(ve.Details["size"].(int)).To(Equal(exactSize))
				Expect(ve.Code).To(Equal(models.CodeBlockTooLarge))

				// 2) Exact size: must accept candidate and invoke consensus seal
				GinkgoT().Setenv(models.MaxBlockBytesEnvVar, strconv.Itoa(exactSize))
				miningAttempted = false
				block, err = GenerateBlockWithContext(context.Background(), spyConsensus, parent, txs)
				Expect(err).NotTo(HaveOccurred())
				Expect(block).NotTo(BeNil())
				Expect(miningAttempted).To(BeTrue())
				tested = true
				break
			}
			Expect(tested).To(BeTrue())
		})
	})
})

type mockConsensus struct {
	sealFn   func(ctx context.Context, b *models.Block) error
	verifyFn func(parent, block *models.Block) error
}

func (m *mockConsensus) Seal(ctx context.Context, b *models.Block) error {
	if m.sealFn != nil {
		return m.sealFn(ctx, b)
	}
	return nil
}

func (m *mockConsensus) Verify(parent, block *models.Block) error {
	if m.verifyFn != nil {
		return m.verifyFn(parent, block)
	}
	return nil
}

func timestampJSONLen(t time.Time) int {
	b, err := t.MarshalJSON()
	if err != nil {
		return 0
	}
	return len(b)
}
