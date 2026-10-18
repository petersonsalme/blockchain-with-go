package domain

import "fmt"

// Validation error codes.
const (
	CodeBlockTooLarge              = "block_too_large"
	CodeTimestampBeforeParent      = "timestamp_before_parent"
	CodeTimestampTooFarInTheFuture = "timestamp_too_far_in_future"
)

// ValidationError is a structured, machine-readable block rule violation.
type ValidationError struct {
	Code    string
	Message string
	Details map[string]any
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}
