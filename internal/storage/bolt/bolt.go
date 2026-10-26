// Package bolt is a bbolt disk-backed implementation of storage.BlockRepository.
package bolt

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"time"

	bbolt "go.etcd.io/bbolt"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
)

var (
	blocksBucket = []byte("blocks")
	hashesBucket = []byte("hashes")
)

// indexBuckets are the buckets Commit, Reorg and Reindex maintain; Reindex rebuilds all of them.
var indexBuckets = [][]byte{utxosBucket, undoBucket, txsBucket, addrtxBucket, addrutxoBucket}

// ErrNilBlock is returned by Append and ReplaceFrom when given a nil block.
var ErrNilBlock = errors.New("bolt: nil block")

// Repository stores blocks in a bbolt database file.
type Repository struct {
	db      *bbolt.DB
	pruning storage.Pruning
}

var _ storage.BlockRepository = (*Repository)(nil)

// Open opens or creates a bbolt database at path, initializing the buckets.
// If the database is locked by another process, Open fails after 1 second.
func Open(path string) (*Repository, error) {
	return OpenPruned(path, storage.Pruning{})
}

// OpenPruned is Open with snapshots and pruning as p says. It returns
// storage.ErrInvalidPruning when p is invalid or when the database is already
// pruned and p.KeepBlocks is 0: pruning is irreversible.
func OpenPruned(path string, p storage.Pruning) (*Repository, error) {
	err := p.Validate()
	if err != nil {
		return nil, err
	}

	db, err := bbolt.Open(path, 0o600, &bbolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, err
	}

	err = db.Update(func(tx *bbolt.Tx) error {
		// A database written before addrutxo existed has indexes that miss it: mark them stale.
		legacy := tx.Bucket(addrutxoBucket) == nil
		for _, name := range append([][]byte{blocksBucket, hashesBucket, snapshotBucket, metaBucket}, indexBuckets...) {
			_, err := tx.CreateBucketIfNotExists(name)
			if err != nil {
				return err
			}
		}

		if legacy {
			err := tx.Bucket(metaBucket).Delete(indexedTipKey)
			if err != nil {
				return err
			}
		}

		if p.KeepBlocks == 0 && metaInt(tx, prunedBelowKey) > 0 {
			return storage.ErrInvalidPruning
		}

		return nil
	})
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	return &Repository{db: db, pruning: p}, nil
}

// Close closes the underlying bbolt database.
func (r *Repository) Close() error {
	return r.db.Close()
}

// Last returns the most recently appended block, or storage.ErrEmpty.
func (r *Repository) Last() (*domain.Block, error) {
	var block *domain.Block

	err := r.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(blocksBucket)
		c := b.Cursor()

		k, v := c.Last()
		if k == nil {
			return storage.ErrEmpty
		}

		var decoded domain.Block

		err := json.Unmarshal(v, &decoded)
		if err != nil {
			return err
		}

		block = &decoded

		return nil
	})
	if err != nil {
		return nil, err
	}

	return block, nil
}

// Append writes b to disk atomically in a single transaction, indexing it by hash.
func (r *Repository) Append(b *domain.Block) error {
	if b == nil {
		return ErrNilBlock
	}

	return r.db.Update(func(tx *bbolt.Tx) error {
		err := checkTip(tx.Bucket(blocksBucket), b)
		if err != nil {
			return err
		}

		return putBlock(tx, b)
	})
}

// checkTip returns storage.ErrConflict unless b extends the last stored block (any b extends an empty chain).
func checkTip(bBucket *bbolt.Bucket, b *domain.Block) error {
	k, v := bBucket.Cursor().Last()
	if k == nil {
		return nil
	}

	var last domain.Block

	err := json.Unmarshal(v, &last)
	if err != nil {
		return err
	}

	if b.Index != last.Index+1 || b.PrevHash != last.Hash {
		return storage.ErrConflict
	}

	return nil
}

// putBlock stores b by height and, when it has a hash, in the hash index.
func putBlock(tx *bbolt.Tx, b *domain.Block) error {
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}

	key := heightKey(b.Index)

	err = tx.Bucket(blocksBucket).Put(key, data)
	if err != nil {
		return err
	}

	if len(b.Hash) > 0 {
		return tx.Bucket(hashesBucket).Put([]byte(b.Hash), key)
	}

	return nil
}

// All returns a snapshot of all blocks in index order, oldest first.
func (r *Repository) All() ([]*domain.Block, error) {
	var blocks []*domain.Block

	err := r.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(blocksBucket)
		blocks = make([]*domain.Block, 0, b.Stats().KeyN)

		return b.ForEach(func(k, v []byte) error {
			var decoded domain.Block

			err := json.Unmarshal(v, &decoded)
			if err != nil {
				return err
			}

			blocks = append(blocks, &decoded)

			return nil
		})
	})
	if err != nil {
		return nil, err
	}

	return blocks, nil
}

// ByHeight returns the block at height h, or storage.ErrNotFound.
func (r *Repository) ByHeight(h int) (*domain.Block, error) {
	if h < 0 {
		return nil, storage.ErrNotFound
	}

	var block *domain.Block

	err := r.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(blocksBucket)
		key := make([]byte, 8)
		binary.BigEndian.PutUint64(key, uint64(h))

		v := b.Get(key)
		if v == nil {
			return storage.ErrNotFound
		}

		var decoded domain.Block

		err := json.Unmarshal(v, &decoded)
		if err != nil {
			return err
		}

		block = &decoded

		return nil
	})
	if err != nil {
		return nil, err
	}

	return block, nil
}

// ByHash returns the block with the given hash, or storage.ErrNotFound.
func (r *Repository) ByHash(hash string) (*domain.Block, error) {
	if len(hash) == 0 {
		return nil, storage.ErrNotFound
	}

	var block *domain.Block

	err := r.db.View(func(tx *bbolt.Tx) error {
		hBucket := tx.Bucket(hashesBucket)

		key := hBucket.Get([]byte(hash))
		if key == nil {
			return storage.ErrNotFound
		}

		bBucket := tx.Bucket(blocksBucket)

		v := bBucket.Get(key)
		if v == nil {
			return storage.ErrNotFound
		}

		var decoded domain.Block

		err := json.Unmarshal(v, &decoded)
		if err != nil {
			return err
		}

		block = &decoded

		return nil
	})
	if err != nil {
		return nil, err
	}

	return block, nil
}

// Range returns up to limit blocks starting at height from, oldest first.
func (r *Repository) Range(from, limit int) ([]*domain.Block, error) {
	if from < 0 || limit < 1 {
		return []*domain.Block{}, nil
	}

	var blocks []*domain.Block

	err := r.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(blocksBucket)
		c := b.Cursor()

		startKey := make([]byte, 8)
		binary.BigEndian.PutUint64(startKey, uint64(from))

		k, v := c.Seek(startKey)
		if k == nil || binary.BigEndian.Uint64(k) != uint64(from) {
			blocks = []*domain.Block{}
			return nil
		}

		blocks = []*domain.Block{}
		for ; k != nil && len(blocks) < limit; k, v = c.Next() {
			var decoded domain.Block

			err := json.Unmarshal(v, &decoded)
			if err != nil {
				return err
			}

			blocks = append(blocks, &decoded)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return blocks, nil
}

// ReplaceFrom drops every block with Index >= height and appends blocks in a
// single transaction, updating the hash index with it.
func (r *Repository) ReplaceFrom(height int, blocks []*domain.Block) error {
	for _, b := range blocks {
		if b == nil {
			return ErrNilBlock
		}
	}

	if height < 1 || len(blocks) == 0 {
		return storage.ErrConflict
	}

	return r.db.Update(func(tx *bbolt.Tx) error {
		bBucket := tx.Bucket(blocksBucket)
		hBucket := tx.Bucket(hashesBucket)

		err := checkSplice(bBucket, height, blocks)
		if err != nil {
			return err
		}

		if height < metaInt(tx, prunedBelowKey) {
			return storage.ErrReorgTooDeep
		}

		err = dropSnapshotFrom(tx, height)
		if err != nil {
			return err
		}

		// Collect first: bbolt cursors must not see deletes made while iterating.
		var stale [][]byte

		c := bBucket.Cursor()
		for k, v := c.Seek(heightKey(height)); k != nil; k, v = c.Next() {
			var removed domain.Block

			err := json.Unmarshal(v, &removed)
			if err != nil {
				return err
			}

			stale = append(stale, append([]byte(nil), k...))
			// Deleting a hash that was never indexed (an empty one) is a no-op.
			err = hBucket.Delete([]byte(removed.Hash))
			if err != nil {
				return err
			}
		}

		for _, key := range stale {
			err := bBucket.Delete(key)
			if err != nil {
				return err
			}
		}

		for _, b := range blocks {
			err := putBlock(tx, b)
			if err != nil {
				return err
			}
		}
		// The indexes still describe the removed tail; mark them stale even when the chain length is unchanged.
		return tx.Bucket(metaBucket).Delete(indexedTipKey)
	})
}

// checkSplice applies the ReplaceFrom preconditions to the stored chain.
func checkSplice(bBucket *bbolt.Bucket, height int, blocks []*domain.Block) error {
	k, _ := bBucket.Cursor().Last()
	if k == nil || uint64(height) > binary.BigEndian.Uint64(k)+1 {
		return storage.ErrConflict
	}

	var prev domain.Block

	err := json.Unmarshal(bBucket.Get(heightKey(height-1)), &prev)
	if err != nil {
		return err
	}

	prevHash := prev.Hash
	for i, b := range blocks {
		if b.Index != height+i || b.PrevHash != prevHash {
			return storage.ErrConflict
		}

		prevHash = b.Hash
	}

	return nil
}

func heightKey(h int) []byte {
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, uint64(h))

	return key
}
