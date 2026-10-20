package chain

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
)

// ErrInvalidBlock is returned by Mine when the sealed block is nil or fails consensus verification.
var ErrInvalidBlock = errors.New("chain: invalid block")

// ErrNilRepository is returned by NewService when repo is nil.
var ErrNilRepository = errors.New("chain: nil repository")

// ErrNilConsensus is returned by NewService when consensus is nil.
var ErrNilConsensus = errors.New("chain: nil consensus")

// ErrStateDiverged is returned by Mine after a state transition failure leaves
// the repository and the UTXO set diverged. The service stops accepting writes.
var ErrStateDiverged = errors.New("chain: state diverged")

// Generator builds the candidate successor of prev holding txs. The returned
// block must contain exactly txs, in the same order.
type Generator func(ctx context.Context, c consensus.Consensus, prev *domain.Block, txs []domain.Transaction) (*domain.Block, error)

// Option customises a Service.
type Option func(*Service)

// WithGenerator replaces the default block generator (GenerateBlockWithContext).
func WithGenerator(g Generator) Option {
	return func(s *Service) { s.generate = g }
}

// WithUTXOSet replaces the default in-memory UTXO set. NewService rebuilds it
// from the chain, so it must start empty. The set belongs exclusively to the
// Service; no external caller may invoke Apply or Revert on it.
func WithUTXOSet(u storage.UTXOSet) Option {
	return func(s *Service) { s.utxos = u }
}

// WithGenesisAllocations makes the genesis block of an empty repository carry
// one transaction without inputs that creates outs.
func WithGenesisAllocations(outs []domain.TxOut) Option {
	return func(s *Service) { s.allocations = outs }
}

// Service mines blocks onto the chain held by a storage.BlockRepository.
type Service struct {
	repo        storage.BlockRepository
	consensus   consensus.Consensus
	generate    Generator
	utxos       storage.UTXOSet
	allocations []domain.TxOut
	diverged    bool
	// writeSem serializes Mine; a buffered channel lets waiters honour ctx.
	writeSem chan struct{}
}

// NewService builds a Service over repo, stores the genesis block when repo is
// empty and rebuilds the UTXO set from the chain.
func NewService(repo storage.BlockRepository, c consensus.Consensus, opts ...Option) (*Service, error) {
	if repo == nil {
		return nil, ErrNilRepository
	}
	if c == nil {
		return nil, ErrNilConsensus
	}
	s := &Service{repo: repo, consensus: c, generate: GenerateBlockWithContext, writeSem: make(chan struct{}, 1)}
	for _, opt := range opts {
		opt(s)
	}
	if s.utxos == nil {
		s.utxos = memory.NewUTXOSet()
	}

	if _, err := repo.Last(); err != nil {
		if !errors.Is(err, storage.ErrEmpty) {
			return nil, err
		}
		genesis := s.genesis()
		if _, err := domain.BlockDelta(s.utxos, genesis, true); err != nil {
			return nil, err
		}
		if err := repo.Append(genesis); err != nil {
			return nil, err
		}
	}

	blocks, err := repo.All()
	if err != nil {
		return nil, err
	}
	for i, b := range blocks {
		delta, err := domain.BlockDelta(s.utxos, b, i == 0 && b.Index == 0)
		if err != nil {
			return nil, err
		}
		if err := s.utxos.Apply(delta); err != nil {
			return nil, err
		}
	}

	return s, nil
}

// genesis builds the first block, with an allocation transaction when configured.
func (s *Service) genesis() *domain.Block {
	now := time.Now()
	block := &domain.Block{Timestamp: now, Transactions: []domain.Transaction{}}
	if len(s.allocations) > 0 {
		alloc := domain.Transaction{Outputs: s.allocations, Timestamp: now}
		alloc.ID = alloc.CalculateHash()
		block.Transactions = append(block.Transactions, alloc)
	}
	return block
}

// verifyInputs checks the signature of every input against the output it
// spends. It must run after a successful BlockDelta over txs, so every
// input resolves either in the set or in an earlier transaction of txs.
func (s *Service) verifyInputs(txs []domain.Transaction) error {
	created := map[domain.OutPoint]domain.TxOut{}
	for i := range txs {
		tx := &txs[i]
		for j, in := range tx.Inputs {
			op := domain.OutPoint{TxID: in.PrevTxID, Index: in.OutIndex}
			spent, ok := created[op]
			if !ok {
				spent, _ = s.utxos.Lookup(op)
			}
			if err := domain.VerifyInputSignature(tx, j, spent); err != nil {
				return err
			}
		}
		txid := tx.CalculateHash()
		for j, out := range tx.Outputs {
			created[domain.OutPoint{TxID: txid, Index: uint32(j)}] = out
		}
	}
	return nil
}

// Blocks returns a snapshot of the chain, genesis first.
func (s *Service) Blocks() ([]*domain.Block, error) {
	return s.repo.All()
}

// Tip returns the most recent block.
func (s *Service) Tip() (*domain.Block, error) {
	return s.repo.Last()
}

// BlockByHeight returns the block with Index h, or storage.ErrNotFound.
func (s *Service) BlockByHeight(h int) (*domain.Block, error) {
	return s.repo.ByHeight(h)
}

// BlockByHash returns the block with the given hash, or storage.ErrNotFound.
func (s *Service) BlockByHash(hash string) (*domain.Block, error) {
	return s.repo.ByHash(hash)
}

// BlocksRange returns up to limit blocks starting at height from.
func (s *Service) BlocksRange(from, limit int) ([]*domain.Block, error) {
	return s.repo.Range(from, limit)
}

// Mine seals txs into a new block and appends it to the chain.
// Every transaction must spend existing, unspent outputs it owns and cover its
// outputs, otherwise the matching domain error is returned and nothing
// changes. Calls are serialized; waiting for the turn is bounded by ctx. A
// rejected block leaves the repository and the UTXO set untouched, and Blocks
// never waits for Mine, nor do the block queries.
func (s *Service) Mine(ctx context.Context, txs []domain.Transaction) (*domain.Block, error) {
	select {
	case s.writeSem <- struct{}{}:
		defer func() { <-s.writeSem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	if s.diverged {
		return nil, ErrStateDiverged
	}

	// select picks at random when both cases are ready; do not mine for a dead ctx.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	prev, err := s.repo.Last()
	if err != nil {
		return nil, err
	}

	delta, err := domain.BlockDelta(s.utxos, &domain.Block{Index: prev.Index + 1, Transactions: txs}, false)
	if err != nil {
		return nil, err
	}
	if err := s.verifyInputs(txs); err != nil {
		return nil, err
	}

	block, err := s.generate(ctx, s.consensus, prev, txs)
	if err != nil {
		return nil, err
	}

	if block != nil {
		if err := domain.ValidateBlockRules(block, prev, time.Now()); err != nil {
			return nil, err
		}
	}

	if block == nil || s.consensus.Verify(prev, block) != nil {
		return nil, ErrInvalidBlock
	}

	if err := s.repo.Append(block); err != nil {
		return nil, err
	}

	if err := s.utxos.Apply(delta); err != nil {
		s.diverged = true
		return nil, fmt.Errorf("%w: %w", ErrStateDiverged, err)
	}

	return block, nil
}
