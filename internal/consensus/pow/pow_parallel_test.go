package pow_test

import (
	"context"
	"encoding/hex"
	"errors"
	"math/bits"
	"runtime"
	"testing"
	"time"

	"go.uber.org/goleak"

	powpkg "github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

func leadingZeroBits(t *testing.T, hash string) int {
	t.Helper()

	raw, err := hex.DecodeString(hash)
	if err != nil {
		t.Fatal(err)
	}

	zeros := 0

	for _, octet := range raw {
		if octet != 0 {
			return zeros + bits.LeadingZeros8(octet)
		}

		zeros += 8
	}

	return zeros
}

func parallelBlock() *domain.Block {
	b := &domain.Block{
		Index:     1,
		Timestamp: time.Date(2021, 8, 15, 14, 30, 45, 100, time.UTC),
		PrevHash:  "prev",
	}
	b.Hash = "unchanged"

	return b
}

func TestSealParallelProducesVerifiableBlock(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	genesis := &domain.Block{Index: 0, Timestamp: time.Date(2021, 8, 15, 14, 30, 45, 100, time.UTC)}
	genesis.Hash = genesis.CalculateHash()
	history := []*domain.Block{genesis}

	for _, w := range []int{1, 2, 4, runtime.NumCPU()} {
		p := powpkg.New(3).WithWorkers(w)
		b := &domain.Block{Index: 1, Timestamp: genesis.Timestamp.Add(time.Second), PrevHash: genesis.Hash}

		err := p.Seal(context.Background(), history, b)
		if err != nil {
			t.Fatalf("workers=%d: Seal: %v", w, err)
		}

		if b.Difficulty != p.ExpectedDifficulty(history) {
			t.Fatalf("workers=%d: difficulty %d", w, b.Difficulty)
		}

		err = p.Verify(history, b)
		if err != nil {
			t.Fatalf("workers=%d: Verify: %v", w, err)
		}
	}
}

func TestMineParallelStopsOnCancellation(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	deadline, cancelDeadline := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelDeadline()

	cases := []struct {
		name       string
		ctx        context.Context
		difficulty int
		want       error
	}{
		{"canceled", canceled, 3, context.Canceled},
		{"deadline", deadline, 64, context.DeadlineExceeded},
	}

	for _, tc := range cases {
		for _, w := range []int{1, 4} {
			b := parallelBlock()
			before := *b

			err := powpkg.MineParallel(tc.ctx, b, tc.difficulty, w)
			if !errors.Is(err, tc.want) {
				t.Fatalf("%s workers=%d: err = %v, want %v", tc.name, w, err, tc.want)
			}

			if b.Hash != before.Hash {
				t.Fatalf("%s workers=%d: Hash changed to %q", tc.name, w, b.Hash)
			}
		}
	}
}

func TestMineParallelSucceedsAndLeaksNothing(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	for _, w := range []int{1, 3, 8} {
		b := parallelBlock()

		err := powpkg.MineParallel(context.Background(), b, 3, w)
		if err != nil {
			t.Fatalf("workers=%d: %v", w, err)
		}

		if b.Hash != b.CalculateHash() || leadingZeroBits(t, b.Hash) < 3 || b.Difficulty != 3 {
			t.Fatalf("workers=%d: invalid block %+v", w, b)
		}
	}
}

func TestMineParallelOneWorkerMatchesSequential(t *testing.T) {
	seq, par := parallelBlock(), parallelBlock()

	err := powpkg.MineWithContext(context.Background(), seq, 3)
	if err != nil {
		t.Fatal(err)
	}

	err = powpkg.MineParallel(context.Background(), par, 3, 1)
	if err != nil {
		t.Fatal(err)
	}

	if par.Nonce != seq.Nonce || par.Hash != seq.Hash {
		t.Fatalf("parallel (%d, %s) != sequential (%d, %s)", par.Nonce, par.Hash, seq.Nonce, seq.Hash)
	}
}

func TestMineParallelNormalisesArguments(t *testing.T) {
	for _, w := range []int{0, -2} {
		b := parallelBlock()

		//nolint:staticcheck // a nil context is part of the contract, as in MineWithContext.
		err := powpkg.MineParallel(nil, b, -1, w)
		if err != nil {
			t.Fatalf("workers=%d: %v", w, err)
		}

		if b.Difficulty != 0 || b.Hash != b.CalculateHash() {
			t.Fatalf("workers=%d: %+v", w, b)
		}
	}
}

func TestWorkersFromEnv(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  int
	}{
		{"", runtime.NumCPU()},
		{"abc", runtime.NumCPU()},
		{"0", runtime.NumCPU()},
		{"-3", runtime.NumCPU()},
		{"1", 1},
		{"3", 3},
	} {
		t.Setenv(powpkg.WorkersEnvVar, tc.value)

		if got := powpkg.WorkersFromEnv(); got != tc.want {
			t.Fatalf("MINING_WORKERS=%q: got %d, want %d", tc.value, got, tc.want)
		}
	}

	t.Setenv(powpkg.WorkersEnvVar, "3")

	if got := powpkg.NewFromEnv().Workers; got != 3 {
		t.Fatalf("NewFromEnv().Workers = %d, want 3", got)
	}
}

func TestWithWorkersSetsField(t *testing.T) {
	if p := powpkg.New(1).WithWorkers(5); p.Workers != 5 {
		t.Fatalf("Workers = %d", p.Workers)
	}
}

// sealWorkerGoroutines returns how many goroutines Seal runs on top of its
// caller, observed while it searches an unsolvable difficulty. It polls until
// the count reaches want (workers start asynchronously, slowly on small CI
// machines) or two seconds pass, and returns the last count.
func sealWorkerGoroutines(t *testing.T, p *powpkg.ProofOfWork, want int) int {
	t.Helper()

	genesis := &domain.Block{Index: 0, Timestamp: time.Date(2021, 8, 15, 14, 30, 45, 100, time.UTC)}
	genesis.Hash = genesis.CalculateHash()
	b := &domain.Block{Index: 1, Timestamp: genesis.Timestamp.Add(time.Second), PrevHash: genesis.Hash}

	ctx, cancel := context.WithCancel(context.Background())
	base := settledGoroutines()
	done := make(chan error, 1)

	go func() { done <- p.Seal(ctx, []*domain.Block{genesis}, b) }()

	// Subtract the goroutine that runs Seal itself.
	n := runtime.NumGoroutine() - base - 1

	for deadline := time.Now().Add(2 * time.Second); n != want && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)

		n = runtime.NumGoroutine() - base - 1
	}

	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Seal returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Seal did not return after cancellation")
	}

	return n
}

func TestSealWorkerCount(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	if got := sealWorkerGoroutines(t, powpkg.New(64).WithWorkers(1), 1); got != 1 {
		t.Fatalf("WithWorkers(1): %d workers", got)
	}

	if got := sealWorkerGoroutines(t, powpkg.New(64).WithWorkers(3), 3); got != 3 {
		t.Fatalf("WithWorkers(3): %d workers", got)
	}

	if got := sealWorkerGoroutines(t, powpkg.New(64), runtime.NumCPU()); got != runtime.NumCPU() {
		t.Fatalf("default: %d workers, want %d", got, runtime.NumCPU())
	}
}

// settledGoroutines returns the goroutine count once it has not changed for
// 50 ms, so goroutines still winding down from an earlier step do not leak
// into the baseline of the next one.
func settledGoroutines() int {
	n, stable := runtime.NumGoroutine(), 0

	for deadline := time.Now().Add(2 * time.Second); stable < 10 && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)

		if m := runtime.NumGoroutine(); m == n {
			stable++
		} else {
			n, stable = m, 0
		}
	}

	return n
}
