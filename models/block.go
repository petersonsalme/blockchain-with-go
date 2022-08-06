package models

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

type Block struct {
	Index     int
	Timestamp string
	BPM       int
	Hash      string
	PrevHash  string
}

func (b *Block) CalculateHash() string {
	record := fmt.Sprintf("%d%s%d%s", b.Index, b.Timestamp, b.BPM, b.PrevHash)
	h := sha256.New()
	h.Write([]byte(record))
	hashed := h.Sum(nil)
	return hex.EncodeToString(hashed)
}

func (b *Block) IsValid(oldBlock *Block) bool {
	if oldBlock.Index+1 != b.Index {
		return false
	}

	if oldBlock.Hash != b.PrevHash {
		return false
	}

	if b.CalculateHash() != b.Hash {
		return false
	}

	return true
}
