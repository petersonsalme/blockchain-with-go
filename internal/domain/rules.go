package domain

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// MaxBlockBytes is the maximum JSON-serialized size of a block (1 MiB). It is a
// consensus rule fixed at compile time, like InitialSubsidy: no node can
// configure it (ADR-0032).
const MaxBlockBytes = 1 << 20

// MaxFutureDrift is how far ahead of the current time a block timestamp may be.
const MaxFutureDrift = 2 * time.Hour

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

// MedianTimeSpan is how many of the newest blocks the median time past covers (ADR-0034).
const MedianTimeSpan = 11

// MedianTimePast is the median timestamp of the last MedianTimeSpan blocks of
// history (all of them when fewer): the element at index len/2 of the sorted
// timestamps. It is the zero time for an empty history.
func MedianTimePast(history []*Block) time.Time {
	window := history[max(0, len(history)-MedianTimeSpan):]

	if len(window) == 0 {
		return time.Time{}
	}

	stamps := make([]time.Time, len(window))
	for i, b := range window {
		stamps[i] = b.Timestamp
	}

	sort.Slice(stamps, func(i, j int) bool { return stamps[i].Before(stamps[j]) })

	return stamps[len(stamps)/2]
}

// ValidateTimestamp rejects blocks not strictly later than the median time past
// of history (which ends with the block's parent) or too far in the future.
func ValidateTimestamp(b *Block, history []*Block, now time.Time) error {
	median := MedianTimePast(history)
	if !b.Timestamp.After(median) {
		return &ValidationError{
			Code:    CodeTimestampNotAfterMedianTimePast,
			Message: "block timestamp is not later than the median time past of the last blocks",
			Details: map[string]any{
				"timestamp":        b.Timestamp.Format(time.RFC3339Nano),
				"median_time_past": median.Format(time.RFC3339Nano),
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
func ValidateBlockRules(b *Block, history []*Block, now time.Time) error {
	err := ValidateSize(b, MaxBlockBytes)
	if err != nil {
		return err
	}

	err = ValidateMerkleRoot(b)
	if err != nil {
		return err
	}

	return ValidateTimestamp(b, history, now)
}
