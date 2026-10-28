package dashboard

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

const notAvailable = "n/a"

// NodeMetrics is what the Metrics screen reads of a node's /metrics text: blockchain_blocks, the sum
// and count of blockchain_mining_duration_seconds, and http_requests_total summed by status class
// (429 is counted apart and left out of 4xx). Every other family is ignored.
type NodeMetrics struct {
	Blocks      float64
	HasBlocks   bool
	MiningSum   float64
	MiningCount float64
	Req2xx      float64
	Req4xx      float64
	Req5xx      float64
	Req429      float64
}

// MeanMining is the mean mining duration, or "n/a" when no block was mined.
func (m NodeMetrics) MeanMining() string {
	if m.MiningCount == 0 {
		return notAvailable
	}

	return strconv.FormatFloat(m.MiningSum/m.MiningCount, 'f', 6, 64) + " s"
}

// ParseMetrics reads the Prometheus text exposition format, as far as the dashboard needs it. A
// sample line it cannot read is an error, whatever its family, so a half-written answer never
// shows partial figures.
func ParseMetrics(body []byte) (NodeMetrics, error) {
	var m NodeMetrics

	for n, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		name, labels, value, err := parseSample(line)
		if err != nil {
			return NodeMetrics{}, fmt.Errorf("line %d: %w", n+1, err)
		}

		switch name {
		case "blockchain_blocks":
			m.Blocks, m.HasBlocks = value, true
		case "blockchain_mining_duration_seconds_sum":
			m.MiningSum = value
		case "blockchain_mining_duration_seconds_count":
			m.MiningCount = value
		case "http_requests_total":
			addRequests(&m, labels["code"], value)
		}
	}

	return m, nil
}

func addRequests(m *NodeMetrics, code string, value float64) {
	if code == "429" {
		m.Req429 += value

		return
	}

	switch {
	case strings.HasPrefix(code, "2"):
		m.Req2xx += value
	case strings.HasPrefix(code, "4"):
		m.Req4xx += value
	case strings.HasPrefix(code, "5"):
		m.Req5xx += value
	}
}

// parseSample splits `name{label="value",...} value [timestamp]`.
func parseSample(line string) (string, map[string]string, float64, error) {
	end := strings.IndexAny(line, "{ \t")
	if end <= 0 {
		return "", nil, 0, errors.New("sample has no name or no value")
	}

	name, rest := line[:end], line[end:]

	var labels map[string]string

	if rest[0] == '{' {
		var err error

		labels, rest, err = parseLabels(rest[1:])
		if err != nil {
			return "", nil, 0, err
		}
	}

	fields := strings.Fields(rest)
	if len(fields) == 0 || len(fields) > 2 {
		return "", nil, 0, errors.New("sample needs a value and at most a timestamp")
	}

	value, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return "", nil, 0, fmt.Errorf("sample value: %w", err)
	}

	if len(fields) == 2 {
		_, err = strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return "", nil, 0, fmt.Errorf("sample timestamp: %w", err)
		}
	}

	return name, labels, value, nil
}

// parseLabels reads `label="value",...}` (the opening brace is already consumed) and returns the
// labels and what follows the closing brace.
func parseLabels(s string) (map[string]string, string, error) {
	labels := map[string]string{}

	for {
		s = strings.TrimLeft(s, " \t")
		if strings.HasPrefix(s, "}") {
			return labels, s[1:], nil
		}

		eq := strings.IndexByte(s, '=')
		if eq <= 0 || !strings.HasPrefix(s[eq+1:], `"`) {
			return nil, "", errors.New(`label must be name="value"`)
		}

		key := strings.TrimSpace(s[:eq])

		value, after, err := quoted(s[eq+2:])
		if err != nil {
			return nil, "", err
		}

		labels[key] = value

		after = strings.TrimLeft(after, " \t")

		switch {
		case strings.HasPrefix(after, ","):
			s = after[1:]
		case strings.HasPrefix(after, "}"):
			s = after
		default:
			return nil, "", errors.New("label value must be followed by , or }")
		}
	}
}

// quoted returns the raw text of a label value up to its closing quote, skipping escaped
// characters, and what follows the quote. Only delimiting matters: no figure depends on a value.
func quoted(s string) (string, string, error) {
	escaped := false

	for i := range len(s) {
		switch {
		case escaped:
			escaped = false
		case s[i] == '\\':
			escaped = true
		case s[i] == '"':
			return s[:i], s[i+1:], nil
		}
	}

	return "", "", errors.New("label value has no closing quote")
}

// metricsRow is one node as the Metrics table shows it; every figure is "n/a" when /metrics could
// not be read or parsed.
type metricsRow struct {
	Name                           string
	Blocks, Mining                 string
	Req2xx, Req4xx, Req5xx, Req429 string
}

func figure(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func rowOf(name string, m NodeMetrics) metricsRow {
	row := metricsRow{
		Name: name, Blocks: notAvailable, Mining: m.MeanMining(),
		Req2xx: figure(m.Req2xx), Req4xx: figure(m.Req4xx), Req5xx: figure(m.Req5xx), Req429: figure(m.Req429),
	}
	if m.HasBlocks {
		row.Blocks = figure(m.Blocks)
	}

	return row
}

func unavailable(name string) metricsRow {
	return metricsRow{name, notAvailable, notAvailable, notAvailable, notAvailable, notAvailable, notAvailable}
}

// metricsRows reads /metrics of every node at once. A node that fails to answer or to parse
// leaves its own row at "n/a" and nothing else.
func (s *server) metricsRows(ctx context.Context) []metricsRow {
	rows := make([]metricsRow, len(s.nodes))

	var wg sync.WaitGroup

	for i, node := range s.nodes {
		wg.Go(func() {
			rows[i] = unavailable(node.Name)

			body, err := s.fetch.Get(ctx, node, Metrics())
			if err != nil {
				return
			}

			m, err := ParseMetrics(body)
			if err != nil {
				return
			}

			rows[i] = rowOf(node.Name, m)
		})
	}

	wg.Wait()

	return rows
}

type metricsPage struct {
	Nodes  []Node
	Node   string
	Rows   []metricsRow
	Charts chartsView
}

// metricsScreen is the Metrics page: the figures table and, for the chosen node, the charts.
func (s *server) metricsScreen(w http.ResponseWriter, r *http.Request) {
	node, ok := s.chosenNode(r)
	if !ok {
		s.fail(w, "", noSuchNode())

		return
	}

	s.show(w, "", "metricspage", metricsPage{
		Nodes: s.nodes, Node: node.Name, Rows: s.metricsRows(r.Context()), Charts: s.charts(r.Context(), node),
	}, false)
}

func (s *server) metricsFragment(w http.ResponseWriter, r *http.Request) {
	s.show(w, "", "metrics", s.metricsRows(r.Context()), true)
}
