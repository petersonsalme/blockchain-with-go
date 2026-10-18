package wallet

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

const (
	addressVersion   byte = 0x00
	pubKeyHashLen         = 20
	checksumLen           = 4
	addressByteLen        = 1 + pubKeyHashLen + checksumLen
	addressStringLen      = addressByteLen * 2
)

var (
	ErrInvalidAddressLength   = errors.New("wallet: invalid address length")
	ErrInvalidAddressEncoding = errors.New("wallet: invalid address encoding")
	ErrInvalidAddressVersion  = errors.New("wallet: invalid address version")
	ErrInvalidAddressChecksum = errors.New("wallet: invalid address checksum")
)

func checksum(payload []byte) []byte {
	first := sha256.Sum256(payload)
	second := sha256.Sum256(first[:])
	return second[:checksumLen]
}

// AddressFromPublicKey derives the address: hex(version || SHA256(pub)[:20] || checksum).
func AddressFromPublicKey(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	payload := append([]byte{addressVersion}, h[:pubKeyHashLen]...)
	return hex.EncodeToString(append(payload, checksum(payload)...))
}

// ValidateAddress checks length, encoding (lowercase hex), version and checksum.
func ValidateAddress(addr string) error {
	if len(addr) != addressStringLen {
		return ErrInvalidAddressLength
	}
	raw, err := hex.DecodeString(addr)
	if err != nil || hex.EncodeToString(raw) != addr {
		return ErrInvalidAddressEncoding
	}
	if raw[0] != addressVersion {
		return ErrInvalidAddressVersion
	}
	payload := raw[:addressByteLen-checksumLen]
	if !bytes.Equal(checksum(payload), raw[addressByteLen-checksumLen:]) {
		return ErrInvalidAddressChecksum
	}
	return nil
}
