package domain_test

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

func seededWallet(b byte) *wallet.Wallet {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = b + byte(i)
	}
	w, err := wallet.FromSeed(seed)
	if err != nil {
		panic(err)
	}
	return w
}

var _ = Describe("UTXO transaction", func() {
	var (
		owner, other *wallet.Wallet
		spent        domain.TxOut
		tx           domain.Transaction
	)

	BeforeEach(func() {
		owner = seededWallet(1)
		other = seededWallet(100)
		spent = domain.TxOut{Amount: 10, Address: owner.Address()}
		tx = domain.Transaction{
			Inputs:    []domain.TxIn{{PrevTxID: "aa", OutIndex: 1}, {PrevTxID: "bb", OutIndex: 2}},
			Outputs:   []domain.TxOut{{Amount: 7, Address: other.Address()}},
			Timestamp: time.Date(2026, 10, 19, 12, 0, 0, 5, time.UTC),
		}
	})

	Describe("UTXOHash", func() {
		It("is a 64-char hex SHA-256 and deterministic", func() {
			Expect(tx.UTXOHash()).To(HaveLen(64))
			Expect(tx.UTXOHash()).To(Equal(tx.UTXOHash()))
		})

		It("matches the canonical JSON vector", func() {
			// sha256 of the canonical JSON; fixes field names, order and timestamp format.
			const payload = `{"inputs":[{"prev_txid":"aa","out_index":1},{"prev_txid":"bb","out_index":2}],` +
				`"outputs":[{"amount":7,"address":"` + "%s" + `"}],"timestamp":"2026-10-19T12:00:00.000000005Z"}`
			Expect(tx.UTXOHash()).To(Equal(sha256Hex(sprintf(payload, other.Address()))))
		})

		It("returns an empty string for a nil transaction", func() {
			var nilTx *domain.Transaction
			Expect(nilTx.UTXOHash()).To(BeEmpty())
		})

		DescribeTable("changes when a committed field changes",
			func(mutate func(t *domain.Transaction)) {
				before := tx.UTXOHash()
				m := tx
				m.Inputs = append([]domain.TxIn(nil), tx.Inputs...)
				m.Outputs = append([]domain.TxOut(nil), tx.Outputs...)
				mutate(&m)
				Expect(m.UTXOHash()).NotTo(Equal(before))
			},
			Entry("PrevTxID", func(t *domain.Transaction) { t.Inputs[0].PrevTxID = "cc" }),
			Entry("OutIndex", func(t *domain.Transaction) { t.Inputs[1].OutIndex = 3 }),
			Entry("Amount", func(t *domain.Transaction) { t.Outputs[0].Amount = 8 }),
			Entry("Address", func(t *domain.Transaction) { t.Outputs[0].Address = "x" }),
			Entry("Timestamp", func(t *domain.Transaction) { t.Timestamp = t.Timestamp.Add(time.Nanosecond) }),
			Entry("extra input", func(t *domain.Transaction) { t.Inputs = append(t.Inputs, domain.TxIn{}) }),
			Entry("extra output", func(t *domain.Transaction) { t.Outputs = append(t.Outputs, domain.TxOut{}) }),
		)

		It("ignores input PublicKey and Signature", func() {
			before := tx.UTXOHash()
			tx.Inputs[0].PublicKey = "pk"
			tx.Inputs[0].Signature = "sig"
			Expect(tx.UTXOHash()).To(Equal(before))
		})

		It("ignores the account fields", func() {
			before := tx.UTXOHash()
			tx.From, tx.To, tx.Amount, tx.Fee, tx.Nonce = "a", "b", 1, 2, 3
			Expect(tx.UTXOHash()).To(Equal(before))
		})

		It("normalises the timestamp to UTC", func() {
			before := tx.UTXOHash()
			tx.Timestamp = tx.Timestamp.In(time.FixedZone("x", 3*3600))
			Expect(tx.UTXOHash()).To(Equal(before))
		})
	})

	Describe("SignInput", func() {
		It("signs the UTXOHash bytes and fills PublicKey/Signature", func() {
			Expect(domain.SignInput(&tx, 1, owner)).To(Succeed())
			Expect(tx.Inputs[1].PublicKey).To(Equal(hex.EncodeToString(owner.PublicKey())))
			sig, err := hex.DecodeString(tx.Inputs[1].Signature)
			Expect(err).NotTo(HaveOccurred())
			hash, _ := hex.DecodeString(tx.UTXOHash())
			Expect(ed25519.Verify(owner.PublicKey(), hash, sig)).To(BeTrue())
			Expect(tx.Inputs[0].Signature).To(BeEmpty())
		})

		It("rejects nil transaction, nil wallet and bad index", func() {
			Expect(domain.SignInput(nil, 0, owner)).To(HaveOccurred())
			Expect(domain.SignInput(&tx, 0, nil)).To(HaveOccurred())
			Expect(domain.SignInput(&tx, 2, owner)).To(MatchError(domain.ErrInputIndex))
			Expect(domain.SignInput(&tx, -1, owner)).To(MatchError(domain.ErrInputIndex))
		})

		It("keeps earlier signatures valid when another input is signed", func() {
			Expect(domain.SignInput(&tx, 0, owner)).To(Succeed())
			Expect(domain.SignInput(&tx, 1, other)).To(Succeed())
			Expect(domain.VerifyInputSignature(&tx, 0, spent)).To(Succeed())
		})
	})

	Describe("VerifyInputSignature", func() {
		BeforeEach(func() {
			Expect(domain.SignInput(&tx, 0, owner)).To(Succeed())
		})

		It("accepts a valid signature by the owner", func() {
			Expect(domain.VerifyInputSignature(&tx, 0, spent)).To(Succeed())
		})

		It("rejects nil transaction and bad index", func() {
			Expect(domain.VerifyInputSignature(nil, 0, spent)).To(HaveOccurred())
			Expect(domain.VerifyInputSignature(&tx, 2, spent)).To(MatchError(domain.ErrInputIndex))
			Expect(domain.VerifyInputSignature(&tx, -1, spent)).To(MatchError(domain.ErrInputIndex))
		})

		It("reports a missing signature or key", func() {
			Expect(domain.VerifyInputSignature(&tx, 1, spent)).To(MatchError(domain.ErrMissingSignature))
			s := tx
			s.Inputs = append([]domain.TxIn(nil), tx.Inputs...)
			s.Inputs[0].PublicKey = ""
			Expect(domain.VerifyInputSignature(&s, 0, spent)).To(MatchError(domain.ErrMissingSignature))
		})

		DescribeTable("reports an invalid public key",
			func(pub string) {
				tx.Inputs[0].PublicKey = pub
				Expect(domain.VerifyInputSignature(&tx, 0, spent)).To(MatchError(domain.ErrInvalidPublicKey))
			},
			Entry("short", "abcd"),
			Entry("not hex", "zz"+hex.EncodeToString(make([]byte, 31))),
			Entry("uppercase hex", func() string {
				return upper(hex.EncodeToString(seededWallet(1).PublicKey()))
			}()),
			Entry("small order (identity)", "0100000000000000000000000000000000000000000000000000000000000000"),
			Entry("non-canonical y", "edffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f"),
		)

		DescribeTable("reports an invalid signature",
			func(sig string) {
				tx.Inputs[0].Signature = sig
				Expect(domain.VerifyInputSignature(&tx, 0, spent)).To(MatchError(domain.ErrInvalidSignature))
			},
			Entry("short", "abcd"),
			Entry("not hex", "zz"+hex.EncodeToString(make([]byte, 63))),
			Entry("wrong bytes", hex.EncodeToString(make([]byte, 64))),
		)

		It("rejects an otherwise valid signature in uppercase hex", func() {
			tx.Inputs[0].Signature = upper(tx.Inputs[0].Signature)
			Expect(domain.VerifyInputSignature(&tx, 0, spent)).To(MatchError(domain.ErrInvalidSignature))
		})

		It("rejects a signature after a committed field changed", func() {
			tx.Outputs[0].Amount++
			Expect(domain.VerifyInputSignature(&tx, 0, spent)).To(MatchError(domain.ErrInvalidSignature))
		})

		It("reports an owner mismatch for a valid signature by another key", func() {
			Expect(domain.VerifyInputSignature(&tx, 0, domain.TxOut{Amount: 10, Address: other.Address()})).
				To(MatchError(domain.ErrInputOwnerMismatch))
		})

		It("checks the signature before the owner", func() {
			tx.Inputs[0].Signature = hex.EncodeToString(make([]byte, 64))
			Expect(domain.VerifyInputSignature(&tx, 0, domain.TxOut{Address: other.Address()})).
				To(MatchError(domain.ErrInvalidSignature))
		})
	})
})

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func sprintf(f string, a ...any) string { return fmt.Sprintf(f, a...) }

func upper(s string) string { return strings.ToUpper(s) }
