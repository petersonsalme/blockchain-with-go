package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
)

const metricName = `[a-zA-Z_:][a-zA-Z0-9_:]*`

var (
	helpLine   = regexp.MustCompile(`^# HELP (` + metricName + `) .+$`)
	typeLine   = regexp.MustCompile(`^# TYPE (` + metricName + `) (counter|gauge|summary)$`)
	sampleLine = regexp.MustCompile(`^(` + metricName + `)(\{([a-zA-Z_][a-zA-Z0-9_]*="([^"\\\n]|\\\\|\\"|\\n)*"(,[a-zA-Z_][a-zA-Z0-9_]*="([^"\\\n]|\\\\|\\"|\\n)*")*)?\})? (-?[0-9]+(\.[0-9]+)?([eE][+-]?[0-9]+)?|NaN|[+-]Inf)$`)
)

func newMetricsServer(t *testing.T, c Chain, cfg Config) *Server {
	t.Helper()
	srv, err := NewServer(c, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func realChain(t *testing.T) (*chain.Service, *bank) {
	t.Helper()
	gomega.RegisterTestingT(t) // the bank helpers assert with gomega
	funds := newBank()
	svc, err := funds.service(memory.New(), pow.NewProofOfWork(1))
	if err != nil {
		t.Fatal(err)
	}
	return svc, funds
}

func do(h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = "192.0.2.1:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// scrape returns the sample lines of GET /metrics keyed by "name{labels}".
func scrape(t *testing.T, h http.Handler) map[string]float64 {
	t.Helper()
	rec := do(h, http.MethodGet, "/metrics", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics = %d, want 200", rec.Code)
	}
	out := map[string]float64{}
	for _, line := range strings.Split(strings.TrimSuffix(rec.Body.String(), "\n"), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		m := sampleLine.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("bad sample line %q", line)
		}
		v, err := strconv.ParseFloat(line[strings.LastIndexByte(line, ' ')+1:], 64)
		if err != nil {
			t.Fatal(err)
		}
		out[line[:strings.LastIndexByte(line, ' ')]] = v
	}
	return out
}

func TestMetricsFormat(t *testing.T) {
	svc, _ := realChain(t)
	h := newMetricsServer(t, svc, Config{}).Handler()
	do(h, http.MethodGet, "/health", "")

	rec := do(h, http.MethodGet, "/metrics", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got, want := rec.Header().Get("Content-Type"), "text/plain; version=0.0.4; charset=utf-8"; got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
	body := rec.Body.String()
	if !strings.HasSuffix(body, "\n") {
		t.Fatal("body must end with a newline")
	}

	helped, typed, sampled := map[string]bool{}, map[string]string{}, map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		switch {
		case helpLine.MatchString(line):
			helped[helpLine.FindStringSubmatch(line)[1]] = true
		case typeLine.MatchString(line):
			m := typeLine.FindStringSubmatch(line)
			typed[m[1]] = m[2]
		case sampleLine.MatchString(line):
			name := sampleLine.FindStringSubmatch(line)[1]
			name = strings.TrimSuffix(strings.TrimSuffix(name, "_sum"), "_count")
			sampled[name] = true
		default:
			t.Fatalf("line %q does not match the text format grammar", line)
		}
	}
	want := map[string]string{
		"http_requests_total":                "counter",
		"blockchain_blocks":                  "gauge",
		"blockchain_mining_duration_seconds": "summary",
	}
	for name, typ := range want {
		if !helped[name] || typed[name] != typ || !sampled[name] {
			t.Errorf("family %s: help=%v type=%q sampled=%v, want HELP, TYPE %s and a sample", name, helped[name], typed[name], sampled[name], typ)
		}
	}
	if len(typed) != len(want) || len(helped) != len(want) {
		t.Errorf("families = %v / %v, want exactly %d", helped, typed, len(want))
	}
}

func TestMetricsMethods(t *testing.T) {
	svc, _ := realChain(t)
	h := newMetricsServer(t, svc, Config{}).Handler()
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := do(h, m, "/metrics", "")
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("%s /metrics = %d Allow %q, want 405 \"GET, HEAD\"", m, rec.Code, rec.Header().Get("Allow"))
		}
	}
	if rec := do(h, http.MethodHead, "/metrics", ""); rec.Code != http.StatusOK {
		t.Errorf("HEAD /metrics = %d, want 200", rec.Code)
	}
}

func TestMetricsRequestsTotal(t *testing.T) {
	svc, _ := realChain(t)
	h := newMetricsServer(t, svc, Config{}).Handler()

	for range 3 {
		do(h, http.MethodGet, "/block/999", "")
	}
	do(h, http.MethodGet, "/block/0", "")
	do(h, http.MethodPut, "/blocks", "")

	got := scrape(t, h)
	for series, want := range map[string]float64{
		`http_requests_total{method="GET",route="GET /block/{ref}",code="404"}`: 3,
		`http_requests_total{method="GET",route="GET /block/{ref}",code="200"}`: 1,
		`http_requests_total{method="PUT",route="/blocks",code="405"}`:          1,
	} {
		if got[series] != want {
			t.Errorf("%s = %v, want %v", series, got[series], want)
		}
	}

	// The scrape counts itself only after it has been answered.
	if v := scrape(t, h)[`http_requests_total{method="GET",route="GET /metrics",code="200"}`]; v != 1 {
		t.Errorf("GET /metrics series = %v after one earlier scrape, want 1", v)
	}
}

func TestMetricsBoundedCardinality(t *testing.T) {
	svc, _ := realChain(t)
	h := newMetricsServer(t, svc, Config{}).Handler()

	for i := range 50 {
		do(h, http.MethodGet, fmt.Sprintf("/nope/%d/%x", i, i*7919), "")
	}
	do(h, "BREW", "/coffee", "")
	do(h, "PURGE", "/coffee", "")

	var unmatched []string
	for series := range scrape(t, h) {
		if strings.Contains(series, `route="unmatched"`) {
			unmatched = append(unmatched, series)
		}
	}
	if len(unmatched) != 2 {
		t.Fatalf("unmatched series = %v, want one for GET/404 and one for OTHER/404", unmatched)
	}
	if v := scrape(t, h)[`http_requests_total{method="GET",route="unmatched",code="404"}`]; v != 50 {
		t.Errorf("unmatched GET 404 = %v, want 50", v)
	}
	if v := scrape(t, h)[`http_requests_total{method="OTHER",route="unmatched",code="404"}`]; v != 2 {
		t.Errorf("OTHER method 404 = %v, want 2", v)
	}
}

func TestMetricsRateLimitedAreCounted(t *testing.T) {
	svc, _ := realChain(t)
	now := time.Unix(fixedNow, 0)
	h := newMetricsServer(t, svc, Config{RateLimit: 1, Now: func() time.Time { return now }}).Handler()
	do(h, http.MethodGet, "/health", "")
	do(h, http.MethodGet, "/health", "")

	rec := do(h, http.MethodGet, "/metrics", "")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("scrape = %d, want 429: /metrics is not exempt from the limiter", rec.Code)
	}
}

func TestMetricsSumMatchesLog(t *testing.T) {
	svc, funds := realChain(t)
	var buf bytes.Buffer
	h := newMetricsServer(t, svc, Config{Logger: slog.New(slog.NewJSONHandler(&buf, nil))}).Handler()

	do(h, http.MethodGet, "/", "")
	do(h, http.MethodPost, "/", funds.payload())
	do(h, http.MethodGet, "/nope", "")
	do(h, http.MethodDelete, "/blocks", "")
	do(h, http.MethodGet, "/metrics", "")

	var sum float64
	for series, v := range scrape(t, h) {
		if strings.HasPrefix(series, "http_requests_total{") {
			sum += v
		}
	}
	// The final scrape is logged after it is answered, so the body holds one record fewer.
	if logged := len(decodeLog(t, &buf)); float64(logged-1) != sum {
		t.Errorf("sum of series = %v, log records before the last scrape = %d", sum, logged-1)
	}
}

func TestMetricsMonotonic(t *testing.T) {
	svc, _ := realChain(t)
	h := newMetricsServer(t, svc, Config{}).Handler()
	prev := scrape(t, h)
	for i := range 10 {
		do(h, http.MethodGet, []string{"/", "/blocks", "/nope", "/health"}[i%4], "")
		cur := scrape(t, h)
		for series, v := range prev {
			if strings.HasPrefix(series, "http_requests_total") && cur[series] < v {
				t.Fatalf("%s went from %v to %v", series, v, cur[series])
			}
		}
		prev = cur
	}
}

func TestMetricsBlocksGauge(t *testing.T) {
	svc, funds := realChain(t)
	h := newMetricsServer(t, svc, Config{}).Handler()

	tip, err := svc.Tip()
	if err != nil {
		t.Fatal(err)
	}
	before := scrape(t, h)["blockchain_blocks"]
	if want := float64(tip.Index + 1); before != want {
		t.Fatalf("blockchain_blocks = %v, want %v", before, want)
	}
	if rec := do(h, http.MethodPost, "/", funds.payload()); rec.Code != http.StatusCreated {
		t.Fatalf("POST / = %d: %s", rec.Code, rec.Body)
	}
	if after := scrape(t, h)["blockchain_blocks"]; after != before+1 {
		t.Errorf("blockchain_blocks = %v after a mined block, want %v", after, before+1)
	}
}

func TestMetricsTipFailureOmitsGauge(t *testing.T) {
	h := newMetricsServer(t, brokenChain{}, Config{}).Handler()
	rec := do(h, http.MethodGet, "/metrics", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "blockchain_blocks") {
		t.Errorf("blockchain_blocks must be omitted when Tip fails:\n%s", body)
	}
	if !strings.Contains(body, "# TYPE blockchain_mining_duration_seconds summary") {
		t.Errorf("the other families must still be served:\n%s", body)
	}
}

// miningChain answers Mine with a fixed outcome.
type miningChain struct {
	brokenChain
	err error
}

func (c miningChain) Mine(context.Context, []domain.Transaction) (*domain.Block, error) {
	if c.err != nil {
		return nil, c.err
	}
	return &domain.Block{}, nil
}

func TestMetricsMiningDuration(t *testing.T) {
	const step = 250 * time.Millisecond
	var ticks time.Duration
	clock := func() time.Time {
		ticks += step // each reading advances the clock; Mine is measured by two readings
		return time.Unix(fixedNow, 0).Add(ticks)
	}
	gomega.RegisterTestingT(t)
	payload := newBank().payload()

	ok := newMetricsServer(t, miningChain{}, Config{Now: clock}).Handler()
	if got := scrape(t, ok); got["blockchain_mining_duration_seconds_count"] != 0 || got["blockchain_mining_duration_seconds_sum"] != 0 {
		t.Fatalf("fresh server: %v, want zero count and sum", got)
	}
	for i := 1; i <= 2; i++ {
		if rec := do(ok, http.MethodPost, "/", payload); rec.Code != http.StatusCreated {
			t.Fatalf("POST / = %d: %s", rec.Code, rec.Body)
		}
		got := scrape(t, ok)
		if c := got["blockchain_mining_duration_seconds_count"]; c != float64(i) {
			t.Errorf("count = %v, want %d", c, i)
		}
		if s, want := got["blockchain_mining_duration_seconds_sum"], float64(i)*step.Seconds(); s != want {
			t.Errorf("sum = %v, want %v", s, want)
		}
	}

	failing := newMetricsServer(t, miningChain{err: errors.New("boom")}, Config{Now: clock}).Handler()
	if rec := do(failing, http.MethodPost, "/", payload); rec.Code != http.StatusInternalServerError {
		t.Fatalf("failing POST / = %d, want 500", rec.Code)
	}
	if got := scrape(t, failing); got["blockchain_mining_duration_seconds_count"] != 0 || got["blockchain_mining_duration_seconds_sum"] != 0 {
		t.Errorf("failed Mine was recorded: %v", got)
	}
}

func TestMetricsServersDoNotShareState(t *testing.T) {
	svc, _ := realChain(t)
	a := newMetricsServer(t, svc, Config{}).Handler()
	b := newMetricsServer(t, svc, Config{}).Handler()
	do(a, http.MethodGet, "/nope", "")
	if v := scrape(t, b)[`http_requests_total{method="GET",route="unmatched",code="404"}`]; v != 0 {
		t.Errorf("second server saw %v requests of the first", v)
	}
}

func TestMetricsConcurrentScrape(t *testing.T) {
	svc, _ := realChain(t)
	h := newMetricsServer(t, svc, Config{}).Handler()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				do(h, http.MethodGet, []string{"/", "/nope", "/metrics", "/health"}[i%4], "")
			}
		}()
	}
	wg.Wait()
	var total float64
	for series, v := range scrape(t, h) {
		if strings.HasPrefix(series, "http_requests_total{") {
			total += v
		}
	}
	if total != 160 {
		t.Errorf("total = %v, want 160", total)
	}
}

func TestEscapeLabelValue(t *testing.T) {
	if got, want := escapeLabelValue("a\\b\"c\nd"), `a\\b\"c\nd`; got != want {
		t.Errorf("escapeLabelValue = %q, want %q", got, want)
	}
	m := newMetrics()
	m.observeRequest("GET", "we\"ird\\\nroute", 200)
	var buf bytes.Buffer
	m.write(&buf, 1, true)
	line := `http_requests_total{method="GET",route="we\"ird\\\nroute",code="200"} 1`
	if !strings.Contains(buf.String(), line+"\n") {
		t.Errorf("missing %q in:\n%s", line, buf.String())
	}
	if !sampleLine.MatchString(line) {
		t.Errorf("escaped line does not match the grammar")
	}
}

func TestMetricsOrderIsDeterministic(t *testing.T) {
	m := newMetrics()
	m.observeRequest("POST", "b", 200)
	m.observeRequest("GET", "b", 404)
	m.observeRequest("GET", "a", 404)
	m.observeRequest("GET", "a", 200)
	var first, second bytes.Buffer
	m.write(&first, 1, true)
	m.write(&second, 1, true)
	if first.String() != second.String() {
		t.Error("two renderings differ")
	}
	if i, j := strings.Index(first.String(), `route="a",code="200"`), strings.Index(first.String(), `route="a",code="404"`); i < 0 || i >= j {
		t.Errorf("series of one route and method not sorted by code:\n%s", first.String())
	}
	a := strings.Index(first.String(), `route="a"`)
	b := strings.Index(first.String(), `method="GET",route="b"`)
	c := strings.Index(first.String(), `method="POST"`)
	if a >= b || b >= c {
		t.Errorf("series not sorted by route, method, code:\n%s", first.String())
	}
}
