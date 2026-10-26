package chain_test

import (
	"context"
	"path/filepath"
	"slices"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	. "github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/bolt"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

const fundedOutputs = 12

// backend opens an empty repository; every chain suite runs on each backend.
type backend struct {
	name string
	open func() storage.IndexedRepository
	// pruned backends drop block bodies below PrunedBelow.
	pruned bool
}

var backends = []backend{
	{"memory", func() storage.IndexedRepository { return memory.New() }, false},
	{"bolt", func() storage.IndexedRepository {
		r, err := bolt.Open(filepath.Join(GinkgoT().TempDir(), "chain.db"))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = r.Close() })
		return r
	}, false},
}

// prunedPolicy snapshots every 2 blocks and keeps the bodies of the last 3.
var prunedPolicy = storage.Pruning{SnapshotInterval: 2, KeepBlocks: 3}

func init() {
	backends = append(backends,
		backend{"memory-pruned", func() storage.IndexedRepository {
			r, err := memory.NewPruned(prunedPolicy)
			Expect(err).NotTo(HaveOccurred())
			return r
		}, true},
		backend{"bolt-pruned", func() storage.IndexedRepository {
			r, err := bolt.OpenPruned(filepath.Join(GinkgoT().TempDir(), "chain.db"), prunedPolicy)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = r.Close() })
			return r
		}, true},
	)
}

// newRepo opens an empty repository of the backend under test.
var newRepo func() storage.IndexedRepository

// funding hands out one signed, valid spend of a genesis allocation per call.
type funding struct {
	owner *wallet.Wallet
	ops   []domain.OutPoint
	next  int
}

// funded builds a Service whose genesis allocates fundedOutputs outputs of 10 to a fresh owner.
func funded(repo storage.IndexedRepository, c consensus.Consensus, opts ...Option) (*Service, *funding) {
	owner, err := wallet.New()
	Expect(err).NotTo(HaveOccurred())

	allocs := make([]domain.TxOut, fundedOutputs)
	for i := range allocs {
		allocs[i] = domain.TxOut{Amount: 10, Address: owner.Address()}
	}

	svc, err := NewService(repo, c, append([]Option{WithGenesisAllocations(allocs)}, opts...)...)
	Expect(err).NotTo(HaveOccurred())

	f := &funding{owner: owner}
	blocks, err := repo.All()
	if err != nil || len(blocks[0].Transactions) == 0 {
		return svc, f
	}
	txid := blocks[0].Transactions[0].CalculateHash()
	for i := range allocs {
		f.ops = append(f.ops, domain.OutPoint{TxID: txid, Index: uint32(i)})
	}
	return svc, f
}

// tx spends the next unused genesis allocation.
func (f *funding) tx() []domain.Transaction {
	op := f.ops[f.next]
	f.next++
	return []domain.Transaction{spend(f.owner, op, 9)}
}

// spend builds a transaction that spends op with w and pays out to a fixed recipient.
func spend(w *wallet.Wallet, op domain.OutPoint, out uint64) domain.Transaction {
	tx := domain.Transaction{
		Inputs:    []domain.TxIn{{PrevTxID: op.TxID, OutIndex: op.Index}},
		Outputs:   []domain.TxOut{{Amount: out, Address: recipient().Address()}},
		Timestamp: time.Now(),
	}
	tx.ID = tx.CalculateHash()
	Expect(domain.SignInput(&tx, 0, w)).To(Succeed())
	return tx
}

func recipient() *wallet.Wallet {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = 7
	}
	w, err := wallet.FromSeed(seed)
	Expect(err).NotTo(HaveOccurred())
	return w
}

// grow seals one block per txs entry on top of chain, without validating the
// transactions, and returns chain followed by the new blocks. chain is not altered.
func grow(c consensus.Consensus, chain []*domain.Block, txss ...[]domain.Transaction) []*domain.Block {
	out := slices.Clone(chain)
	for _, txs := range txss {
		b, err := GenerateBlockWithContext(context.Background(), c, out, txs)
		Expect(err).NotTo(HaveOccurred())
		out = append(out, b)
	}
	return out
}

// outpointOf is the output index of tx.
func outpointOf(tx domain.Transaction, i uint32) domain.OutPoint {
	return domain.OutPoint{TxID: tx.CalculateHash(), Index: i}
}

// hashes lists the block hashes of chain, to compare chains without sharing pointers.
func hashes(chain []*domain.Block) []string {
	out := make([]string, len(chain))
	for i, b := range chain {
		out[i] = b.Hash
	}
	return out
}

// utxoSnapshot is the UTXO set persisted by repo. A complete repository only
// answers Lookup, so it asks for every output any stored block ever created; a
// pruned one has lost those bodies and hands out its current set instead.
func utxoSnapshot(repo storage.IndexedRepository) domain.UTXOSet {
	blocks, err := repo.All()
	Expect(err).NotTo(HaveOccurred())
	if below, err := repo.PrunedBelow(); err == nil && below > 0 {
		set, err := repo.ForkState(len(blocks))
		Expect(err).NotTo(HaveOccurred())
		return set
	}
	set := domain.UTXOSet{}
	for _, b := range blocks {
		for i := range b.Transactions {
			txid := b.Transactions[i].CalculateHash()
			for j := range b.Transactions[i].Outputs {
				op := domain.OutPoint{TxID: txid, Index: uint32(j)}
				if out, ok := repo.Lookup(op); ok {
					set[op] = out
				}
			}
		}
	}
	return set
}

// replayRepo rebuilds the UTXO set from the chain repo holds: from genesis for
// a complete repository, from its snapshot and the retained bodies after it for
// a pruned one.
func replayRepo(repo storage.IndexedRepository) domain.UTXOSet {
	blocks := mustAll(repo)
	below, err := repo.PrunedBelow()
	Expect(err).NotTo(HaveOccurred())
	if below == 0 {
		return replay(blocks)
	}
	snap, err := repo.Snapshot()
	Expect(err).NotTo(HaveOccurred())
	Expect(snap.Height).To(Equal(below - 1))
	set := snap.UTXOs
	for _, b := range blocks[below:] {
		delta, err := domain.BlockDelta(set, b, false)
		Expect(err).NotTo(HaveOccurred())
		set.Apply(delta)
	}
	return set
}

// failingWrites fails Commit and Reorg on demand.
type failingWrites struct {
	storage.IndexedRepository
	commitErr, reorgErr error
}

func (f *failingWrites) Commit(b *domain.Block, d domain.UTXODelta) error {
	if f.commitErr != nil {
		return f.commitErr
	}
	return f.IndexedRepository.Commit(b, d)
}

func (f *failingWrites) Reorg(h int, bs []*domain.Block, ds []domain.UTXODelta) error {
	if f.reorgErr != nil {
		return f.reorgErr
	}
	return f.IndexedRepository.Reorg(h, bs, ds)
}
