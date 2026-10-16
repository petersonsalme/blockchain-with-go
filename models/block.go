package models

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultDifficulty is the number of leading zeros a block hash must have when
// POW_DIFFICULTY is not set.
const DefaultDifficulty = 2

// DifficultyEnvVar overrides DefaultDifficulty when it holds a non-negative integer.
const DifficultyEnvVar = "POW_DIFFICULTY"

type Block struct {
	Index      int
	Timestamp  time.Time
	BPM        int
	Hash       string
	PrevHash   string
	Nonce      int
	Difficulty int
}

// Difficulty returns the proof-of-work difficulty new blocks must satisfy.
func Difficulty() int {
	raw := os.Getenv(DifficultyEnvVar)
	if raw == "" {
		return DefaultDifficulty
	}

	difficulty, err := strconv.Atoi(raw)
	if err != nil || difficulty < 0 {
		return DefaultDifficulty
	}

	return difficulty
}

func (b *Block) CalculateHash() string {
	record := fmt.Sprintf("%d%s%d%s%d%d", b.Index, b.Timestamp.UTC().String(), b.BPM, b.PrevHash, b.Nonce, b.Difficulty)
	h := sha256.New()
	h.Write([]byte(record))
	hashed := h.Sum(nil)
	return hex.EncodeToString(hashed)
}

// Mine searches for a nonce whose hash satisfies the given difficulty and stores
// the resulting nonce, difficulty and hash on the block.
func (b *Block) Mine(difficulty int) {
	if difficulty < 0 {
		difficulty = 0
	}

	b.Difficulty = difficulty

	for b.Nonce = 0; ; b.Nonce++ {
		hash := b.CalculateHash()
		if solvesDifficulty(hash, difficulty) {
			b.Hash = hash
			return
		}
	}
}

// HasValidProofOfWork reports whether the block hash satisfies its own difficulty.
func (b *Block) HasValidProofOfWork() bool {
	return solvesDifficulty(b.Hash, b.Difficulty)
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

	if !b.HasValidProofOfWork() {
		return false
	}

	return true
}

func solvesDifficulty(hash string, difficulty int) bool {
	return strings.HasPrefix(hash, strings.Repeat("0", difficulty))
}
