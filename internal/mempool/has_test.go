package mempool_test

import (
	"testing"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/mempool"
)

func TestHas(t *testing.T) {
	p := mempool.New(0)
	tx := domain.Transaction{ID: "a", Inputs: []domain.TxIn{{PrevTxID: "x"}}}

	if p.Has("a") {
		t.Fatal("empty pool reports a transaction")
	}

	err := p.Add(tx, 1)
	if err != nil {
		t.Fatal(err)
	}

	if !p.Has("a") || p.Has("b") {
		t.Fatal("Has must be true for a pooled ID and false for another")
	}

	p.Remove("a")

	if p.Has("a") {
		t.Fatal("removed transaction still reported")
	}
}
