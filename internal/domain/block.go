package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

type Block struct {
	Index        int
	Timestamp    time.Time
	Transactions []Transaction
	Hash         string
	PrevHash     string
	Nonce        int
	Difficulty   int
	Validator    string
	Signature    string
}

func (b *Block) CalculateHash() string {
	record := fmt.Sprintf("%d%s%s%s%d%d%s", b.Index, b.Timestamp.UTC().String(), TransactionsHash(b.Transactions), b.PrevHash, b.Nonce, b.Difficulty, b.Validator)
	h := sha256.New()
	h.Write([]byte(record))
	hashed := h.Sum(nil)
	return hex.EncodeToString(hashed)
}
