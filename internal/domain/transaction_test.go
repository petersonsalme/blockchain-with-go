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
			Inputs:    []domain.TxIn{{PrevTxID: "aa", OutIndex: 0, PublicKey: "pk", Signature: "sig-1"}},
			Outputs:   []domain.TxOut{{Amount: 100, Address: "bob"}},
			Timestamp: fixedTime,
		}
	})

	Describe("CalculateHash", func() {
		It("does NOT change when ID changes", func() {
			original := baseTx.CalculateHash()
			baseTx.ID = "some-prefilled-id"
			Expect(baseTx.CalculateHash()).To(Equal(original))
		})

		It("does NOT change when an input signature or key changes", func() {
			original := baseTx.CalculateHash()
			baseTx.Inputs[0].Signature = "other"
			baseTx.Inputs[0].PublicKey = "other"
			Expect(baseTx.CalculateHash()).To(Equal(original))
		})
	})

	Describe("Validate", func() {
		It("succeeds for a transaction with empty ID", func() {
			Expect(baseTx.Validate()).To(Succeed())
		})

		It("succeeds when ID matches CalculateHash()", func() {
			baseTx.ID = baseTx.CalculateHash()
			Expect(baseTx.Validate()).To(Succeed())
		})

		It("fails when ID is non-empty and does not match CalculateHash()", func() {
			baseTx.ID = "tampered-or-divergent-id"
			Expect(baseTx.Validate()).To(MatchError(domain.ErrInvalidID))
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
			tx2 := baseTx
			tx2.Timestamp = fixedTime.Add(time.Second)

			order1 := domain.TransactionsHash([]domain.Transaction{baseTx, tx2})
			order2 := domain.TransactionsHash([]domain.Transaction{tx2, baseTx})

			Expect(order1).NotTo(Equal(order2))
		})

		It("is sensitive to transaction content", func() {
			original := domain.TransactionsHash([]domain.Transaction{baseTx})
			modified := baseTx
			modified.Outputs = []domain.TxOut{{Amount: 999, Address: "bob"}}
			Expect(domain.TransactionsHash([]domain.Transaction{modified})).NotTo(Equal(original))
		})

		It("is sensitive to the signature of the first input", func() {
			original := domain.TransactionsHash([]domain.Transaction{baseTx})
			modified := baseTx
			modified.Inputs = []domain.TxIn{{PrevTxID: "aa", OutIndex: 0, PublicKey: "pk", Signature: "changed"}}
			Expect(domain.TransactionsHash([]domain.Transaction{modified})).NotTo(Equal(original))
		})

		It("is sensitive to the signature of every input, not just the first", func() {
			baseTx.Inputs = append(baseTx.Inputs, domain.TxIn{PrevTxID: "bb", OutIndex: 1, PublicKey: "pk", Signature: "sig-2"})
			original := domain.TransactionsHash([]domain.Transaction{baseTx})

			modified := baseTx
			modified.Inputs = append([]domain.TxIn(nil), baseTx.Inputs...)
			modified.Inputs[1].Signature = "changed"
			Expect(domain.TransactionsHash([]domain.Transaction{modified})).NotTo(Equal(original))
		})
	})
})
