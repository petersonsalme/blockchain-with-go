package wallet

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	_ "embed"
	"errors"
	"fmt"
	"slices"
	"strings"

	"golang.org/x/text/unicode/norm"
)

//go:embed bip39_english.txt
var bip39English string

var englishWords = strings.Fields(bip39English)

var (
	ErrInvalidEntropy  = errors.New("wallet: invalid entropy")
	ErrInvalidMnemonic = errors.New("wallet: invalid mnemonic")
)

// MnemonicFromEntropy encodes entropy into a BIP-39 mnemonic sentence.
// Supported entropy lengths are 16, 20, 24, 28, and 32 bytes.
func MnemonicFromEntropy(entropy []byte) (string, error) {
	switch len(entropy) {
	case 16, 20, 24, 28, 32:
	default:
		return "", fmt.Errorf("%w: length must be 16, 20, 24, 28, or 32 bytes, got %d", ErrInvalidEntropy, len(entropy))
	}

	entBits := len(entropy) * 8
	csBits := entBits / 32
	numWords := (entBits + csBits) / 11

	hash := sha256.Sum256(entropy)

	getBit := func(bitPos int) int {
		if bitPos < entBits {
			return int((entropy[bitPos/8] >> (7 - (bitPos % 8))) & 1)
		}

		csPos := bitPos - entBits

		return int((hash[0] >> (7 - csPos)) & 1)
	}

	words := make([]string, numWords)
	for i := range numWords {
		idx := 0
		for j := range 11 {
			idx = (idx << 1) | getBit(i*11+j)
		}

		words[i] = englishWords[idx]
	}

	return strings.Join(words, " "), nil
}

// NewMnemonic generates a random BIP-39 mnemonic with 12 or 24 words.
func NewMnemonic(words int) (string, error) {
	var size int

	switch words {
	case 12:
		size = 16
	case 24:
		size = 32
	default:
		return "", fmt.Errorf("%w: word count must be 12 or 24, got %d", ErrInvalidEntropy, words)
	}

	entropy := make([]byte, size)
	if _, err := rand.Read(entropy); err != nil {
		return "", fmt.Errorf("wallet: failed to read random entropy: %w", err)
	}

	return MnemonicFromEntropy(entropy)
}

// ValidateMnemonic verifies that the mnemonic has valid word count, dictionary words, and checksum.
func ValidateMnemonic(mnemonic string) error {
	_, err := mnemonicToEntropy(mnemonic)
	return err
}

// MnemonicToSeed derives a BIP-39 binary seed from a mnemonic and passphrase using PBKDF2 and NFKD normalization.
func MnemonicToSeed(mnemonic, passphrase string) ([]byte, error) {
	m := strings.Join(strings.Fields(norm.NFKD.String(mnemonic)), " ")
	if err := ValidateMnemonic(m); err != nil {
		return nil, err
	}

	return pbkdf2.Key(sha512.New, m, []byte("mnemonic"+norm.NFKD.String(passphrase)), 2048, 64)
}

func mnemonicToEntropy(mnemonic string) ([]byte, error) {
	ws := strings.Fields(norm.NFKD.String(mnemonic))
	switch len(ws) {
	case 12, 15, 18, 21, 24:
	default:
		return nil, fmt.Errorf("%w: word count must be 12, 15, 18, 21, or 24, got %d", ErrInvalidMnemonic, len(ws))
	}

	indices := make([]int, len(ws))
	for i, w := range ws {
		idx, found := slices.BinarySearch(englishWords, w)
		if !found {
			return nil, fmt.Errorf("%w: unknown word %q", ErrInvalidMnemonic, w)
		}

		indices[i] = idx
	}

	n := len(ws)
	entBits := 32 * n / 3
	csBits := n / 3
	entropyBytes := entBits / 8
	totalBits := 11 * n
	totalBytes := (totalBits + 7) / 8

	data := make([]byte, totalBytes)

	for i, idx := range indices {
		for j := range 11 {
			bit := (idx >> (10 - j)) & 1
			if bit == 1 {
				bitPos := i*11 + j
				data[bitPos/8] |= byte(1 << (7 - (bitPos % 8)))
			}
		}
	}

	entropy := data[:entropyBytes]
	hash := sha256.Sum256(entropy)

	mnemonicCS := data[entropyBytes] >> (8 - csBits)

	expectedCS := hash[0] >> (8 - csBits)
	if mnemonicCS != expectedCS {
		return nil, fmt.Errorf("%w: checksum mismatch", ErrInvalidMnemonic)
	}

	return entropy, nil
}
