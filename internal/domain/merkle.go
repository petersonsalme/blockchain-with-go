package domain

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
)

const (
	merkleLeafPrefix byte = 0x00
	merkleNodePrefix byte = 0x01
)

// ErrMerkleIndex is returned by BuildMerkleProof for an index outside the transaction list.
var ErrMerkleIndex = errors.New("merkle: transaction index out of range")

// MerkleStep is one sibling on the path from a leaf to the root. Left reports
// that the sibling sits on the left of the running hash.
type MerkleStep struct {
	Hash string
	Left bool
}

// MerkleProof proves that a leaf belongs to the tree of a given root.
type MerkleProof struct {
	Steps []MerkleStep
}

func merkleLeaf(tx *Transaction) [sha256.Size]byte {
	data := append([]byte{merkleLeafPrefix}, tx.CalculateHash()...)
	for _, in := range tx.Inputs {
		for _, s := range []string{in.PublicKey, in.Signature} {
			data = binary.BigEndian.AppendUint32(data, uint32(len(s)))
			data = append(data, s...)
		}
	}

	return sha256.Sum256(data)
}

func merkleNode(left, right [sha256.Size]byte) [sha256.Size]byte {
	data := make([]byte, 0, 1+len(left)+len(right))
	data = append(data, merkleNodePrefix)
	data = append(data, left[:]...)
	data = append(data, right[:]...)

	return sha256.Sum256(data)
}

// MerkleLeafHash is the leaf hash of tx (ADR-0009): it covers the recalculated
// transaction hash and, for every input, its public key and signature.
func MerkleLeafHash(tx *Transaction) string {
	leaf := merkleLeaf(tx)
	return hex.EncodeToString(leaf[:])
}

// merkleParents pairs adjacent nodes; an odd last node moves up unchanged.
func merkleParents(level [][sha256.Size]byte) [][sha256.Size]byte {
	var next [][sha256.Size]byte
	for i := 0; i+1 < len(level); i += 2 {
		next = append(next, merkleNode(level[i], level[i+1]))
	}

	if len(level)%2 == 1 {
		next = append(next, level[len(level)-1])
	}

	return next
}

// merkleLevels returns every level of the tree, from the leaves up to the root.
func merkleLevels(level [][sha256.Size]byte) [][][sha256.Size]byte {
	if len(level) == 1 {
		return [][][sha256.Size]byte{level}
	}

	return append([][][sha256.Size]byte{level}, merkleLevels(merkleParents(level))...)
}

func merkleLeaves(txs []Transaction) [][sha256.Size]byte {
	leaves := make([][sha256.Size]byte, len(txs))
	for i := range txs {
		leaves[i] = merkleLeaf(&txs[i])
	}

	return leaves
}

// MerkleRoot is the root of the transaction tree (ADR-0009, RFC 6962): an odd
// node without a pair moves up unchanged. Zero transactions give SHA-256 of
// the empty string.
func MerkleRoot(txs []Transaction) string {
	if len(txs) == 0 {
		empty := sha256.Sum256(nil)
		return hex.EncodeToString(empty[:])
	}

	levels := merkleLevels(merkleLeaves(txs))

	return hex.EncodeToString(levels[len(levels)-1][0][:])
}

// BuildMerkleProof returns the proof that txs[index] is in the tree of txs.
func BuildMerkleProof(txs []Transaction, index int) (MerkleProof, error) {
	if index < 0 || index >= len(txs) {
		return MerkleProof{}, ErrMerkleIndex
	}

	var steps []MerkleStep

	for _, level := range merkleLevels(merkleLeaves(txs)) {
		if sibling := index ^ 1; sibling < len(level) {
			steps = append(steps, MerkleStep{
				Hash: hex.EncodeToString(level[sibling][:]),
				Left: index%2 == 1,
			})
		}

		index /= 2
	}

	return MerkleProof{Steps: steps}, nil
}

func decodeMerkleHash(s string) ([sha256.Size]byte, bool) {
	var out [sha256.Size]byte

	raw, err := hex.DecodeString(s)
	if err != nil || len(raw) != sha256.Size {
		return out, false
	}

	copy(out[:], raw)

	return out, true
}

// VerifyMerkleProof reports whether folding steps over leafHash yields root.
func VerifyMerkleProof(leafHash string, steps []MerkleStep, root string) bool {
	cur, ok := decodeMerkleHash(leafHash)
	if !ok {
		return false
	}

	want, ok := decodeMerkleHash(root)
	if !ok {
		return false
	}

	for _, s := range steps {
		sibling, ok := decodeMerkleHash(s.Hash)
		if !ok {
			return false
		}

		if s.Left {
			cur = merkleNode(sibling, cur)
		} else {
			cur = merkleNode(cur, sibling)
		}
	}

	return cur == want
}
