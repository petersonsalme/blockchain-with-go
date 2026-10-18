package wallet

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/scrypt"
)

const (
	keystoreVersion = 1
	kdfName         = "scrypt"
	cipherName      = "aes-256-gcm"
	saltLen         = 32
	nonceLen        = 12
	dkLen           = 32

	maxScryptN = 1 << 20
	maxScryptR = 32
	maxScryptP = 16
)

var (
	ErrInvalidKeystore = errors.New("wallet: invalid keystore")
	ErrWrongPassword   = errors.New("wallet: wrong password")
	ErrEmptyPassword   = errors.New("wallet: empty password")
)

// scrypt parameters used by Save; overridable in tests only.
var scryptN, scryptR, scryptP = 1 << 15, 8, 1

type keystoreFile struct {
	Version int          `json:"version"`
	Address string       `json:"address"`
	Crypto  keystoreCryp `json:"crypto"`
}

type keystoreCryp struct {
	KDF       string    `json:"kdf"`
	KDFParams kdfParams `json:"kdfparams"`
	Cipher    string    `json:"cipher"`
	Nonce     string    `json:"nonce"`
	Ciphertxt string    `json:"ciphertext"`
}

type kdfParams struct {
	N     int    `json:"n"`
	R     int    `json:"r"`
	P     int    `json:"p"`
	DKLen int    `json:"dklen"`
	Salt  string `json:"salt"`
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidKeystore, fmt.Sprintf(format, args...))
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Save encrypts the wallet seed with password and atomically writes it to path (0600).
func (w *Wallet) Save(path, password string) error {
	if password == "" {
		return ErrEmptyPassword
	}
	salt := make([]byte, saltLen)
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("wallet: generating salt: %w", err)
	}
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("wallet: generating nonce: %w", err)
	}
	key, err := scrypt.Key([]byte(password), salt, scryptN, scryptR, scryptP, dkLen)
	if err != nil {
		return fmt.Errorf("wallet: deriving key: %w", err)
	}
	gcm, err := newGCM(key)
	if err != nil {
		return fmt.Errorf("wallet: creating cipher: %w", err)
	}
	addr := w.Address()
	ct := gcm.Seal(nil, nonce, w.priv.Seed(), []byte(addr))

	data, err := json.MarshalIndent(keystoreFile{
		Version: keystoreVersion,
		Address: addr,
		Crypto: keystoreCryp{
			KDF:       kdfName,
			KDFParams: kdfParams{N: scryptN, R: scryptR, P: scryptP, DKLen: dkLen, Salt: hex.EncodeToString(salt)},
			Cipher:    cipherName,
			Nonce:     hex.EncodeToString(nonce),
			Ciphertxt: hex.EncodeToString(ct),
		},
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("wallet: encoding keystore: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".keystore-*")
	if err != nil {
		return fmt.Errorf("wallet: creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { tmp.Close(); os.Remove(tmpName) }
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("wallet: setting permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("wallet: writing keystore: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("wallet: syncing keystore: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("wallet: closing keystore: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("wallet: replacing keystore: %w", err)
	}
	return nil
}

// Load reads and decrypts a keystore file.
func Load(path, password string) (*Wallet, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("wallet: reading keystore: %w", err)
	}
	var ks keystoreFile
	if err := json.Unmarshal(raw, &ks); err != nil {
		return nil, invalid("malformed json: %v", err)
	}
	if ks.Version != keystoreVersion {
		return nil, invalid("unsupported version %d", ks.Version)
	}
	c := ks.Crypto
	if c.KDF != kdfName {
		return nil, invalid("unsupported kdf %q", c.KDF)
	}
	if c.Cipher != cipherName {
		return nil, invalid("unsupported cipher %q", c.Cipher)
	}
	p := c.KDFParams
	if p.DKLen != dkLen {
		return nil, invalid("unsupported dklen %d", p.DKLen)
	}
	if p.N < 2 || p.N > maxScryptN || p.N&(p.N-1) != 0 {
		return nil, invalid("scrypt n out of range")
	}
	if p.R < 1 || p.R > maxScryptR || p.P < 1 || p.P > maxScryptP || p.R*p.P >= 1<<30 {
		return nil, invalid("scrypt r/p out of range")
	}
	salt, err := hex.DecodeString(p.Salt)
	if err != nil || len(salt) != saltLen {
		return nil, invalid("bad salt")
	}
	nonce, err := hex.DecodeString(c.Nonce)
	if err != nil || len(nonce) != nonceLen {
		return nil, invalid("bad nonce")
	}
	ct, err := hex.DecodeString(c.Ciphertxt)
	if err != nil {
		return nil, invalid("bad ciphertext")
	}
	if password == "" {
		return nil, ErrEmptyPassword
	}
	key, err := scrypt.Key([]byte(password), salt, p.N, p.R, p.P, dkLen)
	if err != nil {
		return nil, invalid("deriving key: %v", err)
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, invalid("creating cipher: %v", err)
	}
	seed, err := gcm.Open(nil, nonce, ct, []byte(ks.Address))
	if err != nil {
		return nil, ErrWrongPassword
	}
	if len(seed) != ed25519.SeedSize {
		return nil, invalid("bad seed length")
	}
	w, err := FromSeed(seed)
	if err != nil {
		return nil, invalid("%v", err)
	}
	if w.Address() != ks.Address {
		return nil, invalid("address mismatch")
	}
	return w, nil
}
