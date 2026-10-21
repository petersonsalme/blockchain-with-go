package pow

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// DefaultDifficulty is the number of leading zeros a block hash must have when
// POW_DIFFICULTY is not set.
const DefaultDifficulty = 2

// Retargeting bounds and defaults. The factor-2 dead band is fixed.
const (
	// MinDifficulty and MaxDifficulty bound retargeting; an initial difficulty
	// outside them is kept, but an adjustment never crosses them.
	MinDifficulty = 1
	MaxDifficulty = 8

	DefaultRetargetInterval = 10
	DefaultTargetBlockTime  = 10 * time.Second

	// RetargetIntervalEnvVar overrides DefaultRetargetInterval with a
	// non-negative integer; 0 disables retargeting.
	RetargetIntervalEnvVar = "POW_RETARGET_INTERVAL"
	// TargetBlockTimeEnvVar overrides DefaultTargetBlockTime with a positive Go duration.
	TargetBlockTimeEnvVar = "TARGET_BLOCK_TIME"
)

// DifficultyEnvVar overrides DefaultDifficulty when it holds a non-negative integer.
const DifficultyEnvVar = "POW_DIFFICULTY"

var _ consensus.Consensus = (*ProofOfWork)(nil)

// ProofOfWork is a Consensus that requires block hashes to start with as many
// zeros as ExpectedDifficulty names for their position in the chain.
type ProofOfWork struct {
	// Difficulty is the initial difficulty, used by the child of genesis.
	Difficulty int
	// RetargetInterval is the number of blocks between adjustments; 0 disables
	// retargeting, so every block is expected at the initial difficulty.
	RetargetInterval int
	// TargetBlockTime is the intended time between blocks.
	TargetBlockTime time.Duration
}

func New(difficulty int) *ProofOfWork {
	return &ProofOfWork{Difficulty: difficulty}
}

func NewProofOfWork(difficulty int) *ProofOfWork {
	return New(difficulty)
}

// NewFromEnv builds a ProofOfWork from POW_DIFFICULTY, POW_RETARGET_INTERVAL and TARGET_BLOCK_TIME.
func NewFromEnv() *ProofOfWork {
	return New(DifficultyFromEnv()).WithRetarget(RetargetIntervalFromEnv(), TargetBlockTimeFromEnv())
}

// NewProofOfWorkFromEnv builds a ProofOfWork from POW_DIFFICULTY, POW_RETARGET_INTERVAL and TARGET_BLOCK_TIME.
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

// RetargetIntervalFromEnv returns the retarget interval configured in the environment.
func RetargetIntervalFromEnv() int {
	interval, err := strconv.Atoi(os.Getenv(RetargetIntervalEnvVar))
	if err != nil || interval < 0 {
		return DefaultRetargetInterval
	}

	return interval
}

// TargetBlockTimeFromEnv returns the target block time configured in the environment.
func TargetBlockTimeFromEnv() time.Duration {
	target, err := time.ParseDuration(os.Getenv(TargetBlockTimeEnvVar))
	if err != nil || target <= 0 {
		return DefaultTargetBlockTime
	}

	return target
}

// WithRetarget enables retargeting every interval blocks towards target per
// block, and returns p. An interval of 0 disables it.
func (p *ProofOfWork) WithRetarget(interval int, target time.Duration) *ProofOfWork {
	p.RetargetInterval = interval
	p.TargetBlockTime = target
	return p
}

// ExpectedDifficulty returns the difficulty the successor of the last block of
// history must carry. history is ordered oldest to newest. It depends on
// nothing but its arguments.
func (p *ProofOfWork) ExpectedDifficulty(history []*domain.Block) int {
	n := p.RetargetInterval
	if n == 0 || len(history) == 0 || history[len(history)-1].Index == 0 {
		return p.Difficulty
	}

	parent := history[len(history)-1]
	height := parent.Index + 1
	if height%n != 0 || height < n+1 || len(history) <= n {
		return parent.Difficulty
	}

	actual := parent.Timestamp.Sub(history[len(history)-1-n].Timestamp)
	target := time.Duration(n) * p.TargetBlockTime

	switch {
	case 2*actual < target && parent.Difficulty < MaxDifficulty:
		return parent.Difficulty + 1
	case actual > 2*target && parent.Difficulty > MinDifficulty:
		return parent.Difficulty - 1
	default:
		return parent.Difficulty
	}
}

// Seal mines b at the difficulty ExpectedDifficulty names for history.
func (p *ProofOfWork) Seal(ctx context.Context, history []*domain.Block, b *domain.Block) error {
	return MineWithContext(ctx, b, p.ExpectedDifficulty(history))
}

// Verify checks the block against its predecessor, the last block of history,
// then that it carries the expected difficulty and solves it.
func (p *ProofOfWork) Verify(history []*domain.Block, b *domain.Block) error {
	prev := history[len(history)-1]
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

	if b.Difficulty != p.ExpectedDifficulty(history) {
		return consensus.ErrInvalidDifficulty
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
