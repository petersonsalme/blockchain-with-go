package pow_test

import (
	"fmt"
	"math"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	powpkg "github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// simulate grows a chain whose blocks take exactly 2^d / hashRate seconds at
// difficulty d, asking the real ExpectedDifficulty for every block, and
// returns the difficulty of each block after genesis.
func simulate(p *powpkg.ProofOfWork, hashRate float64, blocks int) []int {
	history := []*domain.Block{{Index: 0, Timestamp: epoch}}
	difficulties := make([]int, 0, blocks)

	for i := 1; i <= blocks; i++ {
		d := p.ExpectedDifficulty(history)
		taken := time.Duration(math.Exp2(float64(d)) / hashRate * float64(time.Second))
		prev := history[len(history)-1]
		history = append(history, &domain.Block{Index: i, Timestamp: prev.Timestamp.Add(taken), Difficulty: d})
		difficulties = append(difficulties, d)
	}

	return difficulties
}

var _ = Describe("difficulty retargeting simulation", func() {
	const (
		interval = 10
		windows  = 100
	)

	hashRates := []float64{1.45e6}
	for exp := 3; exp <= 9; exp++ {
		hashRates = append(hashRates, math.Pow10(exp))
		if exp < 9 {
			hashRates = append(hashRates, 3*math.Pow10(exp))
		}
	}

	for _, hashRate := range hashRates {
		It(fmt.Sprintf("settles at %g H/s", hashRate), func() {
			p := powpkg.New(powpkg.DefaultDifficulty).WithRetarget(interval, powpkg.DefaultTargetBlockTime)
			difficulties := simulate(p, hashRate, windows*interval)

			// A difficulty is constant over a window, so compare the last 10 windows.
			final := difficulties[len(difficulties)-1]
			for _, d := range difficulties[len(difficulties)-10*interval:] {
				Expect(d).To(Equal(final), "difficulty still moving after %d windows", windows-10)
			}

			if final == powpkg.MinDifficulty || final == powpkg.MaxDifficulty {
				return
			}

			blockTime := math.Exp2(float64(final)) / hashRate
			Expect(blockTime).To(BeNumerically(">=", 1.0))
			Expect(blockTime).To(BeNumerically("<=", 4.0))
		})
	}
})
