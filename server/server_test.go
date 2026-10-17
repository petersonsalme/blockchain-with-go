package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/blockchain"
	"github.com/petersonsalme/blockchain-with-go/consensus"
	"github.com/petersonsalme/blockchain-with-go/models"
)

// rejectingConsensus seals with inner but never accepts a block.
type rejectingConsensus struct{ inner consensus.Consensus }

func (r rejectingConsensus) Seal(ctx context.Context, b *models.Block) error {
	return r.inner.Seal(ctx, b)
}

func (rejectingConsensus) Verify(prev, b *models.Block) error {
	return errors.New("rejected by fake consensus")
}

var _ = Describe("Server", func() {
	var router http.Handler

	BeforeEach(func() {
		previousEngine := consensusEngine
		DeferCleanup(func() { consensusEngine = previousEngine })
		consensusEngine = consensus.NewProofOfWork(1)

		blockchainInstance = blockchain.New()
		router = makeMuxRouter()
	})

	Describe("POST / (handleWriteBlock)", func() {
		Context("when receiving a valid block payload", func() {
			It("returns 201 Created, the new block with populated transaction IDs, and appends to the chain", func() {
				initialLen := len(blockchainInstance.Blocks)

				payload := []byte(`{"Transactions":[{"From":"alice","To":"bob","Amount":42,"Fee":1,"Nonce":0}]}`)
				req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()

				router.ServeHTTP(rec, req)

				Expect(rec.Code).To(Equal(http.StatusCreated))
				Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))

				var createdBlock models.Block
				Expect(json.Unmarshal(rec.Body.Bytes(), &createdBlock)).To(Succeed())
				Expect(createdBlock.Transactions).To(HaveLen(1))
				Expect(createdBlock.Transactions[0].From).To(Equal("alice"))
				Expect(createdBlock.Transactions[0].To).To(Equal("bob"))
				Expect(createdBlock.Transactions[0].Amount).To(Equal(uint64(42)))
				Expect(createdBlock.Transactions[0].ID).NotTo(BeEmpty())
				Expect(createdBlock.Transactions[0].ID).To(Equal(createdBlock.Transactions[0].CalculateHash()))
				Expect(createdBlock.Transactions[0].Timestamp.IsZero()).To(BeFalse())

				Expect(blockchainInstance.Blocks).To(HaveLen(initialLen + 1))
				Expect(blockchainInstance.LastBlock().Transactions).To(HaveLen(1))
				Expect(blockchainInstance.LastBlock().Transactions[0].ID).To(Equal(createdBlock.Transactions[0].ID))
				Expect(blockchainInstance.LastBlock().Hash).To(Equal(createdBlock.Hash))
			})
		})

		Context("when generateBlock produces an invalid block", func() {
			It("returns 422 Unprocessable Entity, error JSON, and leaves the chain unchanged", func() {
				originalGenerateBlock := generateBlock
				DeferCleanup(func() {
					generateBlock = originalGenerateBlock
				})

				generateBlock = func(ctx context.Context, c consensus.Consensus, oldBlock *models.Block, txs []models.Transaction) (*models.Block, error) {
					b, err := originalGenerateBlock(ctx, c, oldBlock, txs)
					if err != nil {
						return nil, err
					}
					b.PrevHash = "tampered-invalid-hash"
					return b, nil
				}

				initialLen := len(blockchainInstance.Blocks)

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

				Expect(blockchainInstance.Blocks).To(HaveLen(initialLen))
			})
		})

		Context("when mining times out (deadline exceeded)", func() {
			It("returns 504 Gateway Timeout, error JSON, and leaves the chain unchanged", func() {
				originalGenerateBlock := generateBlock
				DeferCleanup(func() {
					generateBlock = originalGenerateBlock
				})

				generateBlock = func(ctx context.Context, c consensus.Consensus, oldBlock *models.Block, txs []models.Transaction) (*models.Block, error) {
					return nil, context.DeadlineExceeded
				}

				initialLen := len(blockchainInstance.Blocks)

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

				Expect(blockchainInstance.Blocks).To(HaveLen(initialLen))
			})
		})

		Context("when mining context is canceled", func() {
			It("returns 408 Request Timeout, error JSON, and leaves the chain unchanged", func() {
				originalGenerateBlock := generateBlock
				DeferCleanup(func() {
					generateBlock = originalGenerateBlock
				})

				generateBlock = func(ctx context.Context, c consensus.Consensus, oldBlock *models.Block, txs []models.Transaction) (*models.Block, error) {
					return nil, context.Canceled
				}

				initialLen := len(blockchainInstance.Blocks)

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

				Expect(blockchainInstance.Blocks).To(HaveLen(initialLen))
			})
		})

		Context("when client cancels the request context directly", func() {
			It("returns 408 Request Timeout when request context is canceled", func() {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()

				initialLen := len(blockchainInstance.Blocks)

				payload := []byte(`{"Transactions":[{"From":"alice","To":"bob","Amount":42}]}`)
				req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload)).WithContext(ctx)
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()

				router.ServeHTTP(rec, req)

				Expect(rec.Code).To(Equal(http.StatusRequestTimeout))
				Expect(blockchainInstance.Blocks).To(HaveLen(initialLen))
			})
		})

		Context("when MINING_TIMEOUT environment variable is configured", func() {
			It("aborts mining when timeout expires with real mining", func() {
				Expect(os.Setenv(MiningTimeoutEnvVar, "15ms")).To(Succeed())
				consensusEngine = consensus.NewProofOfWork(10)
				DeferCleanup(func() {
					Expect(os.Unsetenv(MiningTimeoutEnvVar)).To(Succeed())
				})

				initialLen := len(blockchainInstance.Blocks)

				payload := []byte(`{"Transactions":[{"From":"alice","To":"bob","Amount":42}]}`)
				req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()

				router.ServeHTTP(rec, req)

				Expect(rec.Code).To(Equal(http.StatusGatewayTimeout))
				Expect(blockchainInstance.Blocks).To(HaveLen(initialLen))
			})
		})

		Context("when receiving malformed JSON", func() {
			It("returns 400 Bad Request with an error message and json content-type", func() {
				initialLen := len(blockchainInstance.Blocks)

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

				Expect(blockchainInstance.Blocks).To(HaveLen(initialLen))
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
			GinkgoT().Setenv(models.MaxBlockBytesEnvVar, "100")
			rec := post(payload)

			Expect(rec.Code).To(Equal(http.StatusUnprocessableEntity))
			var body map[string]any
			Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
			Expect(body).To(HaveKey("error"))
			Expect(body["code"]).To(Equal("block_too_large"))
			Expect(body["details"]).To(HaveKey("size"))
			Expect(body["details"]).To(HaveKeyWithValue("max", BeNumerically("==", 100)))
			Expect(blockchainInstance.Blocks).To(HaveLen(1))
		})

		It("returns 422 with the code of a timestamp violation after mining", func() {
			original := generateBlock
			DeferCleanup(func() { generateBlock = original })
			generateBlock = func(ctx context.Context, c consensus.Consensus, oldBlock *models.Block, txs []models.Transaction) (*models.Block, error) {
				b, err := original(ctx, c, oldBlock, txs)
				if err == nil {
					b.Timestamp = oldBlock.Timestamp.Add(-time.Second)
				}
				return b, err
			}
			rec := post(payload)

			Expect(rec.Code).To(Equal(http.StatusUnprocessableEntity))
			var body map[string]any
			Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
			Expect(body["code"]).To(Equal("timestamp_before_parent"))
			Expect(blockchainInstance.Blocks).To(HaveLen(1))
		})

		It("returns 422 timestamp_too_far_in_future for a far-future block", func() {
			original := generateBlock
			DeferCleanup(func() { generateBlock = original })
			generateBlock = func(ctx context.Context, c consensus.Consensus, oldBlock *models.Block, txs []models.Transaction) (*models.Block, error) {
				b, err := original(ctx, c, oldBlock, txs)
				if err == nil {
					b.Timestamp = time.Now().Add(3 * time.Hour)
				}
				return b, err
			}
			rec := post(payload)

			Expect(rec.Code).To(Equal(http.StatusUnprocessableEntity))
			var body map[string]any
			Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
			Expect(body["code"]).To(Equal("timestamp_too_far_in_future"))
		})

		It("returns 413 request_too_large for a giant body", func() {
			GinkgoT().Setenv(models.MaxBlockBytesEnvVar, "100")
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
				initialLen := len(blockchainInstance.Blocks)

				req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
				rec := httptest.NewRecorder()

				router.ServeHTTP(rec, req)

				Expect(rec.Code).To(Equal(http.StatusBadRequest))
				Expect(rec.Body.String()).To(MatchJSON(`{"error": "` + expectedMsg + `"}`))
				Expect(blockchainInstance.Blocks).To(HaveLen(initialLen))
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
			consensusEngine = rejectingConsensus{inner: consensusEngine}
			initialLen := len(blockchainInstance.Blocks)

			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"Transactions":[{"From":"alice","To":"bob","Amount":42}]}`))
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusUnprocessableEntity))
			Expect(rec.Body.String()).To(MatchJSON(`{"error": "bloco inválido não pôde ser adicionado"}`))
			Expect(blockchainInstance.Blocks).To(HaveLen(initialLen))
		})

		It("returns 500 with the error message on a generic generator error", func() {
			originalGenerateBlock := generateBlock
			DeferCleanup(func() { generateBlock = originalGenerateBlock })
			generateBlock = func(ctx context.Context, c consensus.Consensus, oldBlock *models.Block, txs []models.Transaction) (*models.Block, error) {
				return nil, errors.New("boom")
			}
			initialLen := len(blockchainInstance.Blocks)

			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"Transactions":[{"From":"alice","To":"bob","Amount":42}]}`))
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusInternalServerError))
			Expect(rec.Body.String()).To(MatchJSON(`{"error": "boom"}`))
			Expect(blockchainInstance.Blocks).To(HaveLen(initialLen))
		})
	})

	Describe("POST /legacy/bpm", func() {
		Context("when receiving a valid BPM payload", func() {
			It("returns 201 Created with Deprecation header and creates block with legacy transaction", func() {
				initialLen := len(blockchainInstance.Blocks)

				payload := []byte(`{"BPM": 42}`)
				req := httptest.NewRequest(http.MethodPost, "/legacy/bpm", bytes.NewReader(payload))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()

				router.ServeHTTP(rec, req)

				Expect(rec.Code).To(Equal(http.StatusCreated))
				Expect(rec.Header().Get("Deprecation")).To(Equal("true"))
				Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))

				var createdBlock models.Block
				Expect(json.Unmarshal(rec.Body.Bytes(), &createdBlock)).To(Succeed())
				Expect(createdBlock.Transactions).To(HaveLen(1))
				Expect(createdBlock.Transactions[0].From).To(Equal("legacy"))
				Expect(createdBlock.Transactions[0].To).To(Equal("legacy"))
				Expect(createdBlock.Transactions[0].Amount).To(Equal(uint64(42)))
				Expect(createdBlock.Transactions[0].ID).NotTo(BeEmpty())
				Expect(createdBlock.Transactions[0].ID).To(Equal(createdBlock.Transactions[0].CalculateHash()))

				Expect(blockchainInstance.Blocks).To(HaveLen(initialLen + 1))
				Expect(blockchainInstance.LastBlock().Transactions[0].Amount).To(Equal(uint64(42)))
			})
		})

		Context("when BPM is invalid or body is malformed", func() {
			DescribeTable("returns 400 Bad Request with Deprecation header and leaves chain unchanged",
				func(body string, expectedMsg string) {
					initialLen := len(blockchainInstance.Blocks)

					req := httptest.NewRequest(http.MethodPost, "/legacy/bpm", strings.NewReader(body))
					rec := httptest.NewRecorder()

					router.ServeHTTP(rec, req)

					Expect(rec.Code).To(Equal(http.StatusBadRequest))
					Expect(rec.Header().Get("Deprecation")).To(Equal("true"))
					Expect(rec.Body.String()).To(MatchJSON(`{"error": "` + expectedMsg + `"}`))
					Expect(blockchainInstance.Blocks).To(HaveLen(initialLen))
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

	Describe("defaultConsensus", func() {
		It("returns proof of work using POW_DIFFICULTY", func() {
			previous, hadPrevious := os.LookupEnv(consensus.DifficultyEnvVar)
			Expect(os.Setenv(consensus.DifficultyEnvVar, "3")).To(Succeed())
			DeferCleanup(func() {
				if hadPrevious {
					Expect(os.Setenv(consensus.DifficultyEnvVar, previous)).To(Succeed())
				} else {
					Expect(os.Unsetenv(consensus.DifficultyEnvVar)).To(Succeed())
				}
			})

			pow, ok := defaultConsensus().(*consensus.ProofOfWork)
			Expect(ok).To(BeTrue())
			Expect(pow.Difficulty).To(Equal(3))
		})

		It("is installed by Run", func() {
			for key, value := range map[string]string{"PORT": "not-a-port", consensus.DifficultyEnvVar: "3"} {
				previous, hadPrevious := os.LookupEnv(key)
				Expect(os.Setenv(key, value)).To(Succeed())
				DeferCleanup(func() {
					if hadPrevious {
						Expect(os.Setenv(key, previous)).To(Succeed())
					} else {
						Expect(os.Unsetenv(key)).To(Succeed())
					}
				})
			}

			// An invalid PORT makes ListenAndServe fail at once, after the engine is set.
			Expect(Run()).To(HaveOccurred())

			pow, ok := consensusEngine.(*consensus.ProofOfWork)
			Expect(ok).To(BeTrue())
			Expect(pow.Difficulty).To(Equal(3))
		})
	})

	Describe("GET / (handleGetBlockchain)", func() {
		It("returns 200 OK with only the valid genesis block", func() {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(json.Valid(rec.Body.Bytes())).To(BeTrue())
			var bc models.Blockchain
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
			var bc models.Blockchain
			Expect(json.Unmarshal(rec.Body.Bytes(), &bc)).To(Succeed())
			Expect(bc.Blocks).To(HaveLen(2))
			Expect(bc.Blocks[1].Index).To(Equal(1))
			Expect(bc.Blocks[1].PrevHash).To(Equal(bc.Blocks[0].Hash))
			Expect(bc.Blocks[1].Transactions).To(HaveLen(1))
			Expect(bc.Blocks[1].Transactions[0].Amount).To(Equal(uint64(77)))
		})
	})
})
