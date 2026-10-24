package domain_test

import (
	"math"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

var _ = Describe("Subsidy", func() {
	DescribeTable("follows the halving schedule",
		func(height int, want uint64) {
			Expect(domain.Subsidy(height)).To(Equal(want))
		},
		Entry("genesis", 0, uint64(5_000_000_000)),
		Entry("last of era 0", 209_999, uint64(5_000_000_000)),
		Entry("first halving", 210_000, uint64(2_500_000_000)),
		Entry("last of era 1", 419_999, uint64(2_500_000_000)),
		Entry("second halving", 420_000, uint64(1_250_000_000)),
		Entry("32nd halving", 32*210_000, uint64(1)),
		Entry("33rd halving", 33*210_000, uint64(0)),
		Entry("64th halving", 64*210_000, uint64(0)),
		Entry("MaxInt", math.MaxInt, uint64(0)),
		Entry("negative", -1, uint64(0)),
		Entry("MinInt", math.MinInt, uint64(0)),
	)

	It("halves per interval and never grows", func() {
		for h := 0; h < 70*domain.HalvingInterval; h += 9973 {
			Expect(domain.Subsidy(h + domain.HalvingInterval)).To(Equal(domain.Subsidy(h) / 2))
			Expect(domain.Subsidy(h + 1)).To(BeNumerically("<=", domain.Subsidy(h)))
		}
	})
})
