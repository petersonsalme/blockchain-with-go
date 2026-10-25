package memory

import (
	"cmp"
	"slices"
	"strings"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
)

var _ storage.IndexedRepository = (*Repository)(nil)

// addrEntry is one transaction that touched an address.
type addrEntry struct {
	height, pos int
	txid        string
}

// index is the state Commit, Reorg and Reindex keep next to the blocks.
type index struct {
	utxos domain.UTXOSet
	// addrutxo is utxos grouped by address.
	addrutxo map[string]map[domain.OutPoint]uint64
	undo     map[int]domain.UTXODelta
	txs      map[string]int
	addrtx   map[string][]addrEntry
	// snap is the stored snapshot, or nil; it is replaced, never edited in place.
	snap *storage.Snapshot
	// prunedBelow is the lowest height that still has a body and undo data.
	prunedBelow int
	// tip is the height the index describes; it is only meaningful while hasTip is set.
	tip    int
	hasTip bool
}

func newIndex() index {
	return index{
		utxos:    domain.UTXOSet{},
		addrutxo: map[string]map[domain.OutPoint]uint64{},
		undo:     map[int]domain.UTXODelta{},
		txs:      map[string]int{},
		addrtx:   map[string][]addrEntry{},
	}
}

func cloneOuts(m map[domain.OutPoint]domain.TxOut) map[domain.OutPoint]domain.TxOut {
	out := make(map[domain.OutPoint]domain.TxOut, len(m))
	for op, o := range m {
		out[op] = o
	}
	return out
}

func (ix index) clone() index {
	c := newIndex()
	c.utxos = domain.UTXOSet(cloneOuts(ix.utxos))
	for a, outs := range ix.addrutxo {
		m := make(map[domain.OutPoint]uint64, len(outs))
		for op, amount := range outs {
			m[op] = amount
		}
		c.addrutxo[a] = m
	}
	for h, d := range ix.undo {
		c.undo[h] = domain.UTXODelta{Spent: cloneOuts(d.Spent), Created: cloneOuts(d.Created)}
	}
	for id, h := range ix.txs {
		c.txs[id] = h
	}
	for a, entries := range ix.addrtx {
		c.addrtx[a] = append([]addrEntry(nil), entries...)
	}
	c.tip, c.hasTip = ix.tip, ix.hasTip
	c.snap, c.prunedBelow = ix.snap, ix.prunedBelow
	return c
}

// holds reports whether every output in outs is in the set with the same value.
func holds(set domain.UTXOSet, outs map[domain.OutPoint]domain.TxOut) bool {
	for op, want := range outs {
		if got, ok := set[op]; !ok || got != want {
			return false
		}
	}
	return true
}

// intersects reports whether any outpoint in outs is in the set.
func intersects(set domain.UTXOSet, outs map[domain.OutPoint]domain.TxOut) bool {
	for op := range outs {
		if _, ok := set[op]; ok {
			return true
		}
	}
	return false
}

// apply adds block b with delta d, or returns ErrUTXOConflict and changes nothing.
func (ix *index) apply(b *domain.Block, d domain.UTXODelta) error {
	if !holds(ix.utxos, d.Spent) || intersects(ix.utxos, d.Created) {
		return storage.ErrUTXOConflict
	}
	ix.applyDelta(d)
	ix.undo[b.Index] = domain.UTXODelta{Spent: cloneOuts(d.Spent), Created: cloneOuts(d.Created)}
	for _, t := range storage.IndexTxs(b, d) {
		ix.txs[t.TxID] = b.Index
		for _, a := range t.Addrs {
			ix.addrtx[a] = append(ix.addrtx[a], addrEntry{height: b.Index, pos: t.Pos, txid: t.TxID})
		}
	}
	ix.tip, ix.hasTip = b.Index, true
	return nil
}

// revert removes block b (the tip), or returns ErrUTXOConflict and changes nothing.
// It leaves tip alone: Reorg always applies at least one block afterwards, which sets it.
func (ix *index) revert(b *domain.Block) error {
	d := ix.undo[b.Index]
	if !holds(ix.utxos, d.Created) || intersects(ix.utxos, d.Spent) {
		return storage.ErrUTXOConflict
	}
	ix.revertDelta(d)
	delete(ix.undo, b.Index)
	ix.dropTxs(b, d)
	return nil
}

// dropTxs removes the txs and addrtx entries of block b, whose delta is d.
func (ix *index) dropTxs(b *domain.Block, d domain.UTXODelta) {
	for _, t := range storage.IndexTxs(b, d) {
		delete(ix.txs, t.TxID)
		for _, a := range t.Addrs {
			kept := ix.addrtx[a][:0]
			for _, e := range ix.addrtx[a] {
				if e.height != b.Index {
					kept = append(kept, e)
				}
			}
			if len(kept) == 0 {
				delete(ix.addrtx, a)
			} else {
				ix.addrtx[a] = kept
			}
		}
	}
}

// dropSnapshotFrom discards the snapshot when its height is >= height: it describes a dropped branch.
func (ix *index) dropSnapshotFrom(height int) {
	if ix.snap != nil && ix.snap.Height >= height {
		ix.snap = nil
	}
}

// applyDelta moves utxos and addrutxo together, like domain.UTXOSet.Apply.
func (ix *index) applyDelta(d domain.UTXODelta) {
	for op, o := range d.Spent {
		ix.dropOut(op, o)
	}
	for op, o := range d.Created {
		ix.putOut(op, o)
	}
}

// revertDelta undoes applyDelta, like domain.UTXOSet.Revert.
func (ix *index) revertDelta(d domain.UTXODelta) {
	for op, o := range d.Created {
		ix.dropOut(op, o)
	}
	for op, o := range d.Spent {
		ix.putOut(op, o)
	}
}

func (ix *index) putOut(op domain.OutPoint, o domain.TxOut) {
	ix.utxos[op] = o
	if ix.addrutxo[o.Address] == nil {
		ix.addrutxo[o.Address] = map[domain.OutPoint]uint64{}
	}
	ix.addrutxo[o.Address][op] = o.Amount
}

func (ix *index) dropOut(op domain.OutPoint, o domain.TxOut) {
	delete(ix.utxos, op)
	delete(ix.addrutxo[o.Address], op)
	if len(ix.addrutxo[o.Address]) == 0 {
		delete(ix.addrutxo, o.Address)
	}
}

// snapshotStep stores the snapshot that the Commit of height t calls for and,
// with p.KeepBlocks > 0, prunes the bodies up to it. chain holds the blocks
// 0..t; pruned entries are replaced by headers in chain.
func (ix *index) snapshotStep(p storage.Pruning, chain []*domain.Block, t int) {
	s, ok := p.SnapshotHeight(t)
	if !ok || s < ix.prunedBelow {
		return
	}
	set := domain.UTXOSet(cloneOuts(ix.utxos))
	for h := t; h > s; h-- {
		set.Revert(ix.undo[h])
	}
	hash := chain[s].Hash
	ix.snap = &storage.Snapshot{Height: s, BlockHash: hash, Hash: storage.SnapshotHash(hash, set), UTXOs: set}
	if p.KeepBlocks == 0 {
		return
	}
	for h := ix.prunedBelow; h <= s; h++ {
		ix.dropTxs(chain[h], ix.undo[h])
		delete(ix.undo, h)
		header := *chain[h]
		header.Transactions = []domain.Transaction{}
		chain[h] = &header
	}
	ix.prunedBelow = s + 1
}

// current reports whether the index describes the tip of blocks.
func (r *Repository) current() bool {
	return len(r.blocks) == 0 || (r.idx.hasTip && r.idx.tip == len(r.blocks)-1)
}

// Lookup reads the UTXO set kept by Commit, Reorg and Reindex.
func (r *Repository) Lookup(op domain.OutPoint) (domain.TxOut, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.idx.utxos.Lookup(op)
}

// Commit appends b and applies d to the UTXO set and the indexes, under one lock.
func (r *Repository) Commit(b *domain.Block, d domain.UTXODelta) error {
	if b == nil {
		return ErrNilBlock
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.current() {
		return storage.ErrIndexStale
	}
	if n := len(r.blocks); n > 0 {
		last := r.blocks[n-1]
		if b.Index != last.Index+1 || b.PrevHash != last.Hash {
			return storage.ErrConflict
		}
	}
	if err := r.idx.apply(b, d); err != nil {
		return err
	}
	r.blocks = append(r.blocks, b)
	r.byHash[b.Hash] = b
	pruned := r.idx.prunedBelow
	r.idx.snapshotStep(r.pruning, r.blocks, b.Index)
	r.rehash(r.blocks, pruned, r.idx.prunedBelow)
	return nil
}

// rehash points byHash at the headers that replaced the bodies in [from, to).
func (r *Repository) rehash(chain []*domain.Block, from, to int) {
	for _, b := range chain[from:to] {
		r.byHash[b.Hash] = b
	}
}

// Reorg swaps the chain tail and its indexes under one lock, on copies that replace the live state only on success.
func (r *Repository) Reorg(height int, blocks []*domain.Block, deltas []domain.UTXODelta) error {
	for _, b := range blocks {
		if b == nil {
			return ErrNilBlock
		}
	}
	if len(deltas) != len(blocks) {
		return storage.ErrConflict
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.checkSplice(height, blocks); err != nil {
		return err
	}
	if height < r.idx.prunedBelow {
		return storage.ErrReorgTooDeep
	}
	if !r.current() {
		return storage.ErrIndexStale
	}

	ix := r.idx.clone()
	for i := len(r.blocks) - 1; i >= height; i-- {
		if err := ix.revert(r.blocks[i]); err != nil {
			return err
		}
	}
	ix.dropSnapshotFrom(height)
	// The cap limit forces append to copy; the pruning below edits only this copy.
	chain := append(r.blocks[:height:height], blocks...)
	for i, b := range blocks {
		if err := ix.apply(b, deltas[i]); err != nil {
			return err
		}
		ix.snapshotStep(r.pruning, chain, b.Index)
	}

	pruned := r.idx.prunedBelow
	r.splice(height, blocks)
	r.blocks = chain
	r.rehash(chain, pruned, ix.prunedBelow)
	r.idx = ix
	return nil
}

func (r *Repository) TxHeight(txid string) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	h, ok := r.idx.txs[txid]
	if !ok {
		return 0, storage.ErrNotFound
	}
	return h, nil
}

func (r *Repository) TxsByAddress(addr string) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entries := r.idx.addrtx[addr]
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.txid
	}
	return out, nil
}

func (r *Repository) IndexCurrent() (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.current(), nil
}

// Reindex rebuilds the UTXO set, undo data and indexes from the blocks, under
// one lock. A pruned repository starts from its snapshot instead of the genesis.
func (r *Repository) Reindex() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	ix := newIndex()
	ix.snap, ix.prunedBelow = r.idx.snap, r.idx.prunedBelow
	from := 0
	if ix.prunedBelow > 0 {
		ix.applyDelta(domain.UTXODelta{Created: cloneOuts(ix.snap.UTXOs)})
		from = ix.prunedBelow
	}
	for _, b := range r.blocks[from:] {
		d, err := domain.BlockDelta(ix.utxos, b, b.Index == 0)
		if err != nil {
			return err
		}
		if err := ix.apply(b, d); err != nil {
			return err
		}
	}
	r.idx = ix
	return nil
}

// Snapshot returns a copy of the stored snapshot, or storage.ErrNotFound.
func (r *Repository) Snapshot() (storage.Snapshot, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.idx.snap == nil {
		return storage.Snapshot{}, storage.ErrNotFound
	}
	snap := *r.idx.snap
	snap.UTXOs = domain.UTXOSet(cloneOuts(snap.UTXOs))
	return snap, nil
}

func (r *Repository) PrunedBelow() (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.idx.prunedBelow, nil
}

func (r *Repository) UTXOsByAddress(addr string) ([]storage.AddressUTXO, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]storage.AddressUTXO, 0, len(r.idx.addrutxo[addr]))
	for op, amount := range r.idx.addrutxo[addr] {
		out = append(out, storage.AddressUTXO{OutPoint: op, Amount: amount})
	}
	slices.SortFunc(out, func(a, b storage.AddressUTXO) int {
		return cmp.Or(strings.Compare(a.OutPoint.TxID, b.OutPoint.TxID), cmp.Compare(a.OutPoint.Index, b.OutPoint.Index))
	})
	return out, nil
}

// ForkState reverts the stored undo data from the tip down to height on a copy of the UTXO set.
func (r *Repository) ForkState(height int) (domain.UTXOSet, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if height < 0 || height > len(r.blocks) {
		return nil, storage.ErrConflict
	}
	if height < r.idx.prunedBelow {
		return nil, storage.ErrReorgTooDeep
	}
	if !r.current() {
		return nil, storage.ErrIndexStale
	}
	set := domain.UTXOSet(cloneOuts(r.idx.utxos))
	for h := len(r.blocks); h > height; h-- {
		set.Revert(r.idx.undo[h-1])
	}
	return set, nil
}

// Restore fills an empty repository from a snapshot, under one lock; see storage.IndexedRepository.
func (r *Repository) Restore(headers []*domain.Block, set domain.UTXOSet) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.blocks) > 0 {
		return storage.ErrConflict
	}
	if err := storage.CheckHeaders(headers); err != nil {
		return err
	}
	ix := newIndex()
	ix.applyDelta(domain.UTXODelta{Created: cloneOuts(set)})
	s := len(headers) - 1
	ix.snap = &storage.Snapshot{Height: s, BlockHash: headers[s].Hash, Hash: storage.SnapshotHash(headers[s].Hash, set), UTXOs: domain.UTXOSet(cloneOuts(set))}
	ix.prunedBelow = s + 1
	ix.tip, ix.hasTip = s, true

	blocks := make([]*domain.Block, len(headers))
	byHash := make(map[string]*domain.Block, len(headers))
	for i, h := range headers {
		header := *h
		header.Transactions = []domain.Transaction{}
		blocks[i] = &header
		byHash[header.Hash] = &header
	}
	r.blocks, r.byHash, r.idx = blocks, byHash, ix
	return nil
}
