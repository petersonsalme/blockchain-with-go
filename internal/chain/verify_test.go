package chain_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	. "github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
	"github.com/petersonsalme/blockchain-with-go/internal/vm"
)

// tampered serves the stored chain with edit applied to a copy of the blocks,
// the way a block edited on disk would read back.
type tampered struct {
	storage.IndexedRepository

	edit     func(all []*domain.Block)
	allErr   error
	pruned   *int
	snapshot *storage.Snapshot
	snapErr  error
	onAll    func()
}

func (t *tampered) All() ([]*domain.Block, error) {
	if t.onAll != nil {
		t.onAll()
	}

	if t.allErr != nil {
		return nil, t.allErr
	}

	all, err := t.IndexedRepository.All()
	if err != nil {
		return nil, err
	}

	out := make([]*domain.Block, len(all))
	for i, b := range all {
		if b != nil {
			c := *b
			c.Transactions = slices.Clone(b.Transactions)

			for j := range c.Transactions {
				c.Transactions[j].Outputs = slices.Clone(c.Transactions[j].Outputs)
				c.Transactions[j].Inputs = slices.Clone(c.Transactions[j].Inputs)
			}

			out[i] = &c
		}
	}

	if t.edit != nil {
		t.edit(out)
	}

	return out, nil
}

func (t *tampered) PrunedBelow() (int, error) {
	if t.pruned != nil {
		return *t.pruned, nil
	}

	return t.IndexedRepository.PrunedBelow()
}

func (t *tampered) Snapshot() (storage.Snapshot, error) {
	if t.snapErr != nil {
		return storage.Snapshot{}, t.snapErr
	}

	if t.snapshot != nil {
		return *t.snapshot, nil
	}

	return t.IndexedRepository.Snapshot()
}

func mergeCode(err error) string {
	if ve, ok := errors.AsType[*domain.ValidationError](err); ok {
		return ve.Code
	}

	return ""
}

var _ = Describe("Service.Verify", func() {
	for _, be := range backends {
		Describe(be.name, func() {
			var (
				repo storage.IndexedRepository
				svc  *Service
				f    *funding
				eng  = pow.New(1)
				ctx  context.Context
			)

			BeforeEach(func() {
				// A Verify that kept the write lock would hang the next call; a deadline fails the spec instead.
				var cancel context.CancelFunc

				ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second) //nolint:fatcontext // replaced once per spec, not nested in a loop

				DeferCleanup(cancel)

				repo = be.open()
				svc, f = funded(repo, eng)

				for range prunedTip {
					_, err := svc.Mine(ctx, f.tx())
					Expect(err).NotTo(HaveOccurred())
				}
			})

			serve := func(edit func(all []*domain.Block)) *tampered {
				t := &tampered{IndexedRepository: repo, edit: edit}
				svc.SwapRepo(t)

				return t
			}

			It("accepts a chain built by Mine", func() {
				Expect(svc.Verify(ctx)).To(Succeed())
			})

			It("accepts a chain extended by AcceptBlock and ReplaceChain", func() {
				local := mustAll(repo)
				cand := grow(eng, local[:len(local)-1], []domain.Transaction{spend(f.owner, f.ops[prunedTip+1], 8)}, []domain.Transaction{spend(f.owner, f.ops[prunedTip+2], 8)})

				if be.pruned {
					cand = grow(eng, local, []domain.Transaction{spend(f.owner, f.ops[prunedTip+1], 8)})
					Expect(svc.AcceptBlock(ctx, cand[len(cand)-1])).To(Succeed())
				} else {
					_, err := svc.ReplaceChain(ctx, cand)
					Expect(err).NotTo(HaveOccurred())
				}

				Expect(svc.Verify(ctx)).To(Succeed())
			})

			It("changes nothing, for a valid and for a tampered chain", func() {
				before := hashes(mustAll(repo))
				utxos := utxoSnapshot(repo)

				current, err := repo.IndexCurrent()
				Expect(err).NotTo(HaveOccurred())

				Expect(svc.Verify(ctx)).To(Succeed())

				serve(func(all []*domain.Block) { all[prunedTip].Transactions[0].Outputs[0].Amount++ })
				Expect(svc.Verify(ctx)).To(HaveOccurred())
				svc.SwapRepo(repo)

				Expect(hashes(mustAll(repo))).To(Equal(before))
				Expect(utxoSnapshot(repo)).To(Equal(utxos))

				again, err := repo.IndexCurrent()
				Expect(err).NotTo(HaveOccurred())
				Expect(again).To(Equal(current))

				_, _, ok := svc.Contract("none")
				Expect(ok).To(BeFalse())
			})

			It("names the height and wraps the sentinel and the rule error", func() {
				serve(func(all []*domain.Block) { all[prunedTip].Transactions[0].Outputs[0].Amount++ })

				err := svc.Verify(ctx)

				Expect(errors.Is(err, ErrInvalidStoredChain)).To(BeTrue(), "%v", err)
				Expect(mergeCode(err)).To(Equal(domain.CodeMerkleRootMismatch))
				Expect(err.Error()).To(HavePrefix(fmt.Sprintf("chain: stored block %d: ", prunedTip)))
			})

			It("stops at the first failing block", func() {
				serve(func(all []*domain.Block) {
					all[prunedTip].Transactions[0].Outputs[0].Amount++
					all[prunedTip-1].Transactions[0].Outputs[0].Amount++
				})

				err := svc.Verify(ctx)

				Expect(err.Error()).To(HavePrefix(fmt.Sprintf("chain: stored block %d: ", prunedTip-1)))
			})

			It("rejects a changed nonce", func() {
				serve(func(all []*domain.Block) { all[prunedTip-2].Nonce++ })

				err := svc.Verify(ctx)

				Expect(errors.Is(err, ErrInvalidStoredChain)).To(BeTrue())
				Expect(errors.Is(err, consensus.ErrInvalidHash)).To(BeTrue(), "%v", err)
				Expect(err.Error()).To(HavePrefix(fmt.Sprintf("chain: stored block %d: ", prunedTip-2)))
			})

			It("rejects a changed input signature", func() {
				local := mustAll(repo)
				txs := slices.Clone(local[prunedTip].Transactions)
				txs[0].Inputs = slices.Clone(txs[0].Inputs)
				sig := []byte(txs[0].Inputs[0].Signature)
				sig[0] ^= 1
				txs[0].Inputs[0].Signature = string(sig)
				bad := grow(eng, local[:prunedTip], txs)

				serve(func(all []*domain.Block) { all[prunedTip] = bad[prunedTip] })

				err := svc.Verify(ctx)

				Expect(errors.Is(err, ErrInvalidStoredChain)).To(BeTrue())
				Expect(errors.Is(err, domain.ErrInvalidSignature)).To(BeTrue(), "%v", err)
				Expect(err.Error()).To(HavePrefix(fmt.Sprintf("chain: stored block %d: ", prunedTip)))
			})

			It("rejects a header whose previous hash changed", func() {
				serve(func(all []*domain.Block) { all[2].PrevHash = "ff" + all[2].PrevHash[2:] })

				err := svc.Verify(ctx)

				Expect(errors.Is(err, ErrInvalidStoredChain)).To(BeTrue())
				Expect(errors.Is(err, consensus.ErrInvalidPrevHash)).To(BeTrue(), "%v", err)
				Expect(err.Error()).To(HavePrefix("chain: stored block 2: "))
			})

			It("rejects a header whose timestamp is not after the median time past", func() {
				serve(func(all []*domain.Block) {
					old := all[3]
					all[3] = &domain.Block{Index: old.Index, Timestamp: all[2].Timestamp.Add(-time.Hour), PrevHash: old.PrevHash, MerkleRoot: old.MerkleRoot, Nonce: old.Nonce}
					all[3].Hash = all[3].CalculateHash()
				})

				err := svc.Verify(ctx)

				Expect(errors.Is(err, ErrInvalidStoredChain)).To(BeTrue())
			})

			It("rejects a block spending an output that does not exist", func() {
				local := mustAll(repo)
				ghost := domain.OutPoint{TxID: "ff", Index: 0}
				bad := grow(eng, local[:prunedTip], []domain.Transaction{spend(f.owner, ghost, 8)})

				serve(func(all []*domain.Block) { all[prunedTip] = bad[prunedTip] })

				err := svc.Verify(ctx)

				Expect(errors.Is(err, domain.ErrInputMissingOrSpent)).To(BeTrue(), "%v", err)
				Expect(err.Error()).To(HavePrefix(fmt.Sprintf("chain: stored block %d: ", prunedTip)))
			})

			It("rejects a block whose contract call fails", func() {
				local := mustAll(repo)
				call := withContract(f.owner, f.ops[prunedTip+1], 9, callOp(strings.Repeat("0", 64), 100))
				bad := grow(eng, local[:prunedTip], []domain.Transaction{call})

				serve(func(all []*domain.Block) { all[prunedTip] = bad[prunedTip] })

				err := svc.Verify(ctx)

				Expect(errors.Is(err, vm.ErrUnknownContract)).To(BeTrue(), "%v", err)
			})

			It("rejects a changed genesis allocation", func() {
				if be.pruned {
					Skip("the pruned genesis is a header")
				}

				serve(func(all []*domain.Block) { all[0].Transactions[0].Outputs[0].Amount++ })

				err := svc.Verify(ctx)

				Expect(errors.Is(err, ErrInvalidStoredChain)).To(BeTrue())
				Expect(mergeCode(err)).To(Equal(domain.CodeMerkleRootMismatch))
				Expect(err.Error()).To(HavePrefix("chain: stored block 0: "))
			})

			It("rejects a genesis allocation with a zero amount", func() {
				if be.pruned {
					Skip("the pruned genesis is a header")
				}

				serve(func(all []*domain.Block) {
					all[0].Transactions[0].Outputs[0].Amount = 0
					all[0].MerkleRoot = domain.MerkleRoot(all[0].Transactions)
				})

				Expect(svc.Verify(ctx)).To(MatchError(domain.ErrInvalidOutput))
			})

			DescribeTable("rejects a malformed genesis",
				func(edit func(g *domain.Block)) {
					serve(func(all []*domain.Block) { edit(all[0]) })

					err := svc.Verify(ctx)

					Expect(errors.Is(err, ErrInvalidStoredChain)).To(BeTrue(), "%v", err)
					Expect(err.Error()).To(HavePrefix("chain: stored block 0: "))
				},
				Entry("index", func(g *domain.Block) { g.Index = 1 }),
				Entry("previous hash", func(g *domain.Block) { g.PrevHash = "aa" }),
				Entry("hash", func(g *domain.Block) { g.Hash = "aa" }),
			)

			It("accepts a genesis whose hash is its own content hash", func() {
				serve(func(all []*domain.Block) { all[0].Hash = all[0].CalculateHash() })

				// block 1 links to the stored empty hash, so the replay stops there, not at the genesis
				err := svc.Verify(ctx)

				Expect(errors.Is(err, consensus.ErrInvalidPrevHash)).To(BeTrue(), "%v", err)
				Expect(err.Error()).To(HavePrefix("chain: stored block 1: "))
			})

			It("rejects a nil block", func() {
				serve(func(all []*domain.Block) { all[2] = nil })

				err := svc.Verify(ctx)

				Expect(errors.Is(err, ErrInvalidBlock)).To(BeTrue())
				Expect(err.Error()).To(HavePrefix("chain: stored block 2: "))
			})

			It("fails on a cancelled context before reading", func() {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()

				Expect(svc.Verify(cancelled)).To(MatchError(context.Canceled))
			})

			It("stops when the context is cancelled during the replay", func() {
				cancelled, cancel := context.WithCancel(ctx)
				serve(nil).onAll = cancel

				Expect(svc.Verify(cancelled)).To(MatchError(context.Canceled))
			})

			It("waits for the write lock", func() {
				entered, release := make(chan struct{}, 1), make(chan struct{})
				serve(nil).onAll = func() {
					select {
					case entered <- struct{}{}:
						<-release
					default:
					}
				}

				done := make(chan error, 1)

				go func() { done <- svc.Verify(ctx) }()

				Eventually(entered).Should(Receive())

				short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()

				_, err := svc.Mine(short, f.tx())
				Expect(err).To(MatchError(context.DeadlineExceeded))

				close(release)
				Eventually(done).Should(Receive(BeNil()))
			})

			It("reports a repository that fails", func() {
				boom := errors.New("boom")
				serve(nil).allErr = boom

				Expect(svc.Verify(ctx)).To(MatchError(boom))
			})

			It("reports an empty repository", func() {
				svc.SwapRepo(&emptyRepo{IndexedRepository: repo})

				Expect(svc.Verify(ctx)).To(MatchError(storage.ErrEmpty))
			})
		})
	}

	Describe("pruned snapshot", func() {
		var (
			repo storage.IndexedRepository
			svc  *Service
			f    *funding
			eng  = pow.New(1)
			ctx  = context.Background()
		)

		BeforeEach(func() {
			for _, be := range backends {
				if be.name == "memory-pruned" {
					repo = be.open()
				}
			}

			svc, f = funded(repo, eng)

			for range prunedTip {
				_, err := svc.Mine(ctx, f.tx())
				Expect(err).NotTo(HaveOccurred())
			}
		})

		It("rejects a snapshot of another height", func() {
			snap, err := repo.Snapshot()
			Expect(err).NotTo(HaveOccurred())

			snap.Height++
			svc.SwapRepo(&tampered{IndexedRepository: repo, snapshot: &snap})

			err = svc.Verify(ctx)

			Expect(errors.Is(err, ErrInvalidStoredChain)).To(BeTrue())
			Expect(err.Error()).To(HavePrefix("chain: stored block 4: "))
		})

		It("rejects a snapshot of another block", func() {
			snap, err := repo.Snapshot()
			Expect(err).NotTo(HaveOccurred())

			snap.BlockHash = "aa"
			svc.SwapRepo(&tampered{IndexedRepository: repo, snapshot: &snap})

			Expect(svc.Verify(ctx)).To(MatchError(ErrInvalidStoredChain))
		})

		It("rejects a missing snapshot", func() {
			svc.SwapRepo(&tampered{IndexedRepository: repo, snapErr: storage.ErrNotFound})

			err := svc.Verify(ctx)

			Expect(errors.Is(err, storage.ErrNotFound)).To(BeTrue())
			Expect(errors.Is(err, ErrInvalidStoredChain)).To(BeTrue())
		})

		It("reports a PrunedBelow that fails", func() {
			svc.SwapRepo(&brokenPruned{IndexedRepository: repo})

			Expect(svc.Verify(ctx)).To(MatchError(errBoom))
		})
	})
})

type emptyRepo struct{ storage.IndexedRepository }

func (emptyRepo) All() ([]*domain.Block, error) { return nil, nil }

type brokenPruned struct{ storage.IndexedRepository }

func (brokenPruned) PrunedBelow() (int, error) { return 0, errBoom }
