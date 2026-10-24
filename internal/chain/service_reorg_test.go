package chain_test

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	. "github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/poa"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

// engineCase names a consensus engine and how to break its seal in place.
type engineCase struct {
	name string
	new  func() consensus.Consensus
	// breakSeal makes b fail verification on its seal while its hash stays consistent.
	breakSeal func(b *domain.Block)
	sealErr   error
	// wrongDifficulty gives b a difficulty the engine does not expect, rehashed.
	wrongDifficulty func(b *domain.Block)
	difficultyErr   error
}

var engineCases = []engineCase{
	{
		name: "pow",
		new:  func() consensus.Consensus { return pow.New(1) },
		breakSeal: func(b *domain.Block) {
			// difficulty 1: a hash solves it when its first hex digit is below 8.
			for b.Hash[0] < '8' {
				b.Nonce++
				b.Hash = b.CalculateHash()
			}
		},
		sealErr:         consensus.ErrInvalidProofOfWork,
		wrongDifficulty: func(b *domain.Block) { b.Difficulty++; b.Hash = b.CalculateHash() },
		difficultyErr:   consensus.ErrInvalidDifficulty,
	},
	{
		name: "poa",
		new: func() consensus.Consensus {
			w, err := wallet.New()
			Expect(err).NotTo(HaveOccurred())
			c, err := poa.New([]ed25519.PublicKey{w.PublicKey()}, w)
			Expect(err).NotTo(HaveOccurred())
			return c
		},
		breakSeal:       func(b *domain.Block) { b.Signature = strings.Repeat("0", 128) },
		sealErr:         consensus.ErrInvalidSignature,
		wrongDifficulty: func(b *domain.Block) { b.Difficulty = 1; b.Hash = b.CalculateHash() },
		difficultyErr:   consensus.ErrInvalidSeal,
	},
}

// fixedDifficulty is a Consensus that accepts any difficulty, so tests can
// weigh chains independently of their length.
type fixedDifficulty struct{ d int }

func (f fixedDifficulty) Seal(_ context.Context, _ []*domain.Block, b *domain.Block) error {
	b.Difficulty = f.d
	b.Hash = b.CalculateHash()
	return nil
}

func (f fixedDifficulty) Verify(history []*domain.Block, b *domain.Block) error {
	prev := history[len(history)-1]
	switch {
	case prev.Index+1 != b.Index:
		return consensus.ErrInvalidIndex
	case prev.Hash != b.PrevHash:
		return consensus.ErrInvalidPrevHash
	case b.CalculateHash() != b.Hash:
		return consensus.ErrInvalidHash
	}
	return nil
}

// replaceFailingRepo fails ReplaceFrom on demand.
type replaceFailingRepo struct {
	storage.BlockRepository
	err error
}

func (r *replaceFailingRepo) ReplaceFrom(h int, bs []*domain.Block) error {
	if r.err != nil {
		return r.err
	}
	return r.BlockRepository.ReplaceFrom(h, bs)
}

// forkFixture is a service holding branch A, with a heavier competing branch B
// that forks from the same ancestor.
//
//	genesis - common - A2[shared, onlyA]
//	                \- B2[shared] - B3[spend of B2's output, owner spend]
type forkFixture struct {
	svc   *Service
	repo  *memory.Repository
	utxos *memory.UTXOSet
	eng   consensus.Consensus
	f     *funding

	base      []*domain.Block // genesis, common
	local     []*domain.Block // base, A2
	candidate []*domain.Block // base, B2, B3
	shared    domain.Transaction
	onlyA     domain.Transaction
	b3        []domain.Transaction
}

func newForkFixture(ec engineCase, opts ...Option) *forkFixture {
	fx := &forkFixture{repo: memory.New(), utxos: memory.NewUTXOSet(), eng: ec.new()}
	fx.svc, fx.f = funded(fx.repo, fx.eng, append([]Option{WithUTXOSet(fx.utxos)}, opts...)...)

	_, err := fx.svc.Mine(context.Background(), fx.f.tx())
	Expect(err).NotTo(HaveOccurred())
	fx.base, _ = fx.repo.All()

	ops := fx.f.ops
	fx.shared = spend(fx.f.owner, ops[1], 9)
	fx.onlyA = spend(fx.f.owner, ops[2], 8)
	_, err = fx.svc.Mine(context.Background(), []domain.Transaction{fx.shared, fx.onlyA})
	Expect(err).NotTo(HaveOccurred())
	fx.local, _ = fx.repo.All()

	fx.b3 = []domain.Transaction{
		spend(recipient(), outpointOf(fx.shared, 0), 5),
		spend(fx.f.owner, ops[3], 7),
	}
	fx.candidate = grow(fx.eng, fx.base, []domain.Transaction{fx.shared}, fx.b3)
	return fx
}

// replay is the UTXO set of chain, rebuilt from the genesis.
func replay(chain []*domain.Block) domain.UTXOSet {
	set, err := domain.BuildUTXOSet(chain)
	Expect(err).NotTo(HaveOccurred())
	return set
}

var _ = Describe("Service.ReplaceChain", func() {
	for _, ec := range engineCases {
		ec := ec
		Describe(ec.name, func() {
			var fx *forkFixture

			BeforeEach(func() { fx = newForkFixture(ec) })

			untouched := func(run func() error) {
				beforeBlocks, _ := fx.svc.Blocks()
				beforeUTXO := fx.utxos.Snapshot()
				Expect(run()).NotTo(Succeed())
				afterBlocks, _ := fx.svc.Blocks()
				Expect(hashes(afterBlocks)).To(Equal(hashes(beforeBlocks)))
				Expect(fx.utxos.Snapshot()).To(Equal(beforeUTXO))
			}

			Describe("a heavier branch", func() {
				It("replaces the local tail and reports the reorg", func() {
					reorg, err := fx.svc.ReplaceChain(context.Background(), fx.candidate)
					Expect(err).NotTo(HaveOccurred())

					Expect(reorg.ForkHeight).To(Equal(2))
					Expect(hashes(reorg.Disconnected)).To(Equal(hashes(fx.local[2:])))
					Expect(hashes(reorg.Connected)).To(Equal(hashes(fx.candidate[2:])))
					Expect(reorg.Orphaned).To(HaveLen(1))
					Expect(reorg.Orphaned[0].ID).To(Equal(fx.onlyA.ID))

					blocks, err := fx.svc.Blocks()
					Expect(err).NotTo(HaveOccurred())
					Expect(hashes(blocks)).To(Equal(hashes(fx.candidate)))

					want := replay(blocks)
					Expect(fx.utxos.Snapshot()).To(Equal(want))
					for _, addr := range []string{fx.f.owner.Address(), recipient().Address()} {
						var sum uint64
						for _, out := range want {
							if out.Address == addr {
								sum += out.Amount
							}
						}
						got, _ := fx.svc.Balance(addr)
						Expect(got).To(Equal(sum), addr)
					}

					_, err = fx.svc.BlockByHash(fx.local[2].Hash)
					Expect(err).To(MatchError(storage.ErrNotFound))
				})

				It("lets the next Mine extend the new tip", func() {
					_, err := fx.svc.ReplaceChain(context.Background(), fx.candidate)
					Expect(err).NotTo(HaveOccurred())

					b, err := fx.svc.Mine(context.Background(), []domain.Transaction{spend(fx.f.owner, fx.f.ops[4], 9)})
					Expect(err).NotTo(HaveOccurred())

					tip := fx.candidate[len(fx.candidate)-1]
					Expect(b.PrevHash).To(Equal(tip.Hash))
					Expect(b.Index).To(Equal(tip.Index + 1))
					blocks, _ := fx.svc.Blocks()
					Expect(fx.utxos.Snapshot()).To(Equal(replay(blocks)))
				})

				It("returns an empty Orphaned when the branches hold the same transactions", func() {
					same := grow(fx.eng, fx.base, []domain.Transaction{fx.shared, fx.onlyA}, nil)
					reorg, err := fx.svc.ReplaceChain(context.Background(), same)
					Expect(err).NotTo(HaveOccurred())
					Expect(reorg.Orphaned).To(BeEmpty())
				})

				It("lists every orphan in chain order", func() {
					// A grows to three blocks, B to four; none of A's transactions are in B.
					a3 := []domain.Transaction{spend(fx.f.owner, fx.f.ops[5], 9)}
					a4 := []domain.Transaction{spend(fx.f.owner, fx.f.ops[6], 9)}
					_, err := fx.svc.Mine(context.Background(), a3)
					Expect(err).NotTo(HaveOccurred())
					_, err = fx.svc.Mine(context.Background(), a4)
					Expect(err).NotTo(HaveOccurred())
					heavier := grow(fx.eng, fx.base,
						[]domain.Transaction{spend(fx.f.owner, fx.f.ops[7], 9)},
						[]domain.Transaction{spend(fx.f.owner, fx.f.ops[8], 9)},
						[]domain.Transaction{spend(fx.f.owner, fx.f.ops[9], 9)},
						[]domain.Transaction{spend(fx.f.owner, fx.f.ops[10], 9)},
					)

					reorg, err := fx.svc.ReplaceChain(context.Background(), heavier)
					Expect(err).NotTo(HaveOccurred())
					Expect(reorg.Orphaned).To(HaveLen(4))
					Expect([]string{reorg.Orphaned[0].ID, reorg.Orphaned[1].ID, reorg.Orphaned[2].ID, reorg.Orphaned[3].ID}).To(Equal(
						[]string{fx.shared.ID, fx.onlyA.ID, a3[0].ID, a4[0].ID}))
				})

				It("extends the chain when the candidate has the local chain as a prefix", func() {
					longer := grow(fx.eng, fx.local, []domain.Transaction{spend(fx.f.owner, fx.f.ops[4], 9)})
					reorg, err := fx.svc.ReplaceChain(context.Background(), longer)
					Expect(err).NotTo(HaveOccurred())
					Expect(reorg.ForkHeight).To(Equal(len(fx.local)))
					Expect(reorg.Disconnected).To(BeEmpty())
					Expect(reorg.Orphaned).To(BeEmpty())
					blocks, _ := fx.svc.Blocks()
					Expect(hashes(blocks)).To(Equal(hashes(longer)))
					Expect(fx.utxos.Snapshot()).To(Equal(replay(blocks)))
				})

				It("verifies the candidate's own blocks, not the prefix it shares with the local chain", func() {
					forged := cloneBlocks(fx.candidate)
					// Same hash string, different content: the local block must be kept.
					forgedBase := *forged[1]
					forgedBase.Transactions = nil
					forged[1] = &forgedBase

					_, err := fx.svc.ReplaceChain(context.Background(), forged)
					Expect(err).NotTo(HaveOccurred())
					blocks, _ := fx.svc.Blocks()
					Expect(blocks[1].Transactions).To(Equal(fx.base[1].Transactions))
				})
			})

			Describe("a candidate without more work", func() {
				It("rejects an equal-work branch (first seen wins)", func() {
					equal := grow(fx.eng, fx.base, []domain.Transaction{fx.shared, fx.onlyA})
					untouched(func() error {
						_, err := fx.svc.ReplaceChain(context.Background(), equal)
						Expect(err).To(MatchError(ErrInsufficientWork))
						return err
					})
				})

				It("rejects a lighter branch", func() {
					lighter := fx.base
					untouched(func() error {
						_, err := fx.svc.ReplaceChain(context.Background(), lighter)
						Expect(err).To(MatchError(ErrInsufficientWork))
						return err
					})
				})

				It("rejects the local chain itself", func() {
					untouched(func() error {
						_, err := fx.svc.ReplaceChain(context.Background(), fx.local)
						Expect(err).To(MatchError(ErrInsufficientWork))
						return err
					})
				})
			})

			Describe("an invalid candidate", func() {
				good := func() []domain.Transaction { return []domain.Transaction{spend(fx.f.owner, fx.f.ops[3], 7)} }
				// onTop builds the block a peer would send after history.
				onTop := func(history []*domain.Block, txs []domain.Transaction) *domain.Block {
					b, err := GenerateBlockWithContext(context.Background(), fx.eng, history, txs)
					Expect(err).NotTo(HaveOccurred())
					return b
				}
				// last is B2 followed by one more block, built by mutate.
				last := func(mutate func(history []*domain.Block) *domain.Block) []*domain.Block {
					history := grow(fx.eng, fx.base, []domain.Transaction{fx.shared})
					return append(history, mutate(history))
				}
				mismatch := func(code string) func(error) {
					return func(err error) {
						var ve *domain.ValidationError
						Expect(errors.As(err, &ve)).To(BeTrue(), "%v", err)
						Expect(string(ve.Code)).To(Equal(code))
					}
				}
				is := func(target error) func(error) {
					return func(err error) {
						Expect(err).To(MatchError(target))
					}
				}
				stranger := func() *wallet.Wallet {
					w, err := wallet.New()
					Expect(err).NotTo(HaveOccurred())
					return w
				}

				type row struct {
					name  string
					build func() []*domain.Block
					check func(error)
				}
				rows := func() []row {
					return []row{
						{"nil block", func() []*domain.Block {
							return last(func([]*domain.Block) *domain.Block { return nil })
						}, is(ErrInvalidBlock)},
						{"empty candidate", func() []*domain.Block { return nil }, is(ErrInvalidBlock)},
						{"nil genesis", func() []*domain.Block {
							c := cloneBlocks(fx.candidate)
							c[0] = nil
							return c
						}, is(ErrInvalidBlock)},
						{"different genesis", func() []*domain.Block {
							other := newForkFixture(ec)
							return grow(other.eng, other.candidate, nil)
						}, is(ErrGenesisMismatch)},
						{"genesis altered under the same hash", func() []*domain.Block {
							c := cloneBlocks(fx.candidate)
							g := *c[0]
							g.Nonce++
							c[0] = &g
							return c
						}, is(ErrGenesisMismatch)},
						{"tampered hash", func() []*domain.Block {
							return last(func(h []*domain.Block) *domain.Block {
								b := onTop(h, good())
								b.Hash = strings.Repeat("0", 64)
								return b
							})
						}, is(consensus.ErrInvalidHash)},
						{"seal that does not verify", func() []*domain.Block {
							return last(func(h []*domain.Block) *domain.Block {
								b := onTop(h, good())
								ec.breakSeal(b)
								return b
							})
						}, is(ec.sealErr)},
						{"difficulty off the expected one", func() []*domain.Block {
							return last(func(h []*domain.Block) *domain.Block {
								b := onTop(h, good())
								ec.wrongDifficulty(b)
								return b
							})
						}, is(ec.difficultyErr)},
						{"wrong merkle root", func() []*domain.Block {
							return last(func(h []*domain.Block) *domain.Block {
								b := onTop(h, good())
								b.MerkleRoot = strings.Repeat("1", 64)
								Expect(fx.eng.Seal(context.Background(), h, b)).To(Succeed())
								return b
							})
						}, mismatch(string(domain.CodeMerkleRootMismatch))},
						{"double spend", func() []*domain.Block {
							return last(func(h []*domain.Block) *domain.Block {
								return onTop(h, []domain.Transaction{spend(fx.f.owner, fx.f.ops[3], 7), spend(fx.f.owner, fx.f.ops[3], 6)})
							})
						}, is(domain.ErrDoubleSpend)},
						{"transaction without inputs", func() []*domain.Block {
							return last(func(h []*domain.Block) *domain.Block {
								tx := domain.Transaction{Outputs: []domain.TxOut{{Amount: 1, Address: fx.f.owner.Address()}}, Timestamp: time.Now()}
								tx.ID = tx.CalculateHash()
								return onTop(h, []domain.Transaction{tx})
							})
						}, is(domain.ErrMissingInputs)},
						{"invalid input signature", func() []*domain.Block {
							return last(func(h []*domain.Block) *domain.Block {
								tx := good()[0]
								tx.Inputs[0].Signature = hex.EncodeToString(make([]byte, 64))
								return onTop(h, []domain.Transaction{tx})
							})
						}, is(domain.ErrInvalidSignature)},
						{"input signed by another owner's key", func() []*domain.Block {
							return last(func(h []*domain.Block) *domain.Block {
								return onTop(h, []domain.Transaction{spend(stranger(), fx.f.ops[3], 7)})
							})
						}, is(domain.ErrInputOwnerMismatch)},
						{"input that only the local branch created", func() []*domain.Block {
							return last(func(h []*domain.Block) *domain.Block {
								return onTop(h, []domain.Transaction{spend(recipient(), outpointOf(fx.onlyA, 0), 5)})
							})
						}, is(domain.ErrInputMissingOrSpent)},
						{"input that the candidate already spent", func() []*domain.Block {
							return last(func(h []*domain.Block) *domain.Block {
								return onTop(h, []domain.Transaction{spend(fx.f.owner, fx.f.ops[1], 7)})
							})
						}, is(domain.ErrInputMissingOrSpent)},
						{"invalid first block of the tail, valid after it", func() []*domain.Block {
							bad := onTop(fx.base, []domain.Transaction{spend(fx.f.owner, fx.f.ops[3], 7), spend(fx.f.owner, fx.f.ops[3], 6)})
							h := append(append([]*domain.Block{}, fx.base...), bad)
							return append(h, onTop(h, nil))
						}, is(domain.ErrDoubleSpend)},
						{"block older than its parent", func() []*domain.Block {
							return last(func(h []*domain.Block) *domain.Block {
								b := onTop(h, good())
								b.Timestamp = h[len(h)-1].Timestamp.Add(-time.Hour)
								Expect(fx.eng.Seal(context.Background(), h, b)).To(Succeed())
								return b
							})
						}, mismatch(string(domain.CodeTimestampBeforeParent))},
					}
				}

				// Row names are fixed, so the table can be declared before fx exists.
				for i, name := range []string{
					"nil block", "empty candidate", "nil genesis", "different genesis",
					"genesis altered under the same hash", "tampered hash", "seal that does not verify",
					"difficulty off the expected one", "wrong merkle root", "double spend",
					"transaction without inputs", "invalid input signature", "input signed by another owner's key",
					"input that only the local branch created", "input that the candidate already spent",
					"invalid first block of the tail, valid after it", "block older than its parent",
				} {
					i, name := i, name
					It("rejects "+name+" and changes nothing", func() {
						r := rows()[i]
						Expect(r.name).To(Equal(name))
						untouched(func() error {
							reorg, err := fx.svc.ReplaceChain(context.Background(), r.build())
							Expect(reorg).To(BeNil())
							r.check(err)
							return err
						})
					})
				}
			})

			Describe("failures while committing", func() {
				It("returns the repository error, changing nothing and not diverging", func() {
					eng := ec.new()
					boom := errors.New("boom")
					repo := &replaceFailingRepo{BlockRepository: memory.New()}
					utxos := memory.NewUTXOSet()
					svc, f := funded(repo, eng, WithUTXOSet(utxos))
					_, err := svc.Mine(context.Background(), f.tx())
					Expect(err).NotTo(HaveOccurred())
					local, _ := repo.All()
					cand := grow(eng, local[:1], f.tx(), f.tx(), f.tx())
					repo.err = boom
					before := utxos.Snapshot()

					reorg, err := svc.ReplaceChain(context.Background(), cand)
					Expect(reorg).To(BeNil())
					Expect(err).To(MatchError(boom))
					Expect(utxos.Snapshot()).To(Equal(before))
					after, _ := repo.All()
					Expect(hashes(after)).To(Equal(hashes(local)))

					repo.err = nil
					_, err = svc.Mine(context.Background(), []domain.Transaction{spend(f.owner, f.ops[8], 9)})
					Expect(err).NotTo(HaveOccurred())
				})

				It("fails stop when applying the UTXO difference fails after the swap", func() {
					eng := ec.new()
					boom := errors.New("boom")
					failing := &failingUTXOSet{UTXOSet: memory.NewUTXOSet()}
					repo := memory.New()
					svc, f := funded(repo, eng, WithUTXOSet(failing))
					_, err := svc.Mine(context.Background(), f.tx())
					Expect(err).NotTo(HaveOccurred())
					local, _ := repo.All()
					cand := grow(eng, local[:1], f.tx(), f.tx())
					failing.applyErr = boom

					_, err = svc.ReplaceChain(context.Background(), cand)
					Expect(errors.Is(err, ErrStateDiverged)).To(BeTrue())
					Expect(err).To(MatchError(ContainSubstring("boom")))

					failing.applyErr = nil
					_, err = svc.ReplaceChain(context.Background(), grow(eng, cand, f.tx()))
					Expect(err).To(MatchError(ErrStateDiverged))
					_, err = svc.Mine(context.Background(), f.tx())
					Expect(err).To(MatchError(ErrStateDiverged))
				})

				It("refuses a diverged service without reading the candidate", func() {
					eng := ec.new()
					failing := &failingUTXOSet{UTXOSet: memory.NewUTXOSet()}
					repo := memory.New()
					svc, f := funded(repo, eng, WithUTXOSet(failing))
					failing.applyErr = errors.New("boom")
					_, err := svc.Mine(context.Background(), f.tx())
					Expect(err).To(MatchError(ErrStateDiverged))
					before, _ := repo.All()

					_, err = svc.ReplaceChain(context.Background(), nil)
					Expect(err).To(MatchError(ErrStateDiverged))
					after, _ := repo.All()
					Expect(hashes(after)).To(Equal(hashes(before)))
				})

				It("returns the repository error when the local chain cannot be read", func() {
					eng := ec.new()
					repo := &allFailingRepo{BlockRepository: memory.New()}
					svc, f := funded(repo, eng)
					cand := grow(eng, mustAll(repo.BlockRepository), f.tx())
					repo.armed.Store(true)

					_, err := svc.ReplaceChain(context.Background(), cand)
					Expect(err).To(MatchError("all failed"))
				})
			})

			Describe("waiting for the turn", func() {
				It("returns the context error to a ReplaceChain queued behind Mine, changing nothing", func() {
					eng := ec.new()
					entered, release := make(chan struct{}), make(chan struct{})
					var calls atomic.Int32
					repo := memory.New()
					svc, f := funded(repo, eng, WithGenerator(func(ctx context.Context, c consensus.Consensus, h []*domain.Block, txs []domain.Transaction) (*domain.Block, error) {
						if calls.Add(1) == 1 {
							close(entered)
							<-release
						}
						return GenerateBlockWithContext(ctx, c, h, txs)
					}))
					cand := grow(eng, mustAll(repo), f.tx(), f.tx())
					done := make(chan error, 1)
					go func() {
						defer GinkgoRecover()
						_, err := svc.Mine(context.Background(), f.tx())
						done <- err
					}()
					Eventually(entered).Should(BeClosed())

					ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
					defer cancel()
					reorg, err := svc.ReplaceChain(ctx, cand)
					Expect(reorg).To(BeNil())
					Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())

					close(release)
					Eventually(done).Should(Receive(BeNil()))
					Expect(mustAll(repo)).To(HaveLen(2))
				})

				It("does not replace the chain for a context that is already cancelled", func() {
					ctx, cancel := context.WithCancel(context.Background())
					cancel()
					untouched(func() error {
						_, err := fx.svc.ReplaceChain(ctx, fx.candidate)
						Expect(errors.Is(err, context.Canceled)).To(BeTrue())
						return err
					})
				})
			})
		})
	}

	Describe("fork choice by work, not length", func() {
		newChain := func(d int, n int) (*Service, *memory.Repository, *memory.UTXOSet) {
			repo, utxos := memory.New(), memory.NewUTXOSet()
			svc, err := NewService(repo, fixedDifficulty{d}, WithUTXOSet(utxos))
			Expect(err).NotTo(HaveOccurred())
			for i := 0; i < n; i++ {
				_, err := svc.Mine(context.Background(), []domain.Transaction{})
				Expect(err).NotTo(HaveOccurred())
			}
			return svc, repo, utxos
		}

		It("keeps three blocks of 4 bits against one block of 5 bits, and yields to one of 6 bits", func() {
			svc, repo, _ := newChain(4, 3)
			local := mustAll(repo)

			_, err := svc.ReplaceChain(context.Background(), grow(fixedDifficulty{5}, local[:1], nil))
			Expect(err).To(MatchError(ErrInsufficientWork))
			Expect(mustAll(repo)).To(HaveLen(4))

			reorg, err := svc.ReplaceChain(context.Background(), grow(fixedDifficulty{6}, local[:1], nil))
			Expect(err).NotTo(HaveOccurred())
			Expect(reorg.ForkHeight).To(Equal(1))
			Expect(reorg.Disconnected).To(HaveLen(3))
			Expect(mustAll(repo)).To(HaveLen(2))
		})

		It("weighs only the blocks after the fork", func() {
			// Equal tails over a heavy common prefix: the prefix must not tip the balance.
			svc, repo, _ := newChain(4, 2)
			local := mustAll(repo)
			_, err := svc.ReplaceChain(context.Background(), grow(fixedDifficulty{4}, local[:2], nil, nil))
			Expect(err).NotTo(HaveOccurred())
			Expect(mustAll(repo)).To(HaveLen(4))
		})
	})

	Describe("concurrent with Mine", func() {
		It("ends with a linked chain and a UTXO set equal to its replay", func() {
			repo, utxos := memory.New(), memory.NewUTXOSet()
			eng := pow.New(1)
			svc, f := funded(repo, eng, WithUTXOSet(utxos))
			base := mustAll(repo)

			// Candidates of growing length over the genesis, each spending its own outputs.
			candidates := make([][]*domain.Block, 4)
			for i := range candidates {
				var txss [][]domain.Transaction
				for j := 0; j <= i+1; j++ {
					txss = append(txss, []domain.Transaction{spend(f.owner, f.ops[j], uint64(9-j%3))})
				}
				candidates[i] = grow(eng, base, txss...)
			}

			// A bounded turn turns a lost release into a failure instead of a hang.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var wg sync.WaitGroup
			for _, cand := range candidates {
				cand := cand
				wg.Add(1)
				go func() {
					defer GinkgoRecover()
					defer wg.Done()
					_, err := svc.ReplaceChain(ctx, cand)
					if err != nil {
						Expect(errors.Is(err, ErrInsufficientWork) || errors.Is(err, domain.ErrInputMissingOrSpent)).To(BeTrue(), "%v", err)
					}
				}()
			}
			for k := 0; k < 4; k++ {
				k := k
				wg.Add(1)
				go func() {
					defer GinkgoRecover()
					defer wg.Done()
					_, err := svc.Mine(ctx, []domain.Transaction{spend(f.owner, f.ops[6+k], 9)})
					if err != nil {
						Expect(errors.Is(err, domain.ErrInputMissingOrSpent)).To(BeTrue(), "%v", err)
					}
				}()
			}
			wg.Wait()

			blocks := mustAll(repo)
			for i := 1; i < len(blocks); i++ {
				Expect(blocks[i].Index).To(Equal(i))
				Expect(blocks[i].PrevHash).To(Equal(blocks[i-1].Hash))
				Expect(eng.Verify(blocks[:i], blocks[i])).To(Succeed())
			}
			Expect(utxos.Snapshot()).To(Equal(replay(blocks)))
		})
	})
})

func mustAll(repo storage.BlockRepository) []*domain.Block {
	blocks, err := repo.All()
	Expect(err).NotTo(HaveOccurred())
	return blocks
}

func cloneBlocks(blocks []*domain.Block) []*domain.Block {
	return append([]*domain.Block(nil), blocks...)
}
