package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// requestTooLargeCode is the error code returned when the request body exceeds the allowed size.
const requestTooLargeCode = "request_too_large"

// respondWithMineError maps an error from chain mining to its HTTP response.
func respondWithMineError(w http.ResponseWriter, err error) {
	var validationErr *domain.ValidationError
	switch {
	case errors.As(err, &validationErr):
		respondWithJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: validationErr.Message, Code: validationErr.Code, Details: validationErr.Details})
	case errors.Is(err, chain.ErrInvalidBlock):
		respondWithJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "bloco inválido não pôde ser adicionado"})
	case errors.Is(err, context.DeadlineExceeded):
		respondWithJSON(w, http.StatusGatewayTimeout, errorResponse{Error: "tempo limite de mineração excedido"})
	case errors.Is(err, context.Canceled):
		respondWithJSON(w, http.StatusRequestTimeout, errorResponse{Error: "mineração cancelada"})
	default:
		respondWithJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
	}
}
