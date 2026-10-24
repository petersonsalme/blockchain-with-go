package pow

import (
	"context"
	"encoding/hex"
	"math"
	"math/bits"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// DefaultDifficulty is the number of leading zero bits a block hash must have
// when POW_DIFFICULTY is not set.
const DefaultDifficulty = 8

// Retargeting bounds and defaults. The factor-2 dead band is fixed.
const (
	// MinDifficulty and MaxDifficulty bound retargeting; an initial difficulty
	// outside them is kept, but an adjustment never crosses them.
	MinDifficulty = 1
	MaxDifficulty = 32

	DefaultRetargetInterval = 10
	DefaultTargetBlockTime  = time.Duration(2_000_000_000) // 2s

	// RetargetIntervalEnvVar overrides DefaultRetargetInterval with a
	// non-negative integer; 0 disables retargeting.
	RetargetIntervalEnvVar = "POW_RETARGET_INTERVAL"
	// TargetBlockTimeEnvVar overrides DefaultTargetBlockTime with a positive Go duration.
	TargetBlockTimeEnvVar = "TARGET_BLOCK_TIME"
)

// hashBits is the size of a SHA-256 hash, the highest difficulty that can be solved.
const hashBits = 256

// WorkersEnvVar overrides the default number of mining workers, runtime.NumCPU(),
// with an integer >= 1.
const WorkersEnvVar = "MINING_WORKERS"

// DifficultyEnvVar overrides DefaultDifficulty when it holds a non-negative integer.
const DifficultyEnvVar = "POW_DIFFICULTY"

var _ consensus.Consensus = (*ProofOfWork)(nil)

// ProofOfWork is a Consensus that requires block hashes to start with as many
// zero bits as ExpectedDifficulty names for their position in the chain.
type ProofOfWork struct {
	// Difficulty is the initial difficulty, used by the child of genesis.
	Difficulty int
	// RetargetInterval is the number of blocks between adjustments; 0 disables
	// retargeting, so every block is expected at the initial difficulty.
	RetargetInterval int
	// TargetBlockTime is the intended time between blocks.
	TargetBlockTime time.Duration
	// Workers is the number of goroutines Seal searches nonces with; 0 means
	// runtime.NumCPU().
	Workers int
}

func New(difficulty int) *ProofOfWork {
	return &ProofOfWork{Difficulty: difficulty}
}

func NewProofOfWork(difficulty int) *ProofOfWork {
	return New(difficulty)
}

// NewFromEnv builds a ProofOfWork from POW_DIFFICULTY, POW_RETARGET_INTERVAL and TARGET_BLOCK_TIME.
func NewFromEnv() *ProofOfWork {
	return New(DifficultyFromEnv()).WithRetarget(RetargetIntervalFromEnv(), TargetBlockTimeFromEnv()).WithWorkers(WorkersFromEnv())
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

// WorkersFromEnv returns the mining worker count configured in the environment:
// MINING_WORKERS when it is an integer >= 1, runtime.NumCPU() otherwise.
func WorkersFromEnv() int {
	workers, err := strconv.Atoi(os.Getenv(WorkersEnvVar))
	if err != nil || workers < 1 {
		return runtime.NumCPU()
	}

	return workers
}

// WithWorkers sets the number of mining workers and returns p.
func (p *ProofOfWork) WithWorkers(n int) *ProofOfWork {
	p.Workers = n
	return p
}

func (p *ProofOfWork) workers() int {
	if p.Workers < 1 {
		return runtime.NumCPU()
	}

	return p.Workers
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
	// The window needs the block n places before the parent, so it only
	// exists once the history is longer than n.
	if (parent.Index+1)%n != 0 || len(history) <= n {
		return parent.Difficulty
	}

	actual := parent.Timestamp.Sub(history[len(history)-1-n].Timestamp)
	// Timestamps going backwards say nothing about the hash rate.
	if actual < 0 {
		return parent.Difficulty
	}

	target := time.Duration(math.MaxInt64)
	if p.TargetBlockTime <= target/time.Duration(n) {
		target = time.Duration(n) * p.TargetBlockTime
	}

	// actual < target-actual is 2*actual < target, and actual-target > target
	// is actual > 2*target, without overflowing for actual >= 0.
	if actual < target-actual && parent.Difficulty < MaxDifficulty {
		return parent.Difficulty + 1
	}

	if actual-target > target && parent.Difficulty > MinDifficulty {
		return parent.Difficulty - 1
	}

	return parent.Difficulty
}

// Seal mines b at the difficulty ExpectedDifficulty names for history.
func (p *ProofOfWork) Seal(ctx context.Context, history []*domain.Block, b *domain.Block) error {
	return MineParallel(ctx, b, p.ExpectedDifficulty(history), p.workers())
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

	difficulty = max(difficulty, 0)

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

// MineParallel searches for a nonce like MineWithContext, with workers
// goroutines: worker i tries the nonces i, i+workers, i+2*workers, … on its own
// copy of the block. The first valid nonce found cancels the others, and
// MineParallel returns only after all of them stopped. Any valid nonce is
// accepted, so the one found may differ between runs when workers > 1. On
// success it stores nonce, difficulty and hash on b; on error b is unchanged.
func MineParallel(ctx context.Context, b *domain.Block, difficulty, workers int) error {
	if ctx == nil {
		ctx = context.Background()
	}

	difficulty = max(difficulty, 0)
	workers = max(workers, 1)

	searchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	found := make(chan domain.Block, workers)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		c := *b
		c.Difficulty = difficulty

		wg.Add(1)
		go func(start int) {
			defer wg.Done()

			for c.Nonce = start; searchCtx.Err() == nil; c.Nonce += workers {
				if hash := c.CalculateHash(); solvesDifficulty(hash, difficulty) {
					c.Hash = hash
					found <- c
					cancel()
					return
				}
			}
		}(i)
	}

	wg.Wait()

	select {
	case winner := <-found:
		b.Nonce, b.Difficulty, b.Hash = winner.Nonce, winner.Difficulty, winner.Hash
		return nil
	default:
		return ctx.Err()
	}
}

// solvesDifficulty reports whether hash, 64 lowercase hex characters read as a
// 256-bit big-endian number, has at least difficulty leading zero bits. A
// difficulty <= 0 is always solved, one above 256 never, and a malformed hash
// never (for difficulty > 0).
func solvesDifficulty(hash string, difficulty int) bool {
	if difficulty <= 0 {
		return true
	}

	if difficulty > hashBits || len(hash) != hashBits/4 || hash != strings.ToLower(hash) {
		return false
	}

	raw, err := hex.DecodeString(hash)
	if err != nil {
		return false
	}

	zeros := 0
	for _, octet := range raw {
		if octet != 0 {
			zeros += bits.LeadingZeros8(octet)
			break
		}
		zeros += 8
	}

	return zeros >= difficulty
}
