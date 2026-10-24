package wallet

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("HD derivation", func() {
	It("matches the SLIP-0010 ed25519 vectors", func() {
		type slip10Vector struct {
			seed      string
			path      string
			chainCode string
			privKey   string
			pubKey    string
		}

		vectors := []slip10Vector{
			// Vector 1
			{
				seed:      "000102030405060708090a0b0c0d0e0f",
				path:      "m",
				chainCode: "90046a93de5380a72b5e45010748567d5ea02bbf6522f979e05c0d8d8ca9fffb",
				privKey:   "2b4be7f19ee27bbf30c667b642d5f4aa69fd169872f8fc3059c08ebae2eb19e7",
				pubKey:    "00a4b2856bfec510abab89753fac1ac0e1112364e7d250545963f135f2a33188ed",
			},
			{
				seed:      "000102030405060708090a0b0c0d0e0f",
				path:      "m/0'",
				chainCode: "8b59aa11380b624e81507a27fedda59fea6d0b779a778918a2fd3590e16e9c69",
				privKey:   "68e0fe46dfb67e368c75379acec591dad19df3cde26e63b93a8e704f1dade7a3",
				pubKey:    "008c8a13df77a28f3445213a0f432fde644acaa215fc72dcdf300d5efaa85d350c",
			},
			{
				seed:      "000102030405060708090a0b0c0d0e0f",
				path:      "m/0'/1'",
				chainCode: "a320425f77d1b5c2505a6b1b27382b37368ee640e3557c315416801243552f14",
				privKey:   "b1d0bad404bf35da785a64ca1ac54b2617211d2777696fbffaf208f746ae84f2",
				pubKey:    "001932a5270f335bed617d5b935c80aedb1a35bd9fc1e31acafd5372c30f5c1187",
			},
			{
				seed:      "000102030405060708090a0b0c0d0e0f",
				path:      "m/0'/1'/2'",
				chainCode: "2e69929e00b5ab250f49c3fb1c12f252de4fed2c1db88387094a0f8c4c9ccd6c",
				privKey:   "92a5b23c0b8a99e37d07df3fb9966917f5d06e02ddbd909c7e184371463e9fc9",
				pubKey:    "00ae98736566d30ed0e9d2f4486a64bc95740d89c7db33f52121f8ea8f76ff0fc1",
			},
			{
				seed:      "000102030405060708090a0b0c0d0e0f",
				path:      "m/0'/1'/2'/2'",
				chainCode: "8f6d87f93d750e0efccda017d662a1b31a266e4a6f5993b15f5c1f07f74dd5cc",
				privKey:   "30d1dc7e5fc04c31219ab25a27ae00b50f6fd66622f6e9c913253d6511d1e662",
				pubKey:    "008abae2d66361c879b900d204ad2cc4984fa2aa344dd7ddc46007329ac76c429c",
			},
			{
				seed:      "000102030405060708090a0b0c0d0e0f",
				path:      "m/0'/1'/2'/2'/1000000000'",
				chainCode: "68789923a0cac2cd5a29172a475fe9e0fb14cd6adb5ad98a3fa70333e7afa230",
				privKey:   "8f94d394a8e8fd6b1bc2f3f49f5c47e385281d5c17e65324b0f62483e37e8793",
				pubKey:    "003c24da049451555d51a7014a37337aa4e12d41e485abccfa46b47dfb2af54b7a",
			},
			// Vector 2
			{
				seed:      "fffcf9f6f3f0edeae7e4e1dedbd8d5d2cfccc9c6c3c0bdbab7b4b1aeaba8a5a29f9c999693908d8a8784817e7b7875726f6c696663605d5a5754514e4b484542",
				path:      "m",
				chainCode: "ef70a74db9c3a5af931b5fe73ed8e1a53464133654fd55e7a66f8570b8e33c3b",
				privKey:   "171cb88b1b3c1db25add599712e36245d75bc65a1a5c9e18d76f9f2b1eab4012",
				pubKey:    "008fe9693f8fa62a4305a140b9764c5ee01e455963744fe18204b4fb948249308a",
			},
			{
				seed:      "fffcf9f6f3f0edeae7e4e1dedbd8d5d2cfccc9c6c3c0bdbab7b4b1aeaba8a5a29f9c999693908d8a8784817e7b7875726f6c696663605d5a5754514e4b484542",
				path:      "m/0'",
				chainCode: "0b78a3226f915c082bf118f83618a618ab6dec793752624cbeb622acb562862d",
				privKey:   "1559eb2bbec5790b0c65d8693e4d0875b1747f4970ae8b650486ed7470845635",
				pubKey:    "0086fab68dcb57aa196c77c5f264f215a112c22a912c10d123b0d03c3c28ef1037",
			},
			{
				seed:      "fffcf9f6f3f0edeae7e4e1dedbd8d5d2cfccc9c6c3c0bdbab7b4b1aeaba8a5a29f9c999693908d8a8784817e7b7875726f6c696663605d5a5754514e4b484542",
				path:      "m/0'/2147483647'",
				chainCode: "138f0b2551bcafeca6ff2aa88ba8ed0ed8de070841f0c4ef0165df8181eaad7f",
				privKey:   "ea4f5bfe8694d8bb74b7b59404632fd5968b774ed545e810de9c32a4fb4192f4",
				pubKey:    "005ba3b9ac6e90e83effcd25ac4e58a1365a9e35a3d3ae5eb07b9e4d90bcf7506d",
			},
			{
				seed:      "fffcf9f6f3f0edeae7e4e1dedbd8d5d2cfccc9c6c3c0bdbab7b4b1aeaba8a5a29f9c999693908d8a8784817e7b7875726f6c696663605d5a5754514e4b484542",
				path:      "m/0'/2147483647'/1'",
				chainCode: "73bd9fff1cfbde33a1b846c27085f711c0fe2d66fd32e139d3ebc28e5a4a6b90",
				privKey:   "3757c7577170179c7868353ada796c839135b3d30554bbb74a4b1e4a5a58505c",
				pubKey:    "002e66aa57069c86cc18249aecf5cb5a9cebbfd6fadeab056254763874a9352b45",
			},
			{
				seed:      "fffcf9f6f3f0edeae7e4e1dedbd8d5d2cfccc9c6c3c0bdbab7b4b1aeaba8a5a29f9c999693908d8a8784817e7b7875726f6c696663605d5a5754514e4b484542",
				path:      "m/0'/2147483647'/1'/2147483646'",
				chainCode: "0902fe8a29f9140480a00ef244bd183e8a13288e4412d8389d140aac1794825a",
				privKey:   "5837736c89570de861ebc173b1086da4f505d4adb387c6a1b1342d5e4ac9ec72",
				pubKey:    "00e33c0f7d81d843c572275f287498e8d408654fdf0d1e065b84e2e6f157aab09b",
			},
			{
				seed:      "fffcf9f6f3f0edeae7e4e1dedbd8d5d2cfccc9c6c3c0bdbab7b4b1aeaba8a5a29f9c999693908d8a8784817e7b7875726f6c696663605d5a5754514e4b484542",
				path:      "m/0'/2147483647'/1'/2147483646'/2'",
				chainCode: "5d70af781f3a37b829f0d060924d5e960bdc02e85423494afc0b1a41bbe196d4",
				privKey:   "551d333177df541ad876a60ea71f00447931c0a9da16f227c11ea080d7391b8d",
				pubKey:    "0047150c75db263559a70d5778bf36abbab30fb061ad69f69ece61a72b0cfa4fc0",
			},
		}

		for _, v := range vectors {
			seedBytes, err := hex.DecodeString(v.seed)
			Expect(err).NotTo(HaveOccurred())

			key, chainCode, err := deriveSLIP10(seedBytes, v.path)
			Expect(err).NotTo(HaveOccurred(), "path %s", v.path)
			Expect(hex.EncodeToString(chainCode)).To(Equal(v.chainCode), "path %s chainCode", v.path)
			Expect(hex.EncodeToString(key)).To(Equal(v.privKey), "path %s privKey", v.path)

			w, err := DeriveKey(seedBytes, v.path)
			Expect(err).NotTo(HaveOccurred(), "path %s", v.path)
			Expect("00"+hex.EncodeToString(w.PublicKey())).To(Equal(v.pubKey), "path %s pubKey", v.path)
		}
	})

	It("rejects 15 and 65 byte seeds", func() {
		for _, size := range []int{15, 65} {
			w, err := DeriveKey(make([]byte, size), "m")
			Expect(err).To(HaveOccurred())
			Expect(w).To(BeNil())
		}
	})

	It("rejects paths without m and with empty segments", func() {
		for _, path := range []string{"", "0'", "M/0'", "m/", "m//0'"} {
			w, err := DeriveKey(make([]byte, 32), path)
			Expect(errors.Is(err, ErrInvalidPath)).To(BeTrue(), "path %q", path)
			Expect(w).To(BeNil())
		}
	})

	It("rejects the non-hardened path m/0", func() {
		for _, path := range []string{"m/0", "m/0'/1"} {
			w, err := DeriveKey(make([]byte, 32), path)
			Expect(errors.Is(err, ErrInvalidPath)).To(BeTrue(), "path %q", path)
			Expect(w).To(BeNil())
		}
	})

	It("rejects m/2147483648'", func() {
		for _, path := range []string{"m/2147483648'", "m/-1'", "m/x'"} {
			w, err := DeriveKey(make([]byte, 32), path)
			Expect(errors.Is(err, ErrInvalidPath)).To(BeTrue(), "path %q", path)
			Expect(w).To(BeNil())
		}
	})

	It("FromMnemonic is deterministic and matches DeriveKey", func() {
		data, err := os.ReadFile("testdata/bip39_vectors.json")
		Expect(err).NotTo(HaveOccurred())
		var vectors bip39Vectors
		Expect(json.Unmarshal(data, &vectors)).To(Succeed())

		mnemonic := vectors.English[0][1]
		passphrase := "TREZOR"
		path := "m/44'/1'/0'/0'/0'"

		w1, err := FromMnemonic(mnemonic, passphrase, path)
		Expect(err).NotTo(HaveOccurred())
		Expect(w1).NotTo(BeNil())

		w2, err := FromMnemonic(mnemonic, passphrase, path)
		Expect(err).NotTo(HaveOccurred())
		Expect(w2).NotTo(BeNil())

		Expect(w1.Address()).To(Equal(w2.Address()))

		seed, err := MnemonicToSeed(mnemonic, passphrase)
		Expect(err).NotTo(HaveOccurred())
		wExpected, err := DeriveKey(seed, path)
		Expect(err).NotTo(HaveOccurred())
		Expect(w1.Address()).To(Equal(wExpected.Address()))

		wOther, err := FromMnemonic(mnemonic, passphrase, "m/44'/1'/0'/0'/1'")
		Expect(err).NotTo(HaveOccurred())
		Expect(wOther.Address()).NotTo(Equal(w1.Address()))

		msg := []byte("test message")
		sig := w1.Sign(msg)
		Expect(Verify(w1.PublicKey(), msg, sig)).To(BeTrue())
	})

	It("FromMnemonic rejects an invalid mnemonic", func() {
		invalidMnemonic := strings.TrimSpace(strings.Repeat("abandon ", 12))
		w, err := FromMnemonic(invalidMnemonic, "TREZOR", "m/44'/1'/0'/0'/0'")
		Expect(errors.Is(err, ErrInvalidMnemonic)).To(BeTrue())
		Expect(w).To(BeNil())
	})
})
