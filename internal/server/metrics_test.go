package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onsi/gomega"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
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
	req := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	req.RemoteAddr = "192.0.2.1:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	return rec
}

// families parses the body of GET /metrics with the expfmt text parser.
func families(t *testing.T, h http.Handler) (*httptest.ResponseRecorder, map[string]*dto.MetricFamily) {
	t.Helper()

	rec := do(h, http.MethodGet, "/metrics", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics = %d, want 200", rec.Code)
	}

	parser := expfmt.NewTextParser(model.UTF8Validation)

	fams, err := parser.TextToMetricFamilies(rec.Body)
	if err != nil {
		t.Fatalf("body does not parse: %v", err)
	}

	return rec, fams
}

// seriesKey renders a sample as name{label="value",...} with the labels sorted by name.
func seriesKey(name string, labels []*dto.LabelPair) string {
	if len(labels) == 0 {
		return name
	}

	pairs := make([]string, 0, len(labels))
	for _, l := range labels {
		pairs = append(pairs, l.GetName()+`="`+l.GetValue()+`"`)
	}

	slices.Sort(pairs)

	return name + "{" + strings.Join(pairs, ",") + "}"
}

// scrape returns the samples of GET /metrics keyed by "name{labels}" (labels sorted); a summary yields _sum and _count.
func scrape(t *testing.T, h http.Handler) map[string]float64 {
	t.Helper()

	_, fams := families(t, h)
	out := map[string]float64{}

	for name, f := range fams {
		for _, m := range f.GetMetric() {
			switch f.GetType() {
			case dto.MetricType_COUNTER:
				out[seriesKey(name, m.GetLabel())] = m.GetCounter().GetValue()
			case dto.MetricType_GAUGE:
				out[seriesKey(name, m.GetLabel())] = m.GetGauge().GetValue()
			case dto.MetricType_SUMMARY:
				out[seriesKey(name+"_sum", m.GetLabel())] = m.GetSummary().GetSampleSum()
				out[seriesKey(name+"_count", m.GetLabel())] = float64(m.GetSummary().GetSampleCount())
			case dto.MetricType_UNTYPED, dto.MetricType_HISTOGRAM, dto.MetricType_GAUGE_HISTOGRAM:
			}
		}
	}

	return out
}

func TestMetricsFormat(t *testing.T) {
	svc, _ := realChain(t)
	h := newMetricsServer(t, svc, Config{}).Handler()
	do(h, http.MethodGet, "/health", "")

	rec, fams := families(t, h)

	if got, want := rec.Header().Get("Content-Type"), "text/plain; version=0.0.4; charset=utf-8; escaping=underscores"; got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}

	want := map[string]dto.MetricType{
		"http_requests_total":                dto.MetricType_COUNTER,
		"blockchain_blocks":                  dto.MetricType_GAUGE,
		"blockchain_mining_duration_seconds": dto.MetricType_SUMMARY,
		"go_goroutines":                      dto.MetricType_GAUGE,
	}
	if runtime.GOOS == "linux" {
		want["process_start_time_seconds"] = dto.MetricType_GAUGE
	}

	for name, typ := range want {
		f := fams[name]
		if f == nil || f.GetType() != typ || len(f.GetMetric()) == 0 || f.GetHelp() == "" {
			t.Errorf("family %s = %v, want type %s with help and a sample", name, f, typ)
		}
	}

	var labels []string
	for _, l := range fams["http_requests_total"].GetMetric()[0].GetLabel() {
		labels = append(labels, l.GetName())
	}

	if !slices.Equal(labels, []string{"code", "method", "route"}) {
		t.Errorf("http_requests_total labels = %v, want code, method, route", labels)
	}

	if q := fams["blockchain_mining_duration_seconds"].GetMetric()[0].GetSummary().GetQuantile(); len(q) != 0 {
		t.Errorf("mining summary has quantiles %v, want none", q)
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
		`http_requests_total{code="404",method="GET",route="GET /block/{ref}"}`: 3,
		`http_requests_total{code="200",method="GET",route="GET /block/{ref}"}`: 1,
		`http_requests_total{code="405",method="PUT",route="/blocks"}`:          1,
	} {
		if got[series] != want {
			t.Errorf("%s = %v, want %v", series, got[series], want)
		}
	}

	// The scrape counts itself only after it has been answered.
	if v := scrape(t, h)[`http_requests_total{code="200",method="GET",route="GET /metrics"}`]; v != 1 {
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

	if v := scrape(t, h)[`http_requests_total{code="404",method="GET",route="unmatched"}`]; v != 50 {
		t.Errorf("unmatched GET 404 = %v, want 50", v)
	}

	if v := scrape(t, h)[`http_requests_total{code="404",method="OTHER",route="unmatched"}`]; v != 2 {
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

	if v := scrape(t, a)[`http_requests_total{code="404",method="GET",route="unmatched"}`]; v != 1 {
		t.Errorf("first server counted %v, want 1", v)
	}

	if v := scrape(t, b)[`http_requests_total{code="404",method="GET",route="unmatched"}`]; v != 0 {
		t.Errorf("second server saw %v requests of the first", v)
	}
}

func TestMetricsConcurrentScrape(t *testing.T) {
	svc, _ := realChain(t)
	h := newMetricsServer(t, svc, Config{}).Handler()

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for range 20 {
				do(h, http.MethodGet, []string{"/", "/nope", "/metrics", "/health"}[i%4], "")
			}
		})
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
