package storage

import (
	"errors"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// ErrUTXOConflict is returned by Apply and Revert when the delta does not match the current set.
var ErrUTXOConflict = errors.New("storage: delta does not match the UTXO set")

// UTXOSet holds the unspent transaction outputs of the chain.
// Implementations must be safe for concurrent use.
type UTXOSet interface {
	domain.UTXOView
	// Apply removes d.Spent and adds d.Created. Every spent outpoint must be
	// present with the same output and every created outpoint absent, or
	// Apply returns ErrUTXOConflict and changes nothing.
	Apply(d domain.UTXODelta) error
	// Revert undoes Apply with the mirrored checks and the same atomicity.
	Revert(d domain.UTXODelta) error
	// Snapshot returns a copy of the set.
	Snapshot() domain.UTXOSet
}
