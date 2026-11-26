package indexer

import (
	"context"
	"errors"
	"fmt"
	"math/big"
)

// ErrHeightNotIndexed is returned when a requested height is above the indexed tip (or the store is empty).
var ErrHeightNotIndexed = errors.New("height is not indexed")

// SupplyRow is the supply snapshot at one block height.
type SupplyRow struct {
	Height   int    `json:"height"`
	Hash     string `json:"hash"`
	Total    string `json:"total"`
	Created  string `json:"created"`
	Spent    string `json:"spent"`
	Coinbase string `json:"coinbase"`
}

// RichAddress is one row of the richest addresses response.
type RichAddress struct {
	Address string `json:"address"`
	Balance string `json:"balance"`
	Share   string `json:"share"`
}

// RichestSnapshot is the richest addresses snapshot at the indexed tip.
type RichestSnapshot struct {
	Height    int           `json:"height"`
	Hash      string        `json:"hash"`
	Total     string        `json:"total"`
	Addresses []RichAddress `json:"addresses"`
}

// RangeStats is the aggregated block, transaction and supply statistics over heights From..To inclusive.
type RangeStats struct {
	From     int    `json:"from"`
	To       int    `json:"to"`
	Blocks   int    `json:"blocks"`
	Txs      int    `json:"txs"`
	Created  string `json:"created"`
	Spent    string `json:"spent"`
	Coinbase string `json:"coinbase"`
}

// Reader is the read-only query port of the indexer HTTP API. Every method reads in one
// REPEATABLE READ read-only transaction so height, hash and amounts come from the same snapshot.
type Reader interface {
	Health(ctx context.Context) (Tip, error)
	SupplyTip(ctx context.Context) (SupplyRow, error)
	SupplyAt(ctx context.Context, height int) (SupplyRow, error)
	Richest(ctx context.Context, limit int) (RichestSnapshot, error)
	Stats(ctx context.Context, from, to int) (RangeStats, error)
}

// ShareString returns balance / total formatted with 4 decimal places ("0.0000".."1.0000")
// using integer arithmetic (balance * 10000 / total). It returns "0.0000" when total is zero
// or when either string is not a positive integer.
func ShareString(balance, total string) string {
	b, ok := new(big.Int).SetString(balance, 10)
	if !ok || b.Sign() <= 0 {
		return "0.0000"
	}

	t, ok := new(big.Int).SetString(total, 10)
	if !ok || t.Sign() <= 0 {
		return "0.0000"
	}

	scaled := new(big.Int).Mul(b, big.NewInt(10000))
	bp := new(big.Int).Quo(scaled, t).Int64()

	return fmt.Sprintf("%d.%04d", bp/10000, bp%10000)
}
