package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// DefaultMiningTimeout is the default duration allowed for block mining before timing out.
const DefaultMiningTimeout = 10 * time.Second

// MiningTimeoutEnvVar overrides DefaultMiningTimeout when set.
const MiningTimeoutEnvVar = "MINING_TIMEOUT"

// Chain is the application port the HTTP adapter drives.
type Chain interface {
	Blocks() ([]*domain.Block, error)
	Mine(ctx context.Context, txs []domain.Transaction) (*domain.Block, error)
}

// Config holds the Server settings.
type Config struct {
	// MiningTimeout bounds one mining request; non-positive means DefaultMiningTimeout.
	MiningTimeout time.Duration
}

// Server is the HTTP adapter over a Chain.
type Server struct {
	chain         Chain
	miningTimeout time.Duration
}

// NewServer builds a Server that serves svc.
func NewServer(svc Chain, cfg Config) *Server {
	timeout := cfg.MiningTimeout
	if timeout <= 0 {
		timeout = DefaultMiningTimeout
	}
	return &Server{chain: svc, miningTimeout: timeout}
}

// ParseMiningTimeout reads a MINING_TIMEOUT value: a Go duration or integer seconds.
// Empty, invalid and non-positive values yield DefaultMiningTimeout.
func ParseMiningTimeout(raw string) time.Duration {
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}

	if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}

	return DefaultMiningTimeout
}

// maxRequestBodyBytes allows headroom over MaxBlockBytes() for JSON overhead of the request.
func maxRequestBodyBytes() int64 {
	return 2 * int64(domain.MaxBlockBytes())
}

// Handler returns the routes of s.
func (s *Server) Handler() http.Handler {
	r := mux.NewRouter()
	r.HandleFunc("/", s.handleGetBlockchain).Methods("GET")
	r.HandleFunc("/", s.handleWriteBlock).Methods("POST")
	r.HandleFunc("/legacy/bpm", s.handleLegacyBPM).Methods("POST")
	return r
}

// HTTPServer wraps Handler in an http.Server listening on addr with the service timeouts.
func (s *Server) HTTPServer(addr string) *http.Server {
	return &http.Server{
		Addr:           addr,
		Handler:        s.Handler(),
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
}

func respondWithJSON(w http.ResponseWriter, code int, payload interface{}) {
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

func (s *Server) handleGetBlockchain(w http.ResponseWriter, r *http.Request) {
	blocks, err := s.chain.Blocks()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	bytes, err := json.MarshalIndent(blocksResponse{Blocks: blocks}, "", "  ")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	io.WriteString(w, string(bytes))
}

func (s *Server) mineAndRespond(w http.ResponseWriter, r *http.Request, txs []domain.Transaction) {
	ctx, cancel := context.WithTimeout(r.Context(), s.miningTimeout)
	defer cancel()

	newBlock, err := s.chain.Mine(ctx, txs)
	if err != nil {
		respondWithMineError(w, err)
		return
	}

	respondWithJSON(w, http.StatusCreated, newBlock)
}

func (s *Server) handleWriteBlock(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	var m Message

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes())
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&m); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			respondWithJSON(w, http.StatusRequestEntityTooLarge, errorResponse{
				Error:   "request body exceeds the maximum size",
				Code:    requestTooLargeCode,
				Details: map[string]any{"max": tooLarge.Limit},
			})
			return
		}
		respondWithJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	if len(m.Transactions) == 0 {
		respondWithJSON(w, http.StatusBadRequest, errorResponse{Error: "transaction list cannot be empty"})
		return
	}

	for i := range m.Transactions {
		if m.Transactions[i].ID == "" {
			m.Transactions[i].ID = m.Transactions[i].CalculateHash()
		}
		if err := m.Transactions[i].Validate(); err != nil {
			respondWithJSON(w, http.StatusBadRequest, errorResponse{Error: fmt.Sprintf("invalid transaction: %s", err.Error())})
			return
		}
		// Timestamp is signed, so the node must not fill it in.
		if m.Transactions[i].Timestamp.IsZero() {
			respondWithJSON(w, http.StatusBadRequest, errorResponse{Error: "transaction timestamp is required", Code: domain.CodeMissingTimestamp})
			return
		}
	}

	s.mineAndRespond(w, r, m.Transactions)
}

// handleLegacyBPM answers 410 Gone: BPM payloads cannot be signed, so the node can no longer accept them.
func (s *Server) handleLegacyBPM(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	w.Header().Set("Deprecation", "true")
	respondWithJSON(w, http.StatusGone, errorResponse{Error: "endpoint removed; send signed transactions to POST /", Code: endpointGoneCode})
}
