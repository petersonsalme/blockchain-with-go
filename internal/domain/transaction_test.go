package domain_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

var _ = Describe("Transaction", func() {
	var baseTx domain.Transaction
	fixedTime := time.Date(2026, 10, 16, 20, 0, 0, 123456000, time.UTC)

	BeforeEach(func() {
		baseTx = domain.Transaction{
			ID:        "",
			From:      "alice",
			To:        "bob",
			Amount:    100,
			Fee:       5,
			Nonce:     1,
			Signature: "sig-alice-123",
			Timestamp: fixedTime,
		}
	})

	Describe("CalculateHash", func() {
		It("is deterministic across multiple calls", func() {
			hash1 := baseTx.CalculateHash()
			hash2 := baseTx.CalculateHash()

			Expect(hash1).To(HaveLen(64))
			Expect(hash1).To(Equal(hash2))
		})

		It("changes when From changes", func() {
			original := baseTx.CalculateHash()
			modified := baseTx
			modified.From = "charlie"
			Expect(modified.CalculateHash()).NotTo(Equal(original))
		})

		It("changes when To changes", func() {
			original := baseTx.CalculateHash()
			modified := baseTx
			modified.To = "dave"
			Expect(modified.CalculateHash()).NotTo(Equal(original))
		})

		It("changes when Amount changes", func() {
			original := baseTx.CalculateHash()
			modified := baseTx
			modified.Amount = 200
			Expect(modified.CalculateHash()).NotTo(Equal(original))
		})

		It("changes when Fee changes", func() {
			original := baseTx.CalculateHash()
			modified := baseTx
			modified.Fee = 10
			Expect(modified.CalculateHash()).NotTo(Equal(original))
		})

		It("changes when Nonce changes", func() {
			original := baseTx.CalculateHash()
			modified := baseTx
			modified.Nonce = 2
			Expect(modified.CalculateHash()).NotTo(Equal(original))
		})

		It("changes when Timestamp changes", func() {
			original := baseTx.CalculateHash()
			modified := baseTx
			modified.Timestamp = fixedTime.Add(time.Second)
			Expect(modified.CalculateHash()).NotTo(Equal(original))
		})

		It("does NOT change when ID changes", func() {
			original := baseTx.CalculateHash()
			modified := baseTx
			modified.ID = "some-prefilled-id"
			Expect(modified.CalculateHash()).To(Equal(original))
		})

		It("does NOT change when Signature changes", func() {
			original := baseTx.CalculateHash()
			modified := baseTx
			modified.Signature = "different-signature"
			Expect(modified.CalculateHash()).To(Equal(original))
		})

		It("provides unambiguous encoding across adjacent field boundaries", func() {
			tx1 := baseTx
			tx1.From = "ab"
			tx1.To = "c"

			tx2 := baseTx
			tx2.From = "a"
			tx2.To = "bc"

			Expect(tx1.CalculateHash()).NotTo(Equal(tx2.CalculateHash()))
		})

		It("returns empty string when called on nil", func() {
			var nilTx *domain.Transaction
			Expect(nilTx.CalculateHash()).To(BeEmpty())
		})
	})

	Describe("Validate", func() {
		It("succeeds for a valid transaction with empty ID", func() {
			Expect(baseTx.Validate()).To(Succeed())
		})

		It("succeeds when ID matches CalculateHash()", func() {
			baseTx.ID = baseTx.CalculateHash()
			Expect(baseTx.Validate()).To(Succeed())
		})

		It("fails when From is empty", func() {
			baseTx.From = ""
			err := baseTx.Validate()
			Expect(err).To(MatchError(domain.ErrEmptyFrom))
		})

		It("fails when To is empty", func() {
			baseTx.To = ""
			err := baseTx.Validate()
			Expect(err).To(MatchError(domain.ErrEmptyTo))
		})

		It("fails when Amount is zero", func() {
			baseTx.Amount = 0
			err := baseTx.Validate()
			Expect(err).To(MatchError(domain.ErrInvalidAmount))
		})

		It("fails when ID is non-empty and does not match CalculateHash()", func() {
			baseTx.ID = "tampered-or-divergent-id"
			err := baseTx.Validate()
			Expect(err).To(MatchError(domain.ErrInvalidID))
		})

		It("fails when transaction is nil", func() {
			var nilTx *domain.Transaction
			Expect(nilTx.Validate()).To(HaveOccurred())
		})
	})

	Describe("TransactionsHash", func() {
		It("returns the same deterministic hash for nil and empty slices", func() {
			hashNil := domain.TransactionsHash(nil)
			hashEmpty := domain.TransactionsHash([]domain.Transaction{})

			Expect(hashNil).To(HaveLen(64))
			Expect(hashEmpty).To(Equal(hashNil))
		})

		It("is sensitive to the order of transactions", func() {
			tx1 := baseTx
			tx1.Nonce = 1
			tx2 := baseTx
			tx2.Nonce = 2

			order1 := domain.TransactionsHash([]domain.Transaction{tx1, tx2})
			order2 := domain.TransactionsHash([]domain.Transaction{tx2, tx1})

			Expect(order1).NotTo(Equal(order2))
		})

		It("is sensitive to transaction content", func() {
			tx1 := baseTx
			hashOriginal := domain.TransactionsHash([]domain.Transaction{tx1})

			txModified := baseTx
			txModified.Amount = 999
			hashModified := domain.TransactionsHash([]domain.Transaction{txModified})

			Expect(hashModified).NotTo(Equal(hashOriginal))
		})

		It("is sensitive to transaction signature", func() {
			tx1 := baseTx
			hashOriginal := domain.TransactionsHash([]domain.Transaction{tx1})

			txModified := baseTx
			txModified.Signature = "modified-signature"
			hashModified := domain.TransactionsHash([]domain.Transaction{txModified})

			Expect(hashModified).NotTo(Equal(hashOriginal))
		})
	})
})
