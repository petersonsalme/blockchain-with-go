package server

import "github.com/petersonsalme/blockchain-with-go/internal/domain"

type Message struct {
	Transactions []domain.Transaction
}

type LegacyMessage struct {
	BPM int
}
