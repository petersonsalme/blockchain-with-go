// Package mempool holds admitted transactions that are not yet in a block and
// hands them out ordered by fee rate (ADR-0012).
package mempool

import (
	"cmp"
	"encoding/json"
	"errors"
	"math/bits"
	"slices"
	"sync"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// DefaultMaxTxs is the pool capacity when New receives a non-positive limit.
const DefaultMaxTxs = 5000

var (
	ErrDuplicate = errors.New("mempool: transaction already in the pool")
	ErrConflict  = errors.New("mempool: transaction spends an outpoint already spent in the pool")
	ErrFull      = errors.New("mempool: pool is full and the fee rate is too low")
)

type entry struct {
	tx   domain.Transaction
	fee  uint64
	size int
}

// Pool is a bounded set of non-conflicting transactions, safe for concurrent use.
type Pool struct {
	mu      sync.Mutex
	maxTxs  int
	ordered []entry // best first, see rank
	byID    map[string]struct{}
	spent   map[domain.OutPoint]string // outpoint -> ID of the pool transaction spending it
}

// New returns an empty pool holding at most maxTxs transactions
// (DefaultMaxTxs when maxTxs <= 0).
func New(maxTxs int) *Pool {
	if maxTxs <= 0 {
		maxTxs = DefaultMaxTxs
	}

	return &Pool{
		maxTxs: maxTxs,
		byID:   make(map[string]struct{}),
		spent:  make(map[domain.OutPoint]string),
	}
}

// Size is the serialized size of tx, the same encoding Block.Size measures.
func Size(tx domain.Transaction) int {
	b, _ := json.Marshal(tx) // Transaction holds only strings, integers and a time.Time: it cannot fail
	return len(b)
}

// rank orders a and b: negative when a ranks ahead of b. Higher fee per byte
// comes first, compared as fee_a*size_b against fee_b*size_a in 128 bits
// (no division, no floats); equal rates fall back to the lower ID.
func rank(a, b entry) int {
	ahi, alo := bits.Mul64(a.fee, uint64(b.size))
	bhi, blo := bits.Mul64(b.fee, uint64(a.size))

	return cmp.Or(cmp.Compare(bhi, ahi), cmp.Compare(blo, alo), cmp.Compare(a.tx.ID, b.tx.ID))
}

// Add admits tx with its precomputed fee (sum(in) - sum(out)). Signatures and
// UTXO existence are the caller's concern. The pool is unchanged on error.
func (p *Pool) Add(tx domain.Transaction, fee uint64) error {
	e := entry{tx: tx, fee: fee, size: Size(tx)}

	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.byID[tx.ID]; ok {
		return ErrDuplicate
	}

	for _, in := range tx.Inputs {
		if _, ok := p.spent[domain.OutPoint{TxID: in.PrevTxID, Index: in.OutIndex}]; ok {
			return ErrConflict
		}
	}

	i, _ := slices.BinarySearchFunc(p.ordered, e, rank)
	if len(p.ordered) >= p.maxTxs {
		if i == len(p.ordered) { // ranks after the last one
			return ErrFull
		}

		p.removeAt(len(p.ordered) - 1)
	}

	p.ordered = slices.Insert(p.ordered, i, e)

	p.byID[tx.ID] = struct{}{}
	for _, in := range tx.Inputs {
		p.spent[domain.OutPoint{TxID: in.PrevTxID, Index: in.OutIndex}] = tx.ID
	}

	return nil
}

// Select returns, in pool order, every transaction that still fits budget
// bytes, skipping the ones that do not. It does not remove them.
func (p *Pool) Select(budget int) []domain.Transaction {
	p.mu.Lock()
	defer p.mu.Unlock()

	var out []domain.Transaction

	for _, e := range p.ordered {
		if e.size > budget {
			continue
		}

		budget -= e.size
		out = append(out, e.tx)
	}

	return out
}

// Remove drops the transactions with the given IDs; unknown IDs are ignored.
func (p *Pool) Remove(ids ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	drop := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		drop[id] = struct{}{}
	}

	p.removeWhere(func(e entry) bool {
		_, ok := drop[e.tx.ID]
		return ok
	})
}

// Prune removes the transactions contained in b and every transaction with an
// input that view no longer holds.
func (p *Pool) Prune(b *domain.Block, view domain.UTXOView) {
	p.mu.Lock()
	defer p.mu.Unlock()

	mined := make(map[string]struct{}, len(b.Transactions))
	for _, tx := range b.Transactions {
		mined[tx.ID] = struct{}{}
	}

	p.removeWhere(func(e entry) bool {
		if _, ok := mined[e.tx.ID]; ok {
			return true
		}

		for _, in := range e.tx.Inputs {
			if _, ok := view.Lookup(domain.OutPoint{TxID: in.PrevTxID, Index: in.OutIndex}); !ok {
				return true
			}
		}

		return false
	})
}

// Len is the number of transactions in the pool.
func (p *Pool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return len(p.ordered)
}

// removeWhere deletes every entry matching drop. The caller holds p.mu.
func (p *Pool) removeWhere(drop func(entry) bool) {
	kept := p.ordered[:0]
	for _, e := range p.ordered {
		if drop(e) {
			p.forget(e)
			continue
		}

		kept = append(kept, e)
	}

	clear(p.ordered[len(kept):])
	p.ordered = kept
}

func (p *Pool) removeAt(i int) {
	p.forget(p.ordered[i])
	p.ordered = slices.Delete(p.ordered, i, i+1)
}

func (p *Pool) forget(e entry) {
	delete(p.byID, e.tx.ID)

	for _, in := range e.tx.Inputs {
		delete(p.spent, domain.OutPoint{TxID: in.PrevTxID, Index: in.OutIndex})
	}
}
