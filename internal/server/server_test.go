package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

// rejectingConsensus seals with inner but never accepts a block.
type rejectingConsensus struct{ inner consensus.Consensus }

func (r rejectingConsensus) Seal(ctx context.Context, history []*domain.Block, b *domain.Block) error {
	return r.inner.Seal(ctx, nil, b)
}

func (rejectingConsensus) Verify(history []*domain.Block, b *domain.Block) error {
	return errors.New("rejected by fake consensus")
}

func newWallet() *wallet.Wallet {
	w, err := wallet.New()
	Expect(err).NotTo(HaveOccurred())
	return w
}

// bank is a funded wallet: the genesis allocates bankOutputs outputs of bankAmount to owner.
type bank struct {
	owner *wallet.Wallet
	mu    sync.Mutex
	txid  string
	next  uint32
}

const (
	bankOutputs = 200
	bankAmount  = 100
)

func newBank() *bank { return &bank{owner: newWallet()} }

// service builds a chain.Service over repo whose genesis, when repo is empty, funds the bank.
func (b *bank) service(repo *memory.Repository, engine consensus.Consensus, opts ...chain.Option) (*chain.Service, error) {
	allocs := make([]domain.TxOut, bankOutputs)
	for i := range allocs {
		allocs[i] = domain.TxOut{Amount: bankAmount, Address: b.owner.Address()}
	}
	svc, err := chain.NewService(repo, engine, append([]chain.Option{chain.WithGenesisAllocations(allocs)}, opts...)...)
	if err != nil {
		return nil, err
	}
	blocks, err := repo.All()
	if err != nil {
		return nil, err
	}
	b.txid = blocks[0].Transactions[0].CalculateHash()
	return svc, nil
}

// spend returns a transaction signed by the owner that pays amount to to, spending output idx.
func (b *bank) spend(idx uint32, to string, amount uint64) domain.Transaction {
	tx := domain.Transaction{
		Inputs:    []domain.TxIn{{PrevTxID: b.txid, OutIndex: idx}},
		Outputs:   []domain.TxOut{{Amount: amount, Address: to}},
		Timestamp: time.Now(),
	}
	tx.ID = tx.CalculateHash()
	Expect(domain.SignInput(&tx, 0, b.owner)).To(Succeed())
	return tx
}

// tx spends the next unused output.
func (b *bank) tx(to string, amount uint64) domain.Transaction {
	b.mu.Lock()
	idx := b.next
	b.next++
	b.mu.Unlock()
	return b.spend(idx, to, amount)
}

// payload is a valid request that spends a fresh output, so it never replays.
func (b *bank) payload() string {
	return payloadOf(b.tx(newWallet().Address(), 42))
}

func payloadOf(txs ...domain.Transaction) string {
	b, err := json.Marshal(Message{Transactions: txs})
	Expect(err).NotTo(HaveOccurred())
	return string(b)
}

type chainBody struct {
	Blocks []*domain.Block
}

// brokenChain fails to read blocks.
type brokenChain struct{}

func (brokenChain) Tip() (*domain.Block, error)               { return nil, errors.New("read failed") }
func (brokenChain) BlockByHeight(int) (*domain.Block, error)  { return nil, errors.New("read failed") }
func (brokenChain) BlockByHash(string) (*domain.Block, error) { return nil, errors.New("read failed") }
func (brokenChain) BlocksRange(int, int) ([]*domain.Block, error) {
	return nil, errors.New("read failed")
}

func (brokenChain) Blocks() ([]*domain.Block, error) { return nil, errors.New("read failed") }
func (brokenChain) Transaction(string) (*domain.Transaction, string, int, error) {
	return nil, "", 0, errors.New("read failed")
}
func (brokenChain) TxProof(string) (*chain.InclusionProof, error) {
	return nil, errors.New("read failed")
}
func (brokenChain) Balance(string) (uint64, []chain.UTXO) {
	return 0, []chain.UTXO{}
}
func (brokenChain) Mine(context.Context, []domain.Transaction) (*domain.Block, error) {
	return nil, errors.New("unused")
}

// spyMineChain counts Mine calls and returns a valid mock block.
type spyMineChain struct {
	brokenChain
	mineCalls int64
}

func (s *spyMineChain) Mine(ctx context.Context, txs []domain.Transaction) (*domain.Block, error) {
	atomic.AddInt64(&s.mineCalls, 1)
	return &domain.Block{Index: 1, Hash: "0000000000000000000000000000000000000000000000000000000000000001"}, nil
}

var _ = Describe("Server", func() {
	var (
		router http.Handler
		funds  *bank
		repo   *memory.Repository
		engine consensus.Consensus
		cfg    Config
	)

	// build wires repo, engine and cfg into a fresh Server; call it again after changing them.
	build := func(opts ...chain.Option) {
		svc, err := funds.service(repo, engine, opts...)
		Expect(err).NotTo(HaveOccurred())
		srv, err := NewServer(svc, cfg)
		Expect(err).NotTo(HaveOccurred())
		router = srv.Handler()
	}
	chainLen := func() int {
		all, err := repo.All()
		Expect(err).NotTo(HaveOccurred())
		return len(all)
	}
	lastBlock := func() *domain.Block {
		b, err := repo.Last()
		Expect(err).NotTo(HaveOccurred())
		return b
	}
	withGenerator := func(g chain.Generator) { build(chain.WithGenerator(g)) }

	BeforeEach(func() {
		funds = newBank()
		repo = memory.New()
		engine = pow.NewProofOfWork(1)
		cfg = Config{}
		build()
	})

	Describe("POST / (handleWriteBlock)", func() {
		Context("when receiving a valid block payload", func() {
			It("returns 201 Created, the new block with populated transaction IDs, and appends to the chain", func() {
				initialLen := chainLen()

				recipient := newWallet()
				sent := funds.tx(recipient.Address(), 42)
				payload := []byte(payloadOf(sent))
				req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()

				router.ServeHTTP(rec, req)

				Expect(rec.Code).To(Equal(http.StatusCreated))
				Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))

				var createdBlock domain.Block
				Expect(json.Unmarshal(rec.Body.Bytes(), &createdBlock)).To(Succeed())
				Expect(createdBlock.Transactions).To(HaveLen(1))
				Expect(createdBlock.Transactions[0].Inputs).To(Equal(sent.Inputs))
				Expect(createdBlock.Transactions[0].Outputs).To(Equal([]domain.TxOut{{Amount: 42, Address: recipient.Address()}}))
				Expect(createdBlock.Transactions[0].ID).NotTo(BeEmpty())
				Expect(createdBlock.Transactions[0].ID).To(Equal(createdBlock.Transactions[0].CalculateHash()))
				Expect(createdBlock.Transactions[0].Timestamp.Equal(sent.Timestamp)).To(BeTrue())

				Expect(chainLen()).To(Equal(initialLen + 1))
				Expect(lastBlock().Transactions).To(HaveLen(1))
				Expect(lastBlock().Transactions[0].ID).To(Equal(createdBlock.Transactions[0].ID))
				Expect(lastBlock().Hash).To(Equal(createdBlock.Hash))
			})
		})

		Context("when the generator produces an invalid block", func() {
			It("returns 422 Unprocessable Entity, error JSON, and leaves the chain unchanged", func() {
				withGenerator(func(ctx context.Context, c consensus.Consensus, history []*domain.Block, txs []domain.Transaction) (*domain.Block, error) {
					b, err := chain.GenerateBlockWithContext(ctx, c, history, txs)
					if err != nil {
						return nil, err
					}
					b.PrevHash = "tampered-invalid-hash"
					return b, nil
				})

				initialLen := chainLen()

				payload := []byte(funds.payload())
				req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()

				router.ServeHTTP(rec, req)

				Expect(rec.Code).To(Equal(http.StatusUnprocessableEntity))
				Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))

				Expect(rec.Body.String()).To(MatchJSON(`{"error": "invalid block could not be added"}`))

				var resp errorResponse
				Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp.Error).To(Equal("invalid block could not be added"))

				Expect(chainLen()).To(Equal(initialLen))
			})
		})

		Context("when mining times out (deadline exceeded)", func() {
			It("returns 504 Gateway Timeout, error JSON, and leaves the chain unchanged", func() {
				withGenerator(func(ctx context.Context, c consensus.Consensus, history []*domain.Block, txs []domain.Transaction) (*domain.Block, error) {
					return nil, context.DeadlineExceeded
				})

				initialLen := chainLen()

				payload := []byte(funds.payload())
				req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()

				router.ServeHTTP(rec, req)

				Expect(rec.Code).To(Equal(http.StatusGatewayTimeout))
				Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))

				Expect(rec.Body.String()).To(MatchJSON(`{"error": "mining timeout exceeded"}`))

				var resp errorResponse
				Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp.Error).To(Equal("mining timeout exceeded"))

				Expect(chainLen()).To(Equal(initialLen))
			})
		})

		Context("when mining context is canceled", func() {
			It("returns 408 Request Timeout, error JSON, and leaves the chain unchanged", func() {
				withGenerator(func(ctx context.Context, c consensus.Consensus, history []*domain.Block, txs []domain.Transaction) (*domain.Block, error) {
					return nil, context.Canceled
				})

				initialLen := chainLen()

				payload := []byte(funds.payload())
				req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()

				router.ServeHTTP(rec, req)

				Expect(rec.Code).To(Equal(http.StatusRequestTimeout))
				Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))

				Expect(rec.Body.String()).To(MatchJSON(`{"error": "mining cancelled"}`))

				var resp errorResponse
				Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp.Error).To(Equal("mining cancelled"))

				Expect(chainLen()).To(Equal(initialLen))
			})
		})

		Context("when client cancels the request context directly", func() {
			It("returns 408 Request Timeout when request context is canceled", func() {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()

				initialLen := chainLen()

				payload := []byte(funds.payload())
				req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload)).WithContext(ctx)
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()

				router.ServeHTTP(rec, req)

				Expect(rec.Code).To(Equal(http.StatusRequestTimeout))
				Expect(chainLen()).To(Equal(initialLen))
			})
		})

		Context("when Config.MiningTimeout is configured", func() {
			It("aborts mining when timeout expires with real mining", func() {
				cfg = Config{MiningTimeout: 15 * time.Millisecond}
				engine = pow.NewProofOfWork(10)
				build()

				initialLen := chainLen()

				payload := []byte(funds.payload())
				req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()

				router.ServeHTTP(rec, req)

				Expect(rec.Code).To(Equal(http.StatusGatewayTimeout))
				Expect(chainLen()).To(Equal(initialLen))
			})
		})

		Context("when receiving malformed JSON", func() {
			It("returns 400 Bad Request with an error message and json content-type", func() {
				initialLen := chainLen()

				req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{invalid-json`))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()

				router.ServeHTTP(rec, req)

				Expect(rec.Code).To(Equal(http.StatusBadRequest))
				Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))

				Expect(rec.Body.String()).To(MatchJSON(`{"error": "invalid request body"}`))

				var resp errorResponse
				Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp.Error).To(Equal("invalid request body"))

				Expect(chainLen()).To(Equal(initialLen))
			})
		})
	})

	Describe("POST / concurrency", func() {

		It("serializes concurrent POSTs into a valid chain and answers 201 to all", func() {
			const n = 12
			codes := make(chan int, n)
			var wg sync.WaitGroup
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func() {
					defer GinkgoRecover()
					defer wg.Done()
					req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(funds.payload()))
					rec := httptest.NewRecorder()
					router.ServeHTTP(rec, req)
					codes <- rec.Code
				}()
			}
			wg.Wait()
			close(codes)
			for code := range codes {
				Expect(code).To(Equal(http.StatusCreated))
			}

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusOK))
			var body chainBody
			Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
			blocks := body.Blocks
			Expect(blocks).To(HaveLen(n + 1))
			for i, b := range blocks {
				Expect(b.Index).To(Equal(i))
				if i > 0 {
					Expect(b.PrevHash).To(Equal(blocks[i-1].Hash))
					Expect(engine.Verify(blocks[:i], b)).To(Succeed())
				}
			}
		})

		It("returns 409 chain_conflict when another writer extends the chain mid-mining", func() {
			withGenerator(func(ctx context.Context, c consensus.Consensus, history []*domain.Block, txs []domain.Transaction) (*domain.Block, error) {
				rival, err := chain.GenerateBlockWithContext(ctx, c, history, txs)
				Expect(err).NotTo(HaveOccurred())
				Expect(repo.Append(rival)).To(Succeed())
				return chain.GenerateBlockWithContext(ctx, c, history, txs)
			})
			initialLen := chainLen()

			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(funds.payload()))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusConflict))
			Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))
			var resp errorResponse
			Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
			Expect(resp.Code).To(Equal("chain_conflict"))
			Expect(resp.Error).NotTo(BeEmpty())
			Expect(chainLen()).To(Equal(initialLen + 1))
		})
	})

	Describe("POST / block rules", func() {
		post := func(body string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			return rec
		}
		It("returns 422 block_too_large when transactions exceed the limit", func() {
			// a signed transaction is ~450 bytes: stay under the 2x request body limit
			GinkgoT().Setenv(domain.MaxBlockBytesEnvVar, "300")
			rec := post(funds.payload())

			Expect(rec.Code).To(Equal(http.StatusUnprocessableEntity))
			var body map[string]any
			Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
			Expect(body).To(HaveKey("error"))
			Expect(body["code"]).To(Equal("block_too_large"))
			Expect(body["details"]).To(HaveKey("size"))
			Expect(body["details"]).To(HaveKeyWithValue("max", BeNumerically("==", 300)))
			Expect(chainLen()).To(Equal(1))
		})

		It("returns 422 with the code of a timestamp violation after mining", func() {
			withGenerator(func(ctx context.Context, c consensus.Consensus, history []*domain.Block, txs []domain.Transaction) (*domain.Block, error) {
				b, err := chain.GenerateBlockWithContext(ctx, c, history, txs)
				if err == nil {
					b.Timestamp = history[len(history)-1].Timestamp.Add(-time.Second)
				}
				return b, err
			})
			rec := post(funds.payload())

			Expect(rec.Code).To(Equal(http.StatusUnprocessableEntity))
			var body map[string]any
			Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
			Expect(body["code"]).To(Equal("timestamp_before_parent"))
			Expect(chainLen()).To(Equal(1))
		})

		It("returns 422 timestamp_too_far_in_future for a far-future block", func() {
			withGenerator(func(ctx context.Context, c consensus.Consensus, history []*domain.Block, txs []domain.Transaction) (*domain.Block, error) {
				b, err := chain.GenerateBlockWithContext(ctx, c, history, txs)
				if err == nil {
					b.Timestamp = time.Now().Add(3 * time.Hour)
				}
				return b, err
			})
			rec := post(funds.payload())

			Expect(rec.Code).To(Equal(http.StatusUnprocessableEntity))
			var body map[string]any
			Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
			Expect(body["code"]).To(Equal("timestamp_too_far_in_future"))
		})

		It("returns 413 request_too_large for a giant body", func() {
			GinkgoT().Setenv(domain.MaxBlockBytesEnvVar, "100")
			rec := post(`{"Transactions":[{"ID":"` + strings.Repeat("a", 1000) + `"}]}`)

			Expect(rec.Code).To(Equal(http.StatusRequestEntityTooLarge))
			var body map[string]any
			Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
			Expect(body["code"]).To(Equal("request_too_large"))
			Expect(body["error"]).To(Equal("request body exceeds the maximum size"))
			Expect(body["details"]).To(HaveKeyWithValue("max", BeNumerically("==", 200)))
		})

		It("returns 413 request_too_large for a giant body with rate limiting enabled and unexhausted", func() {
			GinkgoT().Setenv(domain.MaxBlockBytesEnvVar, "100")
			repo := memory.New()
			engine := pow.NewProofOfWorkFromEnv()
			svc, err := funds.service(repo, engine)
			Expect(err).NotTo(HaveOccurred())
			s, err := NewServer(svc, Config{RateLimit: 10})
			Expect(err).NotTo(HaveOccurred())

			req := httptest.NewRequest("POST", "/", strings.NewReader(`{"Transactions":[{"ID":"`+strings.Repeat("a", 1000)+`"}]}`))
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusRequestEntityTooLarge))
			var body map[string]any
			Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
			Expect(body["code"]).To(Equal("request_too_large"))
			Expect(body["error"]).To(Equal("request body exceeds the maximum size"))
			Expect(body["details"]).To(HaveKeyWithValue("max", BeNumerically("==", 200)))
		})

		It("still returns 201 on the happy path", func() {
			Expect(post(funds.payload()).Code).To(Equal(http.StatusCreated))
		})
	})

	Describe("POST / additional cases", func() {
		DescribeTable("returns 400 for invalid bodies and leaves the chain unchanged",
			func(body string, expectedMsg string) {
				initialLen := chainLen()

				req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
				rec := httptest.NewRecorder()

				router.ServeHTTP(rec, req)

				Expect(rec.Code).To(Equal(http.StatusBadRequest))
				Expect(rec.Body.String()).To(MatchJSON(`{"error": "` + expectedMsg + `"}`))
				Expect(chainLen()).To(Equal(initialLen))
			},
			Entry("empty body", "", "invalid request body"),
			Entry("JSON array", `[1,2]`, "invalid request body"),
			Entry("wrong Transactions type", `{"Transactions":"abc"}`, "invalid request body"),
			Entry("missing Transactions field", `{}`, "transaction list cannot be empty"),
			Entry("empty Transactions list", `{"Transactions":[]}`, "transaction list cannot be empty"),
			Entry("transaction with divergent ID", `{"Transactions":[{"ID":"invalid-id","Outputs":[{"Amount":1,"Address":"bob"}]}]}`, "invalid transaction: transaction id does not match calculated hash"),
		)

		It("rejects blocks with 422 when the injected consensus fails verification", func() {
			engine = rejectingConsensus{inner: engine}
			build()
			initialLen := chainLen()

			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(funds.payload()))
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusUnprocessableEntity))
			Expect(rec.Body.String()).To(MatchJSON(`{"error": "invalid block could not be added"}`))
			Expect(chainLen()).To(Equal(initialLen))
		})

		It("returns 500 with the error message on a generic generator error", func() {
			withGenerator(func(ctx context.Context, c consensus.Consensus, history []*domain.Block, txs []domain.Transaction) (*domain.Block, error) {
				return nil, errors.New("boom")
			})
			initialLen := chainLen()

			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(funds.payload()))
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusInternalServerError))
			Expect(rec.Body.String()).To(MatchJSON(`{"error": "boom"}`))
			Expect(chainLen()).To(Equal(initialLen))
		})
	})

	Describe("block queries", func() {
		get := func(target string) *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
			return rec
		}
		mineN := func(n int) {
			for i := 0; i < n; i++ {
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(funds.payload())))
				Expect(rec.Code).To(Equal(http.StatusCreated))
			}
		}
		type page struct {
			Blocks []*domain.Block `json:"blocks"`
			From   int             `json:"from"`
			Limit  int             `json:"limit"`
			Next   *int            `json:"next"`
		}
		getPage := func(target string) page {
			rec := get(target)
			Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
			Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))
			var p page
			Expect(json.Unmarshal(rec.Body.Bytes(), &p)).To(Succeed())
			return p
		}
		expectError := func(rec *httptest.ResponseRecorder, status int, code string) {
			Expect(rec.Code).To(Equal(status), rec.Body.String())
			Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))
			var resp errorResponse
			Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
			Expect(resp.Code).To(Equal(code))
			Expect(resp.Error).NotTo(BeEmpty())
		}
		expectLinked := func(p page) {
			for i, b := range p.Blocks {
				Expect(b.Index).To(Equal(p.From + i))
				if i > 0 {
					Expect(b.PrevHash).To(Equal(p.Blocks[i-1].Hash))
				}
			}
		}

		Describe("GET /blocks", func() {
			BeforeEach(func() { mineN(4) }) // heights 0..4

			It("defaults to from=0 and limit=20", func() {
				p := getPage("/blocks")
				Expect(p.From).To(Equal(0))
				Expect(p.Limit).To(Equal(20))
				Expect(p.Blocks).To(HaveLen(5))
				Expect(p.Next).To(BeNil())
				expectLinked(p)
			})

			It("returns the window and a next cursor while more blocks exist", func() {
				p := getPage("/blocks?from=1&limit=2")
				Expect(p.From).To(Equal(1))
				Expect(p.Limit).To(Equal(2))
				Expect(p.Blocks).To(HaveLen(2))
				Expect(p.Next).NotTo(BeNil())
				Expect(*p.Next).To(Equal(3))
				expectLinked(p)
			})

			It("omits next when the page ends exactly at the tip", func() {
				p := getPage("/blocks?from=3&limit=2")
				Expect(p.Blocks).To(HaveLen(2))
				Expect(p.Next).To(BeNil())
				Expect(get("/blocks?from=3&limit=2").Body.String()).NotTo(ContainSubstring(`"next"`))
			})

			It("returns an empty list without next when from is past the tip", func() {
				for _, target := range []string{"/blocks?from=5", "/blocks?from=999999999999"} {
					rec := get(target)
					Expect(rec.Code).To(Equal(http.StatusOK))
					Expect(rec.Body.String()).To(ContainSubstring(`"blocks": []`))
					var p page
					Expect(json.Unmarshal(rec.Body.Bytes(), &p)).To(Succeed())
					Expect(p.Blocks).To(BeEmpty())
					Expect(p.Next).To(BeNil())
				}
			})

			It("caps limit at 100", func() {
				p := getPage("/blocks?limit=100")
				Expect(p.Limit).To(Equal(100))
				p = getPage("/blocks?limit=101")
				Expect(p.Limit).To(Equal(100))
				p = getPage("/blocks?limit=9223372036854775807")
				Expect(p.Limit).To(Equal(100))
			})

			It("follows next from 0 and returns the whole chain once, in order", func() {
				all, _ := repo.All()
				var got []*domain.Block
				target := "/blocks?limit=2"
				for target != "" {
					p := getPage(target)
					got = append(got, p.Blocks...)
					target = ""
					if p.Next != nil {
						target = fmt.Sprintf("/blocks?from=%d&limit=2", *p.Next)
					}
				}
				Expect(got).To(HaveLen(len(all)))
				for i := range all {
					Expect(got[i].Hash).To(Equal(all[i].Hash))
				}
			})

			DescribeTable("rejects bad query values with 400 invalid_query",
				func(target string) {
					expectError(get(target), http.StatusBadRequest, "invalid_query")
				},
				Entry("non-integer from", "/blocks?from=abc"),
				Entry("non-integer limit", "/blocks?limit=1.5"),
				Entry("empty from", "/blocks?from="),
				Entry("empty limit", "/blocks?limit="),
				Entry("negative from", "/blocks?from=-1"),
				Entry("negative limit", "/blocks?limit=-1"),
				Entry("zero limit", "/blocks?limit=0"),
				Entry("overflowing limit", "/blocks?limit=99999999999999999999"),
			)

			It("returns 500 when the chain cannot be read", func() {
				srv, err := NewServer(brokenChain{}, Config{})
				Expect(err).NotTo(HaveOccurred())
				h := srv.Handler()
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/blocks", nil))
				Expect(rec.Code).To(Equal(http.StatusInternalServerError))
			})
		})

		Describe("GET /block/{ref}", func() {
			BeforeEach(func() { mineN(2) })

			It("finds a block by height and by hash in the POST / format", func() {
				all, _ := repo.All()
				for _, want := range all {
					refs := []string{strconv.Itoa(want.Index)}
					if want.Hash != "" { // the genesis block is stored unsealed, without a hash
						refs = append(refs, want.Hash)
					}
					for _, ref := range refs {
						rec := get("/block/" + ref)
						Expect(rec.Code).To(Equal(http.StatusOK))
						Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))
						wantJSON, err := json.MarshalIndent(want, "", "  ")
						Expect(err).NotTo(HaveOccurred())
						Expect(rec.Body.String()).To(MatchJSON(wantJSON))
					}
				}
			})

			It("returns 404 block_not_found for an unknown height or hash", func() {
				unknownHash := strings.Repeat("a", 64)
				for _, ref := range []string{"3", "99999", "99999999999999999999", unknownHash} {
					expectError(get("/block/"+ref), http.StatusNotFound, "block_not_found")
				}
			})

			DescribeTable("returns 400 invalid_block_ref for anything else",
				func(ref string) {
					expectError(get("/block/"+ref), http.StatusBadRequest, "invalid_block_ref")
				},
				Entry("letters", "abc"),
				Entry("negative height", "-1"),
				Entry("uppercase hash", strings.Repeat("A", 64)),
				Entry("63 hex characters", strings.Repeat("a", 63)),
				Entry("65 hex characters", strings.Repeat("a", 65)),
				Entry("64 non-hex characters", strings.Repeat("g", 64)),
				Entry("64 decimal digits is a hash, 65 are not", strings.Repeat("1", 65)),
				Entry("decimal with sign", "+1"),
			)

			It("treats 64 zeros as a hash, not as height 0", func() {
				expectError(get("/block/"+strings.Repeat("0", 64)), http.StatusNotFound, "block_not_found")
			})

			It("treats 64 decimal digits as a hash, not a height", func() {
				expectError(get("/block/"+strings.Repeat("1", 64)), http.StatusNotFound, "block_not_found")
			})

			It("returns 500 when the chain cannot be read", func() {
				srv, err := NewServer(brokenChain{}, Config{})
				Expect(err).NotTo(HaveOccurred())
				h := srv.Handler()
				for _, ref := range []string{"0", strings.Repeat("a", 64)} {
					rec := httptest.NewRecorder()
					h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/block/"+ref, nil))
					Expect(rec.Code).To(Equal(http.StatusInternalServerError))
				}
			})
		})

		Describe("GET /health", func() {
			It("reports the current tip", func() {
				mineN(2)
				rec := get("/health")
				Expect(rec.Code).To(Equal(http.StatusOK))
				Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))
				Expect(rec.Body.String()).To(MatchJSON(fmt.Sprintf(`{"status":"ok","height":2,"tip":%q}`, lastBlock().Hash)))
			})

			It("returns 503 unhealthy when the repository fails", func() {
				srv, err := NewServer(brokenChain{}, Config{})
				Expect(err).NotTo(HaveOccurred())
				h := srv.Handler()
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
				expectError(rec, http.StatusServiceUnavailable, "unhealthy")
			})
		})

		Describe("GET /tx/{id}", func() {
			BeforeEach(func() { mineN(2) })

			It("finds a mined transaction with block_hash and height", func() {
				all, _ := repo.All()
				tx := all[1].Transactions[0]
				rec := get("/tx/" + tx.ID)
				Expect(rec.Code).To(Equal(http.StatusOK))
				Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))

				var resp struct {
					Transaction domain.Transaction `json:"transaction"`
					BlockHash   string             `json:"block_hash"`
					Height      int                `json:"height"`
				}
				Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp.Transaction.ID).To(Equal(tx.ID))
				Expect(resp.BlockHash).To(Equal(all[1].Hash))
				Expect(resp.Height).To(Equal(1))
			})

			It("finds the genesis allocation transaction at height 0 with empty block_hash", func() {
				all, _ := repo.All()
				allocTx := all[0].Transactions[0]
				rec := get("/tx/" + allocTx.ID)
				Expect(rec.Code).To(Equal(http.StatusOK))

				var resp struct {
					Transaction domain.Transaction `json:"transaction"`
					BlockHash   string             `json:"block_hash"`
					Height      int                `json:"height"`
				}
				Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp.Transaction.ID).To(Equal(allocTx.ID))
				Expect(resp.BlockHash).To(Equal(""))
				Expect(resp.Height).To(Equal(0))
			})

			It("returns 404 tx_not_found for an unknown 64-char hex id", func() {
				unknownID := strings.Repeat("a", 64)
				expectError(get("/tx/"+unknownID), http.StatusNotFound, "tx_not_found")
			})

			DescribeTable("returns 400 invalid_tx_id for a malformed id",
				func(id string) {
					expectError(get("/tx/"+id), http.StatusBadRequest, "invalid_tx_id")
				},
				Entry("letters too short", "abcd"),
				Entry("uppercase hex", strings.Repeat("A", 64)),
				Entry("63 hex characters", strings.Repeat("b", 63)),
				Entry("65 hex characters", strings.Repeat("b", 65)),
				Entry("64 non-hex characters", strings.Repeat("g", 64)),
			)

			It("returns 500 when the chain cannot be read", func() {
				srv, err := NewServer(brokenChain{}, Config{})
				Expect(err).NotTo(HaveOccurred())
				h := srv.Handler()
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/tx/"+strings.Repeat("a", 64), nil))
				Expect(rec.Code).To(Equal(http.StatusInternalServerError))
			})
		})

		Describe("GET /balance/{addr}", func() {
			BeforeEach(func() { mineN(1) })

			post := func(body string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				return rec
			}

			It("returns 400 invalid_address when ValidateAddress fails", func() {
				for _, bad := range []string{"invalid", "123", "0x123", "bc1qxxx"} {
					expectError(get("/balance/"+bad), http.StatusBadRequest, "invalid_address")
				}
			})

			It("returns 200 with balance 0 and empty utxos slice for a valid address with no outputs", func() {
				unused := newWallet()
				rec := get("/balance/" + unused.Address())
				Expect(rec.Code).To(Equal(http.StatusOK))
				Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))
				Expect(rec.Body.String()).To(ContainSubstring(`"utxos": []`))
				Expect(rec.Body.String()).NotTo(ContainSubstring(`"utxos": null`))

				var resp struct {
					Address string       `json:"address"`
					Balance uint64       `json:"balance"`
					UTXOs   []chain.UTXO `json:"utxos"`
				}
				Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp.Address).To(Equal(unused.Address()))
				Expect(resp.Balance).To(Equal(uint64(0)))
				Expect(resp.UTXOs).To(BeEmpty())
			})

			It("returns 200 with balance and sorted utxos for a funded address", func() {
				rec := get("/balance/" + funds.owner.Address())
				Expect(rec.Code).To(Equal(http.StatusOK))

				var resp struct {
					Address string       `json:"address"`
					Balance uint64       `json:"balance"`
					UTXOs   []chain.UTXO `json:"utxos"`
				}
				Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp.Address).To(Equal(funds.owner.Address()))
				Expect(resp.UTXOs).To(HaveLen(bankOutputs - 1)) // 1 output was spent in mineN(1)
				var sum uint64
				for i, u := range resp.UTXOs {
					sum += u.Amount
					if i > 0 {
						prev := resp.UTXOs[i-1]
						if prev.TxID == u.TxID {
							Expect(prev.Index).To(BeNumerically("<", u.Index))
						} else {
							Expect(prev.TxID < u.TxID).To(BeTrue())
						}
					}
				}
				Expect(resp.Balance).To(Equal(sum))
			})

			It("reflects balance drop of (X - troco) for sender and increase for recipient after POST /", func() {
				alice := newWallet()
				bob := newWallet()

				// First fund Alice from the bank with 100
				rec := post(payloadOf(funds.tx(alice.Address(), 100)))
				Expect(rec.Code).To(Equal(http.StatusCreated))

				// Get Alice's balance and UTXO
				aliceBalRec := get("/balance/" + alice.Address())
				Expect(aliceBalRec.Code).To(Equal(http.StatusOK))
				var aliceBalResp struct {
					Balance uint64       `json:"balance"`
					UTXOs   []chain.UTXO `json:"utxos"`
				}
				Expect(json.Unmarshal(aliceBalRec.Body.Bytes(), &aliceBalResp)).To(Succeed())
				Expect(aliceBalResp.Balance).To(Equal(uint64(100)))
				Expect(aliceBalResp.UTXOs).To(HaveLen(1))

				// Alice spends her 100 output (X = 100), paying Bob 40 and returning 60 change (troco = 60) to Alice
				spentOp := domain.OutPoint{TxID: aliceBalResp.UTXOs[0].TxID, Index: aliceBalResp.UTXOs[0].Index}
				tx := domain.Transaction{
					Inputs: []domain.TxIn{{PrevTxID: spentOp.TxID, OutIndex: spentOp.Index}},
					Outputs: []domain.TxOut{
						{Amount: 40, Address: bob.Address()},
						{Amount: 60, Address: alice.Address()},
					},
					Timestamp: time.Now(),
				}
				tx.ID = tx.CalculateHash()
				Expect(domain.SignInput(&tx, 0, alice)).To(Succeed())

				rec = post(payloadOf(tx))
				Expect(rec.Code).To(Equal(http.StatusCreated))

				// Alice's balance drops by X - troco (100 - 60 = 40)
				var aliceAfter, bobAfter struct {
					Balance uint64       `json:"balance"`
					UTXOs   []chain.UTXO `json:"utxos"`
				}
				Expect(json.Unmarshal(get("/balance/"+alice.Address()).Body.Bytes(), &aliceAfter)).To(Succeed())
				Expect(aliceAfter.Balance).To(Equal(uint64(60)))
				Expect(aliceAfter.UTXOs).To(HaveLen(1))

				// Bob's balance rises by 40
				Expect(json.Unmarshal(get("/balance/"+bob.Address()).Body.Bytes(), &bobAfter)).To(Succeed())
				Expect(bobAfter.Balance).To(Equal(uint64(40)))
				Expect(bobAfter.UTXOs).To(HaveLen(1))
			})
		})

		Describe("while Mine is stuck", func() {
			It("answers every read in under a second", func() {
				mineN(1)
				entered := make(chan struct{})
				release := make(chan struct{})
				withGenerator(func(ctx context.Context, c consensus.Consensus, history []*domain.Block, txs []domain.Transaction) (*domain.Block, error) {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
					}
					return nil, ctx.Err()
				})
				mineN := func() {
					defer GinkgoRecover()
					router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(funds.payload())))
				}
				done := make(chan struct{})
				go func() { defer close(done); mineN() }()
				Eventually(entered).Should(BeClosed())
				defer func() { close(release); <-done }()

				tip := lastBlock()
				targets := []string{
					"/blocks",
					"/block/0",
					"/block/" + tip.Hash,
					"/health",
					"/tx/" + tip.Transactions[0].ID,
					"/balance/" + funds.owner.Address(),
				}
				for _, target := range targets {
					start := time.Now()
					rec := get(target)
					Expect(rec.Code).To(Equal(http.StatusOK), target)
					Expect(time.Since(start)).To(BeNumerically("<", time.Second), target)
				}
			})
		})

		Describe("with concurrent POST / and parallel reads", func() {
			It("keeps every page linked and every block findable", func() {
				const writers, readers, rounds = 6, 6, 40
				var wg sync.WaitGroup
				stop := make(chan struct{})
				for w := 0; w < writers; w++ {
					wg.Add(1)
					go func() {
						defer GinkgoRecover()
						defer wg.Done()
						rec := httptest.NewRecorder()
						router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(funds.payload())))
						Expect(rec.Code).To(Equal(http.StatusCreated))
					}()
				}
				var rg sync.WaitGroup
				for r := 0; r < readers; r++ {
					rg.Add(1)
					go func() {
						defer GinkgoRecover()
						defer rg.Done()
						for i := 0; i < rounds; i++ {
							select {
							case <-stop:
								return
							default:
							}
							p := getPage("/blocks?from=0&limit=3")
							Expect(len(p.Blocks)).To(BeNumerically("<=", 3))
							expectLinked(p)
							for _, b := range p.Blocks {
								if b.Hash != "" { // genesis has no hash
									Expect(get("/block/" + b.Hash).Code).To(Equal(http.StatusOK))
								}
								Expect(get("/block/" + strconv.Itoa(b.Index)).Code).To(Equal(http.StatusOK))
								for _, t := range b.Transactions {
									txRec := get("/tx/" + t.ID)
									Expect(txRec.Code).To(Equal(http.StatusOK))
									var resp struct {
										Transaction domain.Transaction `json:"transaction"`
										BlockHash   string             `json:"block_hash"`
										Height      int                `json:"height"`
									}
									Expect(json.Unmarshal(txRec.Body.Bytes(), &resp)).To(Succeed())
									Expect(resp.Transaction.ID).To(Equal(t.ID))
									Expect(resp.BlockHash).To(Equal(b.Hash))
									Expect(resp.Height).To(Equal(b.Index))
								}
							}
							Expect(get("/health").Code).To(Equal(http.StatusOK))

							balRec := get("/balance/" + funds.owner.Address())
							Expect(balRec.Code).To(Equal(http.StatusOK))
							var balResp struct {
								Address string       `json:"address"`
								Balance uint64       `json:"balance"`
								UTXOs   []chain.UTXO `json:"utxos"`
							}
							Expect(json.Unmarshal(balRec.Body.Bytes(), &balResp)).To(Succeed())
							var sum uint64
							for _, u := range balResp.UTXOs {
								sum += u.Amount
							}
							Expect(balResp.Balance).To(Equal(sum))
						}
					}()
				}
				wg.Wait()
				close(stop)
				rg.Wait()
			})
		})
	})

	Describe("POST /legacy/bpm", func() {
		DescribeTable("returns 410 Gone with Deprecation header and leaves the chain unchanged",
			func(body string) {
				initialLen := chainLen()

				req := httptest.NewRequest(http.MethodPost, "/legacy/bpm", strings.NewReader(body))
				rec := httptest.NewRecorder()

				router.ServeHTTP(rec, req)

				Expect(rec.Code).To(Equal(http.StatusGone))
				Expect(rec.Header().Get("Deprecation")).To(Equal("true"))
				Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))
				Expect(rec.Body.String()).To(MatchJSON(`{"error": "endpoint removed; send signed transactions to POST /", "code": "endpoint_gone"}`))
				Expect(chainLen()).To(Equal(initialLen))
			},
			Entry("valid BPM", `{"BPM": 42}`),
			Entry("invalid BPM", `{"BPM": 0}`),
			Entry("empty body", ""),
		)
	})

	DescribeTable("unsupported methods on / return 405",
		func(method string) {
			req := httptest.NewRequest(method, "/", nil)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusMethodNotAllowed))
			Expect(rec.Body.String()).To(BeEmpty())
		},
		Entry("PUT", http.MethodPut),
		Entry("DELETE", http.MethodDelete),
	)

	Describe("ParseMiningTimeout", func() {
		DescribeTable("parses MINING_TIMEOUT",
			func(raw string, expected time.Duration) {
				Expect(ParseMiningTimeout(raw)).To(Equal(expected))
			},
			Entry("duration", "5s", 5*time.Second),
			Entry("zero duration", "0s", 10*time.Second),
			Entry("negative duration", "-1s", 10*time.Second),
			Entry("integer seconds", "7", 7*time.Second),
			Entry("zero seconds", "0", 10*time.Second),
			Entry("invalid", "abc", 10*time.Second),
			Entry("empty", "", 10*time.Second),
		)

		It("has a 10s default", func() {
			Expect(DefaultMiningTimeout).To(Equal(10 * time.Second))
		})
	})

	Describe("NewServer", func() {
		It("fails when svc is nil", func() {
			_, err := NewServer(nil, Config{})
			Expect(err).To(MatchError(ErrNilService))
		})

		It("falls back to the default mining timeout for a non-positive value", func() {
			svc, err := chain.NewService(memory.New(), engine)
			Expect(err).NotTo(HaveOccurred())
			s1, err := NewServer(svc, Config{})
			Expect(err).NotTo(HaveOccurred())
			Expect(s1.miningTimeout).To(Equal(DefaultMiningTimeout))
			s2, err := NewServer(svc, Config{MiningTimeout: -time.Second})
			Expect(err).NotTo(HaveOccurred())
			Expect(s2.miningTimeout).To(Equal(DefaultMiningTimeout))
			s3, err := NewServer(svc, Config{MiningTimeout: time.Second})
			Expect(err).NotTo(HaveOccurred())
			Expect(s3.miningTimeout).To(Equal(time.Second))
		})

		It("isolates state between two servers in the same process", func() {
			bankA := newBank()
			svcA, _ := bankA.service(memory.New(), engine)
			svcB, _ := chain.NewService(memory.New(), engine)
			srvA, err := NewServer(svcA, Config{})
			Expect(err).NotTo(HaveOccurred())
			srvB, err := NewServer(svcB, Config{})
			Expect(err).NotTo(HaveOccurred())
			a, b := srvA.Handler(), srvB.Handler()

			rec := httptest.NewRecorder()
			a.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(bankA.payload())))
			Expect(rec.Code).To(Equal(http.StatusCreated))

			count := func(h http.Handler) int {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
				var body chainBody
				Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
				return len(body.Blocks)
			}
			Expect(count(a)).To(Equal(2))
			Expect(count(b)).To(Equal(1))
		})

		It("returns 500 on GET / when the chain cannot be read", func() {
			srv, err := NewServer(brokenChain{}, Config{})
			Expect(err).NotTo(HaveOccurred())
			h := srv.Handler()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			Expect(rec.Code).To(Equal(http.StatusInternalServerError))
			Expect(rec.Body.String()).To(ContainSubstring("read failed"))
		})
	})

	Describe("rate limiting wiring via Server.Handler()", func() {
		It("enforces rate limit on POST /, blocks mining when exceeded, and recovers with Config.Now", func() {
			var currentTime = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
			fakeNow := func() time.Time { return currentTime }

			spy := &spyMineChain{}
			srv, err := NewServer(spy, Config{RateLimit: 3, Now: fakeNow})
			Expect(err).NotTo(HaveOccurred())

			h := srv.Handler()
			clientIP := "192.168.10.50:9999"

			// Send N = 3 requests: all should be allowed and call Mine
			for i := 0; i < 3; i++ {
				req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(funds.payload()))
				req.RemoteAddr = clientIP
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				Expect(rec.Code).To(Equal(http.StatusCreated))
			}
			Expect(atomic.LoadInt64(&spy.mineCalls)).To(Equal(int64(3)))

			// Request N+1 (4th) from same IP at same instant must be rate limited (429)
			req4 := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(funds.payload()))
			req4.RemoteAddr = clientIP
			rec4 := httptest.NewRecorder()
			h.ServeHTTP(rec4, req4)

			Expect(rec4.Code).To(Equal(http.StatusTooManyRequests))
			Expect(rec4.Result().Header.Get("Retry-After")).To(Equal("20")) // 60s / 3 = 20s
			var errBody map[string]any
			Expect(json.Unmarshal(rec4.Body.Bytes(), &errBody)).To(Succeed())
			Expect(errBody["code"]).To(Equal("rate_limited"))
			Expect(errBody["error"]).To(Equal("rate limit exceeded"))

			// Barred request must not call Mine (Inv.3)
			Expect(atomic.LoadInt64(&spy.mineCalls)).To(Equal(int64(3)))

			// The bucket is per IP, not per route: every route of Handler() is limited.
			// Without the limiter these routes answer 500 (broken Blocks) or 200/4xx, never 429.
			for _, rt := range []struct{ method, path, body string }{
				{http.MethodGet, "/", ""},
				{http.MethodGet, "/blocks", ""},
				{http.MethodGet, "/block/1", ""},
				{http.MethodGet, "/health", ""},
				{http.MethodPost, "/legacy/bpm", `{"BPM": 72}`},
			} {
				req := httptest.NewRequest(rt.method, rt.path, strings.NewReader(rt.body))
				req.RemoteAddr = clientIP
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				Expect(rec.Code).To(Equal(http.StatusTooManyRequests), "%s %s", rt.method, rt.path)
				Expect(rec.Result().Header.Get("Retry-After")).To(Equal("20"), "%s %s", rt.method, rt.path)
			}
			Expect(atomic.LoadInt64(&spy.mineCalls)).To(Equal(int64(3)))

			// Advance clock by 20s (60s/N) so 1 token recovers
			currentTime = currentTime.Add(20 * time.Second)

			// 5th request should succeed now and call Mine (AC2 & Config.Now wiring)
			req5 := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(funds.payload()))
			req5.RemoteAddr = clientIP
			rec5 := httptest.NewRecorder()
			h.ServeHTTP(rec5, req5)

			Expect(rec5.Code).To(Equal(http.StatusCreated))
			Expect(atomic.LoadInt64(&spy.mineCalls)).To(Equal(int64(4)))
		})

		It("rate limits the 405 and 404 routes too (ADR-0006)", func() {
			now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
			srv, err := NewServer(&spyMineChain{}, Config{RateLimit: 1, Now: func() time.Time { return now }})
			Expect(err).NotTo(HaveOccurred())

			h := srv.Handler()
			for _, rt := range []struct {
				method, path string
				first        int
			}{
				{http.MethodPut, "/blocks", http.StatusMethodNotAllowed},
				{http.MethodGet, "/nao-existe", http.StatusNotFound},
			} {
				now = now.Add(time.Hour) // refill the single token
				for i, want := range []int{rt.first, http.StatusTooManyRequests} {
					req := httptest.NewRequest(rt.method, rt.path, nil)
					req.RemoteAddr = "192.168.10.51:9999"
					rec := httptest.NewRecorder()
					h.ServeHTTP(rec, req)
					Expect(rec.Code).To(Equal(want), "%s %s request %d", rt.method, rt.path, i+1)
				}
			}
		})

		It("never rate limits through Handler() when RateLimit is 0", func() {
			spy := &spyMineChain{}
			srv, err := NewServer(spy, Config{RateLimit: 0})
			Expect(err).NotTo(HaveOccurred())

			h := srv.Handler()
			clientIP := "192.168.10.50:9999"

			for i := 0; i < 100; i++ {
				req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(funds.payload()))
				req.RemoteAddr = clientIP
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				Expect(rec.Code).To(Equal(http.StatusCreated))
			}
			Expect(atomic.LoadInt64(&spy.mineCalls)).To(Equal(int64(100)))
		})
	})

	Describe("HTTPServer over a real listener", func() {
		start := func() string {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			Expect(err).NotTo(HaveOccurred())
			addr := l.Addr().String()
			Expect(l.Close()).To(Succeed())

			svc, err := chain.NewService(repo, engine)
			Expect(err).NotTo(HaveOccurred())
			srv, err := NewServer(svc, cfg)
			Expect(err).NotTo(HaveOccurred())
			hs := srv.HTTPServer(addr)
			DeferCleanup(func() { _ = hs.Close() })
			go func() {
				defer GinkgoRecover()
				_ = hs.ListenAndServe()
			}()
			Eventually(func() error {
				conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
				if err == nil {
					_ = conn.Close() // probe connection; close error is irrelevant
				}
				return err
			}, 5*time.Second, 20*time.Millisecond).Should(Succeed())
			return addr
		}

		It("sets the service address, header limit and handler", func() {
			svc, _ := chain.NewService(memory.New(), engine)
			srv, err := NewServer(svc, Config{})
			Expect(err).NotTo(HaveOccurred())
			hs := srv.HTTPServer(":1234")
			Expect(hs.Addr).To(Equal(":1234"))
			Expect(hs.Handler).NotTo(BeNil())
			Expect(hs.MaxHeaderBytes).To(Equal(1 << 20))
			Expect(hs.ReadTimeout).To(Equal(10 * time.Second))
			Expect(hs.WriteTimeout).To(Equal(10 * time.Second))
		})

		It("serves GET / through a real HTTP client", func() {
			addr := start()

			client := &http.Client{Timeout: 5 * time.Second}
			resp, err := client.Get("http://" + addr + "/")
			Expect(err).NotTo(HaveOccurred())
			defer func() { _ = resp.Body.Close() }() // response body of a test request; close error is irrelevant
			body, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())

			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			var bc chainBody
			Expect(json.Unmarshal(body, &bc)).To(Succeed())
			Expect(bc.Blocks).To(HaveLen(1))
		})

		// A 10s ReadTimeout/WriteTimeout is only observable by waiting for it: a mutant that
		// turns it into 0 (no timeout) leaves the idle connection open and lets the slow reply through.
		It("closes idle connections and drops replies written after 10s", func() {
			handlerDone := make(chan struct{})
			var handlerStarted atomic.Bool
			DeferCleanup(func() {
				if handlerStarted.Load() {
					<-handlerDone
				}
			})

			cfg = Config{MiningTimeout: 60 * time.Second}
			svc, err := chain.NewService(repo, engine, chain.WithGenerator(func(ctx context.Context, c consensus.Consensus, history []*domain.Block, txs []domain.Transaction) (*domain.Block, error) {
				handlerStarted.Store(true)
				defer close(handlerDone)
				time.Sleep(11 * time.Second)
				return nil, errors.New("too slow")
			}))
			Expect(err).NotTo(HaveOccurred())

			l, err := net.Listen("tcp", "127.0.0.1:0")
			Expect(err).NotTo(HaveOccurred())
			addr := l.Addr().String()
			Expect(l.Close()).To(Succeed())
			srv, err := NewServer(svc, cfg)
			Expect(err).NotTo(HaveOccurred())
			hs := srv.HTTPServer(addr)
			DeferCleanup(func() { _ = hs.Close() })
			go func() {
				defer GinkgoRecover()
				_ = hs.ListenAndServe()
			}()
			Eventually(func() error {
				conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
				if err == nil {
					_ = conn.Close() // probe connection; close error is irrelevant
				}
				return err
			}, 5*time.Second, 20*time.Millisecond).Should(Succeed())

			readErr := make(chan error, 1)
			go func() {
				defer GinkgoRecover()
				conn, err := net.Dial("tcp", addr)
				if err != nil {
					readErr <- err
					return
				}
				defer func() { _ = conn.Close() }() // test connection; close error is irrelevant
				_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
				_, err = conn.Read(make([]byte, 1))
				readErr <- err
			}()

			client := &http.Client{Timeout: 20 * time.Second}
			_, postErr := client.Post("http://"+addr+"/", "application/json",
				strings.NewReader(funds.payload()))
			Expect(postErr).To(HaveOccurred())
			var netErr net.Error
			Expect(errors.As(postErr, &netErr) && netErr.Timeout()).To(BeFalse(), "reply was not dropped by WriteTimeout")

			var idleErr error
			Eventually(readErr, 10*time.Second).Should(Receive(&idleErr))
			Expect(idleErr).To(MatchError(io.EOF), "idle connection was not closed by ReadTimeout")
		})
	})

	Describe("GET / (handleGetBlockchain)", func() {
		It("returns 200 OK with only the genesis block, holding the allocation", func() {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(json.Valid(rec.Body.Bytes())).To(BeTrue())
			var bc chainBody
			Expect(json.Unmarshal(rec.Body.Bytes(), &bc)).To(Succeed())
			Expect(bc.Blocks).To(HaveLen(1))
			Expect(bc.Blocks[0].Index).To(Equal(0))
			Expect(bc.Blocks[0].Transactions).To(HaveLen(1))
			Expect(bc.Blocks[0].Transactions[0].Inputs).To(BeEmpty())
			Expect(bc.Blocks[0].Transactions[0].Outputs).To(HaveLen(bankOutputs))
			Expect(bc.Blocks[0].PrevHash).To(BeEmpty())
		})

		It("returns 2 linked blocks after a valid POST", func() {
			post := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(payloadOf(funds.tx(newWallet().Address(), 77))))
			postRec := httptest.NewRecorder()
			router.ServeHTTP(postRec, post)
			Expect(postRec.Code).To(Equal(http.StatusCreated))

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

			Expect(rec.Code).To(Equal(http.StatusOK))
			var bc chainBody
			Expect(json.Unmarshal(rec.Body.Bytes(), &bc)).To(Succeed())
			Expect(bc.Blocks).To(HaveLen(2))
			Expect(bc.Blocks[1].Index).To(Equal(1))
			Expect(bc.Blocks[1].PrevHash).To(Equal(bc.Blocks[0].Hash))
			Expect(bc.Blocks[1].Transactions).To(HaveLen(1))
			Expect(bc.Blocks[1].Transactions[0].Outputs[0].Amount).To(Equal(uint64(77)))
		})
	})

	Describe("POST / UTXO transactions", func() {
		post := func(body string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			return rec
		}
		expectError := func(rec *httptest.ResponseRecorder, status int, code string) {
			Expect(rec.Code).To(Equal(status))
			Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))
			var resp errorResponse
			Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
			Expect(resp.Code).To(Equal(code))
			Expect(resp.Error).NotTo(BeEmpty())
		}

		DescribeTable("maps a rejection to its status and code and leaves the chain unchanged",
			func(mutate func(tx *domain.Transaction), status int, code string) {
				initialLen := chainLen()
				tx := funds.tx(newWallet().Address(), 5)
				mutate(&tx)

				expectError(post(payloadOf(tx)), status, code)
				Expect(chainLen()).To(Equal(initialLen))
			},
			Entry("unsigned", func(tx *domain.Transaction) { tx.Inputs[0].Signature = "" }, http.StatusBadRequest, "missing_signature"),
			Entry("missing public key", func(tx *domain.Transaction) { tx.Inputs[0].PublicKey = "" }, http.StatusBadRequest, "missing_signature"),
			Entry("tampered amount", func(tx *domain.Transaction) { tx.Outputs[0].Amount++; tx.ID = "" }, http.StatusBadRequest, "invalid_signature"),
			Entry("invalid public key", func(tx *domain.Transaction) { tx.Inputs[0].PublicKey = "abcd" }, http.StatusBadRequest, "invalid_public_key"),
			Entry("input owned by another key", func(tx *domain.Transaction) {
				Expect(domain.SignInput(tx, 0, newWallet())).To(Succeed())
			}, http.StatusBadRequest, "input_owner_mismatch"),
			Entry("zero-amount output", func(tx *domain.Transaction) { tx.Outputs[0].Amount = 0; tx.ID = "" }, http.StatusBadRequest, "invalid_output"),
			Entry("no outputs", func(tx *domain.Transaction) { tx.Outputs = nil; tx.ID = "" }, http.StatusBadRequest, "invalid_output"),
			Entry("no inputs", func(tx *domain.Transaction) { tx.Inputs = nil; tx.ID = "" }, http.StatusBadRequest, "missing_inputs"),
			Entry("outputs exceed inputs", func(tx *domain.Transaction) { tx.Outputs[0].Amount = bankAmount + 1; tx.ID = "" }, http.StatusUnprocessableEntity, "insufficient_inputs"),
			Entry("unknown input", func(tx *domain.Transaction) { tx.Inputs[0].PrevTxID = "unknown"; tx.ID = "" }, http.StatusConflict, "input_missing_or_spent"),
		)

		It("never fills the signed Timestamp: a zero timestamp is 400 missing_timestamp", func() {
			initialLen := chainLen()
			tx := funds.tx(newWallet().Address(), 5)
			tx.Timestamp = time.Time{}
			tx.ID = ""

			expectError(post(payloadOf(tx)), http.StatusBadRequest, "missing_timestamp")
			Expect(chainLen()).To(Equal(initialLen))
		})

		It("fills an empty ID without touching signed fields", func() {
			tx := funds.tx(newWallet().Address(), 5)
			tx.ID = ""

			rec := post(payloadOf(tx))
			Expect(rec.Code).To(Equal(http.StatusCreated))
			var blk domain.Block
			Expect(json.Unmarshal(rec.Body.Bytes(), &blk)).To(Succeed())
			Expect(blk.Transactions[0].ID).To(Equal(blk.Transactions[0].CalculateHash()))
			Expect(blk.Transactions[0].Timestamp.Equal(tx.Timestamp)).To(BeTrue())
		})

		It("keeps a wrong ID as 400", func() {
			tx := funds.tx(newWallet().Address(), 5)
			tx.ID = "wrong"
			Expect(post(payloadOf(tx)).Code).To(Equal(http.StatusBadRequest))
		})

		It("returns 409 double_spend for two transactions spending the same outpoint in one request", func() {
			to := newWallet().Address()
			initialLen := chainLen()
			expectError(post(payloadOf(funds.spend(0, to, 5), funds.spend(0, to, 6))), http.StatusConflict, "double_spend")
			Expect(chainLen()).To(Equal(initialLen))
		})

		It("returns 409 input_missing_or_spent for a resend of a mined transaction", func() {
			body := payloadOf(funds.tx(newWallet().Address(), 1))
			Expect(post(body).Code).To(Equal(http.StatusCreated))
			initialLen := chainLen()
			expectError(post(body), http.StatusConflict, "input_missing_or_spent")
			Expect(chainLen()).To(Equal(initialLen))
		})

		It("returns 409 input_missing_or_spent for another transaction spending a mined outpoint", func() {
			to := newWallet().Address()
			Expect(post(payloadOf(funds.spend(0, to, 1))).Code).To(Equal(http.StatusCreated))
			initialLen := chainLen()
			expectError(post(payloadOf(funds.spend(0, to, 2))), http.StatusConflict, "input_missing_or_spent")
			Expect(chainLen()).To(Equal(initialLen))
		})

		It("accepts exactly one of 50 concurrent transactions spending the same outpoint", func() {
			const n = 50
			to := newWallet().Address()
			bodies := make([]string, n)
			for i := range bodies {
				bodies[i] = payloadOf(funds.spend(0, to, uint64(1+i)))
			}
			codes := make(chan int, n)
			var wg sync.WaitGroup
			for i := 0; i < n; i++ {
				body := bodies[i]
				wg.Add(1)
				go func() {
					defer GinkgoRecover()
					defer wg.Done()
					codes <- post(body).Code
				}()
			}
			wg.Wait()
			close(codes)

			created, conflict := 0, 0
			for c := range codes {
				switch c {
				case http.StatusCreated:
					created++
				case http.StatusConflict:
					conflict++
				}
			}
			Expect(created).To(Equal(1))
			Expect(conflict).To(Equal(n - 1))
			Expect(chainLen()).To(Equal(2))
		})
	})

	Describe("routing (ADR-0006)", func() {
		DescribeTable("unsupported methods return 405 with empty body and Allow",
			func(method, path, allow string) {
				req := httptest.NewRequest(method, path, nil)
				rec := httptest.NewRecorder()

				router.ServeHTTP(rec, req)

				Expect(rec.Code).To(Equal(http.StatusMethodNotAllowed))
				Expect(rec.Body.String()).To(BeEmpty())
				Expect(rec.Header().Get("Allow")).To(Equal(allow))
			},
			Entry("PUT /", http.MethodPut, "/", "GET, HEAD, POST"),
			Entry("DELETE /", http.MethodDelete, "/", "GET, HEAD, POST"),
			Entry("PUT /blocks", http.MethodPut, "/blocks", "GET, HEAD"),
			Entry("POST /blocks", http.MethodPost, "/blocks", "GET, HEAD"),
			Entry("POST /block/abc", http.MethodPost, "/block/abc", "GET, HEAD"),
			Entry("POST /tx/abc", http.MethodPost, "/tx/abc", "GET, HEAD"),
			Entry("POST /balance/abc", http.MethodPost, "/balance/abc", "GET, HEAD"),
			Entry("POST /health", http.MethodPost, "/health", "GET, HEAD"),
			Entry("GET /legacy/bpm", http.MethodGet, "/legacy/bpm", "POST"),
			Entry("PUT /legacy/bpm", http.MethodPut, "/legacy/bpm", "POST"),
		)

		It("returns 404 for unregistered paths", func() {
			for _, path := range []string{"/nao-existe", "/blocks/extra", "/block/", "/health/x"} {
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
				Expect(rec.Code).To(Equal(http.StatusNotFound), path)
			}
		})

		It("serves HEAD /health with 200 and no body", func() {
			// ResponseRecorder keeps HEAD bodies; a real server drops them.
			ts := httptest.NewServer(router)
			defer ts.Close()
			resp, err := http.Head(ts.URL + "/health")
			Expect(err).NotTo(HaveOccurred())
			defer func() { _ = resp.Body.Close() }() // response body of a test request; close error is irrelevant
			body, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			Expect(body).To(BeEmpty())
		})

		It("rejects an escaped slash in the block ref with 400 invalid_block_ref", func() {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/block/a%2Fb", nil))
			Expect(rec.Code).To(Equal(http.StatusBadRequest))
			Expect(rec.Body.String()).To(ContainSubstring("invalid_block_ref"))
		})
	})
})
