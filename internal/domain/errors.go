package domain

import "fmt"

// Validation error codes.
const (
	CodeBlockTooLarge              = "block_too_large"
	CodeTimestampBeforeParent      = "timestamp_before_parent"
	CodeTimestampTooFarInTheFuture = "timestamp_too_far_in_future"
)

// Transaction error codes.
const (
	CodeMissingSignature    = "missing_signature"
	CodeInvalidSignature    = "invalid_signature"
	CodeInvalidPublicKey    = "invalid_public_key"
	CodeMissingTimestamp    = "missing_timestamp"
	CodeInputMissingOrSpent = "input_missing_or_spent"
	CodeDoubleSpend         = "double_spend"
	CodeInsufficientInputs  = "insufficient_inputs"
	CodeInputOwnerMismatch  = "input_owner_mismatch"
	CodeInvalidOutput       = "invalid_output"
	CodeMissingInputs       = "missing_inputs"
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
