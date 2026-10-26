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
	ErrInvalidID        = errors.New("transaction id does not match calculated hash")
	ErrMissingSignature = errors.New("missing signature or public key")
	ErrInvalidPublicKey = errors.New("invalid public key")
	ErrInvalidSignature = errors.New("invalid signature")
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

// Transaction moves value by spending Inputs and creating Outputs.
type Transaction struct {
	ID        string
	Inputs    []TxIn
	Outputs   []TxOut
	Timestamp time.Time
	// Contract is an optional contract operation, covered by the hash and signatures.
	Contract *ContractOp `json:",omitempty"`
}

// OutPoint identifies a transaction output.
type OutPoint struct {
	TxID  string
	Index uint32
}

// TxIn spends the output (PrevTxID, OutIndex).
type TxIn struct {
	PrevTxID  string
	OutIndex  uint32
	PublicKey string
	Signature string
}

// TxOut assigns Amount to Address.
type TxOut struct {
	Amount  uint64
	Address string
}

// Validate checks that a non-empty ID matches CalculateHash.
func (t *Transaction) Validate() error {
	if t == nil {
		return errors.New("transaction is nil")
	}

	if t.ID != "" && t.ID != t.CalculateHash() {
		return ErrInvalidID
	}

	return nil
}

type utxoInPayload struct {
	PrevTxID string `json:"prev_txid"`
	OutIndex uint32 `json:"out_index"`
}

type utxoOutPayload struct {
	Amount  uint64 `json:"amount"`
	Address string `json:"address"`
}

type utxoHashPayload struct {
	Inputs    []utxoInPayload  `json:"inputs"`
	Outputs   []utxoOutPayload `json:"outputs"`
	Timestamp string           `json:"timestamp"`
	Contract  *contractPayload `json:"contract,omitempty"`
}

type contractPayload struct {
	Kind     string   `json:"kind"`
	Code     string   `json:"code"`
	Target   string   `json:"target"`
	Args     []uint64 `json:"args"`
	GasLimit uint64   `json:"gas_limit"`
}

// CalculateHash is the SHA-256 hex of the canonical JSON of the inputs (without
// PublicKey/Signature), the outputs, the UTC timestamp and, when present, the contract operation.
func (t *Transaction) CalculateHash() string {
	if t == nil {
		return ""
	}

	payload := utxoHashPayload{
		Inputs:    make([]utxoInPayload, len(t.Inputs)),
		Outputs:   make([]utxoOutPayload, len(t.Outputs)),
		Timestamp: t.Timestamp.UTC().Format(time.RFC3339Nano),
	}
	for i, in := range t.Inputs {
		payload.Inputs[i] = utxoInPayload{PrevTxID: in.PrevTxID, OutIndex: in.OutIndex}
	}

	for i, out := range t.Outputs {
		//nolint:staticcheck // S1016: explicit field copy pins the hashed payload shape
		payload.Outputs[i] = utxoOutPayload{Amount: out.Amount, Address: out.Address}
	}

	if c := t.Contract; c != nil {
		payload.Contract = &contractPayload{
			Kind:     c.Kind,
			Code:     c.Code,
			Target:   c.Target,
			Args:     append([]uint64{}, c.Args...),
			GasLimit: c.GasLimit,
		}
	}

	data, _ := json.Marshal(payload)
	h := sha256.Sum256(data)

	return hex.EncodeToString(h[:])
}

// SignInput signs the CalculateHash bytes with w and stores the key and signature in input i.
func SignInput(tx *Transaction, i int, w *wallet.Wallet) error {
	if tx == nil {
		return errors.New("transaction is nil")
	}

	if w == nil {
		return errors.New("wallet is nil")
	}

	if i < 0 || i >= len(tx.Inputs) {
		return ErrInputIndex
	}

	hashBytes, _ := hex.DecodeString(tx.CalculateHash())
	tx.Inputs[i].PublicKey = hex.EncodeToString(w.PublicKey())
	tx.Inputs[i].Signature = hex.EncodeToString(w.Sign(hashBytes))

	return nil
}

// VerifyInputSignature checks that input i carries a canonical, non-weak key
// whose address owns spent, and a valid signature over CalculateHash.
func VerifyInputSignature(tx *Transaction, i int, spent TxOut) error {
	if tx == nil {
		return errors.New("transaction is nil")
	}

	if i < 0 || i >= len(tx.Inputs) {
		return ErrInputIndex
	}

	in := tx.Inputs[i]

	if in.Signature == "" || in.PublicKey == "" {
		return ErrMissingSignature
	}

	if len(in.PublicKey) != 64 {
		return ErrInvalidPublicKey
	}

	rawPub, err := hex.DecodeString(in.PublicKey)
	if err != nil || hex.EncodeToString(rawPub) != in.PublicKey || isWeakPublicKey(rawPub) {
		return ErrInvalidPublicKey
	}

	if len(in.Signature) != 128 {
		return ErrInvalidSignature
	}

	rawSig, err := hex.DecodeString(in.Signature)
	if err != nil || hex.EncodeToString(rawSig) != in.Signature {
		return ErrInvalidSignature
	}

	hashBytes, _ := hex.DecodeString(tx.CalculateHash())
	if !wallet.Verify(ed25519.PublicKey(rawPub), hashBytes, rawSig) {
		return ErrInvalidSignature
	}

	if wallet.AddressFromPublicKey(ed25519.PublicKey(rawPub)) != spent.Address {
		return ErrInputOwnerMismatch
	}

	return nil
}
