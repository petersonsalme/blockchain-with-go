package chain_test

import (
	"context"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	. "github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/mempool"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
	"github.com/petersonsalme/blockchain-with-go/internal/vm"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

// onceCode sets storage[0] to 1, and reverts when it is already set:
// PUSH8 0, SLOAD, ISZERO, PUSH8 22, JUMPI, REVERT, PUSH8 1, PUSH8 0, SSTORE, STOP.
var onceCode = []byte{
	vm.OpPUSH8, 0, 0, 0, 0, 0, 0, 0, 0,
	vm.OpSLOAD,
	vm.OpISZERO,
	vm.OpPUSH8, 0, 0, 0, 0, 0, 0, 0, 22,
	vm.OpJUMPI,
	vm.OpREVERT,
	vm.OpPUSH8, 0, 0, 0, 0, 0, 0, 0, 1,
	vm.OpPUSH8, 0, 0, 0, 0, 0, 0, 0, 0,
	vm.OpSSTORE,
	vm.OpSTOP,
}

// withContract spends op like spend and attaches the contract operation.
func withContract(w *wallet.Wallet, op domain.OutPoint, out uint64, cop *domain.ContractOp) domain.Transaction {
	tx := domain.Transaction{
		Inputs:    []domain.TxIn{{PrevTxID: op.TxID, OutIndex: op.Index}},
		Outputs:   []domain.TxOut{{Amount: out, Address: recipient().Address()}},
		Timestamp: time.Now(),
		Contract:  cop,
	}
	tx.ID = tx.CalculateHash()
	Expect(domain.SignInput(&tx, 0, w)).To(Succeed())

	return tx
}

func deployOp(code []byte) *domain.ContractOp {
	return &domain.ContractOp{Kind: domain.ContractDeploy, Code: hex.EncodeToString(code), GasLimit: 100}
}

func callOp(target string, gas uint64) *domain.ContractOp {
	return &domain.ContractOp{Kind: domain.ContractCall, Target: target, GasLimit: gas}
}

// poisonedAll makes All return a fixed chain once set.
type poisonedAll struct {
	storage.IndexedRepository
	chain []*domain.Block
}

func (p *poisonedAll) All() ([]*domain.Block, error) {
	if p.chain != nil {
		return p.chain, nil
	}

	return p.IndexedRepository.All()
}

// ForkState follows the poisoned chain, so the fork base matches what All returns.
func (p *poisonedAll) ForkState(height int) (domain.UTXOSet, error) {
	if p.chain != nil {
		return domain.BuildUTXOSet(p.chain[:height])
	}

	return p.IndexedRepository.ForkState(height)
}

var _ = Describe("Service contracts", func() {
	ctx := context.Background()
	missing := hex.EncodeToString(make([]byte, 32))

	for _, be := range backends {
		be := be
		Describe(be.name, func() {
			var (
				repo storage.IndexedRepository
				pool *mempool.Pool
				svc  *Service
				f    *funding
			)

			next := func() domain.OutPoint {
				op := f.ops[f.next]
				f.next++

				return op
			}
			deploy := func(code []byte) string {
				tx := withContract(f.owner, next(), 9, deployOp(code))
				_, err := svc.Mine(ctx, []domain.Transaction{tx})
				Expect(err).NotTo(HaveOccurred())

				return tx.CalculateHash()
			}
			call := func(id string, gas uint64) domain.Transaction {
				return withContract(f.owner, next(), 9, callOp(id, gas))
			}
			counter := func(id string) uint64 {
				_, st, ok := svc.Contract(id)
				Expect(ok).To(BeTrue())

				return st[0]
			}
			chainHashes := func() []string {
				all, err := repo.All()
				Expect(err).NotTo(HaveOccurred())

				return hashes(all)
			}

			BeforeEach(func() {
				newRepo = be.open
				repo = newRepo()
				pool = mempool.New(0)
				svc, f = funded(repo, pow.NewProofOfWork(1), WithMempool(pool))
			})

			Describe("Contract", func() {
				It("reports an unknown contract", func() {
					code, st, ok := svc.Contract(missing)
					Expect(ok).To(BeFalse())
					Expect(code).To(BeNil())
					Expect(st).To(BeNil())
				})

				It("returns copies", func() {
					id := deploy(vm.CounterCode)
					_, err := svc.Mine(ctx, []domain.Transaction{call(id, 100)})
					Expect(err).NotTo(HaveOccurred())

					code, st, ok := svc.Contract(id)
					Expect(ok).To(BeTrue())
					Expect(code).To(Equal(vm.CounterCode))
					code[0] = 0xEE
					st[0] = 99
					st[7] = 7

					code, st, _ = svc.Contract(id)
					Expect(code).To(Equal(vm.CounterCode))
					Expect(st).To(Equal(map[uint64]uint64{0: 1}))
				})

				It("returns an empty, non-nil storage for a fresh contract", func() {
					id := deploy(vm.CounterCode)
					_, st, ok := svc.Contract(id)
					Expect(ok).To(BeTrue())
					Expect(st).NotTo(BeNil())
					Expect(st).To(BeEmpty())
				})

				It("is rebuilt from the chain by NewService", func() {
					id := deploy(vm.CounterCode)
					_, err := svc.Mine(ctx, []domain.Transaction{call(id, 100)})
					Expect(err).NotTo(HaveOccurred())

					again, err := NewService(repo, pow.NewProofOfWork(1))
					Expect(err).NotTo(HaveOccurred())

					_, st, ok := again.Contract(id)
					Expect(ok).To(BeTrue())
					Expect(st).To(Equal(map[uint64]uint64{0: 1}))
				})

				It("fails NewService when the stored chain does not replay", func() {
					bad := withContract(f.owner, next(), 9, callOp(missing, 100))
					chain := grow(pow.NewProofOfWork(1), mustAll(repo), []domain.Transaction{bad})
					_, err := NewService(&poisonedAll{IndexedRepository: repo, chain: chain}, pow.NewProofOfWork(1))
					Expect(err).To(MatchError(vm.ErrUnknownContract))
				})
			})

			Describe("Mine", func() {
				It("counts three calls in consecutive blocks", func() {
					id := deploy(vm.CounterCode)
					for range 3 {
						_, err := svc.Mine(ctx, []domain.Transaction{call(id, 100)})
						Expect(err).NotTo(HaveOccurred())
					}

					Expect(counter(id)).To(Equal(uint64(3)))
				})

				It("sees the writes of earlier transactions of the same block", func() {
					id := deploy(onceCode)
					_, err := svc.Mine(ctx, []domain.Transaction{call(id, 100), call(id, 100)})
					Expect(err).To(MatchError(vm.ErrReverted))
					Expect(counter(id)).To(Equal(uint64(0)))
				})

				It("rejects a failing contract operation and changes nothing", func() {
					id := deploy(vm.CounterCode)
					Expect(svc.Submit(call(id, 100))).To(Succeed())
					pending := svc.Pending()
					before, utxos := chainHashes(), utxoSnapshot(repo)

					for _, tc := range []struct {
						tx  domain.Transaction
						err error
					}{
						{call(missing, 100), vm.ErrUnknownContract},
						{call(id, 10), vm.ErrOutOfGas},
						{withContract(f.owner, next(), 9, &domain.ContractOp{Kind: domain.ContractCall, Target: id}), domain.ErrInvalidContractOp},
					} {
						_, err := svc.Mine(ctx, []domain.Transaction{tc.tx})
						Expect(err).To(MatchError(tc.err))
					}

					Expect(chainHashes()).To(Equal(before))
					Expect(utxoSnapshot(repo)).To(Equal(utxos))
					Expect(svc.Pending()).To(Equal(pending))
					Expect(counter(id)).To(Equal(uint64(0)))
				})

				It("keeps the state when Commit fails", func() {
					failing := &failingWrites{IndexedRepository: newRepo()}
					s, ff := funded(failing, pow.NewProofOfWork(1))
					dep := withContract(ff.owner, ff.ops[0], 9, deployOp(vm.CounterCode))

					failing.commitErr = storage.ErrConflict
					_, err := s.Mine(ctx, []domain.Transaction{dep})
					Expect(err).To(MatchError(storage.ErrConflict))

					_, _, ok := s.Contract(dep.CalculateHash())
					Expect(ok).To(BeFalse())

					failing.commitErr = errors.New("disk")
					_, err = s.Mine(ctx, []domain.Transaction{dep})
					Expect(err).To(MatchError(ErrStateDiverged))

					_, _, ok = s.Contract(dep.CalculateHash())
					Expect(ok).To(BeFalse())
				})
			})

			Describe("Submit", func() {
				It("admits a valid call to a confirmed contract", func() {
					id := deploy(vm.CounterCode)
					Expect(svc.Submit(call(id, 100))).To(Succeed())
					Expect(svc.Pending()).To(HaveLen(1))
				})

				DescribeTable("rejects, leaving the pool empty",
					func(build func(id string) domain.Transaction, want error) {
						id := deploy(vm.CounterCode)
						Expect(svc.Submit(build(id))).To(MatchError(want))
						Expect(svc.Pending()).To(BeEmpty())
					},
					Entry("call to a contract that does not exist", func(string) domain.Transaction { return call(missing, 100) }, vm.ErrUnknownContract),
					Entry("call that runs out of gas", func(id string) domain.Transaction { return call(id, 10) }, vm.ErrOutOfGas),
					Entry("structurally invalid operation", func(id string) domain.Transaction {
						return withContract(f.owner, next(), 9, &domain.ContractOp{Kind: domain.ContractCall, Target: id})
					}, domain.ErrInvalidContractOp),
				)

				It("rejects a call to a contract that is only in the pool", func() {
					dep := withContract(f.owner, next(), 9, deployOp(vm.CounterCode))
					Expect(svc.Submit(dep)).To(Succeed())
					Expect(svc.Submit(call(dep.CalculateHash(), 100))).To(MatchError(vm.ErrUnknownContract))
					Expect(svc.Pending()).To(HaveLen(1))
				})
			})

			Describe("MinePending", func() {
				It("applies pool calls", func() {
					id := deploy(vm.CounterCode)
					Expect(svc.Submit(call(id, 100))).To(Succeed())
					Expect(svc.Submit(call(id, 100))).To(Succeed())
					b, err := svc.MinePending(ctx)
					Expect(err).NotTo(HaveOccurred())
					Expect(b.Transactions).To(HaveLen(2))
					Expect(counter(id)).To(Equal(uint64(2)))
				})

				It("drops a call that fails over the earlier candidates", func() {
					id := deploy(onceCode)
					first, second := call(id, 100), call(id, 100)
					Expect(svc.Submit(first)).To(Succeed())
					Expect(svc.Submit(second)).To(Succeed())
					Expect(svc.Pending()).To(HaveLen(2))
					kept := svc.Pending()[0].ID

					b, err := svc.MinePending(ctx)
					Expect(err).NotTo(HaveOccurred())
					Expect(b.Transactions).To(HaveLen(1))
					Expect(b.Transactions[0].ID).To(Equal(kept))
					Expect(svc.Pending()).To(BeEmpty())
					Expect(counter(id)).To(Equal(uint64(1)))
				})
			})

			Describe("ReplaceChain", func() {
				c := pow.NewProofOfWork(1)

				// fixture: genesis, deploy; local adds two calls, the heavier
				// candidate one call and a plain spend.
				fixture := func() (id string, base []*domain.Block, cand func(gas uint64) []*domain.Block) {
					id = deploy(vm.CounterCode)
					for range 2 {
						_, err := svc.Mine(ctx, []domain.Transaction{call(id, 100)})
						Expect(err).NotTo(HaveOccurred())
					}

					all := mustAll(repo)
					base = all[:2]
					cand = func(gas uint64) []*domain.Block {
						return grow(c, base,
							[]domain.Transaction{call(id, gas)},
							[]domain.Transaction{spend(f.owner, next(), 9)},
							[]domain.Transaction{spend(f.owner, next(), 9)},
						)
					}

					return id, base, cand
				}

				It("takes the contract state of the winning branch", func() {
					id, _, cand := fixture()
					Expect(counter(id)).To(Equal(uint64(2)))

					_, err := svc.ReplaceChain(ctx, cand(100))
					Expect(err).NotTo(HaveOccurred())
					Expect(counter(id)).To(Equal(uint64(1)))
				})

				It("rejects a candidate whose call runs out of gas and changes nothing", func() {
					id, _, cand := fixture()
					before, utxos := chainHashes(), utxoSnapshot(repo)
					_, err := svc.ReplaceChain(ctx, cand(10))
					Expect(err).To(MatchError(vm.ErrOutOfGas))
					Expect(chainHashes()).To(Equal(before))
					Expect(utxoSnapshot(repo)).To(Equal(utxos))
					Expect(counter(id)).To(Equal(uint64(2)))
				})

				It("keeps the state when Reorg fails", func() {
					failing := &failingWrites{IndexedRepository: newRepo()}
					s, ff := funded(failing, c)
					dep := withContract(ff.owner, ff.ops[0], 9, deployOp(vm.CounterCode))
					id := dep.CalculateHash()
					_, err := s.Mine(ctx, []domain.Transaction{dep})
					Expect(err).NotTo(HaveOccurred())
					_, err = s.Mine(ctx, []domain.Transaction{withContract(ff.owner, ff.ops[1], 9, callOp(id, 100))})
					Expect(err).NotTo(HaveOccurred())

					base := mustAll(failing)[:2]
					cand := grow(c, base,
						[]domain.Transaction{spend(ff.owner, ff.ops[2], 9)},
						[]domain.Transaction{spend(ff.owner, ff.ops[3], 9)},
					)
					failing.reorgErr = storage.ErrConflict
					_, err = s.ReplaceChain(ctx, cand)
					Expect(err).To(MatchError(storage.ErrConflict))

					_, st, _ := s.Contract(id)
					Expect(st).To(Equal(map[uint64]uint64{0: 1}))
				})

				It("fails when the shared prefix does not replay", func() {
					bad := withContract(f.owner, next(), 9, callOp(missing, 100))
					poisoned := grow(c, mustAll(repo), []domain.Transaction{bad})
					p := &poisonedAll{IndexedRepository: repo}
					s, err := NewService(p, c)
					Expect(err).NotTo(HaveOccurred())

					p.chain = poisoned
					cand := grow(c, poisoned, []domain.Transaction{spend(f.owner, next(), 9)})
					_, err = s.ReplaceChain(ctx, cand)
					Expect(err).To(MatchError(vm.ErrUnknownContract))
				})
			})

			It("serves Submit and Contract while Mine runs", func() {
				id := deploy(vm.CounterCode)

				var wg sync.WaitGroup

				stop := make(chan struct{})

				for range 4 {
					wg.Add(1)
					go func() {
						defer wg.Done()
						defer GinkgoRecover()

						for {
							select {
							case <-stop:
								return
							default:
							}

							_, _, _ = svc.Contract(id)
							_ = svc.Submit(withContract(f.owner, f.ops[fundedOutputs-1], 9, callOp(id, 100)))
						}
					}()
				}

				for range 5 {
					_, err := svc.Mine(ctx, []domain.Transaction{call(id, 100)})
					Expect(err).NotTo(HaveOccurred())
				}

				close(stop)
				wg.Wait()
				Expect(counter(id)).To(Equal(uint64(5)))
			})
		})
	}
})
