package poa_test

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/consensustest"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/poa"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

func newWallet(seedByte byte) *wallet.Wallet {
	seed := make([]byte, ed25519.SeedSize)
	seed[0] = seedByte
	w, err := wallet.FromSeed(seed)
	Expect(err).NotTo(HaveOccurred())
	return w
}

func newPoA(signer *wallet.Wallet, others ...*wallet.Wallet) *poa.ProofOfAuthority {
	set := []ed25519.PublicKey{signer.PublicKey()}
	for _, o := range others {
		set = append(set, o.PublicKey())
	}
	p, err := poa.New(set, signer)
	Expect(err).NotTo(HaveOccurred())
	return p
}

var _ = consensustest.Run(func() consensus.Consensus { return newPoA(newWallet(1)) })

var _ = Describe("ProofOfAuthority", func() {
	var (
		alice, bob *wallet.Wallet
		p          *poa.ProofOfAuthority
		prev, b    *domain.Block
	)

	BeforeEach(func() {
		alice, bob = newWallet(1), newWallet(2)
		p = newPoA(alice)

		prev = &domain.Block{Index: 0, Timestamp: time.Date(2021, 8, 15, 14, 30, 45, 0, time.UTC)}
		Expect(p.Seal(context.Background(), nil, prev)).To(Succeed())
		b = &domain.Block{
			Index:     1,
			Timestamp: time.Date(2021, 8, 15, 14, 31, 45, 0, time.UTC),
			PrevHash:  prev.Hash,
			Nonce:     7, Difficulty: 3,
		}
	})

	Describe("New", func() {
		It("rejects an empty validator set", func() {
			_, err := poa.New(nil, alice)
			Expect(err).To(HaveOccurred())
		})

		It("rejects a key of invalid size", func() {
			_, err := poa.New([]ed25519.PublicKey{alice.PublicKey(), {1, 2, 3}}, alice)
			Expect(err).To(HaveOccurred())
		})

		It("rejects a nil signer", func() {
			_, err := poa.New([]ed25519.PublicKey{alice.PublicKey()}, nil)
			Expect(err).To(HaveOccurred())
		})

		It("rejects a signer outside the set", func() {
			_, err := poa.New([]ed25519.PublicKey{bob.PublicKey()}, alice)
			Expect(err).To(HaveOccurred())
		})

		It("accepts a signer that is not the first validator", func() {
			_, err := poa.New([]ed25519.PublicKey{bob.PublicKey(), alice.PublicKey()}, alice)
			Expect(err).NotTo(HaveOccurred())
		})

		It("is not affected by later changes to the caller's keys", func() {
			key := append(ed25519.PublicKey(nil), alice.PublicKey()...)
			p, err := poa.New([]ed25519.PublicKey{key}, alice)
			Expect(err).NotTo(HaveOccurred())
			key[0] ^= 0xff

			Expect(p.Seal(context.Background(), nil, b)).To(Succeed())
			Expect(p.Verify([]*domain.Block{prev}, b)).To(Succeed())
		})
	})

	Describe("Seal", func() {
		It("fills the consensus fields", func() {
			Expect(p.Seal(context.Background(), nil, b)).To(Succeed())

			Expect(b.Nonce).To(BeZero())
			Expect(b.Difficulty).To(BeZero())
			Expect(b.Validator).To(Equal(hex.EncodeToString(alice.PublicKey())))
			Expect(b.Hash).To(Equal(b.CalculateHash()))

			sig, err := hex.DecodeString(b.Signature)
			Expect(err).NotTo(HaveOccurred())
			hash, err := hex.DecodeString(b.Hash)
			Expect(err).NotTo(HaveOccurred())
			Expect(wallet.Verify(alice.PublicKey(), hash, sig)).To(BeTrue())
		})

		It("returns the context error and leaves the block untouched", func() {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			Expect(p.Seal(ctx, nil, b)).To(MatchError(context.Canceled))
			Expect(b.Validator).To(BeEmpty())
			Expect(b.Signature).To(BeEmpty())
		})

		It("treats a nil context as background", func() {
			Expect(p.Seal(nil, nil, b)).To(Succeed()) //nolint:staticcheck
		})
	})

	Describe("Verify", func() {
		sealed := func() *domain.Block {
			Expect(p.Seal(context.Background(), nil, b)).To(Succeed())
			return b
		}

		// resign recomputes hash and signature after a field edit so only the
		// edited seal field is wrong.
		resign := func(w *wallet.Wallet) {
			b.Hash = b.CalculateHash()
			raw, _ := hex.DecodeString(b.Hash)
			b.Signature = hex.EncodeToString(w.Sign(raw))
		}

		It("accepts blocks sealed by any validator in the set", func() {
			multi := newPoA(alice, bob)
			other := newPoA(bob, alice)
			Expect(other.Seal(context.Background(), nil, b)).To(Succeed())
			Expect(multi.Verify([]*domain.Block{prev}, b)).To(Succeed())
		})

		DescribeTable("rejects a single-field mutation",
			func(mutate func(b *domain.Block), want error) {
				sealed()
				mutate(b)
				Expect(p.Verify([]*domain.Block{prev}, b)).To(MatchError(want))
			},
			Entry("Index", func(b *domain.Block) { b.Index++ }, consensus.ErrInvalidIndex),
			Entry("PrevHash", func(b *domain.Block) { b.PrevHash = "x" }, consensus.ErrInvalidPrevHash),
			Entry("Timestamp", func(b *domain.Block) { b.Timestamp = b.Timestamp.Add(time.Second) }, consensus.ErrInvalidHash),
			Entry("Transactions", func(b *domain.Block) { b.Transactions = []domain.Transaction{{ID: "x"}} }, consensus.ErrInvalidHash),
			Entry("Validator", func(b *domain.Block) { b.Validator = hex.EncodeToString(make([]byte, 32)) }, consensus.ErrInvalidHash),
			Entry("Hash", func(b *domain.Block) { b.Hash = "x" }, consensus.ErrInvalidHash),
			Entry("Signature", func(b *domain.Block) { b.Signature = hex.EncodeToString(make([]byte, 64)) }, consensus.ErrInvalidSignature),
		)

		DescribeTable("rejects a bad seal",
			func(mutate func(b *domain.Block)) {
				sealed()
				mutate(b)
				resign(alice)
				Expect(p.Verify([]*domain.Block{prev}, b)).To(MatchError(consensus.ErrInvalidSeal))
			},
			Entry("Nonce != 0", func(b *domain.Block) { b.Nonce = 1 }),
			Entry("Difficulty != 0", func(b *domain.Block) { b.Difficulty = 1 }),
			Entry("empty Validator", func(b *domain.Block) { b.Validator = "" }),
			Entry("non-hex Validator", func(b *domain.Block) { b.Validator = "zz" }),
			Entry("short Validator", func(b *domain.Block) { b.Validator = "abcd" }),
			Entry("long Validator", func(b *domain.Block) { b.Validator = hex.EncodeToString(make([]byte, 33)) }),
		)

		DescribeTable("rejects a bad signature encoding",
			func(signature string) {
				sealed()
				b.Signature = signature
				Expect(p.Verify([]*domain.Block{prev}, b)).To(MatchError(consensus.ErrInvalidSignature))
			},
			Entry("empty", ""),
			Entry("non-hex", "zz"),
			Entry("short", hex.EncodeToString(make([]byte, 63))),
			Entry("long", hex.EncodeToString(make([]byte, 65))),
		)

		It("rejects a non-canonical (uppercase) signature", func() {
			sealed()
			b.Signature = strings.ToUpper(b.Signature)
			Expect(p.Verify([]*domain.Block{prev}, b)).To(MatchError(consensus.ErrInvalidSignature))
		})

		It("rejects an uppercase validator through the hash", func() {
			sealed()
			b.Validator = strings.ToUpper(b.Validator)
			Expect(p.Verify([]*domain.Block{prev}, b)).To(MatchError(consensus.ErrInvalidHash))
		})

		It("rejects a signature by a different key than the validator", func() {
			sealed()
			resign(bob)
			Expect(p.Verify([]*domain.Block{prev}, b)).To(MatchError(consensus.ErrInvalidSignature))
		})

		It("rejects an outsider with a valid signature as unknown validator", func() {
			outsider := newPoA(bob)
			Expect(outsider.Seal(context.Background(), nil, b)).To(Succeed())
			Expect(p.Verify([]*domain.Block{prev}, b)).To(MatchError(consensus.ErrUnknownValidator))
		})

		It("checks unknown validator before the signature", func() {
			outsider := newPoA(bob)
			Expect(outsider.Seal(context.Background(), nil, b)).To(Succeed())
			b.Signature = "zz"
			Expect(p.Verify([]*domain.Block{prev}, b)).To(MatchError(consensus.ErrUnknownValidator))
		})

		It("rejects PoW blocks and PoW rejects PoA blocks", func() {
			work := pow.New(1)
			Expect(work.Seal(context.Background(), nil, b)).To(Succeed())
			Expect(p.Verify([]*domain.Block{prev}, b)).To(MatchError(consensus.ErrInvalidSeal))

			powPrev := &domain.Block{Index: 0}
			Expect(work.Seal(context.Background(), nil, powPrev)).To(Succeed())
			b2 := &domain.Block{Index: 1, PrevHash: powPrev.Hash}
			Expect(p.Seal(context.Background(), nil, b2)).To(Succeed())
			Expect(work.Verify([]*domain.Block{powPrev}, b2)).To(MatchError(consensus.ErrInvalidSeal))
		})
	})
})
