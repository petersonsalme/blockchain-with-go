package storage

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// ErrInvalidSnapshot is returned by ReadSnapshotFile when the file is not
// valid JSON or does not agree with itself: height, blockHash or hash.
var ErrInvalidSnapshot = errors.New("storage: invalid snapshot file")

// snapshotFile is the JSON form of a snapshot together with the headers 0..Height.
type snapshotFile struct {
	Height    int             `json:"height"`
	BlockHash string          `json:"blockHash"`
	Hash      string          `json:"hash"`
	Headers   []*domain.Block `json:"headers"`
	UTXOs     []fileUTXO      `json:"utxos"`
}

type fileUTXO struct {
	TxID    string `json:"txid"`
	Index   uint32 `json:"index"`
	Address string `json:"address"`
	Amount  uint64 `json:"amount"`
}

// WriteSnapshotFile writes s and the headers 0..s.Height of its chain to w as
// JSON, with the UTXOs ordered by (txid, index).
func WriteSnapshotFile(w io.Writer, headers []*domain.Block, s Snapshot) error {
	f := snapshotFile{Height: s.Height, BlockHash: s.BlockHash, Hash: s.Hash, Headers: headers, UTXOs: make([]fileUTXO, 0, len(s.UTXOs))}
	for op, o := range s.UTXOs {
		f.UTXOs = append(f.UTXOs, fileUTXO{TxID: op.TxID, Index: op.Index, Address: o.Address, Amount: o.Amount})
	}

	slices.SortFunc(f.UTXOs, func(a, b fileUTXO) int {
		return cmp.Or(strings.Compare(a.TxID, b.TxID), cmp.Compare(a.Index, b.Index))
	})

	return json.NewEncoder(w).Encode(f)
}

// ReadSnapshotFile reads what WriteSnapshotFile wrote. It returns
// ErrInvalidSnapshot, and no data, when the JSON is invalid, height is not
// len(headers)-1, blockHash is not the hash of the last header, or hash is not
// SnapshotHash(blockHash, utxos).
func ReadSnapshotFile(r io.Reader) ([]*domain.Block, Snapshot, error) {
	var f snapshotFile
	if err := json.NewDecoder(r).Decode(&f); err != nil {
		return nil, Snapshot{}, fmt.Errorf("%w: %w", ErrInvalidSnapshot, err)
	}

	if len(f.Headers) == 0 || slices.Contains(f.Headers, nil) {
		return nil, Snapshot{}, fmt.Errorf("%w: no headers", ErrInvalidSnapshot)
	}

	if f.Height != len(f.Headers)-1 {
		return nil, Snapshot{}, fmt.Errorf("%w: height %d does not match %d headers", ErrInvalidSnapshot, f.Height, len(f.Headers))
	}

	if f.BlockHash != f.Headers[f.Height].Hash {
		return nil, Snapshot{}, fmt.Errorf("%w: blockHash is not the hash of the last header", ErrInvalidSnapshot)
	}

	set := make(domain.UTXOSet, len(f.UTXOs))
	for _, u := range f.UTXOs {
		set[domain.OutPoint{TxID: u.TxID, Index: u.Index}] = domain.TxOut{Amount: u.Amount, Address: u.Address}
	}

	if f.Hash != SnapshotHash(f.BlockHash, set) {
		return nil, Snapshot{}, fmt.Errorf("%w: hash does not match the contents", ErrInvalidSnapshot)
	}

	return f.Headers, Snapshot{Height: f.Height, BlockHash: f.BlockHash, Hash: f.Hash, UTXOs: set}, nil
}
