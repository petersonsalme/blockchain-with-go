package dashboard

import (
	"math/big"
	"slices"
)

// ConsensusBlocks is how many recent headers the Consensus screen lists per node.
const ConsensusBlocks = 10

// consensusBody is a node's GET /consensus answer (ADR-0026).
type consensusBody struct {
	Engine    string     `json:"engine"`
	Height    int        `json:"height"`
	Tip       string     `json:"tip"`
	ChainWork string     `json:"chain_work"`
	PoW       *powConfig `json:"pow"`
	PoA       *poaConfig `json:"poa"`
}

type powConfig struct {
	InitialDifficulty int   `json:"initial_difficulty"`
	RetargetInterval  int   `json:"retarget_interval"`
	TargetBlockTimeMS int64 `json:"target_block_time_ms"`
}

type poaConfig struct {
	Validators []string `json:"validators"`
}

// consensusBlock is one recent header as the screen lists it.
type consensusBlock struct {
	Height     int
	Hash       string
	Difficulty int
	Work       string // blockWork(Difficulty)
	Validator  string // empty for PoW blocks
}

// consensusNode is one node on the Consensus screen. Err is set when /consensus could not be
// read (and the other fields are zero); BlocksErr when only /headers failed.
type consensusNode struct {
	Name      string
	Err       string
	BlocksErr string
	Info      consensusBody
	Blocks    []consensusBlock // newest first
	Differs   bool             // reachable and configured unlike the first reachable node
	Heaviest  bool             // Fork, and its chain_work is the highest (ties: every such node)
}

// consensusView is what the Consensus screen renders, nodes in NODES order.
type consensusView struct {
	Nodes     []consensusNode
	Same      bool // at least one reachable node, and no reachable node Differs
	Reachable int  // nodes with an empty Err
	Fork      bool // the reachable nodes report two or more distinct tips
}

// blockWork is 2^max(difficulty, 0) in decimal, as domain.BlockWork (ADR-0012), with math/big.
func blockWork(difficulty int) string {
	return new(big.Int).Lsh(big.NewInt(1), uint(max(difficulty, 0))).String()
}

// recentFrom is the first height of the last ConsensusBlocks blocks up to height: max(height-ConsensusBlocks+1, 0).
func recentFrom(height int) int {
	return max(height-ConsensusBlocks+1, 0)
}

// sameConfig reports whether a and b have the same engine and the same parameters: every
// powConfig field, or the validators in the same order. A nil block equals only a nil block.
func sameConfig(a, b consensusBody) bool {
	if a.Engine != b.Engine {
		return false
	}

	if (a.PoW == nil) != (b.PoW == nil) || (a.PoA == nil) != (b.PoA == nil) {
		return false
	}

	if a.PoW != nil && *a.PoW != *b.PoW {
		return false
	}

	if a.PoA != nil && !slices.Equal(a.PoA.Validators, b.PoA.Validators) {
		return false
	}

	return true
}

// buildConsensusView fills Differs, Heaviest, Same and Fork. A node is reachable when Err is empty.
// Differs compares with the first reachable node in order. Heaviest compares chain_work with
// big.Int.SetString(s, 10); a value that does not parse is never heaviest.
func buildConsensusView(nodes []consensusNode) consensusView {
	out := slices.Clone(nodes)
	view := consensusView{Nodes: out}

	first := -1
	tips := map[string]bool{}

	for i := range out {
		if out[i].Err != "" {
			continue
		}

		view.Reachable++

		if first < 0 {
			first = i
		}

		tips[out[i].Info.Tip] = true
	}

	view.Fork = len(tips) >= 2

	differs := false

	var best *big.Int

	works := make([]*big.Int, len(out))

	for i := range out {
		out[i].Differs = false
		out[i].Heaviest = false

		if out[i].Err != "" {
			continue
		}

		out[i].Differs = !sameConfig(out[first].Info, out[i].Info)

		differs = differs || out[i].Differs

		w, ok := new(big.Int).SetString(out[i].Info.ChainWork, 10)
		if !ok {
			continue
		}

		works[i] = w

		if best == nil || w.Cmp(best) > 0 {
			best = w
		}
	}

	for i := range out {
		out[i].Heaviest = view.Fork && works[i] != nil && works[i].Cmp(best) == 0
	}

	view.Same = first >= 0 && !differs

	return view
}
