package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	"github.com/petersonsalme/blockchain-with-go/blockchain"
	"github.com/petersonsalme/blockchain-with-go/models"
)

// DefaultMiningTimeout is the default duration allowed for block mining before timing out.
const DefaultMiningTimeout = 10 * time.Second

// MiningTimeoutEnvVar overrides DefaultMiningTimeout when set.
const MiningTimeoutEnvVar = "MINING_TIMEOUT"

var blockchainInstance *models.Blockchain
var generateBlock = blockchain.GenerateBlockWithContext

type errorResponse struct {
	Error   string         `json:"error"`
	Code    string         `json:"code,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

// requestTooLargeCode is the error code returned when the request body exceeds the allowed size.
const requestTooLargeCode = "request_too_large"

// maxRequestBodyBytes allows headroom over MaxBlockBytes() for JSON overhead of the request.
func maxRequestBodyBytes() int64 {
	return 2 * int64(models.MaxBlockBytes())
}

func respondWithValidationError(w http.ResponseWriter, r *http.Request, err *models.ValidationError) {
	respondWithJSON(w, r, http.StatusUnprocessableEntity, errorResponse{Error: err.Message, Code: err.Code, Details: err.Details})
}

func miningTimeout() time.Duration {
	raw := os.Getenv(MiningTimeoutEnvVar)
	if raw == "" {
		return DefaultMiningTimeout
	}

	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}

	if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}

	return DefaultMiningTimeout
}

func Run() error {
	blockchainInstance = blockchain.New()
	mux := makeMuxRouter()
	httpAddr := os.Getenv("PORT")
	log.Println("Listening on ", httpAddr)
	s := &http.Server{
		Addr:           ":" + httpAddr,
		Handler:        mux,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	if err := s.ListenAndServe(); err != nil {
		return err
	}

	return nil
}

func makeMuxRouter() http.Handler {
	muxRouter := mux.NewRouter()
	muxRouter.HandleFunc("/", handleGetBlockchain).Methods("GET")
	muxRouter.HandleFunc("/", handleWriteBlock).Methods("POST")
	muxRouter.HandleFunc("/legacy/bpm", handleLegacyBPM).Methods("POST")
	return muxRouter
}

func respondWithJSON(w http.ResponseWriter, r *http.Request, code int, payload interface{}) {
	response, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("HTTP 500: Internal Server Error"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(response)
}

func handleGetBlockchain(w http.ResponseWriter, r *http.Request) {
	bytes, err := json.MarshalIndent(blockchainInstance, "", "  ")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	io.WriteString(w, string(bytes))
}

func mineAndAddBlock(w http.ResponseWriter, r *http.Request, txs []models.Transaction) {
	ctx, cancel := context.WithTimeout(r.Context(), miningTimeout())
	defer cancel()

	newBlock, err := generateBlock(ctx, blockchainInstance.LastBlock(), txs)
	if err != nil {
		var validationErr *models.ValidationError
		if errors.As(err, &validationErr) {
			respondWithValidationError(w, r, validationErr)
			return
		}
		if errors.Is(err, context.DeadlineExceeded) {
			respondWithJSON(w, r, http.StatusGatewayTimeout, errorResponse{Error: "tempo limite de mineração excedido"})
			return
		}
		if errors.Is(err, context.Canceled) {
			respondWithJSON(w, r, http.StatusRequestTimeout, errorResponse{Error: "mineração cancelada"})
			return
		}
		respondWithJSON(w, r, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}

	if newBlock != nil {
		if err := models.ValidateBlockRules(newBlock, blockchainInstance.LastBlock(), time.Now()); err != nil {
			var validationErr *models.ValidationError
			if errors.As(err, &validationErr) {
				respondWithValidationError(w, r, validationErr)
				return
			}
			respondWithJSON(w, r, http.StatusInternalServerError, errorResponse{Error: err.Error()})
			return
		}
	}

	if newBlock == nil || !newBlock.IsValid(blockchainInstance.LastBlock()) {
		respondWithJSON(w, r, http.StatusUnprocessableEntity, errorResponse{Error: "bloco inválido não pôde ser adicionado"})
		return
	}

	blockchainInstance.Add(newBlock)
	blockchainInstance.Dump()

	respondWithJSON(w, r, http.StatusCreated, newBlock)
}

func handleWriteBlock(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	var m models.Message

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes())
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&m); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			respondWithJSON(w, r, http.StatusRequestEntityTooLarge, errorResponse{
				Error:   "corpo da requisição excede o tamanho máximo",
				Code:    requestTooLargeCode,
				Details: map[string]any{"max": tooLarge.Limit},
			})
			return
		}
		respondWithJSON(w, r, http.StatusBadRequest, errorResponse{Error: "corpo da requisição inválido"})
		return
	}

	if len(m.Transactions) == 0 {
		respondWithJSON(w, r, http.StatusBadRequest, errorResponse{Error: "lista de transações não pode ser vazia"})
		return
	}

	for i := range m.Transactions {
		if m.Transactions[i].Timestamp.IsZero() {
			m.Transactions[i].Timestamp = time.Now()
		}
		if m.Transactions[i].ID == "" {
			m.Transactions[i].ID = m.Transactions[i].CalculateHash()
		}
		if err := m.Transactions[i].Validate(); err != nil {
			respondWithJSON(w, r, http.StatusBadRequest, errorResponse{Error: fmt.Sprintf("transação inválida: %s", err.Error())})
			return
		}
	}

	mineAndAddBlock(w, r, m.Transactions)
}

func handleLegacyBPM(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	w.Header().Set("Deprecation", "true")

	var m models.LegacyMessage

	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&m); err != nil {
		respondWithJSON(w, r, http.StatusBadRequest, errorResponse{Error: "corpo da requisição inválido"})
		return
	}

	if m.BPM <= 0 {
		respondWithJSON(w, r, http.StatusBadRequest, errorResponse{Error: "BPM deve ser maior que zero"})
		return
	}

	tx := models.Transaction{
		From:      "legacy",
		To:        "legacy",
		Amount:    uint64(m.BPM),
		Timestamp: time.Now(),
	}
	tx.ID = tx.CalculateHash()

	mineAndAddBlock(w, r, []models.Transaction{tx})
}
