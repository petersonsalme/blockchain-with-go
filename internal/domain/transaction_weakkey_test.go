package domain_test

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing/quick"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

var _ = Describe("Weak Ed25519 public keys on inputs", func() {
	var spent domain.TxOut

	BeforeEach(func() {
		spent = domain.TxOut{Amount: 1, Address: seededWallet(1).Address()}
	})

	weakTx := func(pubHex string) *domain.Transaction {
		return &domain.Transaction{
			Inputs:    []domain.TxIn{{PrevTxID: "aa", PublicKey: pubHex, Signature: "01" + strings.Repeat("00", 63)}},
			Outputs:   []domain.TxOut{{Amount: 1, Address: seededWallet(100).Address()}},
			Timestamp: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC),
		}
	}

	// hexLE builds a 32-byte little-endian key from the first and last byte,
	// with every middle byte equal to mid.
	hexLE := func(first byte, mid byte, last byte) string {
		b := make([]byte, 32)
		for i := range b {
			b[i] = mid
		}

		b[0], b[31] = first, last

		return hex.EncodeToString(b)
	}

	smallOrder := map[string]string{
		"y=0 sign 0":    hexLE(0x00, 0x00, 0x00),
		"y=0 sign 1":    hexLE(0x00, 0x00, 0x80),
		"y=1 sign 0":    hexLE(0x01, 0x00, 0x00),
		"y=1 sign 1":    hexLE(0x01, 0x00, 0x80),
		"y=p-1 sign 0":  hexLE(0xec, 0xff, 0x7f),
		"y=p-1 sign 1":  hexLE(0xec, 0xff, 0xff),
		"y=y8 sign 0":   "26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05",
		"y=y8 sign 1":   "26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc85",
		"y=p-y8 sign 0": "c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac037a",
		"y=p-y8 sign 1": "c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac03fa",
	}
	for name, pub := range smallOrder {
		It("rejects small-order key "+name, func() {
			Expect(domain.VerifyInputSignature(weakTx(pub), 0, spent)).To(MatchError(domain.ErrInvalidPublicKey))
		})
	}

	nonCanonical := map[string]string{
		"y=p sign 0":       hexLE(0xed, 0xff, 0x7f),
		"y=p sign 1":       hexLE(0xed, 0xff, 0xff),
		"y=p+1 sign 0":     hexLE(0xee, 0xff, 0x7f),
		"y=p+1 sign 1":     hexLE(0xee, 0xff, 0xff),
		"y=2^255-1 sign 0": hexLE(0xff, 0xff, 0x7f),
		"y=2^255-1 sign 1": hexLE(0xff, 0xff, 0xff),
	}
	for name, pub := range nonCanonical {
		It("rejects non-canonical key "+name, func() {
			Expect(domain.VerifyInputSignature(weakTx(pub), 0, spent)).To(MatchError(domain.ErrInvalidPublicKey))
		})
	}

	It("does not reject the canonical neighbours of the boundary", func() {
		// y = p-2 and y = 2 are canonical, not small order: the key check
		// passes and the bogus signature is what fails.
		for _, pub := range []string{hexLE(0xeb, 0xff, 0x7f), hexLE(0x02, 0x00, 0x00)} {
			Expect(domain.VerifyInputSignature(weakTx(pub), 0, spent)).To(MatchError(domain.ErrInvalidSignature))
		}
		// Same for a key with a middle byte below 0xff and y[0] >= 0xed,
		// including the first and last middle bytes (bounds of y[1:31]).
		for _, i := range []int{1, 15, 30} {
			b, _ := hex.DecodeString(hexLE(0xff, 0xff, 0x7f))
			b[i] = 0xfe
			Expect(domain.VerifyInputSignature(weakTx(hex.EncodeToString(b)), 0, spent)).
				To(MatchError(domain.ErrInvalidSignature), "middle byte %d", i)
		}
	})

	It("Invariant 1: keys from wallet.FromSeed are never rejected", func() {
		prop := func(seed [32]byte) bool {
			w, err := wallet.FromSeed(seed[:])
			if err != nil {
				return false
			}

			tx := weakTx("")
			tx.Inputs[0].PublicKey, tx.Inputs[0].Signature = "", ""

			return domain.SignInput(tx, 0, w) == nil &&
				domain.VerifyInputSignature(tx, 0, domain.TxOut{Amount: 1, Address: w.Address()}) == nil
		}
		Expect(quick.Check(prop, &quick.Config{MaxCount: 1000})).To(Succeed())
	})

	It("Invariant 2: weak keys yield ErrInvalidPublicKey whatever the other fields", func() {
		keys := []string{}
		for _, k := range smallOrder {
			keys = append(keys, k)
		}

		for _, k := range nonCanonical {
			keys = append(keys, k)
		}

		prop := func(i uint8, prev, addr string, amount uint64, ts int64, sig [64]byte) bool {
			tx := &domain.Transaction{
				Inputs:    []domain.TxIn{{PrevTxID: prev, PublicKey: keys[int(i)%len(keys)], Signature: hex.EncodeToString(sig[:])}},
				Outputs:   []domain.TxOut{{Amount: amount, Address: addr}},
				Timestamp: time.Unix(0, ts).UTC(),
			}

			return errors.Is(domain.VerifyInputSignature(tx, 0, spent), domain.ErrInvalidPublicKey)
		}
		Expect(quick.Check(prop, &quick.Config{MaxCount: 1000})).To(Succeed())
	})
})
