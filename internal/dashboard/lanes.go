package dashboard

// LaneBlocks is how many heights the chain lanes of the Network screen show: the poller reads
// this many headers per node and tick, ending at the node's height.
const LaneBlocks = 8

// BlockRef is one block of a node's recent chain, from its last /headers read.
type BlockRef struct {
	Height int
	Hash   string
}
