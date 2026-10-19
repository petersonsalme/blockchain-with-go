package server

import "github.com/petersonsalme/blockchain-with-go/internal/domain"

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
