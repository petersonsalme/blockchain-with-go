package wallet

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var fixedSeed = bytes.Repeat([]byte{0x01}, ed25519.SeedSize)

var _ = Describe("Wallet", func() {
	Describe("FromSeed", func() {
		It("rejects wrong seed sizes", func() {
			_, err := FromSeed([]byte("short"))
			Expect(err).To(HaveOccurred())
		})

		It("is deterministic with a fixed address vector", func() {
			w1, err := FromSeed(fixedSeed)
			Expect(err).NotTo(HaveOccurred())
			w2, _ := FromSeed(fixedSeed)
			Expect(w1.Address()).To(Equal(w2.Address()))
			Expect(w1.Address()).To(HaveLen(50))
			Expect(ValidateAddress(w1.Address())).To(Succeed())
			Expect(w1.Address()).To(Equal("0034750f98bd59fcfc946da45aaabe933be154a4b5846797f8"))
		})
	})

	Describe("Sign/Verify", func() {
		It("verifies valid signatures and rejects tampering", func() {
			w, _ := New()
			other, _ := New()
			msg := []byte("hello")
			sig := w.Sign(msg)
			Expect(Verify(w.PublicKey(), msg, sig)).To(BeTrue())
			Expect(Verify(w.PublicKey(), []byte("hellO"), sig)).To(BeFalse())
			bad := append([]byte(nil), sig...)
			bad[0] ^= 0xff
			Expect(Verify(w.PublicKey(), msg, bad)).To(BeFalse())
			Expect(Verify(other.PublicKey(), msg, sig)).To(BeFalse())
		})

		It("returns false for invalid key sizes without panicking", func() {
			Expect(Verify(ed25519.PublicKey{1, 2, 3}, []byte("m"), []byte("s"))).To(BeFalse())
			Expect(Verify(nil, nil, nil)).To(BeFalse())
		})
	})
})

var _ = Describe("ValidateAddress", func() {
	var addr string
	BeforeEach(func() {
		w, _ := FromSeed(fixedSeed)
		addr = w.Address()
	})

	It("detects any single-character change", func() {
		for i := range addr {
			b := []byte(addr)
			if b[i] == '0' {
				b[i] = '1'
			} else {
				b[i] = '0'
			}
			Expect(ValidateAddress(string(b))).To(HaveOccurred(), "index %d", i)
		}
	})

	It("reports a checksum error for a changed hash char", func() {
		b := []byte(addr)
		b[10] = map[byte]byte{'0': '1'}[b[10]]
		if b[10] == 0 {
			b[10] = '0'
		}
		Expect(ValidateAddress(string(b))).To(MatchError(ErrInvalidAddressChecksum))
	})

	It("rejects wrong length", func() {
		Expect(ValidateAddress(addr[:48])).To(MatchError(ErrInvalidAddressLength))
		Expect(ValidateAddress("")).To(MatchError(ErrInvalidAddressLength))
	})

	It("rejects non-hex", func() {
		Expect(ValidateAddress("zz" + addr[2:])).To(MatchError(ErrInvalidAddressEncoding))
	})

	It("rejects uppercase", func() {
		upper := []byte(addr)
		for i, c := range upper {
			if c >= 'a' && c <= 'f' {
				upper[i] = c - 32
			}
		}
		Expect(string(upper)).NotTo(Equal(addr))
		Expect(ValidateAddress(string(upper))).To(MatchError(ErrInvalidAddressEncoding))
	})

	It("rejects other versions even with valid checksum", func() {
		raw, _ := hex.DecodeString(addr)
		payload := raw[:21]
		payload[0] = 0x01
		bad := hex.EncodeToString(append(payload, checksum(payload)...))
		Expect(ValidateAddress(bad)).To(MatchError(ErrInvalidAddressVersion))
	})
})
