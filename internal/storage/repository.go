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

// ErrNotFound is returned by ByHeight and ByHash when no stored block matches.
var ErrNotFound = errors.New("storage: block not found")

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
	// ByHeight returns the block with Index h, or ErrNotFound.
	ByHeight(h int) (*domain.Block, error)
	// ByHash returns the block with the given Hash, or ErrNotFound.
	ByHash(hash string) (*domain.Block, error)
	// Range returns up to limit blocks starting at height from, oldest first.
	// It returns an empty slice when from is negative or past the tip, or limit
	// is not positive.
	Range(from, limit int) ([]*domain.Block, error)
}
