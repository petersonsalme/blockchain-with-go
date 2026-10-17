package consensus_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/blockchain"
	"github.com/petersonsalme/blockchain-with-go/consensus"
	"github.com/petersonsalme/blockchain-with-go/models"
)

// pinDifficultyEnv sets POW_DIFFICULTY for the current spec (an empty value
// unsets it) and restores the previous value afterwards.
func pinDifficultyEnv(value string) {
	previous, existed := os.LookupEnv(consensus.DifficultyEnvVar)
	DeferCleanup(func() {
		if existed {
			Expect(os.Setenv(consensus.DifficultyEnvVar, previous)).To(Succeed())
			return
		}

		Expect(os.Unsetenv(consensus.DifficultyEnvVar)).To(Succeed())
	})

	if value == "" {
		Expect(os.Unsetenv(consensus.DifficultyEnvVar)).To(Succeed())
		return
	}

	Expect(os.Setenv(consensus.DifficultyEnvVar, value)).To(Succeed())
}

var _ consensus.Consensus = (*consensus.ProofOfWork)(nil)

var _ = Describe("ProofOfWork", func() {
	Describe("Seal", func() {
		var b *models.Block

		BeforeEach(func() {
			b = &models.Block{
				Index:        1,
				Timestamp:    time.Date(2021, 8, 15, 14, 30, 45, 100, time.UTC),
				Transactions: []models.Transaction{{From: "alice", To: "bob", Amount: 10}},
				PrevHash:     "123",
			}
		})

		It("should fill nonce, difficulty and hash", func() {
			Expect(consensus.NewProofOfWork(2).Seal(context.Background(), b)).To(Succeed())

			Expect(b.Difficulty).To(Equal(2))
			Expect(b.Hash).To(HavePrefix("00"))
			Expect(b.Hash).To(Equal(b.CalculateHash()))
		})

		It("should return context.Canceled when the context is canceled", func() {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			err := consensus.NewProofOfWork(12).Seal(ctx, b)
			Expect(errors.Is(err, context.Canceled)).To(BeTrue())
			Expect(b.Hash).To(BeEmpty())
		})

		It("should return context.DeadlineExceeded when the deadline expires", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()

			err := consensus.NewProofOfWork(12).Seal(ctx, b)
			Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())
			Expect(b.Hash).To(BeEmpty())
		})
	})
	Describe("NewProofOfWorkFromEnv", func() {
		It("should use the difficulty from the environment", func() {
			pinDifficultyEnv("3")
			Expect(consensus.NewProofOfWorkFromEnv().Difficulty).To(Equal(3))
		})
	})
	Describe("DifficultyFromEnv", func() {
		When("the environment variable isn't set", func() {
			It("should return the default difficulty", func() {
				pinDifficultyEnv("")
				Expect(consensus.DifficultyFromEnv()).To(Equal(consensus.DefaultDifficulty))
			})
		})

		When("the environment variable is set", func() {
			It("should return the configured difficulty", func() {
				pinDifficultyEnv("3")
				Expect(consensus.DifficultyFromEnv()).To(Equal(3))
			})
		})

		When("the environment variable isn't a valid difficulty", func() {
			It("should return the default difficulty", func() {
				pinDifficultyEnv("abc")
				Expect(consensus.DifficultyFromEnv()).To(Equal(consensus.DefaultDifficulty))

				Expect(os.Setenv(consensus.DifficultyEnvVar, "-1")).To(Succeed())
				Expect(consensus.DifficultyFromEnv()).To(Equal(consensus.DefaultDifficulty))
			})
		})
	})
	Describe("MineWithContext (background context)", func() {
		var b *models.Block

		BeforeEach(func() {
			b = &models.Block{
				Index:     1,
				Timestamp: time.Date(2021, 8, 15, 14, 30, 45, 100, time.UTC),
				Transactions: []models.Transaction{
					{From: "alice", To: "bob", Amount: 10, Signature: "sig1"},
				},
				PrevHash: "123",
			}
		})

		It("should find a hash with the required leading zeros", func() {
			Expect(consensus.MineWithContext(context.Background(), b, 3)).To(Succeed())

			Expect(b.Difficulty).To(Equal(3))
			Expect(b.Hash).To(HavePrefix("000"))
			Expect(b.Hash).To(Equal(b.CalculateHash()))
			Expect(strings.HasPrefix(b.Hash, strings.Repeat("0", b.Difficulty))).To(BeTrue())
		})

		It("should keep the nonce that solved the proof of work", func() {
			Expect(consensus.MineWithContext(context.Background(), b, 2)).To(Succeed())
			solved := b.Hash

			b.Nonce--
			Expect(b.CalculateHash()).NotTo(Equal(solved))
		})

		It("should treat a negative difficulty as zero", func() {
			Expect(consensus.MineWithContext(context.Background(), b, -1)).To(Succeed())

			Expect(b.Difficulty).To(Equal(0))
			Expect(b.Nonce).To(Equal(0))
			Expect(strings.HasPrefix(b.Hash, strings.Repeat("0", b.Difficulty))).To(BeTrue())
		})
	})
	Describe("MineWithContext", func() {
		var b *models.Block

		BeforeEach(func() {
			b = &models.Block{
				Index:     1,
				Timestamp: time.Date(2021, 8, 15, 14, 30, 45, 100, time.UTC),
				Transactions: []models.Transaction{
					{From: "alice", To: "bob", Amount: 10, Signature: "sig1"},
				},
				PrevHash: "123",
			}
		})

		It("should find a hash with the required leading zeros and return nil error", func() {
			err := consensus.MineWithContext(context.Background(), b, 2)

			Expect(err).NotTo(HaveOccurred())
			Expect(b.Difficulty).To(Equal(2))
			Expect(b.Hash).To(HavePrefix("00"))
			Expect(b.Hash).To(Equal(b.CalculateHash()))
			Expect(strings.HasPrefix(b.Hash, strings.Repeat("0", b.Difficulty))).To(BeTrue())
		})

		It("should treat a negative difficulty as zero", func() {
			err := consensus.MineWithContext(context.Background(), b, -1)

			Expect(err).NotTo(HaveOccurred())
			Expect(b.Difficulty).To(Equal(0))
			Expect(b.Nonce).To(Equal(0))
			Expect(strings.HasPrefix(b.Hash, strings.Repeat("0", b.Difficulty))).To(BeTrue())
		})

		It("should handle a nil context gracefully", func() {
			err := consensus.MineWithContext(nil, b, 1)

			Expect(err).NotTo(HaveOccurred())
			Expect(b.Difficulty).To(Equal(1))
			Expect(strings.HasPrefix(b.Hash, strings.Repeat("0", b.Difficulty))).To(BeTrue())
		})

		When("the context is already canceled before mining", func() {
			It("should abort immediately and return context.Canceled", func() {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()

				err := consensus.MineWithContext(ctx, b, 12)

				Expect(err).To(MatchError(context.Canceled))
				Expect(b.Hash).To(BeEmpty())
			})
		})

		When("the context is canceled while mining is in progress", func() {
			It("should abort and return context.Canceled", func() {
				ctx, cancel := context.WithCancel(context.Background())

				go func() {
					time.Sleep(10 * time.Millisecond)
					cancel()
				}()

				// Use high difficulty so mining will not finish before cancel is called
				err := consensus.MineWithContext(ctx, b, 12)

				Expect(err).To(MatchError(context.Canceled))
				Expect(b.Hash).To(BeEmpty())
			})
		})

		When("the context deadline is exceeded (timeout)", func() {
			It("should abort and return context.DeadlineExceeded", func() {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				defer cancel()

				// Use high difficulty so mining will timeout
				err := consensus.MineWithContext(ctx, b, 12)

				Expect(err).To(MatchError(context.DeadlineExceeded))
				Expect(b.Hash).To(BeEmpty())
			})
		})
	})
	Describe("Verify", func() {
		var oldBlock, newBlock *models.Block
		var pow *consensus.ProofOfWork

		BeforeEach(func() {
			pinDifficultyEnv("2")
			pow = consensus.NewProofOfWorkFromEnv()

			oldBlock = &models.Block{
				Index:        0,
				Timestamp:    time.Date(2021, 8, 15, 14, 30, 45, 100, time.UTC),
				Transactions: []models.Transaction{},
				PrevHash:     "123",
				Hash:         "",
			}

			newBlock = blockchain.GenerateBlock(pow, oldBlock, []models.Transaction{
				{From: "alice", To: "bob", Amount: 10, Signature: "sig1"},
				{From: "carol", To: "dave", Amount: 20, Signature: "sig2"},
			})
		})

		When("everything is fine", func() {
			It("should return true", func() {
				Expect(pow.Verify(oldBlock, newBlock)).To(Succeed())
			})
		})

		When("index doesn't match", func() {
			It("should return false", func() {
				newBlock.Index = 50
				Expect(pow.Verify(oldBlock, newBlock)).To(MatchError(consensus.ErrInvalidIndex))
			})
		})

		When("both index and previous hash don't match", func() {
			It("should report the index first", func() {
				newBlock.Index = 50
				newBlock.PrevHash = "abc"
				Expect(pow.Verify(oldBlock, newBlock)).To(MatchError(consensus.ErrInvalidIndex))
			})
		})

		When("previous hash doesn't match", func() {
			It("should return false", func() {
				newBlock.PrevHash = "abc"
				Expect(pow.Verify(oldBlock, newBlock)).To(MatchError(consensus.ErrInvalidPrevHash))
			})
		})

		When("hash doesn't match", func() {
			It("should return false", func() {
				newBlock.Hash = "123"
				Expect(pow.Verify(oldBlock, newBlock)).To(MatchError(consensus.ErrInvalidHash))
			})
		})

		When("the hash doesn't satisfy the proof of work", func() {
			It("should return false", func() {
				Expect(newBlock.Difficulty).To(BeNumerically(">", 0))

				prefix := strings.Repeat("0", newBlock.Difficulty)
				for newBlock.Nonce++; ; newBlock.Nonce++ {
					newBlock.Hash = newBlock.CalculateHash()
					if !strings.HasPrefix(newBlock.Hash, prefix) {
						break
					}
				}

				Expect(newBlock.Hash).To(Equal(newBlock.CalculateHash()))
				Expect(pow.Verify(oldBlock, newBlock)).To(MatchError(consensus.ErrInvalidProofOfWork))
			})
		})

		When("POW_DIFFICULTY is raised after the block was mined", func() {
			It("should still return true", func() {
				Expect(newBlock.Difficulty).To(Equal(2))

				pinDifficultyEnv("4")

				Expect(pow.Verify(oldBlock, newBlock)).To(Succeed())
			})
		})

		When("the block was mined with a difficulty lower than the current one", func() {
			It("should validate the proof of work against the recorded difficulty", func() {
				Expect(consensus.MineWithContext(context.Background(), newBlock, consensus.DifficultyFromEnv()-1)).To(Succeed())

				Expect(newBlock.Difficulty).To(Equal(consensus.DifficultyFromEnv() - 1))
				Expect(pow.Verify(oldBlock, newBlock)).To(Succeed())
			})
		})

		When("the recorded difficulty is tampered with after mining", func() {
			It("should return false", func() {
				newBlock.Difficulty++

				Expect(newBlock.CalculateHash()).NotTo(Equal(newBlock.Hash))
				Expect(pow.Verify(oldBlock, newBlock)).To(HaveOccurred())
			})
		})

		When("transaction amount is altered after mining", func() {
			It("should return false", func() {
				newBlock.Transactions[0].Amount = 999

				Expect(newBlock.CalculateHash()).NotTo(Equal(newBlock.Hash))
				Expect(pow.Verify(oldBlock, newBlock)).To(HaveOccurred())
			})
		})

		When("transaction sender (From) is altered after mining", func() {
			It("should return false", func() {
				newBlock.Transactions[0].From = "mallory"

				Expect(newBlock.CalculateHash()).NotTo(Equal(newBlock.Hash))
				Expect(pow.Verify(oldBlock, newBlock)).To(HaveOccurred())
			})
		})

		When("transaction recipient (To) is altered after mining", func() {
			It("should return false", func() {
				newBlock.Transactions[0].To = "mallory"

				Expect(newBlock.CalculateHash()).NotTo(Equal(newBlock.Hash))
				Expect(pow.Verify(oldBlock, newBlock)).To(HaveOccurred())
			})
		})

		When("transaction fee is altered after mining", func() {
			It("should return false", func() {
				newBlock.Transactions[0].Fee = 99

				Expect(newBlock.CalculateHash()).NotTo(Equal(newBlock.Hash))
				Expect(pow.Verify(oldBlock, newBlock)).To(HaveOccurred())
			})
		})

		When("transaction nonce is altered after mining", func() {
			It("should return false", func() {
				newBlock.Transactions[0].Nonce = 42

				Expect(newBlock.CalculateHash()).NotTo(Equal(newBlock.Hash))
				Expect(pow.Verify(oldBlock, newBlock)).To(HaveOccurred())
			})
		})

		When("transaction timestamp is altered after mining", func() {
			It("should return false", func() {
				newBlock.Transactions[0].Timestamp = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

				Expect(newBlock.CalculateHash()).NotTo(Equal(newBlock.Hash))
				Expect(pow.Verify(oldBlock, newBlock)).To(HaveOccurred())
			})
		})

		When("a transaction is added after mining", func() {
			It("should return false", func() {
				newBlock.Transactions = append(newBlock.Transactions, models.Transaction{
					From:   "eve",
					To:     "frank",
					Amount: 30,
				})

				Expect(newBlock.CalculateHash()).NotTo(Equal(newBlock.Hash))
				Expect(pow.Verify(oldBlock, newBlock)).To(HaveOccurred())
			})
		})

		When("a transaction is removed after mining", func() {
			It("should return false", func() {
				newBlock.Transactions = newBlock.Transactions[:1]

				Expect(newBlock.CalculateHash()).NotTo(Equal(newBlock.Hash))
				Expect(pow.Verify(oldBlock, newBlock)).To(HaveOccurred())
			})
		})

		When("transactions are reordered after mining", func() {
			It("should return false", func() {
				newBlock.Transactions[0], newBlock.Transactions[1] = newBlock.Transactions[1], newBlock.Transactions[0]

				Expect(newBlock.CalculateHash()).NotTo(Equal(newBlock.Hash))
				Expect(pow.Verify(oldBlock, newBlock)).To(HaveOccurred())
			})
		})

		When("a transaction signature is altered after mining", func() {
			It("should return false", func() {
				newBlock.Transactions[0].Signature = "tampered-sig"

				Expect(newBlock.CalculateHash()).NotTo(Equal(newBlock.Hash))
				Expect(pow.Verify(oldBlock, newBlock)).To(HaveOccurred())
			})
		})
	})
})
