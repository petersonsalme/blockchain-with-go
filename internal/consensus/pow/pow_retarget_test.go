package pow_test

import (
	"context"
	"math"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	powpkg "github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

var epoch = time.Date(2021, 8, 15, 14, 30, 0, 0, time.UTC)

// history builds blocks 0..n-1 where every block after genesis carries
// difficulty d and the timestamp of block i is epoch + at(i).
func history(n, d int, at func(i int) time.Duration) []*domain.Block {
	h := make([]*domain.Block, n)
	for i := range h {
		h[i] = &domain.Block{Index: i, Timestamp: epoch.Add(at(i))}
		if i > 0 {
			h[i].Difficulty = d
		}
	}
	return h
}

func every(step time.Duration) func(int) time.Duration {
	return func(i int) time.Duration { return time.Duration(i) * step }
}

// window returns a timestamp function where the last window of n blocks, ending
// at index last, spans exactly span; earlier blocks are 1s apart.
func window(last, n int, span time.Duration) func(int) time.Duration {
	return func(i int) time.Duration {
		if i < last-n {
			return time.Duration(i) * time.Second
		}
		start := time.Duration(last-n) * time.Second
		return start + span*time.Duration(i-(last-n))/time.Duration(n)
	}
}

func pinRetargetEnv(interval, target string) {
	for name, value := range map[string]string{powpkg.RetargetIntervalEnvVar: interval, powpkg.TargetBlockTimeEnvVar: target} {
		previous, existed := os.LookupEnv(name)
		DeferCleanup(func() {
			if existed {
				Expect(os.Setenv(name, previous)).To(Succeed())
				return
			}
			Expect(os.Unsetenv(name)).To(Succeed())
		})
		if value == "" {
			Expect(os.Unsetenv(name)).To(Succeed())
			continue
		}
		Expect(os.Setenv(name, value)).To(Succeed())
	}
}

var _ = Describe("ProofOfWork difficulty retargeting", func() {
	const interval = 4
	target := 10 * time.Second // per block; a window of 4 blocks targets 40s

	Describe("ExpectedDifficulty", func() {
		var p *powpkg.ProofOfWork

		BeforeEach(func() { p = powpkg.New(3).WithRetarget(interval, target) })

		// Next block is at height 8, a retarget height: the window is ts(7)-ts(3).
		DescribeTable("at a retarget height",
			func(parent int, span time.Duration, want int) {
				h := history(8, parent, window(7, interval, span))
				Expect(p.ExpectedDifficulty(h)).To(Equal(want))
			},
			Entry("fast window raises by one", 4, 5*time.Second, 5),
			Entry("slow window lowers by one", 4, 200*time.Second, 3),
			Entry("window just under half the target raises", 4, 20*time.Second-1, 5),
			Entry("window at half the target keeps", 4, 20*time.Second, 4),
			Entry("window at twice the target keeps", 4, 80*time.Second, 4),
			Entry("window just over twice the target lowers", 4, 80*time.Second+1, 3),
			Entry("on target keeps", 4, 40*time.Second, 4),
			Entry("fast window at MaxDifficulty stays", powpkg.MaxDifficulty, time.Second, powpkg.MaxDifficulty),
			Entry("slow window at MinDifficulty stays", powpkg.MinDifficulty, 500*time.Second, powpkg.MinDifficulty),
			Entry("fast window just below MaxDifficulty raises", powpkg.MaxDifficulty-1, time.Second, powpkg.MaxDifficulty),
			Entry("slow window just above MinDifficulty lowers", powpkg.MinDifficulty+1, 500*time.Second, powpkg.MinDifficulty),
			Entry("fast window below MinDifficulty raises", 0, time.Second, 1),
			Entry("slow window above MaxDifficulty lowers", powpkg.MaxDifficulty+1, 500*time.Second, powpkg.MaxDifficulty),
		)

		It("retargets at the next multiple of the interval too", func() {
			Expect(p.ExpectedDifficulty(history(12, 4, window(11, interval, time.Second)))).To(Equal(5))
		})

		It("keeps the parent's difficulty at every other height, however fast the blocks are", func() {
			for n := 6; n <= 11; n++ {
				if n%interval == 0 {
					continue
				}
				Expect(p.ExpectedDifficulty(history(n, 4, every(time.Millisecond)))).To(Equal(4), "height %d", n)
			}
		})

		It("does not retarget at a multiple of the interval below interval+1", func() {
			Expect(p.ExpectedDifficulty(history(interval, 4, every(time.Millisecond)))).To(Equal(4))
		})

		It("retargets at height interval+1 when that is a multiple of the interval", func() {
			one := powpkg.New(3).WithRetarget(1, target)
			Expect(one.ExpectedDifficulty(history(2, 4, every(time.Millisecond)))).To(Equal(5))
		})

		It("uses the initial difficulty for the child of genesis", func() {
			genesis := history(1, 0, every(time.Second))
			genesis[0].Difficulty = 6
			Expect(p.ExpectedDifficulty(genesis)).To(Equal(3))
			Expect(p.ExpectedDifficulty(nil)).To(Equal(3))
		})

		It("keeps the parent's difficulty when the history is shorter than the window", func() {
			short := []*domain.Block{{Index: 7, Timestamp: epoch, Difficulty: 4}}
			Expect(p.ExpectedDifficulty(short)).To(Equal(4))
		})

		It("takes the height from the parent's index, not from the length of the history", func() {
			// Blocks 13..19: the next height, 20, is a retarget height though 7 blocks are no multiple of 4.
			suffix := history(20, 4, window(19, interval, time.Second))[13:]
			Expect(p.ExpectedDifficulty(suffix)).To(Equal(5))

			// Blocks 11..18: 8 blocks are a multiple of 4, but the next height, 19, is not a retarget height.
			suffix = history(19, 4, every(time.Millisecond))[11:]
			Expect(p.ExpectedDifficulty(suffix)).To(Equal(4))
		})

		It("always expects the initial difficulty when the interval is 0", func() {
			off := powpkg.New(3).WithRetarget(0, target)
			Expect(off.ExpectedDifficulty(history(8, 5, window(7, interval, time.Second)))).To(Equal(3))
			Expect(powpkg.New(3).ExpectedDifficulty(history(8, 5, window(7, interval, time.Second)))).To(Equal(3))
		})

		It("is a pure function of the history", func() {
			h := history(8, 4, window(7, interval, time.Second))
			Expect(p.ExpectedDifficulty(h)).To(Equal(p.ExpectedDifficulty(h)))
			Expect(h[7].Difficulty).To(Equal(4))
		})

		It("lowers the difficulty after a window of 200 years", func() {
			h := history(8, 4, every(time.Second))
			h[7].Timestamp = h[3].Timestamp.Add(200 * 365 * 24 * time.Hour)
			Expect(p.ExpectedDifficulty(h)).To(Equal(3))
		})

		It("keeps the difficulty when the window's timestamps go backwards", func() {
			h := history(8, 4, every(time.Second))
			h[7].Timestamp = h[3].Timestamp.Add(-time.Hour)
			Expect(p.ExpectedDifficulty(h)).To(Equal(4))
		})

		It("saturates the window target instead of overflowing", func() {
			huge := powpkg.New(3).WithRetarget(interval, time.Duration(math.MaxInt64/2))
			h := history(8, 4, window(7, interval, time.Hour))
			Expect(huge.ExpectedDifficulty(h)).To(Equal(5))
		})
	})

	Describe("convergence", func() {
		// Artificial block time: 2^difficulty seconds. With a 8s target the
		// stable difficulties are those with 4s <= 2^d <= 16s, that is 2..4.
		simulate := func(initial int) (difficulties []int, spans []time.Duration) {
			const window = 5
			p := powpkg.New(initial).WithRetarget(window, 8*time.Second)
			h := []*domain.Block{{Index: 0, Timestamp: epoch}}
			for len(h) < 12*window {
				d := p.ExpectedDifficulty(h)
				last := h[len(h)-1]
				h = append(h, &domain.Block{Index: last.Index + 1, Difficulty: d, Timestamp: last.Timestamp.Add(time.Second << d)})
			}
			for i := window + 1; i < len(h); i += window {
				difficulties = append(difficulties, h[i].Difficulty)
				spans = append(spans, h[i-1].Timestamp.Sub(h[i-1-window].Timestamp))
			}
			return difficulties, spans
		}

		DescribeTable("settles inside the dead band within K windows",
			func(initial int) {
				difficulties, spans := simulate(initial)
				const k = 8
				tail := difficulties[k:]
				for _, d := range tail {
					Expect(d).To(Equal(tail[0]))
				}
				for _, span := range spans[k:] {
					Expect(span).To(BeNumerically(">=", 20*time.Second))
					Expect(span).To(BeNumerically("<=", 80*time.Second))
				}
			},
			Entry("starting far too hard", 8),
			Entry("starting far too easy", 1),
		)
	})

	Describe("Verify", func() {
		var (
			p     *powpkg.ProofOfWork
			hist  []*domain.Block
			child func(difficulty int) *domain.Block
		)

		BeforeEach(func() {
			p = powpkg.New(1).WithRetarget(interval, target)
			child = func(difficulty int) *domain.Block {
				prev := hist[len(hist)-1]
				b := &domain.Block{Index: prev.Index + 1, Timestamp: prev.Timestamp.Add(time.Second), PrevHash: prev.Hash}
				Expect(powpkg.MineWithContext(context.Background(), b, difficulty)).To(Succeed())
				return b
			}
		})

		sealPrev := func(h []*domain.Block) {
			for i := 1; i < len(h); i++ {
				h[i].PrevHash = h[i-1].Hash
				h[i].Hash = h[i].CalculateHash()
			}
		}

		DescribeTable("rejects a validly mined block whose difficulty is off by one",
			func(n, parent, offset int) {
				hist = history(n, parent, every(time.Millisecond))
				sealPrev(hist)
				want := p.ExpectedDifficulty(hist)
				Expect(p.Verify(hist, child(want))).To(Succeed())
				Expect(p.Verify(hist, child(want+offset))).To(MatchError(consensus.ErrInvalidDifficulty))
			},
			Entry("retarget height, one too high", 8, 3, 1),
			Entry("retarget height, one too low", 8, 3, -1),
			Entry("plain height, one too high", 7, 3, 1),
			Entry("plain height, one too low", 7, 3, -1),
			Entry("child of genesis, one too high", 1, 0, 1),
		)

		It("checks the difficulty before the proof of work", func() {
			hist = history(7, 3, every(time.Millisecond))
			sealPrev(hist)
			b := child(3)
			b.Difficulty = 4
			for b.Hash = b.CalculateHash(); b.Hash[:4] == "0000"; b.Nonce++ {
				b.Hash = b.CalculateHash()
			}
			Expect(p.Verify(hist, b)).To(MatchError(consensus.ErrInvalidDifficulty))
		})

		It("still reports a bad proof of work for the expected difficulty", func() {
			hist = history(7, 3, every(time.Millisecond))
			sealPrev(hist)
			b := child(3)
			for b.Nonce++; b.CalculateHash()[:3] == "000"; b.Nonce++ {
			}
			b.Hash = b.CalculateHash()
			Expect(p.Verify(hist, b)).To(MatchError(consensus.ErrInvalidProofOfWork))
		})

		It("seals with the expected difficulty so Verify accepts the result", func() {
			hist = history(8, 1, window(7, interval, time.Second))
			sealPrev(hist)
			b := &domain.Block{Index: 8, Timestamp: epoch.Add(time.Minute), PrevHash: hist[7].Hash}
			Expect(p.Seal(context.Background(), hist, b)).To(Succeed())
			Expect(b.Difficulty).To(Equal(2))
			Expect(p.Verify(hist, b)).To(Succeed())
		})
	})

	Describe("configuration", func() {
		It("defaults to every 10 blocks and a 10s target", func() {
			pinRetargetEnv("", "")
			Expect(powpkg.RetargetIntervalFromEnv()).To(Equal(10))
			Expect(powpkg.TargetBlockTimeFromEnv()).To(Equal(10 * time.Second))
		})

		It("reads valid values, including 0 to disable", func() {
			pinRetargetEnv("0", "1m30s")
			Expect(powpkg.RetargetIntervalFromEnv()).To(Equal(0))
			Expect(powpkg.TargetBlockTimeFromEnv()).To(Equal(90 * time.Second))
			pinRetargetEnv("25", "2s")
			Expect(powpkg.RetargetIntervalFromEnv()).To(Equal(25))
		})

		DescribeTable("falls back to the defaults on invalid values",
			func(interval, target string) {
				pinRetargetEnv(interval, target)
				Expect(powpkg.RetargetIntervalFromEnv()).To(Equal(powpkg.DefaultRetargetInterval))
				Expect(powpkg.TargetBlockTimeFromEnv()).To(Equal(powpkg.DefaultTargetBlockTime))
			},
			Entry("garbage", "abc", "soon"),
			Entry("negative interval and negative target", "-1", "-5s"),
			Entry("zero target", "x", "0s"),
		)

		It("wires the three variables into NewProofOfWorkFromEnv", func() {
			pinDifficultyEnv("3")
			pinRetargetEnv("7", "3s")
			p := powpkg.NewProofOfWorkFromEnv()
			Expect(p.Difficulty).To(Equal(3))
			Expect(p.RetargetInterval).To(Equal(7))
			Expect(p.TargetBlockTime).To(Equal(3 * time.Second))
		})

		It("leaves retargeting off for New", func() {
			p := powpkg.New(2)
			Expect(p.RetargetInterval).To(BeZero())
		})
	})
})
