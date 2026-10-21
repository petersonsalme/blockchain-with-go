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
})
