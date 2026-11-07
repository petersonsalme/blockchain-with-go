package domain_test

import (
	"errors"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

var _ = Describe("Block rules", func() {
	Describe("MaxBlockBytes", func() {
		It("is the 1 MiB consensus constant", func() {
			Expect(domain.MaxBlockBytes).To(Equal(1048576))
		})
	})

	Describe("ValidateSize", func() {
		var (
			b    *domain.Block
			size int
		)

		BeforeEach(func() {
			b = &domain.Block{Index: 1, Timestamp: time.Now(), Transactions: []domain.Transaction{}}

			var err error

			size, err = b.Size()
			Expect(err).NotTo(HaveOccurred())
		})

		It("accepts a block exactly at the limit", func() {
			Expect(domain.ValidateSize(b, size)).To(Succeed())
		})

		It("accepts a block one byte under the limit", func() {
			Expect(domain.ValidateSize(b, size+1)).To(Succeed())
		})

		It("rejects a block one byte over the limit", func() {
			err := domain.ValidateSize(b, size-1)

			var ve *domain.ValidationError
			Expect(errors.As(err, &ve)).To(BeTrue())
			Expect(ve.Code).To(Equal(domain.CodeBlockTooLarge))
			Expect(ve.Details).To(HaveKeyWithValue("size", size))
			Expect(ve.Details).To(HaveKeyWithValue("max", size-1))
		})
	})

	Describe("MedianTimePast", func() {
		base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		blocks := func(secs ...int) []*domain.Block {
			out := make([]*domain.Block, len(secs))
			for i, s := range secs {
				out[i] = &domain.Block{Timestamp: base.Add(time.Duration(s) * time.Second)}
			}

			return out
		}

		It("pins the span to 11", func() {
			Expect(domain.MedianTimeSpan).To(Equal(11))
		})

		It("is the zero time for an empty history", func() {
			Expect(domain.MedianTimePast(nil).IsZero()).To(BeTrue())
		})

		It("takes index len/2 of the sorted timestamps, odd window", func() {
			Expect(domain.MedianTimePast(blocks(50, 10, 30, 20, 40))).To(Equal(base.Add(30 * time.Second)))
		})

		It("takes index len/2 of the sorted timestamps, even window", func() {
			Expect(domain.MedianTimePast(blocks(40, 10, 30, 20))).To(Equal(base.Add(30 * time.Second)))
		})

		It("covers only the last 11 blocks", func() {
			// 12 blocks: the first (1000) is outside the window; the window is 1..11.
			h := blocks(1000, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1)
			Expect(domain.MedianTimePast(h)).To(Equal(base.Add(6 * time.Second)))
		})

		It("ignores old outliers at the start of a long history", func() {
			h := blocks(5000, 5000, 5000, 5000, 5000, 5000, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11)
			Expect(domain.MedianTimePast(h)).To(Equal(base.Add(6 * time.Second)))
		})

		It("is not moved by up to 5 outliers among the last 11", func() {
			h := blocks(1, 2, 3, 4, 5, 6, 9000, 9000, 9000, 9000, 9000)
			Expect(domain.MedianTimePast(h)).To(Equal(base.Add(6 * time.Second)))
		})
	})

	Describe("ValidateTimestamp", func() {
		now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
		// Median of the three timestamps is now-1h.
		history := []*domain.Block{
			{Timestamp: now.Add(-2 * time.Hour)},
			{Timestamp: now.Add(-time.Hour)},
			{Timestamp: now.Add(-30 * time.Minute)},
		}
		median := now.Add(-time.Hour)

		check := func(ts time.Time) error {
			return domain.ValidateTimestamp(&domain.Block{Timestamp: ts}, history, now)
		}

		It("rejects a timestamp equal to the median time past", func() {
			var ve *domain.ValidationError
			Expect(errors.As(check(median), &ve)).To(BeTrue())
			Expect(ve.Code).To(Equal(domain.CodeTimestampNotAfterMedianTimePast))
			Expect(ve.Details).To(HaveKeyWithValue("timestamp", median.Format(time.RFC3339Nano)))
			Expect(ve.Details).To(HaveKeyWithValue("median_time_past", median.Format(time.RFC3339Nano)))
		})

		It("rejects a timestamp before the median time past", func() {
			var ve *domain.ValidationError
			Expect(errors.As(check(median.Add(-time.Hour)), &ve)).To(BeTrue())
			Expect(ve.Code).To(Equal(domain.CodeTimestampNotAfterMedianTimePast))
		})

		It("accepts median+1ns, even when earlier than the parent", func() {
			Expect(check(median.Add(time.Nanosecond))).To(Succeed())
		})

		It("accepts now+1h and pins MaxFutureDrift to 2h", func() {
			Expect(domain.MaxFutureDrift).To(Equal(2 * time.Hour))
			Expect(check(now.Add(time.Hour))).To(Succeed())
		})

		It("accepts exactly now+2h", func() {
			Expect(check(now.Add(domain.MaxFutureDrift))).To(Succeed())
		})

		It("rejects now+2h+1ns", func() {
			var ve *domain.ValidationError
			Expect(errors.As(check(now.Add(domain.MaxFutureDrift+time.Nanosecond)), &ve)).To(BeTrue())
			Expect(ve.Code).To(Equal(domain.CodeTimestampTooFarInTheFuture))
		})
	})

	Describe("ValidateBlockRules", func() {
		// paddedBlock returns a valid block (merkle root, timestamp) whose JSON size is size.
		paddedBlock := func(now time.Time, size int) *domain.Block {
			tx := domain.Transaction{Timestamp: now, Outputs: []domain.TxOut{{Amount: 1}}}
			b := &domain.Block{Timestamp: now, Transactions: []domain.Transaction{tx}}
			b.MerkleRoot = domain.MerkleRoot(b.Transactions)

			base, err := b.Size()
			Expect(err).NotTo(HaveOccurred())

			b.Transactions[0].Outputs[0].Address = strings.Repeat("a", size-base)
			b.MerkleRoot = domain.MerkleRoot(b.Transactions)

			got, err := b.Size()
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(size))

			return b
		}

		It("accepts a block of exactly MaxBlockBytes and rejects one byte more", func() {
			now := time.Now()
			parent := []*domain.Block{{Timestamp: now.Add(-time.Hour)}}

			Expect(domain.ValidateBlockRules(paddedBlock(now, domain.MaxBlockBytes), parent, now)).To(Succeed())

			var ve *domain.ValidationError

			err := domain.ValidateBlockRules(paddedBlock(now, domain.MaxBlockBytes+1), parent, now)
			Expect(errors.As(err, &ve)).To(BeTrue())
			Expect(ve.Code).To(Equal(domain.CodeBlockTooLarge))
			Expect(ve.Details).To(HaveKeyWithValue("max", domain.MaxBlockBytes))
		})

		It("gives the same result whatever MAX_BLOCK_BYTES holds (ADR-0032)", func() {
			GinkgoT().Setenv("MAX_BLOCK_BYTES", "512")

			now := time.Now()
			parent := []*domain.Block{{Timestamp: now.Add(-time.Hour)}}

			Expect(domain.ValidateBlockRules(paddedBlock(now, 4096), parent, now)).To(Succeed())

			var ve *domain.ValidationError

			err := domain.ValidateBlockRules(paddedBlock(now, domain.MaxBlockBytes+1), parent, now)
			Expect(errors.As(err, &ve)).To(BeTrue())
			Expect(ve.Code).To(Equal(domain.CodeBlockTooLarge))
		})
	})

	Describe("ValidateMerkleRoot", func() {
		var b *domain.Block

		BeforeEach(func() {
			b = &domain.Block{Index: 1, Timestamp: time.Now(), Transactions: merkleTxs(3)}
			b.MerkleRoot = domain.MerkleRoot(b.Transactions)
		})

		It("accepts a block whose root matches its transactions", func() {
			Expect(domain.ValidateMerkleRoot(b)).To(Succeed())
		})

		It("accepts an empty block carrying the empty root", func() {
			empty := &domain.Block{MerkleRoot: domain.MerkleRoot(nil)}
			Expect(domain.ValidateMerkleRoot(empty)).To(Succeed())
		})

		It("rejects a missing root, citing expected and got", func() {
			expected := b.MerkleRoot
			b.MerkleRoot = ""

			var ve *domain.ValidationError
			Expect(errors.As(domain.ValidateMerkleRoot(b), &ve)).To(BeTrue())
			Expect(ve.Code).To(Equal("merkle_root_mismatch"))
			Expect(ve.Details).To(HaveKeyWithValue("expected", expected))
			Expect(ve.Details).To(HaveKeyWithValue("got", ""))
		})

		It("rejects a transaction changed after the root was set", func() {
			b.Transactions[1].Outputs[0].Amount++

			var ve *domain.ValidationError
			Expect(errors.As(domain.ValidateMerkleRoot(b), &ve)).To(BeTrue())
			Expect(ve.Code).To(Equal(domain.CodeMerkleRootMismatch))
		})

		It("is applied by ValidateBlockRules after the size check and before the timestamp check", func() {
			now := time.Now()
			parent := []*domain.Block{{Timestamp: now.Add(-time.Hour)}}
			b.Timestamp = now.Add(-time.Hour)
			b.MerkleRoot = "bad"

			var ve *domain.ValidationError
			Expect(errors.As(domain.ValidateBlockRules(b, parent, now), &ve)).To(BeTrue())
			Expect(ve.Code).To(Equal(domain.CodeMerkleRootMismatch))

			b.Transactions[0].Outputs[0].Address = strings.Repeat("a", domain.MaxBlockBytes)
			Expect(errors.As(domain.ValidateBlockRules(b, parent, now), &ve)).To(BeTrue())
			Expect(ve.Code).To(Equal(domain.CodeBlockTooLarge))
		})
	})
})
