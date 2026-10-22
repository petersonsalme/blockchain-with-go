package server

import (
	"bufio"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// metricsContentType is the Prometheus text exposition format, version 0.0.4.
const metricsContentType = "text/plain; version=0.0.4; charset=utf-8"

// otherMethod replaces request methods the server does not know, so a client cannot mint series.
const otherMethod = "OTHER"

// requestKey identifies one http_requests_total series.
type requestKey struct {
	route  string
	method string
	code   int
}

// metrics holds the counters of one Server; nothing here is shared between Servers.
type metrics struct {
	mu       sync.Mutex
	requests map[requestKey]uint64
	mineSum  time.Duration
	mineN    uint64
}

func newMetrics() *metrics {
	return &metrics{requests: map[requestKey]uint64{}}
}

// observeRequest counts one answered request; route is a ServeMux pattern or unmatchedRoute.
func (m *metrics) observeRequest(method, route string, code int) {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace:
	default:
		method = otherMethod
	}
	m.mu.Lock()
	m.requests[requestKey{route: route, method: method, code: code}]++
	m.mu.Unlock()
}

// observeMining records one Mine call that returned a block.
func (m *metrics) observeMining(d time.Duration) {
	m.mu.Lock()
	m.mineSum += d
	m.mineN++
	m.mu.Unlock()
}

// middleware counts every request served by next, with the route logRequests reports.
func (m *metrics) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		m.observeRequest(r.Method, routeOf(r), rec.status)
	})
}

// escapeLabelValue escapes a label value for the text format: backslash, double quote and newline.
var escapeLabelValue = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace

// write renders every family. The blocks gauge is skipped when haveBlocks is false.
func (m *metrics) write(w io.Writer, blocks int, haveBlocks bool) {
	m.mu.Lock()
	keys := make([]requestKey, 0, len(m.requests))
	counts := make(map[requestKey]uint64, len(m.requests))
	for k, v := range m.requests {
		keys = append(keys, k)
		counts[k] = v
	}
	sum, n := m.mineSum, m.mineN
	m.mu.Unlock()

	slices.SortFunc(keys, func(a, b requestKey) int {
		if c := strings.Compare(a.route, b.route); c != 0 {
			return c
		}
		if c := strings.Compare(a.method, b.method); c != 0 {
			return c
		}
		return a.code - b.code
	})

	bw := bufio.NewWriter(w)
	bw.WriteString("# HELP http_requests_total HTTP requests served, by method, route pattern and status code.\n")
	bw.WriteString("# TYPE http_requests_total counter\n")
	for _, k := range keys {
		bw.WriteString(`http_requests_total{method="` + escapeLabelValue(k.method) +
			`",route="` + escapeLabelValue(k.route) +
			`",code="` + strconv.Itoa(k.code) + `"} ` + strconv.FormatUint(counts[k], 10) + "\n")
	}
	if haveBlocks {
		bw.WriteString("# HELP blockchain_blocks Number of blocks in the chain.\n")
		bw.WriteString("# TYPE blockchain_blocks gauge\n")
		bw.WriteString("blockchain_blocks " + strconv.Itoa(blocks) + "\n")
	}
	bw.WriteString("# HELP blockchain_mining_duration_seconds Time spent mining blocks that were produced.\n")
	bw.WriteString("# TYPE blockchain_mining_duration_seconds summary\n")
	bw.WriteString("blockchain_mining_duration_seconds_sum " + strconv.FormatFloat(sum.Seconds(), 'g', -1, 64) + "\n")
	bw.WriteString("blockchain_mining_duration_seconds_count " + strconv.FormatUint(n, 10) + "\n")
	_ = bw.Flush() // a failed write to the client cannot be reported
}

// handleMetrics serves the Prometheus text exposition of s.
func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	tip, err := s.chain.Tip()
	w.Header().Set("Content-Type", metricsContentType)
	w.WriteHeader(http.StatusOK)
	if err != nil {
		s.metrics.write(w, 0, false)
		return
	}
	s.metrics.write(w, tip.Index+1, true)
}
