package server

import (
	"net/http"
	"strconv"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// nextHalvingHeight returns the first height after height at which the subsidy schedule
// (ADR-0015) halves: the start of the next era. At a halving height the halving already
// happened, so the result is a full interval away.
func nextHalvingHeight(height int) int {
	return (height/domain.HalvingInterval + 1) * domain.HalvingInterval
}

// halvingResponse is the body of GET /halving.
type halvingResponse struct {
	Height             int    `json:"height"`
	CurrentSubsidy     string `json:"current_subsidy"` // decimal; domain.Subsidy is uint64
	NextHalvingHeight  int    `json:"next_halving_height"`
	BlocksUntilHalving int    `json:"blocks_until_halving"`
}

func (s *Server) handleHalving(w http.ResponseWriter, _ *http.Request) {
	tip, err := s.chain.Tip()
	if err != nil {
		respondWithJSON(w, http.StatusServiceUnavailable, errorResponse{Error: err.Error(), Code: unhealthyCode})
		return
	}

	next := nextHalvingHeight(tip.Index)
	respondWithJSON(w, http.StatusOK, halvingResponse{
		Height:             tip.Index,
		CurrentSubsidy:     strconv.FormatUint(domain.Subsidy(tip.Index), 10),
		NextHalvingHeight:  next,
		BlocksUntilHalving: next - tip.Index,
	})
}
