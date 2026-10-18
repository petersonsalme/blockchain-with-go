package models_test

import (
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/models"
)

var _ = Describe("Block rules", func() {
	Describe("MaxBlockBytes", func() {
		It("returns the default when unset", func() {
			GinkgoT().Setenv(models.MaxBlockBytesEnvVar, "")
			Expect(models.MaxBlockBytes()).To(Equal(models.DefaultMaxBlockBytes))
		})

		It("returns the configured value", func() {
			GinkgoT().Setenv(models.MaxBlockBytesEnvVar, "512")
			Expect(models.MaxBlockBytes()).To(Equal(512))
		})

		DescribeTable("falls back to the default for invalid values",
			func(raw string) {
				GinkgoT().Setenv(models.MaxBlockBytesEnvVar, raw)
				Expect(models.MaxBlockBytes()).To(Equal(models.DefaultMaxBlockBytes))
			},
			Entry("non-numeric", "abc"),
			Entry("zero", "0"),
			Entry("negative", "-5"),
		)
	})

	Describe("ValidateSize", func() {
		var b *models.Block
		var size int

		BeforeEach(func() {
			b = &models.Block{Index: 1, Timestamp: time.Now(), Transactions: []models.Transaction{}}
			var err error
			size, err = b.Size()
			Expect(err).NotTo(HaveOccurred())
		})

		It("accepts a block exactly at the limit", func() {
			Expect(models.ValidateSize(b, size)).To(Succeed())
		})

		It("accepts a block one byte under the limit", func() {
			Expect(models.ValidateSize(b, size+1)).To(Succeed())
		})

		It("rejects a block one byte over the limit", func() {
			err := models.ValidateSize(b, size-1)
			var ve *models.ValidationError
			Expect(errors.As(err, &ve)).To(BeTrue())
			Expect(ve.Code).To(Equal(models.CodeBlockTooLarge))
			Expect(ve.Details).To(HaveKeyWithValue("size", size))
			Expect(ve.Details).To(HaveKeyWithValue("max", size-1))
		})
	})

	Describe("ValidateTimestamp", func() {
		now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
		parent := &models.Block{Timestamp: now.Add(-time.Hour)}

		check := func(ts time.Time) error {
			return models.ValidateTimestamp(&models.Block{Timestamp: ts}, parent, now)
		}

		It("accepts a timestamp equal to the parent's", func() {
			Expect(check(parent.Timestamp)).To(Succeed())
		})

		It("rejects a timestamp 1ns before the parent's", func() {
			var ve *models.ValidationError
			Expect(errors.As(check(parent.Timestamp.Add(-time.Nanosecond)), &ve)).To(BeTrue())
			Expect(ve.Code).To(Equal(models.CodeTimestampBeforeParent))
			Expect(ve.Details).To(HaveKey("timestamp"))
		})

		It("accepts now+1h and pins MaxFutureDrift to 2h", func() {
			Expect(models.MaxFutureDrift).To(Equal(2 * time.Hour))
			Expect(check(now.Add(time.Hour))).To(Succeed())
		})

		It("accepts exactly now+2h", func() {
			Expect(check(now.Add(models.MaxFutureDrift))).To(Succeed())
		})

		It("rejects now+2h+1ns", func() {
			var ve *models.ValidationError
			Expect(errors.As(check(now.Add(models.MaxFutureDrift+time.Nanosecond)), &ve)).To(BeTrue())
			Expect(ve.Code).To(Equal(models.CodeTimestampTooFarInTheFuture))
		})
	})

	Describe("ValidateBlockRules", func() {
		It("applies the configured size limit", func() {
			GinkgoT().Setenv(models.MaxBlockBytesEnvVar, "10")
			now := time.Now()
			err := models.ValidateBlockRules(&models.Block{Timestamp: now}, &models.Block{Timestamp: now}, now)
			var ve *models.ValidationError
			Expect(errors.As(err, &ve)).To(BeTrue())
			Expect(ve.Code).To(Equal(models.CodeBlockTooLarge))
		})
	})
})
