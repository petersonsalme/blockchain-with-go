# ADR-0017: HD wallet with BIP-39 mnemonics and SLIP-0010 ed25519 derivation

Status: Accepted

## Context

`pkg/wallet` holds one Ed25519 key per keystore file (ENG-14). A lost file cannot be recovered, and every new address needs a new file. ENG-22 asks for a 12/24-word mnemonic, key derivation by path, recovery from the mnemonic, and the official BIP-39 test vectors.

BIP-32 defines derivation for secp256k1 only: its non-hardened derivation adds scalars to public points, and Ed25519 private keys are not plain scalars (they are clamped hashes of a seed). Transactions are signed and verified with Ed25519 (`wallet.Sign`/`wallet.Verify`, `domain` signature checks), so the curve is fixed.

## Options

1. **BIP-39 + SLIP-0010 for ed25519 (chosen).** Mnemonic and seed follow BIP-39 exactly; derivation follows SLIP-0010, the BIP-32 variant for ed25519, which allows hardened children only. Cost: about 200 lines on the standard library (`crypto/hmac`, `crypto/sha512`, `crypto/sha256`, `crypto/pbkdf2`, `embed`) plus `golang.org/x/text/unicode/norm` for NFKD (already in the module graph); official vectors exist for both halves (trezor/python-mnemonic, SLIP-0010). No change to signatures, addresses or the keystore.
2. **BIP-39 + BIP-32 on secp256k1.** Real BIP-32 with xpub and non-hardened children. Cost: changes the signature scheme of every transaction and address (schema and consensus change), and a secp256k1 dependency. Out of proportion for ENG-22.
3. **BIP-39 + ad-hoc derivation** (e.g. `SHA-512(seed || index)`). Cost: little code, but no official vectors, no interoperability, and a home-made KDF in a crypto package.

## Decision

Option 1.

- Mnemonic: BIP-39 English wordlist (2048 words, embedded, SHA-256 `2f5eed53a4727b4bf8880d8f3f199efc90e58503646d9ff8eff3a2ed3b24dbda`). `NewMnemonic` generates 12 or 24 words from `crypto/rand`; `MnemonicFromEntropy` and `ValidateMnemonic` accept every BIP-39 length (12, 15, 18, 21, 24 words) so the official vectors run unchanged. Only the English list is supported.
- Seed: `PBKDF2-HMAC-SHA512(NFKD(mnemonic), "mnemonic" + NFKD(passphrase), 2048, 64)`. Before hashing, the mnemonic is NFKD-normalized and its words are re-joined with single spaces, so extra or different whitespace recovers the same wallet. An invalid mnemonic (unknown word, word count, checksum) is rejected before the seed is computed.
- Derivation: SLIP-0010 ed25519. Master `I = HMAC-SHA512("ed25519 seed", seed)`; child `I = HMAC-SHA512(c, 0x00 || k || ser32(i + 2^31))`; `k = I[0:32]` becomes the Ed25519 seed passed to `FromSeed`. Paths look like `m/44'/1'/0'/0'/0'`; every segment must be hardened (`'`) and below 2^31; seeds must be 16 to 64 bytes.
- The derived key is an ordinary `*Wallet`: signing, addresses and `Save`/`Load` do not change. The keystore stores a derived key, never the mnemonic.

## Consequences

- One mnemonic recovers every address derived from it; derivation is deterministic for the same (mnemonic, passphrase, path).
- No xpub: with hardened-only derivation, a watch-only wallet cannot derive child public keys. Each address needs the seed.
- The wallet does not pick a default path or a SLIP-44 coin type; callers pass the path.
- Wallets from other software agree with this one only if they also use SLIP-0010 ed25519 with the same path.
- The BIP-39 vectors file also carries BIP-32 `xprv` values; they are secp256k1 and are ignored.
