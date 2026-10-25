package server

import (
	"encoding/hex"
	"time"

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

// contractRequest is the contract object of POST /tx.
type contractRequest struct {
	Kind     string   `json:"kind"`
	Code     string   `json:"code"`
	Target   string   `json:"target"`
	Args     []uint64 `json:"args"`
	GasLimit uint64   `json:"gas_limit"`
}

// txRequest is the body of POST /tx: a transaction plus the optional lower-case contract object.
type txRequest struct {
	ID        string
	Inputs    []domain.TxIn
	Outputs   []domain.TxOut
	Timestamp time.Time
	Contract  *contractRequest `json:"contract"`
}

// transaction converts r to the domain transaction it carries.
func (r txRequest) transaction() domain.Transaction {
	tx := domain.Transaction{ID: r.ID, Inputs: r.Inputs, Outputs: r.Outputs, Timestamp: r.Timestamp}
	if c := r.Contract; c != nil {
		tx.Contract = &domain.ContractOp{Kind: c.Kind, Code: c.Code, Target: c.Target, Args: c.Args, GasLimit: c.GasLimit}
	}
	return tx
}

// contractResponse is the body of GET /contracts/{id}; Storage maps decimal keys to values.
type contractResponse struct {
	ID      string            `json:"id"`
	Code    string            `json:"code"`
	Storage map[uint64]uint64 `json:"storage"`
}

// newContractResponse builds the response for the deployed contract id.
func newContractResponse(id string, code []byte, storage map[uint64]uint64) contractResponse {
	if storage == nil {
		storage = map[uint64]uint64{}
	}
	return contractResponse{ID: id, Code: hex.EncodeToString(code), Storage: storage}
}
