package wallet

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrInvalidPath indicates that a derivation path is malformed or uses
// unhardened derivation, which is not supported by SLIP-0010 for Ed25519 (ADR-0017).
var ErrInvalidPath = errors.New("wallet: invalid derivation path")

const hardenedOffset uint32 = 1 << 31

func parsePath(path string) ([]uint32, error) {
	segs := strings.Split(path, "/")
	if segs[0] != "m" {
		return nil, fmt.Errorf("%w: path must start with 'm'", ErrInvalidPath)
	}
	if len(segs) == 1 {
		return nil, nil
	}
	indices := make([]uint32, 0, len(segs)-1)
	for _, seg := range segs[1:] {
		if seg == "" {
			return nil, fmt.Errorf("%w: empty segment", ErrInvalidPath)
		}
		if !strings.HasSuffix(seg, "'") {
			return nil, fmt.Errorf("%w: only hardened derivation is supported", ErrInvalidPath)
		}
		numStr := seg[:len(seg)-1]
		n, err := strconv.ParseUint(numStr, 10, 31)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidPath, err)
		}
		indices = append(indices, uint32(n))
	}
	return indices, nil
}

func hmacSHA512(key, data []byte) []byte {
	h := hmac.New(sha512.New, key)
	_, _ = h.Write(data)
	return h.Sum(nil)
}

func deriveSLIP10(seed []byte, path string) (key, chainCode []byte, err error) {
	if len(seed) < 16 || len(seed) > 64 {
		return nil, nil, fmt.Errorf("wallet: seed must be 16 to 64 bytes, got %d", len(seed))
	}
	idx, err := parsePath(path)
	if err != nil {
		return nil, nil, err
	}

	I := hmacSHA512([]byte("ed25519 seed"), seed)
	key, chainCode = I[:32], I[32:]

	for _, i := range idx {
		data := make([]byte, 0, 37)
		data = append(data, 0x00)
		data = append(data, key...)
		data = binary.BigEndian.AppendUint32(data, i+hardenedOffset)
		I = hmacSHA512(chainCode, data)
		key, chainCode = I[:32], I[32:]
	}
	return key, chainCode, nil
}

// DeriveKey derives an Ed25519 Wallet from a master seed using SLIP-0010
// hardened key derivation (ADR-0017). The path must start with "m" and every
// subsequent component must be hardened (e.g. "m/44'/1'/0'/0'/0'").
func DeriveKey(seed []byte, path string) (*Wallet, error) {
	key, _, err := deriveSLIP10(seed, path)
	if err != nil {
		return nil, err
	}
	return FromSeed(key)
}

// FromMnemonic derives an Ed25519 Wallet from a BIP-39 mnemonic phrase and
// optional passphrase using SLIP-0010 key derivation at the specified path (ADR-0017).
func FromMnemonic(mnemonic, passphrase, path string) (*Wallet, error) {
	seed, err := MnemonicToSeed(mnemonic, passphrase)
	if err != nil {
		return nil, err
	}
	return DeriveKey(seed, path)
}
