package p2p

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

var (
	idC = strings.Repeat("c", 32)
	idD = strings.Repeat("d", 32)
)

// chain1 is a chain of the genesis block alone, so its height is 0.
func chain1() fakeChain { return fakeChain{blocks: []*domain.Block{{Index: 0, Hash: gen}}} }

// helloStatus maps the handshake refusals to the HTTP statuses of internal/server.
func helloStatus(err error) int {
	switch {
	case errors.Is(err, ErrBadHello):
		return http.StatusBadRequest
	case errors.Is(err, ErrPeerLimit):
		return http.StatusServiceUnavailable
	default:
		return http.StatusConflict
	}
}

// serve exposes n on the two /p2p routes, as internal/server does, counting the hellos it gets.
func serve(n *Node, hellos *atomic.Int64) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /p2p/hello", func(w http.ResponseWriter, r *http.Request) {
		hellos.Add(1)

		var remote Hello
		if err := json.NewDecoder(r.Body).Decode(&remote); err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}

		local, err := n.Accept(remote)
		if err != nil {
			http.Error(w, err.Error(), helloStatus(err))
			return
		}

		_ = json.NewEncoder(w).Encode(local)
	})
	mux.HandleFunc("GET /p2p/peers", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"peers": []any{}, "addrs": n.Addrs()})
	})

	return mux
}

type testNode struct {
	*Node

	srv    *httptest.Server
	book   *Book
	hellos *atomic.Int64
}

func newTestNode(t *testing.T, c ChainView, id string, l Limits, opts ...Option) *testNode {
	t.Helper()

	tn := &testNode{book: NewBook(l, nil), hellos: &atomic.Int64{}}

	var handler http.Handler

	tn.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	t.Cleanup(tn.srv.Close)

	n, err := NewNode(c, tn.book, id, tn.srv.URL, opts...)
	if err != nil {
		t.Fatal(err)
	}

	tn.Node = n
	handler = serve(n, tn.hellos)

	return tn
}

func peerByID(peers []PeerInfo, id string) (PeerInfo, bool) {
	for _, p := range peers {
		if p.NodeID == id {
			return p, true
		}
	}

	return PeerInfo{}, false
}

func TestRoundConnectsToSeed(t *testing.T) {
	a := newTestNode(t, chain2(), idA, Limits{})
	b := newTestNode(t, chain1(), idB, Limits{})
	b.book.AddAddrs(a.srv.URL)

	b.Round(context.Background())

	pa, ok := peerByID(b.Peers(), idA)
	if !ok || pa.Inbound || pa.Height != 1 || pa.Addr != a.srv.URL {
		t.Fatalf("B peers = %+v", b.Peers())
	}

	pb, ok := peerByID(a.Peers(), idB)
	if !ok || !pb.Inbound || pb.Height != 0 {
		t.Fatalf("A peers = %+v", a.Peers())
	}
}

// stubPeer answers /p2p/hello with the given status and body and /p2p/peers with addrs.
func stubPeer(t *testing.T, status int, helloBody any, addrs []string) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /p2p/hello", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)

		if s, ok := helloBody.(string); ok {
			_, _ = w.Write([]byte(s))
			return
		}

		_ = json.NewEncoder(w).Encode(helloBody)
	})
	mux.HandleFunc("GET /p2p/peers", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"peers": []any{}, "addrs": addrs})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

func TestRoundRefusesIncompatiblePeer(t *testing.T) {
	a := newTestNode(t, chain2(), idA, Limits{}, WithProtocolVersion(2))
	b := newTestNode(t, chain1(), idB, Limits{})
	b.book.AddAddrs(a.srv.URL)

	b.Round(context.Background())
	b.Round(context.Background())

	if len(b.Peers()) != 0 || len(a.Peers()) != 0 {
		t.Fatalf("peers: B=%+v A=%+v", b.Peers(), a.Peers())
	}

	if a.hellos.Load() != 2 {
		t.Fatalf("hellos to A = %d, want one per round", a.hellos.Load())
	}
}

func TestRoundRunsCheckOnTheResponse(t *testing.T) {
	good := func() Hello {
		return Hello{Version: 1, NodeID: idA, GenesisHash: gen, Height: 5, TipHash: tip, ListenAddr: "http://x:1"}
	}

	cases := []struct {
		name   string
		status int
		body   any
		peers  int
	}{
		{"ok", 200, good(), 1},
		{"version", 200, func() Hello { h := good(); h.Version = 2; return h }(), 0},
		{"genesis", 200, func() Hello { h := good(); h.GenesisHash = tip; return h }(), 0},
		{"malformed", 200, func() Hello { h := good(); h.Height = -1; return h }(), 0},
		{"self", 200, func() Hello { h := good(); h.NodeID = idB; return h }(), 0},
		{"garbage", 200, "not json", 0},
		{"trailing", 200, "{}", 0},
		{"conflict", 409, good(), 0},
		{"peer limit", 503, good(), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := stubPeer(t, tc.status, tc.body, nil)
			b := newTestNode(t, chain1(), idB, Limits{})
			b.book.AddAddrs(stub.URL)
			b.Round(context.Background())

			if got := len(b.Peers()); got != tc.peers {
				t.Fatalf("peers = %+v, want %d", b.Peers(), tc.peers)
			}
		})
	}
}

func TestRoundExchangesAddresses(t *testing.T) {
	a := newTestNode(t, chain2(), idA, Limits{})
	b := newTestNode(t, chain2(), idB, Limits{})
	c := newTestNode(t, chain1(), idC, Limits{})

	b.book.AddAddrs(a.srv.URL)
	b.Round(context.Background())
	c.book.AddAddrs(b.srv.URL)

	c.Round(context.Background())

	if !containsStr(c.Addrs(), a.srv.URL) {
		t.Fatalf("C addrs after 1 round = %v", c.Addrs())
	}

	c.Round(context.Background())

	for _, id := range []string{idA, idB} {
		if p, ok := peerByID(c.Peers(), id); !ok || p.Inbound {
			t.Fatalf("C not connected out to %s: %+v", id, c.Peers())
		}
	}

	if containsStr(c.Addrs(), c.srv.URL) {
		t.Fatalf("C learned its own address: %v", c.Addrs())
	}
}

func containsStr(list []string, s string) bool {
	return slices.Contains(list, s)
}

func TestRoundDisconnectsUnresponsiveOutboundPeer(t *testing.T) {
	a := newTestNode(t, chain2(), idA, Limits{})
	b := newTestNode(t, chain1(), idB, Limits{})
	b.book.AddAddrs(a.srv.URL)
	b.Round(context.Background())

	if len(b.Peers()) != 1 {
		t.Fatalf("not connected: %+v", b.Peers())
	}

	a.srv.Close()
	b.Round(context.Background())

	if len(b.Peers()) != 0 {
		t.Fatalf("peers = %+v, want none", b.Peers())
	}

	if !containsStr(b.Addrs(), a.srv.URL) {
		t.Fatal("address of the dead peer was forgotten")
	}
}

func TestRoundDisconnectsPeerThatBecameIncompatible(t *testing.T) {
	var version atomic.Int64
	version.Store(1)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /p2p/hello", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(Hello{Version: int(version.Load()), NodeID: idA, GenesisHash: gen, Height: 1, TipHash: tip})
	})
	mux.HandleFunc("GET /p2p/peers", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"addrs":[]}`)) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	b := newTestNode(t, chain1(), idB, Limits{})
	b.book.AddAddrs(srv.URL)
	b.Round(context.Background())

	if len(b.Peers()) != 1 {
		t.Fatalf("not connected: %+v", b.Peers())
	}

	version.Store(2)
	b.Round(context.Background())

	if len(b.Peers()) != 0 {
		t.Fatalf("peers = %+v, want none", b.Peers())
	}
}

func TestRoundReplacesPeerThatRestartedWithNewNodeID(t *testing.T) {
	var id atomic.Value
	id.Store(idA)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /p2p/hello", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(Hello{Version: 1, NodeID: id.Load().(string), GenesisHash: gen, Height: 1, TipHash: tip})
	})
	mux.HandleFunc("GET /p2p/peers", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"addrs":[]}`)) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	b := newTestNode(t, chain1(), idB, Limits{MaxOutbound: 1})
	b.book.AddAddrs(srv.URL)
	b.Round(context.Background())

	id.Store(idD)
	b.Round(context.Background())

	peers := b.Peers()
	if len(peers) != 1 || peers[0].NodeID != idD || peers[0].Addr != srv.URL {
		t.Fatalf("peers = %+v, want only the new id", peers)
	}
}

func TestRoundRespectsMaxOutbound(t *testing.T) {
	b := newTestNode(t, chain1(), idB, Limits{MaxOutbound: 2})
	ids := []string{idA, idC, idD}

	var nodes []*testNode

	for _, id := range ids {
		n := newTestNode(t, chain2(), id, Limits{})
		nodes = append(nodes, n)
		b.book.AddAddrs(n.srv.URL)
	}

	b.Round(context.Background())
	b.Round(context.Background())

	out := 0

	for _, p := range b.Peers() {
		if !p.Inbound {
			out++
		}
	}

	if out != 2 {
		t.Fatalf("outbound = %d, want 2: %+v", out, b.Peers())
	}
	// the third candidate, in address order, is never dialled
	last := nodes[0]
	for _, n := range nodes {
		if n.srv.URL > last.srv.URL {
			last = n
		}
	}

	if last.hellos.Load() != 0 {
		t.Fatalf("hellos to the third candidate = %d", last.hellos.Load())
	}
}

func TestRoundNeverDialsItself(t *testing.T) {
	stub := stubPeer(t, 200, "{}", nil)
	b := newTestNode(t, chain1(), idB, Limits{})
	b.book.AddAddrs(b.srv.URL, stub.URL)

	b.Round(context.Background())

	if b.hellos.Load() != 0 {
		t.Fatalf("B got %d hellos from itself", b.hellos.Load())
	}
}

func TestRoundDoesNotStoreOwnAddressFromPeers(t *testing.T) {
	b := newTestNode(t, chain1(), idB, Limits{})
	a := stubPeer(t, 200, Hello{Version: 1, NodeID: idA, GenesisHash: gen, Height: 1, TipHash: tip}, []string{b.srv.URL, "http://z:9", "bad"})
	b.book.AddAddrs(a.URL)

	b.Round(context.Background())

	got := b.Addrs()
	if containsStr(got, b.srv.URL) || !containsStr(got, "http://z:9") {
		t.Fatalf("addrs = %v", got)
	}
}

func TestRoundIgnoresBadPeersResponse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"status", 500, `{"addrs":["http://z:9"]}`},
		{"garbage", 200, `nope`},
		{"too large", 200, `{"addrs":["http://z:9"],"pad":"` + strings.Repeat("x", maxResponseBytes) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("POST /p2p/hello", func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(Hello{Version: 1, NodeID: idA, GenesisHash: gen, Height: 1, TipHash: tip})
			})
			mux.HandleFunc("GET /p2p/peers", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			b := newTestNode(t, chain1(), idB, Limits{})
			b.book.AddAddrs(srv.URL)

			b.Round(context.Background())

			if containsStr(b.Addrs(), "http://z:9") || len(b.Peers()) != 1 {
				t.Fatalf("addrs = %v peers = %+v", b.Addrs(), b.Peers())
			}
		})
	}
}

// syncBuf is a goroutine-safe log sink.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.b.String()
}

func TestRoundLogsNetworkFailures(t *testing.T) {
	var buf syncBuf

	b := newTestNode(t, chain1(), idB, Limits{}, WithLogger(slog.New(slog.NewTextHandler(&buf, nil))))
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	b.book.AddAddrs(dead.URL)

	b.Round(context.Background())

	if !strings.Contains(buf.String(), dead.URL) {
		t.Fatalf("log = %q", buf.String())
	}
}

func TestRoundWithBrokenLocalChainDoesNothing(t *testing.T) {
	a := newTestNode(t, chain2(), idA, Limits{})

	var buf syncBuf

	b := newTestNode(t, fakeChain{err: errors.New("boom")}, idB, Limits{}, WithLogger(slog.New(slog.NewTextHandler(&buf, nil))))
	b.book.AddAddrs(a.srv.URL)

	b.Round(context.Background())

	if a.hellos.Load() != 0 || len(b.Peers()) != 0 || !strings.Contains(buf.String(), "boom") {
		t.Fatalf("hellos=%d peers=%+v log=%q", a.hellos.Load(), b.Peers(), buf.String())
	}
}

func TestRoundExpiresPeersNotSeenForThreeIntervals(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	interval := 10 * time.Second
	book := NewBook(Limits{}, clock)

	n, err := NewNode(chain2(), book, idA, "", WithInterval(interval))
	if err != nil {
		t.Fatal(err)
	}

	n.now = clock
	if _, err := n.Accept(hello(idB)); err != nil {
		t.Fatal(err)
	}

	now = now.Add(3 * interval)

	n.Round(context.Background())

	if len(n.Peers()) != 1 {
		t.Fatalf("peer expired after exactly 3 intervals: %+v", n.Peers())
	}

	now = now.Add(time.Nanosecond)

	n.Round(context.Background())

	if len(n.Peers()) != 0 {
		t.Fatalf("peer still listed: %+v", n.Peers())
	}
}

func TestRoundPersistsAddresses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peers.json")
	a := newTestNode(t, chain2(), idA, Limits{})
	b := newTestNode(t, chain1(), idB, Limits{}, WithAddrFile(path))
	b.book.AddAddrs(a.srv.URL)

	b.Round(context.Background())

	got, err := LoadAddrs(path)
	if err != nil || len(got) != 1 || got[0] != a.srv.URL {
		t.Fatalf("saved = %v, %v", got, err)
	}
}

func TestRoundWithoutAddrFileCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	b := newTestNode(t, chain1(), idB, Limits{})
	b.book.AddAddrs("http://z:9")
	b.Round(context.Background())

	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("files created: %v", entries)
	}
}

func TestRoundLogsSaveFailure(t *testing.T) {
	var buf syncBuf

	path := filepath.Join(t.TempDir(), "missing", "peers.json")
	b := newTestNode(t, chain1(), idB, Limits{}, WithAddrFile(path), WithLogger(slog.New(slog.NewTextHandler(&buf, nil))))
	b.Round(context.Background())

	if !strings.Contains(buf.String(), "peers.json") {
		t.Fatalf("log = %q", buf.String())
	}
}

func TestRoundStopsOnCancelledContext(t *testing.T) {
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /p2p/hello", func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	b := newTestNode(t, chain1(), idB, Limits{})
	b.book.AddAddrs(srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() { b.Round(ctx); close(done) }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Round did not return after cancel")
	}
}

func TestRoundSkipsDialingOnceContextIsDone(t *testing.T) {
	a := newTestNode(t, chain2(), idA, Limits{})
	b := newTestNode(t, chain1(), idB, Limits{})
	b.book.AddAddrs(a.srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	b.Round(ctx)

	if a.hellos.Load() != 0 {
		t.Fatalf("hellos = %d", a.hellos.Load())
	}
}

func TestOptionsDefaults(t *testing.T) {
	n, err := NewNode(chain1(), NewBook(Limits{}, nil), idA, "", WithInterval(0), WithClient(nil), WithLogger(nil))
	if err != nil {
		t.Fatal(err)
	}

	if n.interval != 30*time.Second || n.client == nil || n.client.Timeout != 5*time.Second || n.log == nil {
		t.Fatalf("defaults: %v %+v", n.interval, n.client)
	}

	n, _ = NewNode(chain1(), NewBook(Limits{}, nil), idA, "", WithInterval(-1))
	if n.interval != 30*time.Second {
		t.Fatalf("interval = %v", n.interval)
	}

	c := &http.Client{Timeout: time.Second}

	n, _ = NewNode(chain1(), NewBook(Limits{}, nil), idA, "", WithInterval(time.Minute), WithClient(c))
	if n.interval != time.Minute || n.client != c {
		t.Fatal("options not applied")
	}
}

func TestDefaultClientDoesNotFollowRedirects(t *testing.T) {
	var hit atomic.Int64

	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit.Add(1) }))
	t.Cleanup(target.Close)

	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redir.Close)
	b := newTestNode(t, chain1(), idB, Limits{})
	b.book.AddAddrs(redir.URL)

	b.Round(context.Background())

	if hit.Load() != 0 || len(b.Peers()) != 0 {
		t.Fatalf("redirect followed: hits=%d", hit.Load())
	}
}

func TestRunRoundsUntilCancelled(t *testing.T) {
	defer goleak.VerifyNone(t)

	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	a := newTestNode(t, chain2(), idA, Limits{})
	b := newTestNode(t, chain1(), idB, Limits{}, WithInterval(10*time.Millisecond), WithClient(client))
	b.book.AddAddrs(a.srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() { b.Run(ctx); close(done) }()

	deadline := time.Now().Add(3 * time.Second)
	for a.hellos.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	if a.hellos.Load() < 3 {
		t.Fatalf("hellos = %d, want a first round and later ticks", a.hellos.Load())
	}

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}

	a.srv.Close()
	b.srv.Close()
}

func TestRunStartsWithAnImmediateRound(t *testing.T) {
	a := newTestNode(t, chain2(), idA, Limits{})
	b := newTestNode(t, chain1(), idB, Limits{}, WithInterval(time.Hour))
	b.book.AddAddrs(a.srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() { b.Run(ctx); close(done) }()

	deadline := time.Now().Add(3 * time.Second)
	for a.hellos.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	<-done

	if a.hellos.Load() != 1 {
		t.Fatalf("hellos = %d, want 1", a.hellos.Load())
	}
}

func TestRoundSendsJSONHelloAndAcceptsResponseAtTheSizeLimit(t *testing.T) {
	var contentType atomic.Value
	contentType.Store("")

	var getType atomic.Value
	getType.Store("unset")

	padded := func(size int) string {
		base := `{"addrs":["http://z:9"]}`
		return base + strings.Repeat(" ", size-len(base))
	}
	for _, tc := range []struct {
		name string
		size int
		want bool
	}{
		{"at the limit", maxResponseBytes, true},
		{"over the limit", maxResponseBytes + 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("POST /p2p/hello", func(w http.ResponseWriter, r *http.Request) {
				contentType.Store(r.Header.Get("Content-Type"))

				_ = json.NewEncoder(w).Encode(Hello{Version: 1, NodeID: idA, GenesisHash: gen, Height: 1, TipHash: tip})
			})
			mux.HandleFunc("GET /p2p/peers", func(w http.ResponseWriter, r *http.Request) {
				getType.Store(r.Header.Get("Content-Type"))

				_, _ = w.Write([]byte(padded(tc.size)))
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			b := newTestNode(t, chain1(), idB, Limits{})
			b.book.AddAddrs(srv.URL)

			b.Round(context.Background())

			if got := containsStr(b.Addrs(), "http://z:9"); got != tc.want {
				t.Fatalf("addr learned = %v, want %v", got, tc.want)
			}
		})
	}

	if contentType.Load() != "application/json" || getType.Load() != "" {
		t.Fatalf("content types: hello %q, peers %q", contentType.Load(), getType.Load())
	}
}

func TestRoundRenewalOfAHealthyPeerLogsNothing(t *testing.T) {
	var buf syncBuf

	a := newTestNode(t, chain2(), idA, Limits{})
	b := newTestNode(t, chain1(), idB, Limits{}, WithLogger(slog.New(slog.NewTextHandler(&buf, nil))))
	b.book.AddAddrs(a.srv.URL)
	b.Round(context.Background())
	b.Round(context.Background())

	if len(b.Peers()) != 1 || buf.String() != "" {
		t.Fatalf("peers = %+v log = %q", b.Peers(), buf.String())
	}
}

func TestHelloAnnouncesAnUnsealedGenesisByItsCommittedHash(t *testing.T) {
	g := &domain.Block{Index: 0}

	n, err := NewNode(fakeChain{blocks: []*domain.Block{g}}, NewBook(Limits{}, nil), idA, "")
	if err != nil {
		t.Fatal(err)
	}

	h, err := n.Hello()
	if err != nil {
		t.Fatal(err)
	}

	if h.GenesisHash != g.CalculateHash() || h.TipHash != g.CalculateHash() || !isHex(h.GenesisHash, 64) {
		t.Fatalf("hello = %+v", h)
	}

	if err := Check(h, Hello{Version: 1, NodeID: idB, GenesisHash: h.GenesisHash, TipHash: h.TipHash}); err != nil {
		t.Fatal(err)
	}
}
