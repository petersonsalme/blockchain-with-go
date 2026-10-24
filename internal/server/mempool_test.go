package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/mempool"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
)

func (brokenChain) Submit(domain.Transaction) error { return errors.New("write failed") }
func (brokenChain) Pending() []domain.Transaction   { return nil }
func (brokenChain) MinePending(context.Context) (*domain.Block, error) {
	return nil, errors.New("unused")
}

var _ = Describe("Mempool routes", func() {
	var (
		funds  *bank
		svc    *chain.Service
		cfg    Config
		router http.Handler
	)

	build := func(opts ...chain.Option) {
		var err error
		svc, err = funds.service(memory.New(), pow.NewProofOfWork(1), opts...)
		Expect(err).NotTo(HaveOccurred())
		srv, err := NewServer(svc, cfg)
		Expect(err).NotTo(HaveOccurred())
		router = srv.Handler()
	}

	do := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec
	}

	// txIDs returns the IDs of txs, in order.
	txIDs := func(txs []domain.Transaction) []string {
		ids := make([]string, len(txs))
		for i, tx := range txs {
			ids[i] = tx.ID
		}
		return ids
	}

	// submitByFee admits three transactions with fees 10, 50 and 90, in that order.
	submitByFee := func(to string) (fee90, fee50, fee10 string) {
		txs := []domain.Transaction{funds.tx(to, 90), funds.tx(to, 50), funds.tx(to, 10)}
		for _, tx := range txs {
			Expect(svc.Submit(tx)).To(Succeed())
		}
		return txs[2].ID, txs[1].ID, txs[0].ID
	}

	BeforeEach(func() {
		funds = newBank()
		cfg = Config{}
		build()
	})

	It("GET /mempool on a new node answers 200 with an empty transactions array", func() {
		rec := do(http.MethodGet, "/mempool")

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))
		Expect(rec.Body.String()).To(ContainSubstring(`"transactions": []`))

		var body mempoolResponse
		Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
		Expect(body.Transactions).To(BeEmpty())
	})

	It("GET /mempool answers an empty array, not null, when the chain pool is nil", func() {
		srv, err := NewServer(brokenChain{}, Config{})
		Expect(err).NotTo(HaveOccurred())

		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mempool", nil))

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.String()).To(ContainSubstring(`"transactions": []`))
	})

	It("GET /mempool lists the pool in selection order", func() {
		fee90, fee50, fee10 := submitByFee(newWallet().Address())

		rec := do(http.MethodGet, "/mempool")

		Expect(rec.Code).To(Equal(http.StatusOK))
		var body mempoolResponse
		Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
		Expect(txIDs(body.Transactions)).To(Equal([]string{fee90, fee50, fee10}))
	})

	It("POST /mine mines the pool in selection order and empties it", func() {
		fee90, fee50, fee10 := submitByFee(newWallet().Address())

		rec := do(http.MethodPost, "/mine")

		Expect(rec.Code).To(Equal(http.StatusCreated))
		var block domain.Block
		Expect(json.Unmarshal(rec.Body.Bytes(), &block)).To(Succeed())
		Expect(block.Index).To(Equal(1))
		Expect(txIDs(block.Transactions)).To(Equal([]string{fee90, fee50, fee10}))

		rec = do(http.MethodGet, "/mempool")
		var body mempoolResponse
		Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
		Expect(body.Transactions).To(BeEmpty())
	})

	It("POST /mine on an empty pool mines a block with no transactions", func() {
		rec := do(http.MethodPost, "/mine")

		Expect(rec.Code).To(Equal(http.StatusCreated))
		var block domain.Block
		Expect(json.Unmarshal(rec.Body.Bytes(), &block)).To(Succeed())
		Expect(block.Index).To(Equal(1))
		Expect(block.Transactions).To(BeEmpty())
	})

	Describe("POST /mine errors", func() {
		withGenerator := func(g chain.Generator) { build(chain.WithGenerator(g)) }

		It("maps a deadline to 504", func() {
			withGenerator(func(context.Context, consensus.Consensus, []*domain.Block, []domain.Transaction) (*domain.Block, error) {
				return nil, context.DeadlineExceeded
			})

			Expect(do(http.MethodPost, "/mine").Code).To(Equal(http.StatusGatewayTimeout))
		})

		It("maps a chain conflict to 409 with the chain_conflict code", func() {
			withGenerator(func(context.Context, consensus.Consensus, []*domain.Block, []domain.Transaction) (*domain.Block, error) {
				return nil, storage.ErrConflict
			})

			rec := do(http.MethodPost, "/mine")

			Expect(rec.Code).To(Equal(http.StatusConflict))
			Expect(rec.Body.String()).To(ContainSubstring(`"code": "chain_conflict"`))
		})

		It("maps a nil block without error to 422", func() {
			withGenerator(func(context.Context, consensus.Consensus, []*domain.Block, []domain.Transaction) (*domain.Block, error) {
				return nil, nil
			})

			Expect(do(http.MethodPost, "/mine").Code).To(Equal(http.StatusUnprocessableEntity))
		})

		It("honours MiningTimeout", func() {
			cfg = Config{MiningTimeout: 50 * time.Millisecond}
			withGenerator(func(ctx context.Context, _ consensus.Consensus, _ []*domain.Block, _ []domain.Transaction) (*domain.Block, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			})

			Expect(do(http.MethodPost, "/mine").Code).To(Equal(http.StatusGatewayTimeout))
		})
	})

	It("answers 405 with Allow when the method is wrong", func() {
		rec := do(http.MethodPut, "/mine")
		Expect(rec.Code).To(Equal(http.StatusMethodNotAllowed))
		Expect(rec.Header().Get("Allow")).To(Equal("POST"))

		rec = do(http.MethodPost, "/mempool")
		Expect(rec.Code).To(Equal(http.StatusMethodNotAllowed))
		Expect(rec.Header().Get("Allow")).To(Equal("GET, HEAD"))
	})
})

var _ = DescribeTable("respondWithSubmitError",
	func(err error, status int, code string) {
		rec := httptest.NewRecorder()
		respondWithSubmitError(rec, err)

		Expect(rec.Code).To(Equal(status))
		var body errorResponse
		Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
		Expect(body.Code).To(Equal(code))
		Expect(body.Error).To(Equal(err.Error()))
	},
	Entry("a duplicate pool transaction", mempool.ErrDuplicate, http.StatusConflict, "duplicate_tx"),
	Entry("a pool outpoint conflict", mempool.ErrConflict, http.StatusConflict, "mempool_conflict"),
	Entry("a full pool", mempool.ErrFull, http.StatusConflict, "mempool_full"),
	Entry("a wrapped full pool", fmt.Errorf("submit: %w", mempool.ErrFull), http.StatusConflict, "mempool_full"),
	Entry("insufficient inputs", domain.ErrInsufficientInputs, http.StatusUnprocessableEntity, domain.CodeInsufficientInputs),
	Entry("input missing or spent", domain.ErrInputMissingOrSpent, http.StatusConflict, domain.CodeInputMissingOrSpent),
	Entry("an unknown error", errors.New("boom"), http.StatusInternalServerError, ""),
)
