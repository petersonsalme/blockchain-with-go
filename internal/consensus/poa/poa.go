// Package poa implements a Proof of Authority Consensus: blocks are sealed with
// an Ed25519 signature from a member of a fixed validator set.
package poa

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"

	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

var _ consensus.Consensus = (*ProofOfAuthority)(nil)

// ProofOfAuthority accepts blocks signed by any validator in a fixed set.
type ProofOfAuthority struct {
	validators []ed25519.PublicKey
	signer     *wallet.Wallet
}

// New builds a ProofOfAuthority. The set must be non-empty with well-formed
// keys and must contain signer's public key.
func New(validators []ed25519.PublicKey, signer *wallet.Wallet) (*ProofOfAuthority, error) {
	if len(validators) == 0 {
		return nil, errors.New("poa: validator set is empty")
	}

	set := make([]ed25519.PublicKey, len(validators))
	for i, v := range validators {
		if len(v) != ed25519.PublicKeySize {
			return nil, errors.New("poa: validator key has invalid size")
		}

		set[i] = append(ed25519.PublicKey(nil), v...)
	}

	if signer == nil {
		return nil, errors.New("poa: signer is nil")
	}

	p := &ProofOfAuthority{validators: set, signer: signer}
	if !p.isValidator(signer.PublicKey()) {
		return nil, errors.New("poa: signer is not in the validator set")
	}

	return p, nil
}

// Seal fills the consensus fields of b: it names the signer as validator,
// computes the hash and signs it.
func (p *ProofOfAuthority) Seal(ctx context.Context, _ []*domain.Block, b *domain.Block) error { //nolint:contextcheck // Seal accepts a nil context from callers that have none
	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	b.Nonce = 0
	b.Difficulty = 0
	b.Validator = hex.EncodeToString(p.signer.PublicKey())
	b.Hash = b.CalculateHash()

	// Hash is the hex output of CalculateHash, so decoding cannot fail.
	raw, _ := hex.DecodeString(b.Hash)
	b.Signature = hex.EncodeToString(p.signer.Sign(raw))

	return nil
}

// Verify checks b against its predecessor, the last block of history, then
// that it was signed by a member of the validator set.
func (p *ProofOfAuthority) Verify(history []*domain.Block, b *domain.Block) error {
	prev := history[len(history)-1]
	if prev.Index+1 != b.Index {
		return consensus.ErrInvalidIndex
	}

	if prev.Hash != b.PrevHash {
		return consensus.ErrInvalidPrevHash
	}

	if b.CalculateHash() != b.Hash {
		return consensus.ErrInvalidHash
	}

	if b.Nonce != 0 || b.Difficulty != 0 {
		return consensus.ErrInvalidSeal
	}

	key, err := hex.DecodeString(b.Validator)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return consensus.ErrInvalidSeal
	}

	pub := ed25519.PublicKey(key)
	if !p.isValidator(pub) {
		return consensus.ErrUnknownValidator
	}

	sig, err := hex.DecodeString(b.Signature)
	// The re-encoding check rejects uppercase hex: one block, one signature form.
	if err != nil || len(sig) != ed25519.SignatureSize || b.Signature != hex.EncodeToString(sig) {
		return consensus.ErrInvalidSignature
	}

	// b.Hash equals CalculateHash here, so decoding cannot fail.
	hash, _ := hex.DecodeString(b.Hash)
	if !wallet.Verify(pub, hash, sig) {
		return consensus.ErrInvalidSignature
	}

	return nil
}

func (p *ProofOfAuthority) isValidator(key ed25519.PublicKey) bool {
	for _, v := range p.validators {
		if bytes.Equal(v, key) {
			return true
		}
	}

	return false
}
