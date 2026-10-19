# ADR-0005: Proof of Authority seal fields on Block

Status: Accepted (board, 2026-10-19, ENG-43)

## Context
- Consensus is a port (ENG-12) with one adapter, `pow`. `domain.Block` carries only `Nonce`/`Difficulty`/`Hash` as consensus fields, and `Consensus.Seal` is documented as filling exactly those.
- PoA needs the block to name its sealer and carry a verifiable signature; anything kept outside the block cannot be verified after a restart or by another node.
- Ed25519 keys, addresses and an encrypted keystore already exist in `pkg/wallet` (ENG-21).

## Options
A. Explicit fields `Validator`/`Signature` on `domain.Block`. Cost: low (2 fields, 1 line in the hash, additive JSON).
B. Opaque `Seal json.RawMessage` interpreted per adapter. Cost: medium (byte canonicalisation in the hash, validation spread out). Only pays off with PoS on the roadmap.
C. Signature stored outside the block. Rejected: not verifiable after restart or by another node.

## Decision
Option A.
- `domain.Block` gains `Validator string` (hex Ed25519 public key) and `Signature string` (hex).
- `CalculateHash` appends `Validator` at the end of the record; an empty `Validator` yields the same record, so existing PoW hashes are unchanged. `Signature` is not hashed.
- `internal/consensus/poa`: fixed validator set. `Seal` sets `Nonce=0`, `Difficulty=0`, `Validator`=own key, `Hash=CalculateHash()`, `Signature=Sign(hex-decoded Hash)`. `Verify` checks index, prev hash, hash, `Nonce==0 && Difficulty==0`, `Validator` ∈ set, signature valid. Any validator may seal any height (no round-robin turn) — single-node educational project.
- Selection in `cmd/node`: `CONSENSUS=pow|poa` (default `pow`; any other value is a startup error). PoA config: `POA_VALIDATORS` (comma-separated hex public keys, non-empty, valid size), `POA_KEYSTORE` + `POA_KEYSTORE_PASSWORD` (`wallet.Load`); the loaded key must be in the set.
- New sentinels in `internal/consensus`: `ErrUnknownValidator`, `ErrInvalidSignature`, `ErrInvalidSeal`.

## Consequences
- `Consensus.Seal` doc changes to "fills the consensus fields of b" (adapter-specific).
- `GET /`, `GET /blocks` and `GET /block/{id}` expose two new fields (empty under PoW); additive.
- A chain is bound to one consensus: PoA rejects PoW blocks (`Difficulty`/`Nonce` != 0 or no `Validator`) and PoW blocks carry empty `Validator`/`Signature`. Switching `CONSENSUS` on an existing disk chain (ENG-27) is unsupported.
- No proposer rotation, no on-chain validator-set changes, no fork choice between validators — can-wait.
