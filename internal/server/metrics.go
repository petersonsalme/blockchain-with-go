package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// otherMethod replaces request methods the server does not know, so a client cannot mint series.
const otherMethod = "OTHER"

// metrics holds the registry and the families of one Server; nothing here is shared between Servers (ADR-0022).
type metrics struct {
	handler  http.Handler
	requests *prometheus.CounterVec
	mining   prometheus.Summary
}

// blocksCollector reports blockchain_blocks from Tip() at scrape time.
// It sends nothing when Tip fails, so the family is omitted instead of reported as zero.
type blocksCollector struct {
	chain Chain
	desc  *prometheus.Desc
}

func (c blocksCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c blocksCollector) Collect(ch chan<- prometheus.Metric) {
	tip, err := c.chain.Tip()
	if err != nil {
		return
	}

	ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, float64(tip.Index+1))
}

func newMetrics(c Chain) *metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		blocksCollector{chain: c, desc: prometheus.NewDesc("blockchain_blocks", "Number of blocks in the chain.", nil, nil)},
	)

	f := promauto.With(reg)

	return &metrics{
		handler: promhttp.HandlerFor(reg, promhttp.HandlerOpts{}),
		requests: f.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "HTTP requests served, by method, route pattern and status code.",
		}, []string{"method", "route", "code"}),
		mining: f.NewSummary(prometheus.SummaryOpts{
			Name: "blockchain_mining_duration_seconds",
			Help: "Time spent mining blocks that were produced.",
		}),
	}
}

// observeRequest counts one answered request; route is a ServeMux pattern or unmatchedRoute.
func (m *metrics) observeRequest(method, route string, code int) {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace:
	default:
		method = otherMethod
	}

	m.requests.WithLabelValues(method, route, strconv.Itoa(code)).Inc()
}

// observeMining records one Mine call that returned a block.
func (m *metrics) observeMining(d time.Duration) {
	m.mining.Observe(d.Seconds())
}

// middleware counts every request served by next, with the route logRequests reports.
func (m *metrics) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		m.observeRequest(r.Method, routeOf(r), rec.status)
	})
}

// handleMetrics serves the Prometheus text exposition of s.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	s.metrics.handler.ServeHTTP(w, r)
}
