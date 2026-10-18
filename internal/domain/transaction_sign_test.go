package domain_test

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing/quick"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

var _ = Describe("Transaction Signing and Verification", func() {
	var (
		senderWallet    *wallet.Wallet
		recipientWallet *wallet.Wallet
		fixedSeed       []byte
		recipientSeed   []byte
		fixedTime       time.Time
	)

	BeforeEach(func() {
		fixedSeed = make([]byte, 32)
		for i := range fixedSeed {
			fixedSeed[i] = byte(i + 1)
		}
		var err error
		senderWallet, err = wallet.FromSeed(fixedSeed)
		Expect(err).NotTo(HaveOccurred())

		recipientSeed = make([]byte, 32)
		for i := range recipientSeed {
			recipientSeed[i] = byte(i + 42)
		}
		recipientWallet, err = wallet.FromSeed(recipientSeed)
		Expect(err).NotTo(HaveOccurred())

		fixedTime = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	})

	Describe("SignTransaction with fixed seed vector", func() {
		It("populates PublicKey, ID, and Signature matching deterministic vector", func() {
			tx := domain.Transaction{
				From:      senderWallet.Address(),
				To:        recipientWallet.Address(),
				Amount:    1000,
				Fee:       10,
				Nonce:     1,
				Timestamp: fixedTime,
			}

			err := domain.SignTransaction(&tx, senderWallet)
			Expect(err).NotTo(HaveOccurred())

			const (
				expectedPubKey = "79b5562e8fe654f94078b112e8a98ba7901f853ae695bed7e0e3910bad049664"
				expectedHash   = "2c8a9175e62a892b3f0ef42b71d41138836a17225bf6e7a35c6f6a349705f114"
				expectedSig    = "79bc8744fb65a7bd823e8fee8ce6f944c9a3ddf0c0db96115d4347abc269fdea664b2ef27548713aed880704e00c4f36c0ef4f7d26f439882d24e04232f55809"
			)

			Expect(tx.PublicKey).To(Equal(expectedPubKey))
			Expect(tx.ID).To(Equal(expectedHash))
			Expect(tx.Signature).To(Equal(expectedSig))
			Expect(tx.VerifySignature()).To(Succeed())
		})

		It("fails when transaction is nil", func() {
			err := domain.SignTransaction(nil, senderWallet)
			Expect(err).To(HaveOccurred())
		})

		It("fails when wallet is nil", func() {
			tx := domain.Transaction{
				From: senderWallet.Address(),
				To:   recipientWallet.Address(),
			}
			err := domain.SignTransaction(&tx, nil)
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("VerifySignature sentinel errors table", func() {
		var validTx domain.Transaction

		BeforeEach(func() {
			validTx = domain.Transaction{
				From:      senderWallet.Address(),
				To:        recipientWallet.Address(),
				Amount:    500,
				Fee:       5,
				Nonce:     1,
				Timestamp: fixedTime,
			}
			err := domain.SignTransaction(&validTx, senderWallet)
			Expect(err).NotTo(HaveOccurred())
		})

		It("verifies successfully for a valid signed transaction", func() {
			Expect(validTx.VerifySignature()).To(Succeed())
		})

		DescribeTable("sentinel error checks in order",
			func(mutator func(tx *domain.Transaction), expectedErr error) {
				tx := validTx
				mutator(&tx)
				err := tx.VerifySignature()
				Expect(err).To(HaveOccurred())
				Expect(errors.Is(err, expectedErr)).To(BeTrue(), "expected errors.Is with %v, got %v", expectedErr, err)
			},

			// 1. ErrMissingSignature (Signature or PublicKey empty)
			Entry("missing Signature", func(tx *domain.Transaction) {
				tx.Signature = ""
			}, domain.ErrMissingSignature),
			Entry("missing PublicKey", func(tx *domain.Transaction) {
				tx.PublicKey = ""
			}, domain.ErrMissingSignature),
			Entry("both Signature and PublicKey empty", func(tx *domain.Transaction) {
				tx.Signature = ""
				tx.PublicKey = ""
			}, domain.ErrMissingSignature),

			// 2. ErrInvalidPublicKey (not lowercase hex of 64 chars)
			Entry("PublicKey too short (odd length: 63 hex)", func(tx *domain.Transaction) {
				tx.PublicKey = strings.Repeat("a", 63)
			}, domain.ErrInvalidPublicKey),
			Entry("PublicKey too short (even length: 62 hex, matching sender)", func(tx *domain.Transaction) {
				raw31 := make([]byte, 31)
				for i := range raw31 {
					raw31[i] = byte(i + 1)
				}
				tx.PublicKey = hex.EncodeToString(raw31)
				tx.From = wallet.AddressFromPublicKey(ed25519.PublicKey(raw31))
			}, domain.ErrInvalidPublicKey),
			Entry("PublicKey too long (odd length: 65 hex)", func(tx *domain.Transaction) {
				tx.PublicKey = strings.Repeat("a", 65)
			}, domain.ErrInvalidPublicKey),
			Entry("PublicKey too long (even length: 66 hex, matching sender)", func(tx *domain.Transaction) {
				raw33 := make([]byte, 33)
				for i := range raw33 {
					raw33[i] = byte(i + 1)
				}
				tx.PublicKey = hex.EncodeToString(raw33)
				tx.From = wallet.AddressFromPublicKey(ed25519.PublicKey(raw33))
			}, domain.ErrInvalidPublicKey),
			Entry("PublicKey invalid hex chars", func(tx *domain.Transaction) {
				tx.PublicKey = strings.Repeat("z", 64)
			}, domain.ErrInvalidPublicKey),
			Entry("PublicKey uppercase hex", func(tx *domain.Transaction) {
				tx.PublicKey = strings.ToUpper(validTx.PublicKey)
			}, domain.ErrInvalidPublicKey),

			// 3. ErrInvalidRecipient (To fails wallet.ValidateAddress)
			Entry("To address is empty", func(tx *domain.Transaction) {
				tx.To = ""
			}, domain.ErrInvalidRecipient),
			Entry("To address has wrong length", func(tx *domain.Transaction) {
				tx.To = "001122"
			}, domain.ErrInvalidRecipient),
			Entry("To address has invalid checksum", func(tx *domain.Transaction) {
				badChecksum := validTx.To[:len(validTx.To)-2] + "00"
				if badChecksum == validTx.To {
					badChecksum = validTx.To[:len(validTx.To)-2] + "01"
				}
				tx.To = badChecksum
			}, domain.ErrInvalidRecipient),
			Entry("To address has uppercase hex", func(tx *domain.Transaction) {
				tx.To = strings.ToUpper(validTx.To)
			}, domain.ErrInvalidRecipient),

			// 4. ErrSenderMismatch (From != wallet.AddressFromPublicKey(pub))
			Entry("From does not match public key derived address", func(tx *domain.Transaction) {
				tx.From = recipientWallet.Address()
			}, domain.ErrSenderMismatch),

			// 5. ErrInvalidSignature (not hex 128 chars or wallet.Verify fails)
			Entry("Signature too short (odd length: 127 hex)", func(tx *domain.Transaction) {
				tx.Signature = strings.Repeat("a", 127)
			}, domain.ErrInvalidSignature),
			Entry("Signature too short (even length: 126 hex)", func(tx *domain.Transaction) {
				tx.Signature = strings.Repeat("ab", 63)
			}, domain.ErrInvalidSignature),
			Entry("Signature too long (odd length: 129 hex)", func(tx *domain.Transaction) {
				tx.Signature = strings.Repeat("a", 129)
			}, domain.ErrInvalidSignature),
			Entry("Signature too long (even length: 130 hex)", func(tx *domain.Transaction) {
				tx.Signature = strings.Repeat("ab", 65)
			}, domain.ErrInvalidSignature),
			Entry("Signature non-hex chars", func(tx *domain.Transaction) {
				tx.Signature = strings.Repeat("z", 128)
			}, domain.ErrInvalidSignature),
			Entry("Signature uppercase hex", func(tx *domain.Transaction) {
				tx.Signature = strings.ToUpper(validTx.Signature)
			}, domain.ErrInvalidSignature),
			Entry("Signature cryptographic verification failure (corrupted signature)", func(tx *domain.Transaction) {
				corrupted := []byte(tx.Signature)
				if corrupted[0] == '0' {
					corrupted[0] = '1'
				} else {
					corrupted[0] = '0'
				}
				tx.Signature = string(corrupted)
			}, domain.ErrInvalidSignature),
		)

		It("respects check order: missing signature takes precedence over invalid pubkey", func() {
			tx := validTx
			tx.Signature = ""
			tx.PublicKey = "invalid-pub"
			Expect(errors.Is(tx.VerifySignature(), domain.ErrMissingSignature)).To(BeTrue())
		})

		It("respects check order: invalid pubkey takes precedence over invalid recipient", func() {
			tx := validTx
			tx.PublicKey = "invalid-pub"
			tx.To = "invalid-recipient"
			Expect(errors.Is(tx.VerifySignature(), domain.ErrInvalidPublicKey)).To(BeTrue())
		})

		It("respects check order: invalid recipient takes precedence over sender mismatch", func() {
			tx := validTx
			tx.To = "invalid-recipient"
			tx.From = "wrong-sender"
			Expect(errors.Is(tx.VerifySignature(), domain.ErrInvalidRecipient)).To(BeTrue())
		})

		It("respects check order: sender mismatch takes precedence over invalid signature", func() {
			tx := validTx
			tx.From = recipientWallet.Address()
			tx.Signature = "invalid-sig"
			Expect(errors.Is(tx.VerifySignature(), domain.ErrSenderMismatch)).To(BeTrue())
		})

		It("returns error when transaction is nil", func() {
			var nilTx *domain.Transaction
			Expect(nilTx.VerifySignature()).To(HaveOccurred())
		})
	})

	Describe("Property-based testing (testing/quick)", func() {
		const quickRuns = 1000

		It("Invariant 1: For any tx and wallet w with tx.From=w.Address() and valid tx.To, after SignTransaction(tx, w), VerifySignature() is nil", func() {
			prop := func(amount, fee, nonce uint64, tsNanos int64, seedA, seedB [32]byte) bool {
				w1, err := wallet.FromSeed(seedA[:])
				if err != nil {
					return false
				}
				w2, err := wallet.FromSeed(seedB[:])
				if err != nil {
					return false
				}
				if tsNanos < 0 {
					tsNanos = -tsNanos
				}
				ts := time.Unix(tsNanos/1e9, tsNanos%1e9).UTC()
				tx := domain.Transaction{
					From:      w1.Address(),
					To:        w2.Address(),
					Amount:    amount,
					Fee:       fee,
					Nonce:     nonce,
					Timestamp: ts,
				}
				if err := domain.SignTransaction(&tx, w1); err != nil {
					return false
				}
				return tx.VerifySignature() == nil
			}

			cfg := &quick.Config{MaxCount: quickRuns}
			Expect(quick.Check(prop, cfg)).To(Succeed())
		})

		It("Invariant 2: Tampering single field yields ErrInvalidSignature, From yields ErrSenderMismatch, signature byte yields ErrInvalidSignature", func() {
			prop := func(amount, fee, nonce uint64, tsNanos int64, seedA, seedB, seedC [32]byte) bool {
				if seedA == seedC || seedB == seedC {
					seedC[0] ^= 0xff
				}
				w1, _ := wallet.FromSeed(seedA[:])
				w2, _ := wallet.FromSeed(seedB[:])
				w3, _ := wallet.FromSeed(seedC[:])

				if tsNanos < 0 {
					tsNanos = -tsNanos
				}
				ts := time.Unix(tsNanos/1e9, tsNanos%1e9).UTC()
				base := domain.Transaction{
					From:      w1.Address(),
					To:        w2.Address(),
					Amount:    amount,
					Fee:       fee,
					Nonce:     nonce,
					Timestamp: ts,
				}
				if err := domain.SignTransaction(&base, w1); err != nil {
					return false
				}

				// Modifying To to another valid address -> ErrInvalidSignature
				txTo := base
				txTo.To = w3.Address()
				if !errors.Is(txTo.VerifySignature(), domain.ErrInvalidSignature) {
					return false
				}

				// Modifying Amount -> ErrInvalidSignature
				txAmt := base
				if txAmt.Amount == 0 {
					txAmt.Amount = 1
				} else {
					txAmt.Amount ^= 1
				}
				if !errors.Is(txAmt.VerifySignature(), domain.ErrInvalidSignature) {
					return false
				}

				// Modifying Fee -> ErrInvalidSignature
				txFee := base
				if txFee.Fee == 0 {
					txFee.Fee = 1
				} else {
					txFee.Fee ^= 1
				}
				if !errors.Is(txFee.VerifySignature(), domain.ErrInvalidSignature) {
					return false
				}

				// Modifying Nonce -> ErrInvalidSignature
				txNonce := base
				if txNonce.Nonce == 0 {
					txNonce.Nonce = 1
				} else {
					txNonce.Nonce ^= 1
				}
				if !errors.Is(txNonce.VerifySignature(), domain.ErrInvalidSignature) {
					return false
				}

				// Modifying Timestamp by >= 1 ns -> ErrInvalidSignature
				txTS := base
				txTS.Timestamp = txTS.Timestamp.Add(time.Nanosecond)
				if !errors.Is(txTS.VerifySignature(), domain.ErrInvalidSignature) {
					return false
				}

				// Modifying From -> ErrSenderMismatch
				txFrom := base
				txFrom.From = w3.Address()
				if !errors.Is(txFrom.VerifySignature(), domain.ErrSenderMismatch) {
					return false
				}

				// Corrupting one byte/char of Signature -> ErrInvalidSignature
				txSig := base
				sigBytes := []byte(txSig.Signature)
				if sigBytes[0] == '0' {
					sigBytes[0] = '1'
				} else {
					sigBytes[0] = '0'
				}
				txSig.Signature = string(sigBytes)
				if !errors.Is(txSig.VerifySignature(), domain.ErrInvalidSignature) {
					return false
				}

				return true
			}

			cfg := &quick.Config{MaxCount: quickRuns}
			Expect(quick.Check(prop, cfg)).To(Succeed())
		})

		It("Invariant 3: Swapping PublicKey and From for w2 fails verification", func() {
			prop := func(amount, fee, nonce uint64, tsNanos int64, seedA, seedB [32]byte) bool {
				if seedA == seedB {
					seedB[0] ^= 0xff
				}
				w1, _ := wallet.FromSeed(seedA[:])
				w2, _ := wallet.FromSeed(seedB[:])

				if tsNanos < 0 {
					tsNanos = -tsNanos
				}
				ts := time.Unix(tsNanos/1e9, tsNanos%1e9).UTC()
				tx := domain.Transaction{
					From:      w1.Address(),
					To:        w2.Address(),
					Amount:    amount,
					Fee:       fee,
					Nonce:     nonce,
					Timestamp: ts,
				}
				if err := domain.SignTransaction(&tx, w1); err != nil {
					return false
				}

				// Swap PublicKey and From with w2's
				tx.PublicKey = hex.EncodeToString(w2.PublicKey())
				tx.From = w2.Address()

				err := tx.VerifySignature()
				return errors.Is(err, domain.ErrInvalidSignature)
			}

			cfg := &quick.Config{MaxCount: quickRuns}
			Expect(quick.Check(prop, cfg)).To(Succeed())
		})

		It("Invariant 4: CalculateHash is invariant to time zones, Round(0), ID/Signature/PublicKey values, and json serialization round-trip", func() {
			prop := func(from, to string, amount, fee, nonce uint64, sec int64, nsec uint32, id, sig, pub string, tzOffsetSec int32) bool {
				// Avoid extreme timestamps that would break RFC3339 representation
				if sec < 0 {
					sec = -sec
				}
				sec = sec % 2000000000 // up to ~year 2033
				ts := time.Unix(sec, int64(nsec%1000000000)).UTC()

				tx := domain.Transaction{
					ID:        id,
					From:      from,
					To:        to,
					Amount:    amount,
					Fee:       fee,
					Nonce:     nonce,
					Signature: sig,
					PublicKey: pub,
					Timestamp: ts,
				}
				originalHash := tx.CalculateHash()

				// Invariant to time.Location / In(loc)
				offset := int(tzOffsetSec % 43200)
				loc := time.FixedZone("TEST_ZONE", offset)
				txLoc := tx
				txLoc.Timestamp = txLoc.Timestamp.In(loc)
				if txLoc.CalculateHash() != originalHash {
					return false
				}

				// Invariant to Round(0) (strips monotonic clock)
				nowWithMono := time.Now().Add(time.Duration(sec%1000) * time.Second)
				txWithMono := tx
				txWithMono.Timestamp = nowWithMono
				txWithoutMono := txWithMono
				txWithoutMono.Timestamp = nowWithMono.Round(0)
				if txWithMono.CalculateHash() != txWithoutMono.CalculateHash() {
					return false
				}

				// Invariant to ID, Signature, PublicKey mutations
				txMutated := tx
				txMutated.ID = "other-id-" + id
				txMutated.Signature = "other-sig-" + sig
				txMutated.PublicKey = "other-pub-" + pub
				if txMutated.CalculateHash() != originalHash {
					return false
				}

				// Invariant to json.Marshal / json.Unmarshal round-trip
				data, err := json.Marshal(tx)
				if err != nil {
					return false
				}
				var roundTrip domain.Transaction
				if err := json.Unmarshal(data, &roundTrip); err != nil {
					return false
				}
				if roundTrip.CalculateHash() != originalHash {
					return false
				}

				return true
			}

			cfg := &quick.Config{MaxCount: quickRuns}
			Expect(quick.Check(prop, cfg)).To(Succeed())
		})

		It("Invariant 5: Any two transactions differing in at least one signed field produce distinct hashes", func() {
			prop := func(from, to string, amount, fee, nonce uint64, sec int64, nsec uint32, fieldSelector byte) bool {
				if sec < 0 {
					sec = -sec
				}
				sec = sec % 2000000000
				ts := time.Unix(sec, int64(nsec%1000000000)).UTC()

				tx1 := domain.Transaction{
					From:      from,
					To:        to,
					Amount:    amount,
					Fee:       fee,
					Nonce:     nonce,
					Timestamp: ts,
				}
				tx2 := tx1

				switch fieldSelector % 6 {
				case 0:
					tx2.From += "x"
				case 1:
					tx2.To += "y"
				case 2:
					tx2.Amount ^= 1
				case 3:
					tx2.Fee ^= 1
				case 4:
					tx2.Nonce ^= 1
				case 5:
					tx2.Timestamp = tx2.Timestamp.Add(time.Nanosecond)
				}

				return tx1.CalculateHash() != tx2.CalculateHash()
			}

			cfg := &quick.Config{MaxCount: quickRuns}
			Expect(quick.Check(prop, cfg)).To(Succeed())
		})

		It("Invariant 5 boundary shift: From=\"ab\",To=\"c\" vs From=\"a\",To=\"bc\" produce distinct hashes", func() {
			tx1 := domain.Transaction{
				From:      "ab",
				To:        "c",
				Amount:    10,
				Fee:       1,
				Nonce:     0,
				Timestamp: fixedTime,
			}
			tx2 := domain.Transaction{
				From:      "a",
				To:        "bc",
				Amount:    10,
				Fee:       1,
				Nonce:     0,
				Timestamp: fixedTime,
			}
			Expect(tx1.CalculateHash()).NotTo(Equal(tx2.CalculateHash()))
		})

		It("Invariant 6: internal/domain package dependencies include only pkg/wallet as new internal dependency", func() {
			cmd := exec.Command("go", "list", "-deps", "github.com/petersonsalme/blockchain-with-go/internal/domain")
			out, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), string(out))

			deps := strings.Split(string(out), "\n")
			for _, dep := range deps {
				dep = strings.TrimSpace(dep)
				if dep == "" {
					continue
				}
				Expect(dep).NotTo(ContainSubstring("internal/storage"), "internal/domain must not depend on storage")
				Expect(dep).NotTo(ContainSubstring("internal/chain"), "internal/domain must not depend on chain")
				Expect(dep).NotTo(ContainSubstring("internal/server"), "internal/domain must not depend on server")
				Expect(dep).NotTo(Equal("net/http"), "internal/domain must not depend on net/http")
				Expect(dep).NotTo(Equal("os/signal"), "internal/domain must not depend on os/signal")
			}

			// Verify pkg/wallet is present in deps
			hasWallet := false
			for _, dep := range deps {
				if strings.Contains(dep, "blockchain-with-go/pkg/wallet") {
					hasWallet = true
					break
				}
			}
			Expect(hasWallet).To(BeTrue(), "internal/domain must depend on pkg/wallet")
		})
	})
})
