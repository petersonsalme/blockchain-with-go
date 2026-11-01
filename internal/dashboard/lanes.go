package dashboard

// LaneBlocks is how many heights the chain lanes of the Network screen show: the poller reads
// this many headers per node and tick, ending at the node's height.
const LaneBlocks = 8

// BlockRef is one block of a node's recent chain, from its last /headers read.
type BlockRef struct {
	Height int
	Hash   string
}

// Cell is one height of a lane.
type Cell struct {
	Height int
	Hash   string // empty when the node's last read has no block at this height
	Kind   string // "", "tip", "behind", "fork", "missing" or "stale": the blk-- class suffix
}

// Lane is one node's recent chain on the window all lanes share.
type Lane struct {
	Name, Level, Status string
	Cells               []Cell
}

// lanes lays every card, in NODES order, on the heights lo..top, where top is the highest
// reachable node (the highest seen node when none is reachable) and lo = max(top-LaneBlocks+1, 0).
// Blocks are compared with the reference chain: the blocks of the first reachable card at top
// whose tip is majority. It returns no lane when no card has been seen.
func lanes(cards []Card, state State, majority string) (lo, top int, out []Lane) {
	seen, reachable := false, false

	for _, c := range cards {
		switch {
		case c.Reachable && (!reachable || c.Height > top):
			top, reachable, seen = c.Height, true, true
		case c.Seen && !reachable && (!seen || c.Height > top):
			top, seen = c.Height, true
		}
	}

	if !seen {
		return 0, 0, nil
	}

	lo = max(top-LaneBlocks+1, 0)
	ref := map[int]string{}

	for _, c := range cards {
		if c.Reachable && c.Height == top && c.Tip == majority {
			for _, b := range c.Blocks {
				ref[b.Height] = b.Hash
			}

			break
		}
	}

	for _, c := range cards {
		own := map[int]string{}
		for _, b := range c.Blocks {
			own[b.Height] = b.Hash
		}

		lane := Lane{Name: c.Name, Level: c.Level, Status: c.Status}
		for h := lo; h <= top; h++ {
			lane.Cells = append(lane.Cells, Cell{Height: h, Hash: own[h], Kind: cellKind(c, h, own[h], ref[h], top, state)})
		}

		out = append(out, lane)
	}

	return lo, top, out
}

// cellKind marks one block. A block that differs from the reference is a fork, except a node's
// own tip at top before the Fork verdict: two nodes that just mined at once are a race, not a fork.
func cellKind(c Card, h int, hash, ref string, top int, state State) string {
	atTop := h == c.Height && c.Height == top

	switch {
	case hash == "":
		return "missing"
	case !c.Reachable:
		return "stale"
	case ref != "" && ref != hash && (!atTop || state == StateFork):
		return "fork"
	case atTop:
		return "tip"
	case h == c.Height:
		return "behind"
	}

	return ""
}
