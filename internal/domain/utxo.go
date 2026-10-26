package domain

import (
	"errors"
	"maps"
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
	ErrCoinbaseTooLarge    = errors.New("coinbase outputs exceed subsidy plus fees")
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
// Otherwise the transaction at position 0 without inputs is the coinbase: its
// outputs may not exceed Subsidy(b.Index) plus the fees of the other
// transactions (ErrCoinbaseTooLarge); an input-less transaction anywhere else
// is ErrMissingInputs.
func BlockDelta(view UTXOView, b *Block, isGenesis bool) (UTXODelta, error) {
	if b == nil {
		return UTXODelta{}, errors.New("block is nil")
	}

	delta := UTXODelta{Spent: map[OutPoint]TxOut{}, Created: map[OutPoint]TxOut{}}
	seen := map[OutPoint]bool{}

	var fees, coinbaseOut uint64

	for i := range b.Transactions {
		tx := &b.Transactions[i]

		if len(tx.Outputs) == 0 {
			return UTXODelta{}, ErrInvalidOutput
		}

		coinbase := i == 0 && len(tx.Inputs) == 0 && !isGenesis
		if len(tx.Inputs) == 0 && !isGenesis && !coinbase {
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

		if len(tx.Inputs) > 0 {
			if out > in {
				return UTXODelta{}, ErrInsufficientInputs
			}

			fees = addSat(fees, in-out)
		}

		if coinbase {
			coinbaseOut = out
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

	if coinbaseOut > addSat(Subsidy(b.Index), fees) {
		return UTXODelta{}, ErrCoinbaseTooLarge
	}

	return delta, nil
}

// addSat adds a and b, capping at math.MaxUint64 instead of wrapping.
func addSat(a, b uint64) uint64 {
	if a > math.MaxUint64-b {
		return math.MaxUint64
	}

	return a + b
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

	maps.Copy(s, d.Created)
}

// Revert undoes Apply.
func (s UTXOSet) Revert(d UTXODelta) {
	for op := range d.Created {
		delete(s, op)
	}

	maps.Copy(s, d.Spent)
}

// DiffUTXO returns the delta that takes from to to: Spent holds the outputs of
// from that to lacks or holds differently, Created the outputs of to that from
// lacks or holds differently. Neither set is altered.
func DiffUTXO(from, to UTXOSet) UTXODelta {
	delta := UTXODelta{Spent: map[OutPoint]TxOut{}, Created: map[OutPoint]TxOut{}}

	for op, out := range from {
		if got, ok := to[op]; !ok || got != out {
			delta.Spent[op] = out
		}
	}

	for op, out := range to {
		if got, ok := from[op]; !ok || got != out {
			delta.Created[op] = out
		}
	}

	return delta
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
