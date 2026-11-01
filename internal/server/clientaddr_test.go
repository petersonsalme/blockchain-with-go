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

var _ = Describe("clientAddr", func() {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.59.0.0/24")}

	It("ignores X-Forwarded-For when no proxy is trusted", func() {
		Expect(clientAddr("10.59.0.10:4000", []string{"2.2.2.2"}, nil)).To(Equal("10.59.0.10:4000"))
	})

	It("ignores X-Forwarded-For from an untrusted peer", func() {
		Expect(clientAddr("1.1.1.1:4000", []string{"2.2.2.2"}, trusted)).To(Equal("1.1.1.1:4000"))
	})

	It("returns remoteAddr when the header is absent", func() {
		Expect(clientAddr("10.59.0.10:4000", nil, trusted)).To(Equal("10.59.0.10:4000"))
		Expect(clientAddr("10.59.0.10:4000", []string{" "}, trusted)).To(Equal("10.59.0.10:4000"))
	})

	It("returns the rightmost untrusted entry", func() {
		Expect(clientAddr("10.59.0.10:4000", []string{"6.6.6.6, 2.2.2.2"}, trusted)).To(Equal("2.2.2.2"))
	})

	It("skips trusted hops", func() {
		Expect(clientAddr("10.59.0.10:4000", []string{"2.2.2.2, 10.59.0.11"}, trusted)).To(Equal("2.2.2.2"))
	})

	It("returns the leftmost entry when all are trusted", func() {
		Expect(clientAddr("10.59.0.10:4000", []string{"10.59.0.3, 10.59.0.11"}, trusted)).To(Equal("10.59.0.3"))
	})

	It("stops at a malformed entry", func() {
		Expect(clientAddr("10.59.0.10:4000", []string{"2.2.2.2, junk"}, trusted)).To(Equal("10.59.0.10"))
		Expect(clientAddr("10.59.0.10:4000", []string{"junk, 10.59.0.11"}, trusted)).To(Equal("10.59.0.11"))
	})

	It("joins repeated header lines", func() {
		Expect(clientAddr("10.59.0.10:4000", []string{"6.6.6.6", "2.2.2.2"}, trusted)).To(Equal("2.2.2.2"))
	})

	It("unmaps IPv4-mapped addresses", func() {
		Expect(clientAddr("[::ffff:10.59.0.10]:4000", []string{"::ffff:2.2.2.2"}, trusted)).To(Equal("2.2.2.2"))
	})

	It("accepts a peer without a port", func() {
		Expect(clientAddr("10.59.0.10", []string{"2.2.2.2"}, trusted)).To(Equal("2.2.2.2"))
	})
})
