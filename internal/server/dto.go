package server

import (
	"github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

type Message struct {
	Transactions []domain.Transaction
}

type errorResponse struct {
	Error   string         `json:"error"`
	Code    string         `json:"code,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

// blocksResponse is the body of GET /.
type blocksResponse struct {
	Blocks []*domain.Block
}

// pageResponse is the body of GET /blocks. Next is set only when a block exists at that height.
type pageResponse struct {
	Blocks []*domain.Block `json:"blocks"`
	From   int             `json:"from"`
	Limit  int             `json:"limit"`
	Next   *int            `json:"next,omitempty"`
}

// healthResponse is the body of GET /health.
type healthResponse struct {
	Status string `json:"status"`
	Height int    `json:"height"`
	Tip    string `json:"tip"`
}

// txResponse is the body of GET /tx/{id}.
type txResponse struct {
	Transaction *domain.Transaction `json:"transaction"`
	BlockHash   string              `json:"block_hash"`
	Height      int                 `json:"height"`
}

// balanceResponse is the body of GET /balance/{addr}.
type balanceResponse struct {
	Address string       `json:"address"`
	Balance uint64       `json:"balance"`
	UTXOs   []chain.UTXO `json:"utxos"`
}
