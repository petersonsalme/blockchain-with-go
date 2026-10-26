// Package wallet provides Ed25519 identities, checksummed addresses and an
// encrypted on-disk keystore.
package wallet

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
)

// Wallet holds an Ed25519 private key.
type Wallet struct {
	priv ed25519.PrivateKey
}

// New generates a wallet using crypto/rand.
func New() (*Wallet, error) {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, fmt.Errorf("wallet: generating seed: %w", err)
	}

	return FromSeed(seed)
}

// FromSeed builds a deterministic wallet from a 32-byte seed.
func FromSeed(seed []byte) (*Wallet, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("wallet: seed must be %d bytes, got %d", ed25519.SeedSize, len(seed))
	}

	return &Wallet{priv: ed25519.NewKeyFromSeed(seed)}, nil
}

// PublicKey returns the wallet public key.
func (w *Wallet) PublicKey() ed25519.PublicKey {
	return w.priv.Public().(ed25519.PublicKey) //nolint:forcetypeassert // ed25519.PrivateKey.Public always returns an ed25519.PublicKey
}

// Address returns the checksummed address of the wallet.
func (w *Wallet) Address() string {
	return AddressFromPublicKey(w.PublicKey())
}

// Sign signs msg with the wallet private key.
func (w *Wallet) Sign(msg []byte) []byte {
	return ed25519.Sign(w.priv, msg)
}

// Verify reports whether sig is a valid signature of msg by pub.
// Invalid key sizes yield false.
func Verify(pub ed25519.PublicKey, msg, sig []byte) bool {
	if len(pub) != ed25519.PublicKeySize {
		return false
	}

	return ed25519.Verify(pub, msg, sig)
}
