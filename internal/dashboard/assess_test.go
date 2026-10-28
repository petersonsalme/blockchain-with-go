package dashboard_test

import (
	"testing"

	"github.com/petersonsalme/blockchain-with-go/internal/dashboard"
)

func snap(name string, up bool, height int, tip string) dashboard.Snapshot {
	return dashboard.Snapshot{Name: name, Reachable: up, Seen: true, Height: height, Tip: tip}
}

func TestAssess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		snaps       []dashboard.Snapshot
		forkTicks   int
		want        dashboard.State
		unreachable int
		behind      map[string]int
		text        string
	}{
		{"same height and tip", []dashboard.Snapshot{snap("a", true, 5, "x"), snap("b", true, 5, "x")}, 0, dashboard.StateAgreement, 0, nil, "Network in agreement"},
		{"different heights", []dashboard.Snapshot{snap("a", true, 7, "x"), snap("b", true, 5, "y"), snap("c", true, 7, "x")}, 0, dashboard.StateSyncing, 0, map[string]int{"b": 2}, "Network syncing"},
		{"different tips, 1 tick", []dashboard.Snapshot{snap("a", true, 5, "x"), snap("b", true, 5, "y")}, 1, dashboard.StateSyncing, 0, nil, "Network syncing"},
		{"different tips, 2 ticks", []dashboard.Snapshot{snap("a", true, 5, "x"), snap("b", true, 5, "y")}, 2, dashboard.StateSyncing, 0, nil, "Network syncing"},
		{"different tips, 3 ticks", []dashboard.Snapshot{snap("a", true, 5, "x"), snap("b", true, 5, "y")}, 3, dashboard.StateFork, 0, nil, "Fork: nodes report different tips at height 5"},
		{"unreachable left out and counted", []dashboard.Snapshot{snap("a", true, 5, "x"), snap("b", true, 5, "x"), snap("c", false, 2, "z")}, 0, dashboard.StateAgreement, 1, map[string]int{"c": 3}, "Network in agreement · 1 of 3 unreachable"},
		{"unreachable never seen has no gap", []dashboard.Snapshot{snap("a", true, 5, "x"), {Name: "c"}}, 0, dashboard.StateAgreement, 1, nil, "Network in agreement · 1 of 2 unreachable"},
		{"syncing with unreachable", []dashboard.Snapshot{snap("a", true, 6, "x"), snap("b", true, 5, "y"), snap("c", false, 6, "x")}, 0, dashboard.StateSyncing, 1, map[string]int{"b": 1}, "Network syncing · 1 of 3 unreachable"},
		{"none reachable", []dashboard.Snapshot{snap("a", false, 5, "x"), snap("b", false, 5, "x")}, 0, dashboard.StateNone, 2, nil, "No node reachable"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := dashboard.Assess(tc.snaps, tc.forkTicks)
			if got.State != tc.want {
				t.Errorf("state = %v, want %v", got.State, tc.want)
			}

			if got.Unreachable != tc.unreachable || got.Total != len(tc.snaps) {
				t.Errorf("unreachable %d of %d, want %d of %d", got.Unreachable, got.Total, tc.unreachable, len(tc.snaps))
			}

			for name, d := range tc.behind {
				if got.Behind[name] != d {
					t.Errorf("behind[%s] = %d, want %d", name, got.Behind[name], d)
				}
			}

			wantN := 0

			for _, d := range tc.behind {
				if d > 0 {
					wantN++
				}
			}

			if len(got.Behind) != wantN {
				t.Errorf("behind = %v, want only %v", got.Behind, tc.behind)
			}

			if got.Text() != tc.text {
				t.Errorf("text = %q, want %q", got.Text(), tc.text)
			}
		})
	}
}
