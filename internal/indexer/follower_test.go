package indexer_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/indexer"
)

// fakeChain builds blocks whose hash is "<tag><height>" chained on the previous one.
func fakeChain(tag string, from indexer.Block, n int) []indexer.Block {
	out := []indexer.Block{from}
	for i := 1; i < n; i++ {
		prev := out[len(out)-1]
		out = append(out, indexer.Block{Height: prev.Height + 1, Hash: fmt.Sprintf("%s%d", tag, prev.Height+1), PrevHash: prev.Hash})
	}

	return out
}

func genesis(tag string) indexer.Block { return indexer.Block{Height: 0, Hash: tag + "0"} }

// fakeNode serves chain; calls counts the requests.
type fakeNode struct {
	chain      []indexer.Block
	err        error
	blockCalls int
	pageSizes  []int
}

func (n *fakeNode) Blocks(_ context.Context, from, limit int) ([]indexer.Block, error) {
	n.blockCalls++
	n.pageSizes = append(n.pageSizes, limit)

	if n.err != nil {
		return nil, n.err
	}

	if from >= len(n.chain) {
		return nil, nil
	}

	return n.chain[from:min(from+limit, len(n.chain))], nil
}

func (n *fakeNode) Headers(_ context.Context, from, limit int) ([]indexer.Header, error) {
	if from >= len(n.chain) {
		return nil, nil
	}

	var hs []indexer.Header
	for _, b := range n.chain[from:min(from+limit, len(n.chain))] {
		hs = append(hs, indexer.Header{Height: b.Height, Hash: b.Hash})
	}

	return hs, nil
}

// fakeStore keeps blocks in memory and enforces the same Apply contract as the SQL adapter.
type fakeStore struct {
	mu          sync.Mutex
	blocks      []indexer.Block
	applied     []int
	rollbacks   []int
	applyErr    error
	rollbackErr error
	hashesErr   error
}

func (s *fakeStore) Tip(context.Context) (indexer.Tip, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.blocks) == 0 {
		return indexer.Tip{}, false, nil
	}

	b := s.blocks[len(s.blocks)-1]

	return indexer.Tip{Height: b.Height, Hash: b.Hash}, true, nil
}

func (s *fakeStore) Hashes(_ context.Context, from, limit int) ([]string, error) {
	if s.hashesErr != nil {
		return nil, s.hashesErr
	}

	var out []string
	for h := from; h < from+limit && h < len(s.blocks); h++ {
		out = append(out, s.blocks[h].Hash)
	}

	return out, nil
}

func (s *fakeStore) Apply(_ context.Context, b indexer.Block) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.applyErr != nil {
		return s.applyErr
	}

	if len(s.blocks) == 0 && b.Height != 0 || len(s.blocks) > 0 && (b.Height != len(s.blocks) || b.PrevHash != s.blocks[len(s.blocks)-1].Hash) {
		return indexer.ErrNotNext
	}

	s.blocks = append(s.blocks, b)
	s.applied = append(s.applied, b.Height)

	return nil
}

func (s *fakeStore) Rollback(_ context.Context, to int) error {
	if s.rollbackErr != nil {
		return s.rollbackErr
	}

	s.rollbacks = append(s.rollbacks, to)
	s.blocks = s.blocks[:to+1]

	return nil
}

func newFollower(n indexer.Node, s indexer.Store) *indexer.Follower {
	return indexer.NewFollower(n, s, slog.New(slog.DiscardHandler), indexer.WithPollInterval(time.Millisecond))
}

func TestSyncAppliesFromEmptyAcrossPages(t *testing.T) {
	node := &fakeNode{chain: fakeChain("a", genesis("a"), 250)}
	store := &fakeStore{}

	err := newFollower(node, store).Sync(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(store.blocks) != 250 || store.blocks[249].Hash != "a249" {
		t.Fatalf("stored %d blocks", len(store.blocks))
	}

	for _, size := range node.pageSizes {
		if size != 100 {
			t.Fatalf("page size %d, want 100", size)
		}
	}

	if len(store.rollbacks) != 0 {
		t.Fatalf("unexpected rollbacks %v", store.rollbacks)
	}
}

func TestSyncResumesAtStoredTipAndAppliesNothingTwice(t *testing.T) {
	node := &fakeNode{chain: fakeChain("a", genesis("a"), 10)}
	store := &fakeStore{blocks: node.chain[:4]}

	f := newFollower(node, store)

	err := f.Sync(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	err = f.Sync(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	want := []int{4, 5, 6, 7, 8, 9}
	if fmt.Sprint(store.applied) != fmt.Sprint(want) {
		t.Fatalf("applied %v, want %v", store.applied, want)
	}
}

func TestSyncEmptyNodeAndEmptyStoreDoesNothing(t *testing.T) {
	store := &fakeStore{}

	err := newFollower(&fakeNode{}, store).Sync(t.Context())
	if err != nil || len(store.blocks) != 0 {
		t.Fatalf("err %v blocks %d", err, len(store.blocks))
	}
}

func TestSyncReorgs(t *testing.T) {
	tests := []struct {
		name       string
		forkAt     int // highest common height
		newLen     int // length of the new branch (number of blocks)
		oldLen     int
		wantRollTo int
	}{
		{"depth 1, higher", 8, 11, 10, 8},
		{"depth 3, higher", 6, 12, 10, 6},
		{"depth 3, equal height", 6, 10, 10, 6},
		{"depth 3, lower", 6, 9, 10, 6},
		{"to genesis", 0, 5, 10, 0},
		{"deeper than one header page", 1, 1500, 1400, 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			old := fakeChain("a", genesis("a"), tc.oldLen)
			branch := fakeChain("b", old[tc.forkAt], tc.newLen-tc.forkAt)
			newChain := append(append([]indexer.Block{}, old[:tc.forkAt]...), branch...)

			node := &fakeNode{chain: old}
			store := &fakeStore{}
			f := newFollower(node, store)

			err := f.Sync(t.Context())
			if err != nil {
				t.Fatal(err)
			}

			node.chain = newChain
			store.applied = nil

			err = f.Sync(t.Context())
			if err != nil {
				t.Fatal(err)
			}

			if len(store.blocks) != len(newChain) {
				t.Fatalf("stored %d blocks, want %d", len(store.blocks), len(newChain))
			}

			for i, b := range newChain {
				if store.blocks[i].Hash != b.Hash {
					t.Fatalf("height %d: %s, want %s", i, store.blocks[i].Hash, b.Hash)
				}
			}

			if len(store.rollbacks) != 1 || store.rollbacks[0] != tc.wantRollTo {
				t.Fatalf("rollbacks %v, want [%d]", store.rollbacks, tc.wantRollTo)
			}

			if len(store.applied) != 0 && store.applied[0] != tc.forkAt+1 {
				t.Fatalf("first re-applied height %d, want %d", store.applied[0], tc.forkAt+1)
			}
		})
	}
}

func TestSyncNodeShorterWithSamePrefixRollsBackToNodeTip(t *testing.T) {
	old := fakeChain("a", genesis("a"), 10)
	node := &fakeNode{chain: old[:6]}
	store := &fakeStore{blocks: append([]indexer.Block{}, old...)}

	err := newFollower(node, store).Sync(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(store.blocks) != 6 || fmt.Sprint(store.rollbacks) != "[5]" {
		t.Fatalf("blocks %d rollbacks %v", len(store.blocks), store.rollbacks)
	}
}

func TestSyncDifferentGenesisWritesNothing(t *testing.T) {
	store := &fakeStore{blocks: fakeChain("a", genesis("a"), 5)}
	node := &fakeNode{chain: fakeChain("z", genesis("z"), 8)}

	err := newFollower(node, store).Sync(t.Context())
	if !errors.Is(err, indexer.ErrDifferentChain) {
		t.Fatalf("err %v, want ErrDifferentChain", err)
	}

	if err.Error() != "different chain; run bwg indexer resync" {
		t.Fatalf("message %q", err)
	}

	if len(store.rollbacks) != 0 || len(store.applied) != 0 || len(store.blocks) != 5 {
		t.Fatalf("store changed: rollbacks %v applied %v", store.rollbacks, store.applied)
	}
}

func TestSyncDifferentGenesisNodeShorterThanStore(t *testing.T) {
	store := &fakeStore{blocks: fakeChain("a", genesis("a"), 2500)}
	node := &fakeNode{chain: fakeChain("z", genesis("z"), 3)}

	err := newFollower(node, store).Sync(t.Context())
	if !errors.Is(err, indexer.ErrDifferentChain) {
		t.Fatalf("err %v, want ErrDifferentChain", err)
	}
}

func TestSyncPrunedNodeStops(t *testing.T) {
	node := &fakeNode{err: indexer.ErrPruned}
	store := &fakeStore{}

	err := newFollower(node, store).Sync(t.Context())
	if !errors.Is(err, indexer.ErrPruned) {
		t.Fatalf("err %v", err)
	}

	if err.Error() != "the indexer needs a node that keeps every block body" {
		t.Fatalf("message %q", err)
	}

	if len(store.blocks) != 0 {
		t.Fatal("wrote after a pruned answer")
	}
}

func TestSyncErrors(t *testing.T) {
	boom := errors.New("boom")
	chainA := fakeChain("a", genesis("a"), 5)
	chainB := fakeChain("b", genesis("a"), 5) // same genesis, different tail

	tests := []struct {
		name  string
		node  *fakeNode
		store *fakeStore
	}{
		{"node blocks", &fakeNode{chain: chainA, err: boom}, &fakeStore{}},
		{"apply", &fakeNode{chain: chainA}, &fakeStore{applyErr: boom}},
		{"rollback", &fakeNode{chain: chainB}, &fakeStore{blocks: chainA[:5], rollbackErr: boom}},
		{"hashes", &fakeNode{chain: chainB}, &fakeStore{blocks: chainA[:5], hashesErr: boom}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := newFollower(tc.node, tc.store).Sync(t.Context())
			if !errors.Is(err, boom) {
				t.Fatalf("err %v, want boom", err)
			}
		})
	}
}

// A node that answers a page that does not chain although its headers agree with the store is
// inconsistent: Sync fails instead of looping, and Run retries on the next poll.
func TestSyncInconsistentPageFails(t *testing.T) {
	chainA := fakeChain("a", genesis("a"), 5)
	bad := append([]indexer.Block{}, chainA...)
	bad[4] = indexer.Block{Height: 4, Hash: "x4", PrevHash: "nope"}

	node := &pageNode{fakeNode: fakeNode{chain: chainA}, pages: [][]indexer.Block{{bad[4]}}}
	store := &fakeStore{blocks: chainA[:4]}

	err := newFollower(node, store).Sync(t.Context())
	if !errors.Is(err, indexer.ErrInconsistent) {
		t.Fatalf("err %v, want ErrInconsistent", err)
	}
}

// pageNode answers Blocks with canned pages, then falls back to the chain.
type pageNode struct {
	fakeNode

	pages [][]indexer.Block
}

func (n *pageNode) Blocks(ctx context.Context, from, limit int) ([]indexer.Block, error) {
	if len(n.pages) > 0 {
		p := n.pages[0]
		n.pages = n.pages[1:]

		return p, nil
	}

	return n.fakeNode.Blocks(ctx, from, limit)
}

func TestSyncPageStartingAtWrongHeight(t *testing.T) {
	chainA := fakeChain("a", genesis("a"), 5)
	node := &pageNode{fakeNode: fakeNode{chain: chainA}, pages: [][]indexer.Block{{chainA[2]}}}

	err := newFollower(node, &fakeStore{}).Sync(t.Context())
	if !errors.Is(err, indexer.ErrInconsistent) {
		t.Fatalf("err %v, want ErrInconsistent", err)
	}
}

func TestRunFollowsNewBlocksAndStopsOnCancel(t *testing.T) {
	node := &fakeNode{chain: fakeChain("a", genesis("a"), 3)}
	store := &fakeStore{}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)

	go func() { done <- newFollower(node, store).Run(ctx) }()

	deadline := time.After(5 * time.Second)

	for store.tipHeight() != 2 {
		select {
		case <-deadline:
			t.Fatal("tip never reached")
		case <-time.After(time.Millisecond):
		}
	}

	cancel()

	err := <-done
	if err != nil {
		t.Fatalf("Run returned %v on cancel", err)
	}
}

func (s *fakeStore) tipHeight() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.blocks) - 1
}

func TestRunRetriesNodeErrorsAndStopsOnFatal(t *testing.T) {
	node := &flakyNode{fakeNode: fakeNode{chain: fakeChain("a", genesis("a"), 3)}, failures: 3}
	store := &fakeStore{}

	// Three transient failures are retried; the fourth call works; then the node turns pruned.
	node.then = indexer.ErrPruned
	node.thenAfter = 5

	err := newFollower(node, store).Run(t.Context())
	if !errors.Is(err, indexer.ErrPruned) {
		t.Fatalf("err %v, want ErrPruned", err)
	}

	if len(store.blocks) != 3 {
		t.Fatalf("stored %d blocks before the fatal error", len(store.blocks))
	}
}

type flakyNode struct {
	fakeNode

	failures  int
	then      error
	thenAfter int
	calls     int
}

func (n *flakyNode) Blocks(ctx context.Context, from, limit int) ([]indexer.Block, error) {
	n.calls++

	if n.failures > 0 {
		n.failures--

		return nil, errors.New("connection refused")
	}

	if n.calls > n.thenAfter {
		return nil, n.then
	}

	return n.fakeNode.Blocks(ctx, from, limit)
}

func TestRunStopsOnStoreError(t *testing.T) {
	boom := errors.New("boom")
	node := &fakeNode{chain: fakeChain("a", genesis("a"), 3)}

	err := newFollower(node, &fakeStore{applyErr: boom}).Run(t.Context())
	if !errors.Is(err, boom) {
		t.Fatalf("err %v, want boom", err)
	}
}

func TestOptionsAreValidated(t *testing.T) {
	f := indexer.NewFollower(&fakeNode{}, &fakeStore{}, nil, indexer.WithPollInterval(0))
	if f == nil {
		t.Fatal("nil follower")
	}
}
