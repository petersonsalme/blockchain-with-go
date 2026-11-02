package server

import (
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// scheduledIssuance returns the base units the subsidy schedule (ADR-0015) issues up to and
// including height: the sum of domain.Subsidy(h) for h = 1..height. The genesis has no
// coinbase (its coins are the genesis allocations), so it adds nothing; 0 for height < 1.
// It walks one halving era per step and stops at the first era whose subsidy is 0.
func scheduledIssuance(height int) uint64 {
	var total uint64

	for start := 0; start <= height; start += domain.HalvingInterval {
		subsidy := domain.Subsidy(start)
		if subsidy == 0 {
			break
		}

		first := max(start, 1)
		last := min(start+domain.HalvingInterval-1, height)
		//nolint:gosec // last-first+1 is at most HalvingInterval (210000), always positive and fits in uint64.
		total += uint64(last-first+1) * subsidy
	}

	return total
}
