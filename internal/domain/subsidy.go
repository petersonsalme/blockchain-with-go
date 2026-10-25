package domain

// Consensus constants of the block reward (ADR-0015). Nodes that disagree on
// them fork, so they are not configuration.
const (
	// InitialSubsidy is the reward of the first halving era, in base units.
	InitialSubsidy uint64 = 5_000_000_000
	// HalvingInterval is the number of blocks between two halvings.
	HalvingInterval = 210_000
)

// Subsidy returns the block reward at height: InitialSubsidy halved once per
// HalvingInterval blocks. It is 0 for a negative height and once the halvings
// shift every bit out.
func Subsidy(height int) uint64 {
	if height < 0 {
		return 0
	}
	// A shift of 64 or more yields 0 in Go, so no explicit guard is needed.
	return InitialSubsidy >> uint(height/HalvingInterval)
}
