package dashboard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Fetcher limits: below the 2 s poll interval, and room for one /blocks page of 100 blocks.
const (
	DefaultTimeout = 1500 * time.Millisecond
	DefaultMaxBody = 4_194_304 // 4 MiB
)

const (
	maxDecimalDigits = 18 // fits int64
	hexIDLen         = 64
	addressLen       = 50 // 25 bytes in lowercase hex, as pkg/wallet
)

type route int

const (
	routeNone route = iota
	routeHealth
	routePeers
	routeMempool
	routeMetrics
	routeBlocks
	routeHeaders
	routeBlock
	routeTx
	routeTxProof
	routeContract
	routeBalance
)

// Request is one allowlisted read of a node. Build it with the constructors below; the zero
// value and any parameter that fails its pattern are rejected by Fetcher.Get before any I/O.
type Request struct {
	route route
	a, b  string
}

// Health reads GET /health.
func Health() Request { return Request{route: routeHealth} }

// Peers reads GET /p2p/peers.
func Peers() Request { return Request{route: routePeers} }

// Mempool reads GET /mempool.
func Mempool() Request { return Request{route: routeMempool} }

// Metrics reads GET /metrics.
func Metrics() Request { return Request{route: routeMetrics} }

// Blocks reads GET /blocks?from=&limit=.
func Blocks(from, limit int) Request {
	return Request{route: routeBlocks, a: strconv.Itoa(from), b: strconv.Itoa(limit)}
}

// Headers reads GET /headers?from=&limit=.
func Headers(from, limit int) Request {
	return Request{route: routeHeaders, a: strconv.Itoa(from), b: strconv.Itoa(limit)}
}

// Block reads GET /block/{ref}; ref is a decimal height or 64 lowercase hex.
func Block(ref string) Request { return Request{route: routeBlock, a: ref} }

// Tx reads GET /tx/{id}; id is 64 lowercase hex.
func Tx(id string) Request { return Request{route: routeTx, a: id} }

// TxProof reads GET /tx/{id}/proof.
func TxProof(id string) Request { return Request{route: routeTxProof, a: id} }

// Contract reads GET /contracts/{id}.
func Contract(id string) Request { return Request{route: routeContract, a: id} }

// Balance reads GET /balance/{addr}; addr is lowercase hex of the wallet address length.
func Balance(addr string) Request { return Request{route: routeBalance, a: addr} }

// target validates the request against the allowlist and returns its path and raw query.
func (r Request) target() (string, string, error) {
	switch r.route {
	case routeHealth:
		return "/health", "", nil
	case routePeers:
		return "/p2p/peers", "", nil
	case routeMempool:
		return "/mempool", "", nil
	case routeMetrics:
		return "/metrics", "", nil
	case routeBlocks, routeHeaders:
		return r.page()
	case routeBlock:
		if !isDecimal(r.a) && !isLowerHex(r.a, hexIDLen) {
			return "", "", fmt.Errorf("block ref %q is neither a decimal height nor 64 lowercase hex", r.a)
		}

		return "/block/" + url.PathEscape(r.a), "", nil
	case routeTx, routeTxProof, routeContract:
		return r.idPath()
	case routeBalance:
		if !isLowerHex(r.a, addressLen) {
			return "", "", fmt.Errorf("address %q is not %d lowercase hex", r.a, addressLen)
		}

		return "/balance/" + url.PathEscape(r.a), "", nil
	case routeNone:
	}

	return "", "", errors.New("request is not on the allowlist")
}

func (r Request) page() (string, string, error) {
	if !isDecimal(r.a) || !isDecimal(r.b) || r.b == "0" {
		return "", "", fmt.Errorf("from %q and limit %q must be decimal integers, limit above 0", r.a, r.b)
	}

	path := "/blocks"
	if r.route == routeHeaders {
		path = "/headers"
	}

	return path, "from=" + r.a + "&limit=" + r.b, nil
}

func (r Request) idPath() (string, string, error) {
	if !isLowerHex(r.a, hexIDLen) {
		return "", "", fmt.Errorf("id %q is not 64 lowercase hex", r.a)
	}

	id := url.PathEscape(r.a)

	if r.route == routeTx {
		return "/tx/" + id, "", nil
	}

	if r.route == routeTxProof {
		return "/tx/" + id + "/proof", "", nil
	}

	return "/contracts/" + id, "", nil
}

func isDecimal(s string) bool {
	return s != "" && len(s) <= maxDecimalDigits && strings.Trim(s, "0123456789") == ""
}

func isLowerHex(s string, n int) bool {
	return len(s) == n && strings.Trim(s, "0123456789abcdef") == ""
}

// Fetcher reads allowlisted paths of configured nodes. It sends only GET, follows no redirect,
// gives up after its timeout and refuses bodies above its size limit.
type Fetcher struct {
	client  *http.Client
	maxBody int64
}

// NewFetcher returns a Fetcher with the given per-request timeout and body size limit.
func NewFetcher(timeout time.Duration, maxBody int64) *Fetcher {
	return &Fetcher{
		client: &http.Client{
			Timeout:       timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		maxBody: maxBody,
	}
}

// Get reads req from node and returns the body of a 2xx answer. An invalid request is an error
// and sends nothing.
func (f *Fetcher) Get(ctx context.Context, node Node, req Request) ([]byte, error) {
	path, query, err := req.target()
	if err != nil {
		return nil, err
	}

	target := strings.TrimSuffix(node.Base.String(), "/") + path
	if query != "" {
		target += "?" + query
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}

	resp, err := f.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("node %s: %w", node.Name, err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("node %s: GET %s: status %d", node.Name, path, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("node %s: %w", node.Name, err)
	}

	if int64(len(body)) > f.maxBody {
		return nil, fmt.Errorf("node %s: GET %s: body above %d bytes", node.Name, path, f.maxBody)
	}

	return body, nil
}
