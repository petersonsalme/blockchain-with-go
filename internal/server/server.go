package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/p2p"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

// DefaultMiningTimeout is the default duration allowed for block mining before timing out.
const DefaultMiningTimeout = 10 * time.Second

// MiningTimeoutEnvVar overrides DefaultMiningTimeout when set.
const MiningTimeoutEnvVar = "MINING_TIMEOUT"

const (
	defaultPageLimit = 20
	maxPageLimit     = 100
	// defaultAddressTxLimit is the page size of GET /address/{addr}/txs without limit.
	defaultAddressTxLimit = 25
	maxHeaderLimit        = 1000
	// hashLen is the length of a hex-encoded SHA-256 block hash.
	hashLen = 64
)

// Chain is the application port the HTTP adapter drives.
type Chain interface {
	Blocks() ([]*domain.Block, error)
	Tip() (*domain.Block, error)
	BlockByHeight(h int) (*domain.Block, error)
	BlockByHash(hash string) (*domain.Block, error)
	BlocksRange(from, limit int) ([]*domain.Block, error)
	Transaction(id string) (*domain.Transaction, string, int, error)
	TxProof(id string) (*chain.InclusionProof, error)
	Balance(addr string) (uint64, []chain.UTXO)
	AddressHistory(addr, after string, limit int) (chain.AddressPage, error)
	Mine(ctx context.Context, txs []domain.Transaction) (*domain.Block, error)
	Submit(tx domain.Transaction) error
	Pending() []domain.Transaction
	MinePending(ctx context.Context) (*domain.Block, error)
	Contract(id string) (code []byte, storage map[uint64]uint64, ok bool)
}

// Peers is the peer-to-peer port behind the /p2p routes (ADR-0013 handshake, ADR-0018 gossip); *p2p.Node satisfies it.
type Peers interface {
	Accept(hello p2p.Hello) (p2p.Hello, error)
	Peers() []p2p.PeerInfo
	Addrs() []string
	Inv(from string, items []p2p.InvItem) ([]p2p.InvItem, error)
	ReceiveBlock(ctx context.Context, from string, b *domain.Block) (bool, error)
	ReceiveTx(from string, tx domain.Transaction) (bool, error)
	AnnounceBlock(b *domain.Block)
	AnnounceTx(tx domain.Transaction)
}

// Config holds the Server settings.
type Config struct {
	// MiningTimeout bounds one mining request; non-positive means DefaultMiningTimeout.
	MiningTimeout time.Duration
	// RateLimit is requests per minute per IP; non-positive means disabled.
	RateLimit int
	// Logger receives one record per request; nil discards them.
	Logger *slog.Logger
	// Now returns the current time; nil means time.Now.
	Now func() time.Time
	// P2P serves the /p2p routes; nil makes them answer 404 p2p_disabled.
	P2P Peers
	// Explorer holds the files served read-only at /explorer/; nil leaves the route unregistered (404).
	Explorer fs.FS
	// Events is the stream GET /events serves and gossip publishes to; nil makes the server its own.
	Events *Events
}

// Server is the HTTP adapter over a Chain.
type Server struct {
	chain         Chain
	miningTimeout time.Duration
	limiter       *rateLimiter
	logger        *slog.Logger
	now           func() time.Time
	metrics       *metrics
	events        *hub
	p2p           Peers
	explorer      fs.FS
}

// ErrNilService is returned by NewServer when svc is nil.
var ErrNilService = errors.New("server: nil service")

// NewServer builds a Server that serves svc.
func NewServer(svc Chain, cfg Config) (*Server, error) {
	if svc == nil {
		return nil, ErrNilService
	}

	timeout := cfg.MiningTimeout
	if timeout <= 0 {
		timeout = DefaultMiningTimeout
	}

	var limiter *rateLimiter
	if cfg.RateLimit > 0 {
		limiter = newRateLimiter(cfg.RateLimit, cfg.Now)
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	events := cfg.Events
	if events == nil {
		events = NewEvents()
	}

	return &Server{chain: svc, miningTimeout: timeout, limiter: limiter, logger: logger, now: now, metrics: newMetrics(), events: events.hub, p2p: cfg.P2P, explorer: cfg.Explorer}, nil
}

// ParseMiningTimeout reads a MINING_TIMEOUT value: a Go duration or integer seconds.
// Empty, invalid and non-positive values yield DefaultMiningTimeout.
func ParseMiningTimeout(raw string) time.Duration {
	d, err := time.ParseDuration(raw)
	if err == nil && d > 0 {
		return d
	}

	secs, err := strconv.Atoi(raw)
	if err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}

	return DefaultMiningTimeout
}

// maxRequestBodyBytes allows headroom over MaxBlockBytes() for JSON overhead of the request.
func maxRequestBodyBytes() int64 {
	return 2 * int64(domain.MaxBlockBytes())
}

// route is one method pattern that Handler registers.
type route struct {
	pattern string
	handler http.HandlerFunc
}

// Handler returns the routes of s.
//
// Each path also registers a method-less fallback: it is less specific than the method
// patterns, so it only catches the other methods and answers 405 with an Allow header.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, rt := range s.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}

	mux.Handle("/{$}", methodNotAllowed("GET, HEAD, POST"))
	mux.Handle("/tx", methodNotAllowed("POST"))
	mux.Handle("/mine", methodNotAllowed("POST"))
	mux.Handle("/mempool", methodNotAllowed("GET, HEAD"))
	mux.Handle("/blocks", methodNotAllowed("GET, HEAD"))
	mux.Handle("/headers", methodNotAllowed("GET, HEAD"))
	mux.Handle("/block/{ref}", methodNotAllowed("GET, HEAD"))
	mux.Handle("/tx/{id}", methodNotAllowed("GET, HEAD"))
	mux.Handle("/tx/{id}/proof", methodNotAllowed("GET, HEAD"))
	mux.Handle("/balance/{addr}", methodNotAllowed("GET, HEAD"))
	mux.Handle("/address/{addr}/txs", methodNotAllowed("GET, HEAD"))
	mux.Handle("/contracts/{id}", methodNotAllowed("GET, HEAD"))
	mux.Handle("/health", methodNotAllowed("GET, HEAD"))
	mux.Handle("/metrics", methodNotAllowed("GET, HEAD"))
	mux.Handle("/events", methodNotAllowed("GET, HEAD"))
	mux.Handle("/p2p/hello", methodNotAllowed("POST"))
	mux.Handle("/p2p/peers", methodNotAllowed("GET, HEAD"))
	mux.Handle("/p2p/inv", methodNotAllowed("POST"))
	mux.Handle("/p2p/block", methodNotAllowed("POST"))
	mux.Handle("/p2p/tx", methodNotAllowed("POST"))
	mux.Handle("/legacy/bpm", methodNotAllowed("POST"))

	if s.explorer != nil {
		mux.HandleFunc("GET "+explorerPrefix, handleExplorer(s.explorer))
	}

	var h http.Handler = mux
	if s.limiter != nil {
		h = s.limiter.middleware(h)
	}

	return logRequests(s.logger, s.metrics.middleware(h))
}

// methodNotAllowed answers 405 with an empty body and the Allow header set to allow.
func methodNotAllowed(allow string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", allow)
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
}

// HTTPServer wraps Handler in an http.Server listening on addr with the service timeouts.
// Every request context derives from ctx, so cancelling ctx cancels the mining in progress.
func (s *Server) HTTPServer(ctx context.Context, addr string) *http.Server {
	return &http.Server{
		Addr:           addr,
		Handler:        s.Handler(),
		BaseContext:    func(net.Listener) context.Context { return ctx },
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
}

// routes lists the method patterns of Handler; contract_test.go checks them against openapi.yaml.
func (s *Server) routes() []route {
	return []route{
		{"GET /{$}", s.handleGetBlockchain},
		{"POST /{$}", s.handleWriteBlock},
		{"POST /tx", s.handleSubmitTx},
		{"POST /mine", s.handleMine},
		{"GET /mempool", s.handleGetMempool},
		{"GET /blocks", s.handleGetBlocks},
		{"GET /headers", s.handleGetHeaders},
		{"GET /block/{ref}", s.handleGetBlock},
		{"GET /tx/{id}", s.handleGetTx},
		{"GET /tx/{id}/proof", s.handleGetTxProof},
		{"GET /balance/{addr}", s.handleGetBalance},
		{"GET /address/{addr}/txs", s.handleGetAddressTxs},
		{"GET /contracts/{id}", s.handleGetContract},
		{"GET /health", s.handleHealth},
		{"GET /metrics", s.handleMetrics},
		{"GET /events", s.handleEvents},
		{"POST /p2p/hello", s.handleP2PHello},
		{"GET /p2p/peers", s.handleP2PPeers},
		{"POST /p2p/inv", s.handleP2PInv},
		{"POST /p2p/block", s.handleP2PBlock},
		{"POST /p2p/tx", s.handleP2PTx},
		{"POST /legacy/bpm", s.handleLegacyBPM},
	}
}

func respondWithJSON(w http.ResponseWriter, code int, payload any) {
	response, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("HTTP 500: Internal Server Error")) // headers are sent; a failed write to the client cannot be reported

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(response) // headers are sent; a failed write to the client cannot be reported
}

func (s *Server) handleGetBlockchain(w http.ResponseWriter, r *http.Request) {
	blocks, err := s.chain.Blocks()
	if err != nil {
		respondWithJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}

	respondWithJSON(w, http.StatusOK, blocksResponse{Blocks: blocks})
}

// queryInt reads a non-negative integer query parameter, or def when absent.
func queryInt(r *http.Request, name string, def int) (int, error) {
	q := r.URL.Query()
	if !q.Has(name) {
		return def, nil
	}

	v, err := strconv.Atoi(q.Get(name))
	if err != nil || v < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", name)
	}

	return v, nil
}

// handleGetBlocks serves one page of the chain; reads never wait for Mine.
func (s *Server) handleGetBlocks(w http.ResponseWriter, r *http.Request) {
	from, err := queryInt(r, "from", 0)
	if err != nil {
		respondWithJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error(), Code: invalidQueryCode})
		return
	}

	limit, err := queryInt(r, "limit", defaultPageLimit)
	if err == nil && limit == 0 {
		err = errors.New("limit must be positive")
	}

	if err != nil {
		respondWithJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error(), Code: invalidQueryCode})
		return
	}

	limit = min(limit, maxPageLimit)

	// One extra block tells whether a next page exists, atomically with the page.
	blocks, err := s.chain.BlocksRange(from, limit+1)
	if err != nil {
		respondWithJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}

	resp := pageResponse{Blocks: blocks, From: from, Limit: limit}
	if len(blocks) > limit {
		resp.Blocks = blocks[:limit]
		next := from + limit
		resp.Next = &next
	}

	respondWithJSON(w, http.StatusOK, resp)
}

// handleGetHeaders serves one page of block headers without transactions.
func (s *Server) handleGetHeaders(w http.ResponseWriter, r *http.Request) {
	from, err := queryInt(r, "from", 0)
	if err != nil {
		respondWithJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error(), Code: invalidQueryCode})
		return
	}

	limit, err := queryInt(r, "limit", maxHeaderLimit)
	if err == nil && limit == 0 {
		err = errors.New("limit must be positive")
	}

	if err != nil {
		respondWithJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error(), Code: invalidQueryCode})
		return
	}

	limit = min(limit, maxHeaderLimit)

	// One extra block tells whether a next page exists, atomically with the page.
	blocks, err := s.chain.BlocksRange(from, limit+1)
	if err != nil {
		respondWithJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}

	hasMore := len(blocks) > limit
	if hasMore {
		blocks = blocks[:limit]
	}

	headers := make([]*domain.Block, len(blocks))
	for i, b := range blocks {
		h := *b
		h.Transactions = nil
		headers[i] = &h
	}

	resp := pageResponse{Blocks: headers, From: from, Limit: limit}
	if hasMore {
		next := from + limit
		resp.Next = &next
	}

	respondWithJSON(w, http.StatusOK, resp)
}

// handleGetBlock serves one block by hash (64 lowercase hex) or height (decimal).
func (s *Server) handleGetBlock(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("ref")

	var (
		block *domain.Block
		err   error
	)

	switch {
	case isHeight(ref):
		height, convErr := strconv.Atoi(ref)
		if convErr != nil { // beyond int: no such height
			err = storage.ErrNotFound
			break
		}

		block, err = s.chain.BlockByHeight(height)
	case isHash(ref):
		block, err = s.chain.BlockByHash(ref)
	default:
		respondWithJSON(w, http.StatusBadRequest, errorResponse{Error: "block reference must be a 64-character lowercase hex hash or a decimal height", Code: invalidBlockRefCode})
		return
	}

	if errors.Is(err, storage.ErrPruned) {
		respondWithJSON(w, http.StatusGone, errorResponse{Error: "block body was pruned; only its header is kept", Code: blockPrunedCode})
		return
	}

	if errors.Is(err, storage.ErrNotFound) {
		respondWithJSON(w, http.StatusNotFound, errorResponse{Error: "block not found", Code: blockNotFoundCode})
		return
	}

	if err != nil {
		respondWithJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}

	respondWithJSON(w, http.StatusOK, block)
}

// handleHealth reports the chain tip, or 503 when it cannot be read.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	tip, err := s.chain.Tip()
	if err != nil {
		respondWithJSON(w, http.StatusServiceUnavailable, errorResponse{Error: err.Error(), Code: unhealthyCode})
		return
	}

	respondWithJSON(w, http.StatusOK, healthResponse{Status: "ok", Height: tip.Index, Tip: tip.Hash})
}

func isHash(ref string) bool {
	if len(ref) != hashLen {
		return false
	}

	for _, c := range ref {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}

	return true
}

// isHeight reports whether ref is decimal digits and too short to be a hash.
func isHeight(ref string) bool {
	if ref == "" || len(ref) >= hashLen {
		return false
	}

	for _, c := range ref {
		if c < '0' || c > '9' {
			return false
		}
	}

	return true
}

// handleGetTx serves one transaction by its 64-character lowercase hex ID.
func (s *Server) handleGetTx(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isHash(id) {
		respondWithJSON(w, http.StatusBadRequest, errorResponse{
			Error: "transaction id must be a 64-character lowercase hex string",
			Code:  invalidTxIDCode,
		})

		return
	}

	tx, blockHash, height, err := s.chain.Transaction(id)
	if errors.Is(err, storage.ErrNotFound) {
		respondWithJSON(w, http.StatusNotFound, errorResponse{
			Error: "transaction not found",
			Code:  txNotFoundCode,
		})

		return
	}

	if err != nil {
		respondWithJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}

	respondWithJSON(w, http.StatusOK, txResponse{
		Transaction: tx,
		BlockHash:   blockHash,
		Height:      height,
	})
}

// handleGetContract serves the code and storage of a deployed contract by its ID.
func (s *Server) handleGetContract(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isHash(id) {
		respondWithJSON(w, http.StatusBadRequest, errorResponse{
			Error: "contract id must be a 64-character lowercase hex string",
			Code:  invalidContractIDCode,
		})

		return
	}

	code, storage, ok := s.chain.Contract(id)
	if !ok {
		respondWithJSON(w, http.StatusNotFound, errorResponse{Error: "contract not found", Code: contractNotFoundCode})
		return
	}

	respondWithJSON(w, http.StatusOK, newContractResponse(id, code, storage))
}

// handleGetTxProof serves the Merkle inclusion proof of a transaction by its ID.
func (s *Server) handleGetTxProof(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isHash(id) {
		respondWithJSON(w, http.StatusBadRequest, errorResponse{
			Error: "transaction id must be a 64-character lowercase hex string",
			Code:  invalidTxIDCode,
		})

		return
	}

	p, err := s.chain.TxProof(id)
	if errors.Is(err, chain.ErrTxNotFound) {
		respondWithJSON(w, http.StatusNotFound, errorResponse{
			Error: "transaction not found",
			Code:  txNotFoundCode,
		})

		return
	}

	if err != nil {
		respondWithJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}

	steps := make([]proofStep, len(p.Steps))
	for i, st := range p.Steps {
		side := "right"
		if st.Left {
			side = "left"
		}

		steps[i] = proofStep{Hash: st.Hash, Side: side}
	}

	respondWithJSON(w, http.StatusOK, proofResponse{
		TxID:       id,
		BlockIndex: p.BlockIndex,
		BlockHash:  p.BlockHash,
		MerkleRoot: p.MerkleRoot,
		LeafIndex:  p.LeafIndex,
		LeafHash:   p.LeafHash,
		Proof:      steps,
	})
}

// handleGetBalance serves the balance and unspent outputs for addr.
func (s *Server) handleGetBalance(w http.ResponseWriter, r *http.Request) {
	addr := r.PathValue("addr")

	err := wallet.ValidateAddress(addr)
	if err != nil {
		respondWithJSON(w, http.StatusBadRequest, errorResponse{
			Error: err.Error(),
			Code:  invalidAddressCode,
		})

		return
	}

	balance, utxos := s.chain.Balance(addr)
	if utxos == nil {
		utxos = []chain.UTXO{}
	}

	respondWithJSON(w, http.StatusOK, balanceResponse{
		Address: addr,
		Balance: balance,
		UTXOs:   utxos,
	})
}

// handleGetAddressTxs serves one page of the confirmed history of addr, newest first.
func (s *Server) handleGetAddressTxs(w http.ResponseWriter, r *http.Request) {
	addr := r.PathValue("addr")

	err := wallet.ValidateAddress(addr)
	if err != nil {
		respondWithJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error(), Code: invalidAddressCode})
		return
	}

	limit, err := queryInt(r, "limit", defaultAddressTxLimit)
	if err == nil && limit == 0 {
		err = errors.New("limit must be positive")
	}

	if err != nil {
		respondWithJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error(), Code: invalidQueryCode})
		return
	}

	limit = min(limit, maxPageLimit)

	after := r.URL.Query().Get("after")
	if r.URL.Query().Has("after") && !isHash(after) {
		respondWithJSON(w, http.StatusBadRequest, errorResponse{
			Error: "after must be a 64-character lowercase hex transaction id",
			Code:  invalidQueryCode,
		})

		return
	}

	page, err := s.chain.AddressHistory(addr, after, limit)
	if errors.Is(err, chain.ErrUnknownCursor) {
		respondWithJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error(), Code: unknownCursorCode})
		return
	}

	if err != nil {
		respondWithJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}

	resp := addressTxsResponse{
		Address:     addr,
		Txs:         make([]addressTx, 0, len(page.Txs)),
		Limit:       limit,
		PrunedBelow: page.PrunedBelow,
	}

	for _, t := range page.Txs {
		resp.Txs = append(resp.Txs, addressTx{
			Transaction: t.Tx,
			BlockHash:   t.BlockHash,
			Height:      t.Height,
			Timestamp:   t.Timestamp,
		})
	}

	if page.More {
		next := page.Txs[len(page.Txs)-1].Tx.CalculateHash()
		resp.Next = &next
	}

	respondWithJSON(w, http.StatusOK, resp)
}

func (s *Server) mineAndRespond(w http.ResponseWriter, r *http.Request, mine func(context.Context) (*domain.Block, error)) {
	ctx, cancel := context.WithTimeout(r.Context(), s.miningTimeout)
	defer cancel()

	start := s.now()

	newBlock, err := mine(ctx)
	if err != nil {
		respondWithMineError(w, err)
		return
	}

	s.metrics.observeMining(s.now().Sub(start))
	s.events.publish(eventBlock, newBlock)

	if s.p2p != nil {
		s.p2p.AnnounceBlock(newBlock)
	}

	respondWithJSON(w, http.StatusCreated, newBlock)
}

func (s *Server) handleWriteBlock(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }() // closing a server request body cannot fail usefully

	var m Message
	if !decodeJSONBody(w, r, &m) {
		return
	}

	if len(m.Transactions) == 0 {
		respondWithJSON(w, http.StatusBadRequest, errorResponse{Error: "transaction list cannot be empty"})
		return
	}

	for i := range m.Transactions {
		if !checkTx(w, &m.Transactions[i]) {
			return
		}
	}

	s.mineAndRespond(w, r, func(ctx context.Context) (*domain.Block, error) { return s.chain.Mine(ctx, m.Transactions) })
}

// handleSubmitTx admits one signed transaction into the pool; it never mines.
func (s *Server) handleSubmitTx(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }() // closing a server request body cannot fail usefully

	var req txRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	tx := req.transaction()
	if !checkTx(w, &tx) {
		return
	}

	err := s.chain.Submit(tx)
	if err != nil {
		respondWithSubmitError(w, err)
		return
	}

	s.events.publish(eventTx, tx)

	if s.p2p != nil {
		s.p2p.AnnounceTx(tx)
	}

	respondWithJSON(w, http.StatusAccepted, submitResponse{ID: tx.ID})
}

// decodeJSONBody decodes the request body into v, bounded by maxRequestBodyBytes.
// On failure it answers 413 or 400 and reports false.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes())

	err := json.NewDecoder(r.Body).Decode(v)
	if err != nil {
		if respondIfTooLarge(w, err) {
			return false
		}

		respondWithJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})

		return false
	}

	return true
}

// respondIfTooLarge answers 413 and reports true when err is the body size limit being hit.
func respondIfTooLarge(w http.ResponseWriter, err error) bool {
	var tooLarge *http.MaxBytesError
	if !errors.As(err, &tooLarge) {
		return false
	}

	respondWithJSON(w, http.StatusRequestEntityTooLarge, errorResponse{
		Error:   "request body exceeds the maximum size",
		Code:    requestTooLargeCode,
		Details: map[string]any{"max": tooLarge.Limit},
	})

	return true
}

// checkTx fills in an empty ID and rejects a wrong ID or a missing timestamp with 400.
func checkTx(w http.ResponseWriter, tx *domain.Transaction) bool {
	if tx.ID == "" {
		tx.ID = tx.CalculateHash()
	}

	err := tx.Validate()
	if err != nil {
		respondWithJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid transaction: " + err.Error()})
		return false
	}
	// Timestamp is signed, so the node must not fill it in.
	if tx.Timestamp.IsZero() {
		respondWithJSON(w, http.StatusBadRequest, errorResponse{Error: "transaction timestamp is required", Code: domain.CodeMissingTimestamp})
		return false
	}

	return true
}

// handleMine mines a block from the pool; the request body is ignored.
func (s *Server) handleMine(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }() // closing a server request body cannot fail usefully

	s.mineAndRespond(w, r, s.chain.MinePending)
}

// handleGetMempool lists the pool in selection order; it never waits for mining.
func (s *Server) handleGetMempool(w http.ResponseWriter, r *http.Request) {
	txs := s.chain.Pending()
	if txs == nil {
		txs = []domain.Transaction{}
	}

	respondWithJSON(w, http.StatusOK, mempoolResponse{Transactions: txs})
}

// handleLegacyBPM answers 410 Gone: BPM payloads cannot be signed, so the node can no longer accept them.
func (s *Server) handleLegacyBPM(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }() // closing a server request body cannot fail usefully

	w.Header().Set("Deprecation", "true")
	respondWithJSON(w, http.StatusGone, errorResponse{Error: "endpoint removed; send signed transactions to POST /", Code: endpointGoneCode})
}
