package memory

import (
	"sync"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
)

// UTXOSet keeps the unspent outputs in a map guarded by a RWMutex.
type UTXOSet struct {
	mu  sync.RWMutex
	set domain.UTXOSet
}

var _ storage.UTXOSet = (*UTXOSet)(nil)

// NewUTXOSet returns an empty UTXOSet.
func NewUTXOSet() *UTXOSet {
	return &UTXOSet{set: domain.UTXOSet{}}
}

func (u *UTXOSet) Lookup(op domain.OutPoint) (domain.TxOut, bool) {
	u.mu.RLock()
	defer u.mu.RUnlock()

	return u.set.Lookup(op)
}

func (u *UTXOSet) Apply(d domain.UTXODelta) error {
	u.mu.Lock()
	defer u.mu.Unlock()

	if !u.holds(d.Spent) || u.intersects(d.Created) {
		return storage.ErrUTXOConflict
	}
	u.set.Apply(d)
	return nil
}

func (u *UTXOSet) Revert(d domain.UTXODelta) error {
	u.mu.Lock()
	defer u.mu.Unlock()

	if !u.holds(d.Created) || u.intersects(d.Spent) {
		return storage.ErrUTXOConflict
	}
	u.set.Revert(d)
	return nil
}

func (u *UTXOSet) Snapshot() domain.UTXOSet {
	u.mu.RLock()
	defer u.mu.RUnlock()

	out := make(domain.UTXOSet, len(u.set))
	for op, txOut := range u.set {
		out[op] = txOut
	}
	return out
}

// holds reports whether every output in outs is in the set with the same value.
func (u *UTXOSet) holds(outs map[domain.OutPoint]domain.TxOut) bool {
	for op, want := range outs {
		if got, ok := u.set[op]; !ok || got != want {
			return false
		}
	}
	return true
}

// intersects reports whether any outpoint in outs is in the set.
func (u *UTXOSet) intersects(outs map[domain.OutPoint]domain.TxOut) bool {
	for op := range outs {
		if _, ok := u.set[op]; ok {
			return true
		}
	}
	return false
}
