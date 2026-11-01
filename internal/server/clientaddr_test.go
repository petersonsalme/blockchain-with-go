package server

import (
	"net/netip"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ParseTrustedProxies", func() {
	It("returns nil for blank input", func() {
		for _, raw := range []string{"", " ", " , "} {
			prefixes, err := ParseTrustedProxies(raw)
			Expect(err).NotTo(HaveOccurred())
			Expect(prefixes).To(BeNil())
		}
	})

	It("parses CIDRs and masks them", func() {
		prefixes, err := ParseTrustedProxies("10.59.0.0/24, 2001:db8::/32")
		Expect(err).NotTo(HaveOccurred())
		Expect(prefixes).To(Equal([]netip.Prefix{
			netip.MustParsePrefix("10.59.0.0/24"),
			netip.MustParsePrefix("2001:db8::/32"),
		}))

		prefixes, err = ParseTrustedProxies("10.59.0.7/24")
		Expect(err).NotTo(HaveOccurred())
		Expect(prefixes).To(Equal([]netip.Prefix{netip.MustParsePrefix("10.59.0.0/24")}))
	})

	It("turns a bare IP into a single-address prefix", func() {
		prefixes, err := ParseTrustedProxies("10.59.0.10")
		Expect(err).NotTo(HaveOccurred())
		Expect(prefixes).To(Equal([]netip.Prefix{netip.MustParsePrefix("10.59.0.10/32")}))

		prefixes, err = ParseTrustedProxies("::1")
		Expect(err).NotTo(HaveOccurred())
		Expect(prefixes).To(Equal([]netip.Prefix{netip.MustParsePrefix("::1/128")}))

		prefixes, err = ParseTrustedProxies("::ffff:10.0.0.1")
		Expect(err).NotTo(HaveOccurred())
		Expect(prefixes).To(Equal([]netip.Prefix{netip.MustParsePrefix("10.0.0.1/32")}))
	})

	It("rejects an invalid entry", func() {
		for _, tc := range []struct{ raw, bad string }{
			{"10.0.0.0/24,not-a-cidr", "not-a-cidr"},
			{"10.0.0.0/33", "10.0.0.0/33"},
		} {
			prefixes, err := ParseTrustedProxies(tc.raw)
			Expect(prefixes).To(BeNil())
			Expect(err).To(MatchError(ErrInvalidTrustedProxy))
			Expect(err.Error()).To(ContainSubstring(tc.bad))
		}
	})
})
