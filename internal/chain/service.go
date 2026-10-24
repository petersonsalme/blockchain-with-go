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

// ErrInsufficientWork is returned by ReplaceChain when the candidate does not
// carry strictly more accumulated work than the local chain after the fork.
var ErrInsufficientWork = errors.New("chain: candidate has no more work than the local chain")

// ErrGenesisMismatch is returned by ReplaceChain when the candidate does not
// start at the local genesis block.
var ErrGenesisMismatch = errors.New("chain: candidate genesis differs from the local genesis")

// Reorg describes a chain switch made by ReplaceChain.
type Reorg struct {
	// ForkHeight is the Index of the first block that changed.
	ForkHeight int
	// Disconnected are the local blocks removed, oldest first.
	Disconnected []*domain.Block
	// Connected are the candidate blocks added, oldest first.
	Connected []*domain.Block
	// Orphaned are the transactions of Disconnected whose hash is not in
	// Connected, in chain order. They are not revalidated.
	Orphaned []domain.Transaction
}

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

// Service mines blocks onto the chain held by a storage.IndexedRepository,
// which also holds the UTXO set and the transaction indexes.
type Service struct {
	repo        storage.IndexedRepository
	consensus   consensus.Consensus
	generate    Generator
	allocations []domain.TxOut
	pool        *mempool.Pool
	diverged    bool
	// writeSem serializes Mine; a buffered channel lets waiters honour ctx.
	writeSem chan struct{}
}

// NewService builds a Service over repo, stores the genesis block when repo is
// empty. It rebuilds the persisted UTXO set and indexes only when the
// repository reports them out of date.
func NewService(repo storage.IndexedRepository, c consensus.Consensus, opts ...Option) (*Service, error) {
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
	if s.pool == nil {
		s.pool = mempool.New(mempool.DefaultMaxTxs)
	}

	if _, err := repo.Last(); err != nil {
		if !errors.Is(err, storage.ErrEmpty) {
			return nil, err
		}
		genesis := s.genesis()
		delta, err := domain.BlockDelta(repo, genesis, true)
		if err != nil {
			return nil, err
		}
		if err := repo.Commit(genesis, delta); err != nil {
			return nil, err
		}
	}

	current, err := repo.IndexCurrent()
	if err != nil {
		return nil, err
	}
	if !current {
		if err := repo.Reindex(); err != nil {
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
// spends. It must run after a successful BlockDelta over txs and view, so every
// input resolves either in view or in an earlier transaction of txs.
func verifyInputs(view domain.UTXOView, txs []domain.Transaction) error {
	created := map[domain.OutPoint]domain.TxOut{}
	for i := range txs {
		tx := &txs[i]
		for j, in := range tx.Inputs {
			op := domain.OutPoint{TxID: in.PrevTxID, Index: in.OutIndex}
			spent, ok := created[op]
			if !ok {
				spent, _ = view.Lookup(op)
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
	b, i, err := s.locate(id)
	if err != nil {
		return nil, "", 0, err
	}
	tx := b.Transactions[i]
	return &tx, b.Hash, b.Index, nil
}

// locate finds the block holding the transaction id and its position there,
// through the persisted index. It returns storage.ErrNotFound when none does.
func (s *Service) locate(id string) (*domain.Block, int, error) {
	h, err := s.repo.TxHeight(id)
	if err != nil {
		return nil, 0, err
	}
	b, err := s.repo.ByHeight(h)
	if err != nil {
		return nil, 0, err
	}
	for i := range b.Transactions {
		if b.Transactions[i].ID == id {
			return b, i, nil
		}
	}
	return nil, 0, storage.ErrNotFound
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

// TxProof returns the inclusion proof of the transaction with the given ID,
// or ErrTxNotFound. It reads through the transaction index and never waits for
// Mine.
func (s *Service) TxProof(id string) (*InclusionProof, error) {
	b, i, err := s.locate(id)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, ErrTxNotFound
	}
	if err != nil {
		return nil, err
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

// TransactionsByAddress returns, in chain order, the transactions that create
// or spend an output of addr. A reorg drops the transactions of the removed
// blocks and adds those of the new ones. It never waits for Mine.
func (s *Service) TransactionsByAddress(addr string) ([]domain.Transaction, error) {
	var txs []domain.Transaction
	err := s.consistent(func() error {
		var err error
		txs, err = s.addressTxs(addr)
		return err
	})
	if err != nil {
		return nil, err
	}
	return txs, nil
}

// addressTxs reads the transactions of addr from the index and the blocks.
func (s *Service) addressTxs(addr string) ([]domain.Transaction, error) {
	ids, err := s.repo.TxsByAddress(addr)
	if err != nil {
		return nil, err
	}
	txs := make([]domain.Transaction, 0, len(ids))
	blocks := map[int]*domain.Block{}
	for _, id := range ids {
		h, err := s.repo.TxHeight(id)
		if err != nil {
			return nil, err
		}
		b, ok := blocks[h]
		if !ok {
			if b, err = s.repo.ByHeight(h); err != nil {
				return nil, err
			}
			blocks[h] = b
		}
		i := slices.IndexFunc(b.Transactions, func(tx domain.Transaction) bool { return tx.ID == id })
		if i < 0 {
			return nil, storage.ErrNotFound
		}
		txs = append(txs, b.Transactions[i])
	}
	return txs, nil
}

// readAttempts bounds how many times consistent repeats a read that raced a write.
const readAttempts = 5

// ErrTipMoving is returned by the index queries when blocks kept landing while
// they read, so no attempt saw a single chain.
var ErrTipMoving = errors.New("chain: tip kept moving while reading")

// consistent runs read until the chain tip is the same before and after it, so
// a read that spans several repository calls never mixes two chains. An error
// from a read that raced a write is retried too; one that does not is returned.
// After readAttempts attempts it gives up with ErrTipMoving.
func (s *Service) consistent(read func() error) error {
	for range readAttempts {
		before, err := s.repo.Last()
		if err != nil {
			return err
		}
		readErr := read()
		after, err := s.repo.Last()
		if err != nil {
			return err
		}
		if before.Hash == after.Hash {
			return readErr
		}
	}
	return ErrTipMoving
}

// Balance returns the unspent outputs owned by addr, sorted by (TxID, Index),
// plus their sum. Every unspent output of addr was created by a transaction in
// the address index, so it reads those transactions and keeps their outputs
// that the persisted UTXO set still holds. The result is empty when the
// repository cannot be read.
func (s *Service) Balance(addr string) (uint64, []UTXO) {
	var (
		balance uint64
		utxos   []UTXO
	)
	err := s.consistent(func() error {
		txs, err := s.addressTxs(addr)
		balance, utxos = 0, make([]UTXO, 0)
		if err != nil {
			return err
		}
		for i := range txs {
			txid := txs[i].CalculateHash()
			for j := range txs[i].Outputs {
				op := domain.OutPoint{TxID: txid, Index: uint32(j)}
				if got, ok := s.repo.Lookup(op); ok && got.Address == addr {
					balance += got.Amount
					utxos = append(utxos, UTXO{TxID: op.TxID, Index: op.Index, Amount: got.Amount})
				}
			}
		}
		return nil
	})
	if err != nil {
		return 0, make([]UTXO, 0)
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
	delta, err := domain.BlockDelta(s.repo, &domain.Block{Transactions: txs}, false)
	if err != nil {
		return err
	}
	if err := verifyInputs(s.repo, txs); err != nil {
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

// fit picks the pool transactions whose sealed block fits domain.MaxBlockBytes.
// The room left by an empty block is the budget handed to the pool, but each
// transaction after the first also costs a separator byte, so when the block
// is too large the budget drops to that room less the separators of the
// selection just made. It falls on every pass, so the loop ends.
func (s *Service) fit() ([]domain.Transaction, error) {
	limit := domain.MaxBlockBytes()
	empty, err := sealedSize([]domain.Transaction{})
	if err != nil {
		return nil, err
	}
	room := limit - empty
	budget := room
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
		budget = room - (len(txs) - 1)
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
		if _, err := domain.BlockDelta(s.repo, &domain.Block{Transactions: one}, false); err != nil || verifyInputs(s.repo, one) != nil {
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

// writeFailed classifies an error of Commit or Reorg, which run after the block
// was verified. storage.ErrConflict means the tip moved under the Service and
// the write changed nothing, so it is returned as is; any other error leaves the
// state unknown and stops the service. The caller holds writeSem.
func (s *Service) writeFailed(err error) error {
	if errors.Is(err, storage.ErrConflict) {
		return err
	}
	s.diverged = true
	return fmt.Errorf("%w: %w", ErrStateDiverged, err)
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

	delta, err := domain.BlockDelta(s.repo, &domain.Block{Transactions: txs}, false)
	if err != nil {
		return nil, err
	}
	if err := verifyInputs(s.repo, txs); err != nil {
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

	if err := s.repo.Commit(block, delta); err != nil {
		return nil, s.writeFailed(err)
	}

	s.pool.Prune(block, s.repo)
	return block, nil
}

// ReplaceChain switches the local chain to candidate, a full chain starting at
// the local genesis, when it carries strictly more accumulated work after the
// fork point (the first height where the two differ); equal work keeps the
// local chain. Every candidate block from the fork on is verified by the
// consensus, the block rules, the UTXO rules and its input signatures, against
// the UTXO state of the candidate branch itself, before anything is written.
// The first failure returns its error and leaves the repository and the UTXO
// set untouched. The swap is one Reorg, which reverts the stored deltas of the
// dropped blocks and commits the candidate's; a failure of it stops the service
// like Mine does. Calls
// are serialized with Mine; waiting for the turn is bounded by ctx.
// Transactions of the dropped blocks are returned in Reorg.Orphaned; the pool is not revalidated.
func (s *Service) ReplaceChain(ctx context.Context, candidate []*domain.Block) (*Reorg, error) {
	release, err := s.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	local, err := s.repo.All()
	if err != nil {
		return nil, err
	}
	if len(local) == 0 {
		return nil, storage.ErrEmpty
	}
	fork, err := forkPoint(local, candidate)
	if err != nil {
		return nil, err
	}
	// A candidate that ends where the chains still agree has no work of its own.
	if fork == len(candidate) {
		return nil, ErrInsufficientWork
	}

	// forkState is the UTXO set at the fork, the base the candidate is checked
	// against.
	forkState, err := domain.BuildUTXOSet(local[:fork])
	if err != nil {
		return nil, err
	}

	// The shared prefix is the local one, whatever else the candidate carries there.
	history := make([]*domain.Block, fork, len(candidate))
	copy(history, local[:fork])
	now := time.Now()
	var deltas []domain.UTXODelta
	for _, b := range candidate[fork:] {
		if b == nil {
			return nil, ErrInvalidBlock
		}
		if err := s.consensus.Verify(history, b); err != nil {
			return nil, err
		}
		if err := domain.ValidateBlockRules(b, history[len(history)-1], now); err != nil {
			return nil, err
		}
		delta, err := domain.BlockDelta(forkState, b, false)
		if err != nil {
			return nil, err
		}
		if err := verifyInputs(forkState, b.Transactions); err != nil {
			return nil, err
		}
		forkState.Apply(delta)
		deltas = append(deltas, delta)
		history = append(history, b)
	}

	if domain.ChainWork(candidate[fork:]).Cmp(domain.ChainWork(local[fork:])) <= 0 {
		return nil, ErrInsufficientWork
	}

	connected := slices.Clone(candidate[fork:])
	if err := s.repo.Reorg(fork, connected, deltas); err != nil {
		return nil, s.writeFailed(err)
	}

	disconnected := slices.Clone(local[fork:])
	return &Reorg{
		ForkHeight:   fork,
		Disconnected: disconnected,
		Connected:    connected,
		Orphaned:     orphaned(disconnected, connected),
	}, nil
}

// forkPoint returns the first height where candidate and local differ, which is
// at least 1: the candidate must start at the local genesis. Callers hold a
// non-empty local.
func forkPoint(local, candidate []*domain.Block) (int, error) {
	if len(candidate) == 0 || candidate[0] == nil {
		return 0, ErrInvalidBlock
	}
	genesis := candidate[0]
	// The genesis is never sealed, so its Hash may be empty: compare what it commits to.
	if genesis.Hash != local[0].Hash || genesis.CalculateHash() != local[0].CalculateHash() {
		return 0, ErrGenesisMismatch
	}
	fork := 1
	for fork < len(local) && fork < len(candidate) && candidate[fork] != nil && candidate[fork].Hash == local[fork].Hash {
		fork++
	}
	return fork, nil
}

// orphaned lists, in chain order, the transactions of disconnected that no
// connected block holds. Transactions are matched by hash, not by their ID field.
func orphaned(disconnected, connected []*domain.Block) []domain.Transaction {
	kept := map[string]bool{}
	for _, b := range connected {
		for i := range b.Transactions {
			kept[b.Transactions[i].CalculateHash()] = true
		}
	}
	var out []domain.Transaction
	for _, b := range disconnected {
		for i := range b.Transactions {
			if !kept[b.Transactions[i].CalculateHash()] {
				out = append(out, b.Transactions[i])
			}
		}
	}
	return out
}
