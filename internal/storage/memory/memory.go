// Package memory is an in-memory storage.BlockRepository.
package memory

import (
	"errors"
	"sync"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
)

// ErrNilBlock is returned by Append when given a nil block.
var ErrNilBlock = errors.New("memory: nil block")

// Repository keeps blocks in a slice, indexed by hash, guarded by a RWMutex.
type Repository struct {
	mu     sync.RWMutex
	blocks []*domain.Block
	byHash map[string]*domain.Block
}

var _ storage.BlockRepository = (*Repository)(nil)

// New returns an empty Repository.
func New() *Repository {
	return &Repository{byHash: map[string]*domain.Block{}}
}

func (r *Repository) Last() (*domain.Block, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.blocks) == 0 {
		return nil, storage.ErrEmpty
	}
	return r.blocks[len(r.blocks)-1], nil
}

func (r *Repository) Append(b *domain.Block) error {
	if b == nil {
		return ErrNilBlock
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if n := len(r.blocks); n > 0 {
		last := r.blocks[n-1]
		if b.Index != last.Index+1 || b.PrevHash != last.Hash {
			return storage.ErrConflict
		}
	}

	r.blocks = append(r.blocks, b)
	r.byHash[b.Hash] = b
	return nil
}

func (r *Repository) All() ([]*domain.Block, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]*domain.Block, len(r.blocks))
	copy(out, r.blocks)
	return out, nil
}

func (r *Repository) ByHeight(h int) (*domain.Block, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if h < 0 || h >= len(r.blocks) {
		return nil, storage.ErrNotFound
	}
	return r.blocks[h], nil
}

func (r *Repository) ByHash(hash string) (*domain.Block, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	b, ok := r.byHash[hash]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return b, nil
}

func (r *Repository) Range(from, limit int) ([]*domain.Block, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if from < 0 {
		return []*domain.Block{}, nil
	}
	// Bounded by the chain length, not from+limit: that overflows for a huge limit.
	n := max(0, min(limit, len(r.blocks)-from))
	if n == 0 {
		return []*domain.Block{}, nil
	}
	out := make([]*domain.Block, n)
	copy(out, r.blocks[from:from+n])
	return out, nil
}
