package chain_test

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	. "github.com/petersonsalme/blockchain-with-go/internal/chain"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/mempool"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
	"github.com/petersonsalme/blockchain-with-go/pkg/wallet"
)

var _ = Describe("Service with a miner address", func() {
	for _, be := range backends {
		be := be
		Describe(be.name, func() {
			var (
				repo  storage.IndexedRepository
				svc   *Service
				f     *funding
				miner *wallet.Wallet
			)

			BeforeEach(func() {
				newRepo = be.open
				repo = newRepo()
				var err error
				miner, err = wallet.New()
				Expect(err).NotTo(HaveOccurred())
				svc, f = funded(repo, engineCases[0].new(), WithMinerAddress(miner.Address()))
			})

			// expectCoinbase checks that b opens with the only input-less transaction,
			// paying exactly want to the miner.
			expectCoinbase := func(b *domain.Block, want uint64) {
				Expect(b.Transactions).NotTo(BeEmpty())
				cb := b.Transactions[0]
				Expect(cb.Inputs).To(BeEmpty())
				Expect(cb.Outputs).To(Equal([]domain.TxOut{{Amount: want, Address: miner.Address()}}))
				Expect(cb.ID).To(Equal(cb.CalculateHash()))
				for _, tx := range b.Transactions[1:] {
					Expect(tx.Inputs).NotTo(BeEmpty())
				}
			}

			It("MinePending pays Subsidy to the miner in an empty block", func() {
				b, err := svc.MinePending(context.Background())
				Expect(err).NotTo(HaveOccurred())
				Expect(b.Transactions).To(HaveLen(1))
				expectCoinbase(b, domain.Subsidy(b.Index))
				balance, utxos := svc.Balance(miner.Address())
				Expect(balance).To(Equal(domain.Subsidy(b.Index)))
				Expect(utxos).To(HaveLen(1))
			})

			It("Mine pays Subsidy plus the fees and Balance grows by exactly that", func() {
				before, _ := svc.Balance(miner.Address())
				// Fees: 10-9 = 1 and 10-7 = 3.
				txs := []domain.Transaction{spend(f.owner, f.ops[0], 9), spend(f.owner, f.ops[1], 7)}

				b, err := svc.Mine(context.Background(), txs)

				Expect(err).NotTo(HaveOccurred())
				Expect(b.Transactions).To(HaveLen(3))
				expectCoinbase(b, domain.Subsidy(b.Index)+4)
				after, _ := svc.Balance(miner.Address())
				Expect(after - before).To(Equal(domain.Subsidy(b.Index) + 4))
			})

			It("counts the fee of a transaction that spends another one of the same block", func() {
				first := spend(f.owner, f.ops[0], 9) // fee 1, pays 9 to recipient
				second := spend(recipient(), outpointOf(first, 0), 6)

				b, err := svc.Mine(context.Background(), []domain.Transaction{first, second})

				Expect(err).NotTo(HaveOccurred())
				expectCoinbase(b, domain.Subsidy(b.Index)+1+3)
			})

			It("keeps the repository UTXO equal to the replay of the chain and reindexes to the same set", func() {
				_, err := svc.Mine(context.Background(), f.tx())
				Expect(err).NotTo(HaveOccurred())
				_, err = svc.MinePending(context.Background())
				Expect(err).NotTo(HaveOccurred())

				blocks, _ := repo.All()
				Expect(utxoSnapshot(repo)).To(Equal(replay(blocks)))
				Expect(repo.Reindex()).To(Succeed())
				Expect(utxoSnapshot(repo)).To(Equal(replay(blocks)))
			})

			It("another node accepts the mined chain through ReplaceChain", func() {
				_, err := svc.Mine(context.Background(), f.tx())
				Expect(err).NotTo(HaveOccurred())
				_, err = svc.MinePending(context.Background())
				Expect(err).NotTo(HaveOccurred())
				blocks, _ := repo.All()

				other := be.open()
				delta, err := domain.BlockDelta(other, blocks[0], true)
				Expect(err).NotTo(HaveOccurred())
				Expect(other.Commit(blocks[0], delta)).To(Succeed())
				osvc, err := NewService(other, engineCases[0].new())
				Expect(err).NotTo(HaveOccurred())

				_, err = osvc.ReplaceChain(context.Background(), blocks)

				Expect(err).NotTo(HaveOccurred())
				got, _ := osvc.Balance(miner.Address())
				want, _ := svc.Balance(miner.Address())
				Expect(got).To(Equal(want))
			})

			It("Mine still rejects a caller's input-less transaction", func() {
				_, err := svc.Mine(context.Background(), []domain.Transaction{coinbaseTx(1)})
				Expect(err).To(MatchError(domain.ErrMissingInputs))
			})

			It("rejects the block when the miner address is invalid and changes nothing", func() {
				bad, _ := funded(be.open(), engineCases[0].new(), WithMinerAddress("not-an-address"))
				_, err := bad.MinePending(context.Background())
				Expect(err).To(MatchError(domain.ErrInvalidOutput))
				tip, _ := bad.Tip()
				Expect(tip.Index).To(Equal(0))
			})

			It("seals within MAX_BLOCK_BYTES counting the coinbase", func() {
				c := engineCases[0].new()
				base, _ := funded(be.open(), c, WithMinerAddress(miner.Address()))
				empty, err := base.MinePending(context.Background())
				Expect(err).NotTo(HaveOccurred())
				emptySize, err := empty.Size()
				Expect(err).NotTo(HaveOccurred())

				txSize := mempool.Size(spend(f.owner, f.ops[0], 9))

				included := 0
				for k := 0; k <= 18; k++ {
					limit := emptySize + 100 + k*txSize/3
					GinkgoT().Setenv(domain.MaxBlockBytesEnvVar, strconv.Itoa(limit))
					psvc, pf := funded(be.open(), c, WithMinerAddress(miner.Address()))
					for i, out := range []uint64{9, 8, 7, 6, 5} {
						Expect(psvc.Submit(spend(pf.owner, pf.ops[i], out))).To(Succeed())
					}

					blk, err := psvc.MinePending(context.Background())

					Expect(err).NotTo(HaveOccurred(), "limit %d", limit)
					Expect(domain.ValidateSize(blk, domain.MaxBlockBytes())).To(Succeed(), "limit %d", limit)
					included = max(included, len(blk.Transactions)-1)
				}
				Expect(included).To(BeNumerically(">=", 3))
			})

			DescribeTable("fits exactly the transactions whose widest sealed block, coinbase included, is the limit",
				func(count, slack int) {
					for i, out := range []uint64{9, 8, 7, 6, 5} {
						Expect(svc.Submit(spend(f.owner, f.ops[i], out))).To(Succeed())
					}
					pending := svc.Pending()
					zeros := strings.Repeat("0", 64)
					cb := domain.Transaction{
						ID:        zeros,
						Outputs:   []domain.TxOut{{Amount: math.MaxUint64, Address: miner.Address()}},
						Timestamp: time.Date(2000, 1, 1, 0, 0, 0, 123456789, time.FixedZone("", 3600)),
					}
					b := domain.Block{
						Index: math.MaxInt, Timestamp: time.Date(2000, 1, 1, 0, 0, 0, 123456789, time.FixedZone("", 3600)),
						Transactions: append([]domain.Transaction{cb}, pending[:count]...),
						MerkleRoot:   zeros, Hash: zeros, PrevHash: zeros,
						Nonce: math.MaxInt, Difficulty: math.MaxInt, Validator: zeros, Signature: strings.Repeat("0", 128),
					}
					widest, err := b.Size()
					Expect(err).NotTo(HaveOccurred())
					GinkgoT().Setenv(domain.MaxBlockBytesEnvVar, strconv.Itoa(widest+slack))

					blk, err := svc.MinePending(context.Background())

					Expect(err).NotTo(HaveOccurred())
					if slack < 0 {
						// A smaller transaction further down may still take the room left.
						Expect(ids(blk.Transactions)).To(ContainElement(pending[0].ID))
						Expect(ids(blk.Transactions)).NotTo(ContainElement(pending[1].ID))
					} else {
						Expect(ids(blk.Transactions[1:])).To(Equal(ids(pending[:count])))
					}
					Expect(domain.ValidateSize(blk, domain.MaxBlockBytes())).To(Succeed())
				},
				Entry("two transactions, exact", 2, 0),
				Entry("one transaction, exact", 1, 0),
				Entry("one byte short of two", 2, -1),
			)

			It("without the option blocks carry no coinbase", func() {
				plain, _ := funded(be.open(), engineCases[0].new())
				b, err := plain.MinePending(context.Background())
				Expect(err).NotTo(HaveOccurred())
				Expect(b.Transactions).To(BeEmpty())
			})
		})
	}
})
