// Package bolttest builds bbolt files for tests of the bolt adapter and of its
// callers.
package bolttest

import (
	"encoding/binary"
	"encoding/json"
	"errors"

	bbolt "go.etcd.io/bbolt"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// ErrNilBlock is returned by WriteLegacy when given a nil block.
var ErrNilBlock = errors.New("bolttest: nil block")

// WriteLegacy creates the bbolt file at path as a node wrote it before the
// persisted indexes existed (ADR-0011): only the blocks bucket (height, 8
// bytes big endian, to the JSON block) and the hashes bucket (hash to height).
// bolt.Open then reports its indexes stale until Reindex runs.
//
// It stores blocks as given, without checking that they link or replay, so a
// test can also build a chain that Reindex rejects. The encoding is the one
// the bolt adapter reads; the file must not exist yet.
func WriteLegacy(path string, blocks []*domain.Block) error {
	for _, b := range blocks {
		if b == nil {
			return ErrNilBlock
		}
	}

	db, err := bbolt.Open(path, 0o600, nil)
	if err != nil {
		return err
	}

	err = db.Update(func(tx *bbolt.Tx) error {
		bBucket, err := tx.CreateBucket([]byte("blocks"))
		if err != nil {
			return err
		}

		hBucket, err := tx.CreateBucket([]byte("hashes"))
		if err != nil {
			return err
		}

		for _, b := range blocks {
			data, err := json.Marshal(b)
			if err != nil {
				return err
			}

			key := make([]byte, 8)
			binary.BigEndian.PutUint64(key, uint64(b.Index))

			err = bBucket.Put(key, data)
			if err != nil {
				return err
			}

			if b.Hash != "" {
				err = hBucket.Put([]byte(b.Hash), key)
				if err != nil {
					return err
				}
			}
		}

		return nil
	})

	return errors.Join(err, db.Close())
}
