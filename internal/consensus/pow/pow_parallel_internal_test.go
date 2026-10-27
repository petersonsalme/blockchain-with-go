package pow

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// The smallest nonce N that solves the difficulty is known from the sequential
// search, so its worker, N % workers, is the winner. onTry holds every other
// worker before its first hash until the search is cancelled: only the winner
// advances, which makes the nonces each worker tries deterministic, and
// mineParallel returns only if the winner cancels the losers.
func TestMineParallelPartitionsNoncesAndCancelsLosers(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	const workers, difficulty = 4, 10

	block := func() *domain.Block {
		return &domain.Block{Index: 1, Timestamp: time.Date(2021, 8, 15, 14, 30, 45, 100, time.UTC), PrevHash: "prev-15", Hash: "unchanged"}
	}

	seq := block()

	err := MineWithContext(context.Background(), seq, difficulty)
	if err != nil {
		t.Fatal(err)
	}

	if seq.Nonce < workers {
		t.Fatalf("precondition: smallest nonce %d is below %d workers, pick another PrevHash", seq.Nonce, workers)
	}

	winner := seq.Nonce % workers

	var mu sync.Mutex

	tried := make(map[int][]int, workers)
	onTry := func(ctx context.Context, worker, nonce int) {
		mu.Lock()

		tried[worker] = append(tried[worker], nonce)
		mu.Unlock()

		if worker != winner {
			<-ctx.Done()
		}
	}

	b := block()
	done := make(chan error, 1)

	go func() { done <- mineParallel(context.Background(), b, difficulty, workers, onTry) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("mineParallel did not return: the winner did not cancel the losers")
	}

	var want []int
	for n := winner; n <= seq.Nonce; n += workers {
		want = append(want, n)
	}

	for w := range workers {
		expected := []int{w}
		if w == winner {
			expected = want
		}

		if !slices.Equal(tried[w], expected) {
			t.Errorf("worker %d tried %v, want %v", w, tried[w], expected)
		}
	}

	if b.Nonce != seq.Nonce || b.Hash != seq.Hash || b.Difficulty != difficulty {
		t.Fatalf("block (%d, %s, %d), want (%d, %s, %d)", b.Nonce, b.Hash, b.Difficulty, seq.Nonce, seq.Hash, difficulty)
	}
}
