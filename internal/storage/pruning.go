package storage

import "errors"

// ErrInvalidPruning is returned when a Pruning is negative, asks to keep
// blocks without a snapshot interval, or contradicts a pruned database.
var ErrInvalidPruning = errors.New("storage: invalid pruning configuration")

// ErrReorgTooDeep is returned by Reorg, ReplaceFrom and ForkState when the
// height lies below PrunedBelow: the bodies and undo data it needs are gone.
var ErrReorgTooDeep = errors.New("storage: height is below the pruned history")

// ErrPruned is returned by readers that need the body of a pruned block.
var ErrPruned = errors.New("storage: block body is pruned")

// Pruning configures the periodic UTXO snapshot and the pruning of old bodies.
// The zero value keeps the whole history and stores no snapshot.
type Pruning struct {
	// SnapshotInterval (N) is the distance between snapshots; 0 disables them.
	SnapshotInterval int
	// KeepBlocks (K) is how far the snapshot lags the tip. With K > 0 the
	// bodies below a snapshot are pruned; with K == 0 snapshots are stored but
	// nothing is pruned.
	KeepBlocks int
}

// Validate returns ErrInvalidPruning for a negative value, or for KeepBlocks > 0 without SnapshotInterval.
func (p Pruning) Validate() error {
	if p.SnapshotInterval < 0 || p.KeepBlocks < 0 || (p.KeepBlocks > 0 && p.SnapshotInterval == 0) {
		return ErrInvalidPruning
	}

	return nil
}

// SnapshotHeight returns the height S of the snapshot that the Commit of
// height t stores, and whether there is one: N > 0, t-K >= 1 and (t-K) mod N == 0.
func (p Pruning) SnapshotHeight(t int) (int, bool) {
	s := t - p.KeepBlocks
	if p.SnapshotInterval <= 0 || s < 1 || s%p.SnapshotInterval != 0 {
		return 0, false
	}

	return s, true
}
