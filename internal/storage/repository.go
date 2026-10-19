// Package storage defines the port through which the chain persists blocks.
package storage

import (
	"errors"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// ErrEmpty is returned by Last when the repository holds no block.
var ErrEmpty = errors.New("storage: repository is empty")

// ErrConflict is returned by Append when the block does not extend the current last block.
var ErrConflict = errors.New("storage: block does not extend the chain tip")

// BlockRepository stores the blocks of a chain in append order.
// Implementations must be safe for concurrent use.
type BlockRepository interface {
	// Last returns the most recently appended block, or ErrEmpty.
	Last() (*domain.Block, error)
	// Append adds b at the end of the chain. An empty repository accepts any
	// block; otherwise b must satisfy b.Index == last.Index+1 and
	// b.PrevHash == last.Hash, or Append returns ErrConflict and changes nothing.
	// The check and the append are atomic.
	Append(b *domain.Block) error
	// All returns a snapshot of the chain, oldest block first.
	All() ([]*domain.Block, error)
}
