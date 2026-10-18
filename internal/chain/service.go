package chain

import (
	"context"
	"errors"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
)

// ErrInvalidBlock is returned by Mine when the sealed block is nil or fails consensus verification.
var ErrInvalidBlock = errors.New("chain: invalid block")

// Generator builds the candidate successor of prev holding txs.
type Generator func(ctx context.Context, c consensus.Consensus, prev *domain.Block, txs []domain.Transaction) (*domain.Block, error)

// Option customises a Service.
type Option func(*Service)

// WithGenerator replaces the default block generator (GenerateBlockWithContext).
func WithGenerator(g Generator) Option {
	return func(s *Service) { s.generate = g }
}

// Service mines blocks onto the chain held by a storage.BlockRepository.
type Service struct {
	repo      storage.BlockRepository
	consensus consensus.Consensus
	generate  Generator
}

// NewService builds a Service over repo and stores the genesis block when repo is empty.
func NewService(repo storage.BlockRepository, c consensus.Consensus, opts ...Option) (*Service, error) {
	s := &Service{repo: repo, consensus: c, generate: GenerateBlockWithContext}
	for _, opt := range opts {
		opt(s)
	}

	if _, err := repo.Last(); err != nil {
		if !errors.Is(err, storage.ErrEmpty) {
			return nil, err
		}
		genesis := &domain.Block{Timestamp: time.Now(), Transactions: []domain.Transaction{}}
		if err := repo.Append(genesis); err != nil {
			return nil, err
		}
	}

	return s, nil
}

// Blocks returns a snapshot of the chain, genesis first.
func (s *Service) Blocks() ([]*domain.Block, error) {
	return s.repo.All()
}

// Mine seals txs into a new block and appends it to the chain.
// A rejected block leaves the repository untouched.
func (s *Service) Mine(ctx context.Context, txs []domain.Transaction) (*domain.Block, error) {
	prev, err := s.repo.Last()
	if err != nil {
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

	return block, nil
}
