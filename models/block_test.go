package models_test

import (
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
				Index:     0,
				Timestamp: time.Date(2021, 8, 15, 14, 30, 45, 100, time.UTC),
				BPM:       0,
				PrevHash:  "123",
				Hash:      "",
			}
		})

		It("should calculate the block hash successfully", func() {
			Expect(b.CalculateHash()).To(Equal("244e67e7982dc88d8666d87dc7ec260f7e2d6273931b5202197a2990dc0abf83"))
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
				BPM:       10,
				PrevHash:  "123",
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
	Describe("IsValid", func() {
		var oldBlock, newBlock *models.Block

		BeforeEach(func() {
			pinDifficultyEnv("2")

			oldBlock = &models.Block{
				Index:     0,
				Timestamp: time.Date(2021, 8, 15, 14, 30, 45, 100, time.UTC),
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

		When("the difficulty is lower than the required one", func() {
			It("should return false", func() {
				newBlock.Mine(models.Difficulty() - 1)

				Expect(newBlock.HasValidProofOfWork()).To(BeTrue())
				Expect(newBlock.IsValid(oldBlock)).To(BeFalse())
			})
		})
	})
})
