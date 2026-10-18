package wallet

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Keystore", func() {
	var (
		dir  string
		path string
		w    *Wallet
	)
	const pw = "correct horse"

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		path = filepath.Join(dir, "ks.json")
		var err error
		w, err = New()
		Expect(err).NotTo(HaveOccurred())
		Expect(w.Save(path, pw)).To(Succeed())
	})

	mutate := func(fn func(m map[string]any)) string {
		raw, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		var m map[string]any
		Expect(json.Unmarshal(raw, &m)).To(Succeed())
		fn(m)
		out, _ := json.Marshal(m)
		p := filepath.Join(dir, "mut.json")
		Expect(os.WriteFile(p, out, 0o600)).To(Succeed())
		return p
	}
	crypt := func(m map[string]any) map[string]any { return m["crypto"].(map[string]any) }

	It("round-trips", func() {
		l, err := Load(path, pw)
		Expect(err).NotTo(HaveOccurred())
		Expect(l.Address()).To(Equal(w.Address()))
		Expect(l.PublicKey()).To(Equal(w.PublicKey()))
		Expect(Verify(w.PublicKey(), []byte("x"), l.Sign([]byte("x")))).To(BeTrue())
	})

	It("saves with 0600 permissions", func() {
		st, err := os.Stat(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Mode().Perm()).To(Equal(os.FileMode(0o600)))
	})

	It("leaves no temp files behind", func() {
		entries, _ := os.ReadDir(dir)
		Expect(entries).To(HaveLen(1))
	})

	It("rejects empty password on save", func() {
		Expect(w.Save(filepath.Join(dir, "x.json"), "")).To(MatchError(ErrEmptyPassword))
	})

	It("fails with the wrong password", func() {
		_, err := Load(path, "nope")
		Expect(err).To(MatchError(ErrWrongPassword))
	})

	It("uses fresh salt, nonce and ciphertext per save", func() {
		p2 := filepath.Join(dir, "ks2.json")
		Expect(w.Save(p2, pw)).To(Succeed())
		read := func(p string) keystoreFile {
			raw, _ := os.ReadFile(p)
			var k keystoreFile
			Expect(json.Unmarshal(raw, &k)).To(Succeed())
			return k
		}
		a, b := read(path), read(p2)
		Expect(a.Crypto.KDFParams.Salt).NotTo(Equal(b.Crypto.KDFParams.Salt))
		Expect(a.Crypto.Nonce).NotTo(Equal(b.Crypto.Nonce))
		Expect(a.Crypto.Ciphertxt).NotTo(Equal(b.Crypto.Ciphertxt))
		l, err := Load(p2, pw)
		Expect(err).NotTo(HaveOccurred())
		Expect(l.Address()).To(Equal(w.Address()))
	})

	flip := func(s string) string {
		if s[0] == '0' {
			return "1" + s[1:]
		}
		return "0" + s[1:]
	}

	DescribeTable("tampered files",
		func(fn func(m map[string]any), wantWrongPw bool, wantInvalid bool) {
			p := mutate(fn)
			var err error
			Expect(func() { _, err = Load(p, pw) }).NotTo(Panic())
			Expect(err).To(HaveOccurred())
			if wantWrongPw {
				Expect(err).To(MatchError(ErrWrongPassword))
			}
			if wantInvalid {
				Expect(err).To(MatchError(ErrInvalidKeystore))
			}
		},
		Entry("ciphertext", func(m map[string]any) { c := crypt(m); c["ciphertext"] = flip(c["ciphertext"].(string)) }, true, false),
		Entry("nonce", func(m map[string]any) { c := crypt(m); c["nonce"] = flip(c["nonce"].(string)) }, true, false),
		Entry("salt", func(m map[string]any) {
			k := crypt(m)["kdfparams"].(map[string]any)
			k["salt"] = flip(k["salt"].(string))
		}, true, false),
		Entry("address", func(m map[string]any) { m["address"] = flip(m["address"].(string)) }, true, false),
		Entry("version", func(m map[string]any) { m["version"] = 2 }, false, true),
		Entry("kdf", func(m map[string]any) { crypt(m)["kdf"] = "pbkdf2" }, false, true),
		Entry("cipher", func(m map[string]any) { crypt(m)["cipher"] = "aes-128-cbc" }, false, true),
		Entry("absurd n", func(m map[string]any) { crypt(m)["kdfparams"].(map[string]any)["n"] = 1 << 30 }, false, true),
		Entry("non power-of-two n", func(m map[string]any) { crypt(m)["kdfparams"].(map[string]any)["n"] = 1000 }, false, true),
		Entry("absurd r", func(m map[string]any) { crypt(m)["kdfparams"].(map[string]any)["r"] = 1 << 20 }, false, true),
		Entry("bad dklen", func(m map[string]any) { crypt(m)["kdfparams"].(map[string]any)["dklen"] = 16 }, false, true),
		Entry("non-hex salt", func(m map[string]any) { crypt(m)["kdfparams"].(map[string]any)["salt"] = "zz" }, false, true),
		Entry("short nonce", func(m map[string]any) { crypt(m)["nonce"] = "00" }, false, true),
		Entry("non-hex ciphertext", func(m map[string]any) { crypt(m)["ciphertext"] = "zz" }, false, true),
	)

	It("rejects a valid-but-different address bound by AAD swap", func() {
		other, _ := New()
		p := mutate(func(m map[string]any) { m["address"] = other.Address() })
		_, err := Load(p, pw)
		Expect(err).To(HaveOccurred())
	})

	It("rejects invalid JSON and missing files", func() {
		bad := filepath.Join(dir, "bad.json")
		Expect(os.WriteFile(bad, []byte("{not json"), 0o600)).To(Succeed())
		_, err := Load(bad, pw)
		Expect(err).To(MatchError(ErrInvalidKeystore))
		_, err = Load(filepath.Join(dir, "missing.json"), pw)
		Expect(err).To(HaveOccurred())
		Expect(strings.Contains(err.Error(), "missing.json")).To(BeTrue())
	})

	Context("Load parameter boundaries", func() {
		Describe("N parameter", func() {
			It("accepts N at lower boundary (2)", func() {
				pN2 := filepath.Join(dir, "ks-n2.json")
				origN := scryptN
				scryptN = 2
				defer func() { scryptN = origN }()
				wN2, err := New()
				Expect(err).NotTo(HaveOccurred())
				Expect(wN2.Save(pN2, pw)).To(Succeed())
				loaded, err := Load(pN2, pw)
				Expect(err).NotTo(HaveOccurred())
				Expect(loaded.Address()).To(Equal(wN2.Address()))
			})

			It("rejects N at lower boundary - 1 (1)", func() {
				pMut := mutate(func(m map[string]any) { crypt(m)["kdfparams"].(map[string]any)["n"] = 1 })
				_, err := Load(pMut, pw)
				Expect(err).To(MatchError(ErrInvalidKeystore))
			})

			It("rejects non-power-of-two N (3)", func() {
				pMut := mutate(func(m map[string]any) { crypt(m)["kdfparams"].(map[string]any)["n"] = 3 })
				_, err := Load(pMut, pw)
				Expect(err).To(MatchError(ErrInvalidKeystore))
			})

			It("accepts N at upper boundary (maxScryptN = 1<<20) before KDF", func() {
				pMut := mutate(func(m map[string]any) { crypt(m)["kdfparams"].(map[string]any)["n"] = maxScryptN })
				_, err := Load(pMut, "")
				Expect(err).To(MatchError(ErrEmptyPassword))
			})

			It("rejects N at upper boundary + 1 non-power-of-two (maxScryptN + 1)", func() {
				pMut := mutate(func(m map[string]any) { crypt(m)["kdfparams"].(map[string]any)["n"] = maxScryptN + 1 })
				_, err := Load(pMut, pw)
				Expect(err).To(MatchError(ErrInvalidKeystore))
			})

			It("rejects power-of-two N exceeding maxScryptN (maxScryptN << 1)", func() {
				pMut := mutate(func(m map[string]any) { crypt(m)["kdfparams"].(map[string]any)["n"] = maxScryptN << 1 })
				_, err := Load(pMut, pw)
				Expect(err).To(MatchError(ErrInvalidKeystore))
			})
		})

		Describe("R parameter", func() {
			It("accepts R at lower boundary (1)", func() {
				pR1 := filepath.Join(dir, "ks-r1.json")
				origR := scryptR
				scryptR = 1
				defer func() { scryptR = origR }()
				wR1, err := New()
				Expect(err).NotTo(HaveOccurred())
				Expect(wR1.Save(pR1, pw)).To(Succeed())
				loaded, err := Load(pR1, pw)
				Expect(err).NotTo(HaveOccurred())
				Expect(loaded.Address()).To(Equal(wR1.Address()))
			})

			It("rejects R at lower boundary - 1 (0)", func() {
				pMut := mutate(func(m map[string]any) { crypt(m)["kdfparams"].(map[string]any)["r"] = 0 })
				_, err := Load(pMut, pw)
				Expect(err).To(MatchError(ErrInvalidKeystore))
			})

			It("accepts R at upper boundary (maxScryptR = 32)", func() {
				pR32 := filepath.Join(dir, "ks-r32.json")
				origR := scryptR
				scryptR = maxScryptR
				defer func() { scryptR = origR }()
				wR32, err := New()
				Expect(err).NotTo(HaveOccurred())
				Expect(wR32.Save(pR32, pw)).To(Succeed())
				loaded, err := Load(pR32, pw)
				Expect(err).NotTo(HaveOccurred())
				Expect(loaded.Address()).To(Equal(wR32.Address()))
			})

			It("rejects R at upper boundary + 1 (33)", func() {
				pMut := mutate(func(m map[string]any) { crypt(m)["kdfparams"].(map[string]any)["r"] = maxScryptR + 1 })
				_, err := Load(pMut, pw)
				Expect(err).To(MatchError(ErrInvalidKeystore))
			})
		})

		Describe("P parameter", func() {
			It("accepts P at lower boundary (1)", func() {
				pP1 := filepath.Join(dir, "ks-p1.json")
				origP := scryptP
				scryptP = 1
				defer func() { scryptP = origP }()
				wP1, err := New()
				Expect(err).NotTo(HaveOccurred())
				Expect(wP1.Save(pP1, pw)).To(Succeed())
				loaded, err := Load(pP1, pw)
				Expect(err).NotTo(HaveOccurred())
				Expect(loaded.Address()).To(Equal(wP1.Address()))
			})

			It("rejects P at lower boundary - 1 (0)", func() {
				pMut := mutate(func(m map[string]any) { crypt(m)["kdfparams"].(map[string]any)["p"] = 0 })
				_, err := Load(pMut, pw)
				Expect(err).To(MatchError(ErrInvalidKeystore))
			})

			It("accepts P at upper boundary (maxScryptP = 16)", func() {
				pP16 := filepath.Join(dir, "ks-p16.json")
				origP := scryptP
				scryptP = maxScryptP
				defer func() { scryptP = origP }()
				wP16, err := New()
				Expect(err).NotTo(HaveOccurred())
				Expect(wP16.Save(pP16, pw)).To(Succeed())
				loaded, err := Load(pP16, pw)
				Expect(err).NotTo(HaveOccurred())
				Expect(loaded.Address()).To(Equal(wP16.Address()))
			})

			It("rejects P at upper boundary + 1 (17)", func() {
				pMut := mutate(func(m map[string]any) { crypt(m)["kdfparams"].(map[string]any)["p"] = maxScryptP + 1 })
				_, err := Load(pMut, pw)
				Expect(err).To(MatchError(ErrInvalidKeystore))
			})
		})

		Describe("dkLen parameter", func() {
			It("rejects dkLen at limit - 1 (31)", func() {
				pMut := mutate(func(m map[string]any) { crypt(m)["kdfparams"].(map[string]any)["dklen"] = dkLen - 1 })
				_, err := Load(pMut, pw)
				Expect(err).To(MatchError(ErrInvalidKeystore))
			})

			It("rejects dkLen at limit + 1 (33)", func() {
				pMut := mutate(func(m map[string]any) { crypt(m)["kdfparams"].(map[string]any)["dklen"] = dkLen + 1 })
				_, err := Load(pMut, pw)
				Expect(err).To(MatchError(ErrInvalidKeystore))
			})
		})

		Describe("salt length parameter", func() {
			It("rejects salt length at limit - 1 (31 bytes)", func() {
				pMut := mutate(func(m map[string]any) {
					crypt(m)["kdfparams"].(map[string]any)["salt"] = hex.EncodeToString(make([]byte, saltLen-1))
				})
				_, err := Load(pMut, pw)
				Expect(err).To(MatchError(ErrInvalidKeystore))
			})

			It("rejects salt length at limit + 1 (33 bytes)", func() {
				pMut := mutate(func(m map[string]any) {
					crypt(m)["kdfparams"].(map[string]any)["salt"] = hex.EncodeToString(make([]byte, saltLen+1))
				})
				_, err := Load(pMut, pw)
				Expect(err).To(MatchError(ErrInvalidKeystore))
			})
		})

		Describe("nonce length parameter", func() {
			It("rejects nonce length at limit - 1 (11 bytes)", func() {
				pMut := mutate(func(m map[string]any) {
					crypt(m)["nonce"] = hex.EncodeToString(make([]byte, nonceLen-1))
				})
				_, err := Load(pMut, pw)
				Expect(err).To(MatchError(ErrInvalidKeystore))
			})

			It("rejects nonce length at limit + 1 (13 bytes)", func() {
				pMut := mutate(func(m map[string]any) {
					crypt(m)["nonce"] = hex.EncodeToString(make([]byte, nonceLen+1))
				})
				_, err := Load(pMut, pw)
				Expect(err).To(MatchError(ErrInvalidKeystore))
			})
		})
	})
})
