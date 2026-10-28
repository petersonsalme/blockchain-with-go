package dashboard

import (
	"fmt"
	"time"
)

// forkPolls is how many consecutive ticks the same-height-different-tips condition must hold
// before the network is called forked; fewer count as syncing (two nodes may mine at once).
const forkPolls = 3

// State is the verdict of Assess over the reachable nodes.
type State int

// The states of the network.
const (
	StateNone State = iota
	StateAgreement
	StateSyncing
	StateFork
)

// String is the CSS class suffix of the state.
func (s State) String() string {
	switch s {
	case StateNone:
		return "none"
	case StateAgreement:
		return "agreement"
	case StateSyncing:
		return "syncing"
	case StateFork:
		return "fork"
	}

	return "unknown"
}

// Snapshot is what the poller last learned about one node. When the node is unreachable, Height,
// Tip and the other fields keep the last known values; Seen tells whether there ever was one.
type Snapshot struct {
	Name      string
	Reachable bool
	Seen      bool
	Height    int
	Tip       string
	Peers     int
	PeerAddrs []string
	TipTime   time.Time
}

// Assessment is the verdict over a set of snapshots.
type Assessment struct {
	State       State
	Height      int            // height of the reachable nodes when they are on the same one
	Unreachable int            // nodes not reachable
	Total       int            // nodes configured
	Behind      map[string]int // blocks each node (an unreachable one at its last known height) is behind the highest reachable node
}

// forkCandidate reports whether at least two nodes are reachable at one height with different tips.
func forkCandidate(snaps []Snapshot) bool {
	var first *Snapshot

	for i := range snaps {
		s := &snaps[i]
		if !s.Reachable {
			continue
		}

		if first == nil {
			first = s

			continue
		}

		if s.Height == first.Height && s.Tip != first.Tip && sameHeight(snaps) {
			return true
		}
	}

	return false
}

// sameHeight reports whether every reachable node is at one height.
func sameHeight(snaps []Snapshot) bool {
	height, set := 0, false

	for _, s := range snaps {
		if !s.Reachable {
			continue
		}

		if set && s.Height != height {
			return false
		}

		height, set = s.Height, true
	}

	return true
}

// Assess compares the reachable nodes only. forkTicks is how many consecutive ticks (this one
// included) forkCandidate held. Different heights are syncing; one height with different tips is
// syncing until forkTicks reaches forkPolls, then a fork; otherwise agreement.
func Assess(snaps []Snapshot, forkTicks int) Assessment {
	a := Assessment{Total: len(snaps), Behind: map[string]int{}}

	maxHeight, anyReachable := 0, false

	for _, s := range snaps {
		if !s.Reachable {
			a.Unreachable++

			continue
		}

		anyReachable = true
		maxHeight = max(maxHeight, s.Height)
	}

	for _, s := range snaps {
		if s.Seen && s.Height < maxHeight {
			a.Behind[s.Name] = maxHeight - s.Height
		}
	}

	a.Height = maxHeight

	if !anyReachable {
		a.State = StateNone

		return a
	}

	candidate := forkCandidate(snaps)

	if !sameHeight(snaps) {
		a.State = StateSyncing

		return a
	}

	if candidate && forkTicks >= forkPolls {
		a.State = StateFork

		return a
	}

	a.State = StateAgreement
	if candidate {
		a.State = StateSyncing
	}

	return a
}

// Text is the banner sentence; "k of n unreachable" is appended whenever k > 0 and a node answers.
func (a Assessment) Text() string {
	var text string

	switch a.State {
	case StateNone:
		return "No node reachable"
	case StateAgreement:
		text = "Network in agreement"
	case StateSyncing:
		text = "Network syncing"
	case StateFork:
		text = fmt.Sprintf("Fork: nodes report different tips at height %d", a.Height)
	}

	if a.Unreachable > 0 {
		text += fmt.Sprintf(" · %d of %d unreachable", a.Unreachable, a.Total)
	}

	return text
}
