// Package storage defines the port through which the chain persists blocks.
package storage

import "errors"

// ErrEmpty is returned by Last when the repository holds no block.
var ErrEmpty = errors.New("storage: repository is empty")

// ErrConflict is returned by Commit, Reorg, ForkState and Restore when the
// blocks or the height do not fit the stored chain.
var ErrConflict = errors.New("storage: block does not extend the chain tip")

// ErrNotFound is returned by ByHeight and ByHash when no stored block matches.
var ErrNotFound = errors.New("storage: block not found")
