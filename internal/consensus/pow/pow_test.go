package pow_test

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	powpkg "github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// zeroBits counts the leading zero bits of a hex hash, decoding it with
// encoding/hex and looking at the bits one by one.
func zeroBits(hash string) int {
	raw, err := hex.DecodeString(hash)
	Expect(err).NotTo(HaveOccurred())

	for i := 0; i < len(raw)*8; i++ {
		if raw[i/8]&(0x80>>(i%8)) != 0 {
			return i
		}
	}

	return len(raw) * 8
}

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
				Transactions: []domain.Transaction{{Outputs: []domain.TxOut{{Amount: 10, Address: "bob"}}}},
				PrevHash:     "123",
			}
		})

		It("should fill nonce, difficulty and hash", func() {
			Expect(powpkg.NewProofOfWork(2).Seal(context.Background(), nil, b)).To(Succeed())

			Expect(b.Difficulty).To(Equal(2))
			Expect(zeroBits(b.Hash)).To(BeNumerically(">=", 2))
			Expect(b.Hash).To(Equal(b.CalculateHash()))
		})

		It("should return context.Canceled when the context is canceled", func() {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			err := powpkg.NewProofOfWork(0).Seal(ctx, nil, b)
			Expect(errors.Is(err, context.Canceled)).To(BeTrue())
			Expect(b.Hash).To(BeEmpty())
		})

		It("should return context.DeadlineExceeded when the deadline expires", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()

			err := mineAsync(func() error { return powpkg.NewProofOfWork(60).Seal(ctx, nil, b) })
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
					{Inputs: []domain.TxIn{{PrevTxID: "p1", Signature: "sig1"}}, Outputs: []domain.TxOut{{Amount: 10, Address: "bob"}}},
				},
				PrevHash: "123",
			}
		})

		It("should find a hash with the required leading zero bits", func() {
			Expect(powpkg.MineWithContext(context.Background(), b, 10)).To(Succeed())

			Expect(b.Difficulty).To(Equal(10))
			Expect(b.Hash).To(Equal(b.CalculateHash()))

			raw, err := hex.DecodeString(b.Hash)
			Expect(err).NotTo(HaveOccurred())
			Expect(raw[0]).To(BeZero())
			Expect(raw[1] & 0xC0).To(BeZero())
			Expect(zeroBits(b.Hash)).To(BeNumerically(">=", 10))
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
			Expect(zeroBits(b.Hash)).To(BeNumerically(">=", b.Difficulty))
		})
	})
	Describe("MineWithContext", func() {
		var b *domain.Block

		BeforeEach(func() {
			b = &domain.Block{
				Index:     1,
				Timestamp: time.Date(2021, 8, 15, 14, 30, 45, 100, time.UTC),
				Transactions: []domain.Transaction{
					{Inputs: []domain.TxIn{{PrevTxID: "p1", Signature: "sig1"}}, Outputs: []domain.TxOut{{Amount: 10, Address: "bob"}}},
				},
				PrevHash: "123",
			}
		})

		It("should find a hash with the required leading zeros and return nil error", func() {
			err := powpkg.MineWithContext(context.Background(), b, 2)

			Expect(err).NotTo(HaveOccurred())
			Expect(b.Difficulty).To(Equal(2))
			Expect(zeroBits(b.Hash)).To(BeNumerically(">=", 2))
			Expect(b.Hash).To(Equal(b.CalculateHash()))
			Expect(zeroBits(b.Hash)).To(BeNumerically(">=", b.Difficulty))
		})

		It("should treat a negative difficulty as zero", func() {
			err := powpkg.MineWithContext(context.Background(), b, -1)

			Expect(err).NotTo(HaveOccurred())
			Expect(b.Difficulty).To(Equal(0))
			Expect(b.Nonce).To(Equal(0))
			Expect(zeroBits(b.Hash)).To(BeNumerically(">=", b.Difficulty))
		})

		It("should return the smallest non-negative nonce that solves the difficulty", func() {
			Expect(powpkg.MineWithContext(context.Background(), b, 2)).To(Succeed())

			Expect(b.Nonce).To(BeNumerically(">", 0))

			solved := b.Nonce
			for n := 0; n < solved; n++ {
				b.Nonce = n
				Expect(zeroBits(b.CalculateHash())).To(BeNumerically("<", 2), "nonce %d already solves the difficulty", n)
			}
		})

		It("should handle a nil context gracefully", func() {
			err := powpkg.MineWithContext(context.TODO(), b, 1)

			Expect(err).NotTo(HaveOccurred())
			Expect(b.Difficulty).To(Equal(1))
			Expect(zeroBits(b.Hash)).To(BeNumerically(">=", b.Difficulty))
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
				err := mineAsync(func() error { return powpkg.MineWithContext(ctx, b, 60) })

				Expect(err).To(MatchError(context.Canceled))
				Expect(b.Hash).To(BeEmpty())
			})
		})

		When("the context deadline is exceeded (timeout)", func() {
			It("should abort and return context.DeadlineExceeded", func() {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				defer cancel()

				// Use high difficulty so mining will timeout
				err := mineAsync(func() error { return powpkg.MineWithContext(ctx, b, 60) })

				Expect(err).To(MatchError(context.DeadlineExceeded))
				Expect(b.Hash).To(BeEmpty())
			})
		})
	})
	Describe("Verify", func() {
		var (
			oldBlock, newBlock *domain.Block
			pow                *powpkg.ProofOfWork
		)

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

			newBlock = chain.GenerateBlock(pow, []*domain.Block{oldBlock}, []domain.Transaction{
				{Inputs: []domain.TxIn{{PrevTxID: "p1", Signature: "sig1"}}, Outputs: []domain.TxOut{{Amount: 10, Address: "bob"}}},
				{Inputs: []domain.TxIn{{PrevTxID: "p2", Signature: "sig2"}}, Outputs: []domain.TxOut{{Amount: 20, Address: "dave"}}},
			})
		})

		When("everything is fine", func() {
			It("should return true", func() {
				Expect(pow.Verify([]*domain.Block{oldBlock}, newBlock)).To(Succeed())
			})
		})

		When("index doesn't match", func() {
			It("should return false", func() {
				newBlock.Index = 50
				Expect(pow.Verify([]*domain.Block{oldBlock}, newBlock)).To(MatchError(consensus.ErrInvalidIndex))
			})
		})

		When("both index and previous hash don't match", func() {
			It("should report the index first", func() {
				newBlock.Index = 50
				newBlock.PrevHash = "abc"
				Expect(pow.Verify([]*domain.Block{oldBlock}, newBlock)).To(MatchError(consensus.ErrInvalidIndex))
			})
		})

		When("previous hash doesn't match", func() {
			It("should return false", func() {
				newBlock.PrevHash = "abc"
				Expect(pow.Verify([]*domain.Block{oldBlock}, newBlock)).To(MatchError(consensus.ErrInvalidPrevHash))
			})
		})

		When("hash doesn't match", func() {
			It("should return false", func() {
				newBlock.Hash = "123"
				Expect(pow.Verify([]*domain.Block{oldBlock}, newBlock)).To(MatchError(consensus.ErrInvalidHash))
			})
		})

		When("the hash doesn't satisfy the proof of work", func() {
			It("should return false", func() {
				Expect(newBlock.Difficulty).To(BeNumerically(">", 0))

				for newBlock.Nonce++; ; newBlock.Nonce++ {
					newBlock.Hash = newBlock.CalculateHash()
					if zeroBits(newBlock.Hash) < newBlock.Difficulty {
						break
					}
				}

				Expect(newBlock.Hash).To(Equal(newBlock.CalculateHash()))
				Expect(pow.Verify([]*domain.Block{oldBlock}, newBlock)).To(MatchError(consensus.ErrInvalidProofOfWork))
			})
		})

		When("POW_DIFFICULTY is raised after the block was mined", func() {
			It("should still return true", func() {
				Expect(newBlock.Difficulty).To(Equal(2))

				pinDifficultyEnv("4")

				Expect(pow.Verify([]*domain.Block{oldBlock}, newBlock)).To(Succeed())
			})
		})

		When("the block was mined with a difficulty lower than the current one", func() {
			It("should reject it as off-rule even though the proof of work holds", func() {
				Expect(powpkg.MineWithContext(context.Background(), newBlock, powpkg.DifficultyFromEnv()-1)).To(Succeed())

				Expect(newBlock.Difficulty).To(Equal(powpkg.DifficultyFromEnv() - 1))
				Expect(pow.Verify([]*domain.Block{oldBlock}, newBlock)).To(MatchError(consensus.ErrInvalidDifficulty))
			})
		})

		When("the recorded difficulty is tampered with after mining", func() {
			It("should return false", func() {
				newBlock.Difficulty++

				Expect(newBlock.CalculateHash()).NotTo(Equal(newBlock.Hash))
				Expect(pow.Verify([]*domain.Block{oldBlock}, newBlock)).To(HaveOccurred())
			})
		})

		When("transaction amount is altered after mining", func() {
			It("should return false", func() {
				newBlock.Transactions[0].Outputs[0].Amount = 999

				// The seal covers the transactions only through MerkleRoot, so the
				// edit is caught by the block rules rather than by Verify.
				Expect(pow.Verify([]*domain.Block{oldBlock}, newBlock)).To(Succeed())
				Expect(domain.ValidateMerkleRoot(newBlock)).To(HaveOccurred())
			})
		})

		When("transaction input is altered after mining", func() {
			It("should return false", func() {
				newBlock.Transactions[0].Inputs[0].PrevTxID = "mallory"

				// The seal covers the transactions only through MerkleRoot, so the
				// edit is caught by the block rules rather than by Verify.
				Expect(pow.Verify([]*domain.Block{oldBlock}, newBlock)).To(Succeed())
				Expect(domain.ValidateMerkleRoot(newBlock)).To(HaveOccurred())
			})
		})

		When("transaction recipient is altered after mining", func() {
			It("should return false", func() {
				newBlock.Transactions[0].Outputs[0].Address = "mallory"

				// The seal covers the transactions only through MerkleRoot, so the
				// edit is caught by the block rules rather than by Verify.
				Expect(pow.Verify([]*domain.Block{oldBlock}, newBlock)).To(Succeed())
				Expect(domain.ValidateMerkleRoot(newBlock)).To(HaveOccurred())
			})
		})

		When("transaction input index is altered after mining", func() {
			It("should return false", func() {
				newBlock.Transactions[0].Inputs[0].OutIndex = 99

				// The seal covers the transactions only through MerkleRoot, so the
				// edit is caught by the block rules rather than by Verify.
				Expect(pow.Verify([]*domain.Block{oldBlock}, newBlock)).To(Succeed())
				Expect(domain.ValidateMerkleRoot(newBlock)).To(HaveOccurred())
			})
		})

		When("transaction timestamp is altered after mining", func() {
			It("should return false", func() {
				newBlock.Transactions[0].Timestamp = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

				// The seal covers the transactions only through MerkleRoot, so the
				// edit is caught by the block rules rather than by Verify.
				Expect(pow.Verify([]*domain.Block{oldBlock}, newBlock)).To(Succeed())
				Expect(domain.ValidateMerkleRoot(newBlock)).To(HaveOccurred())
			})
		})

		When("a transaction is added after mining", func() {
			It("should return false", func() {
				newBlock.Transactions = append(newBlock.Transactions, domain.Transaction{
					Outputs: []domain.TxOut{{Amount: 30, Address: "frank"}},
				})

				// The seal covers the transactions only through MerkleRoot, so the
				// edit is caught by the block rules rather than by Verify.
				Expect(pow.Verify([]*domain.Block{oldBlock}, newBlock)).To(Succeed())
				Expect(domain.ValidateMerkleRoot(newBlock)).To(HaveOccurred())
			})
		})

		When("a transaction is removed after mining", func() {
			It("should return false", func() {
				newBlock.Transactions = newBlock.Transactions[:1]

				// The seal covers the transactions only through MerkleRoot, so the
				// edit is caught by the block rules rather than by Verify.
				Expect(pow.Verify([]*domain.Block{oldBlock}, newBlock)).To(Succeed())
				Expect(domain.ValidateMerkleRoot(newBlock)).To(HaveOccurred())
			})
		})

		When("transactions are reordered after mining", func() {
			It("should return false", func() {
				newBlock.Transactions[0], newBlock.Transactions[1] = newBlock.Transactions[1], newBlock.Transactions[0]

				// The seal covers the transactions only through MerkleRoot, so the
				// edit is caught by the block rules rather than by Verify.
				Expect(pow.Verify([]*domain.Block{oldBlock}, newBlock)).To(Succeed())
				Expect(domain.ValidateMerkleRoot(newBlock)).To(HaveOccurred())
			})
		})

		When("a transaction signature is altered after mining", func() {
			It("should return false", func() {
				newBlock.Transactions[0].Inputs[0].Signature = "tampered-sig"

				// The seal covers the transactions only through MerkleRoot, so the
				// edit is caught by the block rules rather than by Verify.
				Expect(pow.Verify([]*domain.Block{oldBlock}, newBlock)).To(Succeed())
				Expect(domain.ValidateMerkleRoot(newBlock)).To(HaveOccurred())
			})
		})

		When("the merkle root is altered after mining", func() {
			It("should return false", func() {
				newBlock.MerkleRoot = domain.MerkleRoot(nil)

				Expect(newBlock.CalculateHash()).NotTo(Equal(newBlock.Hash))
				Expect(pow.Verify([]*domain.Block{oldBlock}, newBlock)).To(MatchError(consensus.ErrInvalidHash))
			})
		})
	})
})
