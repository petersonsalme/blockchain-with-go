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

// mempoolResponse is the body of GET /mempool: the pool in selection order.
type mempoolResponse struct {
	Transactions []domain.Transaction `json:"transactions"`
}

// submitResponse is the body of POST /tx.
type submitResponse struct {
	ID string `json:"id"`
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

// proofStep is one sibling of the path from a leaf to the Merkle root; Side is "left" or "right".
type proofStep struct {
	Hash string `json:"hash"`
	Side string `json:"side"`
}

// proofResponse is the body of GET /tx/{id}/proof.
type proofResponse struct {
	TxID       string      `json:"tx_id"`
	BlockIndex int         `json:"block_index"`
	BlockHash  string      `json:"block_hash"`
	MerkleRoot string      `json:"merkle_root"`
	LeafIndex  int         `json:"leaf_index"`
	LeafHash   string      `json:"leaf_hash"`
	Proof      []proofStep `json:"proof"`
}
