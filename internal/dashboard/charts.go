package dashboard

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Chart geometry: every chart is drawn in a 400x120 viewBox with a 14-unit band on top for the
// scale label and 100 units of plot below it.
const (
	chartWidth   = 400
	chartHeight  = 120
	plotTop      = 14
	plotHeight   = 100
	plotBottom   = 114 // plotTop + plotHeight: the baseline
	flatY        = 64  // plotTop + plotHeight/2: where a flat series is drawn
	headerCount  = 50  // headers behind the interval chart
	barFill      = 0.8
	maxSeriesCls = 6
)

type bar struct{ X, Y, W, H string }

type barChart struct {
	Bars []bar
	Max  string
}

type line struct {
	Name   string
	Class  string
	Points string
}

type lineChart struct {
	Lines    []line
	Min, Max string
}

// chartsView is what the charts fragment renders. Each *Msg replaces its chart when that chart's
// data cannot be drawn; the others do not depend on it. The height chart does not depend on any.
type chartsView struct {
	Node          string
	Intervals     barChart
	IntervalMsg   string
	Txs           barChart
	TxsMsg        string
	Difficulty    barChart
	DifficultyMsg string
	Heights       lineChart
	HasHeights    bool
	Ticks         int
}

func f2(v float64) string { return fmt.Sprintf("%.2f", v) }

// bars draws one bar per value, scaled so the largest fills the plot and captioned with label of
// that largest value. Values come only from parsed numbers; a negative one (clock skew between
// nodes) is drawn as zero.
func bars(values []float64, label func(top float64) string) barChart {
	top := 0.0
	for _, v := range values {
		top = max(top, v)
	}

	slot := float64(chartWidth) / float64(max(len(values), 1))
	chart := barChart{Max: label(top)}

	for i, v := range values {
		h := 0.0
		if top > 0 {
			h = max(v, 0) / top * plotHeight
		}

		chart.Bars = append(chart.Bars, bar{
			X: f2(float64(i)*slot + slot*(1-barFill)/2), Y: f2(plotBottom - h), W: f2(slot * barFill), H: f2(h),
		})
	}

	return chart
}

// intervalChart draws the seconds between consecutive blocks.
func intervalChart(values []float64) barChart {
	return bars(values, func(t float64) string { return fmt.Sprintf("%.1f s", t) })
}

// whole labels a chart whose values are counts or difficulties.
func whole(t float64) string { return fmt.Sprintf("%.0f", t) }

// heightChart draws one polyline per node over the ticks the poller kept. All lines share one
// scale; a chart where every height is equal is drawn at mid height.
func heightChart(history []Series) lineChart {
	lo, hi, seen := 0, 0, false

	for _, s := range history {
		for _, h := range s.Heights {
			if !seen {
				lo, hi, seen = h, h, true

				continue
			}

			lo, hi = min(lo, h), max(hi, h)
		}
	}

	chart := lineChart{Min: strconv.Itoa(lo), Max: strconv.Itoa(hi)}

	for i, s := range history {
		pts := make([]string, len(s.Heights))

		for j, h := range s.Heights {
			y := float64(flatY)
			if hi > lo {
				y = plotBottom - float64(h-lo)/float64(hi-lo)*plotHeight
			}

			pts[j] = f2(float64(j)*chartWidth/float64(HistoryTicks-1)) + "," + f2(y)
		}

		chart.Lines = append(chart.Lines, line{Name: s.Name, Class: fmt.Sprintf("series-%d", i%maxSeriesCls), Points: strings.Join(pts, " ")})
	}

	return chart
}

// charts builds the charts for node. The height history comes from the poller; the intervals and
// difficulties cost one /headers read of the node, the transactions one /blocks read, all over the
// same last headerCount blocks as of the height the poller last saw.
func (s *server) charts(ctx context.Context, node Node) chartsView {
	view := chartsView{Node: node.Name, Ticks: HistoryTicks, Heights: heightChart(s.poll.History())}
	view.HasHeights = len(view.Heights.Lines) > 0

	snap, ok := s.cardOf(node.Name)
	if !ok || !snap.Seen || snap.Height < 1 {
		view.IntervalMsg, view.TxsMsg, view.DifficultyMsg = tooFewBlocks, tooFewBlocks, tooFewBlocks

		return view
	}

	from := max(snap.Height-headerCount+1, 0)

	var headers headersBody

	perr := s.read(ctx, node, Headers(from, headerCount), &headers)
	if perr != nil {
		view.IntervalMsg, view.TxsMsg, view.DifficultyMsg = notAvailable, notAvailable, notAvailable

		return view
	}

	blocks := headers.Blocks
	if len(blocks) < 2 {
		view.IntervalMsg, view.TxsMsg, view.DifficultyMsg = tooFewBlocks, tooFewBlocks, tooFewBlocks

		return view
	}

	values := make([]float64, len(blocks)-1)
	for i := range values {
		values[i] = blocks[i+1].Timestamp.Sub(blocks[i].Timestamp).Seconds()
	}

	view.Intervals = intervalChart(values)

	difficulties := make([]float64, len(blocks))
	for i := range difficulties {
		difficulties[i] = float64(blocks[i].Difficulty)
	}

	view.Difficulty = bars(difficulties, whole)

	var body struct {
		Blocks []nodeBlock `json:"blocks"`
	}

	if err := s.read(ctx, node, Blocks(from, headerCount), &body); err != nil {
		view.TxsMsg = notAvailable

		return view
	}

	txs := make([]float64, len(body.Blocks))
	for i := range txs {
		txs[i] = float64(len(body.Blocks[i].Transactions))
	}

	view.Txs = bars(txs, whole)

	return view
}

func (s *server) chartsFragment(w http.ResponseWriter, r *http.Request) {
	node, ok := s.chosenNode(r)
	if !ok {
		s.fail(w, place{}, noSuchNode())

		return
	}

	s.show(w, place{}, "charts", s.charts(r.Context(), node), true)
}

// chosenNode is the node named by ?node=, or the first one when the query has none.
func (s *server) chosenNode(r *http.Request) (Node, bool) {
	name := r.URL.Query().Get("node")
	if name == "" && len(s.nodes) > 0 {
		return s.nodes[0], true
	}

	return s.lookup(name)
}

// cardOf is the poller's latest snapshot of the node called name.
func (s *server) cardOf(name string) (Snapshot, bool) {
	for _, c := range s.poll.View().Cards {
		if c.Name == name {
			return c.Snapshot, true
		}
	}

	return Snapshot{}, false
}

const tooFewBlocks = "Not enough blocks yet."
