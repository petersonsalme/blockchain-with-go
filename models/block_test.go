package models_test

import (
	"context"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/blockchain"
	"github.com/petersonsalme/blockchain-with-go/models"
)

// pinDifficultyEnv sets POW_DIFFICULTY for the current spec (an empty value
// unsets it) and restores the previous value afterwards.
func pinDifficultyEnv(value string) {
	previous, existed := os.LookupEnv(models.DifficultyEnvVar)
	DeferCleanup(func() {
		if existed {
			Expect(os.Setenv(models.DifficultyEnvVar, previous)).To(Succeed())
			return
		}

		Expect(os.Unsetenv(models.DifficultyEnvVar)).To(Succeed())
	})

	if value == "" {
		Expect(os.Unsetenv(models.DifficultyEnvVar)).To(Succeed())
		return
	}

	Expect(os.Setenv(models.DifficultyEnvVar, value)).To(Succeed())
}

var _ = Describe("Block", func() {
	Describe("CalculateHash", func() {
		var b *models.Block

		BeforeEach(func() {
			b = &models.Block{
				Index:        0,
				Timestamp:    time.Date(2021, 8, 15, 14, 30, 45, 100, time.UTC),
				Transactions: []models.Transaction{},
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
	Describe("Difficulty", func() {
		When("the environment variable isn't set", func() {
			It("should return the default difficulty", func() {
				pinDifficultyEnv("")
				Expect(models.Difficulty()).To(Equal(models.DefaultDifficulty))
			})
		})

		When("the environment variable is set", func() {
			It("should return the configured difficulty", func() {
				pinDifficultyEnv("3")
				Expect(models.Difficulty()).To(Equal(3))
			})
		})

		When("the environment variable isn't a valid difficulty", func() {
			It("should return the default difficulty", func() {
				pinDifficultyEnv("abc")
				Expect(models.Difficulty()).To(Equal(models.DefaultDifficulty))

				Expect(os.Setenv(models.DifficultyEnvVar, "-1")).To(Succeed())
				Expect(models.Difficulty()).To(Equal(models.DefaultDifficulty))
			})
		})
	})
	Describe("Mine", func() {
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
			b.Mine(3)

			Expect(b.Difficulty).To(Equal(3))
			Expect(b.Hash).To(HavePrefix("000"))
			Expect(b.Hash).To(Equal(b.CalculateHash()))
			Expect(b.HasValidProofOfWork()).To(BeTrue())
		})

		It("should keep the nonce that solved the proof of work", func() {
			b.Mine(2)
			solved := b.Hash

			b.Nonce--
			Expect(b.CalculateHash()).NotTo(Equal(solved))
		})

		It("should treat a negative difficulty as zero", func() {
			b.Mine(-1)

			Expect(b.Difficulty).To(Equal(0))
			Expect(b.Nonce).To(Equal(0))
			Expect(b.HasValidProofOfWork()).To(BeTrue())
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
			err := b.MineWithContext(context.Background(), 2)

			Expect(err).NotTo(HaveOccurred())
			Expect(b.Difficulty).To(Equal(2))
			Expect(b.Hash).To(HavePrefix("00"))
			Expect(b.Hash).To(Equal(b.CalculateHash()))
			Expect(b.HasValidProofOfWork()).To(BeTrue())
		})

		It("should treat a negative difficulty as zero", func() {
			err := b.MineWithContext(context.Background(), -1)

			Expect(err).NotTo(HaveOccurred())
			Expect(b.Difficulty).To(Equal(0))
			Expect(b.Nonce).To(Equal(0))
			Expect(b.HasValidProofOfWork()).To(BeTrue())
		})

		It("should handle a nil context gracefully", func() {
			err := b.MineWithContext(nil, 1)

			Expect(err).NotTo(HaveOccurred())
			Expect(b.Difficulty).To(Equal(1))
			Expect(b.HasValidProofOfWork()).To(BeTrue())
		})

		When("the context is already canceled before mining", func() {
			It("should abort immediately and return context.Canceled", func() {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()

				err := b.MineWithContext(ctx, 12)

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
				err := b.MineWithContext(ctx, 12)

				Expect(err).To(MatchError(context.Canceled))
				Expect(b.Hash).To(BeEmpty())
			})
		})

		When("the context deadline is exceeded (timeout)", func() {
			It("should abort and return context.DeadlineExceeded", func() {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				defer cancel()

				// Use high difficulty so mining will timeout
				err := b.MineWithContext(ctx, 12)

				Expect(err).To(MatchError(context.DeadlineExceeded))
				Expect(b.Hash).To(BeEmpty())
			})
		})
	})
	Describe("IsValid", func() {
		var oldBlock, newBlock *models.Block

		BeforeEach(func() {
			pinDifficultyEnv("2")

			oldBlock = &models.Block{
				Index:        0,
				Timestamp:    time.Date(2021, 8, 15, 14, 30, 45, 100, time.UTC),
				Transactions: []models.Transaction{},
				PrevHash:     "123",
				Hash:         "",
			}

			newBlock = blockchain.GenerateBlock(oldBlock, []models.Transaction{
				{From: "alice", To: "bob", Amount: 10, Signature: "sig1"},
				{From: "carol", To: "dave", Amount: 20, Signature: "sig2"},
			})
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
				Expect(newBlock.HasValidProofOfWork()).To(BeFalse())
				Expect(newBlock.IsValid(oldBlock)).To(BeFalse())
			})
		})

		When("POW_DIFFICULTY is raised after the block was mined", func() {
			It("should still return true", func() {
				Expect(newBlock.Difficulty).To(Equal(2))

				pinDifficultyEnv("4")

				Expect(newBlock.HasValidProofOfWork()).To(BeTrue())
				Expect(newBlock.IsValid(oldBlock)).To(BeTrue())
			})
		})

		When("the block was mined with a difficulty lower than the current one", func() {
			It("should validate the proof of work against the recorded difficulty", func() {
				newBlock.Mine(models.Difficulty() - 1)

				Expect(newBlock.Difficulty).To(Equal(models.Difficulty() - 1))
				Expect(newBlock.HasValidProofOfWork()).To(BeTrue())
				Expect(newBlock.IsValid(oldBlock)).To(BeTrue())
			})
		})

		When("the recorded difficulty is tampered with after mining", func() {
			It("should return false", func() {
				newBlock.Difficulty++

				Expect(newBlock.CalculateHash()).NotTo(Equal(newBlock.Hash))
				Expect(newBlock.IsValid(oldBlock)).To(BeFalse())
			})
		})

		When("transaction amount is altered after mining", func() {
			It("should return false", func() {
				newBlock.Transactions[0].Amount = 999

				Expect(newBlock.CalculateHash()).NotTo(Equal(newBlock.Hash))
				Expect(newBlock.IsValid(oldBlock)).To(BeFalse())
			})
		})

		When("transaction sender (From) is altered after mining", func() {
			It("should return false", func() {
				newBlock.Transactions[0].From = "mallory"

				Expect(newBlock.CalculateHash()).NotTo(Equal(newBlock.Hash))
				Expect(newBlock.IsValid(oldBlock)).To(BeFalse())
			})
		})

		When("transaction recipient (To) is altered after mining", func() {
			It("should return false", func() {
				newBlock.Transactions[0].To = "mallory"

				Expect(newBlock.CalculateHash()).NotTo(Equal(newBlock.Hash))
				Expect(newBlock.IsValid(oldBlock)).To(BeFalse())
			})
		})

		When("transaction fee is altered after mining", func() {
			It("should return false", func() {
				newBlock.Transactions[0].Fee = 99

				Expect(newBlock.CalculateHash()).NotTo(Equal(newBlock.Hash))
				Expect(newBlock.IsValid(oldBlock)).To(BeFalse())
			})
		})

		When("transaction nonce is altered after mining", func() {
			It("should return false", func() {
				newBlock.Transactions[0].Nonce = 42

				Expect(newBlock.CalculateHash()).NotTo(Equal(newBlock.Hash))
				Expect(newBlock.IsValid(oldBlock)).To(BeFalse())
			})
		})

		When("transaction timestamp is altered after mining", func() {
			It("should return false", func() {
				newBlock.Transactions[0].Timestamp = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

				Expect(newBlock.CalculateHash()).NotTo(Equal(newBlock.Hash))
				Expect(newBlock.IsValid(oldBlock)).To(BeFalse())
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
				Expect(newBlock.IsValid(oldBlock)).To(BeFalse())
			})
		})

		When("a transaction is removed after mining", func() {
			It("should return false", func() {
				newBlock.Transactions = newBlock.Transactions[:1]

				Expect(newBlock.CalculateHash()).NotTo(Equal(newBlock.Hash))
				Expect(newBlock.IsValid(oldBlock)).To(BeFalse())
			})
		})

		When("transactions are reordered after mining", func() {
			It("should return false", func() {
				newBlock.Transactions[0], newBlock.Transactions[1] = newBlock.Transactions[1], newBlock.Transactions[0]

				Expect(newBlock.CalculateHash()).NotTo(Equal(newBlock.Hash))
				Expect(newBlock.IsValid(oldBlock)).To(BeFalse())
			})
		})

		When("a transaction signature is altered after mining", func() {
			It("should return false", func() {
				newBlock.Transactions[0].Signature = "tampered-sig"

				Expect(newBlock.CalculateHash()).NotTo(Equal(newBlock.Hash))
				Expect(newBlock.IsValid(oldBlock)).To(BeFalse())
			})
		})
	})
})
