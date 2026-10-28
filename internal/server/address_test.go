package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
)

var _ = Describe("GET /address/{addr}/txs", func() {
	var (
		funds  *bank
		svc    *chain.Service
		router http.Handler
		dest   string
	)

	get := func(path string) (*httptest.ResponseRecorder, addressTxsResponse) {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

		var body addressTxsResponse
		if rec.Code == http.StatusOK {
			Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
		}

		return rec, body
	}

	// mineTxs mines n blocks of one bank transaction to dest and returns their ids, oldest first.
	mineTxs := func(n int) []string {
		var ids []string

		for range n {
			tx := funds.tx(dest, 10)
			_, err := svc.Mine(context.Background(), []domain.Transaction{tx})
			Expect(err).NotTo(HaveOccurred())

			ids = append(ids, tx.CalculateHash())
		}

		return ids
	}

	itemIDs := func(r addressTxsResponse) []string {
		var ids []string
		for _, t := range r.Txs {
			ids = append(ids, t.Transaction.CalculateHash())
		}

		return ids
	}

	reversed := func(ids []string) []string {
		out := make([]string, len(ids))
		for i, id := range ids {
			out[len(ids)-1-i] = id
		}

		return out
	}

	BeforeEach(func() {
		funds = newBank()
		dest = newWallet().Address()

		var err error

		svc, err = funds.service(memory.New(), pow.NewProofOfWork(1))
		Expect(err).NotTo(HaveOccurred())
		srv, err := NewServer(svc, Config{})
		Expect(err).NotTo(HaveOccurred())

		router = srv.Handler()
	})

	It("answers 200 with an empty array and no next for an address without history", func() {
		rec, body := get("/address/" + dest + "/txs")

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))
		Expect(rec.Body.String()).To(ContainSubstring(`"txs": []`))
		Expect(rec.Body.String()).NotTo(ContainSubstring(`"next"`))
		Expect(body.Address).To(Equal(dest))
		Expect(body.Limit).To(Equal(25))
		Expect(body.PrunedBelow).To(BeZero())
	})

	It("lists one page newest first with the block of each transaction", func() {
		ids := mineTxs(3)
		rec, body := get("/address/" + dest + "/txs")

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(itemIDs(body)).To(Equal(reversed(ids)))
		Expect(body.Next).To(BeNil())

		tip, err := svc.Tip()
		Expect(err).NotTo(HaveOccurred())
		Expect(body.Txs[0].Height).To(Equal(tip.Index))
		Expect(body.Txs[0].BlockHash).To(Equal(tip.Hash))
		Expect(body.Txs[0].Timestamp).To(BeTemporally("==", tip.Timestamp))
	})

	It("pages with next until it is absent, listing every transaction once", func() {
		ids := mineTxs(5)

		var (
			got  []string
			path = "/address/" + dest + "/txs?limit=2"
		)

		pages := 0

		for range 10 {
			rec, body := get(path)
			Expect(rec.Code).To(Equal(http.StatusOK))
			Expect(body.Limit).To(Equal(2))

			got = append(got, itemIDs(body)...)
			pages++

			if body.Next == nil {
				break
			}

			Expect(*body.Next).To(Equal(itemIDs(body)[len(body.Txs)-1]))

			path = fmt.Sprintf("/address/%s/txs?limit=2&after=%s", dest, *body.Next)
		}

		Expect(pages).To(Equal(3))
		Expect(got).To(Equal(reversed(ids)))
	})

	It("sets next only while older transactions remain, also at an exact multiple", func() {
		mineTxs(4)

		_, body := get("/address/" + dest + "/txs?limit=4")
		Expect(body.Txs).To(HaveLen(4))
		Expect(body.Next).To(BeNil())

		_, body = get("/address/" + dest + "/txs?limit=3")
		Expect(body.Txs).To(HaveLen(3))
		Expect(body.Next).NotTo(BeNil())
	})

	It("defaults the limit to 25 and clamps it to 100", func() {
		txs := make([]domain.Transaction, 26)
		for i := range txs {
			txs[i] = funds.tx(dest, 1)
		}

		_, err := svc.Mine(context.Background(), txs)
		Expect(err).NotTo(HaveOccurred())

		_, body := get("/address/" + dest + "/txs")
		Expect(body.Limit).To(Equal(25))
		Expect(body.Txs).To(HaveLen(25))
		Expect(body.Next).NotTo(BeNil())

		_, body = get("/address/" + dest + "/txs?limit=1000")
		Expect(body.Limit).To(Equal(100))
		Expect(body.Txs).To(HaveLen(26))
		Expect(body.Next).To(BeNil())
	})

	It("lists the spending transaction in the sender's history", func() {
		ids := mineTxs(1)
		_, body := get("/address/" + funds.owner.Address() + "/txs")

		Expect(itemIDs(body)).To(ContainElement(ids[0]))
		Expect(body.Txs[len(body.Txs)-1].Height).To(BeZero()) // the genesis allocation
	})

	It("keeps the cursor valid when a block lands between two page reads", func() {
		ids := mineTxs(3)
		_, first := get("/address/" + dest + "/txs?limit=2")
		Expect(first.Next).NotTo(BeNil())

		fresh := mineTxs(1)

		_, rest := get("/address/" + dest + "/txs?after=" + *first.Next)
		Expect(append(itemIDs(first), itemIDs(rest)...)).To(Equal(reversed(ids)))

		_, top := get("/address/" + dest + "/txs?limit=1")
		Expect(itemIDs(top)).To(Equal(fresh))
	})

	It("answers 400 invalid_address for a bad address", func() {
		rec, _ := get("/address/bad/txs")

		Expect(rec.Code).To(Equal(http.StatusBadRequest))
		Expect(rec.Body.String()).To(ContainSubstring(invalidAddressCode))
	})

	DescribeTable("answers 400 invalid_query",
		func(query string) {
			rec, _ := get("/address/" + dest + "/txs?" + query)

			Expect(rec.Code).To(Equal(http.StatusBadRequest))
			Expect(rec.Body.String()).To(ContainSubstring(invalidQueryCode))
		},
		Entry("limit zero", "limit=0"),
		Entry("limit negative", "limit=-1"),
		Entry("limit not a number", "limit=abc"),
		Entry("after not hex", "after=xyz"),
		Entry("after empty", "after="),
		Entry("after uppercase", "after=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"),
	)

	It("answers 422 unknown_cursor for a well-formed id outside the history", func() {
		mineTxs(1)

		rec, _ := get("/address/" + dest + "/txs?after=" + zeroHash)

		Expect(rec.Code).To(Equal(http.StatusUnprocessableEntity))
		Expect(rec.Body.String()).To(ContainSubstring(unknownCursorCode))
	})

	It("answers 500 when the history cannot be read", func() {
		srv, err := NewServer(brokenChain{}, Config{})
		Expect(err).NotTo(HaveOccurred())

		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/address/"+dest+"/txs", nil))

		Expect(rec.Code).To(Equal(http.StatusInternalServerError))
	})
})
