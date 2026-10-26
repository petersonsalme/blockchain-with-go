package storage_test

import (
	"testing"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
)

func TestSnapshotHashVector(t *testing.T) {
	blockHash := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	a := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	z := "0000000000000000000000000000000000000000000000000000000000000001"
	set := domain.UTXOSet{
		{TxID: a, Index: 1}: {Amount: 7, Address: "bob"},
		{TxID: z, Index: 2}: {Amount: 1_000_000_000_000, Address: "carol"},
		{TxID: a, Index: 0}: {Amount: 5, Address: "alice"},
	}
	// Computed independently (Python hashlib/struct) from the encoding of ADR-0014.
	const want = "f73dabb44cb341674ab26d26c2ae41b97aa2e9a60b64a44d90e43b389da963c8"
	if got := storage.SnapshotHash(blockHash, set); got != want {
		t.Fatalf("SnapshotHash = %s, want %s", got, want)
	}

	const empty = "a0fab1377f49a759b57f63318262ebe89fabfc990e8e93ceac2984561482b9d4"
	if got := storage.SnapshotHash(blockHash, nil); got != empty {
		t.Fatalf("SnapshotHash(empty) = %s, want %s", got, empty)
	}
}

func TestSnapshotHashDependsOnEveryField(t *testing.T) {
	op := domain.OutPoint{TxID: "t", Index: 1}
	base := storage.SnapshotHash("h", domain.UTXOSet{op: {Amount: 1, Address: "a"}})

	variants := map[string]string{
		"block hash": storage.SnapshotHash("g", domain.UTXOSet{op: {Amount: 1, Address: "a"}}),
		"index":      storage.SnapshotHash("h", domain.UTXOSet{{TxID: "t", Index: 2}: {Amount: 1, Address: "a"}}),
		"txid":       storage.SnapshotHash("h", domain.UTXOSet{{TxID: "u", Index: 1}: {Amount: 1, Address: "a"}}),
		"amount":     storage.SnapshotHash("h", domain.UTXOSet{op: {Amount: 2, Address: "a"}}),
		"address":    storage.SnapshotHash("h", domain.UTXOSet{op: {Amount: 1, Address: "b"}}),
	}
	for name, v := range variants {
		if v == base {
			t.Errorf("hash ignores %s", name)
		}
	}
}
