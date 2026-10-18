package domain

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

var (
	ErrEmptyFrom        = errors.New("from cannot be empty")
	ErrEmptyTo          = errors.New("to cannot be empty")
	ErrInvalidAmount    = errors.New("amount must be greater than zero")
	ErrInvalidID        = errors.New("transaction id does not match calculated hash")
	ErrMissingSignature = errors.New("missing signature or public key")
	ErrInvalidPublicKey = errors.New("invalid public key")
	ErrInvalidRecipient = errors.New("invalid recipient address")
	ErrSenderMismatch   = errors.New("sender does not match public key")
	ErrInvalidSignature = errors.New("invalid signature")
)

type Transaction struct {
	ID        string
	From      string
	To        string
	Amount    uint64
	Fee       uint64
	Nonce     uint64
	Signature string
	PublicKey string
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

func SignTransaction(tx *Transaction, w *wallet.Wallet) error {
	if tx == nil {
		return errors.New("transaction is nil")
	}
	if w == nil {
		return errors.New("wallet is nil")
	}

	hashHex := tx.CalculateHash()
	hashBytes, _ := hex.DecodeString(hashHex)

	pub := w.PublicKey()
	sig := w.Sign(hashBytes)

	tx.PublicKey = hex.EncodeToString(pub)
	tx.Signature = hex.EncodeToString(sig)
	tx.ID = hashHex
	return nil
}

func (t *Transaction) VerifySignature() error {
	if t == nil {
		return errors.New("transaction is nil")
	}

	if t.Signature == "" || t.PublicKey == "" {
		return ErrMissingSignature
	}

	if len(t.PublicKey) != 64 {
		return ErrInvalidPublicKey
	}
	rawPub, err := hex.DecodeString(t.PublicKey)
	if err != nil || hex.EncodeToString(rawPub) != t.PublicKey {
		return ErrInvalidPublicKey
	}

	if err := wallet.ValidateAddress(t.To); err != nil {
		return ErrInvalidRecipient
	}

	if t.From != wallet.AddressFromPublicKey(ed25519.PublicKey(rawPub)) {
		return ErrSenderMismatch
	}

	if len(t.Signature) != 128 {
		return ErrInvalidSignature
	}
	rawSig, err := hex.DecodeString(t.Signature)
	if err != nil || hex.EncodeToString(rawSig) != t.Signature {
		return ErrInvalidSignature
	}

	hashBytes, _ := hex.DecodeString(t.CalculateHash())
	if !wallet.Verify(ed25519.PublicKey(rawPub), hashBytes, rawSig) {
		return ErrInvalidSignature
	}

	return nil
}
