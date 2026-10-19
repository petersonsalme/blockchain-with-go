package domain

import (
	"errors"
	"math"

	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

var (
	ErrInputIndex          = errors.New("input index out of range")
	ErrInputMissingOrSpent = errors.New("input is missing or already spent")
	ErrDoubleSpend         = errors.New("outpoint spent or created more than once")
	ErrInsufficientInputs  = errors.New("outputs exceed inputs")
	ErrInputOwnerMismatch  = errors.New("input key does not own the spent output")
	ErrInvalidOutput       = errors.New("invalid output")
	ErrMissingInputs       = errors.New("transaction has no inputs")
)

// UTXOView is a read-only view of the unspent outputs.
type UTXOView interface {
	Lookup(OutPoint) (TxOut, bool)
}

// UTXODelta is the net effect of a block: outputs removed from and added to the set.
// Outputs created and spent inside the same block appear in neither map.
type UTXODelta struct {
	Spent   map[OutPoint]TxOut
	Created map[OutPoint]TxOut
}

// BlockDelta computes the effect of b on view without altering it. When
// isGenesis is true, transactions without inputs are accepted as allocations.
func BlockDelta(view UTXOView, b *Block, isGenesis bool) (UTXODelta, error) {
	if b == nil {
		return UTXODelta{}, errors.New("block is nil")
	}

	delta := UTXODelta{Spent: map[OutPoint]TxOut{}, Created: map[OutPoint]TxOut{}}
	seen := map[OutPoint]bool{}

	for i := range b.Transactions {
		tx := &b.Transactions[i]

		if len(tx.Outputs) == 0 {
			return UTXODelta{}, ErrInvalidOutput
		}
		if len(tx.Inputs) == 0 && !isGenesis {
			return UTXODelta{}, ErrMissingInputs
		}

		var in, out uint64
		for _, o := range tx.Outputs {
			if o.Amount == 0 || wallet.ValidateAddress(o.Address) != nil || o.Amount > math.MaxUint64-out {
				return UTXODelta{}, ErrInvalidOutput
			}
			out += o.Amount
		}

		for _, txIn := range tx.Inputs {
			op := OutPoint{TxID: txIn.PrevTxID, Index: txIn.OutIndex}
			if seen[op] {
				return UTXODelta{}, ErrDoubleSpend
			}
			seen[op] = true

			prev, ok := delta.Created[op]
			if ok {
				delete(delta.Created, op)
			} else if prev, ok = view.Lookup(op); ok {
				delta.Spent[op] = prev
			} else {
				return UTXODelta{}, ErrInputMissingOrSpent
			}

			if prev.Amount > math.MaxUint64-in {
				return UTXODelta{}, ErrInsufficientInputs
			}
			in += prev.Amount
		}

		if len(tx.Inputs) > 0 && out > in {
			return UTXODelta{}, ErrInsufficientInputs
		}

		txid := tx.CalculateHash()
		for j, o := range tx.Outputs {
			op := OutPoint{TxID: txid, Index: uint32(j)}
			if _, exists := delta.Created[op]; exists || seen[op] {
				return UTXODelta{}, ErrDoubleSpend
			}
			if _, exists := view.Lookup(op); exists {
				return UTXODelta{}, ErrDoubleSpend
			}
			delta.Created[op] = o
		}
	}

	return delta, nil
}

// UTXOSet is the set of unspent outputs.
type UTXOSet map[OutPoint]TxOut

// Lookup implements UTXOView.
func (s UTXOSet) Lookup(op OutPoint) (TxOut, bool) {
	out, ok := s[op]
	return out, ok
}

// Apply removes the spent outputs and adds the created ones.
func (s UTXOSet) Apply(d UTXODelta) {
	for op := range d.Spent {
		delete(s, op)
	}
	for op, out := range d.Created {
		s[op] = out
	}
}

// Revert undoes Apply.
func (s UTXOSet) Revert(d UTXODelta) {
	for op := range d.Created {
		delete(s, op)
	}
	for op, out := range d.Spent {
		s[op] = out
	}
}

// BuildUTXOSet replays blocks in order; only the first block, and only when
// its Index is 0, is the genesis, so a later Index 0 block cannot re-create
// allocations that were already spent.
func BuildUTXOSet(blocks []*Block) (UTXOSet, error) {
	set := UTXOSet{}
	for i, b := range blocks {
		d, err := BlockDelta(set, b, i == 0 && b != nil && b.Index == 0)
		if err != nil {
			return nil, err
		}
		set.Apply(d)
	}
	return set, nil
}
