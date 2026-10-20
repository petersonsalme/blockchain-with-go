// Package consensus defines the extension point used to seal and verify blocks.
package consensus

import (
	"context"
	"errors"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

var (
	ErrInvalidIndex       = errors.New("consensus: invalid block index")
	ErrInvalidPrevHash    = errors.New("consensus: previous hash mismatch")
	ErrInvalidHash        = errors.New("consensus: block hash mismatch")
	ErrInvalidProofOfWork = errors.New("consensus: invalid proof of work")
	ErrInvalidSeal        = errors.New("consensus: invalid seal")
	ErrUnknownValidator   = errors.New("consensus: unknown validator")
	ErrInvalidSignature   = errors.New("consensus: invalid signature")
)

// Consensus decides how blocks are produced and accepted.
type Consensus interface {
	// Seal fills the consensus fields of b.
	Seal(ctx context.Context, b *domain.Block) error
	// Verify returns nil when b is a valid successor of prev.
	Verify(prev, b *domain.Block) error
}
