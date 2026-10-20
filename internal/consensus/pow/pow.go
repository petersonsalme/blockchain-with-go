package pow

import (
	"context"
	"os"
	"strconv"
	"strings"

	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// DefaultDifficulty is the number of leading zeros a block hash must have when
// POW_DIFFICULTY is not set.
const DefaultDifficulty = 2

// DifficultyEnvVar overrides DefaultDifficulty when it holds a non-negative integer.
const DifficultyEnvVar = "POW_DIFFICULTY"

var _ consensus.Consensus = (*ProofOfWork)(nil)

// ProofOfWork is a Consensus that requires block hashes to start with Difficulty zeros.
type ProofOfWork struct {
	Difficulty int
}

func New(difficulty int) *ProofOfWork {
	return &ProofOfWork{Difficulty: difficulty}
}

func NewProofOfWork(difficulty int) *ProofOfWork {
	return New(difficulty)
}

// NewFromEnv builds a ProofOfWork using the difficulty from POW_DIFFICULTY.
func NewFromEnv() *ProofOfWork {
	return New(DifficultyFromEnv())
}

// NewProofOfWorkFromEnv builds a ProofOfWork using the difficulty from POW_DIFFICULTY.
func NewProofOfWorkFromEnv() *ProofOfWork {
	return NewFromEnv()
}

// DifficultyFromEnv returns the proof-of-work difficulty configured in the environment.
func DifficultyFromEnv() int {
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

func (p *ProofOfWork) Seal(ctx context.Context, b *domain.Block) error {
	return MineWithContext(ctx, b, p.Difficulty)
}

// Verify checks the block against its predecessor, validating the proof of
// work against the block's own recorded difficulty.
func (p *ProofOfWork) Verify(prev, b *domain.Block) error {
	if prev.Index+1 != b.Index {
		return consensus.ErrInvalidIndex
	}

	if prev.Hash != b.PrevHash {
		return consensus.ErrInvalidPrevHash
	}

	if b.CalculateHash() != b.Hash {
		return consensus.ErrInvalidHash
	}

	if b.Validator != "" || b.Signature != "" {
		return consensus.ErrInvalidSeal
	}

	if !solvesDifficulty(b.Hash, b.Difficulty) {
		return consensus.ErrInvalidProofOfWork
	}

	return nil
}

// MineWithContext searches for a nonce whose hash satisfies the given difficulty
// and stores the resulting nonce, difficulty and hash on the block. It checks
// ctx.Err() in the nonce loop and aborts with the context error if canceled or timed out.
func MineWithContext(ctx context.Context, b *domain.Block, difficulty int) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if difficulty < 0 {
		difficulty = 0
	}

	b.Difficulty = difficulty

	for b.Nonce = 0; ; b.Nonce++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		hash := b.CalculateHash()
		if solvesDifficulty(hash, difficulty) {
			b.Hash = hash
			return nil
		}
	}
}

func solvesDifficulty(hash string, difficulty int) bool {
	return strings.HasPrefix(hash, strings.Repeat("0", difficulty))
}
