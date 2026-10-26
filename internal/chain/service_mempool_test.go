package chain_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	. "github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/poa"
	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/mempool"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

var _ = Describe("Service mempool", func() {
	var (
		repo *memory.Repository
		pool *mempool.Pool
		svc  *Service
		f    *funding
	)

	ids := func(txs []domain.Transaction) []string {
		out := make([]string, 0, len(txs))
		for _, tx := range txs {
			out = append(out, tx.ID)
		}

		return out
	}
	spendOp := func(i int, out uint64) domain.Transaction { return spend(f.owner, f.ops[i], out) }
	chainLen := func() int {
		all, err := repo.All()
		Expect(err).NotTo(HaveOccurred())

		return len(all)
	}

	BeforeEach(func() {
		repo = memory.New()
		pool = mempool.New(0)
		svc, f = funded(repo, pow.NewProofOfWork(1), WithMempool(pool))
	})

	Describe("Submit", func() {
		It("admits a valid transaction with fee = inputs - outputs", func() {
			cheap, rich := spendOp(0, 9), spendOp(1, 4)
			Expect(svc.Submit(cheap)).To(Succeed())
			Expect(svc.Submit(rich)).To(Succeed())
			// Equal sizes: the larger fee (6 against 1) ranks first.
			Expect(ids(svc.Pending())).To(Equal([]string{rich.ID, cheap.ID}))
		})

		It("ranks a transaction by fee = sum of inputs - sum of outputs", func() {
			multi := domain.Transaction{
				Inputs: []domain.TxIn{
					{PrevTxID: f.ops[0].TxID, OutIndex: f.ops[0].Index},
					{PrevTxID: f.ops[1].TxID, OutIndex: f.ops[1].Index},
				},
				Outputs: []domain.TxOut{
					{Amount: 6, Address: recipient().Address()},
					{Amount: 6, Address: recipient().Address()},
				},
				Timestamp: time.Now(),
			}
			multi.ID = multi.CalculateHash()
			Expect(domain.SignInput(&multi, 0, f.owner)).To(Succeed())
			Expect(domain.SignInput(&multi, 1, f.owner)).To(Succeed())

			single := spendOp(2, 4) // fee 6
			// multi has fee 8 over a larger size: it ranks below single, but a fee
			// of 14 (outputs counted once) or a wrapped one would rank it first.
			sm, ss := mempool.Size(multi), mempool.Size(single)
			Expect(8 * ss).To(BeNumerically("<", 6*sm))
			Expect(14 * ss).To(BeNumerically(">", 6*sm))

			Expect(svc.Submit(multi)).To(Succeed())
			Expect(svc.Submit(single)).To(Succeed())
			Expect(ids(svc.Pending())).To(Equal([]string{single.ID, multi.ID}))
		})

		It("builds a default pool without the option", func() {
			bare, bf := funded(memory.New(), pow.NewProofOfWork(1))
			Expect(bare.Submit(bf.tx()[0])).To(Succeed())
			Expect(bare.Pending()).To(HaveLen(1))
		})

		DescribeTable("rejects what Mine rejects, leaving the pool untouched",
			func(build func() (domain.Transaction, error)) {
				tx, want := build()
				err := svc.Submit(tx)
				Expect(errors.Is(err, want)).To(BeTrue(), "got %v", err)
				Expect(svc.Pending()).To(BeEmpty())
				Expect(pool.Len()).To(BeZero())
			},
			Entry("unknown input", func() (domain.Transaction, error) {
				return spend(f.owner, domain.OutPoint{TxID: "nope"}, 1), domain.ErrInputMissingOrSpent
			}),
			Entry("outputs above inputs", func() (domain.Transaction, error) {
				return spend(f.owner, f.ops[0], 11), domain.ErrInsufficientInputs
			}),
			Entry("no outputs", func() (domain.Transaction, error) {
				tx := spend(f.owner, f.ops[0], 9)
				tx.Outputs = nil

				return tx, domain.ErrInvalidOutput
			}),
			Entry("a signature from another key", func() (domain.Transaction, error) {
				stranger, err := wallet.New()
				Expect(err).NotTo(HaveOccurred())

				return spend(stranger, f.ops[0], 9), domain.ErrInputOwnerMismatch
			}),
		)

		It("rejects an input created by another pool transaction", func() {
			parent := spendOp(0, 9)
			Expect(svc.Submit(parent)).To(Succeed())
			child := spend(f.owner, domain.OutPoint{TxID: parent.CalculateHash(), Index: 0}, 1)
			Expect(errors.Is(svc.Submit(child), domain.ErrInputMissingOrSpent)).To(BeTrue())
			Expect(ids(svc.Pending())).To(Equal([]string{parent.ID}))
		})

		It("surfaces the pool's duplicate and conflict errors", func() {
			a := spendOp(0, 9)
			Expect(svc.Submit(a)).To(Succeed())
			Expect(svc.Submit(a)).To(MatchError(mempool.ErrDuplicate))
			Expect(svc.Submit(spendOp(0, 8))).To(MatchError(mempool.ErrConflict))
			Expect(svc.Pending()).To(HaveLen(1))
		})
	})

	Describe("MinePending", func() {
		It("mines an empty block from an empty pool", func() {
			blk, err := minePending(svc)
			Expect(err).NotTo(HaveOccurred())
			Expect(blk.Transactions).To(BeEmpty())
			Expect(blk.Transactions).NotTo(BeNil())
			Expect(chainLen()).To(Equal(2))
		})

		It("mines the pool in selection order and empties it", func() {
			for i, out := range []uint64{9, 3, 6} {
				Expect(svc.Submit(spendOp(i, out))).To(Succeed())
			}

			order := ids(svc.Pending())
			blk, err := minePending(svc)
			Expect(err).NotTo(HaveOccurred())
			Expect(ids(blk.Transactions)).To(Equal(order))
			Expect(svc.Pending()).To(BeEmpty())

			for _, tx := range blk.Transactions {
				_, found := repo.Lookup(domain.OutPoint{TxID: tx.CalculateHash(), Index: 0})
				Expect(found).To(BeTrue())
			}
		})

		It("honours a cancelled context and leaves the pool alone", func() {
			Expect(svc.Submit(spendOp(0, 9))).To(Succeed())

			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			_, err := svc.MinePending(ctx)
			Expect(err).To(MatchError(context.Canceled))
			Expect(svc.Pending()).To(HaveLen(1))
			Expect(chainLen()).To(Equal(1))
		})

		It("refuses to write after the state diverged", func() {
			failing := &failingWrites{IndexedRepository: memory.New()}
			dsvc, df := funded(failing, pow.NewProofOfWork(1))
			failing.commitErr = errors.New("boom")
			_, err := dsvc.Mine(context.Background(), df.tx())
			Expect(err).To(MatchError(ErrStateDiverged))
			Expect(dsvc.Submit(spendOp(0, 9))).NotTo(Succeed())
			_, err = minePending(dsvc)
			Expect(err).To(MatchError(ErrStateDiverged))
		})

		It("keeps the pool when the block is rejected", func() {
			rsvc, rf := funded(memory.New(), pow.NewProofOfWork(1), WithMempool(pool),
				WithGenerator(func(context.Context, consensus.Consensus, []*domain.Block, []domain.Transaction) (*domain.Block, error) {
					return nil, nil
				}))
			Expect(rsvc.Submit(rf.tx()[0])).To(Succeed())
			before := ids(rsvc.Pending())

			_, err := minePending(rsvc)
			Expect(err).To(MatchError(ErrInvalidBlock))
			Expect(ids(rsvc.Pending())).To(Equal(before))

			_, err = rsvc.Mine(context.Background(), rf.tx())
			Expect(err).To(MatchError(ErrInvalidBlock))
			Expect(ids(rsvc.Pending())).To(Equal(before))
		})

		It("surfaces a repository failure without touching the pool", func() {
			rr := &allFailingRepo{IndexedRepository: memory.New()}
			rsvc, rf := funded(rr, pow.NewProofOfWork(1), WithMempool(pool))
			Expect(rsvc.Submit(rf.tx()[0])).To(Succeed())
			rr.armed.Store(true)

			_, err := minePending(rsvc)
			Expect(err).To(MatchError("all failed"))
			Expect(rsvc.Pending()).To(HaveLen(1))
		})
	})

	Describe("conflicts", func() {
		It("drops from the pool a transaction a Mine block conflicts with", func() {
			pooled := spendOp(0, 9)
			Expect(svc.Submit(pooled)).To(Succeed())

			survivor := spendOp(1, 9)
			Expect(svc.Submit(survivor)).To(Succeed())

			_, err := svc.Mine(context.Background(), []domain.Transaction{spendOp(0, 8)})
			Expect(err).NotTo(HaveOccurred())
			Expect(ids(svc.Pending())).To(Equal([]string{survivor.ID}))

			blk, err := minePending(svc)
			Expect(err).NotTo(HaveOccurred())
			Expect(ids(blk.Transactions)).To(Equal([]string{survivor.ID}))
		})

		It("drops from the pool a transaction a Mine block includes", func() {
			pooled := spendOp(0, 9)
			Expect(svc.Submit(pooled)).To(Succeed())
			_, err := svc.Mine(context.Background(), []domain.Transaction{pooled})
			Expect(err).NotTo(HaveOccurred())
			Expect(svc.Pending()).To(BeEmpty())
		})

		It("leaves no pool transaction spending outside the UTXO set", func() {
			for i := range 6 {
				Expect(svc.Submit(spendOp(i, uint64(9-i)))).To(Succeed())
			}

			_, err := svc.Mine(context.Background(), []domain.Transaction{spendOp(1, 5), spendOp(3, 5)})
			Expect(err).NotTo(HaveOccurred())
			Expect(svc.Pending()).To(HaveLen(4))

			for _, tx := range svc.Pending() {
				for _, in := range tx.Inputs {
					_, ok := repo.Lookup(domain.OutPoint{TxID: in.PrevTxID, Index: in.OutIndex})
					Expect(ok).To(BeTrue())
				}
			}
		})
	})

	Describe("stale pool entries", func() {
		It("discards a transaction whose input a block spent after Submit validated it", func() {
			stale := spendOp(0, 9)
			Expect(svc.Submit(stale)).To(Succeed())
			_, err := svc.Mine(context.Background(), []domain.Transaction{spendOp(0, 8)})
			Expect(err).NotTo(HaveOccurred())
			// The late half of a Submit that validated before that Mine.
			Expect(pool.Add(stale, 1)).To(Succeed())

			good := spendOp(1, 9)
			Expect(svc.Submit(good)).To(Succeed())

			blk, err := minePending(svc)
			Expect(err).NotTo(HaveOccurred())
			Expect(ids(blk.Transactions)).To(Equal([]string{good.ID}))
			Expect(svc.Pending()).To(BeEmpty())
		})

		It("discards an entry with a bad signature", func() {
			forged := spendOp(0, 9)
			forged.Outputs[0].Amount = 8 // signed over 9
			Expect(pool.Add(forged, 2)).To(Succeed())

			blk, err := minePending(svc)
			Expect(err).NotTo(HaveOccurred())
			Expect(blk.Transactions).To(BeEmpty())
			Expect(svc.Pending()).To(BeEmpty())
		})

		It("never fails MinePending whatever the interleaving of Submit, Mine and MinePending", func() {
			for range 25 {
				rp := mempool.New(0)
				rsvc, rf := funded(memory.New(), pow.NewProofOfWork(1), WithMempool(rp))
				a, b := spend(rf.owner, rf.ops[0], 9), spend(rf.owner, rf.ops[0], 8)
				other := spend(rf.owner, rf.ops[1], 9)
				Expect(rsvc.Submit(other)).To(Succeed())

				var wg sync.WaitGroup

				errs := make(chan error, 2)

				wg.Add(3)
				go func() { defer wg.Done(); _ = rsvc.Submit(a) }()
				go func() { defer wg.Done(); _, _ = rsvc.Mine(context.Background(), []domain.Transaction{b}) }()
				go func() {
					defer wg.Done()

					_, err := minePending(rsvc)
					errs <- err
				}()

				wg.Wait()
				Expect(<-errs).NotTo(HaveOccurred())

				_, err := minePending(rsvc)
				Expect(err).NotTo(HaveOccurred())
				Expect(rsvc.Pending()).To(BeEmpty())
			}
		})
	})

	Describe("block size", func() {
		engines := map[string]func() consensus.Consensus{
			"pow": func() consensus.Consensus { return pow.NewProofOfWork(1) },
			"poa": func() consensus.Consensus {
				w, err := wallet.New()
				Expect(err).NotTo(HaveOccurred())
				c, err := poa.New([]ed25519.PublicKey{w.PublicKey()}, w)
				Expect(err).NotTo(HaveOccurred())

				return c
			},
		}
		for name, mk := range engines {
			It("seals a block within MAX_BLOCK_BYTES for any pool content with "+name, func() {
				c := mk()
				base, bf := funded(memory.New(), c)
				empty, err := minePending(base)
				Expect(err).NotTo(HaveOccurred())
				emptySize, err := empty.Size()
				Expect(err).NotTo(HaveOccurred())

				txSize := mempool.Size(bf.tx()[0])

				included := 0

				for k := range 19 {
					limit := emptySize + 8 + k*txSize/3
					GinkgoT().Setenv(domain.MaxBlockBytesEnvVar, strconv.Itoa(limit))

					psvc, pf := funded(memory.New(), c)
					for i, out := range []uint64{9, 8, 7, 6, 5} {
						Expect(psvc.Submit(spend(pf.owner, pf.ops[i], out))).To(Succeed())
					}

					order := ids(psvc.Pending())

					blk, err := minePending(psvc)
					Expect(err).NotTo(HaveOccurred(), "limit %d", limit)
					Expect(domain.ValidateSize(blk, domain.MaxBlockBytes())).To(Succeed(), "limit %d", limit)

					var want []string

					in := map[string]bool{}
					for _, id := range ids(blk.Transactions) {
						in[id] = true
					}

					for _, id := range order {
						if in[id] {
							want = append(want, id)
						}
					}

					Expect(ids(blk.Transactions)).To(Equal(append([]string{}, want...)))
					Expect(psvc.Pending()).To(HaveLen(len(order) - len(blk.Transactions)))
					included = max(included, len(blk.Transactions))
				}

				Expect(included).To(BeNumerically(">=", 3))
			})
		}

		DescribeTable("fits exactly the transactions whose widest sealed block is the limit",
			func(count, slack int) {
				for i, out := range []uint64{9, 8, 7, 6, 5} {
					Expect(svc.Submit(spendOp(i, out))).To(Succeed())
				}

				pending := svc.Pending()
				// Mirrors the widest block the service reserves room for.
				widest := func(txs []domain.Transaction) int {
					zeros := strings.Repeat("0", 64)
					b := domain.Block{
						Index: math.MaxInt, Timestamp: time.Date(2000, 1, 1, 0, 0, 0, 123456789, time.FixedZone("", 3600)),
						Transactions: txs, MerkleRoot: zeros, Hash: zeros, PrevHash: zeros,
						Nonce: math.MaxInt, Difficulty: math.MaxInt, Validator: zeros, Signature: strings.Repeat("0", 128),
					}
					n, err := b.Size()
					Expect(err).NotTo(HaveOccurred())

					return n
				}
				GinkgoT().Setenv(domain.MaxBlockBytesEnvVar, strconv.Itoa(widest(pending[:count])+slack))

				blk, err := minePending(svc)
				Expect(err).NotTo(HaveOccurred())

				if slack < 0 {
					// A smaller transaction further down may still take the room left.
					Expect(ids(blk.Transactions)).To(ContainElement(pending[0].ID))
					Expect(ids(blk.Transactions)).NotTo(ContainElement(pending[1].ID))
				} else {
					Expect(ids(blk.Transactions)).To(Equal(ids(pending[:count])))
				}

				Expect(svc.Pending()).To(HaveLen(5 - len(blk.Transactions)))
			},
			Entry("two transactions", 2, 0),
			Entry("one byte short of two transactions", 2, -1),
		)

		It("keeps the best transaction when a cheaper one is exactly as large as an empty block", func() {
			best := spendOp(0, 4)
			// Neither Submit nor Mine checks the ID against the content, so pad it
			// until the transaction is as large as an empty sealed block: then both
			// fit a budget equal to the limit.
			empty, err := func() (int, error) {
				zeros := strings.Repeat("0", 64)
				b := domain.Block{
					Index: math.MaxInt, Timestamp: time.Date(2000, 1, 1, 0, 0, 0, 123456789, time.FixedZone("", 3600)),
					Transactions: []domain.Transaction{}, MerkleRoot: zeros, Hash: zeros, PrevHash: zeros,
					Nonce: math.MaxInt, Difficulty: math.MaxInt, Validator: zeros, Signature: strings.Repeat("0", 128),
				}

				return b.Size()
			}()
			Expect(err).NotTo(HaveOccurred())

			stamp := time.Date(2000, 1, 1, 0, 0, 0, 123456789, time.UTC)
			build := func(pad int) domain.Transaction {
				tx := domain.Transaction{
					Inputs:    []domain.TxIn{{PrevTxID: f.ops[1].TxID, OutIndex: f.ops[1].Index}},
					Outputs:   []domain.TxOut{{Amount: 9, Address: recipient().Address()}},
					Timestamp: stamp, // fixed: time.Now() trims trailing zeros, so its size varies
				}
				tx.ID = tx.CalculateHash()
				Expect(domain.SignInput(&tx, 0, f.owner)).To(Succeed())
				tx.ID += strings.Repeat("x", pad)

				return tx
			}
			cheap := build(0)
			cheap = build(empty - mempool.Size(cheap))
			Expect(mempool.Size(cheap)).To(Equal(empty))

			Expect(svc.Submit(best)).To(Succeed())
			Expect(svc.Submit(cheap)).To(Succeed())
			Expect(ids(svc.Pending())).To(Equal([]string{best.ID, cheap.ID}))
			GinkgoT().Setenv(domain.MaxBlockBytesEnvVar, strconv.Itoa(empty+mempool.Size(best)))

			blk, err := minePending(svc)
			Expect(err).NotTo(HaveOccurred())
			Expect(ids(blk.Transactions)).To(Equal([]string{best.ID}))
			Expect(ids(svc.Pending())).To(Equal([]string{cheap.ID}))
		})

		It("refills the room the separators leave after an oversized first pick", func() {
			empty, err := func() (int, error) {
				zeros := strings.Repeat("0", 64)
				b := domain.Block{
					Index: math.MaxInt, Timestamp: time.Date(2000, 1, 1, 0, 0, 0, 123456789, time.FixedZone("", 3600)),
					Transactions: []domain.Transaction{}, MerkleRoot: zeros, Hash: zeros, PrevHash: zeros,
					Nonce: math.MaxInt, Difficulty: math.MaxInt, Validator: zeros, Signature: strings.Repeat("0", 128),
				}

				return b.Size()
			}()
			Expect(err).NotTo(HaveOccurred())

			stamp := time.Date(2000, 1, 1, 0, 0, 0, 123456789, time.UTC)
			build := func(op int, out uint64, pad int) domain.Transaction {
				tx := domain.Transaction{
					Inputs:    []domain.TxIn{{PrevTxID: f.ops[op].TxID, OutIndex: f.ops[op].Index}},
					Outputs:   []domain.TxOut{{Amount: out, Address: recipient().Address()}},
					Timestamp: stamp,
				}
				tx.ID = tx.CalculateHash()
				Expect(domain.SignInput(&tx, 0, f.owner)).To(Succeed())
				tx.ID += strings.Repeat("x", pad)

				return tx
			}
			// Sizes s, s+1 and s: best and mid fill the room exactly, so their
			// block is one byte too large (the separator); the next pass must give
			// the pool room-1, which takes best and last but skips mid.
			best, mid, last := build(0, 4, 0), build(1, 8, 1), build(2, 9, 0)
			Expect(mempool.Size(mid)).To(Equal(mempool.Size(best) + 1))
			Expect(mempool.Size(last)).To(Equal(mempool.Size(best)))

			for _, tx := range []domain.Transaction{best, mid, last} {
				Expect(svc.Submit(tx)).To(Succeed())
			}

			Expect(ids(svc.Pending())).To(Equal([]string{best.ID, mid.ID, last.ID}))
			GinkgoT().Setenv(domain.MaxBlockBytesEnvVar, strconv.Itoa(empty+2*mempool.Size(best)+1))

			blk, err := minePending(svc)
			Expect(err).NotTo(HaveOccurred())
			Expect(ids(blk.Transactions)).To(Equal([]string{best.ID, last.ID}))
		})

		It("fails instead of looping when even an empty block is too large", func() {
			GinkgoT().Setenv(domain.MaxBlockBytesEnvVar, "10")
			Expect(svc.Submit(spendOp(0, 9))).To(Succeed())

			done := make(chan error, 1)

			go func() { _, err := svc.MinePending(context.Background()); done <- err }()

			var err error
			Eventually(done, 2*time.Second).Should(Receive(&err))

			var verr *domain.ValidationError
			Expect(errors.As(err, &verr)).To(BeTrue())
			Expect(verr.Code).To(Equal(domain.CodeBlockTooLarge))
			Expect(svc.Pending()).To(HaveLen(1))
		})
	})

	Describe("reads while a block is being mined", func() {
		It("does not make Pending or Submit wait for Mine", func() {
			entered, release := make(chan struct{}), make(chan struct{})

			var once sync.Once

			gate := func(ctx context.Context, c consensus.Consensus, h []*domain.Block, txs []domain.Transaction) (*domain.Block, error) {
				once.Do(func() { close(entered); <-release })
				return GenerateBlockWithContext(ctx, c, h, txs)
			}
			gsvc, gf := funded(memory.New(), pow.NewProofOfWork(1), WithGenerator(gate))
			done := make(chan struct{})

			go func() {
				defer GinkgoRecover()
				defer close(done)

				_, err := minePending(gsvc)
				Expect(err).NotTo(HaveOccurred())
			}()

			Eventually(entered).Should(BeClosed())

			read := make(chan int, 1)

			go func() {
				defer GinkgoRecover()

				Expect(gsvc.Submit(gf.tx()[0])).To(Succeed())

				read <- len(gsvc.Pending())
			}()

			Eventually(read, time.Second).Should(Receive(Equal(1)))
			close(release)
			Eventually(done).Should(BeClosed())
			// The tx submitted during sealing is not in that block and stays pooled.
			Expect(gsvc.Pending()).To(HaveLen(1))
		})
	})
})
