package domain_test

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"slices"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// refLeaf computes the ADR-0009 leaf hash independently of the code under test.
func refLeaf(tx domain.Transaction) []byte {
	data := []byte{0x00}
	data = append(data, tx.CalculateHash()...)
	for _, in := range tx.Inputs {
		for _, s := range []string{in.PublicKey, in.Signature} {
			data = binary.BigEndian.AppendUint32(data, uint32(len(s)))
			data = append(data, s...)
		}
	}
	sum := sha256.Sum256(data)
	return sum[:]
}

// refRoot computes the ADR-0009 root: RFC 6962 nodes, odd node promoted unchanged.
func refRoot(txs []domain.Transaction) string {
	if len(txs) == 0 {
		sum := sha256.Sum256(nil)
		return hex.EncodeToString(sum[:])
	}
	level := make([][]byte, len(txs))
	for i, tx := range txs {
		level[i] = refLeaf(tx)
	}
	for len(level) > 1 {
		var next [][]byte
		for i := 0; i < len(level); i += 2 {
			if i+1 == len(level) {
				next = append(next, level[i])
				continue
			}
			sum := sha256.Sum256(slices.Concat([]byte{0x01}, level[i], level[i+1]))
			next = append(next, sum[:])
		}
		level = next
	}
	return hex.EncodeToString(level[0])
}

func merkleTxs(n int) []domain.Transaction {
	base := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	txs := make([]domain.Transaction, n)
	for i := range txs {
		txs[i] = domain.Transaction{
			Inputs: []domain.TxIn{{
				PrevTxID:  fmt.Sprintf("prev-%d", i),
				OutIndex:  uint32(i),
				PublicKey: fmt.Sprintf("pk-%d", i),
				Signature: fmt.Sprintf("sig-%d", i),
			}},
			Outputs:   []domain.TxOut{{Amount: uint64(10 + i), Address: fmt.Sprintf("addr-%d", i)}},
			Timestamp: base.Add(time.Duration(i) * time.Second),
		}
		txs[i].ID = txs[i].CalculateHash()
	}
	return txs
}

// cloneTxs deep-copies txs so a mutation never reaches the original.
func cloneTxs(txs []domain.Transaction) []domain.Transaction {
	out := make([]domain.Transaction, len(txs))
	for i, tx := range txs {
		out[i] = tx
		out[i].Inputs = slices.Clone(tx.Inputs)
		out[i].Outputs = slices.Clone(tx.Outputs)
	}
	return out
}

var _ = Describe("Merkle tree", func() {
	Describe("MerkleRoot", func() {
		It("is SHA-256 of the empty string for zero transactions", func() {
			const empty = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
			Expect(domain.MerkleRoot(nil)).To(Equal(empty))
			Expect(domain.MerkleRoot([]domain.Transaction{})).To(Equal(empty))
		})

		It("is the leaf hash for one transaction", func() {
			txs := merkleTxs(1)
			Expect(domain.MerkleRoot(txs)).To(Equal(hex.EncodeToString(refLeaf(txs[0]))))
			Expect(domain.MerkleLeafHash(&txs[0])).To(Equal(hex.EncodeToString(refLeaf(txs[0]))))
		})

		It("pins the root of two transactions", func() {
			txs := merkleTxs(2)
			sum := sha256.Sum256(slices.Concat([]byte{0x01}, refLeaf(txs[0]), refLeaf(txs[1])))
			Expect(domain.MerkleRoot(txs)).To(Equal(hex.EncodeToString(sum[:])))
		})

		It("promotes the unpaired node unchanged for three transactions", func() {
			txs := merkleTxs(3)
			left := sha256.Sum256(slices.Concat([]byte{0x01}, refLeaf(txs[0]), refLeaf(txs[1])))
			sum := sha256.Sum256(slices.Concat([]byte{0x01}, left[:], refLeaf(txs[2])))
			Expect(domain.MerkleRoot(txs)).To(Equal(hex.EncodeToString(sum[:])))
		})

		It("pins the root of five transactions", func() {
			txs := merkleTxs(5)
			n01 := sha256.Sum256(slices.Concat([]byte{0x01}, refLeaf(txs[0]), refLeaf(txs[1])))
			n23 := sha256.Sum256(slices.Concat([]byte{0x01}, refLeaf(txs[2]), refLeaf(txs[3])))
			n03 := sha256.Sum256(slices.Concat([]byte{0x01}, n01[:], n23[:]))
			sum := sha256.Sum256(slices.Concat([]byte{0x01}, n03[:], refLeaf(txs[4])))
			Expect(domain.MerkleRoot(txs)).To(Equal(hex.EncodeToString(sum[:])))
		})

		It("matches the reference for 0..33 transactions", func() {
			for n := 0; n <= 33; n++ {
				txs := merkleTxs(n)
				Expect(domain.MerkleRoot(txs)).To(Equal(refRoot(txs)), "n=%d", n)
			}
		})

		It("uses the recalculated transaction hash, not the ID field", func() {
			txs := merkleTxs(2)
			want := domain.MerkleRoot(txs)
			txs[0].ID = "forged"
			Expect(domain.MerkleRoot(txs)).To(Equal(want))
		})

		It("does not modify its input", func() {
			for n := 0; n <= 9; n++ {
				txs := merkleTxs(n)
				snapshot := cloneTxs(txs)
				domain.MerkleRoot(txs)
				_, _ = domain.BuildMerkleProof(txs, 0)
				Expect(txs).To(Equal(snapshot))
			}
		})
	})

	Describe("tampering", func() {
		It("changes the root for every kind of alteration, n = 1..9", func() {
			for n := 1; n <= 9; n++ {
				txs := merkleTxs(n)
				root := domain.MerkleRoot(txs)

				for i := range txs {
					c := cloneTxs(txs)
					c[i].Outputs[0].Amount++
					Expect(domain.MerkleRoot(c)).NotTo(Equal(root), "output n=%d i=%d", n, i)

					c = cloneTxs(txs)
					c[i].Inputs[0].PublicKey += "x"
					Expect(domain.MerkleRoot(c)).NotTo(Equal(root), "pubkey n=%d i=%d", n, i)

					c = cloneTxs(txs)
					c[i].Inputs[0].Signature += "x"
					Expect(domain.MerkleRoot(c)).NotTo(Equal(root), "signature n=%d i=%d", n, i)

					c = slices.Delete(cloneTxs(txs), i, i+1)
					Expect(domain.MerkleRoot(c)).NotTo(Equal(root), "remove n=%d i=%d", n, i)

					for j := i + 1; j < n; j++ {
						c = cloneTxs(txs)
						c[i], c[j] = c[j], c[i]
						Expect(domain.MerkleRoot(c)).NotTo(Equal(root), "swap n=%d %d,%d", n, i, j)
					}
				}

				if n%2 == 1 {
					c := append(cloneTxs(txs), txs[n-1])
					Expect(domain.MerkleRoot(c)).NotTo(Equal(root), "duplicate last n=%d", n)
				}
			}
		})

		It("separates the public key from the signature in the leaf", func() {
			a := merkleTxs(1)
			b := cloneTxs(a)
			a[0].Inputs[0].PublicKey, a[0].Inputs[0].Signature = "ab", "c"
			b[0].Inputs[0].PublicKey, b[0].Inputs[0].Signature = "a", "bc"
			Expect(domain.MerkleRoot(a)).NotTo(Equal(domain.MerkleRoot(b)))
		})
	})

	Describe("proofs", func() {
		It("verifies every index for n = 1..33 and rejects forgeries", func() {
			for n := 1; n <= 33; n++ {
				txs := merkleTxs(n)
				root := domain.MerkleRoot(txs)
				otherRoot := domain.MerkleRoot(merkleTxs(n + 1))

				for i := range txs {
					proof, err := domain.BuildMerkleProof(txs, i)
					Expect(err).NotTo(HaveOccurred())
					leaf := domain.MerkleLeafHash(&txs[i])
					Expect(domain.VerifyMerkleProof(leaf, proof.Steps, root)).To(BeTrue(), "n=%d i=%d", n, i)

					Expect(domain.VerifyMerkleProof(leaf, proof.Steps, otherRoot)).To(BeFalse(), "other root n=%d i=%d", n, i)

					for j := range txs {
						if j == i {
							continue
						}
						other := domain.MerkleLeafHash(&txs[j])
						Expect(domain.VerifyMerkleProof(other, proof.Steps, root)).To(BeFalse(), "leaf n=%d i=%d j=%d", n, i, j)
					}

					for s := range proof.Steps {
						flipped := slices.Clone(proof.Steps)
						flipped[s].Left = !flipped[s].Left
						Expect(domain.VerifyMerkleProof(leaf, flipped, root)).To(BeFalse(), "side n=%d i=%d s=%d", n, i, s)

						altered := slices.Clone(proof.Steps)
						altered[s].Hash = otherRoot
						Expect(domain.VerifyMerkleProof(leaf, altered, root)).To(BeFalse(), "sibling n=%d i=%d s=%d", n, i, s)
					}
				}
			}
		})

		It("builds an empty proof for a single transaction", func() {
			txs := merkleTxs(1)
			proof, err := domain.BuildMerkleProof(txs, 0)
			Expect(err).NotTo(HaveOccurred())
			Expect(proof.Steps).To(BeEmpty())
		})

		It("records the side of each sibling", func() {
			txs := merkleTxs(3)
			p0, _ := domain.BuildMerkleProof(txs, 0)
			Expect(p0.Steps).To(HaveLen(2))
			Expect(p0.Steps[0]).To(Equal(domain.MerkleStep{Hash: domain.MerkleLeafHash(&txs[1]), Left: false}))
			Expect(p0.Steps[1].Left).To(BeFalse())

			p1, _ := domain.BuildMerkleProof(txs, 1)
			Expect(p1.Steps[0]).To(Equal(domain.MerkleStep{Hash: domain.MerkleLeafHash(&txs[0]), Left: true}))

			p2, _ := domain.BuildMerkleProof(txs, 2)
			Expect(p2.Steps).To(HaveLen(1))
			Expect(p2.Steps[0].Left).To(BeTrue())
		})

		It("rejects an index outside [0, len)", func() {
			txs := merkleTxs(3)
			for _, i := range []int{-1, 3, 100} {
				_, err := domain.BuildMerkleProof(txs, i)
				Expect(err).To(HaveOccurred(), "i=%d", i)
			}
			_, err := domain.BuildMerkleProof(nil, 0)
			Expect(err).To(HaveOccurred())
		})

		It("rejects malformed hashes", func() {
			txs := merkleTxs(2)
			root := domain.MerkleRoot(txs)
			leaf := domain.MerkleLeafHash(&txs[0])
			proof, _ := domain.BuildMerkleProof(txs, 0)

			Expect(domain.VerifyMerkleProof("zz", proof.Steps, root)).To(BeFalse())
			Expect(domain.VerifyMerkleProof(leaf[:62], proof.Steps, root)).To(BeFalse())
			Expect(domain.VerifyMerkleProof(leaf, []domain.MerkleStep{{Hash: "zz"}}, root)).To(BeFalse())
			Expect(domain.VerifyMerkleProof(leaf, []domain.MerkleStep{{Hash: leaf[:62]}}, root)).To(BeFalse())
			Expect(domain.VerifyMerkleProof(leaf, proof.Steps, "")).To(BeFalse())
			Expect(domain.VerifyMerkleProof("", nil, "")).To(BeFalse())
			Expect(domain.VerifyMerkleProof(leaf[:62], nil, leaf[:62])).To(BeFalse())
		})

		It("accepts the leaf itself as root with no steps", func() {
			txs := merkleTxs(1)
			leaf := domain.MerkleLeafHash(&txs[0])
			Expect(domain.VerifyMerkleProof(leaf, nil, leaf)).To(BeTrue())
			Expect(domain.VerifyMerkleProof(leaf, nil, domain.MerkleRoot(merkleTxs(2)))).To(BeFalse())
		})
	})
})
