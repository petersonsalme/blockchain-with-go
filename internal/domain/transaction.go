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

	ErrNonceReused          = errors.New("nonce already used by this sender")
	ErrNonceGap             = errors.New("nonce skips ahead of the sender's next expected nonce")
	ErrDuplicateTransaction = errors.New("duplicate transaction in request")
)

// smallOrderY lists the y coordinates (32 bytes little-endian, sign bit
// cleared) of the Ed25519 points of order 1, 2, 4 and 8. The sign bit is
// ignored, which yields the 10 small-order encodings rejected by libsodium's
// ge25519_has_small_order: y = 0, 1, p-1, y8 and p-y8.
// Source: libsodium crypto_core/ed25519/ref10/ed25519_ref10.c.
var smallOrderY = [...][32]byte{
	{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
	{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
	{0xec, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f},
	{0x26, 0xe8, 0x95, 0x8f, 0xc2, 0xb2, 0x27, 0xb0, 0x45, 0xc3, 0xf4, 0x89, 0xf2, 0xef, 0x98, 0xf0, 0xd5, 0xdf, 0xac, 0x05, 0xd3, 0xc6, 0x33, 0x39, 0xb1, 0x38, 0x02, 0x88, 0x6d, 0x53, 0xfc, 0x05},
	{0xc7, 0x17, 0x6a, 0x70, 0x3d, 0x4d, 0xd8, 0x4f, 0xba, 0x3c, 0x0b, 0x76, 0x0d, 0x10, 0x67, 0x0f, 0x2a, 0x20, 0x53, 0xfa, 0x2c, 0x39, 0xcc, 0xc6, 0x4e, 0xc7, 0xfd, 0x77, 0x92, 0xac, 0x03, 0x7a},
}

// isWeakPublicKey reports whether an Ed25519 public key has a small-order
// point or a non-canonical y coordinate (y >= p = 2^255-19), either of which
// lets a signature verify without knowledge of a private key.
func isWeakPublicKey(pub []byte) bool {
	var y [32]byte
	copy(y[:], pub)
	y[31] &= 0x7f

	for _, bad := range smallOrderY {
		if y == bad {
			return true
		}
	}

	// y >= p: p is ed ff ... ff 7f in little-endian.
	if y[31] != 0x7f || y[0] < 0xed {
		return false
	}
	for _, b := range y[1:31] {
		if b != 0xff {
			return false
		}
	}
	return true
}

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
	if isWeakPublicKey(rawPub) {
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
