package p2p

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

var syncT0 = time.Unix(1_700_000_000, 0).UTC()

func syncID(i int) string { return fmt.Sprintf("%032x", i) }

func sealed(b *domain.Block) *domain.Block {
	b.Hash = b.CalculateHash()
	return b
}

// grow returns base extended by one block per difficulty, all sealed by validator.
func grow(base []*domain.Block, validator string, diffs ...int) []*domain.Block {
	out := slices.Clone(base)
	if len(out) == 0 {
		out = append(out, sealed(&domain.Block{Timestamp: syncT0, MerkleRoot: domain.MerkleRoot(nil)}))
	}

	for _, d := range diffs {
		prev := out[len(out)-1]
		out = append(out, sealed(&domain.Block{
			Index: prev.Index + 1, Timestamp: syncT0.Add(time.Duration(prev.Index+1) * time.Second),
			MerkleRoot: domain.MerkleRoot(nil), PrevHash: linkHash(prev), Difficulty: d, Validator: validator,
		}))
	}

	return out
}

func sameChain(a, b []*domain.Block) bool {
	return slices.EqualFunc(a, b, func(x, y *domain.Block) bool { return x.Hash == y.Hash })
}

func ones(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = 1
	}

	return out
}

// syncLedger is a fake SyncLedger over an in-memory slice.
type syncLedger struct {
	mu         sync.Mutex
	blocks     []*domain.Block
	accepted   []int
	replaced   [][]*domain.Block
	acceptErr  func(*domain.Block) error
	replaceErr func([]*domain.Block) error

	rangeReqs            []string // "from/limit" of every BlocksRange call
	tipCalls, rangeCalls int
	failTip, failRange   int  // the call to fail, counting from 1; 0 never fails
	emptyRange           bool // the failing BlocksRange answers an empty page instead of an error
}

func newSyncLedger(blocks []*domain.Block) *syncLedger {
	return &syncLedger{blocks: slices.Clone(blocks)}
}

func (l *syncLedger) Tip() (*domain.Block, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.tipCalls++
	if l.tipCalls == l.failTip {
		return nil, errors.New("tip unavailable")
	}

	return l.blocks[len(l.blocks)-1], nil
}

func (l *syncLedger) BlockByHeight(h int) (*domain.Block, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.blocks[h], nil
}

func (l *syncLedger) BlocksRange(from, limit int) ([]*domain.Block, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.rangeCalls++

	l.rangeReqs = append(l.rangeReqs, fmt.Sprintf("%d/%d", from, limit))
	if l.rangeCalls == l.failRange {
		if l.emptyRange {
			return nil, nil
		}

		return nil, errors.New("range unavailable")
	}

	if from >= len(l.blocks) {
		return nil, nil
	}

	return slices.Clone(l.blocks[from:min(from+limit, len(l.blocks))]), nil
}

func (l *syncLedger) HasBlock(hash string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	return slices.ContainsFunc(l.blocks, func(b *domain.Block) bool { return b.Hash == hash })
}

func (l *syncLedger) AcceptBlock(_ context.Context, b *domain.Block) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.acceptErr != nil {
		err := l.acceptErr(b)
		if err != nil {
			return err
		}
	}

	if b.PrevHash != linkHash(l.blocks[len(l.blocks)-1]) {
		return ErrNotOnTip
	}

	l.blocks = append(l.blocks, b)
	l.accepted = append(l.accepted, b.Index)

	return nil
}

func (l *syncLedger) ReplaceChain(_ context.Context, candidate []*domain.Block) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.replaceErr != nil {
		err := l.replaceErr(candidate)
		if err != nil {
			return err
		}
	}

	l.replaced = append(l.replaced, slices.Clone(candidate))
	l.blocks = slices.Clone(candidate)

	return nil
}

func (l *syncLedger) height() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return len(l.blocks) - 1
}

func (l *syncLedger) tipValidator() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.blocks[len(l.blocks)-1].Validator
}

// syncPeer is a fake peer serving /headers, /blocks and a hello over a chain. The hook may rewrite
// a page (returning the blocks to send) or answer by itself (returning ok=true).
type syncPeer struct {
	srv   *httptest.Server
	mu    sync.Mutex
	chain []*domain.Block
	reqs  []string
	hook  func(w http.ResponseWriter, r *http.Request, route string, from, limit int, page []*domain.Block) (out []*domain.Block, handled bool)
}

func newSyncPeer(t *testing.T, chain []*domain.Block) *syncPeer {
	t.Helper()

	p := &syncPeer{chain: chain}
	p.srv = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.srv.Close)

	return p
}

func (p *syncPeer) serve(w http.ResponseWriter, r *http.Request) {
	route := strings.TrimPrefix(r.URL.Path, "/")

	if route == "p2p/hello" {
		tip := p.chain[len(p.chain)-1]
		_ = json.NewEncoder(w).Encode(Hello{Version: ProtocolVersion, NodeID: syncID(99), GenesisHash: p.chain[0].Hash, Height: tip.Index, TipHash: tip.Hash, ListenAddr: p.srv.URL})

		return
	}

	if route == "p2p/peers" {
		_ = json.NewEncoder(w).Encode(map[string]any{"peers": []any{}, "addrs": []string{}})
		return
	}

	from, _ := strconv.Atoi(r.URL.Query().Get("from"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	p.mu.Lock()
	p.reqs = append(p.reqs, fmt.Sprintf("%s?from=%d&limit=%d", route, from, limit))

	var page []*domain.Block

	if from < len(p.chain) {
		for _, b := range p.chain[from:min(from+limit, len(p.chain))] {
			c := *b
			if route == "headers" {
				c.Transactions = nil
			}

			page = append(page, &c)
		}
	}

	hook := p.hook
	p.mu.Unlock()

	if hook != nil {
		out, handled := hook(w, r, route, from, limit, page)
		if handled {
			return
		}

		page = out
	}

	_ = json.NewEncoder(w).Encode(map[string]any{"blocks": page, "from": from, "limit": limit})
}

func (p *syncPeer) requests() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return slices.Clone(p.reqs)
}

func (p *syncPeer) hello() Hello {
	tip := p.chain[len(p.chain)-1]
	return Hello{NodeID: "", Height: tip.Index, TipHash: tip.Hash, ListenAddr: p.srv.URL}
}

// syncClient never keeps connections alive, so no goroutine outlives a test.
func syncClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: &http.Transport{DisableKeepAlives: true}}
}

func newSyncNode(t *testing.T, l *syncLedger, opts ...Option) *Node {
	t.Helper()

	opts = append([]Option{WithSyncLedger(l), WithClient(syncClient(5 * time.Second))}, opts...)

	n, err := NewNode(l, NewBook(Limits{}, nil), syncID(1000), "", opts...)
	if err != nil {
		t.Fatal(err)
	}

	return n
}

func (n *Node) connectTo(t *testing.T, id string, p *syncPeer) {
	t.Helper()

	h := p.hello()
	h.NodeID = id

	err := n.book.Connect(h, false)
	if err != nil {
		t.Fatal(err)
	}
}

func hasPeer(n *Node, id string) bool {
	_, ok := peerByID(n.Peers(), id)
	return ok
}

func TestSyncOffWithoutLedger(t *testing.T) {
	peer := newSyncPeer(t, grow(nil, "A", ones(3)...))
	n, _ := NewNode(chain1(), NewBook(Limits{}, nil), idA, "")
	n.connectTo(t, idB, peer)

	if n.Sync(context.Background()) || len(peer.requests()) != 0 {
		t.Fatalf("sync ran without a ledger: %v", peer.requests())
	}

	n2, _ := NewNode(chain1(), NewBook(Limits{}, nil), idA, "", WithSyncLedger(nil))
	if n2.sync != nil || n2.Sync(context.Background()) {
		t.Fatal("WithSyncLedger(nil) must keep sync off")
	}
}

func TestSyncConstants(t *testing.T) {
	if SyncAttemptTimeout != 2*time.Minute || HeaderBatch != 1000 || BodyBatch != 16 || DefaultBanDuration != time.Hour {
		t.Fatal("sync constants changed")
	}

	n := newSyncNode(t, newSyncLedger(grow(nil, "")))
	if n.syncTimeout != SyncAttemptTimeout || n.banFor != DefaultBanDuration {
		t.Fatalf("defaults = %v %v", n.syncTimeout, n.banFor)
	}
}

func TestWithBanDuration(t *testing.T) {
	for _, tc := range []struct {
		in, want time.Duration
	}{{0, time.Hour}, {-time.Second, time.Hour}, {3 * time.Second, 3 * time.Second}} {
		n := newSyncNode(t, newSyncLedger(grow(nil, "")), WithBanDuration(tc.in))
		if n.banFor != tc.want {
			t.Fatalf("WithBanDuration(%v) = %v, want %v", tc.in, n.banFor, tc.want)
		}
	}
}

func TestSyncDownloadsHeadersThenBodies(t *testing.T) {
	canonical := grow(nil, "A", ones(40)...)
	l := newSyncLedger(canonical[:1])
	peer := newSyncPeer(t, canonical)
	n := newSyncNode(t, l)
	n.connectTo(t, idA, peer)

	if !n.Sync(context.Background()) {
		t.Fatal("Sync = false")
	}

	want := make([]int, 40)
	for i := range want {
		want[i] = i + 1
	}

	if !slices.Equal(l.accepted, want) || len(l.replaced) != 0 {
		t.Fatalf("accepted %v, replaced %d", l.accepted, len(l.replaced))
	}

	wantReqs := []string{
		"headers?from=0&limit=1", "headers?from=1&limit=1000",
		"blocks?from=1&limit=16", "blocks?from=17&limit=16", "blocks?from=33&limit=8",
	}
	if got := peer.requests(); !slices.Equal(got, wantReqs) {
		t.Fatalf("requests = %v, want %v", got, wantReqs)
	}

	if n.Sync(context.Background()) {
		t.Fatal("a second Sync with nothing new reported a change")
	}
}

// emptyGenesis is the chain as chain.Service builds it: the genesis block is
// never sealed (Hash == "") and block 1 links to it with PrevHash == "".
func emptyGenesis(blocks int) []*domain.Block {
	canonical := grow(nil, "A", ones(blocks)...)
	canonical[0].Hash = ""
	canonical[1].PrevHash = ""
	canonical[1].Hash = canonical[1].CalculateHash()

	for i := 2; i < len(canonical); i++ {
		canonical[i].PrevHash = canonical[i-1].Hash
		canonical[i].Hash = canonical[i].CalculateHash()
	}

	return canonical
}

func TestSyncFromAnUnsealedGenesis(t *testing.T) {
	canonical := emptyGenesis(5)
	l := newSyncLedger(canonical[:1])
	n := newSyncNode(t, l)
	n.connectTo(t, idA, newSyncPeer(t, canonical))

	if !n.Sync(context.Background()) || l.height() != 5 {
		t.Fatalf("synced to %d, want 5", l.height())
	}

	if !hasPeer(n, idA) {
		t.Fatal("an honest peer was banned")
	}
}

func TestCheckHeaderLinksToAnUnsealedGenesis(t *testing.T) {
	canonical := emptyGenesis(2)

	err := checkHeader(canonical[1], 0, 1, canonical[0])
	if err != nil {
		t.Fatalf("honest child of the genesis rejected: %v", err)
	}

	bad := *canonical[1]
	bad.PrevHash = "ff"
	bad.Hash = bad.CalculateHash()

	err = checkHeader(&bad, 0, 1, canonical[0])
	if err == nil || !strings.Contains(err.Error(), "does not link to its parent") {
		t.Fatalf("want header 1 does not link to its parent, got %v", err)
	}
}

func TestSyncPagesHeaders(t *testing.T) {
	for _, tc := range []struct {
		blocks int
		pages  []string
	}{
		{999, []string{"headers?from=1&limit=1000"}},
		{1000, []string{"headers?from=1&limit=1000", "headers?from=1001&limit=1000"}},
		{1001, []string{"headers?from=1&limit=1000", "headers?from=1001&limit=1000"}},
	} {
		canonical := grow(nil, "A", ones(tc.blocks)...)
		l := newSyncLedger(canonical[:1])
		peer := newSyncPeer(t, canonical)
		n := newSyncNode(t, l)
		n.connectTo(t, idA, peer)

		if !n.Sync(context.Background()) || l.height() != tc.blocks {
			t.Fatalf("%d blocks: synced to %d", tc.blocks, l.height())
		}

		var headers []string

		for _, r := range peer.requests() {
			if strings.HasPrefix(r, "headers?from=1") && r != "headers?from=1&limit=1" {
				headers = append(headers, r)
			}
		}

		if !slices.Equal(headers, tc.pages) {
			t.Fatalf("%d blocks: header requests = %v, want %v", tc.blocks, headers, tc.pages)
		}
	}
}

func TestSyncReorgToTheBranchWithMoreWork(t *testing.T) {
	base := grow(nil, "A", ones(3)...) // heights 0..3
	x := grow(base, "X", 1, 1)         // local, work 4 above the fork
	y := grow(base, "Y", 1, 2, 1)      // peer: 2, then 4 (total 6 > 4 at height 5), then one more

	l := newSyncLedger(x)
	peer := newSyncPeer(t, y)
	n := newSyncNode(t, l)
	n.connectTo(t, idA, peer)

	if !n.Sync(context.Background()) {
		t.Fatal("Sync = false")
	}

	if len(l.replaced) != 1 || !sameChain(l.replaced[0], y[:6]) {
		t.Fatalf("ReplaceChain calls = %d (%v)", len(l.replaced), l.replaced)
	}

	if !slices.Equal(l.accepted, []int{6}) {
		t.Fatalf("accepted %v, want [6]", l.accepted)
	}

	if l.height() != 6 || l.tipValidator() != "Y" {
		t.Fatalf("height %d", l.height())
	}
}

func TestSyncReorgFromTheSameHeight(t *testing.T) {
	base := grow(nil, "A", ones(2)...)
	x := grow(base, "X", 1)
	y := grow(base, "Y", 5)

	l := newSyncLedger(x)
	peer := newSyncPeer(t, y)
	n := newSyncNode(t, l)
	n.connectTo(t, idA, peer)

	if !n.Sync(context.Background()) || len(l.replaced) != 1 || len(l.accepted) != 0 || l.tipValidator() != "Y" {
		t.Fatalf("replaced %d accepted %v", len(l.replaced), l.accepted)
	}
}

func TestSyncKeepsTheBranchWithLessOrEqualWork(t *testing.T) {
	base := grow(nil, "A", ones(2)...)
	x := grow(base, "X", 3, 3)

	for name, y := range map[string][]*domain.Block{
		"equal": grow(base, "Y", 3, 3, 0)[:4],
		"less":  grow(base, "Y", 3, 2, 1),
	} {
		t.Run(name, func(t *testing.T) {
			l := newSyncLedger(x)
			peer := newSyncPeer(t, y)
			n := newSyncNode(t, l)
			n.connectTo(t, idA, peer)

			if n.Sync(context.Background()) || len(l.replaced) != 0 || len(l.accepted) != 0 {
				t.Fatalf("changed the chain: %v", l.accepted)
			}

			if n.book.Banned(idA, peer.srv.URL) || !hasPeer(n, idA) {
				t.Fatal("an honest shorter branch must not cost the peer")
			}
		})
	}
}

func TestSyncReorgFetchesBodiesInBatches(t *testing.T) {
	base := grow(nil, "A", ones(2)...)
	x := grow(base, "X", ones(17)...) // 17 blocks above the fork
	y := grow(base, "Y", ones(19)...)

	l := newSyncLedger(x)
	peer := newSyncPeer(t, y)
	n := newSyncNode(t, l)
	n.connectTo(t, idA, peer)

	if !n.Sync(context.Background()) {
		t.Fatal("Sync = false")
	}

	// 18 blocks beat 17: two body pages (16 + 2), then the last block by AcceptBlock.
	var bodies []string

	for _, r := range peer.requests() {
		if strings.HasPrefix(r, "blocks") {
			bodies = append(bodies, r)
		}
	}

	if !slices.Equal(bodies, []string{"blocks?from=3&limit=16", "blocks?from=19&limit=2", "blocks?from=21&limit=1"}) {
		t.Fatalf("body requests = %v", bodies)
	}

	if len(l.replaced) != 1 || len(l.replaced[0]) != 3+18 || !slices.Equal(l.accepted, []int{21}) {
		t.Fatalf("replaced %d, accepted %v", len(l.replaced), l.accepted)
	}
}

func TestSyncFindsAForkBelowTheWindow(t *testing.T) {
	base := grow(nil, "A", ones(50)...)
	x := grow(base, "X", ones(1050)...) // height 1100
	y := grow(base, "Y", ones(1051)...) // height 1101

	l := newSyncLedger(x)
	peer := newSyncPeer(t, y)
	n := newSyncNode(t, l)
	n.connectTo(t, idA, peer)

	if !n.Sync(context.Background()) || l.height() != 1101 || l.tipValidator() != "Y" {
		t.Fatalf("height %d", l.height())
	}

	if len(l.replaced) != 1 || len(l.replaced[0]) != 1102 || len(l.accepted) != 0 {
		t.Fatalf("replaced %d accepted %d", len(l.replaced), len(l.accepted))
	}

	reqs := peer.requests()

	if !slices.Contains(l.rangeReqs, "51/1000") || !slices.Contains(l.rangeReqs, "1051/50") {
		t.Fatalf("local ranges = %v", l.rangeReqs)
	}

	if reqs[0] != "headers?from=101&limit=1000" || reqs[1] != "headers?from=0&limit=101" {
		t.Fatalf("window requests = %v", reqs[:2])
	}
}

func TestSyncBansTheMisbehavingPeerAndTriesTheNext(t *testing.T) {
	canonical := grow(nil, "A", ones(40)...)
	cases := headerLies(canonical)
	maps.Copy(cases, bodyLies(canonical))

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			clock := time.Unix(5_000, 0)
			l := newSyncLedger(canonical[:1])
			bad := newSyncPeer(t, canonical)
			bad.hook = func(_ http.ResponseWriter, _ *http.Request, route string, from, limit int, page []*domain.Block) ([]*domain.Block, bool) {
				return mutate(route, from, limit, page), false
			}
			good := newSyncPeer(t, canonical)
			n := newSyncNode(t, l, WithBanDuration(time.Minute))
			n.now = func() time.Time { return clock }
			n.book.now = n.now
			n.connectTo(t, syncID(1), bad)
			n.connectTo(t, syncID(2), good)

			if !n.Sync(context.Background()) || l.height() != 40 {
				t.Fatalf("height %d; the next candidate was not tried", l.height())
			}

			assertBanned(t, n, syncID(1), bad.srv.URL)

			if len(good.requests()) == 0 || hasPeer(n, syncID(2)) == false {
				t.Fatalf("the honest peer was not used or lost: %v", good.requests())
			}

			clock = clock.Add(time.Minute - time.Nanosecond)

			if !n.book.Banned(syncID(1), bad.srv.URL) {
				t.Fatal("the ban ended early")
			}

			clock = clock.Add(time.Nanosecond)

			if n.book.Banned(syncID(1), bad.srv.URL) {
				t.Fatal("the ban did not end")
			}
		})
	}
}

type pageMutation = func(route string, from, limit int, page []*domain.Block) []*domain.Block

// headerLies maps each way a peer can lie about the headers of canonical to the rewrite of the page it serves.
// orig returns a copy of page that a hook may edit.
func orig(page []*domain.Block) []*domain.Block {
	out := make([]*domain.Block, len(page))
	for i, b := range page {
		c := *b
		out[i] = &c
	}

	return out
}

func headerLies(canonical []*domain.Block) map[string]pageMutation {
	return map[string]pageMutation{
		"header with a wrong hash": func(route string, from, _ int, page []*domain.Block) []*domain.Block {
			if route == "headers" && from == 1 {
				page = orig(page)
				page[len(page)-1].Hash = strings.Repeat("0", 64)
			}

			return page
		},
		"header that does not link": func(route string, from, _ int, page []*domain.Block) []*domain.Block {
			if route == "headers" && from == 1 {
				page = orig(page)
				page[3].PrevHash = strings.Repeat("0", 64)
				page[3] = sealed(page[3])
			}

			return page
		},
		"first header that does not link to the tip": func(route string, from, _ int, page []*domain.Block) []*domain.Block {
			if route == "headers" && from == 1 {
				page = orig(page)
				page[0].PrevHash = strings.Repeat("0", 64)
				page[0] = sealed(page[0])
			}

			return page
		},
		"header out of order": func(route string, from, _ int, page []*domain.Block) []*domain.Block {
			if route == "headers" && from == 1 {
				page = orig(page)
				page[2], page[3] = page[3], page[2]
			}

			return page
		},
		"header with the wrong first height": func(route string, from, _ int, page []*domain.Block) []*domain.Block {
			if route == "headers" && from == 1 {
				return page[1:]
			}

			return page
		},
		"null header": func(route string, from, _ int, page []*domain.Block) []*domain.Block {
			if route == "headers" && from == 1 {
				page = orig(page)
				page[2] = nil
			}

			return page
		},
		"header with an impossible difficulty": func(route string, from, _ int, page []*domain.Block) []*domain.Block {
			if route == "headers" && from == 1 {
				page = orig(page)
				page[2].Difficulty = 257
				page[2] = sealed(page[2])
			}

			return page
		},
		"header with a negative difficulty": func(route string, from, _ int, page []*domain.Block) []*domain.Block {
			if route == "headers" && from == 1 {
				page = orig(page)
				page[2].Difficulty = -1
				page[2] = sealed(page[2])
			}

			return page
		},
		"header page longer than asked": func(route string, from, limit int, page []*domain.Block) []*domain.Block {
			if route == "headers" && from == 1 {
				return append(orig(page), canonical[1])
			}

			return page
		},
		"short window": func(route string, from, _ int, page []*domain.Block) []*domain.Block {
			if route == "headers" && from == 0 {
				return nil
			}

			return page
		},
		"another genesis": func(route string, from, _ int, page []*domain.Block) []*domain.Block {
			if route == "headers" && from == 0 {
				page = orig(page)
				page[0].Validator = "other"
				page[0] = sealed(page[0])
			}

			return page
		},
	}
}

// bodyLies maps each way a peer can lie about the block bodies of canonical to the rewrite of the page it serves.
func bodyLies(canonical []*domain.Block) map[string]pageMutation {
	return map[string]pageMutation{
		"body with another hash": func(route string, from, _ int, page []*domain.Block) []*domain.Block {
			if route == "blocks" && from == 1 {
				page = orig(page)
				page[1].Hash = strings.Repeat("0", 64)
			}

			return page
		},
		"body with another height": func(route string, from, _ int, page []*domain.Block) []*domain.Block {
			if route == "blocks" && from == 1 {
				page = orig(page)
				page[1].Index = 99
			}

			return page
		},
		"null body": func(route string, from, _ int, page []*domain.Block) []*domain.Block {
			if route == "blocks" && from == 1 {
				page = orig(page)
				page[1] = nil
			}

			return page
		},
		"short body page": func(route string, from, _ int, page []*domain.Block) []*domain.Block {
			if route == "blocks" && from == 1 {
				return page[:15]
			}

			return page
		},
		"long body page": func(route string, from, _ int, page []*domain.Block) []*domain.Block {
			if route == "blocks" && from == 1 {
				return append(orig(page), canonical[1])
			}

			return page
		},
		"body with transactions and a wrong merkle root": func(route string, from, _ int, page []*domain.Block) []*domain.Block {
			if route == "blocks" && from == 1 {
				page = orig(page)
				page[1].Transactions = []domain.Transaction{{ID: "x"}}
			}

			return page
		},
	}
}

func assertBanned(t *testing.T, n *Node, id, addr string) {
	t.Helper()

	if hasPeer(n, id) {
		t.Fatal("the banned peer is still connected")
	}

	h := Hello{NodeID: id, ListenAddr: addr}

	err := n.book.Connect(h, false)
	if !errors.Is(err, ErrBanned) {
		t.Fatalf("Connect = %v, want ErrBanned", err)
	}

	if slices.Contains(n.book.Candidates(), addr) {
		t.Fatal("Candidates returned the banned address")
	}
}

func TestSyncBansWhenTheLedgerRejectsTheBlocks(t *testing.T) {
	base := grow(nil, "A", ones(2)...)
	reject := errors.New("invalid block")

	t.Run("AcceptBlock", func(t *testing.T) {
		l := newSyncLedger(base[:1])
		l.acceptErr = func(b *domain.Block) error {
			if b.Validator == "bad" {
				return reject
			}

			return nil
		}
		bad := newSyncPeer(t, grow(base[:1], "bad", ones(5)...))
		good := newSyncPeer(t, grow(base[:1], "good", ones(5)...))
		n := newSyncNode(t, l)
		n.connectTo(t, syncID(1), bad)
		n.connectTo(t, syncID(2), good)

		if !n.Sync(context.Background()) || l.tipValidator() != "good" || l.height() != 5 {
			t.Fatalf("height %d validator %s", l.height(), l.tipValidator())
		}

		assertBanned(t, n, syncID(1), bad.srv.URL)
	})

	t.Run("ReplaceChain", func(t *testing.T) {
		x := grow(base, "X", 1)
		l := newSyncLedger(x)
		l.replaceErr = func(c []*domain.Block) error {
			if c[len(c)-1].Validator == "bad" {
				return reject
			}

			return nil
		}
		bad := newSyncPeer(t, grow(base, "bad", 4, 4))
		good := newSyncPeer(t, grow(base, "good", 4, 4))
		n := newSyncNode(t, l)
		n.connectTo(t, syncID(1), bad)
		n.connectTo(t, syncID(2), good)

		if !n.Sync(context.Background()) || l.tipValidator() != "good" {
			t.Fatalf("validator %s", l.tipValidator())
		}

		assertBanned(t, n, syncID(1), bad.srv.URL)
	})
}

func TestSyncStopsWithoutBanOnLedgerOutcomes(t *testing.T) {
	base := grow(nil, "A", ones(2)...)

	for name, err := range map[string]error{
		"insufficient work": errors.Join(errors.New("wrap"), ErrInsufficientWork),
		"not on tip":        errors.Join(errors.New("wrap"), ErrNotOnTip),
		"local failure":     errors.Join(errors.New("wrap"), ErrLocalFailure),
		"pruned":            errors.Join(errors.New("wrap"), ErrPeerPruned),
	} {
		t.Run(name+" on AcceptBlock", func(t *testing.T) {
			l := newSyncLedger(base[:1])
			l.acceptErr = func(*domain.Block) error { return err }
			peer := newSyncPeer(t, base)
			n := newSyncNode(t, l)
			n.connectTo(t, idA, peer)

			if n.Sync(context.Background()) || n.book.Banned(idA, peer.srv.URL) || !hasPeer(n, idA) {
				t.Fatal("expected a quiet stop")
			}
		})

		t.Run(name+" on ReplaceChain", func(t *testing.T) {
			l := newSyncLedger(grow(base, "X", 1))
			l.replaceErr = func([]*domain.Block) error { return err }
			peer := newSyncPeer(t, grow(base, "Y", 4, 4))
			n := newSyncNode(t, l)
			n.connectTo(t, idA, peer)

			if n.Sync(context.Background()) || n.book.Banned(idA, peer.srv.URL) || !hasPeer(n, idA) {
				t.Fatal("expected a quiet stop")
			}
		})
	}
}

func TestSyncDisconnectsWithoutBanOnRequestFailure(t *testing.T) {
	canonical := grow(nil, "A", ones(5)...)

	cases := map[string]func(w http.ResponseWriter, r *http.Request, route string, from int) bool{
		"timeout": func(_ http.ResponseWriter, r *http.Request, _ string, from int) bool {
			if from == 1 {
				select {
				case <-r.Context().Done():
				case <-time.After(5 * time.Second):
				}

				return true
			}

			return false
		},
		"server error": func(w http.ResponseWriter, _ *http.Request, _ string, from int) bool {
			if from == 1 {
				http.Error(w, "boom", http.StatusInternalServerError)
				return true
			}

			return false
		},
		"garbage": func(w http.ResponseWriter, _ *http.Request, _ string, from int) bool {
			if from == 1 {
				_, _ = w.Write([]byte("not json"))
				return true
			}

			return false
		},
	}
	for name, handle := range cases {
		t.Run(name, func(t *testing.T) {
			l := newSyncLedger(canonical[:1])
			slow := newSyncPeer(t, canonical)
			slow.hook = func(w http.ResponseWriter, r *http.Request, route string, from, _ int, page []*domain.Block) ([]*domain.Block, bool) {
				return page, handle(w, r, route, from)
			}
			good := newSyncPeer(t, canonical)
			n := newSyncNode(t, l, WithClient(syncClient(100*time.Millisecond)))
			n.connectTo(t, syncID(1), slow)
			n.connectTo(t, syncID(2), good)

			if !n.Sync(context.Background()) || l.height() != 5 {
				t.Fatalf("height %d", l.height())
			}

			if hasPeer(n, syncID(1)) || n.book.Banned(syncID(1), slow.srv.URL) {
				t.Fatal("a failing peer is disconnected, not banned")
			}
		})
	}
}

func TestSyncPeerPruned(t *testing.T) {
	canonical := grow(nil, "A", ones(5)...)
	gone := func(w http.ResponseWriter, _ *http.Request, _ string, _, _ int, _ []*domain.Block) ([]*domain.Block, bool) {
		http.Error(w, `{"code":"block_pruned"}`, http.StatusGone)
		return nil, true
	}
	stripped := func(_ http.ResponseWriter, _ *http.Request, route string, _, _ int, page []*domain.Block) ([]*domain.Block, bool) {
		if route == "blocks" {
			for _, b := range page {
				b.MerkleRoot = strings.Repeat("0", 64)
			}
		}

		return page, false
	}

	for name, hook := range map[string]func(http.ResponseWriter, *http.Request, string, int, int, []*domain.Block) ([]*domain.Block, bool){"410": gone, "no transactions": stripped} {
		t.Run(name, func(t *testing.T) {
			l := newSyncLedger(canonical[:1])
			pruned := newSyncPeer(t, canonical)
			pruned.hook = hook
			good := newSyncPeer(t, canonical)
			n := newSyncNode(t, l)
			n.connectTo(t, syncID(1), pruned)
			n.connectTo(t, syncID(2), good)

			if !n.Sync(context.Background()) || l.height() != 5 {
				t.Fatalf("height %d", l.height())
			}

			if !hasPeer(n, syncID(1)) || n.book.Banned(syncID(1), pruned.srv.URL) {
				t.Fatal("a pruned peer is neither banned nor disconnected")
			}
		})
	}
}

func TestSyncCancelledReturnsWithoutLeaks(t *testing.T) {
	defer goleak.VerifyNone(t)

	canonical := grow(nil, "A", ones(5)...)
	started := make(chan struct{}, 1)
	peer := newSyncPeer(t, canonical)
	peer.hook = func(_ http.ResponseWriter, r *http.Request, _ string, from, _ int, page []*domain.Block) ([]*domain.Block, bool) {
		if from == 1 {
			started <- struct{}{}

			<-r.Context().Done()

			return nil, true
		}

		return page, false
	}

	defer peer.srv.Close()

	l := newSyncLedger(canonical[:1])
	n := newSyncNode(t, l)
	n.connectTo(t, idA, peer)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool)

	go func() { done <- n.Sync(ctx) }()

	<-started
	cancel()

	select {
	case changed := <-done:
		if changed {
			t.Fatal("changed the tip")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Sync did not return after the cancel")
	}

	if !hasPeer(n, idA) || n.book.Banned(idA, peer.srv.URL) {
		t.Fatal("a cancel must not cost the peer")
	}
}

func TestSyncAlreadyCancelledMakesNoRequest(t *testing.T) {
	canonical := grow(nil, "A", ones(5)...)
	peer := newSyncPeer(t, canonical)
	n := newSyncNode(t, newSyncLedger(canonical[:1]))
	n.connectTo(t, idA, peer)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if n.Sync(ctx) || len(peer.requests()) != 0 || !hasPeer(n, idA) {
		t.Fatalf("requests = %v", peer.requests())
	}
}

func TestSyncAttemptTimeout(t *testing.T) {
	canonical := grow(nil, "A", ones(5)...)
	peer := newSyncPeer(t, canonical)
	peer.hook = func(_ http.ResponseWriter, r *http.Request, _ string, _, _ int, _ []*domain.Block) ([]*domain.Block, bool) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}

		return nil, true
	}
	n := newSyncNode(t, newSyncLedger(canonical[:1]))
	n.syncTimeout = 50 * time.Millisecond
	n.connectTo(t, idA, peer)

	start := time.Now()
	if n.Sync(context.Background()) || time.Since(start) > 3*time.Second {
		t.Fatal("the attempt was not bounded by its timeout")
	}

	if !hasPeer(n, idA) || n.book.Banned(idA, peer.srv.URL) {
		t.Fatal("a timed out attempt costs the peer nothing")
	}
}

func TestSyncCandidates(t *testing.T) {
	canonical := grow(nil, "A", ones(10)...)
	local := newSyncLedger(canonical[:4])  // height 3, on the canonical chain
	short := newSyncPeer(t, canonical[:3]) // height 2 < 3: never a candidate
	same := newSyncPeer(t, grow(canonical[:3], "Z", 1))
	high1 := newSyncPeer(t, canonical)
	high2 := newSyncPeer(t, canonical)
	low := newSyncPeer(t, canonical[:8])
	known := newSyncPeer(t, canonical[:4]) // its tip is in the ledger

	n := newSyncNode(t, local)
	n.connectTo(t, syncID(7), short)
	n.connectTo(t, syncID(6), low)
	n.connectTo(t, syncID(5), high2)
	n.connectTo(t, syncID(4), high1)
	n.connectTo(t, syncID(3), known)
	n.connectTo(t, syncID(2), same)

	// a peer without an address, whatever its height
	err := n.book.Connect(Hello{NodeID: syncID(1), Height: 50, TipHash: strings.Repeat("9", 64)}, true)
	if err != nil {
		t.Fatal(err)
	}

	tip, _ := local.Tip()

	var got []string

	for _, p := range n.syncCandidates(tip) {
		got = append(got, p.NodeID)
	}

	want := []string{syncID(4), syncID(5), syncID(6), syncID(2)}
	if !slices.Equal(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}

	if !n.Sync(context.Background()) {
		t.Fatal("Sync = false")
	}

	if len(high2.requests()) != 0 || len(low.requests()) != 0 || len(short.requests()) != 0 || len(known.requests()) != 0 || len(same.requests()) != 0 {
		t.Fatal("after the first attempt changed the tip no other peer is asked")
	}

	if local.height() != 10 {
		t.Fatalf("height %d", local.height())
	}
}

func TestSyncTriesTheNextCandidateAfterAQuietStop(t *testing.T) {
	canonical := grow(nil, "A", ones(5)...)
	l := newSyncLedger(canonical[:1])
	pruned := newSyncPeer(t, canonical)
	pruned.hook = func(w http.ResponseWriter, _ *http.Request, _ string, _, _ int, _ []*domain.Block) ([]*domain.Block, bool) {
		http.Error(w, "gone", http.StatusGone)
		return nil, true
	}
	good := newSyncPeer(t, canonical)
	n := newSyncNode(t, l)
	n.connectTo(t, syncID(1), pruned)
	n.connectTo(t, syncID(2), good)

	if !n.Sync(context.Background()) || len(pruned.requests()) == 0 || l.height() != 5 {
		t.Fatalf("height %d", l.height())
	}
}

func TestSyncNeverAsksABannedAddressAgain(t *testing.T) {
	canonical := grow(nil, "A", ones(5)...)
	l := newSyncLedger(canonical[:1])
	peer := newSyncPeer(t, canonical)
	// Two identities behind one address: the first lies, so the second is skipped.
	peer.hook = func(_ http.ResponseWriter, _ *http.Request, route string, _, _ int, page []*domain.Block) ([]*domain.Block, bool) {
		if route == "headers" {
			return []*domain.Block{nil}, false
		}

		return page, false
	}
	n := newSyncNode(t, l)
	n.connectTo(t, syncID(1), peer)
	n.connectTo(t, syncID(2), peer)

	if n.Sync(context.Background()) {
		t.Fatal("Sync = true")
	}

	if got := peer.requests(); len(got) != 1 {
		t.Fatalf("requests = %v, want a single one before the ban", got)
	}

	if !n.book.Banned(syncID(2), peer.srv.URL) {
		t.Fatal("the second identity shares the banned address")
	}

	// The second identity is still connected, but banned through the address: not a candidate.
	tip, _ := l.Tip()
	if !hasPeer(n, syncID(2)) || len(n.syncCandidates(tip)) != 0 {
		t.Fatalf("candidates = %v", n.syncCandidates(tip))
	}
}

func TestSyncBodyResponseBound(t *testing.T) {
	t.Setenv(domain.MaxBlockBytesEnvVar, "1000")

	canonical := grow(nil, "A", 1)

	for name, tc := range map[string]struct {
		pad  int
		want int
	}{"within twice the limit": {1500, 1}, "beyond twice the limit": {2500, 0}} {
		t.Run(name, func(t *testing.T) {
			peer := newSyncPeer(t, canonical)
			peer.hook = func(_ http.ResponseWriter, _ *http.Request, route string, _, _ int, page []*domain.Block) ([]*domain.Block, bool) {
				if route == "blocks" {
					page[0].Signature = strings.Repeat("s", tc.pad)
				}

				return page, false
			}
			l := newSyncLedger(canonical[:1])
			n := newSyncNode(t, l)
			n.connectTo(t, idA, peer)

			n.Sync(context.Background())

			if l.height() != tc.want {
				t.Fatalf("height %d, want %d", l.height(), tc.want)
			}

			if tc.want == 0 && (hasPeer(n, idA) || n.book.Banned(idA, peer.srv.URL)) {
				t.Fatal("an oversized response disconnects without a ban")
			}
		})
	}
}

func TestRoundSyncs(t *testing.T) {
	canonical := grow(nil, "A", ones(20)...)
	peer := newSyncPeer(t, canonical)
	l := newSyncLedger(canonical[:1])
	n := newSyncNode(t, l)
	n.book.AddAddrs(peer.srv.URL)

	n.Round(context.Background())

	if l.height() != 20 {
		t.Fatalf("height %d after a round", l.height())
	}
}

// forkScenario is a reorg of two blocks followed by an extension of 17, so a sync does every kind of request.
func forkScenario() (local, peer []*domain.Block) {
	base := grow(nil, "A", ones(3)...)
	return grow(base, "X", 1, 1), grow(base, "Y", ones(20)...)
}

func TestSyncLocalFailuresCostThePeerNothing(t *testing.T) {
	x, y := forkScenario()

	for _, empty := range []bool{false, true} {
		for call := 1; call <= 12; call++ {
			for _, onTip := range []bool{true, false} {
				l := newSyncLedger(x)
				if onTip {
					l.failTip = call
				} else {
					l.failRange, l.emptyRange = call, empty
				}

				peer := newSyncPeer(t, y)
				n := newSyncNode(t, l)
				n.connectTo(t, idA, peer)

				n.Sync(context.Background())

				if n.book.Banned(idA, peer.srv.URL) || !hasPeer(n, idA) {
					t.Fatalf("call %d (tip %v, empty %v): a local failure must not cost the peer", call, onTip, empty)
				}
			}
		}
	}
}

func TestSyncRequestFailuresDisconnectWithoutBan(t *testing.T) {
	x, y := forkScenario()

	for failAt := 1; failAt <= 12; failAt++ {
		l := newSyncLedger(x)
		peer := newSyncPeer(t, y)
		calls := 0
		peer.hook = func(w http.ResponseWriter, _ *http.Request, _ string, _, _ int, page []*domain.Block) ([]*domain.Block, bool) {
			calls++
			if calls == failAt {
				http.Error(w, "boom", http.StatusInternalServerError)
				return nil, true
			}

			return page, false
		}
		n := newSyncNode(t, l)
		n.connectTo(t, idA, peer)

		n.Sync(context.Background())

		if n.book.Banned(idA, peer.srv.URL) {
			t.Fatalf("request %d: a failed request must not ban", failAt)
		}

		if len(peer.requests()) >= failAt && hasPeer(n, idA) {
			t.Fatalf("request %d failed but the peer is still connected", failAt)
		}
	}
}

func TestSyncAcceptsAnUnsealedGenesisAndTheHighestDifficulty(t *testing.T) {
	genesis := &domain.Block{Timestamp: syncT0, MerkleRoot: domain.MerkleRoot(nil)} // never sealed: Hash is empty
	canonical := grow([]*domain.Block{genesis}, "A", 1, maxDifficulty, 1)
	l := newSyncLedger(canonical[:1])
	peer := newSyncPeer(t, canonical)
	n := newSyncNode(t, l)
	n.connectTo(t, idA, peer)

	if !n.Sync(context.Background()) || l.height() != 3 || n.book.Banned(idA, peer.srv.URL) {
		t.Fatalf("height %d", l.height())
	}
}
