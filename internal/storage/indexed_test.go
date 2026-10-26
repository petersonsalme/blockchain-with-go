package storage_test

import (
	"testing"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
)

func tx(salt int64, ins []domain.OutPoint, outs ...domain.TxOut) domain.Transaction {
	t := domain.Transaction{Timestamp: time.Unix(1_700_000_000+salt, 0).UTC(), Outputs: outs}
	for _, op := range ins {
		t.Inputs = append(t.Inputs, domain.TxIn{PrevTxID: op.TxID, OutIndex: op.Index})
	}

	return t
}

func TestIndexTxs(t *testing.T) {
	g := tx(0, nil, domain.TxOut{Amount: 10, Address: "zed"}, domain.TxOut{Amount: 5, Address: "amy"})
	gid := g.CalculateHash()
	// t1 spends g:0 (from before the block, so it is in the delta) and pays bob twice.
	t1 := tx(1, []domain.OutPoint{{TxID: gid, Index: 0}}, domain.TxOut{Amount: 4, Address: "bob"}, domain.TxOut{Amount: 6, Address: "bob"})
	// t2 spends t1:1, created earlier in the same block, so only the block can resolve it.
	t2 := tx(2, []domain.OutPoint{{TxID: t1.CalculateHash(), Index: 1}}, domain.TxOut{Amount: 6, Address: "cat"})
	// t3 spends an outpoint nobody knows: it contributes only its outputs.
	t3 := tx(3, []domain.OutPoint{{TxID: "unknown", Index: 0}}, domain.TxOut{Amount: 1, Address: "dan"})
	b := &domain.Block{Transactions: []domain.Transaction{t1, t2, t3}}
	d := domain.UTXODelta{Spent: map[domain.OutPoint]domain.TxOut{{TxID: gid, Index: 0}: {Amount: 10, Address: "zed"}}}

	got := storage.IndexTxs(b, d)

	want := []storage.TxIndex{
		{TxID: t1.CalculateHash(), Pos: 0, Addrs: []string{"bob", "zed"}},
		{TxID: t2.CalculateHash(), Pos: 1, Addrs: []string{"bob", "cat"}},
		{TxID: t3.CalculateHash(), Pos: 2, Addrs: []string{"dan"}},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}

	for i := range want {
		if got[i].TxID != want[i].TxID || got[i].Pos != want[i].Pos || len(got[i].Addrs) != len(want[i].Addrs) {
			t.Fatalf("entry %d: got %+v, want %+v", i, got[i], want[i])
		}

		for j := range want[i].Addrs {
			if got[i].Addrs[j] != want[i].Addrs[j] {
				t.Fatalf("entry %d: got %v, want %v", i, got[i].Addrs, want[i].Addrs)
			}
		}
	}
}

func TestIndexTxsEmptyBlock(t *testing.T) {
	if got := storage.IndexTxs(&domain.Block{}, domain.UTXODelta{}); len(got) != 0 {
		t.Fatalf("got %v, want none", got)
	}
}
