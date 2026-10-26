package p2p

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

var (
	hashX = strings.Repeat("e", 64)
	hashY = strings.Repeat("f", 64)
	idE   = strings.Repeat("1", 32)
)

type fakeLedger struct {
	mu        sync.Mutex
	blocks    map[string]bool
	txs       map[string]bool
	err       error
	blockHits int
	txHits    int
	gate      chan struct{}
}

func newLedger() *fakeLedger { return &fakeLedger{blocks: map[string]bool{}, txs: map[string]bool{}} }

func (l *fakeLedger) HasBlock(h string) bool { l.mu.Lock(); defer l.mu.Unlock(); return l.blocks[h] }
func (l *fakeLedger) HasTx(id string) bool   { l.mu.Lock(); defer l.mu.Unlock(); return l.txs[id] }

func (l *fakeLedger) AcceptBlock(_ context.Context, _ *domain.Block) error {
	if l.gate != nil {
		<-l.gate
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.blockHits++

	return l.err
}

func (l *fakeLedger) SubmitTx(domain.Transaction) error {
	if l.gate != nil {
		<-l.gate
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.txHits++

	return l.err
}

func (l *fakeLedger) hits() (int, int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.blockHits, l.txHits
}

// fakePeer records the gossip requests it gets; it wants everything unless wantNothing is set.
type fakePeer struct {
	srv         *httptest.Server
	wantNothing bool

	mu   sync.Mutex
	reqs []string
	body map[string]map[string]any
}

func newFakePeer(t *testing.T) *fakePeer {
	t.Helper()

	p := &fakePeer{body: map[string]map[string]any{}}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any

		_ = json.NewDecoder(r.Body).Decode(&m)

		p.mu.Lock()
		p.reqs = append(p.reqs, r.URL.Path)
		p.body[r.URL.Path] = m
		p.mu.Unlock()

		if r.URL.Path == "/p2p/inv" {
			want := []any{}
			if !p.wantNothing {
				want, _ = m["items"].([]any)
			}

			_ = json.NewEncoder(w).Encode(map[string]any{"want": want})

			return
		}

		_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
	}))
	t.Cleanup(p.srv.Close)

	return p
}

func (p *fakePeer) paths() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]string(nil), p.reqs...)
}

func (p *fakePeer) waitFor(t *testing.T, n int) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for len(p.paths()) < n && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}

	if got := len(p.paths()); got < n {
		t.Fatalf("peer got %v, want %d requests", p.paths(), n)
	}
}

func connect(t *testing.T, n *Node, id, addr string) {
	t.Helper()

	err := n.book.Connect(Hello{NodeID: id, ListenAddr: addr}, true)
	if err != nil {
		t.Fatal(err)
	}
}

// gossipNode builds a node with peers A, C and D connected, running its worker.
func gossipNode(t *testing.T, l Ledger, opts ...Option) (*Node, [3]*fakePeer) {
	t.Helper()

	a, c, d := newFakePeer(t), newFakePeer(t), newFakePeer(t)
	book := NewBook(Limits{}, nil)

	n, err := NewNode(chain1(), book, idE, "", append([]Option{WithLedger(l)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}

	connect(t, n, idA, a.srv.URL)
	connect(t, n, idC, c.srv.URL)
	connect(t, n, idD, d.srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() { n.relayLoop(ctx); close(done) }()

	t.Cleanup(func() { cancel(); <-done })

	return n, [3]*fakePeer{a, c, d}
}

func blk(h string) *domain.Block { return &domain.Block{Index: 1, Hash: h} }

func TestReceiveBlockRelaysWithoutEcho(t *testing.T) {
	n, peers := gossipNode(t, newLedger())
	a := peers[0]
	c := peers[1]
	d := peers[2]

	ok, err := n.ReceiveBlock(context.Background(), idA, blk(hashX))
	if !ok || err != nil {
		t.Fatalf("got %v, %v", ok, err)
	}

	for _, p := range []*fakePeer{c, d} {
		p.waitFor(t, 2)

		got := p.paths()
		if got[0] != "/p2p/inv" || got[1] != "/p2p/block" {
			t.Fatalf("paths = %v", got)
		}

		if p.body["/p2p/inv"]["node_id"] != idE || p.body["/p2p/block"]["node_id"] != idE {
			t.Fatalf("node_id missing: %v", p.body)
		}

		items := p.body["/p2p/inv"]["items"].([]any)[0].(map[string]any)
		if items["type"] != "block" || items["hash"] != hashX {
			t.Fatalf("item = %v", items)
		}

		if p.body["/p2p/block"]["block"].(map[string]any)["Hash"] != hashX {
			t.Fatalf("block body = %v", p.body["/p2p/block"])
		}
	}

	time.Sleep(50 * time.Millisecond)

	if len(a.paths()) != 0 {
		t.Fatalf("origin got %v", a.paths())
	}
}

func TestReceiveTxRelaysWithoutEcho(t *testing.T) {
	n, peers := gossipNode(t, newLedger())
	a := peers[0]
	c := peers[1]
	d := peers[2]

	ok, err := n.ReceiveTx(idA, domain.Transaction{ID: hashY})
	if !ok || err != nil {
		t.Fatalf("got %v, %v", ok, err)
	}

	for _, p := range []*fakePeer{c, d} {
		p.waitFor(t, 2)

		if got := p.paths(); got[1] != "/p2p/tx" {
			t.Fatalf("paths = %v", got)
		}

		if p.body["/p2p/tx"]["tx"].(map[string]any)["ID"] != hashY || p.body["/p2p/tx"]["node_id"] != idE {
			t.Fatalf("tx body = %v", p.body["/p2p/tx"])
		}
	}

	time.Sleep(50 * time.Millisecond)

	if len(a.paths()) != 0 {
		t.Fatalf("origin got %v", a.paths())
	}
}

func TestInvalidObjectIsNotRelayedAndStaysSeen(t *testing.T) {
	boom := errors.New("bad signature")
	l := newLedger()
	l.err = boom
	n, peers := gossipNode(t, l)

	var err error

	a := peers[0]
	c := peers[1]

	ok, err := n.ReceiveBlock(context.Background(), idA, blk(hashX))
	if ok || !errors.Is(err, ErrInvalidObject) || !errors.Is(err, boom) {
		t.Fatalf("got %v, %v", ok, err)
	}

	ok, err = n.ReceiveBlock(context.Background(), idA, blk(hashX))
	if ok || err != nil {
		t.Fatalf("second delivery: %v, %v", ok, err)
	}

	_, err = n.ReceiveTx(idA, domain.Transaction{ID: hashY})
	if !errors.Is(err, ErrInvalidObject) {
		t.Fatal(err)
	}

	if b, x := l.hits(); b != 1 || x != 1 {
		t.Fatalf("ledger hits = %d, %d", b, x)
	}

	time.Sleep(50 * time.Millisecond)

	if len(a.paths())+len(c.paths()) != 0 {
		t.Fatal("invalid object relayed")
	}
}

func TestNotOnTipIsForgottenAndNotRelayed(t *testing.T) {
	l := newLedger()
	l.err = errors.Join(errors.New("wrap"), ErrNotOnTip)
	n, peers := gossipNode(t, l)

	var err error

	c := peers[1]

	for range 2 {
		ok, err := n.ReceiveBlock(context.Background(), idA, blk(hashX))
		if ok || !errors.Is(err, ErrNotOnTip) {
			t.Fatalf("got %v, %v", ok, err)
		}
	}

	if b, _ := l.hits(); b != 2 {
		t.Fatalf("ledger hits = %d, want 2", b)
	}

	// a transaction rejected for the same reason is forgotten too
	_, err = n.ReceiveTx(idA, domain.Transaction{ID: hashY})
	if !errors.Is(err, ErrNotOnTip) {
		t.Fatal(err)
	}

	time.Sleep(50 * time.Millisecond)

	if len(c.paths()) != 0 {
		t.Fatal("relayed")
	}
}

func TestReceiveKnownByLedgerSkipsIt(t *testing.T) {
	l := newLedger()
	l.blocks[hashX], l.txs[hashY] = true, true
	n, _ := gossipNode(t, l)

	ok, err := n.ReceiveBlock(context.Background(), idA, blk(hashX))
	ok2, err2 := n.ReceiveTx(idA, domain.Transaction{ID: hashY})

	if ok || err != nil || ok2 || err2 != nil {
		t.Fatalf("got %v %v %v %v", ok, err, ok2, err2)
	}

	if b, x := l.hits(); b+x != 0 {
		t.Fatal("ledger called")
	}
}

func TestReceiveRejectsUnknownSenderBadHashAndNilBlock(t *testing.T) {
	l := newLedger()
	n, _ := gossipNode(t, l)

	var err error

	_, err = n.ReceiveBlock(context.Background(), idB, blk(hashX))
	if !errors.Is(err, ErrUnknownPeer) {
		t.Fatal(err)
	}

	_, err = n.ReceiveTx(idB, domain.Transaction{ID: hashY})
	if !errors.Is(err, ErrUnknownPeer) {
		t.Fatal(err)
	}

	_, err = n.ReceiveBlock(context.Background(), idA, blk("short"))
	if !errors.Is(err, ErrInvalidObject) {
		t.Fatal(err)
	}

	_, err = n.ReceiveTx(idA, domain.Transaction{ID: "ABC"})
	if !errors.Is(err, ErrInvalidObject) {
		t.Fatal(err)
	}

	_, err = n.ReceiveBlock(context.Background(), idA, nil)
	if !errors.Is(err, ErrInvalidObject) {
		t.Fatal(err)
	}

	if b, x := l.hits(); b+x != 0 || n.seen.has("short") {
		t.Fatal("bad input reached the ledger or the seen set")
	}
}

func TestConcurrentDeliveryCallsLedgerOnceAndRelaysOnce(t *testing.T) {
	l := newLedger()
	l.gate = make(chan struct{})
	n, peers := gossipNode(t, l)
	c := peers[1]

	var wg sync.WaitGroup

	var accepted atomic.Int64

	for range 8 {
		wg.Go(func() {
			ok, err := n.ReceiveBlock(context.Background(), idA, blk(hashX))
			if ok && err == nil {
				accepted.Add(1)
			}
		})
	}

	time.Sleep(50 * time.Millisecond)
	close(l.gate)
	wg.Wait()

	if b, _ := l.hits(); b != 1 || accepted.Load() != 1 {
		t.Fatalf("hits=%d accepted=%d", b, accepted.Load())
	}

	c.waitFor(t, 2)
	time.Sleep(50 * time.Millisecond)

	if len(c.paths()) != 2 {
		t.Fatalf("relayed %v", c.paths())
	}
}

func TestInvAnswersUnknownItemsInOrder(t *testing.T) {
	l := newLedger()
	l.blocks[hashX] = true
	n, _ := gossipNode(t, l)

	var err error

	n.seen.add(hashY)

	h3 := strings.Repeat("9", 64)
	h4 := strings.Repeat("8", 64)
	l.txs[h4] = true

	got, err := n.Inv(idA, []InvItem{{InvBlock, hashX}, {InvTx, hashY}, {InvTx, h3}, {InvTx, h4}, {InvBlock, h4}})
	if err != nil {
		t.Fatal(err)
	}

	want := []InvItem{{InvTx, h3}, {InvBlock, h4}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}

	got, _ = n.Inv(idA, []InvItem{{InvBlock, hashX}})
	if got == nil || len(got) != 0 {
		t.Fatalf("empty answer must be a non-nil empty slice, got %#v", got)
	}
}

func TestInvDoesNotMarkItemsSeen(t *testing.T) {
	n, _ := gossipNode(t, newLedger())

	for range 2 {
		got, _ := n.Inv(idA, []InvItem{{InvBlock, hashX}})
		if len(got) != 1 {
			t.Fatalf("got %v", got)
		}
	}
}

func TestInvValidation(t *testing.T) {
	n, _ := gossipNode(t, newLedger())
	many := make([]InvItem, MaxInvItems+1)

	for i := range many {
		many[i] = InvItem{InvBlock, hashX}
	}

	cases := []struct {
		name  string
		from  string
		items []InvItem
		want  error
	}{
		{"unknown peer", idB, []InvItem{{InvBlock, hashX}}, ErrUnknownPeer},
		{"empty", idA, nil, ErrBadInv},
		{"too many", idA, many, ErrBadInv},
		{"bad type", idA, []InvItem{{"header", hashX}}, ErrBadInv},
		{"upper hash", idA, []InvItem{{InvBlock, strings.ToUpper(hashX)}}, ErrBadInv},
		{"short hash", idA, []InvItem{{InvBlock, "abc"}}, ErrBadInv},
		{"second item bad", idA, []InvItem{{InvBlock, hashX}, {InvTx, "z"}}, ErrBadInv},
	}
	for _, tc := range cases {
		_, err := n.Inv(tc.from, tc.items)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v", tc.name, err)
		}
	}

	_, err := n.Inv(idA, many[:MaxInvItems])
	if err != nil {
		t.Fatalf("exactly MaxInvItems must pass: %v", err)
	}
}

func TestGossipDisabledWithoutLedger(t *testing.T) {
	n, err := NewNode(chain1(), NewBook(Limits{}, nil), idE, "")
	if err != nil {
		t.Fatal(err)
	}

	connect(t, n, idA, "")

	_, err = n.Inv(idA, []InvItem{{InvBlock, hashX}})
	if !errors.Is(err, ErrGossipDisabled) {
		t.Fatal(err)
	}

	_, err = n.ReceiveBlock(context.Background(), idA, blk(hashX))
	if !errors.Is(err, ErrGossipDisabled) {
		t.Fatal(err)
	}

	_, err = n.ReceiveTx(idA, domain.Transaction{ID: hashY})
	if !errors.Is(err, ErrGossipDisabled) {
		t.Fatal(err)
	}

	n.AnnounceBlock(blk(hashX))
	n.AnnounceTx(domain.Transaction{ID: hashY})

	if len(n.queue) != 0 || n.seen.has(hashX) {
		t.Fatal("announce did something without a ledger")
	}
}

func TestAnnounceOffersToEveryAddressedPeer(t *testing.T) {
	n, peers := gossipNode(t, newLedger())
	a := peers[0]
	c := peers[1]
	d := peers[2]

	connect(t, n, idB, "") // no address: skipped

	n.AnnounceBlock(blk(hashX))
	n.AnnounceTx(domain.Transaction{ID: hashY})

	for _, p := range []*fakePeer{a, c, d} {
		p.waitFor(t, 4)
	}

	if !n.seen.has(hashX) || !n.seen.has(hashY) {
		t.Fatal("announced hashes must be seen")
	}

	_, err := n.ReceiveBlock(context.Background(), idA, blk(hashX))
	if err != nil {
		t.Fatal(err)
	}
}

func TestPeerThatWantsNothingGetsNoObject(t *testing.T) {
	n, peers := gossipNode(t, newLedger())
	a := peers[0]
	a.wantNothing = true

	n.AnnounceBlock(blk(hashX))
	a.waitFor(t, 1)
	time.Sleep(50 * time.Millisecond)

	if got := a.paths(); len(got) != 1 || got[0] != "/p2p/inv" {
		t.Fatalf("paths = %v", got)
	}
}

func TestRelayFailureIsLoggedAndKeepsPeerConnected(t *testing.T) {
	var buf syncBuf

	log := slogTo(&buf)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", http.StatusInternalServerError) }))
	t.Cleanup(bad.Close)

	good := newFakePeer(t)

	n, err := NewNode(chain1(), NewBook(Limits{}, nil), idE, "", WithLedger(newLedger()), WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}

	connect(t, n, idA, bad.URL)
	connect(t, n, idC, good.srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() { n.relayLoop(ctx); close(done) }()

	t.Cleanup(func() { cancel(); <-done })

	n.AnnounceBlock(blk(hashX))
	good.waitFor(t, 2)

	if !strings.Contains(buf.String(), "relay failed") || len(n.Peers()) != 2 {
		t.Fatalf("log=%q peers=%d", buf.String(), len(n.Peers()))
	}
}

func TestAnnounceWithFullQueueReturnsAtOnce(t *testing.T) {
	var buf syncBuf

	n, err := NewNode(chain1(), NewBook(Limits{}, nil), idE, "", WithLedger(newLedger()), WithLogger(slogTo(&buf)))
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})

	go func() {
		for range relayQueueSize + 10 {
			n.AnnounceBlock(blk(hashX))
		}

		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Announce blocked")
	}

	if len(n.queue) != relayQueueSize || !strings.Contains(buf.String(), "queue full") {
		t.Fatalf("queue=%d log=%q", len(n.queue), buf.String())
	}
}

func TestSeenSetIsBoundedAndEvictsOldestFirst(t *testing.T) {
	s := newSeenSet(3)
	for _, h := range []string{"a", "b", "c", "d"} {
		if !s.add(h) {
			t.Fatal(h)
		}
	}

	if s.has("a") || !s.has("b") || !s.has("d") || s.order.Len() != 3 || len(s.items) != 3 {
		t.Fatal("oldest not evicted")
	}

	if s.add("b") {
		t.Fatal("add of a present hash must report false")
	}

	s.add("e") // b is still the oldest: re-adding does not refresh it

	if s.has("b") || !s.has("c") {
		t.Fatal("re-add refreshed the entry")
	}

	s.remove("c")
	s.remove("zzz")

	if s.has("c") || s.order.Len() != 2 || len(s.items) != 2 {
		t.Fatal("remove")
	}
}

func TestWithMaxSeen(t *testing.T) {
	mk := func(opts ...Option) *Node {
		n, err := NewNode(chain1(), NewBook(Limits{}, nil), idE, "", opts...)
		if err != nil {
			t.Fatal(err)
		}

		return n
	}

	if mk().seen.limit != DefaultMaxSeen || mk(WithMaxSeen(0)).seen.limit != DefaultMaxSeen || mk(WithMaxSeen(-1)).seen.limit != DefaultMaxSeen {
		t.Fatal("default expected")
	}

	if mk(WithMaxSeen(7)).seen.limit != 7 {
		t.Fatal("custom expected")
	}

	if DefaultMaxSeen != 10000 {
		t.Fatal("DefaultMaxSeen")
	}
}

func TestRunStopsRelayWorkerOnCancel(t *testing.T) {
	defer goleak.VerifyNone(t)

	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	p := newFakePeer(t)

	n, err := NewNode(chain1(), NewBook(Limits{}, nil), idE, "", WithLedger(newLedger()), WithInterval(time.Hour), WithClient(client))
	if err != nil {
		t.Fatal(err)
	}

	connect(t, n, idA, p.srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() { n.Run(ctx); close(done) }()

	n.AnnounceBlock(blk(hashX))
	p.waitFor(t, 2) // the worker started by Run relayed it

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}

	p.srv.Close()
}

func slogTo(w io.Writer) *slog.Logger { return slog.New(slog.NewTextHandler(w, nil)) }

func TestSuccessfulRelayLogsNothing(t *testing.T) {
	var buf syncBuf

	good := newFakePeer(t)

	n, err := NewNode(chain1(), NewBook(Limits{}, nil), idE, "", WithLedger(newLedger()), WithLogger(slogTo(&buf)))
	if err != nil {
		t.Fatal(err)
	}

	connect(t, n, idC, good.srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() { n.relayLoop(ctx); close(done) }()

	n.AnnounceBlock(blk(hashX))
	good.waitFor(t, 2)
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if buf.String() != "" {
		t.Fatalf("log = %q", buf.String())
	}
}
