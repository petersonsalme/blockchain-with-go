package pow_test

import (
	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/consensustest"
	powpkg "github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
)

var _ = consensustest.Run(func() consensus.Consensus { return powpkg.New(1) })
