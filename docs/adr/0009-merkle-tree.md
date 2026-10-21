# ADR-0009: Transaction Merkle tree in the block header

Status: Accepted (Lead, ENG-20, 2026-10-21). Originally drafted as ADR-0004; renumbered because 0004 to 0008 were taken by the time of implementation.

## Context
- `Block.CalculateHash` covered the transactions through `TransactionsHash`, a linear SHA-256 of every `tx.CalculateHash()` and signature. Proving that a transaction is in a block required all the transactions of the block.
- ENG-20 asks for a `MerkleRoot` in the header, inclusion proofs and a documented rule for an odd number of leaves.
- `Transaction.ID` (ADR-0001) does not cover the `PublicKey` and `Signature` of the inputs. The block hash covered the signatures but not the public keys. "Altering a transaction invalidates the root" requires the leaf to cover the whole transaction.

## Options (odd number of leaves)
A. Duplicate the last node (Bitcoin). Known flaw (CVE-2012-2459): `[a,b,c]` and `[a,b,c,c]` share a root, and without domain separation an inner node can pass as a leaf.
B. Promote the unpaired last node to the next level without rehashing, with domain prefixes `0x00` (leaf) and `0x01` (inner node), as in RFC 6962 section 2.1. A proof can have fewer steps than the tree height, so every step carries the side of its sibling.
C. Pad to a power of two with a fixed empty leaf. Adds fake leaves and a bigger tree, and the proof of an empty leaf is ambiguous.

## Decision
Option B, with SHA-256 and 64-character lowercase hex:
- **Leaf:** `SHA-256(0x00 || leafData(tx))`, where `leafData(tx)` is `ascii(tx.CalculateHash())` followed, for each input in order, by `u32be(len(PublicKey)) || PublicKey || u32be(len(Signature)) || Signature`. It uses the recalculated hash, not the `ID` field.
- **Inner node:** `SHA-256(0x01 || left || right)`, with the 32 raw bytes of each child.
- **Odd count:** at every level the last unpaired node moves up unchanged.
- **One leaf:** the root is the leaf hash. **Zero transactions:** the root is `SHA-256("")` = `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.
- **Header:** `Block.MerkleRoot string`. `CalculateHash` uses `b.MerkleRoot` instead of `TransactionsHash`, which is removed.
- **Validation:** `ValidateBlockRules` rejects a block whose `MerkleRoot` differs from `MerkleRoot(Transactions)` with the code `merkle_root_mismatch`. The genesis and `GenerateBlockWithContext` fill the root before sealing.
- **Proof:** a list of steps `{sibling hash, side}` from the leaf to the root. Verifying needs only the leaf hash, the steps and the root.

## Consequences
- The block hash commits to the transactions only through the root, so `merkle_root_mismatch` must run on every path that accepts a block (today: `Mine`). Consensus `Verify` checks the seal over the header and no longer notices a transaction edit on its own. Replaying the stored chain in `NewService` still does not revalidate blocks (pre-existing gap, out of scope).
- Any change to a field of a transaction (including `PublicKey` and `Signature`), a reorder, an insertion or a removal changes the root.
- `GET /` exposes `MerkleRoot` on each block (additive).
- Block hashes differ from earlier versions. A `chain.db` written before this change holds blocks without `MerkleRoot`.
- A light client can verify inclusion with the header and a proof, without the other transactions.
