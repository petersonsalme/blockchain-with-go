package chain

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/mempool"
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

// ErrTxNotFound is returned by TxProof when no block holds the transaction.
var ErrTxNotFound = errors.New("chain: transaction not found")

// Generator builds the candidate successor of the last block of history,
// ordered oldest to newest, holding txs. The returned
// block must contain exactly txs, in the same order.
type Generator func(ctx context.Context, c consensus.Consensus, history []*domain.Block, txs []domain.Transaction) (*domain.Block, error)

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

// WithMempool replaces the default pool (mempool.New(mempool.DefaultMaxTxs)).
// The pool belongs exclusively to the Service.
func WithMempool(p *mempool.Pool) Option {
	return func(s *Service) { s.pool = p }
}

// Service mines blocks onto the chain held by a storage.BlockRepository.
type Service struct {
	repo        storage.BlockRepository
	consensus   consensus.Consensus
	generate    Generator
	utxos       storage.UTXOSet
	allocations []domain.TxOut
	pool        *mempool.Pool
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
	if s.pool == nil {
		s.pool = mempool.New(mempool.DefaultMaxTxs)
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
	block.MerkleRoot = domain.MerkleRoot(block.Transactions)
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

// UTXO represents an unspent transaction output.
type UTXO struct {
	TxID   string `json:"txid"`
	Index  uint32 `json:"index"`
	Amount uint64 `json:"amount"`
}

// Transaction returns the transaction with the given ID, the hash and height of
// the block that contains it, or storage.ErrNotFound.
func (s *Service) Transaction(id string) (*domain.Transaction, string, int, error) {
	blocks, err := s.repo.All()
	if err != nil {
		return nil, "", 0, err
	}
	for _, b := range blocks {
		for i := range b.Transactions {
			if b.Transactions[i].ID == id {
				tx := b.Transactions[i]
				return &tx, b.Hash, b.Index, nil
			}
		}
	}
	return nil, "", 0, storage.ErrNotFound
}

// InclusionProof proves that a transaction belongs to the block at BlockIndex:
// folding Steps over LeafHash yields MerkleRoot, the root in that block's header.
type InclusionProof struct {
	BlockIndex int
	BlockHash  string
	MerkleRoot string
	LeafIndex  int
	LeafHash   string
	Steps      []domain.MerkleStep
}

// TxProof returns the inclusion proof of the first transaction with the given
// ID, scanning from genesis, or ErrTxNotFound. It reads a snapshot and never
// waits for Mine.
func (s *Service) TxProof(id string) (*InclusionProof, error) {
	blocks, err := s.repo.All()
	if err != nil {
		return nil, err
	}
	for _, b := range blocks {
		for i := range b.Transactions {
			if b.Transactions[i].ID != id {
				continue
			}
			proof, err := domain.BuildMerkleProof(b.Transactions, i)
			if err != nil {
				return nil, err
			}
			return &InclusionProof{
				BlockIndex: b.Index,
				BlockHash:  b.Hash,
				MerkleRoot: b.MerkleRoot,
				LeafIndex:  i,
				LeafHash:   domain.MerkleLeafHash(&b.Transactions[i]),
				Steps:      proof.Steps,
			}, nil
		}
	}
	return nil, ErrTxNotFound
}

// Balance returns the unspent outputs owned by addr, sorted by (TxID, Index),
// plus their sum, from a single snapshot of the UTXO set.
func (s *Service) Balance(addr string) (uint64, []UTXO) {
	snap := s.utxos.Snapshot()
	var (
		balance uint64
		utxos   = make([]UTXO, 0)
	)
	for op, out := range snap {
		if out.Address == addr {
			balance += out.Amount
			utxos = append(utxos, UTXO{
				TxID:   op.TxID,
				Index:  op.Index,
				Amount: out.Amount,
			})
		}
	}
	slices.SortFunc(utxos, func(a, b UTXO) int {
		if c := strings.Compare(a.TxID, b.TxID); c != 0 {
			return c
		}
		return cmp.Compare(a.Index, b.Index)
	})
	return balance, utxos
}

// lock takes the write turn, bounded by ctx. The returned func releases it.
func (s *Service) lock(ctx context.Context) (func(), error) {
	select {
	case s.writeSem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	release := func() { <-s.writeSem }

	if s.diverged {
		release()
		return nil, ErrStateDiverged
	}

	// select picks at random when both cases are ready; do not mine for a dead ctx.
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// Mine seals txs into a new block and appends it to the chain.
// Every transaction must spend existing, unspent outputs it owns and cover its
// outputs, otherwise the matching domain error is returned and nothing
// changes. Calls are serialized; waiting for the turn is bounded by ctx. A
// rejected block leaves the repository, the UTXO set and the pool untouched,
// and Blocks never waits for Mine, nor do the block queries. An accepted block
// removes from the pool the transactions it holds and those it conflicts with.
func (s *Service) Mine(ctx context.Context, txs []domain.Transaction) (*domain.Block, error) {
	release, err := s.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	return s.mineLocked(ctx, txs)
}

// Submit admits tx into the pool after the checks Mine applies to a block of
// that one transaction, against the confirmed UTXO set. An input created by
// another pool transaction is rejected with domain.ErrInputMissingOrSpent. It
// never waits for Mine: a block mined meanwhile can leave tx stale, and
// MinePending discards it.
func (s *Service) Submit(tx domain.Transaction) error {
	txs := []domain.Transaction{tx}
	delta, err := domain.BlockDelta(s.utxos, &domain.Block{Transactions: txs}, false)
	if err != nil {
		return err
	}
	if err := s.verifyInputs(txs); err != nil {
		return err
	}

	// BlockDelta guarantees in >= out and no overflow for a transaction with inputs.
	var in, out uint64
	for _, o := range delta.Spent {
		in += o.Amount
	}
	for _, o := range tx.Outputs {
		out += o.Amount
	}
	return s.pool.Add(tx, in-out)
}

// Pending returns the pool in selection order. It never waits for Mine.
func (s *Service) Pending() []domain.Transaction {
	return s.pool.Select(math.MaxInt)
}

// MinePending mines a block with the pool transactions of highest fee per byte
// that fit domain.MaxBlockBytes, in selection order, and an empty block when
// the pool is empty. Transactions that went stale since Submit are dropped
// from the pool instead of failing the block. Locking and failure semantics
// are those of Mine.
func (s *Service) MinePending(ctx context.Context) (*domain.Block, error) {
	release, err := s.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	txs, err := s.fit()
	if err != nil {
		return nil, err
	}
	return s.mineLocked(ctx, s.dropStale(txs))
}

// fit picks the pool transactions whose sealed block fits domain.MaxBlockBytes:
// it shrinks the budget handed to the pool by the excess until it does.
func (s *Service) fit() ([]domain.Transaction, error) {
	limit := domain.MaxBlockBytes()
	budget := limit
	for {
		txs := s.pool.Select(budget)
		size, err := sealedSize(txs)
		if err != nil {
			return nil, err
		}
		// An empty block that is already too large is left to the generator to reject.
		if size <= limit || len(txs) == 0 {
			return txs, nil
		}
		budget -= max(size-limit, 1)
	}
}

// dropStale removes from the pool, and from txs, the transactions Mine would no
// longer accept: a Submit that validated before an earlier block can add them
// after it. The caller holds writeSem, so the UTXO set cannot change underneath.
func (s *Service) dropStale(txs []domain.Transaction) []domain.Transaction {
	valid := make([]domain.Transaction, 0, len(txs))
	var stale []string
	for i := range txs {
		one := txs[i : i+1]
		if _, err := domain.BlockDelta(s.utxos, &domain.Block{Transactions: one}, false); err != nil || s.verifyInputs(one) != nil {
			stale = append(stale, txs[i].ID)
			continue
		}
		valid = append(valid, txs[i])
	}
	s.pool.Remove(stale...)
	return valid
}

// sealedSize is the serialized size txs can reach in a sealed block: every
// header field of the largest width consensus can give it, so the real block
// is never bigger. The block generator only reserves a hash.
func sealedSize(txs []domain.Transaction) (int, error) {
	hash := strings.Repeat("0", 64)
	b := domain.Block{
		Index:        math.MaxInt,
		Timestamp:    time.Date(2000, 1, 1, 0, 0, 0, 123456789, time.FixedZone("", 3600)),
		Transactions: txs,
		MerkleRoot:   hash,
		Hash:         hash,
		PrevHash:     hash,
		Nonce:        math.MaxInt,
		Difficulty:   math.MaxInt,
		Validator:    hash,
		Signature:    strings.Repeat("0", 128),
	}
	return b.Size()
}

// mineLocked seals txs and appends the block. The caller holds writeSem.
func (s *Service) mineLocked(ctx context.Context, txs []domain.Transaction) (*domain.Block, error) {
	history, err := s.repo.All()
	if err != nil {
		return nil, err
	}
	if len(history) == 0 {
		return nil, storage.ErrEmpty
	}
	prev := history[len(history)-1]

	delta, err := domain.BlockDelta(s.utxos, &domain.Block{Transactions: txs}, false)
	if err != nil {
		return nil, err
	}
	if err := s.verifyInputs(txs); err != nil {
		return nil, err
	}

	block, err := s.generate(ctx, s.consensus, history, txs)
	if err != nil {
		return nil, err
	}

	if block != nil {
		if err := domain.ValidateBlockRules(block, prev, time.Now()); err != nil {
			return nil, err
		}
	}

	if block == nil || s.consensus.Verify(history, block) != nil {
		return nil, ErrInvalidBlock
	}

	if err := s.repo.Append(block); err != nil {
		return nil, err
	}

	if err := s.utxos.Apply(delta); err != nil {
		s.diverged = true
		return nil, fmt.Errorf("%w: %w", ErrStateDiverged, err)
	}

	s.pool.Prune(block, s.utxos)
	return block, nil
}
