package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// DefaultInterval is the poll interval: node load depends on it and the node count only.
const DefaultInterval = 2 * time.Second

// HistoryTicks is how many ticks of heights the poller keeps per node.
const HistoryTicks = 60

// Series is the heights one node had at the poller's last ticks, oldest first.
type Series struct {
	Name    string
	Heights []int
}

// Poller reads every node once per tick and keeps the latest snapshots. Handlers read copies
// through View and never reach a node.
type Poller struct {
	nodes    []Node
	fetch    *Fetcher
	interval time.Duration
	now      func() time.Time

	mu        sync.Mutex
	snaps     []Snapshot
	forkTicks int
	heights   [][]int
}

// NewPoller returns a Poller for nodes. interval and now are injected for tests.
func NewPoller(nodes []Node, fetch *Fetcher, interval time.Duration, now func() time.Time) *Poller {
	snaps := make([]Snapshot, len(nodes))
	for i, n := range nodes {
		snaps[i] = Snapshot{Name: n.Name}
	}

	return &Poller{nodes: nodes, fetch: fetch, interval: interval, now: now, snaps: snaps, heights: make([][]int, len(nodes))}
}

// Run ticks at once and then every interval until ctx is done.
func (p *Poller) Run(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		p.Tick(ctx)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Tick polls all nodes concurrently, so a slow node does not delay the others, then replaces the snapshots.
func (p *Poller) Tick(ctx context.Context) {
	p.mu.Lock()
	prev := append([]Snapshot(nil), p.snaps...)
	p.mu.Unlock()

	next := make([]Snapshot, len(p.nodes))

	var wg sync.WaitGroup

	for i, node := range p.nodes {
		wg.Go(func() { next[i] = p.poll(ctx, node, prev[i]) })
	}

	wg.Wait()

	p.mu.Lock()
	defer p.mu.Unlock()

	p.snaps = next

	for i, s := range next {
		if s.Seen {
			p.heights[i] = append(p.heights[i], s.Height)
			p.heights[i] = p.heights[i][max(len(p.heights[i])-HistoryTicks, 0):]
		}
	}

	if forkCandidate(next) && sameHeight(next) {
		p.forkTicks++
	} else {
		p.forkTicks = 0
	}
}

// History returns a copy of the height history of every node that has been seen, in NODES order.
// It is what the height chart draws; reading it never reaches a node.
func (p *Poller) History() []Series {
	p.mu.Lock()
	defer p.mu.Unlock()

	var out []Series

	for i, n := range p.nodes {
		if len(p.heights[i]) > 0 {
			out = append(out, Series{Name: n.Name, Heights: append([]int(nil), p.heights[i]...)})
		}
	}

	return out
}

type healthBody struct {
	Status string `json:"status"`
	Height int    `json:"height"`
	Tip    string `json:"tip"`
}

type peersBody struct {
	Peers []struct {
		Addr string `json:"addr"`
	} `json:"peers"`
}

type mempoolBody struct {
	Transactions []json.RawMessage `json:"transactions"`
}

type headersBody struct {
	Blocks []struct {
		Timestamp  time.Time `json:"timestamp"`
		Difficulty uint64    `json:"difficulty"`
	} `json:"blocks"`
}

// Card is one node as the page shows it.
type Card struct {
	Snapshot

	Behind  int
	Since   string
	Differs bool   // fork: reachable, and its tip is not the majority tip
	Level   string // ok, warn or bad: the dot, badge and border of the node
	Status  string // what the badge says
}

// View is what the Network screen renders: the verdict and a card per node, in NODES order.
type View struct {
	Assessment Assessment
	Cards      []Card
	Agree      int // reachable nodes on the majority tip
	Reachable  int // reachable nodes
}

// Banner is the CSS class suffix and text of the verdict.
func (v View) Banner() string { return v.Assessment.Text() }

// View returns a copy of the latest state, with "time since last block" taken at the moment of the call.
func (p *Poller) View() View {
	p.mu.Lock()
	snaps := append([]Snapshot(nil), p.snaps...)
	forkTicks := p.forkTicks
	p.mu.Unlock()

	a := Assess(snaps, forkTicks)
	now := p.now()
	cards := make([]Card, len(snaps))

	for i, s := range snaps {
		c := Card{Snapshot: s, Behind: a.Behind[s.Name], Since: "unknown"}
		if !s.TipTime.IsZero() {
			c.Since = Since(now.Sub(s.TipTime))
		}

		cards[i] = c
	}

	majority, agree := majorityTip(snaps)

	for i := range cards {
		c := &cards[i]
		c.Differs = a.State == StateFork && c.Reachable && c.Tip != majority
		c.Level, c.Status = nodeStatus(*c)
	}

	return View{Assessment: a, Cards: cards, Agree: agree, Reachable: len(snaps) - a.Unreachable}
}

// majorityTip is the tip most reachable nodes at the highest height report (the first in NODES
// order on a tie) and how many report it.
func majorityTip(snaps []Snapshot) (string, int) {
	top := 0

	for _, s := range snaps {
		if s.Reachable {
			top = max(top, s.Height)
		}
	}

	counts := map[string]int{}
	best, bestN := "", 0

	for _, s := range snaps {
		if !s.Reachable || s.Height != top {
			continue
		}

		counts[s.Tip]++
		if counts[s.Tip] > bestN {
			best, bestN = s.Tip, counts[s.Tip]
		}
	}

	return best, bestN
}

// nodeStatus is the level and badge text of a node: unreachable and different tip are bad, behind is warn.
func nodeStatus(c Card) (level, status string) {
	switch {
	case !c.Reachable:
		return "bad", "Unreachable"
	case c.Differs:
		return "bad", "Different tip"
	case c.Behind > 0:
		return "warn", fmt.Sprintf("Behind by %d", c.Behind)
	}

	return "ok", "Up to date"
}

// poll reads one node: /health, /p2p/peers and /mempool at once, then the tip header. A node whose /health
// fails is unreachable and keeps its previous values; a failing peers, mempool or header read only blanks that field.
func (p *Poller) poll(ctx context.Context, node Node, prev Snapshot) Snapshot {
	var (
		wg                    sync.WaitGroup
		health                healthBody
		peers                 peersBody
		pool                  mempoolBody
		healthErr, pErr, mErr error
	)

	wg.Go(func() { healthErr = p.get(ctx, node, Health(), &health) })
	wg.Go(func() { pErr = p.get(ctx, node, Peers(), &peers) })
	wg.Go(func() { mErr = p.get(ctx, node, Mempool(), &pool) })
	wg.Wait()

	prev.Reachable = false

	if healthErr != nil || health.Status != "ok" {
		return prev
	}

	s := Snapshot{Name: node.Name, Reachable: true, Seen: true, Height: health.Height, Tip: health.Tip}

	if pErr == nil {
		s.Peers = len(peers.Peers)
		for _, peer := range peers.Peers {
			s.PeerAddrs = append(s.PeerAddrs, peer.Addr)
		}
	}

	if mErr == nil {
		s.Pool, s.PoolKnown = len(pool.Transactions), true
	}

	var headers headersBody

	err := p.get(ctx, node, Headers(health.Height, 1), &headers)
	if err == nil && len(headers.Blocks) > 0 {
		s.TipTime = headers.Blocks[0].Timestamp
	}

	return s
}

func (p *Poller) get(ctx context.Context, node Node, req Request, into any) error {
	body, err := p.fetch.Get(ctx, node, req)
	if err != nil {
		return err
	}

	err = json.Unmarshal(body, into)
	if err != nil {
		return fmt.Errorf("node %s: %w", node.Name, err)
	}

	return nil
}

// Since renders an age as "12 s ago", "3 min ago" or "2 h ago".
func Since(d time.Duration) string {
	if d < 0 {
		return "just now"
	}

	if d < time.Minute {
		return fmt.Sprintf("%d s ago", int(d/time.Second))
	}

	if d < time.Hour {
		return fmt.Sprintf("%d min ago", int(d/time.Minute))
	}

	return fmt.Sprintf("%d h ago", int(d/time.Hour))
}
