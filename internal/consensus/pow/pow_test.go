package pow_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	powpkg "github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// pinDifficultyEnv sets POW_DIFFICULTY for the current spec (an empty value
// unsets it) and restores the previous value afterwards.
func pinDifficultyEnv(value string) {
	previous, existed := os.LookupEnv(powpkg.DifficultyEnvVar)
	DeferCleanup(func() {
		if existed {
			Expect(os.Setenv(powpkg.DifficultyEnvVar, previous)).To(Succeed())
			return
		}

		Expect(os.Unsetenv(powpkg.DifficultyEnvVar)).To(Succeed())
	})

	if value == "" {
		Expect(os.Unsetenv(powpkg.DifficultyEnvVar)).To(Succeed())
		return
	}

	Expect(os.Setenv(powpkg.DifficultyEnvVar, value)).To(Succeed())
}

// mineAsync runs mine in a goroutine and fails the spec if it does not return
// within a second, so a broken cancellation fails fast instead of hanging.
func mineAsync(mine func() error) error {
	done := make(chan error, 1)
	go func() { done <- mine() }()

	var err error
	Eventually(done, time.Second).Should(Receive(&err))
	return err
}

var _ consensus.Consensus = (*powpkg.ProofOfWork)(nil)

var _ = Describe("ProofOfWork", func() {
	Describe("Seal", func() {
		var b *domain.Block

		BeforeEach(func() {
			b = &domain.Block{
				Index:        1,
				Timestamp:    time.Date(2021, 8, 15, 14, 30, 45, 100, time.UTC),
				Transactions: []domain.Transaction{{From: "alice", To: "bob", Amount: 10}},
				PrevHash:     "123",
			}
		})

		It("should fill nonce, difficulty and hash", func() {
			Expect(powpkg.NewProofOfWork(2).Seal(context.Background(), b)).To(Succeed())

			Expect(b.Difficulty).To(Equal(2))
			Expect(b.Hash).To(HavePrefix("00"))
			Expect(b.Hash).To(Equal(b.CalculateHash()))
		})

		It("should return context.Canceled when the context is canceled", func() {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			err := powpkg.NewProofOfWork(0).Seal(ctx, b)
			Expect(errors.Is(err, context.Canceled)).To(BeTrue())
			Expect(b.Hash).To(BeEmpty())
		})

		It("should return context.DeadlineExceeded when the deadline expires", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()

			err := mineAsync(func() error { return powpkg.NewProofOfWork(12).Seal(ctx, b) })
			Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())
			Expect(b.Hash).To(BeEmpty())
		})
	})
	Describe("NewProofOfWorkFromEnv", func() {
		It("should use the difficulty from the environment", func() {
			pinDifficultyEnv("3")
			Expect(powpkg.NewProofOfWorkFromEnv().Difficulty).To(Equal(3))
		})
	})
	Describe("DifficultyFromEnv", func() {
		When("the environment variable isn't set", func() {
			It("should return the default difficulty", func() {
				pinDifficultyEnv("")
				Expect(powpkg.DifficultyFromEnv()).To(Equal(powpkg.DefaultDifficulty))
			})
		})

		When("the environment variable is set", func() {
			It("should return the configured difficulty", func() {
				pinDifficultyEnv("3")
				Expect(powpkg.DifficultyFromEnv()).To(Equal(3))
			})
		})

		When("the environment variable is zero", func() {
			It("should return zero instead of the default difficulty", func() {
				pinDifficultyEnv("0")
				Expect(powpkg.DifficultyFromEnv()).To(Equal(0))
			})
		})

		When("the environment variable isn't a valid difficulty", func() {
			It("should return the default difficulty", func() {
				pinDifficultyEnv("abc")
				Expect(powpkg.DifficultyFromEnv()).To(Equal(powpkg.DefaultDifficulty))

				Expect(os.Setenv(powpkg.DifficultyEnvVar, "-1")).To(Succeed())
				Expect(powpkg.DifficultyFromEnv()).To(Equal(powpkg.DefaultDifficulty))
			})
		})
	})
	Describe("MineWithContext (background context)", func() {
		var b *domain.Block

		BeforeEach(func() {
			b = &domain.Block{
				Index:     1,
				Timestamp: time.Date(2021, 8, 15, 14, 30, 45, 100, time.UTC),
				Transactions: []domain.Transaction{
					{From: "alice", To: "bob", Amount: 10, Signature: "sig1"},
				},
				PrevHash: "123",
			}
		})

		It("should find a hash with the required leading zeros", func() {
			Expect(powpkg.MineWithContext(context.Background(), b, 3)).To(Succeed())

			Expect(b.Difficulty).To(Equal(3))
			Expect(b.Hash).To(HavePrefix("000"))
			Expect(b.Hash).To(Equal(b.CalculateHash()))
			Expect(strings.HasPrefix(b.Hash, strings.Repeat("0", b.Difficulty))).To(BeTrue())
		})

		It("should keep the nonce that solved the proof of work", func() {
			Expect(powpkg.MineWithContext(context.Background(), b, 2)).To(Succeed())
			solved := b.Hash

			b.Nonce--
			Expect(b.CalculateHash()).NotTo(Equal(solved))
		})

		It("should treat a negative difficulty as zero", func() {
			Expect(powpkg.MineWithContext(context.Background(), b, -1)).To(Succeed())

			Expect(b.Difficulty).To(Equal(0))
			Expect(b.Nonce).To(Equal(0))
			Expect(strings.HasPrefix(b.Hash, strings.Repeat("0", b.Difficulty))).To(BeTrue())
		})
	})
	Describe("MineWithContext", func() {
		var b *domain.Block

		BeforeEach(func() {
			b = &domain.Block{
				Index:     1,
				Timestamp: time.Date(2021, 8, 15, 14, 30, 45, 100, time.UTC),
				Transactions: []domain.Transaction{
					{From: "alice", To: "bob", Amount: 10, Signature: "sig1"},
				},
				PrevHash: "123",
			}
		})

		It("should find a hash with the required leading zeros and return nil error", func() {
			err := powpkg.MineWithContext(context.Background(), b, 2)

			Expect(err).NotTo(HaveOccurred())
			Expect(b.Difficulty).To(Equal(2))
			Expect(b.Hash).To(HavePrefix("00"))
			Expect(b.Hash).To(Equal(b.CalculateHash()))
			Expect(strings.HasPrefix(b.Hash, strings.Repeat("0", b.Difficulty))).To(BeTrue())
		})

		It("should treat a negative difficulty as zero", func() {
			err := powpkg.MineWithContext(context.Background(), b, -1)

			Expect(err).NotTo(HaveOccurred())
			Expect(b.Difficulty).To(Equal(0))
			Expect(b.Nonce).To(Equal(0))
			Expect(strings.HasPrefix(b.Hash, strings.Repeat("0", b.Difficulty))).To(BeTrue())
		})

		It("should return the smallest non-negative nonce that solves the difficulty", func() {
			Expect(powpkg.MineWithContext(context.Background(), b, 2)).To(Succeed())

			Expect(b.Nonce).To(BeNumerically(">", 0))

			solved := b.Nonce
			for n := 0; n < solved; n++ {
				b.Nonce = n
				Expect(b.CalculateHash()).NotTo(HavePrefix("00"), "nonce %d already solves the difficulty", n)
			}
		})

		It("should handle a nil context gracefully", func() {
			err := powpkg.MineWithContext(nil, b, 1)

			Expect(err).NotTo(HaveOccurred())
			Expect(b.Difficulty).To(Equal(1))
			Expect(strings.HasPrefix(b.Hash, strings.Repeat("0", b.Difficulty))).To(BeTrue())
		})

		When("the context is already canceled before mining", func() {
			It("should abort immediately and return context.Canceled", func() {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()

				err := powpkg.MineWithContext(ctx, b, 0)

				Expect(err).To(MatchError(context.Canceled))
				Expect(b.Hash).To(BeEmpty())
			})
		})

		When("the context is already canceled and the difficulty is zero", func() {
			It("should return context.Canceled without filling the hash", func() {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()

				err := powpkg.MineWithContext(ctx, b, 0)

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
				err := mineAsync(func() error { return powpkg.MineWithContext(ctx, b, 12) })

				Expect(err).To(MatchError(context.Canceled))
				Expect(b.Hash).To(BeEmpty())
			})
		})

		When("the context deadline is exceeded (timeout)", func() {
			It("should abort and return context.DeadlineExceeded", func() {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				defer cancel()

				// Use high difficulty so mining will timeout
				err := mineAsync(func() error { return powpkg.MineWithContext(ctx, b, 12) })

				Expect(err).To(MatchError(context.DeadlineExceeded))
				Expect(b.Hash).To(BeEmpty())
			})
		})
	})
	Describe("Verify", func() {
		var oldBlock, newBlock *domain.Block
		var pow *powpkg.ProofOfWork

		BeforeEach(func() {
			pinDifficultyEnv("2")
			pow = powpkg.NewProofOfWorkFromEnv()

			oldBlock = &domain.Block{
				Index:        0,
				Timestamp:    time.Date(2021, 8, 15, 14, 30, 45, 100, time.UTC),
				Transactions: []domain.Transaction{},
				PrevHash:     "123",
				Hash:         "",
			}

			newBlock = chain.GenerateBlock(pow, oldBlock, []domain.Transaction{
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
				Expect(powpkg.MineWithContext(context.Background(), newBlock, powpkg.DifficultyFromEnv()-1)).To(Succeed())

				Expect(newBlock.Difficulty).To(Equal(powpkg.DifficultyFromEnv() - 1))
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
				newBlock.Transactions = append(newBlock.Transactions, domain.Transaction{
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
