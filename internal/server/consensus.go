package server

import (
	"errors"
	"math/big"
	"net/http"
	"slices"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// ConsensusInfo is what GET /consensus reports about the node's engine; cmd/bwg fills it from
// the configuration it parsed. The route reports it, it does not prove it.
type ConsensusInfo struct {
	Engine           string        // "pow" or "poa"
	Difficulty       int           // pow: initial difficulty (POW_DIFFICULTY)
	RetargetInterval int           // pow: POW_RETARGET_INTERVAL; 0 disables retargeting
	TargetBlockTime  time.Duration // pow: TARGET_BLOCK_TIME
	Validators       []string      // poa: lowercase hex public keys, in POA_VALIDATORS order
}

// consensusUnknownCode: the server was built without ConsensusInfo.
const consensusUnknownCode = "consensus_unknown"

// chainChangedCode: the chain changed while its headers were read; retry.
const chainChangedCode = "chain_changed"

// consensusResponse is the body of GET /consensus. Exactly one of PoW and PoA is set.
type consensusResponse struct {
	Engine    string     `json:"engine"`
	Height    int        `json:"height"`
	Tip       string     `json:"tip"`
	ChainWork string     `json:"chain_work"` // decimal; 2^Difficulty sums overflow int64
	PoW       *powParams `json:"pow,omitempty"`
	PoA       *poaParams `json:"poa,omitempty"`
}

type powParams struct {
	InitialDifficulty int   `json:"initial_difficulty"`
	RetargetInterval  int   `json:"retarget_interval"`
	TargetBlockTimeMS int64 `json:"target_block_time_ms"`
}

type poaParams struct {
	Validators []string `json:"validators"`
}

var errChainChanged = errors.New("server: chain changed during the read")

func (s *Server) chainWork(tip *domain.Block) (*big.Int, error) {
	work := new(big.Int)

	var (
		prev  string
		count int
	)

	for from := 0; from <= tip.Index; from += maxHeaderLimit {
		limit := min(maxHeaderLimit, tip.Index-from+1)

		blocks, err := s.chain.BlocksRange(from, limit)
		if err != nil {
			return nil, err
		}

		for _, b := range blocks {
			if b == nil {
				return nil, errChainChanged
			}

			if b.Index > 0 && b.PrevHash != prev {
				return nil, errChainChanged
			}

			prev = b.Hash
			work.Add(work, domain.BlockWork(b))

			count++
		}
	}

	if count != tip.Index+1 || prev != tip.Hash {
		return nil, errChainChanged
	}

	return work, nil
}

func (s *Server) handleConsensus(w http.ResponseWriter, _ *http.Request) {
	if s.consensus == nil {
		respondWithJSON(w, http.StatusNotFound, errorResponse{Error: "consensus info not configured", Code: consensusUnknownCode})
		return
	}

	tip, err := s.chain.Tip()
	if err != nil {
		respondWithJSON(w, http.StatusServiceUnavailable, errorResponse{Error: err.Error(), Code: unhealthyCode})
		return
	}

	work, err := s.chainWork(tip)
	if err != nil {
		if errors.Is(err, errChainChanged) {
			respondWithJSON(w, http.StatusServiceUnavailable, errorResponse{Error: err.Error(), Code: chainChangedCode})
			return
		}

		respondWithJSON(w, http.StatusServiceUnavailable, errorResponse{Error: err.Error(), Code: unhealthyCode})

		return
	}

	resp := consensusResponse{
		Engine:    s.consensus.Engine,
		Height:    tip.Index,
		Tip:       tip.Hash,
		ChainWork: work.String(),
	}

	switch s.consensus.Engine {
	case "pow":
		resp.PoW = &powParams{
			InitialDifficulty: s.consensus.Difficulty,
			RetargetInterval:  s.consensus.RetargetInterval,
			TargetBlockTimeMS: s.consensus.TargetBlockTime.Milliseconds(),
		}
	case "poa":
		resp.PoA = &poaParams{
			Validators: slices.Clone(s.consensus.Validators),
		}
	}

	respondWithJSON(w, http.StatusOK, resp)
}
