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
	// writeSem serializes Mine; a buffered channel lets waiters honour ctx.
	writeSem chan struct{}
	// nonces holds the next expected nonce per sender; only touched under writeSem
	// (and in NewService, before the Service is shared).
	nonces map[string]uint64
}

// NewService builds a Service over repo and stores the genesis block when repo is empty.
func NewService(repo storage.BlockRepository, c consensus.Consensus, opts ...Option) (*Service, error) {
	s := &Service{repo: repo, consensus: c, generate: GenerateBlockWithContext, writeSem: make(chan struct{}, 1), nonces: map[string]uint64{}}
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

	blocks, err := repo.All()
	if err != nil {
		return nil, err
	}
	for _, b := range blocks {
		for _, tx := range b.Transactions {
			s.nonces[tx.From]++
		}
	}

	return s, nil
}

// checkTransactions verifies every signature and, per sender, that nonces are
// consecutive from the sender's next expected nonce. It returns the nonces to
// commit once the block is appended and never modifies s.nonces.
func (s *Service) checkTransactions(txs []domain.Transaction) (map[string]uint64, error) {
	next := map[string]uint64{}
	seen := make(map[string]struct{}, len(txs))
	for i := range txs {
		tx := &txs[i]
		if err := tx.VerifySignature(); err != nil {
			return nil, err
		}
		hash := tx.CalculateHash()
		if _, dup := seen[hash]; dup {
			return nil, domain.ErrDuplicateTransaction
		}
		seen[hash] = struct{}{}

		want, ok := next[tx.From]
		if !ok {
			want = s.nonces[tx.From]
		}
		switch {
		case tx.Nonce < want:
			return nil, domain.ErrNonceReused
		case tx.Nonce > want:
			return nil, domain.ErrNonceGap
		}
		next[tx.From] = want + 1
	}
	return next, nil
}

// Blocks returns a snapshot of the chain, genesis first.
func (s *Service) Blocks() ([]*domain.Block, error) {
	return s.repo.All()
}

// Mine seals txs into a new block and appends it to the chain.
// Transactions must be signed by their From address and carry consecutive
// nonces per sender, otherwise the matching domain error is returned and
// nothing changes. Calls are serialized; waiting for the turn is bounded by ctx. A rejected
// block leaves the repository untouched, and Blocks never waits for Mine.
func (s *Service) Mine(ctx context.Context, txs []domain.Transaction) (*domain.Block, error) {
	select {
	case s.writeSem <- struct{}{}:
		defer func() { <-s.writeSem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// select picks at random when both cases are ready; do not mine for a dead ctx.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	pending, err := s.checkTransactions(txs)
	if err != nil {
		return nil, err
	}

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

	for from, n := range pending {
		s.nonces[from] = n
	}

	return block, nil
}
