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

	max, err := strconv.Atoi(raw)
	if err != nil || max <= 0 {
		return DefaultMaxBlockBytes
	}

	return max
}

// Size returns the length in bytes of the block's JSON encoding.
func (b *Block) Size() (int, error) {
	encoded, err := json.Marshal(b)
	if err != nil {
		return 0, err
	}

	return len(encoded), nil
}

// ValidateSize rejects blocks whose serialized size is greater than max.
func ValidateSize(b *Block, max int) error {
	size, err := b.Size()
	if err != nil {
		return err
	}

	if size > max {
		return &ValidationError{
			Code:    CodeBlockTooLarge,
			Message: fmt.Sprintf("block size %d exceeds the maximum of %d bytes", size, max),
			Details: map[string]any{"size": size, "max": max},
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

// ValidateBlockRules applies the structural block rules (size and timestamp).
func ValidateBlockRules(b, parent *Block, now time.Time) error {
	if err := ValidateSize(b, MaxBlockBytes()); err != nil {
		return err
	}

	return ValidateTimestamp(b, parent, now)
}
