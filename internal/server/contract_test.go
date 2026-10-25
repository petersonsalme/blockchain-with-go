package server

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/client"
	"github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/p2p"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
	"github.com/petersonsalme/blockchain-with-go/internal/vm"
)

const specPath = "../../openapi.yaml"

// loadSpec loads and validates the OpenAPI document at path.
func loadSpec(path string) (*openapi3.T, error) {
	doc, err := openapi3.NewLoader().LoadFromFile(path)
	if err != nil {
		return nil, err
	}
	return doc, doc.Validate(context.Background())
}

// specPairs lists every declared (operation, status) pair as "METHOD /path STATUS".
func specPairs(doc *openapi3.T) []string {
	var pairs []string
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			for status := range op.Responses.Map() {
				pairs = append(pairs, fmt.Sprintf("%s %s %s", method, path, status))
			}
		}
	}
	sort.Strings(pairs)
	return pairs
}

// contractCase produces one declared (operation, status) pair against Server.Handler().
type contractCase struct {
	method, route string // route is the spec path template
	status        int
	// serve builds the handler and the request (target path and body).
	serve func() (h http.Handler, target, body string)
	// warmup requests are sent first and discarded, to spend rate-limit tokens.
	warmup int
	// cancelAfter, when positive, cancels the request context that long after it starts:
	// a streaming handler ends then.
	cancelAfter time.Duration
	// specInvalid marks a request that the spec itself rejects (a 4xx for bad input).
	specInvalid bool
}

func (c contractCase) pair() string { return fmt.Sprintf("%s %s %d", c.method, c.route, c.status) }

const (
	zeroHash = "0000000000000000000000000000000000000000000000000000000000000000"
	fixedNow = 1700000000
)

// fakeLocal is the hello the contract tests' P2P double answers.
var fakeLocal = p2p.Hello{Version: 1, NodeID: idA, GenesisHash: zeroHash, Height: 3, TipHash: zeroHash, ListenAddr: "http://10.0.0.1:8080"}

// contractCases returns one case per declared (operation, status) pair.
func contractCases() []contractCase {
	real := func(cfg Config, opts ...chain.Option) (http.Handler, *bank) {
		funds := newBank()
		svc, err := funds.service(memory.New(), pow.NewProofOfWork(1), opts...)
		Expect(err).NotTo(HaveOccurred())
		srv, err := NewServer(svc, cfg)
		Expect(err).NotTo(HaveOccurred())
		return srv.Handler(), funds
	}
	broken := func() http.Handler {
		srv, err := NewServer(brokenChain{}, Config{})
		Expect(err).NotTo(HaveOccurred())
		return srv.Handler()
	}
	plain := func(target string) func() (http.Handler, string, string) {
		return func() (http.Handler, string, string) {
			h, _ := real(Config{})
			return h, target, ""
		}
	}
	brokenAt := func(target string) func() (http.Handler, string, string) {
		return func() (http.Handler, string, string) { return broken(), target, "" }
	}
	limited := func(target string) func() (http.Handler, string, string) {
		return func() (http.Handler, string, string) {
			now := time.Unix(fixedNow, 0)
			h, _ := real(Config{RateLimit: 1, Now: func() time.Time { return now }})
			return h, target, ""
		}
	}
	generator := func(err error) func() (http.Handler, string, string) {
		return func() (http.Handler, string, string) {
			h, funds := real(Config{}, chain.WithGenerator(func(context.Context, consensus.Consensus, []*domain.Block, []domain.Transaction) (*domain.Block, error) {
				return nil, err
			}))
			return h, "/", funds.payload()
		}
	}
	mineWith := func(block *domain.Block, err error) func() (http.Handler, string, string) {
		return func() (http.Handler, string, string) {
			h, _ := real(Config{}, chain.WithGenerator(func(context.Context, consensus.Consensus, []*domain.Block, []domain.Transaction) (*domain.Block, error) {
				return block, err
			}))
			return h, "/mine", ""
		}
	}
	post := func(body func(*bank) string, opts ...chain.Option) func() (http.Handler, string, string) {
		return func() (http.Handler, string, string) {
			h, funds := real(Config{}, opts...)
			return h, "/", body(funds)
		}
	}
	limitedPost := func(target string) func() (http.Handler, string, string) {
		return func() (http.Handler, string, string) {
			h, _, _ := limited(target)()
			return h, target, "{}"
		}
	}

	// p2pPost serves cfg over a real chain and sends a valid POST /p2p/hello body.
	p2pPost := func(cfg Config) func() (http.Handler, string, string) {
		return func() (http.Handler, string, string) {
			h, _ := real(cfg)
			return h, "/p2p/hello", helloBody(idB, 7)
		}
	}

	cases := []contractCase{
		{method: "GET", route: "/", status: 200, serve: plain("/")},
		{method: "GET", route: "/", status: 429, serve: limited("/"), warmup: 1},
		{method: "GET", route: "/", status: 500, serve: brokenAt("/")},

		{method: "POST", route: "/", status: 201, serve: post((*bank).payload)},
		{method: "POST", route: "/", status: 400, specInvalid: true, serve: post(func(*bank) string { return `{"Transactions":[]}` })},
		{method: "POST", route: "/", status: 408, serve: generator(context.Canceled)},
		{method: "POST", route: "/", status: 409, serve: generator(storage.ErrConflict)},
		{method: "POST", route: "/", status: 413, serve: post(func(b *bank) string {
			return strings.TrimSuffix(b.payload(), "}") + `,"Pad":"` + strings.Repeat("a", int(maxRequestBodyBytes())) + `"}`
		})},
		{method: "POST", route: "/", status: 422, serve: post(func(b *bank) string {
			return payloadOf(b.tx(newWallet().Address(), bankAmount+1))
		})},
		{method: "POST", route: "/", status: 429, serve: func() (http.Handler, string, string) {
			now := time.Unix(fixedNow, 0)
			h, funds := real(Config{RateLimit: 1, Now: func() time.Time { return now }})
			return h, "/", funds.payload()
		}, warmup: 1},
		{method: "POST", route: "/", status: 500, serve: generator(errors.New("boom"))},
		{method: "POST", route: "/", status: 504, serve: generator(context.DeadlineExceeded)},

		{method: "GET", route: "/blocks", status: 200, serve: plain("/blocks?from=0&limit=1")},
		{method: "GET", route: "/blocks", status: 400, specInvalid: true, serve: plain("/blocks?from=abc")},
		{method: "GET", route: "/blocks", status: 429, serve: limited("/blocks"), warmup: 1},
		{method: "GET", route: "/blocks", status: 500, serve: brokenAt("/blocks")},

		{method: "GET", route: "/block/{ref}", status: 200, serve: plain("/block/0")},
		{method: "GET", route: "/block/{ref}", status: 400, specInvalid: true, serve: plain("/block/not-a-ref")},
		{method: "GET", route: "/block/{ref}", status: 404, serve: plain("/block/999")},
		{method: "GET", route: "/block/{ref}", status: 429, serve: limited("/block/0"), warmup: 1},
		{method: "GET", route: "/block/{ref}", status: 500, serve: brokenAt("/block/0")},

		{method: "GET", route: "/tx/{id}", status: 200, serve: func() (http.Handler, string, string) {
			h, funds := real(Config{})
			return h, "/tx/" + funds.txid, ""
		}},
		{method: "GET", route: "/tx/{id}", status: 400, specInvalid: true, serve: plain("/tx/xyz")},
		{method: "GET", route: "/tx/{id}", status: 404, serve: plain("/tx/" + zeroHash)},
		{method: "GET", route: "/tx/{id}", status: 429, serve: limited("/tx/" + zeroHash), warmup: 1},
		{method: "GET", route: "/tx/{id}", status: 500, serve: brokenAt("/tx/" + zeroHash)},

		{method: "GET", route: "/tx/{id}/proof", status: 200, serve: func() (http.Handler, string, string) {
			h, funds := real(Config{})
			return h, "/tx/" + funds.txid + "/proof", ""
		}},
		{method: "GET", route: "/tx/{id}/proof", status: 400, specInvalid: true, serve: plain("/tx/xyz/proof")},
		{method: "GET", route: "/tx/{id}/proof", status: 404, serve: plain("/tx/" + zeroHash + "/proof")},
		{method: "GET", route: "/tx/{id}/proof", status: 429, serve: limited("/tx/" + zeroHash + "/proof"), warmup: 1},
		{method: "GET", route: "/tx/{id}/proof", status: 500, serve: brokenAt("/tx/" + zeroHash + "/proof")},

		{method: "GET", route: "/balance/{addr}", status: 200, serve: func() (http.Handler, string, string) {
			h, funds := real(Config{})
			return h, "/balance/" + funds.owner.Address(), ""
		}},
		{method: "GET", route: "/balance/{addr}", status: 400, serve: plain("/balance/bad")},
		{method: "GET", route: "/balance/{addr}", status: 429, serve: limited("/balance/bad"), warmup: 1},

		{method: "GET", route: "/contracts/{id}", status: 200, serve: func() (http.Handler, string, string) {
			h, funds := real(Config{})
			id := deployCounter(h, funds)
			return h, "/contracts/" + id, ""
		}},
		{method: "GET", route: "/contracts/{id}", status: 400, serve: plain("/contracts/xyz")},
		{method: "GET", route: "/contracts/{id}", status: 404, serve: plain("/contracts/" + zeroHash)},
		{method: "GET", route: "/contracts/{id}", status: 429, serve: limited("/contracts/" + zeroHash), warmup: 1},

		{method: "GET", route: "/health", status: 200, serve: plain("/health")},
		{method: "GET", route: "/health", status: 429, serve: limited("/health"), warmup: 1},
		{method: "GET", route: "/health", status: 503, serve: brokenAt("/health")},

		{method: "GET", route: "/metrics", status: 200, serve: plain("/metrics")},
		{method: "GET", route: "/metrics", status: 429, serve: limited("/metrics"), warmup: 1},

		{method: "POST", route: "/legacy/bpm", status: 410, serve: func() (http.Handler, string, string) {
			h, _ := real(Config{})
			return h, "/legacy/bpm", `{"BPM":70}`
		}},
		{method: "POST", route: "/legacy/bpm", status: 429, serve: limitedPost("/legacy/bpm"), warmup: 1},

		{method: "POST", route: "/tx", status: 202, serve: func() (http.Handler, string, string) {
			h, funds := real(Config{})
			return h, "/tx", txBody(funds.tx(newWallet().Address(), 42))
		}},
		{method: "POST", route: "/tx", status: 400, specInvalid: true, serve: func() (http.Handler, string, string) {
			h, _ := real(Config{})
			return h, "/tx", "{}"
		}},
		{method: "POST", route: "/tx", status: 409, serve: func() (http.Handler, string, string) {
			h, funds := real(Config{})
			return h, "/tx", txBody(funds.tx(newWallet().Address(), 42))
		}, warmup: 1},
		{method: "POST", route: "/tx", status: 413, serve: func() (http.Handler, string, string) {
			h, funds := real(Config{})
			return h, "/tx", strings.TrimSuffix(txBody(funds.tx(newWallet().Address(), 42)), "}") + `,"Pad":"` + strings.Repeat("a", int(maxRequestBodyBytes())) + `"}`
		}},
		{method: "POST", route: "/tx", status: 422, serve: func() (http.Handler, string, string) {
			h, funds := real(Config{})
			return h, "/tx", txBody(funds.tx(newWallet().Address(), bankAmount+1))
		}},
		{method: "POST", route: "/tx", status: 429, serve: func() (http.Handler, string, string) {
			now := time.Unix(fixedNow, 0)
			h, funds := real(Config{RateLimit: 1, Now: func() time.Time { return now }})
			return h, "/tx", txBody(funds.tx(newWallet().Address(), 42))
		}, warmup: 1},
		{method: "POST", route: "/tx", status: 500, serve: func() (http.Handler, string, string) {
			return broken(), "/tx", txBody(newBank().tx(newWallet().Address(), 42))
		}},

		{method: "POST", route: "/mine", status: 201, serve: plain("/mine")},
		{method: "POST", route: "/mine", status: 408, serve: mineWith(nil, context.Canceled)},
		{method: "POST", route: "/mine", status: 409, serve: mineWith(nil, storage.ErrConflict)},
		{method: "POST", route: "/mine", status: 422, serve: mineWith(nil, nil)},
		{method: "POST", route: "/mine", status: 429, serve: limited("/mine"), warmup: 1},
		{method: "POST", route: "/mine", status: 500, serve: mineWith(nil, errors.New("boom"))},
		{method: "POST", route: "/mine", status: 504, serve: mineWith(nil, context.DeadlineExceeded)},

		{method: "GET", route: "/events", status: 200, serve: plain("/events"), cancelAfter: 50 * time.Millisecond},
		{method: "GET", route: "/events", status: 429, serve: limited("/events"), warmup: 1},

		{method: "GET", route: "/mempool", status: 200, serve: plain("/mempool")},
		{method: "GET", route: "/mempool", status: 429, serve: limited("/mempool"), warmup: 1},

		{method: "POST", route: "/p2p/hello", status: 200, serve: p2pPost(Config{P2P: &fakePeers{local: fakeLocal}})},
		{method: "POST", route: "/p2p/hello", status: 400, serve: p2pPost(Config{P2P: &fakePeers{err: p2p.ErrBadHello}})},
		{method: "POST", route: "/p2p/hello", status: 404, serve: p2pPost(Config{})},
		{method: "POST", route: "/p2p/hello", status: 409, serve: p2pPost(Config{P2P: &fakePeers{err: p2p.ErrSelf}})},
		{method: "POST", route: "/p2p/hello", status: 413, serve: func() (http.Handler, string, string) {
			h, target, body := p2pPost(Config{P2P: &fakePeers{}})()
			return h, target, strings.TrimSuffix(body, "}") + `,"pad":"` + strings.Repeat("a", int(maxRequestBodyBytes())) + `"}`
		}},
		{method: "POST", route: "/p2p/hello", status: 429, serve: p2pPost(Config{P2P: &fakePeers{}, RateLimit: 1, Now: func() time.Time { return time.Unix(fixedNow, 0) }}), warmup: 1},
		{method: "POST", route: "/p2p/hello", status: 500, serve: p2pPost(Config{P2P: &fakePeers{err: errors.New("boom")}})},
		{method: "POST", route: "/p2p/hello", status: 503, serve: p2pPost(Config{P2P: &fakePeers{err: p2p.ErrPeerLimit}})},

		{method: "GET", route: "/p2p/peers", status: 200, serve: func() (http.Handler, string, string) {
			h, _, _ := p2pPost(Config{P2P: &fakePeers{
				peers: []p2p.PeerInfo{{NodeID: idB, Addr: "http://10.0.0.2:8080", Height: 7, TipHash: zeroHash, Inbound: true, LastSeen: time.Unix(fixedNow, 0)}},
				addrs: []string{"http://10.0.0.2:8080"},
			}})()
			return h, "/p2p/peers", ""
		}},
		{method: "GET", route: "/p2p/peers", status: 404, serve: func() (http.Handler, string, string) {
			h, _, _ := p2pPost(Config{})()
			return h, "/p2p/peers", ""
		}},
		{method: "GET", route: "/p2p/peers", status: 429, serve: func() (http.Handler, string, string) {
			h, _, _ := p2pPost(Config{P2P: &fakePeers{}, RateLimit: 1, Now: func() time.Time { return time.Unix(fixedNow, 0) }})()
			return h, "/p2p/peers", ""
		}, warmup: 1},
	}
	return cases
}

var _ = Describe("OpenAPI contract", func() {
	var (
		doc    *openapi3.T
		router routers.Router
	)

	BeforeEach(func() {
		var err error
		doc, err = loadSpec(specPath)
		Expect(err).NotTo(HaveOccurred())
		router, err = legacy.NewRouter(doc)
		Expect(err).NotTo(HaveOccurred())
	})

	Describe("openapi.yaml", func() {
		It("is a valid OpenAPI 3.0 document", func() {
			Expect(doc.OpenAPI).To(HavePrefix("3.0."))
		})

		It("fails validation when a $ref points to a schema that does not exist", func() {
			raw, err := os.ReadFile(specPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(raw)).To(ContainSubstring(`"#/components/schemas/block"`))
			broken := strings.Replace(string(raw), `"#/components/schemas/block"`, `"#/components/schemas/missing"`, 1)
			path := filepath.Join(GinkgoT().TempDir(), "broken.yaml")
			Expect(os.WriteFile(path, []byte(broken), 0o600)).To(Succeed())

			_, err = loadSpec(path)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("missing"))
		})

		It("declares exactly the routes registered in Server.Handler()", func() {
			srv, err := NewServer(brokenChain{}, Config{})
			Expect(err).NotTo(HaveOccurred())
			var routes []string
			for _, rt := range srv.routes() {
				routes = append(routes, strings.Replace(rt.pattern, "{$}", "", 1))
			}
			var declared []string
			for path, item := range doc.Paths.Map() {
				for method := range item.Operations() {
					declared = append(declared, method+" "+path)
				}
			}
			Expect(declared).To(ConsistOf(routes))
		})

		It("marks POST /legacy/bpm as deprecated", func() {
			Expect(doc.Paths.Value("/legacy/bpm").Post.Deprecated).To(BeTrue())
		})
	})

	Describe("coverage of the spec", func() {
		It("has a case for every declared (operation, status) pair and no extra", func() {
			var have []string
			for _, c := range contractCases() {
				have = append(have, c.pair())
			}
			Expect(have).To(ConsistOf(specPairs(doc)))
		})
	})

	Describe("Server.Handler()", func() {
		for _, c := range contractCases() {
			It(c.pair()+" matches the spec", func() {
				h, target, body := c.serve()

				newReq := func() *http.Request {
					req := httptest.NewRequest(c.method, target, strings.NewReader(body))
					if c.cancelAfter > 0 {
						ctx, cancel := context.WithTimeout(req.Context(), c.cancelAfter)
						DeferCleanup(cancel)
						req = req.WithContext(ctx)
					}
					if body != "" {
						req.Header.Set("Content-Type", "application/json")
					}
					return req
				}
				for range c.warmup { // warmups only spend rate-limit tokens: a cancelled context keeps a stream from holding them open
					ctx, cancel := context.WithCancel(context.Background())
					cancel()
					h.ServeHTTP(httptest.NewRecorder(), newReq().WithContext(ctx))
				}

				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, newReq())
				Expect(rec.Code).To(Equal(c.status), rec.Body.String())

				// The handler consumed the body of the request it served; validate a fresh copy.
				req := newReq()
				route, pathParams, err := router.FindRoute(req)
				Expect(err).NotTo(HaveOccurred())
				Expect(route.Path).To(Equal(c.route))
				input := &openapi3filter.RequestValidationInput{Request: req, PathParams: pathParams, Route: route}

				reqErr := openapi3filter.ValidateRequest(context.Background(), input)
				if c.specInvalid {
					Expect(reqErr).To(HaveOccurred(), "the spec should reject this request")
				} else {
					Expect(reqErr).NotTo(HaveOccurred())
				}

				result := rec.Result()
				defer func() { _ = result.Body.Close() }()
				respBody, err := io.ReadAll(result.Body)
				Expect(err).NotTo(HaveOccurred())
				wantType := "application/json"
				if c.route == "/metrics" && c.status == 200 {
					wantType = metricsContentType
				}
				if c.route == "/events" && c.status == 200 {
					wantType = "text/event-stream"
				}
				Expect(result.Header.Get("Content-Type")).To(Equal(wantType))
				Expect(openapi3filter.ValidateResponse(context.Background(), &openapi3filter.ResponseValidationInput{
					RequestValidationInput: input,
					Status:                 result.StatusCode,
					Header:                 result.Header,
					Body:                   io.NopCloser(bytes.NewReader(respBody)),
					// the validator cannot decode a text/event-stream body; status and headers are still checked
					Options: &openapi3filter.Options{ExcludeResponseBody: c.route == "/events" && c.status == 200},
				})).To(Succeed())
			})
		}
	})

	Describe("generated client", func() {
		var (
			ctx   = context.Background()
			funds *bank
			srv   *httptest.Server
			api   *client.ClientWithResponses
		)

		connect := func(h http.Handler) {
			srv = httptest.NewServer(h)
			DeferCleanup(srv.Close)
			var err error
			api, err = client.NewClientWithResponses(srv.URL)
			Expect(err).NotTo(HaveOccurred())
		}

		BeforeEach(func() {
			funds = newBank()
			svc, err := funds.service(memory.New(), pow.NewProofOfWork(1))
			Expect(err).NotTo(HaveOccurred())
			s, err := NewServer(svc, Config{})
			Expect(err).NotTo(HaveOccurred())
			connect(s.Handler())
		})

		It("decodes GET /health into JSON200", func() {
			mined, err := api.WriteBlockWithResponse(ctx, toMessage(funds.tx(newWallet().Address(), 42)))
			Expect(err).NotTo(HaveOccurred())
			Expect(mined.JSON201).NotTo(BeNil(), string(mined.Body))

			resp, err := api.GetHealthWithResponse(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode()).To(Equal(http.StatusOK))
			Expect(resp.JSON200).NotTo(BeNil())
			Expect(resp.JSON200.Status).To(Equal("ok"))
			Expect(resp.JSON200.Height).To(Equal(1))
			Expect(resp.JSON200.Tip).To(Equal(mined.JSON201.Hash))
			Expect(resp.JSON200.Tip).To(HaveLen(64))
		})

		It("decodes GET /health failures into JSON503", func() {
			bad, err := NewServer(brokenChain{}, Config{})
			Expect(err).NotTo(HaveOccurred())
			connect(bad.Handler())
			resp, err := api.GetHealthWithResponse(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.JSON503).NotTo(BeNil())
			Expect(resp.JSON503.Code).To(HaveValue(Equal("unhealthy")))
		})

		It("deploys with POST /tx and decodes GET /contracts/{id} into JSON200, JSON400 and JSON404", func() {
			id := deployCounter(srv.Config.Handler, funds)

			resp, err := api.GetContractWithResponse(ctx, id)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.JSON200).NotTo(BeNil(), string(resp.Body))
			Expect(resp.JSON200.Id).To(Equal(id))
			Expect(resp.JSON200.Code).To(Equal(hex.EncodeToString(vm.CounterCode)))
			Expect(resp.JSON200.Storage).To(BeEmpty())

			bad, err := api.GetContractWithResponse(ctx, "xyz")
			Expect(err).NotTo(HaveOccurred())
			Expect(bad.JSON400).NotTo(BeNil())
			Expect(bad.JSON400.Code).To(HaveValue(Equal("invalid_contract_id")))

			missing, err := api.GetContractWithResponse(ctx, zeroHash)
			Expect(err).NotTo(HaveOccurred())
			Expect(missing.JSON404).NotTo(BeNil())
			Expect(missing.JSON404.Code).To(HaveValue(Equal("contract_not_found")))
		})

		It("decodes GET /block/{ref} into JSON200 and JSON404", func() {
			resp, err := api.GetBlockWithResponse(ctx, "0")
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.JSON200).NotTo(BeNil())
			Expect(resp.JSON200.Index).To(Equal(0))
			Expect(resp.JSON200.Transactions).NotTo(BeNil())
			Expect(*resp.JSON200.Transactions).To(HaveLen(1))

			missing, err := api.GetBlockWithResponse(ctx, "999")
			Expect(err).NotTo(HaveOccurred())
			Expect(missing.JSON404).NotTo(BeNil())
			Expect(missing.JSON404.Code).To(HaveValue(Equal("block_not_found")))
		})

		It("decodes GET /tx/{id}/proof into JSON200 and JSON404", func() {
			resp, err := api.GetTxProofWithResponse(ctx, funds.txid)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode()).To(Equal(http.StatusOK), string(resp.Body))
			Expect(resp.JSON200).NotTo(BeNil())
			Expect(resp.JSON200.TxId).To(Equal(funds.txid))
			Expect(resp.JSON200.MerkleRoot).NotTo(BeEmpty())

			missing, err := api.GetTxProofWithResponse(ctx, zeroHash)
			Expect(err).NotTo(HaveOccurred())
			Expect(missing.JSON404).NotTo(BeNil())
			Expect(missing.JSON404.Code).To(HaveValue(Equal("tx_not_found")))
		})

		It("posts transactions with POST / and decodes JSON201 and JSON422", func() {
			to := newWallet().Address()
			resp, err := api.WriteBlockWithResponse(ctx, toMessage(funds.tx(to, 42)))
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode()).To(Equal(http.StatusCreated), string(resp.Body))
			Expect(resp.JSON201).NotTo(BeNil())
			Expect(resp.JSON201.Index).To(Equal(1))
			Expect(resp.JSON201.Transactions).NotTo(BeNil())
			Expect((*resp.JSON201.Transactions)[0].Outputs[0].Address).To(Equal(to))
			Expect((*resp.JSON201.Transactions)[0].Outputs[0].Amount).To(Equal(uint64(42)))

			over, err := api.WriteBlockWithResponse(ctx, toMessage(funds.tx(to, bankAmount+1)))
			Expect(err).NotTo(HaveOccurred())
			Expect(over.JSON422).NotTo(BeNil())
			Expect(over.JSON422.Code).To(HaveValue(Equal(domain.CodeInsufficientInputs)))
		})

		It("submits with POST /tx, lists GET /mempool and mines with POST /mine", func() {
			tx := funds.tx(newWallet().Address(), 42)
			submitted, err := api.SubmitTxWithResponse(ctx, toClientTx(tx))
			Expect(err).NotTo(HaveOccurred())
			Expect(submitted.JSON202).NotTo(BeNil(), string(submitted.Body))
			Expect(submitted.JSON202.Id).To(Equal(tx.ID))

			again, err := api.SubmitTxWithResponse(ctx, toClientTx(tx))
			Expect(err).NotTo(HaveOccurred())
			Expect(again.JSON409).NotTo(BeNil(), string(again.Body))
			Expect(again.JSON409.Code).To(HaveValue(Equal(duplicateTxCode)))

			pool, err := api.GetMempoolWithResponse(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(pool.JSON200).NotTo(BeNil())
			Expect(pool.JSON200.Transactions).To(HaveLen(1))

			mined, err := api.MinePendingWithResponse(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(mined.JSON201).NotTo(BeNil(), string(mined.Body))
			Expect(mined.JSON201.Index).To(Equal(1))
			Expect(mined.JSON201.Transactions).NotTo(BeNil())
			Expect(*mined.JSON201.Transactions).To(HaveLen(1))
			Expect((*mined.JSON201.Transactions)[0].ID).To(HaveValue(Equal(tx.ID)))

			empty, err := api.GetMempoolWithResponse(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(empty.JSON200).NotTo(BeNil())
			Expect(empty.JSON200.Transactions).To(BeEmpty())
		})
	})
})

// toClientTx converts a domain transaction to the generated client type.
func toClientTx(tx domain.Transaction) client.Transaction {
	ins := make([]client.TxIn, len(tx.Inputs))
	for i, in := range tx.Inputs {
		ins[i] = client.TxIn{PrevTxID: in.PrevTxID, OutIndex: in.OutIndex, PublicKey: in.PublicKey, Signature: in.Signature}
	}
	outs := make([]client.TxOut, len(tx.Outputs))
	for i, out := range tx.Outputs {
		outs[i] = client.TxOut{Amount: out.Amount, Address: out.Address}
	}
	return client.Transaction{ID: &tx.ID, Inputs: &ins, Outputs: outs, Timestamp: tx.Timestamp}
}

// toMessage converts a domain transaction to the generated request body.
func toMessage(tx domain.Transaction) client.WriteBlockJSONRequestBody {
	return client.WriteBlockJSONRequestBody{Transactions: []client.Transaction{toClientTx(tx)}}
}

// deployCounter submits a signed deploy of vm.CounterCode through h, mines it and returns the contract ID.
// toContractClientTx is toClientTx plus the contract object of tx.
func toContractClientTx(tx domain.Transaction) client.Transaction {
	req := toClientTx(tx)
	if c := tx.Contract; c != nil {
		req.Contract = &client.ContractOp{Kind: client.ContractOpKind(c.Kind), Code: &c.Code, Target: &c.Target, Args: &c.Args, GasLimit: c.GasLimit}
	}
	return req
}

func deployCounter(h http.Handler, funds *bank) string {
	tx := funds.tx(newWallet().Address(), 10)
	tx.Contract = &domain.ContractOp{Kind: domain.ContractDeploy, Code: hex.EncodeToString(vm.CounterCode), GasLimit: 100}
	tx.ID = tx.CalculateHash()
	Expect(domain.SignInput(&tx, 0, funds.owner)).To(Succeed())
	body, err := json.Marshal(toContractClientTx(tx))
	Expect(err).NotTo(HaveOccurred())
	for _, step := range []struct{ path, body string }{{"/tx", string(body)}, {"/mine", ""}} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, step.path, strings.NewReader(step.body)))
		Expect(rec.Code).To(BeNumerically("<", 300), rec.Body.String())
	}
	return tx.ID
}
