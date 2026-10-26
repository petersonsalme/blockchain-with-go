package domain

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"
)

// DefaultMaxBlockBytes is the maximum serialized block size when MAX_BLOCK_BYTES is not set.
const DefaultMaxBlockBytes = 1 << 20

// MaxBlockBytesEnvVar overrides DefaultMaxBlockBytes when it holds a positive integer.
const MaxBlockBytesEnvVar = "MAX_BLOCK_BYTES"

// MaxFutureDrift is how far ahead of the current time a block timestamp may be.
const MaxFutureDrift = 2 * time.Hour

// MaxBlockBytes returns the maximum serialized size a block may have.
func MaxBlockBytes() int {
	raw := os.Getenv(MaxBlockBytesEnvVar)
	if raw == "" {
		return DefaultMaxBlockBytes
	}

	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		return DefaultMaxBlockBytes
	}

	return limit
}

// Size returns the length in bytes of the block's JSON encoding.
func (b *Block) Size() (int, error) {
	encoded, err := json.Marshal(b)
	if err != nil {
		return 0, err
	}

	return len(encoded), nil
}

// ValidateSize rejects blocks whose serialized size is greater than limit.
func ValidateSize(b *Block, limit int) error {
	size, err := b.Size()
	if err != nil {
		return err
	}

	if size > limit {
		return &ValidationError{
			Code:    CodeBlockTooLarge,
			Message: fmt.Sprintf("block size %d exceeds the maximum of %d bytes", size, limit),
			Details: map[string]any{"size": size, "max": limit},
		}
	}

	return nil
}

// ValidateTimestamp rejects blocks older than their parent or too far in the future.
func ValidateTimestamp(b, parent *Block, now time.Time) error {
	if b.Timestamp.Before(parent.Timestamp) {
		return &ValidationError{
			Code:    CodeTimestampBeforeParent,
			Message: "block timestamp is before its parent's timestamp",
			Details: map[string]any{
				"timestamp":        b.Timestamp.Format(time.RFC3339Nano),
				"parent_timestamp": parent.Timestamp.Format(time.RFC3339Nano),
			},
		}
	}

	limit := now.Add(MaxFutureDrift)
	if b.Timestamp.After(limit) {
		return &ValidationError{
			Code:    CodeTimestampTooFarInTheFuture,
			Message: fmt.Sprintf("block timestamp is more than %s in the future", MaxFutureDrift),
			Details: map[string]any{
				"timestamp": b.Timestamp.Format(time.RFC3339Nano),
				"limit":     limit.Format(time.RFC3339Nano),
			},
		}
	}

	return nil
}

// ValidateMerkleRoot rejects blocks whose MerkleRoot does not match their transactions.
func ValidateMerkleRoot(b *Block) error {
	expected := MerkleRoot(b.Transactions)
	if b.MerkleRoot != expected {
		return &ValidationError{
			Code:    CodeMerkleRootMismatch,
			Message: "block merkle root does not match its transactions",
			Details: map[string]any{"expected": expected, "got": b.MerkleRoot},
		}
	}

	return nil
}

// ValidateBlockRules applies the structural block rules (size, merkle root and timestamp).
func ValidateBlockRules(b, parent *Block, now time.Time) error {
	err := ValidateSize(b, MaxBlockBytes())
	if err != nil {
		return err
	}

	err = ValidateMerkleRoot(b)
	if err != nil {
		return err
	}

	return ValidateTimestamp(b, parent, now)
}
