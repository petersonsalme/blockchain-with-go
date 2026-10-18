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

// Repository keeps blocks in a slice guarded by a RWMutex.
type Repository struct {
	mu     sync.RWMutex
	blocks []*domain.Block
}

var _ storage.BlockRepository = (*Repository)(nil)

// New returns an empty Repository.
func New() *Repository {
	return &Repository{}
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

	r.blocks = append(r.blocks, b)
	return nil
}

func (r *Repository) All() ([]*domain.Block, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]*domain.Block, len(r.blocks))
	copy(out, r.blocks)
	return out, nil
}
