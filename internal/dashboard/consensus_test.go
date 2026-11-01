package dashboard

import "testing"

func ctPoW(retarget int) *powConfig {
	return &powConfig{InitialDifficulty: 4, RetargetInterval: retarget, TargetBlockTimeMS: 1000}
}

func ctPoA(validators ...string) *poaConfig {
	return &poaConfig{Validators: validators}
}

func ctBody(tip, work string, pow *powConfig) consensusBody {
	return consensusBody{Engine: "pow", Height: 5, Tip: tip, ChainWork: work, PoW: pow}
}

func ctNode(name string, info consensusBody) consensusNode {
	return consensusNode{Name: name, Info: info}
}

func TestBlockWork(t *testing.T) {
	tests := []struct {
		difficulty int
		want       string
	}{
		{0, "1"},
		{1, "2"},
		{10, "1024"},
		{70, "1180591620717411303424"},
		{-3, "1"},
	}
	for _, tt := range tests {
		if got := blockWork(tt.difficulty); got != tt.want {
			t.Errorf("blockWork(%d) = %q, want %q", tt.difficulty, got, tt.want)
		}
	}
}

func TestRecentFrom(t *testing.T) {
	tests := []struct {
		height int
		want   int
	}{
		{0, 0},
		{3, 0},
		{9, 0},
		{10, 1},
		{20, 11},
	}
	for _, tt := range tests {
		if got := recentFrom(tt.height); got != tt.want {
			t.Errorf("recentFrom(%d) = %d, want %d", tt.height, got, tt.want)
		}
	}
}

func TestSameConfig(t *testing.T) {
	base := consensusBody{Engine: "pow", PoW: ctPoW(10)}

	tests := []struct {
		name string
		a, b consensusBody
		want bool
	}{
		{
			name: "equal pow",
			a:    base,
			b:    consensusBody{Engine: "pow", PoW: ctPoW(10)},
			want: true,
		},
		{
			name: "initial difficulty changed",
			a:    base,
			b:    consensusBody{Engine: "pow", PoW: &powConfig{InitialDifficulty: 5, RetargetInterval: 10, TargetBlockTimeMS: 1000}},
			want: false,
		},
		{
			name: "retarget interval changed",
			a:    base,
			b:    consensusBody{Engine: "pow", PoW: ctPoW(11)},
			want: false,
		},
		{
			name: "target block time changed",
			a:    base,
			b:    consensusBody{Engine: "pow", PoW: &powConfig{InitialDifficulty: 4, RetargetInterval: 10, TargetBlockTimeMS: 2000}},
			want: false,
		},
		{
			name: "engine changed",
			a:    base,
			b:    consensusBody{Engine: "pow2", PoW: ctPoW(10)},
			want: false,
		},
		{
			name: "poa validators same",
			a:    consensusBody{Engine: "poa", PoA: ctPoA("v1", "v2")},
			b:    consensusBody{Engine: "poa", PoA: ctPoA("v1", "v2")},
			want: true,
		},
		{
			name: "poa validators reordered",
			a:    consensusBody{Engine: "poa", PoA: ctPoA("v1", "v2")},
			b:    consensusBody{Engine: "poa", PoA: ctPoA("v2", "v1")},
			want: false,
		},
		{
			name: "poa validator missing",
			a:    consensusBody{Engine: "poa", PoA: ctPoA("v1", "v2")},
			b:    consensusBody{Engine: "poa", PoA: ctPoA("v1")},
			want: false,
		},
		{
			name: "pow nil versus non-nil",
			a:    consensusBody{Engine: "pow", PoW: nil},
			b:    consensusBody{Engine: "pow", PoW: ctPoW(10)},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameConfig(tt.a, tt.b); got != tt.want {
				t.Errorf("sameConfig() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConsensusViewDiffers(t *testing.T) {
	view := buildConsensusView([]consensusNode{
		ctNode("a", ctBody("t1", "10", ctPoW(10))),
		ctNode("b", ctBody("t1", "10", ctPoW(10))),
		ctNode("c", ctBody("t1", "10", ctPoW(20))),
	})
	wantDiffers := []bool{false, false, true}

	for i, want := range wantDiffers {
		if view.Nodes[i].Differs != want {
			t.Errorf("node %d Differs = %v, want %v", i, view.Nodes[i].Differs, want)
		}
	}

	if view.Same {
		t.Errorf("Same = true, want false when a node differs")
	}

	equal := buildConsensusView([]consensusNode{
		ctNode("a", ctBody("t1", "10", ctPoW(10))),
		ctNode("b", ctBody("t1", "10", ctPoW(10))),
		ctNode("c", ctBody("t1", "10", ctPoW(10))),
	})
	if !equal.Same {
		t.Errorf("Same = false, want true when all configurations are equal")
	}

	firstDiffers := buildConsensusView([]consensusNode{
		ctNode("a", ctBody("t1", "10", ctPoW(10))),
		ctNode("b", ctBody("t1", "10", ctPoW(20))),
	})
	if firstDiffers.Nodes[0].Differs {
		t.Errorf("node 0 Differs = true, want false when the first node is the reference")
	}

	if !firstDiffers.Nodes[1].Differs {
		t.Errorf("node 1 Differs = false, want true when its configuration differs from the first node")
	}
}

func TestConsensusViewFirstUnreachable(t *testing.T) {
	view := buildConsensusView([]consensusNode{
		{Name: "a", Err: "connection refused"},
		ctNode("b", ctBody("t1", "10", ctPoW(10))),
		ctNode("c", ctBody("t1", "10", ctPoW(10))),
	})
	for i, n := range view.Nodes {
		if n.Differs {
			t.Errorf("node %d (%s) Differs = true, want false", i, n.Name)
		}
	}

	if !view.Same {
		t.Errorf("Same = false, want true")
	}

	if view.Reachable != 2 {
		t.Errorf("Reachable = %d, want 2", view.Reachable)
	}
}

func TestConsensusViewNoFork(t *testing.T) {
	view := buildConsensusView([]consensusNode{
		ctNode("a", ctBody("t1", "10", ctPoW(10))),
		ctNode("b", ctBody("t1", "11", ctPoW(10))),
		ctNode("c", ctBody("t1", "12", ctPoW(10))),
	})
	if view.Fork {
		t.Errorf("Fork = true, want false when all reachable nodes share one tip")
	}

	for i, n := range view.Nodes {
		if n.Heaviest {
			t.Errorf("node %d Heaviest = true, want false without a fork", i)
		}
	}
}

func TestConsensusViewHeaviest(t *testing.T) {
	tests := []struct {
		name         string
		works        []string
		tips         []string
		wantHeaviest []bool
	}{
		{
			name:         "higher work wins numerically",
			works:        []string{"9", "10"},
			tips:         []string{"t1", "t2"},
			wantHeaviest: []bool{false, true},
		},
		{
			name:         "tie on different tips marks both",
			works:        []string{"10", "10"},
			tips:         []string{"t1", "t2"},
			wantHeaviest: []bool{true, true},
		},
		{
			name:         "unparsable work is never heaviest",
			works:        []string{"x", "9"},
			tips:         []string{"t1", "t2"},
			wantHeaviest: []bool{false, true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var nodes []consensusNode
			for i := range tt.works {
				nodes = append(nodes, ctNode(string(rune('a'+i)), ctBody(tt.tips[i], tt.works[i], ctPoW(10))))
			}

			view := buildConsensusView(nodes)
			if !view.Fork {
				t.Fatalf("Fork = false, want true")
			}

			for i, want := range tt.wantHeaviest {
				if view.Nodes[i].Heaviest != want {
					t.Errorf("node %d Heaviest = %v, want %v", i, view.Nodes[i].Heaviest, want)
				}
			}
		})
	}
}

func TestConsensusViewNoReachable(t *testing.T) {
	view := buildConsensusView([]consensusNode{
		{Name: "a", Err: "down"},
		{Name: "b", Err: "down"},
	})
	if view.Same {
		t.Errorf("Same = true, want false")
	}

	if view.Fork {
		t.Errorf("Fork = true, want false")
	}

	if view.Reachable != 0 {
		t.Errorf("Reachable = %d, want 0", view.Reachable)
	}
}
