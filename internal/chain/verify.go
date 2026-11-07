package chain

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
	"github.com/petersonsalme/blockchain-with-go/internal/vm"
)

// Verify replays the stored chain under the current block rules (ADR-0035) on
// scratch state and writes nothing. Heights below PrunedBelow are headers and
// get the header rules only. The first failing block stops the replay; the
// error wraps ErrInvalidStoredChain and the rule's own error and names the
// height. Calls are serialized with Mine; waiting for the turn is bounded by ctx.
func (s *Service) Verify(ctx context.Context) error {
	release, err := s.lock(ctx)
	if err != nil {
		return err
	}
	defer release()

	all, err := s.repo.All()
	if err != nil {
		return err
	}

	if len(all) == 0 {
		return storage.ErrEmpty
	}

	pruned, err := s.repo.PrunedBelow()
	if err != nil {
		return err
	}

	utxos := domain.UTXOSet{}

	if pruned > 0 {
		utxos, err = s.snapshotBase(all, pruned)
		if err != nil {
			return storedBlockError(pruned-1, err)
		}
	}

	now := time.Now()
	contracts := vm.State{}

	for h, b := range all {
		err := ctx.Err()
		if err != nil {
			return err
		}

		if b == nil {
			return storedBlockError(h, ErrInvalidBlock)
		}

		err = s.verifyStored(all[:h], b, h >= pruned, now, utxos, contracts)
		if err != nil {
			return storedBlockError(h, err)
		}
	}

	return nil
}

func storedBlockError(h int, err error) error {
	return fmt.Errorf("chain: stored block %d: %w: %w", h, ErrInvalidStoredChain, err)
}

// snapshotBase returns a copy of the stored snapshot set, the UTXO set the
// replay of a pruned repository starts from. The snapshot must describe the
// block just below PrunedBelow.
func (s *Service) snapshotBase(all []*domain.Block, pruned int) (domain.UTXOSet, error) {
	snap, err := s.repo.Snapshot()
	if err != nil {
		return nil, err
	}

	last := all[pruned-1]
	if last == nil || snap.Height != pruned-1 || snap.BlockHash != last.Hash {
		return nil, fmt.Errorf("snapshot at height %d (block %q) does not match the stored block %q", snap.Height, snap.BlockHash, last.Hash)
	}

	return maps.Clone(snap.UTXOs), nil
}

// verifyStored checks b, the block at the height len(history), against
// history. A block with a body also moves utxos and contracts forward.
func (s *Service) verifyStored(history []*domain.Block, b *domain.Block, body bool, now time.Time, utxos domain.UTXOSet, contracts vm.State) error {
	if len(history) == 0 {
		return verifyGenesis(b, body, utxos)
	}

	err := s.consensus.Verify(history, b)
	if err != nil {
		return err
	}

	if !body {
		return domain.ValidateTimestamp(b, history, now)
	}

	err = domain.ValidateBlockRules(b, history, now)
	if err != nil {
		return err
	}

	delta, err := domain.BlockDelta(utxos, b, false)
	if err != nil {
		return err
	}

	err = verifyInputs(utxos, b.Transactions)
	if err != nil {
		return err
	}

	contractDelta, err := vm.BlockDelta(contracts, b)
	if err != nil {
		return err
	}

	utxos.Apply(delta)
	contracts.Apply(contractDelta)

	return nil
}

var errBadGenesis = errors.New("genesis must have index 0, no previous hash and a hash of its own content or none")

// verifyGenesis checks the header fields of the genesis and, when its body is
// stored, its Merkle root and its allocations.
func verifyGenesis(b *domain.Block, body bool, utxos domain.UTXOSet) error {
	if b.Index != 0 || b.PrevHash != "" || (b.Hash != "" && b.Hash != b.CalculateHash()) {
		return errBadGenesis
	}

	if !body {
		return nil
	}

	err := domain.ValidateMerkleRoot(b)
	if err != nil {
		return err
	}

	delta, err := domain.BlockDelta(utxos, b, true)
	if err != nil {
		return err
	}

	utxos.Apply(delta)

	return nil
}
