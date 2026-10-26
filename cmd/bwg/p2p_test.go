package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/p2p"
	"github.com/petersonsalme/blockchain-with-go/internal/server"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
)

func TestParseSeedPeers(t *testing.T) {
	got, err := parseSeedPeers(" HTTP://A:80 , https://b.example:443/ ")
	if err != nil || !reflect.DeepEqual(got, []string{"http://a:80", "https://b.example:443"}) {
		t.Fatalf("got %v, %v", got, err)
	}

	for _, blank := range []string{"", "  "} {
		got, err := parseSeedPeers(blank)
		if err != nil || got != nil {
			t.Fatalf("blank %q: %v, %v", blank, got, err)
		}
	}

	for _, bad := range []string{"nope", "http://a", "http://a:80,", "ftp://a:1", "http://a:80,http://b"} {
		_, err := parseSeedPeers(bad)
		if err == nil || !strings.HasPrefix(err.Error(), seedPeersEnvVar+": invalid item ") {
			t.Fatalf("%q: err = %v", bad, err)
		}
	}

	_, err = parseSeedPeers("http://a:80,xx")
	if err == nil || !strings.Contains(err.Error(), `"xx"`) {
		t.Fatalf("err = %v, want the bad item quoted", err)
	}
}

func TestParsePositiveInt(t *testing.T) {
	for raw, want := range map[string]int{"3": 3, " 7 ": 7, "": 0, "x": 0, "0": 0, "-2": 0, "1": 1} {
		if got := parsePositiveInt(raw); got != want {
			t.Errorf("%q = %d, want %d", raw, got, want)
		}
	}
}

func TestParsePeerInterval(t *testing.T) {
	for raw, want := range map[string]time.Duration{"2s": 2 * time.Second, " 1m ": time.Minute, "": 0, "x": 0, "0s": 0, "-1s": 0, "1ns": 1} {
		if got := parsePeerInterval(raw); got != want {
			t.Errorf("%q = %v, want %v", raw, got, want)
		}
	}
}

// newTestService builds a service with the given number of mined blocks. A
// non-nil genesis seeds the repository, so several nodes can share one chain
// start (chain.NewService stamps a fresh genesis with the current time).
func newTestService(t *testing.T, blocks int, genesis *domain.Block) *chain.Service {
	t.Helper()
	t.Setenv("POW_DIFFICULTY", "1")

	repo := memory.New()
	if genesis != nil {
		delta, err := domain.BlockDelta(repo, genesis, true)
		if err != nil {
			t.Fatal(err)
		}

		err = repo.Commit(genesis, delta)
		if err != nil {
			t.Fatal(err)
		}
	}

	svc, err := chain.NewService(repo, pow.NewProofOfWorkFromEnv())
	if err != nil {
		t.Fatal(err)
	}

	for range blocks {
		_, err := svc.Mine(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
	}

	return svc
}

func TestNewP2PNodeConfigErrors(t *testing.T) {
	svc := newTestService(t, 0, nil)
	t.Run("bad seed", func(t *testing.T) {
		t.Setenv(seedPeersEnvVar, "http://a:80,bad")

		_, err := newP2PNode(svc, "", discardLogger())
		if err == nil || !strings.HasPrefix(err.Error(), "SEED_PEERS: invalid item ") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("bad listen address", func(t *testing.T) {
		t.Setenv(p2pListenAddrEnvVar, "nope")

		_, err := newP2PNode(svc, "", discardLogger())
		if !errors.Is(err, p2p.ErrBadAddr) || !strings.HasPrefix(err.Error(), "P2P_LISTEN_ADDR: ") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("corrupt peers file", func(t *testing.T) {
		dir := t.TempDir()

		err := os.WriteFile(filepath.Join(dir, "peers.json"), []byte("{"), 0o600)
		if err != nil {
			t.Fatal(err)
		}

		_, err = newP2PNode(svc, dir, discardLogger())
		if err == nil || !strings.Contains(err.Error(), "peers.json") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestNewP2PNodeBookAndIdentity(t *testing.T) {
	svc := newTestService(t, 0, nil)
	t.Setenv(seedPeersEnvVar, "http://seed:1")
	t.Setenv(p2pListenAddrEnvVar, " HTTP://Me:9 ")

	t.Run("without DATA_DIR only the seeds and no file", func(t *testing.T) {
		t.Chdir(t.TempDir())

		n, err := newP2PNode(svc, "  ", discardLogger())
		if err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(n.Addrs(), []string{"http://seed:1"}) {
			t.Fatalf("addrs = %v", n.Addrs())
		}

		h, _ := n.Hello()
		if h.ListenAddr != "http://me:9" {
			t.Fatalf("listen = %q", h.ListenAddr)
		}

		n.Round(context.Background())

		if entries, _ := os.ReadDir("."); len(entries) != 0 {
			t.Fatalf("files created: %v", entries)
		}
	})

	t.Run("missing file is an empty list", func(t *testing.T) {
		n, err := newP2PNode(svc, t.TempDir(), discardLogger())
		if err != nil || !reflect.DeepEqual(n.Addrs(), []string{"http://seed:1"}) {
			t.Fatalf("addrs = %v, %v", n.Addrs(), err)
		}
	})

	t.Run("file plus seeds, saved on every round", func(t *testing.T) {
		dir := t.TempDir()

		err := p2p.SaveAddrs(filepath.Join(dir, "peers.json"), []string{"http://old:2"})
		if err != nil {
			t.Fatal(err)
		}

		n, err := newP2PNode(svc, dir, discardLogger())
		if err != nil {
			t.Fatal(err)
		}

		want := []string{"http://old:2", "http://seed:1"}
		if !reflect.DeepEqual(n.Addrs(), want) {
			t.Fatalf("addrs = %v", n.Addrs())
		}

		n.Round(context.Background())

		saved, err := p2p.LoadAddrs(filepath.Join(dir, "peers.json"))
		if err != nil || !reflect.DeepEqual(saved, want) {
			t.Fatalf("saved = %v, %v", saved, err)
		}
	})

	t.Run("a new NodeID per start", func(t *testing.T) {
		a, _ := newP2PNode(svc, "", discardLogger())
		b, _ := newP2PNode(svc, "", discardLogger())
		ha, _ := a.Hello()

		hb, _ := b.Hello()
		if ha.NodeID == hb.NodeID {
			t.Fatal("same NodeID twice")
		}
	})
}

func TestNewP2PNodeAppliesLimits(t *testing.T) {
	svc := newTestService(t, 0, nil)
	t.Setenv(maxInboundPeersEnvVar, "1")

	n, err := newP2PNode(svc, "", discardLogger())
	if err != nil {
		t.Fatal(err)
	}

	local, _ := n.Hello()

	hello := func(id string) p2p.Hello {
		h := local
		h.NodeID = id

		return h
	}

	_, err = n.Accept(hello(strings.Repeat("a", 32)))
	if err != nil {
		t.Fatal(err)
	}

	_, err = n.Accept(hello(strings.Repeat("b", 32)))
	if !errors.Is(err, p2p.ErrPeerLimit) {
		t.Fatalf("err = %v, want ErrPeerLimit", err)
	}
}

// p2pNode is a real node: server.Handler and p2p.Node behind an httptest.Server.
type p2pNode struct {
	node    *p2p.Node
	srv     *httptest.Server
	genesis *domain.Block
}

func startP2PNode(t *testing.T, blocks int, genesis *domain.Block) *p2pNode {
	t.Helper()
	svc := newTestService(t, blocks, genesis)

	g, err := svc.BlockByHeight(0)
	if err != nil {
		t.Fatal(err)
	}

	pn := &p2pNode{genesis: g}

	var handler http.Handler

	pn.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	t.Cleanup(pn.srv.Close)
	t.Setenv(p2pListenAddrEnvVar, pn.srv.URL)

	node, err := newP2PNode(svc, "", discardLogger())
	if err != nil {
		t.Fatal(err)
	}

	s, err := server.NewServer(svc, server.Config{P2P: node})
	if err != nil {
		t.Fatal(err)
	}

	handler = s.Handler()
	pn.node = node

	return pn
}

type peersBody struct {
	Peers []struct {
		NodeID  string `json:"node_id"`
		Addr    string `json:"addr"`
		Height  int    `json:"height"`
		Inbound bool   `json:"inbound"`
	} `json:"peers"`
	Addrs []string `json:"addrs"`
}

func getPeers(t *testing.T, base string) peersBody {
	t.Helper()

	resp, err := getURL(base + "/p2p/peers")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = resp.Body.Close() }() // test response body

	raw, _ := io.ReadAll(resp.Body)

	var body peersBody
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &body) != nil {
		t.Fatalf("GET /p2p/peers = %d %s", resp.StatusCode, raw)
	}

	return body
}

func TestTwoNodesConnectAndExchangeHeights(t *testing.T) {
	a := startP2PNode(t, 2, nil)
	t.Setenv(seedPeersEnvVar, a.srv.URL)
	b := startP2PNode(t, 0, a.genesis)

	b.node.Round(context.Background())

	onA := getPeers(t, a.srv.URL)
	if len(onA.Peers) != 1 || !onA.Peers[0].Inbound || onA.Peers[0].Height != 0 || onA.Peers[0].Addr != b.srv.URL {
		t.Fatalf("A peers = %+v", onA)
	}

	onB := getPeers(t, b.srv.URL)
	if len(onB.Peers) != 1 || onB.Peers[0].Inbound || onB.Peers[0].Height != 2 || onB.Peers[0].Addr != a.srv.URL {
		t.Fatalf("B peers = %+v", onB)
	}
}

func TestIncompatibleVersionIsRefusedByBothSides(t *testing.T) {
	svc := newTestService(t, 2, nil)
	genesis, _ := svc.BlockByHeight(0)

	var handler http.Handler

	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	t.Cleanup(srvA.Close)

	nodeA, err := p2p.NewNode(svc, p2p.NewBook(p2p.Limits{}, nil), strings.Repeat("a", 32), srvA.URL, p2p.WithProtocolVersion(2))
	if err != nil {
		t.Fatal(err)
	}

	s, err := server.NewServer(svc, server.Config{P2P: nodeA})
	if err != nil {
		t.Fatal(err)
	}

	handler = s.Handler()

	t.Setenv(seedPeersEnvVar, srvA.URL)
	b := startP2PNode(t, 0, genesis)

	b.node.Round(context.Background())
	b.node.Round(context.Background())

	if got := getPeers(t, srvA.URL); len(got.Peers) != 0 {
		t.Fatalf("A lists %+v", got.Peers)
	}

	if got := getPeers(t, b.srv.URL); len(got.Peers) != 0 {
		t.Fatalf("B lists %+v", got.Peers)
	}
}

func TestAddressExchangeConnectsThirdNode(t *testing.T) {
	a := startP2PNode(t, 2, nil)
	t.Setenv(seedPeersEnvVar, a.srv.URL)
	b := startP2PNode(t, 1, a.genesis)
	b.node.Round(context.Background())
	t.Setenv(seedPeersEnvVar, b.srv.URL)
	c := startP2PNode(t, 0, a.genesis)

	c.node.Round(context.Background())
	c.node.Round(context.Background())

	onC := getPeers(t, c.srv.URL)
	if !containsAddr(onC.Addrs, a.srv.URL) || len(onC.Peers) != 2 {
		t.Fatalf("C = %+v", onC)
	}

	for _, p := range onC.Peers {
		if p.Inbound || (p.Addr != a.srv.URL && p.Addr != b.srv.URL) {
			t.Fatalf("C peers = %+v", onC.Peers)
		}
	}
}

func containsAddr(list []string, s string) bool {
	return slices.Contains(list, s)
}

func TestRunStartsAndStopsTheDialer(t *testing.T) {
	defer goleak.VerifyNone(t)

	svc := newTestService(t, 0, nil)
	t.Setenv(peerIntervalEnvVar, "10ms")

	node, err := newP2PNode(svc, "", discardLogger())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	stop := startDialer(ctx, node)

	time.Sleep(30 * time.Millisecond)
	cancel()
	stop()
}

func TestRunDialsSeedsWhileServingAndStopsOnShutdown(t *testing.T) {
	var hellos atomic.Int64

	seed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hellos.Add(1)
		http.Error(w, "no", http.StatusConflict)
	}))
	port := freePort(t)
	t.Setenv("PORT", port)
	t.Setenv("POW_DIFFICULTY", "1")
	t.Setenv(seedPeersEnvVar, seed.URL)
	t.Setenv(peerIntervalEnvVar, "20ms")

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- run(ctx, discardLogger()) }()

	deadline := time.Now().Add(5 * time.Second)
	for hellos.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if hellos.Load() < 2 {
		cancel()
		t.Fatalf("seed got %d hellos, want a first round and later ones", hellos.Load())
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return")
	}

	after := hellos.Load()

	time.Sleep(100 * time.Millisecond)

	if hellos.Load() != after {
		t.Fatal("dialer still running after shutdown")
	}

	seed.Close()
	http.DefaultClient.CloseIdleConnections()
	goleak.VerifyNone(t)
}

func TestRunFailsOnBadP2PConfig(t *testing.T) {
	t.Setenv("PORT", freePort(t))
	t.Setenv("POW_DIFFICULTY", "1")
	t.Setenv(seedPeersEnvVar, "bad")

	err := run(context.Background(), discardLogger())
	if err == nil || !strings.Contains(err.Error(), "SEED_PEERS") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunWithDataDirPersistsPeers(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	port := freePort(t)
	t.Setenv("PORT", port)
	t.Setenv("POW_DIFFICULTY", "1")
	t.Setenv(seedPeersEnvVar, "http://127.0.0.1:1")

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- run(ctx, discardLogger()) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if saved, _ := p2p.LoadAddrs(filepath.Join(dir, "peers.json")); len(saved) == 1 {
			break
		}

		time.Sleep(20 * time.Millisecond)
	}

	cancel()

	err := <-done
	if err != nil {
		t.Fatal(err)
	}

	saved, err := p2p.LoadAddrs(filepath.Join(dir, "peers.json"))
	if err != nil || !reflect.DeepEqual(saved, []string{"http://127.0.0.1:1"}) {
		t.Fatalf("saved = %v, %v", saved, err)
	}
}

func TestMaxOutboundPeersLimitsDialedPeers(t *testing.T) {
	a1 := startP2PNode(t, 1, nil)
	a2 := startP2PNode(t, 1, a1.genesis)
	t.Setenv(seedPeersEnvVar, a1.srv.URL+","+a2.srv.URL)
	t.Setenv(maxOutboundPeersEnvVar, "1")
	b := startP2PNode(t, 0, a1.genesis)

	b.node.Round(context.Background())

	if got := getPeers(t, b.srv.URL); len(got.Peers) != 1 {
		t.Fatalf("B peers = %+v, want exactly 1 outbound", got.Peers)
	}
}

func TestNewP2PNodeLogsToTheGivenLogger(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	t.Setenv(seedPeersEnvVar, url)

	var buf bytes.Buffer

	n, err := newP2PNode(newTestService(t, 0, nil), "", slog.New(slog.NewTextHandler(&buf, nil)))
	if err != nil {
		t.Fatal(err)
	}

	n.Round(context.Background())

	if buf.Len() == 0 {
		t.Fatal("dial failure was not logged to the process logger")
	}
}

func TestMapSyncError(t *testing.T) {
	other := errors.New("boom")

	cases := []struct {
		name string
		err  error
		want error // the p2p sentinel the result must satisfy; nil for a pass-through
	}{
		{"insufficient work", chain.ErrInsufficientWork, p2p.ErrInsufficientWork},
		{"not on tip", chain.ErrNotOnTip, p2p.ErrNotOnTip},
		{"state diverged", chain.ErrStateDiverged, p2p.ErrLocalFailure},
		{"conflict", storage.ErrConflict, p2p.ErrLocalFailure},
		{"reorg too deep", storage.ErrReorgTooDeep, p2p.ErrLocalFailure},
		{"empty", storage.ErrEmpty, p2p.ErrLocalFailure},
		{"wrapped", fmt.Errorf("ctx: %w", chain.ErrInsufficientWork), p2p.ErrInsufficientWork},
		{"invalid block", chain.ErrInvalidBlock, nil},
		{"other", other, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mapSyncError(c.err)
			if c.want == nil {
				if got != c.err {
					t.Fatalf("got %v, want the error unchanged", got)
				}

				return
			}

			if !errors.Is(got, c.want) || !errors.Is(got, c.err) {
				t.Fatalf("got %v, want %v wrapping %v", got, c.want, c.err)
			}
		})
	}

	if mapSyncError(nil) != nil {
		t.Fatal("nil must stay nil")
	}
}
