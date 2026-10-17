// Package consensus defines the extension point used to seal and verify blocks.
package consensus

import (
	"context"
	"errors"

	"github.com/petersonsalme/blockchain-with-go/models"
)

var (
	ErrInvalidIndex       = errors.New("consensus: invalid block index")
	ErrInvalidPrevHash    = errors.New("consensus: previous hash mismatch")
	ErrInvalidHash        = errors.New("consensus: block hash mismatch")
	ErrInvalidProofOfWork = errors.New("consensus: invalid proof of work")
)

// Consensus decides how blocks are produced and accepted.
type Consensus interface {
	// Seal fills the consensus fields (Nonce, Difficulty, Hash) of b.
	Seal(ctx context.Context, b *models.Block) error
	// Verify returns nil when b is a valid successor of prev.
	Verify(prev, b *models.Block) error
}
