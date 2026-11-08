package indexer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

const (
	blockPage  = 100  // the node caps GET /blocks at 100
	headerPage = 1000 // the node caps GET /headers at 1000
	// DefaultPollInterval is how long Run waits when the store is level with the node.
	DefaultPollInterval = time.Second
)

// nodeError marks an error that came from reading the node: Run retries it (unless it is
// ErrPruned), while a store error stops the indexer.
type nodeError struct{ err error }

func (e nodeError) Error() string { return e.err.Error() }
func (e nodeError) Unwrap() error { return e.err }

// Follower copies the canonical chain of a node into a store.
type Follower struct {
	node     Node
	store    Store
	log      *slog.Logger
	interval time.Duration
}

// Option configures a Follower.
type Option func(*Follower)

// WithPollInterval sets the wait between polls once the store is level with the node.
func WithPollInterval(d time.Duration) Option {
	return func(f *Follower) {
		if d > 0 {
			f.interval = d
		}
	}
}

// NewFollower returns a Follower over node and store; a nil logger discards.
func NewFollower(node Node, store Store, log *slog.Logger, opts ...Option) *Follower {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	f := &Follower{node: node, store: store, log: log, interval: DefaultPollInterval}
	for _, o := range opts {
		o(f)
	}

	return f
}

// Run syncs, waits one poll interval and repeats until ctx is done (nil). Errors reading the
// node are logged and retried, except ErrPruned; every other error stops it.
func (f *Follower) Run(ctx context.Context) error {
	for {
		err := f.Sync(ctx)
		if ctx.Err() != nil {
			return nil //nolint:nilerr // cancelling the context is the normal way to stop Run
		}

		if err != nil {
			if !errors.Is(err, ErrInconsistent) && (!errors.As(err, &nodeError{}) || errors.Is(err, ErrPruned)) {
				return err
			}

			f.log.Warn("sync failed, retrying", "error", err)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(f.interval):
		}
	}
}

// Sync applies blocks until the store is level with the node, rolling back first when the
// stored chain forked from the node's.
func (f *Follower) Sync(ctx context.Context) error {
	for {
		tip, ok, err := f.store.Tip(ctx)
		if err != nil {
			return err
		}

		from := 0
		if ok {
			from = tip.Height + 1
		}

		blocks, err := f.node.Blocks(ctx, from, blockPage)
		if err != nil {
			return nodeError{err}
		}

		if len(blocks) == 0 {
			if !ok {
				return nil
			}

			level, err := f.level(ctx, tip)
			if err != nil {
				return err
			}

			if level {
				return nil
			}

			err = f.rollbackToFork(ctx, tip)
			if err != nil {
				return err
			}

			continue
		}

		if blocks[0].Height != from {
			return fmt.Errorf("%w: asked for height %d, got %d", ErrInconsistent, from, blocks[0].Height)
		}

		if ok && blocks[0].PrevHash != tip.Hash {
			err := f.rollbackToFork(ctx, tip)
			if err != nil {
				return err
			}

			continue
		}

		progressed, err := f.apply(ctx, blocks)
		if err != nil {
			return err
		}

		if !progressed {
			// A page that does not chain although the stored tip is the node's block at that
			// height: the node answered two requests from two different chains.
			return fmt.Errorf("%w: page at height %d does not chain on the stored tip", ErrInconsistent, from)
		}
	}
}

// apply stores blocks in order; it stops at the first block that does not chain, reporting
// whether it stored at least one block or found the chain moved (rollback done by the caller's loop).
func (f *Follower) apply(ctx context.Context, blocks []Block) (bool, error) {
	for i, b := range blocks {
		err := f.store.Apply(ctx, b)
		if errors.Is(err, ErrNotNext) {
			return i > 0, nil
		}

		if err != nil {
			return false, err
		}

		f.log.Debug("block applied", "height", b.Height, "hash", b.Hash)
	}

	return true, nil
}

// level reports whether the node's header at the stored tip is the stored tip.
func (f *Follower) level(ctx context.Context, tip Tip) (bool, error) {
	hs, err := f.node.Headers(ctx, tip.Height, 1)
	if err != nil {
		return false, nodeError{err}
	}

	return len(hs) == 1 && hs[0].Height == tip.Height && hs[0].Hash == tip.Hash, nil
}

// rollbackToFork finds the highest height F at which the stored hash is the node's and rolls the
// store back to it. No common height at all is ErrDifferentChain, with nothing written.
func (f *Follower) rollbackToFork(ctx context.Context, tip Tip) error {
	fork, err := f.forkPoint(ctx, tip.Height)
	if err != nil {
		return err
	}

	if fork == tip.Height {
		return fmt.Errorf("%w: the node's block at height %d is the stored one but its successor is not", ErrInconsistent, fork)
	}

	f.log.Info("reorg: rolling back", "from", tip.Height, "to", fork)

	return f.store.Rollback(ctx, fork)
}

// forkPoint walks back over /headers pages, descending, to the highest height <= top where the
// stored hash equals the node's.
func (f *Follower) forkPoint(ctx context.Context, top int) (int, error) {
	for hi := top; hi >= 0; {
		lo := max(0, hi-headerPage+1)

		hs, err := f.node.Headers(ctx, lo, hi-lo+1)
		if err != nil {
			return 0, nodeError{err}
		}

		stored, err := f.store.Hashes(ctx, lo, len(hs))
		if err != nil {
			return 0, err
		}

		for i := min(len(hs), len(stored)) - 1; i >= 0; i-- {
			if hs[i].Height != lo+i {
				return 0, fmt.Errorf("%w: header %d at height %d", ErrInconsistent, hs[i].Height, lo+i)
			}

			if hs[i].Hash == stored[i] {
				return lo + i, nil
			}
		}

		hi = lo - 1
	}

	return 0, ErrDifferentChain
}
