package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
)

// requestTooLargeCode is the error code returned when the request body exceeds the allowed size.
const requestTooLargeCode = "request_too_large"

// chainConflictCode is the error code returned when another block was appended while mining.
const chainConflictCode = "chain_conflict"

// endpointGoneCode is the error code returned by the removed POST /legacy/bpm.
const endpointGoneCode = "endpoint_gone"

// rateLimitedCode is the error code returned when requests exceed the rate limit.
const rateLimitedCode = "rate_limited"

// transactionErrors maps transaction rejections to their HTTP status and code.
var transactionErrors = []struct {
	err    error
	status int
	code   string
}{
	{domain.ErrMissingSignature, http.StatusBadRequest, domain.CodeMissingSignature},
	{domain.ErrInvalidSignature, http.StatusBadRequest, domain.CodeInvalidSignature},
	{domain.ErrInvalidPublicKey, http.StatusBadRequest, domain.CodeInvalidPublicKey},
	{domain.ErrInputOwnerMismatch, http.StatusBadRequest, domain.CodeInputOwnerMismatch},
	{domain.ErrInvalidOutput, http.StatusBadRequest, domain.CodeInvalidOutput},
	{domain.ErrMissingInputs, http.StatusBadRequest, domain.CodeMissingInputs},
	{domain.ErrInputMissingOrSpent, http.StatusConflict, domain.CodeInputMissingOrSpent},
	{domain.ErrDoubleSpend, http.StatusConflict, domain.CodeDoubleSpend},
	{domain.ErrInsufficientInputs, http.StatusUnprocessableEntity, domain.CodeInsufficientInputs},
}

// respondWithMineError maps an error from chain mining to its HTTP response.
func respondWithMineError(w http.ResponseWriter, err error) {
	for _, m := range transactionErrors {
		if errors.Is(err, m.err) {
			respondWithJSON(w, m.status, errorResponse{Error: err.Error(), Code: m.code})
			return
		}
	}

	var validationErr *domain.ValidationError
	switch {
	case errors.As(err, &validationErr):
		respondWithJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: validationErr.Message, Code: validationErr.Code, Details: validationErr.Details})
	case errors.Is(err, chain.ErrInvalidBlock):
		respondWithJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "invalid block could not be added"})
	case errors.Is(err, storage.ErrConflict):
		respondWithJSON(w, http.StatusConflict, errorResponse{Error: "chain changed during mining; try again", Code: chainConflictCode})
	case errors.Is(err, context.DeadlineExceeded):
		respondWithJSON(w, http.StatusGatewayTimeout, errorResponse{Error: "mining timeout exceeded"})
	case errors.Is(err, context.Canceled):
		respondWithJSON(w, http.StatusRequestTimeout, errorResponse{Error: "mining cancelled"})
	default:
		respondWithJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
	}
}
