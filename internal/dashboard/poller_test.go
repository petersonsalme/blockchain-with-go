package dashboard_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/dashboard"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// chainNode is a fake node whose state the test changes between ticks.
type chainNode struct {
	*countingNode

	height atomic.Int64
	tip    atomic.Value // string
	peers  atomic.Value // string: JSON array of addresses
	up     atomic.Bool
	noHdrs atomic.Bool  // /headers answers an empty list
	last   atomic.Value // time.Time
}

func newChainNode(t *testing.T, height int, tip string) *chainNode {
	t.Helper()

	c := &chainNode{}
	c.height.Store(int64(height))
	c.tip.Store(tip)
	c.peers.Store(`[]`)
	c.up.Store(true)
	c.last.Store(t0.Add(-12 * time.Second))
	c.countingNode = newCountingNode(t, func(w http.ResponseWriter, r *http.Request) {
		if !c.up.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)

			return
		}

		switch r.URL.Path {
		case "/health":
			_, _ = fmt.Fprintf(w, `{"status":"ok","height":%d,"tip":%q}`, c.height.Load(), c.tip.Load())
		case "/p2p/peers":
			var addrs []string

			_ = json.Unmarshal([]byte(c.peers.Load().(string)), &addrs)

			peers := make([]map[string]string, len(addrs))
			for i, a := range addrs {
				peers[i] = map[string]string{"addr": a}
			}

			_ = json.NewEncoder(w).Encode(map[string]any{"peers": peers, "addrs": []string{"known:1"}})
		case "/headers":
			if c.noHdrs.Load() {
				_, _ = fmt.Fprint(w, `{"blocks":[],"from":0,"limit":1}`)

				return
			}

			if r.URL.RawQuery != fmt.Sprintf("from=%d&limit=1", c.height.Load()) {
				http.Error(w, "bad query", http.StatusBadRequest)

				return
			}

			_, _ = fmt.Fprintf(w, `{"blocks":[{"Timestamp":%q}],"from":0,"limit":1}`, c.last.Load().(time.Time).Format(time.RFC3339))
		default:
			http.NotFound(w, r)
		}
	})

	return c
}

func newPoller(t *testing.T, nodes ...*chainNode) *dashboard.Poller {
	t.Helper()

	var list []dashboard.Node

	for i, n := range nodes {
		node := n.node(t)
		node.Name = fmt.Sprintf("node%d", i+1)
		list = append(list, node)
	}

	return dashboard.NewPoller(list, dashboard.NewFetcher(time.Second, 1<<20), time.Hour, func() time.Time { return t0 })
}

func TestPollerSnapshot(t *testing.T) {
	t.Parallel()

	n := newChainNode(t, 4, hex64)
	n.peers.Store(`["a:1","b:2"]`)
	p := newPoller(t, n)
	p.Tick(context.Background())

	cards := p.View().Cards
	if len(cards) != 1 {
		t.Fatalf("cards = %d", len(cards))
	}

	c := cards[0]
	if !c.Reachable || c.Height != 4 || c.Tip != hex64 || c.Peers != 2 || c.Since != "12 s ago" {
		t.Errorf("card = %+v", c)
	}
}

func TestPollerForkNeedsThreeConsecutiveTicks(t *testing.T) {
	t.Parallel()

	a, b := newChainNode(t, 5, "aa"), newChainNode(t, 5, "bb")
	p := newPoller(t, a, b)
	ctx := context.Background()

	want := []dashboard.State{dashboard.StateSyncing, dashboard.StateSyncing, dashboard.StateFork}
	for i, w := range want {
		p.Tick(ctx)

		if got := p.View().Assessment.State; got != w {
			t.Fatalf("tick %d: state = %v, want %v", i+1, got, w)
		}
	}

	// The condition clears: the count restarts.
	b.tip.Store("aa")
	p.Tick(ctx)

	if got := p.View().Assessment.State; got != dashboard.StateAgreement {
		t.Fatalf("cleared: state = %v", got)
	}

	b.tip.Store("bb")
	p.Tick(ctx)
	p.Tick(ctx)

	if got := p.View().Assessment.State; got != dashboard.StateSyncing {
		t.Fatalf("after reset, 2 ticks: state = %v, want syncing", got)
	}

	// A different height breaks the run too.
	b.height.Store(6)
	p.Tick(ctx)
	b.height.Store(5)
	p.Tick(ctx)

	if got := p.View().Assessment.State; got != dashboard.StateSyncing {
		t.Fatalf("after height break: state = %v, want syncing", got)
	}
}

func TestPollerUnreachableKeepsLastHeight(t *testing.T) {
	t.Parallel()

	n1, n2, n3 := newChainNode(t, 5, "aa"), newChainNode(t, 5, "aa"), newChainNode(t, 5, "aa")
	p := newPoller(t, n1, n2, n3)
	ctx := context.Background()

	p.Tick(ctx)
	n3.up.Store(false)
	p.Tick(ctx)

	v := p.View()
	if c := v.Cards[2]; c.Reachable || c.Height != 5 || !c.Seen || c.Behind != 0 {
		t.Errorf("node3 card = %+v", c)
	}

	if got := v.Assessment.Text(); got != "Network in agreement · 1 of 3 unreachable" {
		t.Errorf("banner = %q", got)
	}

	for _, n := range []*chainNode{n1, n2} {
		n.height.Store(7)
		n.tip.Store("bb")
	}

	p.Tick(ctx)

	v = p.View()
	if c := v.Cards[2]; c.Reachable || c.Height != 5 || c.Behind != 2 {
		t.Errorf("node3 card = %+v, want unreachable at 5 behind by 2", c)
	}

	n3.up.Store(true)
	n3.height.Store(7)
	n3.tip.Store("bb")
	p.Tick(ctx)

	if got := p.View().Assessment.Text(); got != "Network in agreement" {
		t.Errorf("banner = %q", got)
	}
}

// ticks polls n times, enough for the fork verdict (three consecutive ticks).
func ticks(p *dashboard.Poller, n int) {
	for range n {
		p.Tick(context.Background())
	}
}

func TestViewMarksTheNodeOffTheMajorityTipInAFork(t *testing.T) {
	t.Parallel()

	a, b, c := newChainNode(t, 6, "aa"), newChainNode(t, 6, "aa"), newChainNode(t, 6, "bb")
	p := newPoller(t, a, b, c)

	ticks(p, 3)

	v := p.View()
	if v.Assessment.State != dashboard.StateFork {
		t.Fatalf("state = %v, want fork", v.Assessment.State)
	}

	if v.Agree != 2 || v.Reachable != 3 {
		t.Errorf("agree = %d reachable = %d, want 2 and 3", v.Agree, v.Reachable)
	}

	for i, want := range []struct {
		differs       bool
		level, status string
	}{{false, "ok", "Up to date"}, {false, "ok", "Up to date"}, {true, "bad", "Different tip"}} {
		if c := v.Cards[i]; c.Differs != want.differs || c.Level != want.level || c.Status != want.status {
			t.Errorf("card %d = differs %t, %q, %q; want %t, %q, %q", i, c.Differs, c.Level, c.Status, want.differs, want.level, want.status)
		}
	}
}

func TestViewDoesNotFlagAnyNodeBeforeTheForkVerdict(t *testing.T) {
	t.Parallel()

	a, b := newChainNode(t, 6, "aa"), newChainNode(t, 6, "bb")
	p := newPoller(t, a, b)

	ticks(p, 1) // syncing: two nodes may just have mined at once

	for i, c := range p.View().Cards {
		if c.Differs || c.Level != "ok" {
			t.Errorf("card %d = differs %t level %q, want an unflagged ok card while syncing", i, c.Differs, c.Level)
		}
	}
}

func TestViewTipTieFavoursTheFirstNode(t *testing.T) {
	t.Parallel()

	a, b := newChainNode(t, 6, "aa"), newChainNode(t, 6, "bb")
	p := newPoller(t, a, b)

	ticks(p, 3)

	v := p.View()
	if v.Agree != 1 || v.Cards[0].Differs || !v.Cards[1].Differs {
		t.Errorf("agree = %d, node1 differs = %t, node2 differs = %t; want 1, false, true", v.Agree, v.Cards[0].Differs, v.Cards[1].Differs)
	}
}

func TestViewStatusOfANodeBehindAndOfAnUnreachableNode(t *testing.T) {
	t.Parallel()

	a, b, c := newChainNode(t, 7, "bb"), newChainNode(t, 7, "bb"), newChainNode(t, 5, "aa")
	p := newPoller(t, a, b, c)

	ticks(p, 1)

	v := p.View()
	if got := v.Cards[2]; got.Level != "warn" || got.Status != "Behind by 2" || got.Differs {
		t.Errorf("trailing node = %q, %q, differs %t; want warn, Behind by 2, false", got.Level, got.Status, got.Differs)
	}

	if v.Agree != 2 {
		t.Errorf("agree = %d, want 2: the node that trails is not on the tip", v.Agree)
	}

	c.up.Store(false)
	ticks(p, 1)

	v = p.View()
	if got := v.Cards[2]; got.Level != "bad" || got.Status != "Unreachable" {
		t.Errorf("unreachable node = %q, %q; want bad, Unreachable", got.Level, got.Status)
	}

	if v.Reachable != 2 {
		t.Errorf("reachable = %d, want 2", v.Reachable)
	}
}

func TestViewAgreementCountsEveryReachableNode(t *testing.T) {
	t.Parallel()

	p := newPoller(t, newChainNode(t, 3, "aa"), newChainNode(t, 3, "aa"), newChainNode(t, 3, "aa"))

	ticks(p, 1)

	if v := p.View(); v.Agree != 3 || v.Reachable != 3 || v.Cards[0].Level != "ok" {
		t.Errorf("agree = %d reachable = %d level %q, want 3, 3, ok", v.Agree, v.Reachable, v.Cards[0].Level)
	}
}

func TestForkShowsInTheMatrixAndTheSidebar(t *testing.T) {
	t.Parallel()

	a, b, c := newChainNode(t, 6, "aa"), newChainNode(t, 6, "aa"), newChainNode(t, 6, "bb")
	p := newPoller(t, a, b, c)

	ticks(p, 3)

	h := newTestServerWithPoller(t, p, a, b, c)

	frag := do(h, http.MethodGet, "/fragments/network").Body.String()
	for _, want := range []string{
		`banner banner--bad banner-fork`, `<td class="cell-diff">`, `class="cell-mark"`, "different tip", `<span class="kpi-value">2/3</span>`,
	} {
		if !strings.Contains(frag, want) {
			t.Errorf("network fragment lacks %q:\n%s", want, frag)
		}
	}

	if got := strings.Count(frag, `class="cell-diff"`); got != 1 {
		t.Errorf("%d cells marked as different, want 1 (only node3's tip)", got)
	}

	side := do(h, http.MethodGet, "/fragments/sidebar").Body.String()
	for _, want := range []string{`id="sidebar-nodes"`, `hx-get="/fragments/sidebar"`, `nav-node--bad`, "Different tip", "Up to date", `<span class="nav-node-height">#6</span>`} {
		if !strings.Contains(side, want) {
			t.Errorf("sidebar fragment lacks %q:\n%s", want, side)
		}
	}

	if got := strings.Count(side, "nav-node--ok"); got != 2 {
		t.Errorf("%d nodes marked ok in the sidebar, want 2", got)
	}
}

func TestNetworkPageExplainsHowToReadItOutsideTheRefreshedFragment(t *testing.T) {
	t.Parallel()

	a := newChainNode(t, 2, "aa")
	p := newPoller(t, a)
	p.Tick(context.Background())

	h := newTestServerWithPoller(t, p, a)

	page := do(h, http.MethodGet, "/").Body.String()
	for _, want := range []string{
		`<button type="button" class="info-btn" aria-label="How to read this page" aria-describedby="network-help">`,
		`id="network-help" role="tooltip"`, "Behind by N", "Unreachable",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}

	// htmx replaces the fragment every 2 s: a tooltip inside it would close under the pointer.
	if frag := do(h, http.MethodGet, "/fragments/network").Body.String(); strings.Contains(frag, "network-help") {
		t.Error("the help tooltip is inside the refreshed fragment")
	}
}

func TestSidebarMarksAnUnreachableNodeAsDown(t *testing.T) {
	t.Parallel()

	a, b := newChainNode(t, 4, "aa"), newChainNode(t, 4, "aa")
	p := newPoller(t, a, b)

	ticks(p, 1)
	b.up.Store(false)
	ticks(p, 1)

	side := do(newTestServerWithPoller(t, p, a, b), http.MethodGet, "/fragments/sidebar").Body.String()
	if !strings.Contains(side, "nav-node--bad nav-node--down") || !strings.Contains(side, "Unreachable") {
		t.Errorf("sidebar does not mark node2 as down:\n%s", side)
	}
}

func TestPollerSlowNodeDoesNotDelayOthers(t *testing.T) {
	t.Parallel()

	slow := newCountingNode(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(400 * time.Millisecond):
		case <-r.Context().Done():
		}

		http.Error(w, "slow", http.StatusServiceUnavailable)
	})
	fast := newChainNode(t, 1, "aa")

	sn := slow.node(t)
	sn.Name = "slow"
	fn := fast.node(t)
	fn.Name = "fast"

	p := dashboard.NewPoller([]dashboard.Node{sn, fn}, dashboard.NewFetcher(time.Second, 1<<20), time.Hour, time.Now)

	done := make(chan struct{})

	go func() { p.Tick(context.Background()); close(done) }()

	// The tick as a whole takes the slow node's time, but both are fetched at once.
	start := time.Now()

	<-done

	if d := time.Since(start); d > 700*time.Millisecond {
		t.Errorf("tick took %v: nodes were fetched one after another", d)
	}

	if hits := fast.hits.Load(); hits == 0 {
		t.Error("fast node was not polled")
	}
}

func TestPollerEscapesNodeStrings(t *testing.T) {
	t.Parallel()

	const tip = `<script>alert(1)</script>`

	n := newChainNode(t, 1, tip)
	n.peers.Store(`["\"><img src=x onerror=alert(1)>"]`)
	p := newPoller(t, n)
	p.Tick(context.Background())

	body := do(newTestServerWithPoller(t, p, n), http.MethodGet, "/fragments/network").Body.String()

	for _, raw := range []string{"<script>alert(1)", `"><img src=x`, "<img"} {
		if strings.Contains(body, raw) {
			t.Errorf("fragment holds raw %q:\n%s", raw, body)
		}
	}

	for _, esc := range []string{"&lt;script&gt;alert(1)&lt;/script&gt;", "&#34;&gt;&lt;img src=x onerror=alert(1)&gt;"} {
		if !strings.Contains(body, esc) {
			t.Errorf("fragment lacks escaped %q:\n%s", esc, body)
		}
	}
}

func TestBrowserRequestsCauseNoNodeRequests(t *testing.T) {
	t.Parallel()

	n := newChainNode(t, 1, "aa")
	p := newPoller(t, n)
	p.Tick(context.Background())

	h := newTestServerWithPoller(t, p, n)
	before := n.hits.Load()

	for range 25 {
		for _, path := range []string{"/", "/fragments/network"} {
			if rec := do(h, http.MethodGet, path); rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d", path, rec.Code)
			}
		}
	}

	if got := n.hits.Load(); got != before {
		t.Errorf("node requests grew from %d to %d on browser requests", before, got)
	}
}

func TestNetworkFragment(t *testing.T) {
	t.Parallel()

	a, b := newChainNode(t, 7, "aa"), newChainNode(t, 5, "bb")
	p := newPoller(t, a, b)
	p.Tick(context.Background())

	h := newTestServerWithPoller(t, p, a, b)

	frag := do(h, http.MethodGet, "/fragments/network").Body.String()
	for _, want := range []string{
		`hx-get="/fragments/network"`, `hx-trigger="every 2s"`, `hx-swap="outerHTML"`,
		"Network syncing", "behind by 2", "12 s ago", `class="banner banner--warn banner-syncing"`,
	} {
		if !strings.Contains(frag, want) {
			t.Errorf("fragment lacks %q:\n%s", want, frag)
		}
	}

	if page := do(h, http.MethodGet, "/").Body.String(); !strings.Contains(page, `hx-get="/fragments/network"`) {
		t.Error("the page does not embed the network container")
	}

	if other := strings.ReplaceAll(do(h, http.MethodGet, "/n/node1/").Body.String(), sidebarPoll, ""); strings.Contains(other, `hx-trigger="every 2s"`) {
		t.Error("a node page polls the network fragment")
	}
}

// sidebarPoll is the node list in the sidebar, which every full page refreshes.
const sidebarPoll = `hx-get="/fragments/sidebar" hx-trigger="every 2s" hx-swap="outerHTML"`

func TestPollerRunTicksUntilCancelled(t *testing.T) {
	t.Parallel()

	n := newChainNode(t, 1, "aa")
	node := n.node(t)
	node.Name = "n"
	p := dashboard.NewPoller([]dashboard.Node{node}, dashboard.NewFetcher(time.Second, 1<<20), 10*time.Millisecond, time.Now)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() { p.Run(ctx); close(done) }()

	deadline := time.After(5 * time.Second)

	for n.hits.Load() < 6 { // at least two ticks of three requests
		select {
		case <-deadline:
			t.Fatal("poller did not tick")
		case <-time.After(5 * time.Millisecond):
		}
	}

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestSinceFormats(t *testing.T) {
	t.Parallel()

	tests := []struct {
		ago  time.Duration
		want string
	}{
		{-3 * time.Second, "just now"},
		{0, "0 s ago"},
		{59 * time.Second, "59 s ago"},
		{60 * time.Second, "1 min ago"},
		{119 * time.Second, "1 min ago"},
		{59 * time.Minute, "59 min ago"},
		{time.Hour, "1 h ago"},
		{49 * time.Hour, "49 h ago"},
	}

	for _, tc := range tests {
		if got := dashboard.Since(tc.ago); got != tc.want {
			t.Errorf("Since(%v) = %q, want %q", tc.ago, got, tc.want)
		}
	}
}

func TestPollerWithoutTipHeaderShowsUnknownAge(t *testing.T) {
	t.Parallel()

	n := newChainNode(t, 3, "aa")
	n.noHdrs.Store(true)
	p := newPoller(t, n)
	p.Tick(context.Background())

	if c := p.View().Cards[0]; !c.Reachable || c.Height != 3 || c.Since != "unknown" {
		t.Errorf("card = %+v, want reachable at 3 with unknown age", c)
	}
}

func TestDefaultIntervalIsTwoSeconds(t *testing.T) {
	t.Parallel()

	if dashboard.DefaultInterval != 2*time.Second {
		t.Errorf("DefaultInterval = %v, want 2s", dashboard.DefaultInterval)
	}
}
