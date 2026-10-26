package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/p2p"
)

const (
	idA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	idB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// fakePeers is a Peers double: it records the hello it receives and answers the canned values.
type fakePeers struct {
	got   p2p.Hello
	calls int
	local p2p.Hello
	err   error
	peers []p2p.PeerInfo
	addrs []string

	// gossip: canned answers, the arguments the handlers passed and the announcements.
	want         []p2p.InvItem
	gossipErr    error
	accepted     bool
	gotFrom      string
	gotItems     []p2p.InvItem
	gotBlock     *domain.Block
	gotTx        domain.Transaction
	announcedBlk []*domain.Block
	announcedTx  []domain.Transaction
}

func (f *fakePeers) Inv(from string, items []p2p.InvItem) ([]p2p.InvItem, error) {
	f.gotFrom, f.gotItems = from, items
	return f.want, f.gossipErr
}

func (f *fakePeers) ReceiveBlock(_ context.Context, from string, b *domain.Block) (bool, error) {
	f.gotFrom, f.gotBlock = from, b
	return f.accepted, f.gossipErr
}

func (f *fakePeers) ReceiveTx(from string, tx domain.Transaction) (bool, error) {
	f.gotFrom, f.gotTx = from, tx
	return f.accepted, f.gossipErr
}

func (f *fakePeers) AnnounceBlock(b *domain.Block)    { f.announcedBlk = append(f.announcedBlk, b) }
func (f *fakePeers) AnnounceTx(tx domain.Transaction) { f.announcedTx = append(f.announcedTx, tx) }

func (f *fakePeers) Accept(h p2p.Hello) (p2p.Hello, error) {
	f.got, f.calls = h, f.calls+1
	if f.err != nil {
		return p2p.Hello{}, f.err
	}

	return f.local, nil
}
func (f *fakePeers) Peers() []p2p.PeerInfo { return f.peers }
func (f *fakePeers) Addrs() []string       { return f.addrs }

func p2pHandler(t *testing.T, cfg Config) http.Handler {
	t.Helper()
	svc, _ := realChain(t)

	srv, err := NewServer(svc, cfg)
	if err != nil {
		t.Fatal(err)
	}

	return srv.Handler()
}

func helloBody(id string, height int) string {
	b, _ := json.Marshal(p2p.Hello{Version: 1, NodeID: id, GenesisHash: strings.Repeat("0", 64), Height: height, TipHash: strings.Repeat("1", 64), ListenAddr: "http://10.0.0.2:8080"})
	return string(b)
}

func errCode(t *testing.T, body string) string {
	t.Helper()

	var e errorResponse

	err := json.Unmarshal([]byte(body), &e)
	if err != nil {
		t.Fatalf("body %q: %v", body, err)
	}

	return e.Code
}

func TestPostHelloAcceptedReturnsLocalHello(t *testing.T) {
	local := p2p.Hello{Version: 1, NodeID: idA, GenesisHash: strings.Repeat("0", 64), Height: 3, TipHash: strings.Repeat("2", 64), ListenAddr: "http://10.0.0.1:8080"}
	f := &fakePeers{local: local}
	h := p2pHandler(t, Config{P2P: f})

	rec := do(h, http.MethodPost, "/p2p/hello", helloBody(idB, 7))

	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}

	var got p2p.Hello

	err := json.Unmarshal(rec.Body.Bytes(), &got)
	if err != nil || got != local {
		t.Fatalf("body = %s (%v), want %+v", rec.Body, err, local)
	}

	if f.calls != 1 || f.got.NodeID != idB || f.got.Height != 7 || f.got.ListenAddr != "http://10.0.0.2:8080" {
		t.Fatalf("Accept received %+v (%d calls)", f.got, f.calls)
	}
}

func TestPostHelloErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"bad hello", p2p.ErrBadHello, http.StatusBadRequest, "invalid_hello"},
		{"version", p2p.ErrIncompatibleVersion, http.StatusConflict, "incompatible_version"},
		{"genesis", p2p.ErrGenesisMismatch, http.StatusConflict, "genesis_mismatch"},
		{"self", p2p.ErrSelf, http.StatusConflict, "self_connection"},
		{"limit", p2p.ErrPeerLimit, http.StatusServiceUnavailable, "peer_limit"},
		{"wrapped", errors.Join(errors.New("ctx"), p2p.ErrSelf), http.StatusConflict, "self_connection"},
		{"other", errors.New("boom"), http.StatusInternalServerError, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := p2pHandler(t, Config{P2P: &fakePeers{err: c.err}})

			rec := do(h, http.MethodPost, "/p2p/hello", helloBody(idB, 1))
			if rec.Code != c.status || errCode(t, rec.Body.String()) != c.code {
				t.Fatalf("got %d %s, want %d %q", rec.Code, rec.Body, c.status, c.code)
			}

			if c.code == "" && !strings.Contains(rec.Body.String(), "boom") {
				t.Fatalf("500 body %s lacks the error", rec.Body)
			}
		})
	}
}

func TestPostHelloRejectsBadBodies(t *testing.T) {
	good := helloBody(idB, 1)

	cases := map[string]string{
		"not json":      "{",
		"empty":         "",
		"unknown field": strings.Replace(good, `{`, `{"extra":1,`, 1),
		"wrong type":    `{"version":"1"}`,
		"trailing data": good + good,
		"trailing junk": good + " x",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakePeers{}

			rec := do(p2pHandler(t, Config{P2P: f}), http.MethodPost, "/p2p/hello", body)
			if rec.Code != http.StatusBadRequest || errCode(t, rec.Body.String()) != "invalid_hello" || f.calls != 0 {
				t.Fatalf("got %d %s (%d calls)", rec.Code, rec.Body, f.calls)
			}
		})
	}
}

func TestPostHelloTrailingWhitespaceIsFine(t *testing.T) {
	f := &fakePeers{}

	rec := do(p2pHandler(t, Config{P2P: f}), http.MethodPost, "/p2p/hello", helloBody(idB, 1)+"\n \n")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestPostHelloBodyLimit(t *testing.T) {
	f := &fakePeers{}
	body := strings.TrimSuffix(helloBody(idB, 1), "}") + `,"pad":"` + strings.Repeat("a", int(maxRequestBodyBytes())) + `"}`

	rec := do(p2pHandler(t, Config{P2P: f}), http.MethodPost, "/p2p/hello", body)
	if rec.Code != http.StatusRequestEntityTooLarge || errCode(t, rec.Body.String()) != requestTooLargeCode || f.calls != 0 {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}

	var e errorResponse

	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	if e.Details["max"] != float64(maxRequestBodyBytes()) {
		t.Fatalf("details = %v", e.Details)
	}
}

func TestGetPeersShape(t *testing.T) {
	seen := time.Date(2026, 10, 1, 12, 0, 0, 0, time.FixedZone("x", -3*3600))
	f := &fakePeers{
		peers: []p2p.PeerInfo{
			{NodeID: idB, Addr: "http://10.0.0.2:8080", Height: 7, TipHash: strings.Repeat("1", 64), Inbound: true, LastSeen: seen},
			{NodeID: idA, Addr: "", Height: 0, TipHash: strings.Repeat("2", 64), LastSeen: seen},
		},
		addrs: []string{"http://10.0.0.3:8080", "http://10.0.0.2:8080"},
	}
	rec := do(p2pHandler(t, Config{P2P: f}), http.MethodGet, "/p2p/peers", "")

	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}

	var raw struct {
		Peers []map[string]any `json:"peers"`
		Addrs []string         `json:"addrs"`
	}

	err := json.Unmarshal(rec.Body.Bytes(), &raw)
	if err != nil {
		t.Fatal(err)
	}

	want0 := map[string]any{"node_id": idB, "addr": "http://10.0.0.2:8080", "height": float64(7), "tip_hash": strings.Repeat("1", 64), "inbound": true, "last_seen": "2026-10-01T15:00:00Z"}
	if len(raw.Peers) != 2 || len(raw.Peers[0]) != len(want0) {
		t.Fatalf("peers = %v", raw.Peers)
	}

	for k, v := range want0 {
		if raw.Peers[0][k] != v {
			t.Fatalf("peer[0][%s] = %v, want %v", k, raw.Peers[0][k], v)
		}
	}

	if raw.Peers[1]["node_id"] != idA || raw.Peers[1]["inbound"] != false || raw.Peers[1]["addr"] != "" {
		t.Fatalf("peer[1] = %v", raw.Peers[1])
	}

	if len(raw.Addrs) != 2 || raw.Addrs[0] != "http://10.0.0.3:8080" || raw.Addrs[1] != "http://10.0.0.2:8080" {
		t.Fatalf("addrs = %v", raw.Addrs)
	}
}

func TestGetPeersEmptyListsAreArrays(t *testing.T) {
	for name, f := range map[string]*fakePeers{"nil": {}, "empty": {peers: []p2p.PeerInfo{}, addrs: []string{}}} {
		t.Run(name, func(t *testing.T) {
			rec := do(p2pHandler(t, Config{P2P: f}), http.MethodGet, "/p2p/peers", "")

			var m map[string]json.RawMessage

			err := json.Unmarshal(rec.Body.Bytes(), &m)
			if err != nil {
				t.Fatal(err)
			}

			if string(m["peers"]) != "[]" || string(m["addrs"]) != "[]" {
				t.Fatalf("body = %s", rec.Body)
			}
		})
	}
}

func TestP2PDisabled(t *testing.T) {
	h := p2pHandler(t, Config{})
	for _, rt := range [][2]string{{http.MethodPost, "/p2p/hello"}, {http.MethodGet, "/p2p/peers"}} {
		rec := do(h, rt[0], rt[1], helloBody(idB, 1))
		if rec.Code != http.StatusNotFound || errCode(t, rec.Body.String()) != "p2p_disabled" {
			t.Fatalf("%s %s: %d %s", rt[0], rt[1], rec.Code, rec.Body)
		}
	}
}

func TestP2PMethodNotAllowed(t *testing.T) {
	cases := []struct{ method, path, allow string }{
		{http.MethodGet, "/p2p/hello", "POST"},
		{http.MethodPut, "/p2p/hello", "POST"},
		{http.MethodPost, "/p2p/peers", "GET, HEAD"},
		{http.MethodDelete, "/p2p/peers", "GET, HEAD"},
	}

	for _, p2pCfg := range []Peers{nil, &fakePeers{}} {
		h := p2pHandler(t, Config{P2P: p2pCfg})
		for _, c := range cases {
			rec := do(h, c.method, c.path, "")
			if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != c.allow || rec.Body.Len() != 0 {
				t.Fatalf("%s %s: %d allow=%q body=%q", c.method, c.path, rec.Code, rec.Header().Get("Allow"), rec.Body)
			}
		}
	}
}

func TestP2PRoutesShareRateLimiterAndMetrics(t *testing.T) {
	now := time.Unix(fixedNow, 0)

	h := p2pHandler(t, Config{P2P: &fakePeers{}, RateLimit: 1, Now: func() time.Time { return now }})
	if rec := do(h, http.MethodGet, "/p2p/peers", ""); rec.Code != http.StatusOK {
		t.Fatalf("first = %d", rec.Code)
	}

	if rec := do(h, http.MethodPost, "/p2p/hello", helloBody(idB, 1)); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second = %d", rec.Code)
	}

	h = p2pHandler(t, Config{P2P: &fakePeers{}})
	do(h, http.MethodGet, "/p2p/peers", "")
	do(h, http.MethodPost, "/p2p/hello", helloBody(idB, 1))

	m := scrape(t, h)
	if m[`http_requests_total{method="GET",route="GET /p2p/peers",code="200"}`] != 1 ||
		m[`http_requests_total{method="POST",route="POST /p2p/hello",code="200"}`] != 1 {
		t.Fatalf("metrics = %v", m)
	}
}

// hashedChain gives the p2p node a chain whose genesis block carries a hash: chain.Service stores
// the genesis block without one, which the handshake rejects as malformed.
type hashedChain struct{ Chain }

func (c hashedChain) BlockByHeight(h int) (*domain.Block, error) {
	b, err := c.Chain.BlockByHeight(h)
	if err != nil || b.Hash != "" {
		return b, err
	}

	cp := *b
	cp.Hash = strings.Repeat("0", 64)

	return &cp, nil
}

func (c hashedChain) Tip() (*domain.Block, error) {
	b, err := c.Chain.Tip()
	if err != nil || b.Hash != "" {
		return b, err
	}

	cp := *b
	cp.Hash = strings.Repeat("0", 64)

	return &cp, nil
}

// TestRealNodeHandshake drives a real p2p.Node: the caller shows up as an inbound peer, and a
// refused hello leaves GET /p2p/peers unchanged.
func TestRealNodeHandshake(t *testing.T) {
	svc, _ := realChain(t)

	node, err := p2p.NewNode(hashedChain{svc}, p2p.NewBook(p2p.Limits{MaxInbound: 1}, nil), idA, "http://10.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}

	srv, err := NewServer(svc, Config{P2P: node})
	if err != nil {
		t.Fatal(err)
	}

	h := srv.Handler()
	local, _ := node.Hello()
	hello := func(id string, height int) string {
		b, _ := json.Marshal(p2p.Hello{Version: 1, NodeID: id, GenesisHash: local.GenesisHash, Height: height, TipHash: strings.Repeat("1", 64), ListenAddr: "http://10.0.0.2:8080"})
		return string(b)
	}

	rec := do(h, http.MethodPost, "/p2p/hello", hello(idB, 9))

	var got p2p.Hello
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got != local {
		t.Fatalf("hello = %d %s", rec.Code, rec.Body)
	}

	after := do(h, http.MethodGet, "/p2p/peers", "").Body.String()

	var pr struct {
		Peers []struct {
			NodeID  string `json:"node_id"`
			Height  int    `json:"height"`
			Inbound bool   `json:"inbound"`
		} `json:"peers"`
		Addrs []string `json:"addrs"`
	}

	err = json.Unmarshal([]byte(after), &pr)
	if err != nil || len(pr.Peers) != 1 || pr.Peers[0].NodeID != idB || pr.Peers[0].Height != 9 || !pr.Peers[0].Inbound || len(pr.Addrs) != 1 {
		t.Fatalf("peers = %s (%v)", after, err)
	}

	refused := []string{
		strings.Replace(hello("cccccccccccccccccccccccccccccccc", 1), `"version":1`, `"version":2`, 1),
		strings.Replace(hello("cccccccccccccccccccccccccccccccc", 1), local.GenesisHash, strings.Repeat("f", 64), 1),
		hello(idA, 1),
		hello("cccccccccccccccccccccccccccccccc", 1), // peer limit: idB holds the only inbound slot
		strings.Replace(hello("cccccccccccccccccccccccccccccccc", 1), `"node_id":"cccccccccccccccccccccccccccccccc"`, `"node_id":"zz"`, 1),
	}

	wantStatus := []int{409, 409, 409, 503, 400}
	for i, body := range refused {
		if rec := do(h, http.MethodPost, "/p2p/hello", body); rec.Code != wantStatus[i] {
			t.Fatalf("refused[%d] = %d %s, want %d", i, rec.Code, rec.Body, wantStatus[i])
		}

		if now := do(h, http.MethodGet, "/p2p/peers", "").Body.String(); now != after {
			t.Fatalf("refused[%d] changed the peers:\n%s\nvs\n%s", i, now, after)
		}
	}
}
