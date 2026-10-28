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
	plotBottom   = plotTop + plotHeight
	flatY        = plotTop + plotHeight/2
	headerCount  = 50 // headers behind the interval chart
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

// chartsView is what the charts fragment renders. IntervalMsg replaces the interval chart when the
// chosen node's headers cannot be drawn; the height chart does not depend on it.
type chartsView struct {
	Node        string
	Intervals   barChart
	IntervalMsg string
	Heights     lineChart
	HasHeights  bool
	Ticks       int
}

func f2(v float64) string { return fmt.Sprintf("%.2f", v) }

// intervalChart draws one bar per value, scaled so the largest fills the plot. Values come only
// from parsed numbers; a negative one (clock skew between nodes) is drawn as zero.
func intervalChart(values []float64) barChart {
	top := 0.0
	for _, v := range values {
		top = max(top, v)
	}

	slot := float64(chartWidth) / float64(max(len(values), 1))
	chart := barChart{Max: fmt.Sprintf("%.1f s", top)}

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

// heightChart draws one polyline per node over the ticks the poller kept. All lines share one
// scale; a chart where every height is equal is drawn at mid height.
func heightChart(history []Series) lineChart {
	lo, hi, seen := 0, 0, false

	for _, s := range history {
		for _, h := range s.Heights {
			if !seen || h < lo {
				lo = h
			}

			if !seen || h > hi {
				hi = h
			}

			seen = true
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

// charts builds both charts for node. The height history comes from the poller; the intervals cost
// one /headers read of the node, from the height the poller last saw.
func (s *server) charts(ctx context.Context, node Node) chartsView {
	view := chartsView{Node: node.Name, Ticks: HistoryTicks, Heights: heightChart(s.poll.History())}
	view.HasHeights = len(view.Heights.Lines) > 0

	snap, ok := s.cardOf(node.Name)
	if !ok || !snap.Seen || snap.Height < 1 {
		view.IntervalMsg = "Not enough blocks yet."

		return view
	}

	var headers headersBody

	perr := s.read(ctx, node, Headers(max(snap.Height-headerCount+1, 0), headerCount), &headers)
	if perr != nil {
		view.IntervalMsg = notAvailable

		return view
	}

	blocks := headers.Blocks
	if len(blocks) < 2 {
		view.IntervalMsg = "Not enough blocks yet."

		return view
	}

	values := make([]float64, len(blocks)-1)
	for i := range values {
		values[i] = blocks[i+1].Timestamp.Sub(blocks[i].Timestamp).Seconds()
	}

	view.Intervals = intervalChart(values)

	return view
}

func (s *server) chartsFragment(w http.ResponseWriter, r *http.Request) {
	node, ok := s.chosenNode(r)
	if !ok {
		s.fail(w, "", noSuchNode())

		return
	}

	s.show(w, "", "charts", s.charts(r.Context(), node), true)
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
