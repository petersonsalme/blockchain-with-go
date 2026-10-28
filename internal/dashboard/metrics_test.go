package dashboard_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/dashboard"
)

func TestParseMetricsReadsARealNode(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("testdata/node_metrics.txt")
	if err != nil {
		t.Fatal(err)
	}

	m, err := dashboard.ParseMetrics(body)
	if err != nil {
		t.Fatalf("ParseMetrics: %v", err)
	}

	want := dashboard.NodeMetrics{
		Blocks: 2, HasBlocks: true, MiningSum: 0.000241988, MiningCount: 1,
		Req2xx: 4, Req4xx: 2,
	}
	if m != want {
		t.Errorf("metrics = %+v, want %+v", m, want)
	}
}

func TestParseMetricsIgnoresOtherFamiliesAndComments(t *testing.T) {
	t.Parallel()

	text := "# HELP go_goroutines Number of goroutines.\n" +
		"# TYPE go_goroutines gauge\n" +
		"go_goroutines 12\n" +
		"\n" +
		"process_cpu_seconds_total 1.5 1700000000000\n" +
		"blockchain_blocks 7\n" +
		"  # an indented comment\n" +
		"blockchain_blocks_other 99\n" +
		"http_requests_total_other{code=\"200\"} 5\n" +
		"http_requests_total{code=\"200\",route=\"GET /a b\"} 3 1700000000000\r\n"

	m, err := dashboard.ParseMetrics([]byte(text))
	if err != nil {
		t.Fatalf("ParseMetrics: %v", err)
	}

	if want := (dashboard.NodeMetrics{Blocks: 7, HasBlocks: true, Req2xx: 3}); m != want {
		t.Errorf("metrics = %+v, want %+v", m, want)
	}
}

func TestParseMetricsGroupsStatusClasses(t *testing.T) {
	t.Parallel()

	text := `http_requests_total{code="100"} 1
http_requests_total{code="200"} 2
http_requests_total{code="201"} 3
http_requests_total{code="302"} 4
http_requests_total{code="400"} 5
http_requests_total{code="404"} 6
http_requests_total{code="429"} 7
http_requests_total{code="500"} 8
http_requests_total{code="503"} 9
http_requests_total{route="x\"}y",code="500"} 10
http_requests_total{route="no code"} 11
`

	m, err := dashboard.ParseMetrics([]byte(text))
	if err != nil {
		t.Fatalf("ParseMetrics: %v", err)
	}

	if m.Req2xx != 5 || m.Req4xx != 11 || m.Req5xx != 27 || m.Req429 != 7 {
		t.Errorf("classes = 2xx %v, 4xx %v, 5xx %v, 429 %v; want 5, 11, 27, 7", m.Req2xx, m.Req4xx, m.Req5xx, m.Req429)
	}
}

func TestParseMetricsRejectsMalformedSamples(t *testing.T) {
	t.Parallel()

	for _, line := range []string{
		"blockchain_blocks",
		"blockchain_blocks abc",
		"blockchain_blocks 1 notatimestamp",
		"blockchain_blocks 1 2 3",
		`http_requests_total{code="200" 1`,
		`http_requests_total{code=200} 1`,
		`http_requests_total{code="200"x} 1`,
		`http_requests_total{code="200} 1`,
		`http_requests_total{="200"} 1`,
		`go_goroutines{a="b"`,
		`{a="b"} 1`,
	} {
		_, err := dashboard.ParseMetrics([]byte("blockchain_blocks 1\n" + line + "\n"))
		if err == nil {
			t.Errorf("ParseMetrics accepted %q", line)
		}
	}
}

func TestMeanMining(t *testing.T) {
	t.Parallel()

	if got := (dashboard.NodeMetrics{MiningSum: 3, MiningCount: 0}).MeanMining(); got != "n/a" {
		t.Errorf("no blocks mined: %q, want n/a", got)
	}

	if got := (dashboard.NodeMetrics{MiningSum: 3, MiningCount: 4}).MeanMining(); got != "0.750000 s" {
		t.Errorf("mean = %q, want 0.750000 s", got)
	}
}

// metricsNode is a fake node with a settable /metrics answer, a settable height and a fixed list of
// block timestamps that /headers serves by from and limit.
type metricsNode struct {
	*countingNode

	height  atomic.Int64
	metrics atomic.Value // string
	stamps  []time.Time
}

func newMetricsNode(t *testing.T, metrics string, stamps ...time.Time) *metricsNode {
	t.Helper()

	m := &metricsNode{stamps: stamps}
	m.height.Store(int64(len(stamps) - 1))
	m.metrics.Store(metrics)
	m.countingNode = newCountingNode(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = fmt.Fprintf(w, `{"status":"ok","height":%d,"tip":"aa"}`, m.height.Load())
		case "/p2p/peers":
			_, _ = fmt.Fprint(w, `{"peers":[],"addrs":[]}`)
		case "/metrics":
			_, _ = fmt.Fprint(w, m.metrics.Load().(string))
		case "/headers":
			from, _ := strconv.Atoi(r.URL.Query().Get("from"))
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			upto := min(from+limit, len(m.stamps))

			var parts []string
			for i := from; i < upto; i++ {
				parts = append(parts, fmt.Sprintf(`{"Index":%d,"Timestamp":%q}`, i, m.stamps[i].Format(time.RFC3339)))
			}

			_, _ = fmt.Fprintf(w, `{"blocks":[%s],"from":%d,"limit":%d}`, strings.Join(parts, ","), from, limit)
		default:
			http.NotFound(w, r)
		}
	})

	return m
}

func (m *metricsNode) count(prefix string) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	n := 0

	for _, s := range m.seen {
		if strings.HasPrefix(s, prefix) {
			n++
		}
	}

	return n
}

func secondsAfter(secs ...int) []time.Time {
	out := make([]time.Time, len(secs))
	for i, s := range secs {
		out[i] = t0.Add(time.Duration(s) * time.Second)
	}

	return out
}

const nodeMetricsText = `# HELP blockchain_blocks Number of blocks.
blockchain_blocks 12
blockchain_mining_duration_seconds_sum 3
blockchain_mining_duration_seconds_count 4
http_requests_total{code="200",method="GET",route="GET /health"} 30
http_requests_total{code="201",method="POST",route="POST /mine"} 2
http_requests_total{code="400",method="POST",route="POST /tx"} 5
http_requests_total{code="429",method="GET",route="GET /blocks"} 6
http_requests_total{code="500",method="GET",route="GET /x"} 7
`

// metricsServer ticks a poller for the nodes (names node1, node2, ...) and serves them.
func metricsServer(t *testing.T, ticks int, nodes ...*metricsNode) (http.Handler, *dashboard.Poller) {
	t.Helper()

	var list []dashboard.Node

	for i, n := range nodes {
		node := n.node(t)
		node.Name = fmt.Sprintf("node%d", i+1)
		list = append(list, node)
	}

	fetch := dashboard.NewFetcher(time.Second, 1<<20)
	poll := dashboard.NewPoller(list, fetch, time.Hour, func() time.Time { return t0 })

	for range ticks {
		poll.Tick(context.Background())
	}

	h, err := dashboard.NewServer(list, fetch, poll)
	if err != nil {
		t.Fatal(err)
	}

	return h, poll
}

func TestMetricsScreenShowsTheNodeFigures(t *testing.T) {
	t.Parallel()

	n := newMetricsNode(t, nodeMetricsText, secondsAfter(0, 1)...)
	h, _ := metricsServer(t, 1, n)

	rec := do(h, http.MethodGet, "/metrics")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics = %d", rec.Code)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"<td>node1</td>", "<td>12</td>", "<td>0.750000 s</td>", "<td>32</td>", "<td>5</td>", "<td>7</td>", "<td>6</td>",
		`hx-get="/fragments/metrics"`, `hx-trigger="every 2s"`, `href="/metrics"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q:\n%s", want, body)
		}
	}
}

func TestMetricsScreenMeanIsNotAvailableWithoutMinedBlocks(t *testing.T) {
	t.Parallel()

	text := "blockchain_blocks 1\nblockchain_mining_duration_seconds_sum 0\nblockchain_mining_duration_seconds_count 0\n"
	h, _ := metricsServer(t, 1, newMetricsNode(t, text, secondsAfter(0)...))

	if body := do(h, http.MethodGet, "/fragments/metrics").Body.String(); !strings.Contains(body, "<td>n/a</td>") || !strings.Contains(body, "<td>1</td>") {
		t.Errorf("fragment:\n%s", body)
	}
}

func TestMetricsScreenParseErrorAffectsOnlyThatNode(t *testing.T) {
	t.Parallel()

	good := newMetricsNode(t, nodeMetricsText, secondsAfter(0, 1)...)
	bad := newMetricsNode(t, "blockchain_blocks 99\nthis is not a sample\n", secondsAfter(0, 1)...)
	down := newMetricsNode(t, "", secondsAfter(0, 1)...)
	h, _ := metricsServer(t, 1, good, bad, down)

	down.Close()

	for _, path := range []string{"/metrics", "/fragments/metrics"} {
		rec := do(h, http.MethodGet, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}

		body := rec.Body.String()
		if !strings.Contains(body, "<td>12</td>") {
			t.Errorf("%s lost the good node:\n%s", path, body)
		}

		if strings.Contains(body, "<td>99</td>") {
			t.Errorf("%s shows figures of a node whose /metrics did not parse:\n%s", path, body)
		}

		if got := strings.Count(body, "<td>n/a</td>"); got != 12 {
			t.Errorf("%s has %d n/a cells, want 12 (6 for each of 2 nodes)", path, got)
		}
	}
}

func TestMetricsScreenNeedsOneMetricsReadPerNodePerView(t *testing.T) {
	t.Parallel()

	n := newMetricsNode(t, nodeMetricsText, secondsAfter(0, 1)...)
	h, _ := metricsServer(t, 1, n)

	do(h, http.MethodGet, "/metrics")
	do(h, http.MethodGet, "/fragments/metrics")

	if got := n.count("GET /metrics"); got != 2 {
		t.Errorf("/metrics reads = %d, want 2", got)
	}
}

func TestIntervalChartScalesTheKnownSeries(t *testing.T) {
	t.Parallel()

	n := newMetricsNode(t, nodeMetricsText, secondsAfter(0, 1, 3, 7)...)
	h, _ := metricsServer(t, 1, n)

	body := do(h, http.MethodGet, "/fragments/charts?node=node1").Body.String()
	for _, want := range []string{
		`viewBox="0 0 400 120"`,
		`<rect class="bar" x="13.33" y="89.00" width="106.67" height="25.00"/>`,
		`<rect class="bar" x="146.67" y="64.00" width="106.67" height="50.00"/>`,
		`<rect class="bar" x="280.00" y="14.00" width="106.67" height="100.00"/>`,
		`hx-get="/fragments/charts?node=node1"`, `hx-trigger="every 10s"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("charts lack %q:\n%s", want, body)
		}
	}

	if got := strings.Count(body, `class="bar"`); got != 3 {
		t.Errorf("%d bars, want 3", got)
	}
}

func TestIntervalChartUsesTheLast50HeadersWithOneRead(t *testing.T) {
	t.Parallel()

	stamps := make([]time.Time, 80)
	for i := range stamps {
		stamps[i] = t0.Add(time.Duration(i*2) * time.Second)
	}

	n := newMetricsNode(t, nodeMetricsText, stamps...)
	h, _ := metricsServer(t, 1, n)

	before := n.count("GET /headers?from=30&limit=50")
	body := do(h, http.MethodGet, "/fragments/charts?node=node1").Body.String()

	if got := strings.Count(body, `class="bar"`); got != 49 {
		t.Errorf("%d bars, want 49 intervals between 50 headers", got)
	}

	if got := n.count("GET /headers?from=30&limit=50") - before; got != 1 {
		t.Errorf("header reads for the view = %d, want 1", got)
	}
}

func TestIntervalChartClampsNegativeIntervals(t *testing.T) {
	t.Parallel()

	n := newMetricsNode(t, nodeMetricsText, secondsAfter(5, 3, 7)...)
	h, _ := metricsServer(t, 1, n)

	body := do(h, http.MethodGet, "/fragments/charts?node=node1").Body.String()
	if !strings.Contains(body, `y="114.00" width="160.00" height="0.00"`) || !strings.Contains(body, `height="100.00"`) {
		t.Errorf("negative interval not drawn as zero:\n%s", body)
	}
}

func TestIntervalChartWithoutDataSaysSo(t *testing.T) {
	t.Parallel()

	one := newMetricsNode(t, nodeMetricsText, secondsAfter(0)...)
	h, _ := metricsServer(t, 1, one)

	body := do(h, http.MethodGet, "/fragments/charts?node=node1").Body.String()
	if got := strings.Count(body, `class="bar"`); got != 0 {
		t.Errorf("%d bars for a one-block chain", got)
	}

	if !strings.Contains(body, "Not enough blocks") {
		t.Errorf("no explanation:\n%s", body)
	}

	cold, _ := metricsServer(t, 0, one)
	if body := do(cold, http.MethodGet, "/fragments/charts?node=node1").Body.String(); !strings.Contains(body, "Not enough blocks") {
		t.Errorf("before the first poll:\n%s", body)
	}
}

func TestChartsHeaderFailureIsLocal(t *testing.T) {
	t.Parallel()

	n := newMetricsNode(t, nodeMetricsText, secondsAfter(0, 1, 2)...)
	h, _ := metricsServer(t, 2, n)

	n.Close()

	rec := do(h, http.MethodGet, "/fragments/charts?node=node1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "n/a") || !strings.Contains(body, `class="line series-0"`) {
		t.Errorf("height chart should survive a failed header read:\n%s", body)
	}
}

func TestHeightChartScalesAndCountsPoints(t *testing.T) {
	t.Parallel()

	n1 := newMetricsNode(t, nodeMetricsText, secondsAfter(0, 1, 2)...)
	n2 := newMetricsNode(t, nodeMetricsText, secondsAfter(0, 1)...)
	h, poll := metricsServer(t, 0, n1, n2)

	n1.height.Store(20)
	n2.height.Store(10)
	poll.Tick(context.Background())

	n1.height.Store(30)
	poll.Tick(context.Background())

	body := do(h, http.MethodGet, "/fragments/charts?node=node1").Body.String()
	// Heights: node1 20, 30; node2 10, 10. Range 10..30 over 100 px, 60 slots across 400.
	for _, want := range []string{
		`<polyline class="line series-0" points="0.00,64.00 6.78,14.00"/>`,
		`<polyline class="line series-1" points="0.00,114.00 6.78,114.00"/>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("charts lack %q:\n%s", want, body)
		}
	}

	if got := strings.Count(body, "<polyline"); got != 2 {
		t.Errorf("%d polylines, want one per node", got)
	}
}

func TestHeightChartKeepsTheLast60Ticks(t *testing.T) {
	t.Parallel()

	n := newMetricsNode(t, nodeMetricsText, secondsAfter(0, 1)...)
	h, poll := metricsServer(t, 0, n)

	for i := range 75 {
		n.height.Store(int64(i))
		poll.Tick(context.Background())
	}

	hist := poll.History()
	if len(hist) != 1 || hist[0].Name != "node1" || len(hist[0].Heights) != 60 || hist[0].Heights[0] != 15 || hist[0].Heights[59] != 74 {
		t.Fatalf("history = %+v", hist)
	}

	hist[0].Heights[0] = -1
	if poll.History()[0].Heights[0] != 15 {
		t.Error("History returns the poller's own slice")
	}

	body := do(h, http.MethodGet, "/fragments/charts?node=node1").Body.String()
	if pts := strings.Fields(strings.SplitN(strings.SplitN(body, `points="`, 2)[1], `"`, 2)[0]); len(pts) != 60 {
		t.Errorf("%d points, want 60", len(pts))
	}
}

func TestHeightChartFlatSeriesIsDrawnAtMidHeight(t *testing.T) {
	t.Parallel()

	n := newMetricsNode(t, nodeMetricsText, secondsAfter(0, 1)...)
	h, _ := metricsServer(t, 2, n)

	if body := do(h, http.MethodGet, "/fragments/charts?node=node1").Body.String(); !strings.Contains(body, `points="0.00,64.00 6.78,64.00"`) {
		t.Errorf("flat series:\n%s", body)
	}
}

func TestChartsPageViewsCauseNoHeightReads(t *testing.T) {
	t.Parallel()

	n := newMetricsNode(t, nodeMetricsText, secondsAfter(0, 1, 2)...)
	h, _ := metricsServer(t, 1, n)

	before := n.count("GET /health")

	var wg sync.WaitGroup

	for range 20 {
		wg.Go(func() {
			for _, p := range []string{"/metrics", "/fragments/charts?node=node1", "/fragments/metrics"} {
				do(h, http.MethodGet, p)
			}
		})
	}

	wg.Wait()

	if got := n.count("GET /health") - before; got != 0 {
		t.Errorf("%d /health reads caused by page views", got)
	}
}

func TestChartsChooseTheNode(t *testing.T) {
	t.Parallel()

	a := newMetricsNode(t, nodeMetricsText, secondsAfter(0, 1, 2)...)
	b := newMetricsNode(t, nodeMetricsText, secondsAfter(0, 10, 30)...)
	h, _ := metricsServer(t, 1, a, b)

	if body := do(h, http.MethodGet, "/fragments/charts?node=node2").Body.String(); !strings.Contains(body, `height="50.00"`) || !strings.Contains(body, "Block interval of node2") {
		t.Errorf("node2 chart:\n%s", body)
	}

	if body := do(h, http.MethodGet, "/metrics").Body.String(); !strings.Contains(body, "Block interval of node1") || !strings.Contains(body, `<option value="node2">`) {
		t.Errorf("default node or selector missing:\n%s", body)
	}

	if rec := do(h, http.MethodGet, "/fragments/charts?node=nope"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown node: %d, want 404", rec.Code)
	}

	if rec := do(h, http.MethodGet, "/metrics?node=nope"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown node on the page: %d, want 404", rec.Code)
	}

	if got := b.count("GET /headers?from=0&limit=50"); got != 1 {
		t.Errorf("node2 header reads = %d, want 1", got)
	}
}

func TestChartsHaveNoInlineStylesAndEscapeNodeNames(t *testing.T) {
	t.Parallel()

	n := newMetricsNode(t, nodeMetricsText, secondsAfter(0, 1, 2)...)

	node := n.node(t)
	node.Name = `<b>"x"</b>&`

	fetch := dashboard.NewFetcher(time.Second, 1<<20)
	poll := dashboard.NewPoller([]dashboard.Node{node}, fetch, time.Hour, func() time.Time { return t0 })
	poll.Tick(context.Background())

	h, err := dashboard.NewServer([]dashboard.Node{node}, fetch, poll)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/metrics", "/fragments/charts?node=" + "%3Cb%3E%22x%22%3C%2Fb%3E%26"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))

		body := rec.Body.String()
		if rec.Code != http.StatusOK || strings.Contains(body, "<b>") || strings.Contains(body, `"x"</b>`) {
			t.Errorf("%s: %d, unescaped name:\n%s", path, rec.Code, body)
		}

		if strings.Contains(body, "style=") || strings.Contains(body, "<script>") {
			t.Errorf("%s has an inline style or script", path)
		}
	}
}

func TestParseMetricsLabelEscapes(t *testing.T) {
	t.Parallel()

	m, err := dashboard.ParseMetrics([]byte("http_requests_total{route=\"a\\\\b\\nc\\\"d\",code=\"200\"} 2\n"))
	if err != nil || m.Req2xx != 2 {
		t.Fatalf("escaped label: %+v, %v", m, err)
	}

	_, err = dashboard.ParseMetrics([]byte("http_requests_total{route=\"a\\"))
	if err == nil {
		t.Error("label value ending in a backslash accepted")
	}
}
