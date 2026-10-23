package chain_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	. "github.com/petersonsalme/blockchain-with-go/internal/chain"
	powpkg "github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

var _ = Describe("Blockchain", Ordered, func() {
	pow := powpkg.NewProofOfWork(2)

	Describe("GenerateBlock", func() {
		var oldBlock *domain.Block
		var newBlock *domain.Block
		var sampleTxs []domain.Transaction

		BeforeAll(func() {
			oldBlock = &domain.Block{
				Index:        0,
				Timestamp:    time.Now(),
				Transactions: []domain.Transaction{},
				PrevHash:     "",
				Hash:         strings.Repeat("a", 64),
			}
			sampleTxs = []domain.Transaction{
				{Outputs: []domain.TxOut{{Amount: 10, Address: "bob"}}},
			}
		})

		It("should generate new block successfully", func() {
			newBlock = GenerateBlock(pow, []*domain.Block{oldBlock}, sampleTxs)
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
			Expect(pow.Verify([]*domain.Block{oldBlock}, newBlock)).To(Succeed())
		})

		It("should return nil when generation fails due to size limit", func() {
			GinkgoT().Setenv(domain.MaxBlockBytesEnvVar, "50")
			result := GenerateBlock(pow, []*domain.Block{oldBlock}, sampleTxs)
			Expect(result).To(BeNil())
		})

		It("should return nil when consensus seal fails", func() {
			failingConsensus := &mockConsensus{
				sealFn: func(ctx context.Context, b *domain.Block) error {
					return errors.New("seal error")
				},
			}
			result := GenerateBlock(failingConsensus, []*domain.Block{oldBlock}, sampleTxs)
			Expect(result).To(BeNil())
		})
	})
	Describe("GenerateBlockWithContext", func() {
		var oldBlock *domain.Block
		var sampleTxs []domain.Transaction

		BeforeAll(func() {
			oldBlock = &domain.Block{
				Index:        0,
				Timestamp:    time.Now(),
				Transactions: []domain.Transaction{},
				PrevHash:     "",
				Hash:         strings.Repeat("a", 64),
			}
			sampleTxs = []domain.Transaction{
				{Outputs: []domain.TxOut{{Amount: 10, Address: "bob"}}},
			}
		})

		It("should generate new block successfully with valid context", func() {
			newBlock, err := GenerateBlockWithContext(context.Background(), pow, []*domain.Block{oldBlock}, sampleTxs)
			Expect(err).NotTo(HaveOccurred())
			Expect(newBlock).NotTo(BeNil())
			Expect(newBlock.Index).To(Equal(oldBlock.Index + 1))
			Expect(newBlock.Transactions).To(Equal(sampleTxs))
			Expect(newBlock.PrevHash).To(Equal(oldBlock.Hash))
			Expect(newBlock.Hash).NotTo(BeEmpty())
			Expect(pow.Verify([]*domain.Block{oldBlock}, newBlock)).To(Succeed())
		})

		It("should return an error when context is canceled", func() {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			newBlock, err := GenerateBlockWithContext(ctx, pow, []*domain.Block{oldBlock}, sampleTxs)
			Expect(err).To(MatchError(context.Canceled))
			Expect(newBlock).To(BeNil())
		})

		It("should return an error when consensus seal fails", func() {
			failingConsensus := &mockConsensus{
				sealFn: func(ctx context.Context, b *domain.Block) error {
					return errors.New("seal error")
				},
			}
			newBlock, err := GenerateBlockWithContext(context.Background(), failingConsensus, []*domain.Block{oldBlock}, sampleTxs)
			Expect(err).To(MatchError("seal error"))
			Expect(newBlock).To(BeNil())
		})
	})

	Describe("GenerateBlockWithContext size limit", func() {
		It("returns a ValidationError without mining when the candidate is too large", func() {
			GinkgoT().Setenv(domain.MaxBlockBytesEnvVar, "50")
			slowPoW := powpkg.NewProofOfWork(60)

			parent := genesisBlock()
			done := make(chan struct{})
			var (
				block *domain.Block
				err   error
			)
			go func() {
				defer close(done)
				block, err = GenerateBlockWithContext(context.Background(), slowPoW, []*domain.Block{parent}, []domain.Transaction{{Outputs: []domain.TxOut{{Amount: 1, Address: "b"}}}})
			}()
			Eventually(done, time.Second).Should(BeClosed())

			Expect(block).To(BeNil())
			var ve *domain.ValidationError
			Expect(errors.As(err, &ve)).To(BeTrue())
			Expect(ve.Code).To(Equal(domain.CodeBlockTooLarge))
		})

		It("accounts for the 64-character hash placeholder in size estimation before mining", func() {
			parent := genesisBlock()
			txs := []domain.Transaction{{Outputs: []domain.TxOut{{Amount: 1, Address: "bob"}}}}

			spyConsensus := &mockConsensus{}

			refTimestamp := time.Now().Truncate(time.Second).Add(123456789 * time.Nanosecond)
			fullTimestampLen := timestampJSONLen(refTimestamp)

			tested := false
			for attempt := 0; attempt < 100; attempt++ {
				candidate := domain.Block{
					Index:        parent.Index + 1,
					Timestamp:    time.Now(),
					Transactions: txs,
					MerkleRoot:   domain.MerkleRoot(txs),
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
				GinkgoT().Setenv(domain.MaxBlockBytesEnvVar, strconv.Itoa(exactSize-1))
				miningAttempted := false
				var capturedBlock domain.Block
				spyConsensus.sealFn = func(ctx context.Context, b *domain.Block) error {
					miningAttempted = true
					capturedBlock = *b
					return nil
				}

				block, err := GenerateBlockWithContext(context.Background(), spyConsensus, []*domain.Block{parent}, txs)
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
				var ve *domain.ValidationError
				Expect(errors.As(err, &ve)).To(BeTrue())

				// If estimate size is smaller than exactSize, timestamp had trailing zeroes: retry
				if ve.Details["size"].(int) < exactSize {
					time.Sleep(50 * time.Microsecond)
					continue
				}

				// If estimate size is greater than exactSize, mutant produced > 64 chars: must fail
				Expect(ve.Details["size"].(int)).To(Equal(exactSize))
				Expect(ve.Code).To(Equal(domain.CodeBlockTooLarge))

				// 2) Exact size: must accept candidate and invoke consensus seal
				GinkgoT().Setenv(domain.MaxBlockBytesEnvVar, strconv.Itoa(exactSize))
				miningAttempted = false
				block, err = GenerateBlockWithContext(context.Background(), spyConsensus, []*domain.Block{parent}, txs)
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
	sealFn   func(ctx context.Context, b *domain.Block) error
	verifyFn func(history []*domain.Block, block *domain.Block) error
}

func (m *mockConsensus) Seal(ctx context.Context, history []*domain.Block, b *domain.Block) error {
	if m.sealFn != nil {
		return m.sealFn(ctx, b)
	}
	return nil
}

func (m *mockConsensus) Verify(history []*domain.Block, block *domain.Block) error {
	if m.verifyFn != nil {
		return m.verifyFn(history, block)
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

func genesisBlock() *domain.Block {
	return &domain.Block{Timestamp: time.Now(), Transactions: []domain.Transaction{}}
}
