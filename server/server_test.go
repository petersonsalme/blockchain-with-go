package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/blockchain"
	"github.com/petersonsalme/blockchain-with-go/models"
)

var _ = Describe("Server", func() {
	var router http.Handler

	BeforeEach(func() {
		previousDifficulty, hadDifficulty := os.LookupEnv(models.DifficultyEnvVar)
		Expect(os.Setenv(models.DifficultyEnvVar, "1")).To(Succeed())
		DeferCleanup(func() {
			if hadDifficulty {
				Expect(os.Setenv(models.DifficultyEnvVar, previousDifficulty)).To(Succeed())
			} else {
				Expect(os.Unsetenv(models.DifficultyEnvVar)).To(Succeed())
			}
		})

		blockchainInstance = blockchain.New()
		router = makeMuxRouter()
	})

	Describe("POST / (handleWriteBlock)", func() {
		Context("when receiving a valid block payload", func() {
			It("returns 201 Created, the new block with correct BPM, and appends to the chain", func() {
				initialLen := len(blockchainInstance.Blocks)

				payload := []byte(`{"BPM": 42}`)
				req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()

				router.ServeHTTP(rec, req)

				Expect(rec.Code).To(Equal(http.StatusCreated))
				Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))

				var createdBlock models.Block
				Expect(json.Unmarshal(rec.Body.Bytes(), &createdBlock)).To(Succeed())
				Expect(createdBlock.BPM).To(Equal(42))

				Expect(blockchainInstance.Blocks).To(HaveLen(initialLen + 1))
				Expect(blockchainInstance.LastBlock().BPM).To(Equal(42))
				Expect(blockchainInstance.LastBlock().Hash).To(Equal(createdBlock.Hash))
			})
		})

		Context("when generateBlock produces an invalid block", func() {
			It("returns 422 Unprocessable Entity, error JSON, and leaves the chain unchanged", func() {
				originalGenerateBlock := generateBlock
				DeferCleanup(func() {
					generateBlock = originalGenerateBlock
				})

				generateBlock = func(ctx context.Context, oldBlock *models.Block, BPM int) (*models.Block, error) {
					b, err := originalGenerateBlock(ctx, oldBlock, BPM)
					if err != nil {
						return nil, err
					}
					b.PrevHash = "tampered-invalid-hash"
					return b, nil
				}

				initialLen := len(blockchainInstance.Blocks)

				payload := []byte(`{"BPM": 42}`)
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

				generateBlock = func(ctx context.Context, oldBlock *models.Block, BPM int) (*models.Block, error) {
					return nil, context.DeadlineExceeded
				}

				initialLen := len(blockchainInstance.Blocks)

				payload := []byte(`{"BPM": 42}`)
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

				generateBlock = func(ctx context.Context, oldBlock *models.Block, BPM int) (*models.Block, error) {
					return nil, context.Canceled
				}

				initialLen := len(blockchainInstance.Blocks)

				payload := []byte(`{"BPM": 42}`)
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

				payload := []byte(`{"BPM": 42}`)
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
				Expect(os.Setenv(models.DifficultyEnvVar, "10")).To(Succeed())
				DeferCleanup(func() {
					Expect(os.Unsetenv(MiningTimeoutEnvVar)).To(Succeed())
					Expect(os.Setenv(models.DifficultyEnvVar, "1")).To(Succeed())
				})

				initialLen := len(blockchainInstance.Blocks)

				payload := []byte(`{"BPM": 42}`)
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

	Describe("GET / (handleGetBlockchain)", func() {
		It("returns 200 OK with the blockchain instance", func() {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusOK))
			var bc models.Blockchain
			Expect(json.Unmarshal(rec.Body.Bytes(), &bc)).To(Succeed())
			Expect(bc.Blocks).To(HaveLen(1))
		})
	})
})
