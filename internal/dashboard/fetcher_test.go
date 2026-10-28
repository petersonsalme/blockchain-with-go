package dashboard_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/dashboard"
)

const (
	hex64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	addr  = "00" + "0123456789abcdef0123456789abcdef01234567" + "89abcdef"
)

// countingNode is a node that counts and records what it receives.
type countingNode struct {
	*httptest.Server

	hits atomic.Int64
	mu   sync.Mutex
	seen []string
}

func newCountingNode(t *testing.T, h http.HandlerFunc) *countingNode {
	t.Helper()

	n := &countingNode{}
	n.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.hits.Add(1)
		n.mu.Lock()
		n.seen = append(n.seen, r.Method+" "+r.URL.RequestURI())
		n.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(n.Close)

	return n
}

func (n *countingNode) node(t *testing.T) dashboard.Node {
	t.Helper()

	u, err := url.Parse(n.URL)
	if err != nil {
		t.Fatal(err)
	}

	return dashboard.Node{Name: "n", Base: u}
}

func okNode(t *testing.T) *countingNode {
	t.Helper()

	return newCountingNode(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"ok":true}`)) })
}

func TestFetcherAllowlistedRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		req  dashboard.Request
		want string
	}{
		{dashboard.Health(), "/health"},
		{dashboard.Peers(), "/p2p/peers"},
		{dashboard.Mempool(), "/mempool"},
		{dashboard.Metrics(), "/metrics"},
		{dashboard.Blocks(5, 100), "/blocks?from=5&limit=100"},
		{dashboard.Headers(0, 1), "/headers?from=0&limit=1"},
		{dashboard.Block("7"), "/block/7"},
		{dashboard.Block(hex64), "/block/" + hex64},
		{dashboard.Tx(hex64), "/tx/" + hex64},
		{dashboard.TxProof(hex64), "/tx/" + hex64 + "/proof"},
		{dashboard.Contract(hex64), "/contracts/" + hex64},
		{dashboard.Balance(addr), "/balance/" + addr},
	}

	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()

			n := okNode(t)
			f := dashboard.NewFetcher(time.Second, 1<<10)

			body, err := f.Get(context.Background(), n.node(t), tc.req)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}

			if string(body) != `{"ok":true}` {
				t.Errorf("body = %q", body)
			}

			if len(n.seen) != 1 || n.seen[0] != "GET "+tc.want {
				t.Errorf("node saw %v, want [GET %s]", n.seen, tc.want)
			}
		})
	}
}

func TestFetcherRejectsRequestsOutsideAllowlist(t *testing.T) {
	t.Parallel()

	tests := map[string]dashboard.Request{
		"zero value":           {},
		"negative from":        dashboard.Blocks(-1, 10),
		"zero limit":           dashboard.Blocks(0, 0),
		"negative headers":     dashboard.Headers(-1, 10),
		"empty ref":            dashboard.Block(""),
		"traversal ref":        dashboard.Block("../health"),
		"upper hex ref":        dashboard.Block(strings.ToUpper(hex64)),
		"short hex ref":        dashboard.Block(hex64[:63]),
		"long decimal ref":     dashboard.Block(strings.Repeat("1", 65)),
		"query in ref":         dashboard.Block("1?x=2"),
		"short tx id":          dashboard.Tx(hex64[:63]),
		"slash tx id":          dashboard.Tx(hex64[:60] + "/../"),
		"upper tx proof":       dashboard.TxProof(strings.ToUpper(hex64)),
		"non hex contract":     dashboard.Contract(strings.Repeat("g", 64)),
		"short address":        dashboard.Balance(addr[:49]),
		"long address":         dashboard.Balance(addr + "0"),
		"upper address":        dashboard.Balance(strings.ToUpper(addr)),
		"escaped slash addr":   dashboard.Balance(addr[:46] + "%2f.."),
		"empty address":        dashboard.Balance(""),
		"decimal ref overflow": dashboard.Block("99999999999999999999"),
		"19 digit ref":         dashboard.Block(strings.Repeat("9", 19)),
	}

	for name, req := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			n := okNode(t)
			f := dashboard.NewFetcher(time.Second, 1<<10)

			body, err := f.Get(context.Background(), n.node(t), req)
			if err == nil {
				t.Fatalf("Get = %q, want error", body)
			}

			if n.hits.Load() != 0 {
				t.Errorf("node got %d requests, want 0", n.hits.Load())
			}
		})
	}
}

func TestFetcherDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()

	target := okNode(t)
	redirector := newCountingNode(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/health", http.StatusFound)
	})
	f := dashboard.NewFetcher(time.Second, 1<<10)

	_, err := f.Get(context.Background(), redirector.node(t), dashboard.Health())
	if err == nil {
		t.Fatal("Get followed or accepted a redirect")
	}

	if target.hits.Load() != 0 {
		t.Errorf("redirect target got %d requests, want 0", target.hits.Load())
	}
}

func TestFetcherBodyLimit(t *testing.T) {
	t.Parallel()

	n := newCountingNode(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(strings.Repeat("x", 11))) })

	_, err := dashboard.NewFetcher(time.Second, 10).Get(context.Background(), n.node(t), dashboard.Health())
	if err == nil {
		t.Error("11 bytes with limit 10: want error")
	}

	body, err := dashboard.NewFetcher(time.Second, 11).Get(context.Background(), n.node(t), dashboard.Health())
	if err != nil || len(body) != 11 {
		t.Errorf("11 bytes with limit 11: len %d, err %v; want the body", len(body), err)
	}
}

func TestFetcherTimeout(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	n := newCountingNode(t, func(_ http.ResponseWriter, _ *http.Request) { <-release })

	defer close(release)

	start := time.Now()

	_, err := dashboard.NewFetcher(50*time.Millisecond, 1<<10).Get(context.Background(), n.node(t), dashboard.Health())
	if err == nil {
		t.Fatal("slow node: want error")
	}

	if time.Since(start) > 5*time.Second {
		t.Error("Get did not honour the timeout")
	}
}

func TestFetcherNon2xxIsError(t *testing.T) {
	t.Parallel()

	n := newCountingNode(t, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "nope", http.StatusNotFound) })

	_, err := dashboard.NewFetcher(time.Second, 1<<10).Get(context.Background(), n.node(t), dashboard.Health())
	if err == nil {
		t.Fatal("404 from node: want error")
	}
}

func TestFetcherUnreachableNodeIsError(t *testing.T) {
	t.Parallel()

	n := okNode(t)
	node := n.node(t)

	n.Close()

	_, err := dashboard.NewFetcher(time.Second, 1<<10).Get(context.Background(), node, dashboard.Health())
	if err == nil {
		t.Fatal("closed node: want error")
	}
}

func TestDefaultFetcherLimits(t *testing.T) {
	t.Parallel()

	if dashboard.DefaultTimeout != 1500*time.Millisecond || dashboard.DefaultMaxBody != 4<<20 {
		t.Errorf("defaults = %v, %d", dashboard.DefaultTimeout, dashboard.DefaultMaxBody)
	}
}

func TestFetcherAcceptsEighteenDigitHeight(t *testing.T) {
	t.Parallel()

	n := okNode(t)

	_, err := dashboard.NewFetcher(time.Second, 1<<10).Get(context.Background(), n.node(t), dashboard.Block(strings.Repeat("9", 18)))
	if err != nil {
		t.Fatalf("18 digit height: %v", err)
	}
}

func TestFetcherStatusRange(t *testing.T) {
	t.Parallel()

	for status, ok := range map[int]bool{200: true, 299: true, 300: false} {
		n := newCountingNode(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })

		_, err := dashboard.NewFetcher(time.Second, 1<<10).Get(context.Background(), n.node(t), dashboard.Health())
		if (err == nil) != ok {
			t.Errorf("status %d: err = %v, want ok = %v", status, err, ok)
		}
	}
}
