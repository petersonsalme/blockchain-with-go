package storage

import (
	"errors"
	"sort"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// ErrIndexStale is returned by Commit and Reorg when the persisted indexes do
// not describe the current tip (the chain was written with Append or
// ReplaceFrom, or the database predates the indexes). Reindex repairs it.
var ErrIndexStale = errors.New("storage: indexes are stale, run Reindex")

// IndexedRepository is a BlockRepository that also persists the UTXO set, the
// per-block undo data and two lookup indexes (txid -> height and
// address -> transactions), all updated atomically with the blocks.
// Implementations must be safe for concurrent use.
//
// Append and ReplaceFrom keep their BlockRepository behaviour and do not touch
// the indexes; after ReplaceFrom, or an Append that moves the tip, IndexCurrent
// reports false until Reindex runs. Commit and Reorg refuse to run on stale
// indexes (ErrIndexStale).
type IndexedRepository interface {
	BlockRepository
	// Lookup reads the persisted UTXO set.
	domain.UTXOView
	// Commit appends b and applies d in one atomic step: the tip check of
	// Append (ErrConflict), the check of UTXOSet.Apply (ErrUTXOConflict), the
	// block, its undo data, the UTXO set and the indexes. A nil block returns
	// the adapter's ErrNilBlock. Any rejection changes nothing.
	Commit(b *domain.Block, d domain.UTXODelta) error
	// Reorg reverts, from the tip down to height, the stored deltas, drops
	// those blocks and their index entries, then commits each of blocks with
	// the matching delta of deltas, in one atomic step. It has the
	// preconditions of ReplaceFrom plus len(deltas) == len(blocks), otherwise
	// ErrConflict. A rejection changes nothing.
	Reorg(height int, blocks []*domain.Block, deltas []domain.UTXODelta) error
	// TxHeight returns the height of the block holding txid, or ErrNotFound.
	TxHeight(txid string) (int, error)
	// TxsByAddress returns the ids of the transactions that created or spent
	// an output of addr, oldest first (by height, then position in the block).
	// It returns an empty, non-nil slice when there are none.
	TxsByAddress(addr string) ([]string, error)
	// IndexCurrent reports whether the indexes describe the current tip. An
	// empty repository is current.
	IndexCurrent() (bool, error)
	// Reindex discards the UTXO set, undo data and indexes and rebuilds them
	// from the blocks, atomically. It returns the error of the first block
	// that does not replay, leaving the repository unchanged.
	Reindex() error
}

// TxIndex describes one transaction of a block for the indexes: its id, its
// position in the block and the sorted, distinct addresses of the outputs it
// creates and of the outputs it spends.
type TxIndex struct {
	TxID  string
	Pos   int
	Addrs []string
}

// IndexTxs lists the transactions of b with the addresses they touch. d is the
// delta of b: it resolves the outputs spent from before the block, while
// outputs created and spent inside b are resolved from the block itself.
func IndexTxs(b *domain.Block, d domain.UTXODelta) []TxIndex {
	local := map[domain.OutPoint]domain.TxOut{}
	out := make([]TxIndex, 0, len(b.Transactions))
	for pos := range b.Transactions {
		tx := &b.Transactions[pos]
		txid := tx.CalculateHash()
		addrs := map[string]struct{}{}
		for _, in := range tx.Inputs {
			op := domain.OutPoint{TxID: in.PrevTxID, Index: in.OutIndex}
			if o, ok := d.Spent[op]; ok {
				addrs[o.Address] = struct{}{}
			} else if o, ok := local[op]; ok {
				addrs[o.Address] = struct{}{}
			}
		}
		for i, o := range tx.Outputs {
			addrs[o.Address] = struct{}{}
			local[domain.OutPoint{TxID: txid, Index: uint32(i)}] = o
		}
		list := make([]string, 0, len(addrs))
		for a := range addrs {
			list = append(list, a)
		}
		sort.Strings(list)
		out = append(out, TxIndex{TxID: txid, Pos: pos, Addrs: list})
	}
	return out
}
