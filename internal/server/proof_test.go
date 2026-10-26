package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
)

type proofBody struct {
	TxID       string `json:"tx_id"`
	BlockIndex int    `json:"block_index"`
	BlockHash  string `json:"block_hash"`
	MerkleRoot string `json:"merkle_root"`
	LeafIndex  int    `json:"leaf_index"`
	LeafHash   string `json:"leaf_hash"`
	Proof      []struct {
		Hash string `json:"hash"`
		Side string `json:"side"`
	} `json:"proof"`
}

var _ = Describe("GET /tx/{id}/proof", func() {
	var (
		router http.Handler
		funds  *bank
		repo   *memory.Repository
	)

	get := func(target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))

		return rec
	}
	mineBlock := func(n int) {
		txs := make([]domain.Transaction, n)
		for i := range txs {
			txs[i] = funds.tx(newWallet().Address(), 42)
		}

		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(payloadOf(txs...))))
		Expect(rec.Code).To(Equal(http.StatusCreated), rec.Body.String())
	}

	BeforeEach(func() {
		funds = newBank()
		repo = memory.New()
		svc, err := funds.service(repo, pow.New(1))
		Expect(err).NotTo(HaveOccurred())
		srv, err := NewServer(svc, Config{})
		Expect(err).NotTo(HaveOccurred())

		router = srv.Handler()

		for _, n := range []int{1, 2, 3, 5} {
			mineBlock(n)
		}
	})

	It("serves a proof that verifies against the block header for every transaction of the chain", func() {
		var chainResp chainBody

		rec := get("/")
		Expect(json.Unmarshal(rec.Body.Bytes(), &chainResp)).To(Succeed())
		Expect(chainResp.Blocks).To(HaveLen(5))

		count := 0

		for _, b := range chainResp.Blocks {
			for i := range b.Transactions {
				tx := &b.Transactions[i]
				rec := get("/tx/" + tx.ID + "/proof")
				Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
				Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))

				var p proofBody
				Expect(json.Unmarshal(rec.Body.Bytes(), &p)).To(Succeed())
				Expect(p.TxID).To(Equal(tx.ID))
				Expect(p.BlockIndex).To(Equal(b.Index))
				Expect(p.BlockHash).To(Equal(b.Hash))
				Expect(p.LeafIndex).To(Equal(i))
				Expect(p.LeafHash).To(Equal(domain.MerkleLeafHash(tx)))
				Expect(p.MerkleRoot).To(Equal(chainResp.Blocks[p.BlockIndex].MerkleRoot))

				steps := make([]domain.MerkleStep, len(p.Proof))
				for j, s := range p.Proof {
					Expect(s.Side).To(Or(Equal("left"), Equal("right")))
					steps[j] = domain.MerkleStep{Hash: s.Hash, Left: s.Side == "left"}
				}

				Expect(domain.VerifyMerkleProof(p.LeafHash, steps, p.MerkleRoot)).To(BeTrue())

				count++
			}
		}

		Expect(count).To(Equal(1 + 1 + 2 + 3 + 5))
	})

	It("renders the proof as [] when the block has one transaction", func() {
		all, _ := repo.All()
		rec := get("/tx/" + all[1].Transactions[0].ID + "/proof")
		Expect(rec.Code).To(Equal(http.StatusOK))

		var raw map[string]json.RawMessage
		Expect(json.Unmarshal(rec.Body.Bytes(), &raw)).To(Succeed())
		Expect(string(raw["proof"])).To(Equal("[]"))
	})

	It("names left and right siblings", func() {
		all, _ := repo.All()
		for i, want := range []string{"right", "left"} {
			rec := get("/tx/" + all[2].Transactions[i].ID + "/proof")

			var p proofBody
			Expect(json.Unmarshal(rec.Body.Bytes(), &p)).To(Succeed())
			Expect(p.Proof).To(HaveLen(1))
			Expect(p.Proof[0].Side).To(Equal(want))
		}
	})

	It("returns 404 tx_not_found for an unknown id and changes nothing", func() {
		before, _ := repo.All()
		rec := get("/tx/" + strings.Repeat("a", 64) + "/proof")
		Expect(rec.Code).To(Equal(http.StatusNotFound))
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))

		var resp errorResponse
		Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
		Expect(resp.Code).To(Equal("tx_not_found"))
		Expect(resp.Error).NotTo(BeEmpty())

		after, _ := repo.All()
		Expect(after).To(Equal(before))
	})

	It("returns 400 invalid_tx_id for a malformed id", func() {
		rec := get("/tx/" + strings.Repeat("A", 64) + "/proof")
		Expect(rec.Code).To(Equal(http.StatusBadRequest))

		var resp errorResponse
		Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())
		Expect(resp.Code).To(Equal("invalid_tx_id"))
	})

	It("returns 500 when the chain cannot be read", func() {
		srv, err := NewServer(brokenChain{}, Config{})
		Expect(err).NotTo(HaveOccurred())

		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/tx/"+strings.Repeat("a", 64)+"/proof", nil))
		Expect(rec.Code).To(Equal(http.StatusInternalServerError))
	})

	It("answers 405 with Allow for other methods", func() {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/tx/"+strings.Repeat("a", 64)+"/proof", nil))
		Expect(rec.Code).To(Equal(http.StatusMethodNotAllowed))
		Expect(rec.Header().Get("Allow")).To(Equal("GET, HEAD"))
	})
})
