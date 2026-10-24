package wallet

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type bip39Vectors struct {
	English [][4]string `json:"english"`
}

var _ = Describe("Mnemonic", func() {
	var vectors bip39Vectors

	BeforeEach(func() {
		data, err := os.ReadFile("testdata/bip39_vectors.json")
		Expect(err).NotTo(HaveOccurred())
		Expect(json.Unmarshal(data, &vectors)).To(Succeed())
	})

	It("matches the official BIP-39 vectors", func() {
		Expect(vectors.English).To(HaveLen(24))
		for _, v := range vectors.English {
			entropy, err := hex.DecodeString(v[0])
			Expect(err).NotTo(HaveOccurred())

			mnemonic, err := MnemonicFromEntropy(entropy)
			Expect(err).NotTo(HaveOccurred())
			Expect(mnemonic).To(Equal(v[1]))

			Expect(ValidateMnemonic(v[1])).To(Succeed())

			recoveredEntropy, err := mnemonicToEntropy(v[1])
			Expect(err).NotTo(HaveOccurred())
			Expect(recoveredEntropy).To(Equal(entropy))
		}
	})

	It("embeds the 2048-word sorted list", func() {
		Expect(englishWords).To(HaveLen(2048))
		Expect(slices.IsSorted(englishWords)).To(BeTrue())
		Expect(englishWords[0]).To(Equal("abandon"))
		Expect(englishWords[2047]).To(Equal("zoo"))
	})

	It("MnemonicFromEntropy rejects 15, 17 and 33 bytes", func() {
		for _, size := range []int{15, 17, 33} {
			_, err := MnemonicFromEntropy(make([]byte, size))
			Expect(errors.Is(err, ErrInvalidEntropy)).To(BeTrue())
		}
	})

	It("NewMnemonic returns valid 12 and 24 word mnemonics", func() {
		for _, n := range []int{12, 24} {
			m1, err := NewMnemonic(n)
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.Fields(m1)).To(HaveLen(n))
			Expect(ValidateMnemonic(m1)).To(Succeed())

			m2, err := NewMnemonic(n)
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.Fields(m2)).To(HaveLen(n))
			Expect(ValidateMnemonic(m2)).To(Succeed())

			Expect(m1).NotTo(Equal(m2))
		}
	})

	It("NewMnemonic rejects 15 and 0 words", func() {
		for _, n := range []int{15, 0} {
			_, err := NewMnemonic(n)
			Expect(errors.Is(err, ErrInvalidEntropy)).To(BeTrue())
		}
	})

	It("ValidateMnemonic rejects 11 and 13 words", func() {
		v0 := vectors.English[0][1]
		ws := strings.Fields(v0)
		short := strings.Join(ws[:11], " ")
		long := v0 + " abandon"

		Expect(errors.Is(ValidateMnemonic(short), ErrInvalidMnemonic)).To(BeTrue())
		Expect(errors.Is(ValidateMnemonic(long), ErrInvalidMnemonic)).To(BeTrue())
	})

	It("ValidateMnemonic rejects an unknown word", func() {
		v0 := vectors.English[0][1]
		ws := strings.Fields(v0)
		ws[len(ws)-1] = "zzzz"
		unknown := strings.Join(ws, " ")

		Expect(errors.Is(ValidateMnemonic(unknown), ErrInvalidMnemonic)).To(BeTrue())
	})

	It("ValidateMnemonic rejects a bad checksum", func() {
		badChecksum := strings.Repeat("abandon ", 11) + "abandon"
		Expect(errors.Is(ValidateMnemonic(badChecksum), ErrInvalidMnemonic)).To(BeTrue())
	})

	It("derives the official BIP-39 seeds", func() {
		Expect(vectors.English).To(HaveLen(24))
		for _, v := range vectors.English {
			seed, err := MnemonicToSeed(v[1], "TREZOR")
			Expect(err).NotTo(HaveOccurred())
			Expect(hex.EncodeToString(seed)).To(Equal(v[2]))
		}
	})

	It("MnemonicToSeed rejects an invalid mnemonic", func() {
		invalidMnemonic := strings.TrimSpace(strings.Repeat("abandon ", 12))
		seed, err := MnemonicToSeed(invalidMnemonic, "TREZOR")
		Expect(errors.Is(err, ErrInvalidMnemonic)).To(BeTrue())
		Expect(seed).To(BeNil())
	})

	It("MnemonicToSeed ignores extra whitespace", func() {
		v0Mnemonic := vectors.English[0][1]
		spacedMnemonic := "\t" + strings.Join(strings.Fields(v0Mnemonic), "  ") + "\n"
		seedFromSpaced, err := MnemonicToSeed(spacedMnemonic, "TREZOR")
		Expect(err).NotTo(HaveOccurred())

		seedExpected, err := MnemonicToSeed(v0Mnemonic, "TREZOR")
		Expect(err).NotTo(HaveOccurred())
		Expect(seedFromSpaced).To(Equal(seedExpected))
	})

	It("MnemonicToSeed gives the same seed for NFC and NFD passphrases", func() {
		v0Mnemonic := vectors.English[0][1]
		seedNFC, err := MnemonicToSeed(v0Mnemonic, "caf\u00e9")
		Expect(err).NotTo(HaveOccurred())

		seedNFD, err := MnemonicToSeed(v0Mnemonic, "cafe\u0301")
		Expect(err).NotTo(HaveOccurred())

		seedPlain, err := MnemonicToSeed(v0Mnemonic, "cafe")
		Expect(err).NotTo(HaveOccurred())

		Expect(seedNFC).To(Equal(seedNFD))
		Expect(seedNFC).NotTo(Equal(seedPlain))
	})
})
