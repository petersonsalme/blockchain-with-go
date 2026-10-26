package storage

import (
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// Snapshot is the UTXO set as of the block at Height.
type Snapshot struct {
	Height    int
	BlockHash string
	Hash      string
	UTXOs     domain.UTXOSet
}

// AddressUTXO is one unspent output of an address.
type AddressUTXO struct {
	OutPoint domain.OutPoint
	Amount   uint64
}

// SnapshotHash is the hex SHA-256 over the 64-char block hash as ASCII, then
// each entry of set sorted by (TxID, Index): TxID (ASCII), Index (4 B big-endian),
// len(Address) (2 B big-endian), Address, Amount (8 B big-endian).
func SnapshotHash(blockHash string, set domain.UTXOSet) string {
	ops := make([]domain.OutPoint, 0, len(set))
	for op := range set {
		ops = append(ops, op)
	}

	slices.SortFunc(ops, func(a, b domain.OutPoint) int {
		return cmp.Or(strings.Compare(a.TxID, b.TxID), cmp.Compare(a.Index, b.Index))
	})

	h := sha256.New()
	h.Write([]byte(blockHash))

	var buf []byte

	for _, op := range ops {
		o := set[op]
		buf = append(buf[:0], op.TxID...)
		buf = binary.BigEndian.AppendUint32(buf, op.Index)
		buf = binary.BigEndian.AppendUint16(buf, uint16(len(o.Address)))
		buf = append(buf, o.Address...)
		buf = binary.BigEndian.AppendUint64(buf, o.Amount)
		h.Write(buf)
	}

	return hex.EncodeToString(h.Sum(nil))
}
