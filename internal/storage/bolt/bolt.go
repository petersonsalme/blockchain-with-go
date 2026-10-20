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

// ErrNilBlock is returned by Append when given a nil block.
var ErrNilBlock = errors.New("bolt: nil block")

// Repository stores blocks in a bbolt database file.
type Repository struct {
	db *bbolt.DB
}

var _ storage.BlockRepository = (*Repository)(nil)

// Open opens or creates a bbolt database at path, initializing the buckets.
// If the database is locked by another process, Open fails after 1 second.
func Open(path string) (*Repository, error) {
	db, err := bbolt.Open(path, 0o600, &bbolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, err
	}

	err = db.Update(func(tx *bbolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(blocksBucket); err != nil {
			return err
		}
		_, err := tx.CreateBucketIfNotExists(hashesBucket)
		return err
	})
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	return &Repository{db: db}, nil
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
		if err := json.Unmarshal(v, &decoded); err != nil {
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
		bBucket := tx.Bucket(blocksBucket)
		c := bBucket.Cursor()
		k, v := c.Last()
		if k != nil {
			var last domain.Block
			if err := json.Unmarshal(v, &last); err != nil {
				return err
			}
			if b.Index != last.Index+1 || b.PrevHash != last.Hash {
				return storage.ErrConflict
			}
		}

		data, err := json.Marshal(b)
		if err != nil {
			return err
		}

		key := make([]byte, 8)
		binary.BigEndian.PutUint64(key, uint64(b.Index))

		if err := bBucket.Put(key, data); err != nil {
			return err
		}

		if len(b.Hash) > 0 {
			hBucket := tx.Bucket(hashesBucket)
			return hBucket.Put([]byte(b.Hash), key)
		}
		return nil
	})
}

// All returns a snapshot of all blocks in index order, oldest first.
func (r *Repository) All() ([]*domain.Block, error) {
	var blocks []*domain.Block
	err := r.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket(blocksBucket)
		blocks = make([]*domain.Block, 0, b.Stats().KeyN)
		return b.ForEach(func(k, v []byte) error {
			var decoded domain.Block
			if err := json.Unmarshal(v, &decoded); err != nil {
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
		if err := json.Unmarshal(v, &decoded); err != nil {
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
		if err := json.Unmarshal(v, &decoded); err != nil {
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
			if err := json.Unmarshal(v, &decoded); err != nil {
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
