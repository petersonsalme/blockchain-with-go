package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrEmptyFrom     = errors.New("from cannot be empty")
	ErrEmptyTo       = errors.New("to cannot be empty")
	ErrInvalidAmount = errors.New("amount must be greater than zero")
	ErrInvalidID     = errors.New("transaction id does not match calculated hash")
)

type Transaction struct {
	ID        string
	From      string
	To        string
	Amount    uint64
	Fee       uint64
	Nonce     uint64
	Signature string
	Timestamp time.Time
}

type txHashPayload struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Amount    uint64 `json:"amount"`
	Fee       uint64 `json:"fee"`
	Nonce     uint64 `json:"nonce"`
	Timestamp string `json:"timestamp"`
}

func (t *Transaction) CalculateHash() string {
	if t == nil {
		return ""
	}

	payload := txHashPayload{
		From:      t.From,
		To:        t.To,
		Amount:    t.Amount,
		Fee:       t.Fee,
		Nonce:     t.Nonce,
		Timestamp: t.Timestamp.UTC().Format(time.RFC3339Nano),
	}

	data, _ := json.Marshal(payload)
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func (t *Transaction) Validate() error {
	if t == nil {
		return errors.New("transaction is nil")
	}

	if t.From == "" {
		return ErrEmptyFrom
	}

	if t.To == "" {
		return ErrEmptyTo
	}

	if t.Amount == 0 {
		return ErrInvalidAmount
	}

	if t.ID != "" && t.ID != t.CalculateHash() {
		return ErrInvalidID
	}

	return nil
}

func TransactionsHash(txs []Transaction) string {
	h := sha256.New()
	for _, tx := range txs {
		h.Write([]byte(tx.CalculateHash() + tx.Signature))
	}
	return hex.EncodeToString(h.Sum(nil))
}
