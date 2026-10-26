package domain_test

import (
	"math"
	"math/rand"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

var utxoEpoch = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func newTx(n int, ins []domain.OutPoint, outs ...domain.TxOut) domain.Transaction {
	tx := domain.Transaction{
		Outputs:   outs,
		Timestamp: utxoEpoch.Add(time.Duration(n) * time.Second),
	}
	for _, in := range ins {
		tx.Inputs = append(tx.Inputs, domain.TxIn{PrevTxID: in.TxID, OutIndex: in.Index})
	}

	return tx
}

func outpoint(tx domain.Transaction, i uint32) domain.OutPoint {
	return domain.OutPoint{TxID: tx.CalculateHash(), Index: i}
}

func blockOf(index int, txs ...domain.Transaction) *domain.Block {
	return &domain.Block{Index: index, Timestamp: utxoEpoch, Transactions: txs}
}

func clone(s domain.UTXOSet) domain.UTXOSet {
	c := domain.UTXOSet{}
	for k, v := range s {
		c[k] = v
	}

	return c
}

var _ = Describe("UTXO rules", func() {
	var (
		alice, bob string
		genesisTx  domain.Transaction
		genesis    *domain.Block
		set        domain.UTXOSet
		g0         domain.OutPoint
	)

	BeforeEach(func() {
		alice = seededWallet(1).Address()
		bob = seededWallet(100).Address()
		genesisTx = newTx(0, nil, domain.TxOut{Amount: 100, Address: alice}, domain.TxOut{Amount: 50, Address: bob})
		genesis = blockOf(0, genesisTx)

		var err error

		set, err = domain.BuildUTXOSet([]*domain.Block{genesis})
		Expect(err).NotTo(HaveOccurred())

		g0 = outpoint(genesisTx, 0)
	})

	Describe("BlockDelta", func() {
		It("accepts input-less allocations in the genesis block", func() {
			d, err := domain.BlockDelta(domain.UTXOSet{}, genesis, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(d.Spent).To(BeEmpty())
			Expect(d.Created).To(Equal(map[domain.OutPoint]domain.TxOut{
				outpoint(genesisTx, 0): {Amount: 100, Address: alice},
				outpoint(genesisTx, 1): {Amount: 50, Address: bob},
			}))
		})

		It("rejects a nil block", func() {
			_, err := domain.BlockDelta(set, nil, false)
			Expect(err).To(HaveOccurred())
		})

		It("accepts an empty non-genesis block with an empty delta", func() {
			d, err := domain.BlockDelta(set, blockOf(1), false)
			Expect(err).NotTo(HaveOccurred())
			Expect(d.Spent).To(BeEmpty())
			Expect(d.Created).To(BeEmpty())
		})

		It("spends an output and creates new ones", func() {
			tx := newTx(1, []domain.OutPoint{g0}, domain.TxOut{Amount: 60, Address: bob}, domain.TxOut{Amount: 30, Address: alice})
			d, err := domain.BlockDelta(set, blockOf(1, tx), false)
			Expect(err).NotTo(HaveOccurred())
			Expect(d.Spent).To(Equal(map[domain.OutPoint]domain.TxOut{g0: {Amount: 100, Address: alice}}))
			Expect(d.Created).To(HaveLen(2))
			Expect(d.Created[outpoint(tx, 0)]).To(Equal(domain.TxOut{Amount: 60, Address: bob}))
			Expect(d.Created[outpoint(tx, 1)]).To(Equal(domain.TxOut{Amount: 30, Address: alice}))
		})

		It("accepts outputs equal to inputs and allows a fee", func() {
			tx := newTx(1, []domain.OutPoint{g0}, domain.TxOut{Amount: 100, Address: bob})
			_, err := domain.BlockDelta(set, blockOf(1, tx), false)
			Expect(err).NotTo(HaveOccurred())
		})

		It("sums several inputs", func() {
			tx := newTx(1, []domain.OutPoint{g0, outpoint(genesisTx, 1)}, domain.TxOut{Amount: 150, Address: bob})
			_, err := domain.BlockDelta(set, blockOf(1, tx), false)
			Expect(err).NotTo(HaveOccurred())
		})

		It("lets a later transaction spend an earlier output of the same block", func() {
			t1 := newTx(1, []domain.OutPoint{g0}, domain.TxOut{Amount: 90, Address: bob})
			t2 := newTx(2, []domain.OutPoint{outpoint(t1, 0)}, domain.TxOut{Amount: 80, Address: alice})
			d, err := domain.BlockDelta(set, blockOf(1, t1, t2), false)
			Expect(err).NotTo(HaveOccurred())
			Expect(d.Spent).To(HaveLen(1))
			Expect(d.Spent).To(HaveKey(g0))
			Expect(d.Created).To(Equal(map[domain.OutPoint]domain.TxOut{outpoint(t2, 0): {Amount: 80, Address: alice}}))
		})

		It("rejects spending an output created later in the same block", func() {
			t2 := newTx(2, nil, domain.TxOut{Amount: 5, Address: bob})
			t1 := newTx(1, []domain.OutPoint{outpoint(t2, 0)}, domain.TxOut{Amount: 5, Address: bob})
			_, err := domain.BlockDelta(set, blockOf(1, t1, t2), false)
			Expect(err).To(MatchError(domain.ErrInputMissingOrSpent))
		})

		It("rejects a missing input", func() {
			tx := newTx(1, []domain.OutPoint{{TxID: "nope"}}, domain.TxOut{Amount: 1, Address: bob})
			_, err := domain.BlockDelta(set, blockOf(1, tx), false)
			Expect(err).To(MatchError(domain.ErrInputMissingOrSpent))
		})

		It("rejects an input with the right tx but wrong index", func() {
			tx := newTx(1, []domain.OutPoint{outpoint(genesisTx, 9)}, domain.TxOut{Amount: 1, Address: bob})
			_, err := domain.BlockDelta(set, blockOf(1, tx), false)
			Expect(err).To(MatchError(domain.ErrInputMissingOrSpent))
		})

		It("rejects an input already spent by a previous block", func() {
			tx := newTx(1, []domain.OutPoint{g0}, domain.TxOut{Amount: 100, Address: bob})
			d, err := domain.BlockDelta(set, blockOf(1, tx), false)
			Expect(err).NotTo(HaveOccurred())
			set.Apply(d)

			again := newTx(2, []domain.OutPoint{g0}, domain.TxOut{Amount: 100, Address: bob})
			_, err = domain.BlockDelta(set, blockOf(2, again), false)
			Expect(err).To(MatchError(domain.ErrInputMissingOrSpent))
		})

		It("rejects the same outpoint twice in one transaction", func() {
			tx := newTx(1, []domain.OutPoint{g0, g0}, domain.TxOut{Amount: 1, Address: bob})
			_, err := domain.BlockDelta(set, blockOf(1, tx), false)
			Expect(err).To(MatchError(domain.ErrDoubleSpend))
		})

		It("rejects the same outpoint in two transactions of one block", func() {
			t1 := newTx(1, []domain.OutPoint{g0}, domain.TxOut{Amount: 1, Address: bob})
			t2 := newTx(2, []domain.OutPoint{g0}, domain.TxOut{Amount: 2, Address: bob})
			_, err := domain.BlockDelta(set, blockOf(1, t1, t2), false)
			Expect(err).To(MatchError(domain.ErrDoubleSpend))
		})

		It("rejects a created outpoint that already exists", func() {
			_, err := domain.BlockDelta(set, genesis, true)
			Expect(err).To(MatchError(domain.ErrDoubleSpend))
		})

		It("rejects re-creating an outpoint created and spent earlier in the same genesis block", func() {
			t1 := newTx(5, nil, domain.TxOut{Amount: 5, Address: alice})
			t2 := newTx(6, []domain.OutPoint{outpoint(t1, 0)}, domain.TxOut{Amount: 5, Address: bob})
			_, err := domain.BlockDelta(domain.UTXOSet{}, blockOf(0, t1, t2, t1), true)
			Expect(err).To(MatchError(domain.ErrDoubleSpend))
		})

		It("rejects the same transaction twice in one block", func() {
			_, err := domain.BlockDelta(domain.UTXOSet{}, blockOf(0, genesisTx, genesisTx), true)
			Expect(err).To(MatchError(domain.ErrDoubleSpend))
		})

		It("rejects outputs larger than inputs", func() {
			tx := newTx(1, []domain.OutPoint{g0}, domain.TxOut{Amount: 101, Address: bob})
			_, err := domain.BlockDelta(set, blockOf(1, tx), false)
			Expect(err).To(MatchError(domain.ErrInsufficientInputs))
		})

		It("rejects a transaction with no outputs", func() {
			tx := newTx(1, []domain.OutPoint{g0})
			_, err := domain.BlockDelta(set, blockOf(1, tx), false)
			Expect(err).To(MatchError(domain.ErrInvalidOutput))
			_, err = domain.BlockDelta(domain.UTXOSet{}, blockOf(0, newTx(0, nil)), true)
			Expect(err).To(MatchError(domain.ErrInvalidOutput))
		})

		It("rejects a zero amount and an invalid address", func() {
			zero := newTx(1, []domain.OutPoint{g0}, domain.TxOut{Amount: 0, Address: bob})
			_, err := domain.BlockDelta(set, blockOf(1, zero), false)
			Expect(err).To(MatchError(domain.ErrInvalidOutput))

			bad := newTx(1, []domain.OutPoint{g0}, domain.TxOut{Amount: 1, Address: "bob"})
			_, err = domain.BlockDelta(set, blockOf(1, bad), false)
			Expect(err).To(MatchError(domain.ErrInvalidOutput))
		})

		It("rejects input-less transactions outside the genesis and position 0", func() {
			spend := newTx(1, []domain.OutPoint{g0}, domain.TxOut{Amount: 100, Address: bob})
			tx := newTx(2, nil, domain.TxOut{Amount: 1, Address: bob})
			_, err := domain.BlockDelta(set, blockOf(1, spend, tx), false)
			Expect(err).To(MatchError(domain.ErrMissingInputs))
		})

		It("does not accept input-less transactions in genesis mode with inputs missing", func() {
			tx := newTx(1, []domain.OutPoint{{TxID: "x"}}, domain.TxOut{Amount: 1, Address: bob})
			_, err := domain.BlockDelta(domain.UTXOSet{}, blockOf(0, tx), true)
			Expect(err).To(MatchError(domain.ErrInputMissingOrSpent))
		})

		It("applies the value check to genesis transactions that do have inputs", func() {
			g := newTx(1, []domain.OutPoint{g0}, domain.TxOut{Amount: 101, Address: bob})
			_, err := domain.BlockDelta(set, blockOf(0, g), true)
			Expect(err).To(MatchError(domain.ErrInsufficientInputs))
		})

		It("never wraps around on output overflow", func() {
			tx := newTx(1, []domain.OutPoint{g0},
				domain.TxOut{Amount: math.MaxUint64, Address: bob}, domain.TxOut{Amount: 2, Address: bob})
			_, err := domain.BlockDelta(set, blockOf(1, tx), false)
			Expect(err).To(MatchError(domain.ErrInvalidOutput))
		})

		It("never wraps around on input overflow", func() {
			big := domain.UTXOSet{
				{TxID: "a"}: {Amount: math.MaxUint64, Address: alice},
				{TxID: "b"}: {Amount: 2, Address: alice},
			}
			tx := newTx(1, []domain.OutPoint{{TxID: "a"}, {TxID: "b"}}, domain.TxOut{Amount: 1, Address: bob})
			_, err := domain.BlockDelta(big, blockOf(1, tx), false)
			Expect(err).To(MatchError(domain.ErrInsufficientInputs))
		})

		It("accepts outputs summing exactly to MaxUint64", func() {
			big := domain.UTXOSet{{TxID: "a"}: {Amount: math.MaxUint64, Address: alice}}
			tx := newTx(1, []domain.OutPoint{{TxID: "a"}},
				domain.TxOut{Amount: math.MaxUint64 - 1, Address: bob}, domain.TxOut{Amount: 1, Address: bob})
			_, err := domain.BlockDelta(big, blockOf(1, tx), false)
			Expect(err).NotTo(HaveOccurred())
		})

		It("does not mutate the view on success or failure", func() {
			before := clone(set)
			ok := newTx(1, []domain.OutPoint{g0}, domain.TxOut{Amount: 1, Address: bob})
			_, err := domain.BlockDelta(set, blockOf(1, ok), false)
			Expect(err).NotTo(HaveOccurred())
			Expect(set).To(Equal(before))

			bad := newTx(2, []domain.OutPoint{g0}, domain.TxOut{Amount: 1000, Address: bob})
			_, err = domain.BlockDelta(set, blockOf(1, ok, bad), false)
			Expect(err).To(HaveOccurred())
			Expect(set).To(Equal(before))
		})
	})

	Describe("UTXOSet", func() {
		It("is a UTXOView", func() {
			var v domain.UTXOView = set

			out, ok := v.Lookup(g0)
			Expect(ok).To(BeTrue())
			Expect(out).To(Equal(domain.TxOut{Amount: 100, Address: alice}))

			_, ok = v.Lookup(domain.OutPoint{TxID: "zz"})
			Expect(ok).To(BeFalse())
		})

		It("Apply removes spent and adds created; Revert restores", func() {
			tx := newTx(1, []domain.OutPoint{g0}, domain.TxOut{Amount: 60, Address: bob})
			d, err := domain.BlockDelta(set, blockOf(1, tx), false)
			Expect(err).NotTo(HaveOccurred())

			before := clone(set)

			set.Apply(d)
			Expect(set).NotTo(HaveKey(g0))
			Expect(set).To(HaveKey(outpoint(tx, 0)))
			Expect(set).To(HaveKey(outpoint(genesisTx, 1)))
			Expect(set).To(HaveLen(2))

			set.Revert(d)
			Expect(set).To(Equal(before))
		})
	})

	Describe("BuildUTXOSet", func() {
		It("returns an empty set for no blocks", func() {
			s, err := domain.BuildUTXOSet(nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(s).To(BeEmpty())
			Expect(s).NotTo(BeNil())
		})

		It("treats only Index 0 as genesis", func() {
			b1 := blockOf(1, newTx(1, nil, domain.TxOut{Amount: domain.InitialSubsidy + 1, Address: bob}))
			_, err := domain.BuildUTXOSet([]*domain.Block{genesis, b1})
			Expect(err).To(MatchError(domain.ErrCoinbaseTooLarge))
		})

		It("returns an error, without panicking, for a nil first block", func() {
			var (
				s   domain.UTXOSet
				err error
			)

			Expect(func() { s, err = domain.BuildUTXOSet([]*domain.Block{nil}) }).NotTo(Panic())
			Expect(err).To(HaveOccurred())
			Expect(s).To(BeNil())
		})

		It("does not treat a first block with Index other than 0 as genesis", func() {
			first := blockOf(1, newTx(1, nil, domain.TxOut{Amount: domain.InitialSubsidy + 1, Address: bob}))
			s, err := domain.BuildUTXOSet([]*domain.Block{first})
			Expect(err).To(MatchError(domain.ErrCoinbaseTooLarge))
			Expect(s).To(BeNil())
		})

		It("treats a repeated Index 0 block past the first as non-genesis, so an allocation cannot be re-spent", func() {
			spend1 := newTx(1, []domain.OutPoint{g0}, domain.TxOut{Amount: 100, Address: bob})
			spend2 := newTx(2, []domain.OutPoint{g0}, domain.TxOut{Amount: 100, Address: alice})
			realloc := newTx(0, nil, domain.TxOut{Amount: domain.InitialSubsidy + 1, Address: alice})
			blocks := []*domain.Block{genesis, blockOf(1, spend1), blockOf(0, realloc), blockOf(1, spend2)}
			s, err := domain.BuildUTXOSet(blocks)
			Expect(err).To(MatchError(domain.ErrCoinbaseTooLarge))
			Expect(s).To(BeNil())
		})

		It("treats an Index 0 block right after the genesis as non-genesis, so it cannot mint an allocation", func() {
			mint := blockOf(0, newTx(7, nil, domain.TxOut{Amount: domain.InitialSubsidy + 1, Address: bob}))
			s, err := domain.BuildUTXOSet([]*domain.Block{genesis, mint})
			Expect(err).To(MatchError(domain.ErrCoinbaseTooLarge))
			Expect(s).To(BeNil())
		})

		It("wraps nothing: a failing block returns its sentinel", func() {
			bad := blockOf(1, newTx(1, []domain.OutPoint{{TxID: "q"}}, domain.TxOut{Amount: 1, Address: bob}))
			s, err := domain.BuildUTXOSet([]*domain.Block{genesis, bad})
			Expect(err).To(MatchError(domain.ErrInputMissingOrSpent))
			Expect(s).To(BeNil())
		})
	})

	Describe("invariants over random valid chains", func() {
		addrs := func() []string {
			return []string{seededWallet(1).Address(), seededWallet(50).Address(), seededWallet(100).Address()}
		}

		// randomChain builds a valid chain, spending random unspent outputs.
		randomChain := func(r *rand.Rand) []*domain.Block {
			as := addrs()
			n := 0
			next := func() int { n++; return n }

			var gtxs []domain.Transaction
			for i := 0; i < 1+r.Intn(3); i++ {
				gtxs = append(gtxs, newTx(next(), nil, domain.TxOut{Amount: uint64(1 + r.Intn(1000)), Address: as[r.Intn(3)]}))
			}

			blocks := []*domain.Block{blockOf(0, gtxs...)}
			cur, err := domain.BuildUTXOSet(blocks)
			Expect(err).NotTo(HaveOccurred())

			for b := 1; b <= 6; b++ {
				view := clone(cur)

				var txs []domain.Transaction

				for t := 0; t < 1+r.Intn(3); t++ {
					var (
						ops []domain.OutPoint
						sum uint64
					)

					for op, out := range view {
						if len(ops) < 1+r.Intn(3) {
							ops = append(ops, op)
							sum += out.Amount

							delete(view, op)
						}
					}

					if len(ops) == 0 {
						break
					}

					outs := []domain.TxOut{{Amount: 1 + uint64(r.Intn(int(sum))), Address: as[r.Intn(3)]}}
					if rest := sum - outs[0].Amount; rest > 0 && r.Intn(2) == 0 {
						outs = append(outs, domain.TxOut{Amount: 1 + uint64(r.Intn(int(rest))), Address: as[r.Intn(3)]})
					}

					tx := newTx(next(), ops, outs...)

					txs = append(txs, tx)
					for i, o := range outs {
						view[outpoint(tx, uint32(i))] = o
					}
				}

				blk := blockOf(b, txs...)
				d, err := domain.BlockDelta(cur, blk, false)
				Expect(err).NotTo(HaveOccurred())
				cur.Apply(d)

				blocks = append(blocks, blk)
			}

			return blocks
		}

		It("holds for 200 random chains", func() {
			for seed := int64(0); seed < 200; seed++ {
				r := rand.New(rand.NewSource(seed))
				blocks := randomChain(r)

				// BuildUTXOSet == incremental deltas, twice the same.
				built, err := domain.BuildUTXOSet(blocks)
				Expect(err).NotTo(HaveOccurred())
				again, err := domain.BuildUTXOSet(blocks)
				Expect(err).NotTo(HaveOccurred())
				Expect(again).To(Equal(built))

				inc := domain.UTXOSet{}
				spentOnce := map[domain.OutPoint]bool{}

				for _, b := range blocks {
					before := clone(inc)
					d, err := domain.BlockDelta(inc, b, b.Index == 0)
					Expect(err).NotTo(HaveOccurred())
					Expect(inc).To(Equal(before)) // view untouched

					// Apply then Revert gives the original set.
					inc.Apply(d)
					applied := clone(inc)
					inc.Revert(d)
					Expect(inc).To(Equal(before))
					inc.Apply(d)
					Expect(inc).To(Equal(applied))

					// each outpoint is spent at most once; sum(in) >= sum(out).
					for _, tx := range b.Transactions {
						var in, out uint64

						for _, i := range tx.Inputs {
							op := domain.OutPoint{TxID: i.PrevTxID, Index: i.OutIndex}
							Expect(spentOnce[op]).To(BeFalse())
							spentOnce[op] = true
						}

						for _, o := range tx.Outputs {
							out += o.Amount
						}

						if b.Index != 0 {
							for _, i := range tx.Inputs {
								in += lookupAmount(blocks, i.PrevTxID, i.OutIndex)
							}

							Expect(in).To(BeNumerically(">=", out))
						}
					}
				}

				Expect(inc).To(Equal(built))
			}
		})

		It("leaves the view identical whether BlockDelta fails or succeeds", func() {
			for seed := int64(0); seed < 200; seed++ {
				r := rand.New(rand.NewSource(seed))
				blocks := randomChain(r)
				view, err := domain.BuildUTXOSet(blocks[:len(blocks)-1])
				Expect(err).NotTo(HaveOccurred())

				// Corrupt the last block: duplicate a transaction or drop its outputs.
				last := *blocks[len(blocks)-1]

				last.Transactions = append([]domain.Transaction(nil), last.Transactions...)
				if len(last.Transactions) > 0 {
					if r.Intn(2) == 0 {
						last.Transactions = append(last.Transactions, last.Transactions[0])
					} else {
						last.Transactions[0].Outputs = nil
					}
				}

				before := clone(view)
				_, _ = domain.BlockDelta(view, &last, false)
				Expect(view).To(Equal(before))
				_, _ = domain.BlockDelta(view, blocks[len(blocks)-1], false)
				Expect(view).To(Equal(before))
			}
		})
	})
})

// lookupAmount finds the amount of an output created anywhere in blocks.
func lookupAmount(blocks []*domain.Block, txid string, idx uint32) uint64 {
	for _, b := range blocks {
		for _, tx := range b.Transactions {
			if tx.CalculateHash() == txid && int(idx) < len(tx.Outputs) {
				return tx.Outputs[idx].Amount
			}
		}
	}

	return 0
}
