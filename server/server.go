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
	Error string `json:"error"`
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

func handleWriteBlock(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	var m models.Message

	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&m); err != nil {
		respondWithJSON(w, r, http.StatusBadRequest, errorResponse{Error: "corpo da requisição inválido"})
		fmt.Println(err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), miningTimeout())
	defer cancel()

	newBlock, err := generateBlock(ctx, blockchainInstance.LastBlock(), m.BPM)
	if err != nil {
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

	if !newBlock.IsValid(blockchainInstance.LastBlock()) {
		respondWithJSON(w, r, http.StatusUnprocessableEntity, errorResponse{Error: "bloco inválido não pôde ser adicionado"})
		return
	}

	blockchainInstance.Add(newBlock)
	blockchainInstance.Dump()

	respondWithJSON(w, r, http.StatusCreated, newBlock)
}
