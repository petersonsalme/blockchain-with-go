package dashboard

import (
	"fmt"
	"testing"
)

// chain is the blocks from..to of a chain whose hashes start with prefix.
func chain(prefix string, from, to int) []BlockRef {
	var out []BlockRef

	for h := from; h <= to; h++ {
		out = append(out, BlockRef{Height: h, Hash: fmt.Sprintf("%s%d", prefix, h)})
	}

	return out
}

// laneCard is a seen card named name, at height h with tip, and blocks.
func laneCard(name string, reachable bool, h int, tip string, blocks []BlockRef) Card {
	return Card{Snapshot: Snapshot{Name: name, Reachable: reachable, Seen: true, Height: h, Tip: tip, Blocks: blocks}}
}

// kinds returns the Kind of every cell of a lane, oldest first.
func kinds(l Lane) []string {
	out := make([]string, 0, len(l.Cells))
	for _, c := range l.Cells {
		out = append(out, c.Kind)
	}

	return out
}

// hashes returns the Hash of every cell of a lane, oldest first.
func hashes(l Lane) []string {
	out := make([]string, 0, len(l.Cells))
	for _, c := range l.Cells {
		out = append(out, c.Hash)
	}

	return out
}

func TestLanesWindow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cards     []Card
		majority  string
		lo, top   int
		lanes     int
		wantKinds map[int][]string // lane index to kinds
	}{
		{
			name: "aligned",
			cards: []Card{
				laneCard("n1", true, 10, "a10", chain("a", 3, 10)),
				laneCard("n2", true, 10, "a10", chain("a", 3, 10)),
				laneCard("n3", true, 10, "a10", chain("a", 3, 10)),
			},
			majority:  "a10",
			lo:        3,
			top:       10,
			lanes:     3,
			wantKinds: map[int][]string{0: {"", "", "", "", "", "", "", "tip"}},
		},
		{
			name:      "short chain",
			cards:     []Card{laneCard("n1", true, 3, "a3", chain("a", 0, 3))},
			majority:  "a3",
			lo:        0,
			top:       3,
			lanes:     1,
			wantKinds: map[int][]string{0: {"", "", "", "tip"}},
		},
		{
			name: "behind",
			cards: []Card{
				laneCard("n1", true, 10, "a10", chain("a", 3, 10)),
				laneCard("n2", true, 8, "a8", chain("a", 1, 8)),
				laneCard("n3", true, 10, "a10", chain("a", 3, 10)),
			},
			majority:  "a10",
			lo:        3,
			top:       10,
			lanes:     3,
			wantKinds: map[int][]string{1: {"", "", "", "", "", "behind", "missing", "missing"}},
		},
		{
			name: "stopped",
			cards: []Card{
				laneCard("n1", true, 12, "a12", chain("a", 5, 12)),
				laneCard("n2", true, 12, "a12", chain("a", 5, 12)),
				laneCard("n3", false, 10, "a10", chain("a", 3, 10)),
			},
			majority:  "a12",
			lo:        5,
			top:       12,
			lanes:     3,
			wantKinds: map[int][]string{2: {"stale", "stale", "stale", "stale", "stale", "stale", "missing", "missing"}},
		},
		{
			name: "all down",
			cards: []Card{
				laneCard("n1", false, 9, "a9", chain("a", 2, 9)),
				laneCard("n2", false, 10, "a10", chain("a", 3, 10)),
			},
			majority:  "",
			lo:        3,
			top:       10,
			lanes:     2,
			wantKinds: map[int][]string{0: {"stale", "stale", "stale", "stale", "stale", "stale", "stale", "missing"}, 1: {"stale", "stale", "stale", "stale", "stale", "stale", "stale", "stale"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			lo, top, out := lanes(tc.cards, StateAgreement, tc.majority)
			if lo != tc.lo || top != tc.top {
				t.Fatalf("window = %d..%d, want %d..%d", lo, top, tc.lo, tc.top)
			}

			if len(out) != tc.lanes {
				t.Fatalf("lanes = %d, want %d", len(out), tc.lanes)
			}

			for i, c := range tc.cards {
				if out[i].Name != c.Name {
					t.Errorf("lane %d is %q, want %q (NODES order)", i, out[i].Name, c.Name)
				}
			}

			for i, want := range tc.wantKinds {
				if got := kinds(out[i]); fmt.Sprint(got) != fmt.Sprint(want) {
					t.Errorf("lane %q kinds = %q, want %q", out[i].Name, got, want)
				}
			}
		})
	}
}

func TestLanesAlignedHashesAndHeights(t *testing.T) {
	t.Parallel()

	cards := []Card{
		laneCard("n1", true, 10, "a10", chain("a", 3, 10)),
		laneCard("n2", true, 10, "a10", chain("a", 3, 10)),
	}

	_, _, out := lanes(cards, StateAgreement, "a10")

	for _, l := range out {
		for i, c := range l.Cells {
			if c.Height != 3+i {
				t.Errorf("lane %q cell %d height = %d, want %d", l.Name, i, c.Height, 3+i)
			}
		}

		want := []string{"a3", "a4", "a5", "a6", "a7", "a8", "a9", "a10"}
		if got := hashes(l); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("lane %q hashes = %q, want %q", l.Name, got, want)
		}
	}
}

func TestLanesNoneSeen(t *testing.T) {
	t.Parallel()

	lo, top, out := lanes([]Card{{Snapshot: Snapshot{Name: "n1"}}}, StateNone, "")
	if out != nil || lo != 0 || top != 0 {
		t.Fatalf("lanes = %d, %d, %v; want no lane at 0..0", lo, top, out)
	}
}

func TestLanesFork(t *testing.T) {
	t.Parallel()

	forked := append(chain("a", 3, 6), chain("b", 7, 10)...)

	tests := []struct {
		name     string
		cards    []Card
		state    State
		majority string
		lane     int // the lane to check
		want     []string
		noFork   []int // lanes that must have no "fork" cell
	}{
		{
			name: "race before the verdict",
			cards: []Card{
				laneCard("n1", true, 10, "a10", chain("a", 3, 10)),
				laneCard("n2", true, 10, "b10", forked),
				laneCard("n3", true, 10, "a10", chain("a", 3, 10)),
			},
			state:    StateSyncing,
			majority: "a10",
			lane:     1,
			want:     []string{"", "", "", "", "fork", "fork", "fork", "tip"},
			noFork:   []int{0, 2},
		},
		{
			name: "confirmed fork",
			cards: []Card{
				laneCard("n1", true, 10, "a10", chain("a", 3, 10)),
				laneCard("n2", true, 10, "b10", forked),
				laneCard("n3", true, 10, "a10", chain("a", 3, 10)),
			},
			state:    StateFork,
			majority: "a10",
			lane:     1,
			want:     []string{"", "", "", "", "fork", "fork", "fork", "fork"},
			noFork:   []int{0, 2},
		},
		{
			name: "trailing branch",
			cards: []Card{
				laneCard("n1", true, 10, "a10", chain("a", 3, 10)),
				laneCard("n2", true, 9, "b9", append(chain("a", 3, 6), chain("b", 7, 9)...)),
				laneCard("n3", true, 10, "a10", chain("a", 3, 10)),
			},
			state:    StateFork,
			majority: "a10",
			lane:     1,
			want:     []string{"", "", "", "", "fork", "fork", "fork", "missing"},
			noFork:   []int{0, 2},
		},
		{
			name: "tie favours the first node",
			cards: []Card{
				laneCard("n1", true, 10, "b10", forked),
				laneCard("n2", true, 10, "a10", chain("a", 3, 10)),
			},
			state:    StateFork,
			majority: "b10",
			lane:     1,
			want:     []string{"", "", "", "", "fork", "fork", "fork", "fork"},
			noFork:   []int{0},
		},
		{
			name: "minority node first",
			cards: []Card{
				laneCard("n1", true, 10, "b10", forked),
				laneCard("n2", true, 10, "a10", chain("a", 3, 10)),
				laneCard("n3", true, 10, "a10", chain("a", 3, 10)),
			},
			state:    StateFork,
			majority: "a10",
			lane:     0,
			want:     []string{"", "", "", "", "fork", "fork", "fork", "fork"},
			noFork:   []int{1, 2},
		},
		{
			name: "reference skips a behind card",
			cards: []Card{
				laneCard("n1", true, 8, "a8", chain("a", 3, 8)),
				laneCard("n2", true, 10, "a10", chain("a", 3, 10)),
				laneCard("n3", true, 10, "b10", forked),
			},
			state:    StateFork,
			majority: "a10",
			lane:     2,
			want:     []string{"", "", "", "", "fork", "fork", "fork", "fork"},
			noFork:   []int{0},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, _, out := lanes(tc.cards, tc.state, tc.majority)

			if got := kinds(out[tc.lane]); fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("lane %q kinds = %q, want %q", out[tc.lane].Name, got, tc.want)
			}

			for _, i := range tc.noFork {
				for _, c := range out[i].Cells {
					if c.Kind == "fork" {
						t.Errorf("lane %q cell %d is a fork, want none", out[i].Name, c.Height)
					}
				}
			}
		})
	}
}
