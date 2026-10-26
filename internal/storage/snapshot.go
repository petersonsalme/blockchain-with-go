package storage

import (
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
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
//
// Every address of set must be at most math.MaxUint16 bytes, or the 2-byte
// prefix would wrap and two sets could hash alike: SnapshotHash panics
// otherwise. Domain addresses (wallet.ValidateAddress) have 50 bytes and
// ReadSnapshotFile rejects any other, so production paths never reach the panic.
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

		addrLen := len(o.Address)
		if addrLen > math.MaxUint16 {
			panic(fmt.Sprintf("storage: address of %d bytes does not fit the 2-byte length prefix", addrLen))
		}

		buf = binary.BigEndian.AppendUint16(buf, uint16(addrLen))
		buf = append(buf, o.Address...)
		buf = binary.BigEndian.AppendUint64(buf, o.Amount)
		h.Write(buf)
	}

	return hex.EncodeToString(h.Sum(nil))
}
