// Package indexer follows the canonical chain of one node through its public API and keeps a
// store (Postgres, ADR-0038) equal to it across reorgs, crashes and restarts. It holds no SQL:
// the follower works over two ports, the node reader and the store.
package indexer

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrDifferentChain stops the indexer when the node and the store have no common block, not
	// even the genesis.
	ErrDifferentChain = errors.New("different chain; run bwg indexer resync")
	// ErrPruned stops the indexer when the node no longer holds a block body it needs (HTTP 410).
	ErrPruned = errors.New("the indexer needs a node that keeps every block body")
	// ErrNotNext is returned by Store.Apply, writing nothing, when the block is not the child of
	// the stored tip.
	ErrNotNext = errors.New("indexer: block does not extend the stored tip")
	// ErrInconsistent is a node answer that contradicts itself between two requests; the next
	// poll asks again.
	ErrInconsistent = errors.New("indexer: inconsistent answer from the node")
)

// Block is a block as the indexer needs it.
type Block struct {
	Height   int
	Hash     string
	PrevHash string
	Time     time.Time
	Txs      []Tx
}

// Tx is a transaction; one without inputs creates coins (a coinbase or a genesis allocation).
type Tx struct {
	ID      string
	Inputs  []OutRef
	Outputs []Out
}

// OutRef points at the output (TxID, Index).
type OutRef struct {
	TxID  string
	Index uint32
}

// Out is an output.
type Out struct {
	Address string
	Amount  uint64
}

// Header is the (height, hash) of a block.
type Header struct {
	Height int
	Hash   string
}

// Tip is the last fully applied block.
type Tip struct {
	Height int
	Hash   string
}

// Node reads the canonical chain of one node.
type Node interface {
	// Blocks returns up to limit blocks from height from, read consistently by the node.
	Blocks(ctx context.Context, from, limit int) ([]Block, error)
	// Headers returns up to limit headers from height from.
	Headers(ctx context.Context, from, limit int) ([]Header, error)
}

// Store is the database. Apply and Rollback are each one transaction; the tip is the last row of
// the blocks it holds, never stored apart from the data it describes.
type Store interface {
	// Tip returns the stored tip; ok is false when nothing is stored.
	Tip(ctx context.Context) (tip Tip, ok bool, err error)
	// Hashes returns the stored hashes of heights from.. (at most limit, fewer past the tip).
	Hashes(ctx context.Context, from, limit int) ([]string, error)
	// Apply stores b, or returns ErrNotNext and writes nothing when b is not tip+1 chained on the tip.
	Apply(ctx context.Context, b Block) error
	// Rollback removes every height above to, restoring what those blocks spent.
	Rollback(ctx context.Context, to int) error
}
