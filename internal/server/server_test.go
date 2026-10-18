package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
)

// rejectingConsensus seals with inner but never accepts a block.
type rejectingConsensus struct{ inner consensus.Consensus }

func (r rejectingConsensus) Seal(ctx context.Context, b *domain.Block) error {
	return r.inner.Seal(ctx, b)
}

func (rejectingConsensus) Verify(prev, b *domain.Block) error {
	return errors.New("rejected by fake consensus")
}

type chainBody struct {
	Blocks []*domain.Block
}

// brokenChain fails to read blocks.
type brokenChain struct{}

func (brokenChain) Blocks() ([]*domain.Block, error) { return nil, errors.New("read failed") }
func (brokenChain) Mine(context.Context, []domain.Transaction) (*domain.Block, error) {
	return nil, errors.New("unused")
}

var _ = Describe("Server", func() {
	var (
		router http.Handler
		repo   *memory.Repository
		engine consensus.Consensus
		cfg    Config
	)

	// build wires repo, engine and cfg into a fresh Server; call it again after changing them.
	build := func(opts ...chain.Option) {
		svc, err := chain.NewService(repo, engine, opts...)
		Expect(err).NotTo(HaveOccurred())
		router = NewServer(svc, cfg).Handler()
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
		repo = memory.New()
		engine = pow.NewProofOfWork(1)
		cfg = Config{}
		build()
	})

	Describe("POST / (handleWriteBlock)", func() {
		Context("when receiving a valid block payload", func() {
			It("returns 201 Created, the new block with populated transaction IDs, and appends to the chain", func() {
				initialLen := chainLen()

				payload := []byte(`{"Transactions":[{"From":"alice","To":"bob","Amount":42,"Fee":1,"Nonce":0}]}`)
				req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()

				router.ServeHTTP(rec, req)

				Expect(rec.Code).To(Equal(http.StatusCreated))
				Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))

				var createdBlock domain.Block
				Expect(json.Unmarshal(rec.Body.Bytes(), &createdBlock)).To(Succeed())
				Expect(createdBlock.Transactions).To(HaveLen(1))
				Expect(createdBlock.Transactions[0].From).To(Equal("alice"))
				Expect(createdBlock.Transactions[0].To).To(Equal("bob"))
				Expect(createdBlock.Transactions[0].Amount).To(Equal(uint64(42)))
				Expect(createdBlock.Transactions[0].ID).NotTo(BeEmpty())
				Expect(createdBlock.Transactions[0].ID).To(Equal(createdBlock.Transactions[0].CalculateHash()))
				Expect(createdBlock.Transactions[0].Timestamp.IsZero()).To(BeFalse())

				Expect(chainLen()).To(Equal(initialLen + 1))
				Expect(lastBlock().Transactions).To(HaveLen(1))
				Expect(lastBlock().Transactions[0].ID).To(Equal(createdBlock.Transactions[0].ID))
				Expect(lastBlock().Hash).To(Equal(createdBlock.Hash))
			})
		})

		Context("when the generator produces an invalid block", func() {
			It("returns 422 Unprocessable Entity, error JSON, and leaves the chain unchanged", func() {
				withGenerator(func(ctx context.Context, c consensus.Consensus, oldBlock *domain.Block, txs []domain.Transaction) (*domain.Block, error) {
					b, err := chain.GenerateBlockWithContext(ctx, c, oldBlock, txs)
					if err != nil {
						return nil, err
					}
					b.PrevHash = "tampered-invalid-hash"
					return b, nil
				})

				initialLen := chainLen()

				payload := []byte(`{"Transactions":[{"From":"alice","To":"bob","Amount":42}]}`)
				req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()

				router.ServeHTTP(rec, req)

				Expect(rec.Code).To(Equal(http.StatusUnprocessableEntity))
				Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))

				Expect(rec.Body.String()).To(MatchJSON(`{"error": "bloco inválido não pôde ser adicionado"}`))

				var resp errorResponse
				Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp.Error).To(Equal("bloco inválido não pôde ser adicionado"))

				Expect(chainLen()).To(Equal(initialLen))
			})
		})

		Context("when mining times out (deadline exceeded)", func() {
			It("returns 504 Gateway Timeout, error JSON, and leaves the chain unchanged", func() {
				withGenerator(func(ctx context.Context, c consensus.Consensus, oldBlock *domain.Block, txs []domain.Transaction) (*domain.Block, error) {
					return nil, context.DeadlineExceeded
				})

				initialLen := chainLen()

				payload := []byte(`{"Transactions":[{"From":"alice","To":"bob","Amount":42}]}`)
				req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()

				router.ServeHTTP(rec, req)

				Expect(rec.Code).To(Equal(http.StatusGatewayTimeout))
				Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))

				Expect(rec.Body.String()).To(MatchJSON(`{"error": "tempo limite de mineração excedido"}`))

				var resp errorResponse
				Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp.Error).To(Equal("tempo limite de mineração excedido"))

				Expect(chainLen()).To(Equal(initialLen))
			})
		})

		Context("when mining context is canceled", func() {
			It("returns 408 Request Timeout, error JSON, and leaves the chain unchanged", func() {
				withGenerator(func(ctx context.Context, c consensus.Consensus, oldBlock *domain.Block, txs []domain.Transaction) (*domain.Block, error) {
					return nil, context.Canceled
				})

				initialLen := chainLen()

				payload := []byte(`{"Transactions":[{"From":"alice","To":"bob","Amount":42}]}`)
				req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()

				router.ServeHTTP(rec, req)

				Expect(rec.Code).To(Equal(http.StatusRequestTimeout))
				Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))

				Expect(rec.Body.String()).To(MatchJSON(`{"error": "mineração cancelada"}`))

				var resp errorResponse
				Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp.Error).To(Equal("mineração cancelada"))

				Expect(chainLen()).To(Equal(initialLen))
			})
		})

		Context("when client cancels the request context directly", func() {
			It("returns 408 Request Timeout when request context is canceled", func() {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()

				initialLen := chainLen()

				payload := []byte(`{"Transactions":[{"From":"alice","To":"bob","Amount":42}]}`)
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

				payload := []byte(`{"Transactions":[{"From":"alice","To":"bob","Amount":42}]}`)
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

				Expect(rec.Body.String()).To(MatchJSON(`{"error": "corpo da requisição inválido"}`))

				var resp errorResponse
				Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp.Error).To(Equal("corpo da requisição inválido"))

				Expect(chainLen()).To(Equal(initialLen))
			})
		})
	})

	Describe("POST / block rules", func() {
		post := func(body string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			return rec
		}
		payload := `{"Transactions":[{"From":"alice","To":"bob","Amount":42,"Fee":1,"Nonce":0}]}`

		It("returns 422 block_too_large when transactions exceed the limit", func() {
			GinkgoT().Setenv(domain.MaxBlockBytesEnvVar, "100")
			rec := post(payload)

			Expect(rec.Code).To(Equal(http.StatusUnprocessableEntity))
			var body map[string]any
			Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
			Expect(body).To(HaveKey("error"))
			Expect(body["code"]).To(Equal("block_too_large"))
			Expect(body["details"]).To(HaveKey("size"))
			Expect(body["details"]).To(HaveKeyWithValue("max", BeNumerically("==", 100)))
			Expect(chainLen()).To(Equal(1))
		})

		It("returns 422 with the code of a timestamp violation after mining", func() {
			withGenerator(func(ctx context.Context, c consensus.Consensus, oldBlock *domain.Block, txs []domain.Transaction) (*domain.Block, error) {
				b, err := chain.GenerateBlockWithContext(ctx, c, oldBlock, txs)
				if err == nil {
					b.Timestamp = oldBlock.Timestamp.Add(-time.Second)
				}
				return b, err
			})
			rec := post(payload)

			Expect(rec.Code).To(Equal(http.StatusUnprocessableEntity))
			var body map[string]any
			Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
			Expect(body["code"]).To(Equal("timestamp_before_parent"))
			Expect(chainLen()).To(Equal(1))
		})

		It("returns 422 timestamp_too_far_in_future for a far-future block", func() {
			withGenerator(func(ctx context.Context, c consensus.Consensus, oldBlock *domain.Block, txs []domain.Transaction) (*domain.Block, error) {
				b, err := chain.GenerateBlockWithContext(ctx, c, oldBlock, txs)
				if err == nil {
					b.Timestamp = time.Now().Add(3 * time.Hour)
				}
				return b, err
			})
			rec := post(payload)

			Expect(rec.Code).To(Equal(http.StatusUnprocessableEntity))
			var body map[string]any
			Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
			Expect(body["code"]).To(Equal("timestamp_too_far_in_future"))
		})

		It("returns 413 request_too_large for a giant body", func() {
			GinkgoT().Setenv(domain.MaxBlockBytesEnvVar, "100")
			rec := post(`{"Transactions":[{"From":"` + strings.Repeat("a", 1000) + `"}]}`)

			Expect(rec.Code).To(Equal(http.StatusRequestEntityTooLarge))
			var body map[string]any
			Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
			Expect(body["code"]).To(Equal("request_too_large"))
		})

		It("still returns 201 on the happy path", func() {
			Expect(post(payload).Code).To(Equal(http.StatusCreated))
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
			Entry("empty body", "", "corpo da requisição inválido"),
			Entry("JSON array", `[1,2]`, "corpo da requisição inválido"),
			Entry("wrong Transactions type", `{"Transactions":"abc"}`, "corpo da requisição inválido"),
			Entry("missing Transactions field", `{}`, "lista de transações não pode ser vazia"),
			Entry("empty Transactions list", `{"Transactions":[]}`, "lista de transações não pode ser vazia"),
			Entry("transaction with zero Amount", `{"Transactions":[{"From":"alice","To":"bob","Amount":0}]}`, "transação inválida: amount must be greater than zero"),
			Entry("transaction with empty From", `{"Transactions":[{"From":"","To":"bob","Amount":10}]}`, "transação inválida: from cannot be empty"),
			Entry("transaction with empty To", `{"Transactions":[{"From":"alice","To":"","Amount":10}]}`, "transação inválida: to cannot be empty"),
			Entry("transaction with divergent ID", `{"Transactions":[{"ID":"invalid-id","From":"alice","To":"bob","Amount":10}]}`, "transação inválida: transaction id does not match calculated hash"),
		)

		It("rejects blocks with 422 when the injected consensus fails verification", func() {
			engine = rejectingConsensus{inner: engine}
			build()
			initialLen := chainLen()

			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"Transactions":[{"From":"alice","To":"bob","Amount":42}]}`))
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusUnprocessableEntity))
			Expect(rec.Body.String()).To(MatchJSON(`{"error": "bloco inválido não pôde ser adicionado"}`))
			Expect(chainLen()).To(Equal(initialLen))
		})

		It("returns 500 with the error message on a generic generator error", func() {
			withGenerator(func(ctx context.Context, c consensus.Consensus, oldBlock *domain.Block, txs []domain.Transaction) (*domain.Block, error) {
				return nil, errors.New("boom")
			})
			initialLen := chainLen()

			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"Transactions":[{"From":"alice","To":"bob","Amount":42}]}`))
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusInternalServerError))
			Expect(rec.Body.String()).To(MatchJSON(`{"error": "boom"}`))
			Expect(chainLen()).To(Equal(initialLen))
		})
	})

	Describe("POST /legacy/bpm", func() {
		Context("when receiving a valid BPM payload", func() {
			It("returns 201 Created with Deprecation header and creates block with legacy transaction", func() {
				initialLen := chainLen()

				payload := []byte(`{"BPM": 42}`)
				req := httptest.NewRequest(http.MethodPost, "/legacy/bpm", bytes.NewReader(payload))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()

				router.ServeHTTP(rec, req)

				Expect(rec.Code).To(Equal(http.StatusCreated))
				Expect(rec.Header().Get("Deprecation")).To(Equal("true"))
				Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))

				var createdBlock domain.Block
				Expect(json.Unmarshal(rec.Body.Bytes(), &createdBlock)).To(Succeed())
				Expect(createdBlock.Transactions).To(HaveLen(1))
				Expect(createdBlock.Transactions[0].From).To(Equal("legacy"))
				Expect(createdBlock.Transactions[0].To).To(Equal("legacy"))
				Expect(createdBlock.Transactions[0].Amount).To(Equal(uint64(42)))
				Expect(createdBlock.Transactions[0].ID).NotTo(BeEmpty())
				Expect(createdBlock.Transactions[0].ID).To(Equal(createdBlock.Transactions[0].CalculateHash()))

				Expect(chainLen()).To(Equal(initialLen + 1))
				Expect(lastBlock().Transactions[0].Amount).To(Equal(uint64(42)))
			})
		})

		Context("when BPM is invalid or body is malformed", func() {
			DescribeTable("returns 400 Bad Request with Deprecation header and leaves chain unchanged",
				func(body string, expectedMsg string) {
					initialLen := chainLen()

					req := httptest.NewRequest(http.MethodPost, "/legacy/bpm", strings.NewReader(body))
					rec := httptest.NewRecorder()

					router.ServeHTTP(rec, req)

					Expect(rec.Code).To(Equal(http.StatusBadRequest))
					Expect(rec.Header().Get("Deprecation")).To(Equal("true"))
					Expect(rec.Body.String()).To(MatchJSON(`{"error": "` + expectedMsg + `"}`))
					Expect(chainLen()).To(Equal(initialLen))
				},
				Entry("BPM is zero", `{"BPM": 0}`, "BPM deve ser maior que zero"),
				Entry("BPM is negative", `{"BPM": -10}`, "BPM deve ser maior que zero"),
				Entry("BPM is wrong type", `{"BPM": "abc"}`, "corpo da requisição inválido"),
				Entry("empty body", "", "corpo da requisição inválido"),
			)
		})
	})

	DescribeTable("unsupported methods on / return 405",
		func(method string) {
			req := httptest.NewRequest(method, "/", nil)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusMethodNotAllowed))
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
		It("falls back to the default mining timeout for a non-positive value", func() {
			svc, err := chain.NewService(memory.New(), engine)
			Expect(err).NotTo(HaveOccurred())
			Expect(NewServer(svc, Config{}).miningTimeout).To(Equal(DefaultMiningTimeout))
			Expect(NewServer(svc, Config{MiningTimeout: -time.Second}).miningTimeout).To(Equal(DefaultMiningTimeout))
			Expect(NewServer(svc, Config{MiningTimeout: time.Second}).miningTimeout).To(Equal(time.Second))
		})

		It("isolates state between two servers in the same process", func() {
			svcA, _ := chain.NewService(memory.New(), engine)
			svcB, _ := chain.NewService(memory.New(), engine)
			a, b := NewServer(svcA, Config{}).Handler(), NewServer(svcB, Config{}).Handler()

			rec := httptest.NewRecorder()
			a.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"Transactions":[{"From":"alice","To":"bob","Amount":1}]}`)))
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
			h := NewServer(brokenChain{}, Config{}).Handler()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			Expect(rec.Code).To(Equal(http.StatusInternalServerError))
			Expect(rec.Body.String()).To(ContainSubstring("read failed"))
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
			hs := NewServer(svc, cfg).HTTPServer(addr)
			DeferCleanup(func() { _ = hs.Close() })
			go func() {
				defer GinkgoRecover()
				_ = hs.ListenAndServe()
			}()
			Eventually(func() error {
				conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
				if err == nil {
					conn.Close()
				}
				return err
			}, 5*time.Second, 20*time.Millisecond).Should(Succeed())
			return addr
		}

		It("sets the service address, header limit and handler", func() {
			svc, _ := chain.NewService(memory.New(), engine)
			hs := NewServer(svc, Config{}).HTTPServer(":1234")
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
			defer resp.Body.Close()
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
			svc, err := chain.NewService(repo, engine, chain.WithGenerator(func(ctx context.Context, c consensus.Consensus, prev *domain.Block, txs []domain.Transaction) (*domain.Block, error) {
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
			hs := NewServer(svc, cfg).HTTPServer(addr)
			DeferCleanup(func() { _ = hs.Close() })
			go func() {
				defer GinkgoRecover()
				_ = hs.ListenAndServe()
			}()
			Eventually(func() error {
				conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
				if err == nil {
					conn.Close()
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
				defer conn.Close()
				_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
				_, err = conn.Read(make([]byte, 1))
				readErr <- err
			}()

			client := &http.Client{Timeout: 20 * time.Second}
			_, postErr := client.Post("http://"+addr+"/", "application/json",
				strings.NewReader(`{"Transactions":[{"From":"alice","To":"bob","Amount":1}]}`))
			Expect(postErr).To(HaveOccurred())
			var netErr net.Error
			Expect(errors.As(postErr, &netErr) && netErr.Timeout()).To(BeFalse(), "reply was not dropped by WriteTimeout")

			var idleErr error
			Eventually(readErr, 10*time.Second).Should(Receive(&idleErr))
			Expect(idleErr).To(MatchError(io.EOF), "idle connection was not closed by ReadTimeout")
		})
	})

	Describe("GET / (handleGetBlockchain)", func() {
		It("returns 200 OK with only the valid genesis block", func() {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(json.Valid(rec.Body.Bytes())).To(BeTrue())
			var bc chainBody
			Expect(json.Unmarshal(rec.Body.Bytes(), &bc)).To(Succeed())
			Expect(bc.Blocks).To(HaveLen(1))
			Expect(bc.Blocks[0].Index).To(Equal(0))
			Expect(bc.Blocks[0].Transactions).To(BeEmpty())
			Expect(bc.Blocks[0].PrevHash).To(BeEmpty())
		})

		It("returns 2 linked blocks after a valid POST", func() {
			post := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"Transactions":[{"From":"alice","To":"bob","Amount":77}]}`))
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
			Expect(bc.Blocks[1].Transactions[0].Amount).To(Equal(uint64(77)))
		})
	})
})
