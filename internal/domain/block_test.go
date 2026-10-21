package domain_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

var _ = Describe("Block", func() {
	Describe("CalculateHash", func() {
		var b *domain.Block

		BeforeEach(func() {
			b = &domain.Block{
				Index:        0,
				Timestamp:    time.Date(2021, 8, 15, 14, 30, 45, 100, time.UTC),
				Transactions: []domain.Transaction{},
				MerkleRoot:   "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				PrevHash:     "123",
				Hash:         "",
			}
		})

		It("should calculate the block hash successfully", func() {
			Expect(b.CalculateHash()).To(Equal("88920d5c885bdbb82f32f116a8c34b320eabd362041084756e76e65639d5972c"))
		})

		It("should change the hash when only the merkle root changes", func() {
			original := b.CalculateHash()
			b.MerkleRoot = "00" + b.MerkleRoot[2:]
			Expect(b.CalculateHash()).NotTo(Equal(original))
		})

		It("should not depend on the transactions except through the merkle root", func() {
			original := b.CalculateHash()
			b.Transactions = []domain.Transaction{{Outputs: []domain.TxOut{{Amount: 1, Address: "a"}}}}
			Expect(b.CalculateHash()).To(Equal(original))
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

		It("should change the hash when the validator changes", func() {
			original := b.CalculateHash()
			b.Validator = "ab"
			Expect(b.CalculateHash()).NotTo(Equal(original))
		})

		It("should not change the hash when the signature changes", func() {
			original := b.CalculateHash()
			b.Signature = "cd"
			Expect(b.CalculateHash()).To(Equal(original))
		})

		It("should append the validator at the end of the legacy record", func() {
			legacy := func(b *domain.Block) string {
				record := fmt.Sprintf("%d%s%s%s%d%d", b.Index, b.Timestamp.UTC().String(), b.MerkleRoot, b.PrevHash, b.Nonce, b.Difficulty)
				sum := sha256.Sum256([]byte(record))
				return hex.EncodeToString(sum[:])
			}

			for _, nonce := range []int{0, 1, 42} {
				for _, difficulty := range []int{0, 2} {
					b.Nonce, b.Difficulty = nonce, difficulty

					b.Validator = ""
					Expect(b.CalculateHash()).To(Equal(legacy(b)))

					record := fmt.Sprintf("%d%s%s%s%d%d%s", b.Index, b.Timestamp.UTC().String(), b.MerkleRoot, b.PrevHash, b.Nonce, b.Difficulty, "ab")
					sum := sha256.Sum256([]byte(record))
					b.Validator = "ab"
					Expect(b.CalculateHash()).To(Equal(hex.EncodeToString(sum[:])))
				}
			}
		})
	})
})
