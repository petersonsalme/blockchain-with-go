package bolt

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"encoding/json"
	"slices"
	"strings"

	bbolt "go.etcd.io/bbolt"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
)

// Index buckets, all written in the same transaction as the blocks they describe:
//
//	utxos   txid|index (4B BE)                 -> JSON TxOut
//	undo    height (8B BE)                     -> JSON delta of that block
//	txs     txid                               -> height (8B BE)
//	addrtx  address|height (8B BE)|pos (4B BE) -> txid
//	addrutxo address|txid|index (4B BE)        -> amount (8B BE)
//	snapshot txid|index (4B BE)                -> JSON TxOut, the UTXO set at meta.snapshotHeight
//	meta    "indexedTip"                       -> height (8B BE) the indexes describe
//	        "snapshotHeight"                   -> height (8B BE) of the snapshot
//	        "snapshotHash"                     -> storage.SnapshotHash, hex
//	        "prunedBelow"                      -> lowest height with a body (8B BE)
var (
	utxosBucket  = []byte("utxos")
	undoBucket   = []byte("undo")
	txsBucket    = []byte("txs")
	addrtxBucket = []byte("addrtx")
	metaBucket   = []byte("meta")

	addrutxoBucket = []byte("addrutxo")
	snapshotBucket = []byte("snapshot")

	indexedTipKey     = []byte("indexedTip")
	snapshotHeightKey = []byte("snapshotHeight")
	snapshotHashKey   = []byte("snapshotHash")
	prunedBelowKey    = []byte("prunedBelow")
)

const addrSep = '|'

var _ storage.IndexedRepository = (*Repository)(nil)

type outDTO struct {
	TxID    string
	Index   uint32
	Amount  uint64
	Address string
}

// deltaDTO is the on-disk form of a domain.UTXODelta: sorted slices, because JSON cannot key a map by OutPoint.
type deltaDTO struct {
	Spent   []outDTO
	Created []outDTO
}

func encodeOuts(m map[domain.OutPoint]domain.TxOut) []outDTO {
	out := make([]outDTO, 0, len(m))
	for op, o := range m {
		out = append(out, outDTO{TxID: op.TxID, Index: op.Index, Amount: o.Amount, Address: o.Address})
	}
	slices.SortFunc(out, func(a, b outDTO) int {
		return cmp.Or(strings.Compare(a.TxID, b.TxID), cmp.Compare(a.Index, b.Index))
	})
	return out
}

func decodeOuts(list []outDTO) map[domain.OutPoint]domain.TxOut {
	m := make(map[domain.OutPoint]domain.TxOut, len(list))
	for _, o := range list {
		m[domain.OutPoint{TxID: o.TxID, Index: o.Index}] = domain.TxOut{Amount: o.Amount, Address: o.Address}
	}
	return m
}

func encodeDelta(d domain.UTXODelta) ([]byte, error) {
	return json.Marshal(deltaDTO{Spent: encodeOuts(d.Spent), Created: encodeOuts(d.Created)})
}

func decodeDelta(data []byte) (domain.UTXODelta, error) {
	var dto deltaDTO
	if err := json.Unmarshal(data, &dto); err != nil {
		return domain.UTXODelta{}, err
	}
	return domain.UTXODelta{Spent: decodeOuts(dto.Spent), Created: decodeOuts(dto.Created)}, nil
}

func utxoKey(op domain.OutPoint) []byte {
	return binary.BigEndian.AppendUint32([]byte(op.TxID), op.Index)
}

func addrUTXOKey(addr string, op domain.OutPoint) []byte {
	return binary.BigEndian.AppendUint32(append(append([]byte(addr), addrSep), op.TxID...), op.Index)
}

// outPointOf splits a utxos or snapshot key into txid and index.
func outPointOf(k []byte) domain.OutPoint {
	n := len(k) - 4
	return domain.OutPoint{TxID: string(k[:n]), Index: binary.BigEndian.Uint32(k[n:])}
}

// metaInt reads an 8-byte big-endian meta value; a missing key is 0.
func metaInt(tx *bbolt.Tx, key []byte) int {
	v := tx.Bucket(metaBucket).Get(key)
	if v == nil {
		return 0
	}
	return int(binary.BigEndian.Uint64(v))
}

func addrKey(addr string, height, pos int) []byte {
	key := append([]byte(addr), addrSep)
	key = binary.BigEndian.AppendUint64(key, uint64(height))
	return binary.BigEndian.AppendUint32(key, uint32(pos))
}

// utxoHolds reports whether the stored set holds every output in outs with the same value.
func utxoHolds(tx *bbolt.Tx, outs map[domain.OutPoint]domain.TxOut) (bool, error) {
	bucket := tx.Bucket(utxosBucket)
	for op, want := range outs {
		v := bucket.Get(utxoKey(op))
		if v == nil {
			return false, nil
		}
		var got domain.TxOut
		if err := json.Unmarshal(v, &got); err != nil {
			return false, err
		}
		if got != want {
			return false, nil
		}
	}
	return true, nil
}

// utxoIntersects reports whether any outpoint in outs is in the stored set.
func utxoIntersects(tx *bbolt.Tx, outs map[domain.OutPoint]domain.TxOut) bool {
	bucket := tx.Bucket(utxosBucket)
	for op := range outs {
		if bucket.Get(utxoKey(op)) != nil {
			return true
		}
	}
	return false
}

// putOuts adds outs to the stored set; deleteOuts removes them.
func putOuts(tx *bbolt.Tx, outs map[domain.OutPoint]domain.TxOut) error {
	bucket := tx.Bucket(utxosBucket)
	for op, o := range outs {
		data, err := json.Marshal(o)
		if err != nil {
			return err
		}
		if err := bucket.Put(utxoKey(op), data); err != nil {
			return err
		}
		if err := tx.Bucket(addrutxoBucket).Put(addrUTXOKey(o.Address, op), binary.BigEndian.AppendUint64(nil, o.Amount)); err != nil {
			return err
		}
	}
	return nil
}

func deleteOuts(tx *bbolt.Tx, outs map[domain.OutPoint]domain.TxOut) error {
	bucket := tx.Bucket(utxosBucket)
	for op, o := range outs {
		if err := bucket.Delete(utxoKey(op)); err != nil {
			return err
		}
		if err := tx.Bucket(addrutxoBucket).Delete(addrUTXOKey(o.Address, op)); err != nil {
			return err
		}
	}
	return nil
}

// current reports whether the indexes describe the stored tip; an empty chain is current.
func current(tx *bbolt.Tx) bool {
	k, _ := tx.Bucket(blocksBucket).Cursor().Last()
	if k == nil {
		return true
	}
	return bytes.Equal(tx.Bucket(metaBucket).Get(indexedTipKey), k)
}

// writeIndex stores the undo data and the index entries of b and moves indexedTip to it.
// The UTXO set is updated by the caller.
func writeIndex(tx *bbolt.Tx, b *domain.Block, d domain.UTXODelta) error {
	data, err := encodeDelta(d)
	if err != nil {
		return err
	}
	if err := tx.Bucket(undoBucket).Put(heightKey(b.Index), data); err != nil {
		return err
	}
	for _, t := range storage.IndexTxs(b, d) {
		if err := tx.Bucket(txsBucket).Put([]byte(t.TxID), heightKey(b.Index)); err != nil {
			return err
		}
		for _, a := range t.Addrs {
			if err := tx.Bucket(addrtxBucket).Put(addrKey(a, b.Index, t.Pos), []byte(t.TxID)); err != nil {
				return err
			}
		}
	}
	return tx.Bucket(metaBucket).Put(indexedTipKey, heightKey(b.Index))
}

// commit is the body of Commit; Reorg runs it once per new block.
func commit(tx *bbolt.Tx, p storage.Pruning, b *domain.Block, d domain.UTXODelta) error {
	if !current(tx) {
		return storage.ErrIndexStale
	}
	if err := checkTip(tx.Bucket(blocksBucket), b); err != nil {
		return err
	}
	holds, err := utxoHolds(tx, d.Spent)
	if err != nil {
		return err
	}
	if !holds || utxoIntersects(tx, d.Created) {
		return storage.ErrUTXOConflict
	}
	if err := putBlock(tx, b); err != nil {
		return err
	}
	if err := deleteOuts(tx, d.Spent); err != nil {
		return err
	}
	if err := putOuts(tx, d.Created); err != nil {
		return err
	}
	if err := writeIndex(tx, b, d); err != nil {
		return err
	}
	return snapshotStep(tx, p, b.Index)
}

// readSet loads the stored UTXO set.
func readSet(tx *bbolt.Tx) (domain.UTXOSet, error) {
	set := domain.UTXOSet{}
	err := tx.Bucket(utxosBucket).ForEach(func(k, v []byte) error {
		var o domain.TxOut
		if err := json.Unmarshal(v, &o); err != nil {
			return err
		}
		set[outPointOf(k)] = o
		return nil
	})
	return set, err
}

// snapshotStep stores the snapshot that the Commit of height t calls for and,
// with p.KeepBlocks > 0, prunes the bodies up to it.
func snapshotStep(tx *bbolt.Tx, p storage.Pruning, t int) error {
	s, ok := p.SnapshotHeight(t)
	prunedBelow := metaInt(tx, prunedBelowKey)
	if !ok || s < prunedBelow {
		return nil
	}
	set, err := readSet(tx)
	if err != nil {
		return err
	}
	for h := t; h > s; h-- {
		d, err := decodeDelta(tx.Bucket(undoBucket).Get(heightKey(h)))
		if err != nil {
			return err
		}
		set.Revert(d)
	}
	var at domain.Block
	if err := json.Unmarshal(tx.Bucket(blocksBucket).Get(heightKey(s)), &at); err != nil {
		return err
	}
	if err := dropSnapshotFrom(tx, 0); err != nil {
		return err
	}
	sb := tx.Bucket(snapshotBucket)
	for op, o := range set {
		data, err := json.Marshal(o)
		if err != nil {
			return err
		}
		if err := sb.Put(utxoKey(op), data); err != nil {
			return err
		}
	}
	meta := tx.Bucket(metaBucket)
	if err := meta.Put(snapshotHeightKey, heightKey(s)); err != nil {
		return err
	}
	if err := meta.Put(snapshotHashKey, []byte(storage.SnapshotHash(at.Hash, set))); err != nil {
		return err
	}
	if p.KeepBlocks == 0 {
		return nil
	}
	for h := prunedBelow; h <= s; h++ {
		if err := pruneBlock(tx, h); err != nil {
			return err
		}
	}
	return meta.Put(prunedBelowKey, heightKey(s+1))
}

// pruneBlock replaces the block at h by its header and drops its undo data and index entries.
func pruneBlock(tx *bbolt.Tx, h int) error {
	var b domain.Block
	if err := json.Unmarshal(tx.Bucket(blocksBucket).Get(heightKey(h)), &b); err != nil {
		return err
	}
	d, err := decodeDelta(tx.Bucket(undoBucket).Get(heightKey(h)))
	if err != nil {
		return err
	}
	for _, t := range storage.IndexTxs(&b, d) {
		if err := tx.Bucket(txsBucket).Delete([]byte(t.TxID)); err != nil {
			return err
		}
		for _, a := range t.Addrs {
			if err := tx.Bucket(addrtxBucket).Delete(addrKey(a, h, t.Pos)); err != nil {
				return err
			}
		}
	}
	if err := tx.Bucket(undoBucket).Delete(heightKey(h)); err != nil {
		return err
	}
	b.Transactions = []domain.Transaction{}
	data, err := json.Marshal(&b)
	if err != nil {
		return err
	}
	return tx.Bucket(blocksBucket).Put(heightKey(h), data)
}

// dropSnapshotFrom discards the stored snapshot when its height is >= height.
func dropSnapshotFrom(tx *bbolt.Tx, height int) error {
	meta := tx.Bucket(metaBucket)
	if meta.Get(snapshotHeightKey) == nil || metaInt(tx, snapshotHeightKey) < height {
		return nil
	}
	if err := tx.DeleteBucket(snapshotBucket); err != nil {
		return err
	}
	if _, err := tx.CreateBucket(snapshotBucket); err != nil {
		return err
	}
	if err := meta.Delete(snapshotHeightKey); err != nil {
		return err
	}
	return meta.Delete(snapshotHashKey)
}

// revert removes the stored block at height h, undoing its delta and index entries.
func revert(tx *bbolt.Tx, h int) error {
	var b domain.Block
	if err := json.Unmarshal(tx.Bucket(blocksBucket).Get(heightKey(h)), &b); err != nil {
		return err
	}
	d, err := decodeDelta(tx.Bucket(undoBucket).Get(heightKey(h)))
	if err != nil {
		return err
	}
	holds, err := utxoHolds(tx, d.Created)
	if err != nil {
		return err
	}
	if !holds || utxoIntersects(tx, d.Spent) {
		return storage.ErrUTXOConflict
	}
	if err := deleteOuts(tx, d.Created); err != nil {
		return err
	}
	if err := putOuts(tx, d.Spent); err != nil {
		return err
	}
	for _, t := range storage.IndexTxs(&b, d) {
		if err := tx.Bucket(txsBucket).Delete([]byte(t.TxID)); err != nil {
			return err
		}
		for _, a := range t.Addrs {
			if err := tx.Bucket(addrtxBucket).Delete(addrKey(a, h, t.Pos)); err != nil {
				return err
			}
		}
	}
	if err := tx.Bucket(undoBucket).Delete(heightKey(h)); err != nil {
		return err
	}
	// Deleting a hash that was never indexed (an empty one) is a no-op.
	if err := tx.Bucket(hashesBucket).Delete([]byte(b.Hash)); err != nil {
		return err
	}
	if err := tx.Bucket(blocksBucket).Delete(heightKey(h)); err != nil {
		return err
	}
	return tx.Bucket(metaBucket).Put(indexedTipKey, heightKey(h-1))
}

// Lookup reads the persisted UTXO set.
func (r *Repository) Lookup(op domain.OutPoint) (domain.TxOut, bool) {
	var out domain.TxOut
	found := false
	_ = r.db.View(func(tx *bbolt.Tx) error {
		v := tx.Bucket(utxosBucket).Get(utxoKey(op))
		if v == nil {
			return nil
		}
		found = json.Unmarshal(v, &out) == nil
		return nil
	})
	return out, found
}

// Commit writes b, its delta, its undo data and its index entries in one transaction.
func (r *Repository) Commit(b *domain.Block, d domain.UTXODelta) error {
	if b == nil {
		return ErrNilBlock
	}
	return r.db.Update(func(tx *bbolt.Tx) error { return commit(tx, r.pruning, b, d) })
}

// Reorg reverts the tail and commits the new blocks in one transaction.
func (r *Repository) Reorg(height int, blocks []*domain.Block, deltas []domain.UTXODelta) error {
	for _, b := range blocks {
		if b == nil {
			return ErrNilBlock
		}
	}
	if height < 1 || len(blocks) == 0 || len(deltas) != len(blocks) {
		return storage.ErrConflict
	}

	return r.db.Update(func(tx *bbolt.Tx) error {
		if err := checkSplice(tx.Bucket(blocksBucket), height, blocks); err != nil {
			return err
		}
		if height < metaInt(tx, prunedBelowKey) {
			return storage.ErrReorgTooDeep
		}
		if !current(tx) {
			return storage.ErrIndexStale
		}
		tip, _ := tx.Bucket(blocksBucket).Cursor().Last()
		for h := int(binary.BigEndian.Uint64(tip)); h >= height; h-- {
			if err := revert(tx, h); err != nil {
				return err
			}
		}
		if err := dropSnapshotFrom(tx, height); err != nil {
			return err
		}
		for i, b := range blocks {
			if err := commit(tx, r.pruning, b, deltas[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *Repository) TxHeight(txid string) (int, error) {
	var h int
	err := r.db.View(func(tx *bbolt.Tx) error {
		v := tx.Bucket(txsBucket).Get([]byte(txid))
		if v == nil {
			return storage.ErrNotFound
		}
		h = int(binary.BigEndian.Uint64(v))
		return nil
	})
	return h, err
}

func (r *Repository) TxsByAddress(addr string) ([]string, error) {
	ids := []string{}
	prefix := append([]byte(addr), addrSep)
	err := r.db.View(func(tx *bbolt.Tx) error {
		c := tx.Bucket(addrtxBucket).Cursor()
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			ids = append(ids, string(v))
		}
		return nil
	})
	return ids, err
}

func (r *Repository) IndexCurrent() (bool, error) {
	var ok bool
	err := r.db.View(func(tx *bbolt.Tx) error {
		ok = current(tx)
		return nil
	})
	return ok, err
}

// Reindex drops the index buckets and rebuilds them from the blocks in one
// transaction; a pruned database starts from its snapshot instead of the genesis.
func (r *Repository) Reindex() error {
	return r.db.Update(func(tx *bbolt.Tx) error {
		for _, name := range indexBuckets {
			if err := tx.DeleteBucket(name); err != nil {
				return err
			}
			if _, err := tx.CreateBucket(name); err != nil {
				return err
			}
		}
		meta := tx.Bucket(metaBucket)
		if err := meta.Delete(indexedTipKey); err != nil {
			return err
		}

		set := domain.UTXOSet{}
		from := metaInt(tx, prunedBelowKey)
		if from > 0 {
			err := tx.Bucket(snapshotBucket).ForEach(func(k, v []byte) error {
				var o domain.TxOut
				if err := json.Unmarshal(v, &o); err != nil {
					return err
				}
				set[outPointOf(k)] = o
				return nil
			})
			if err != nil {
				return err
			}
			if err := putOuts(tx, set); err != nil {
				return err
			}
		}
		c := tx.Bucket(blocksBucket).Cursor()
		for k, v := c.Seek(heightKey(from)); k != nil; k, v = c.Next() {
			var b domain.Block
			if err := json.Unmarshal(v, &b); err != nil {
				return err
			}
			d, err := domain.BlockDelta(set, &b, b.Index == 0)
			if err != nil {
				return err
			}
			set.Apply(d)
			if err := deleteOuts(tx, d.Spent); err != nil {
				return err
			}
			if err := putOuts(tx, d.Created); err != nil {
				return err
			}
			if err := writeIndex(tx, &b, d); err != nil {
				return err
			}
		}
		return nil
	})
}

// Snapshot returns the stored snapshot, or storage.ErrNotFound.
func (r *Repository) Snapshot() (storage.Snapshot, error) {
	var snap storage.Snapshot
	err := r.db.View(func(tx *bbolt.Tx) error {
		meta := tx.Bucket(metaBucket)
		hash := meta.Get(snapshotHashKey)
		if meta.Get(snapshotHeightKey) == nil || hash == nil {
			return storage.ErrNotFound
		}
		snap.Height = metaInt(tx, snapshotHeightKey)
		snap.Hash = string(hash)
		var at domain.Block
		if err := json.Unmarshal(tx.Bucket(blocksBucket).Get(heightKey(snap.Height)), &at); err != nil {
			return err
		}
		snap.BlockHash = at.Hash
		snap.UTXOs = domain.UTXOSet{}
		return tx.Bucket(snapshotBucket).ForEach(func(k, v []byte) error {
			var o domain.TxOut
			if err := json.Unmarshal(v, &o); err != nil {
				return err
			}
			snap.UTXOs[outPointOf(k)] = o
			return nil
		})
	})
	return snap, err
}

func (r *Repository) PrunedBelow() (int, error) {
	var n int
	err := r.db.View(func(tx *bbolt.Tx) error {
		n = metaInt(tx, prunedBelowKey)
		return nil
	})
	return n, err
}

func (r *Repository) UTXOsByAddress(addr string) ([]storage.AddressUTXO, error) {
	out := []storage.AddressUTXO{}
	prefix := append([]byte(addr), addrSep)
	err := r.db.View(func(tx *bbolt.Tx) error {
		c := tx.Bucket(addrutxoBucket).Cursor()
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			out = append(out, storage.AddressUTXO{OutPoint: outPointOf(k[len(prefix):]), Amount: binary.BigEndian.Uint64(v)})
		}
		return nil
	})
	return out, err
}

// ForkState reverts the stored undo data from the tip down to height on a copy of the UTXO set.
func (r *Repository) ForkState(height int) (domain.UTXOSet, error) {
	var set domain.UTXOSet
	err := r.db.View(func(tx *bbolt.Tx) error {
		n := 0
		if k, _ := tx.Bucket(blocksBucket).Cursor().Last(); k != nil {
			n = int(binary.BigEndian.Uint64(k)) + 1
		}
		if height < 0 || height > n {
			return storage.ErrConflict
		}
		if height < metaInt(tx, prunedBelowKey) {
			return storage.ErrReorgTooDeep
		}
		if !current(tx) {
			return storage.ErrIndexStale
		}
		var err error
		if set, err = readSet(tx); err != nil {
			return err
		}
		for h := n - 1; h >= height; h-- {
			d, err := decodeDelta(tx.Bucket(undoBucket).Get(heightKey(h)))
			if err != nil {
				return err
			}
			set.Revert(d)
		}
		return nil
	})
	return set, err
}

// Restore fills an empty database from a snapshot in one transaction; see storage.IndexedRepository.
func (r *Repository) Restore(headers []*domain.Block, set domain.UTXOSet) error {
	if err := storage.CheckHeaders(headers); err != nil {
		return err
	}
	s := len(headers) - 1
	return r.db.Update(func(tx *bbolt.Tx) error {
		if k, _ := tx.Bucket(blocksBucket).Cursor().First(); k != nil {
			return storage.ErrConflict
		}
		for _, h := range headers {
			header := *h
			header.Transactions = []domain.Transaction{}
			if err := putBlock(tx, &header); err != nil {
				return err
			}
		}
		if err := putOuts(tx, set); err != nil {
			return err
		}
		sb := tx.Bucket(snapshotBucket)
		for op, o := range set {
			data, err := json.Marshal(o)
			if err != nil {
				return err
			}
			if err := sb.Put(utxoKey(op), data); err != nil {
				return err
			}
		}
		meta := tx.Bucket(metaBucket)
		if err := meta.Put(snapshotHeightKey, heightKey(s)); err != nil {
			return err
		}
		if err := meta.Put(snapshotHashKey, []byte(storage.SnapshotHash(headers[s].Hash, set))); err != nil {
			return err
		}
		if err := meta.Put(prunedBelowKey, heightKey(s+1)); err != nil {
			return err
		}
		return meta.Put(indexedTipKey, heightKey(s))
	})
}
