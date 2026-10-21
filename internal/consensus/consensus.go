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
	ErrInvalidDifficulty  = errors.New("consensus: unexpected difficulty")
	ErrInvalidProofOfWork = errors.New("consensus: invalid proof of work")
	ErrInvalidSeal        = errors.New("consensus: invalid seal")
	ErrUnknownValidator   = errors.New("consensus: unknown validator")
	ErrInvalidSignature   = errors.New("consensus: invalid signature")
)

// Consensus decides how blocks are produced and accepted.
type Consensus interface {
	// Seal fills the consensus fields of b, a candidate successor of the last
	// block of history. history is ordered oldest to newest.
	Seal(ctx context.Context, history []*domain.Block, b *domain.Block) error
	// Verify returns nil when b is a valid successor of the last block of
	// history. history is ordered oldest to newest and must not be empty.
	Verify(history []*domain.Block, b *domain.Block) error
}
