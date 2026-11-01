package mempool_test

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/mempool"
)

// mkTx builds a transaction with the given ID spending (prev, idx). pad grows its size.
func mkTx(id, prev string, idx uint32, pad int) domain.Transaction {
	addr := ""

	var addrSb21 strings.Builder
	for range pad {
		addrSb21.WriteString("x")
	}

	addr += addrSb21.String()

	return domain.Transaction{
		ID:        id,
		Inputs:    []domain.TxIn{{PrevTxID: prev, OutIndex: idx}},
		Outputs:   []domain.TxOut{{Amount: 1, Address: addr}},
		Timestamp: time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC),
	}
}

func ids(txs []domain.Transaction) []string {
	out := make([]string, len(txs))
	for i, t := range txs {
		out[i] = t.ID
	}

	return out
}

type view map[domain.OutPoint]bool

func (v view) Lookup(op domain.OutPoint) (domain.TxOut, bool) { return domain.TxOut{}, v[op] }

// sameSize returns n transactions of identical size (IDs and prev IDs of equal length).
func sameSize(n int) []domain.Transaction {
	txs := make([]domain.Transaction, n)
	for i := range txs {
		txs[i] = mkTx(fmt.Sprintf("t%03d", i), fmt.Sprintf("p%03d", i), 0, 10)
	}

	return txs
}

var _ = Describe("Pool", func() {
	Describe("Size", func() {
		It("is the length of the JSON encoding", func() {
			tx := mkTx("a", "b", 0, 0)
			Expect(mempool.Size(tx)).To(Equal(len(`{"ID":"a","Inputs":[{"PrevTxID":"b","OutIndex":0,"PublicKey":"","Signature":""}],"Outputs":[{"Amount":1,"Address":""}],"Timestamp":"2026-10-22T00:00:00Z"}`)))
		})
	})

	Describe("Entries", func() {
		It("lists fee and size in the order of Select, ties by ID and 128-bit rates included", func() {
			p := mempool.New(0)
			big := uint64(math.MaxUint64)
			fees := map[string]uint64{"b": 100, "a": 100, "c": 7, "d": big, "e": big - 1}

			for id, fee := range fees {
				Expect(p.Add(mkTx(id, "p"+id, 0, 10), fee)).To(Succeed())
			}

			Expect(p.Add(mkTx("f", "pf", 0, 40), big)).To(Succeed()) // larger tx, same fee as d: ranks after it

			sel := p.Select(math.MaxInt)
			entries := p.Entries()
			Expect(entries).To(HaveLen(len(sel)))
			Expect(entries).To(HaveLen(6))

			for i, e := range entries {
				Expect(e.Tx.ID).To(Equal(sel[i].ID))
				Expect(e.Size).To(Equal(mempool.Size(sel[i])))
			}

			Expect(entries[0].Tx.ID).To(Equal("d"))
			Expect(entries[0].Fee).To(Equal(big))
			Expect(entries[1].Tx.ID).To(Equal("e"))
			Expect(entries[2].Tx.ID).To(Equal("f"))
			Expect(entries[3].Tx.ID).To(Equal("a")) // rate tie with b: lower ID first
			Expect(entries[3].Fee).To(Equal(uint64(100)))
			Expect(entries[4].Tx.ID).To(Equal("b"))
		})

		It("is empty for an empty pool and returns a copy", func() {
			p := mempool.New(0)
			Expect(p.Entries()).To(BeEmpty())

			Expect(p.Add(mkTx("a", "pa", 0, 0), 5)).To(Succeed())
			Expect(p.Add(mkTx("b", "pb", 0, 0), 9)).To(Succeed())

			got := p.Entries()
			got[0], got[1] = got[1], got[0]
			got[0].Fee = 0

			Expect(ids(p.Select(math.MaxInt))).To(Equal([]string{"b", "a"}))
			Expect(p.Entries()[0].Fee).To(Equal(uint64(9)))
		})
	})

	Describe("New", func() {
		It("uses DefaultMaxTxs for a non-positive limit", func() {
			for _, n := range []int{0, -3} {
				p := mempool.New(n)
				for i := range mempool.DefaultMaxTxs + 1 {
					_ = p.Add(mkTx(fmt.Sprintf("t%d", i), fmt.Sprintf("p%d", i), 0, 0), uint64(i))
				}

				Expect(p.Len()).To(Equal(mempool.DefaultMaxTxs))
			}
		})
	})

	Describe("Select ordering", func() {
		It("orders by fee per byte descending, not by absolute fee", func() {
			p := mempool.New(10)
			big := mkTx("big", "p1", 0, 200)   // large fee, low rate
			small := mkTx("small", "p2", 0, 0) // small fee, high rate

			Expect(p.Add(big, 1000)).To(Succeed())
			Expect(p.Add(small, 500)).To(Succeed())
			Expect(ids(p.Select(1 << 20))).To(Equal([]string{"small", "big"}))
		})

		It("breaks equal rates by ID ascending", func() {
			p := mempool.New(10)
			for _, id := range []string{"c", "a", "b"} {
				Expect(p.Add(mkTx(id, "p"+id, 0, 0), 10)).To(Succeed())
			}

			Expect(ids(p.Select(1 << 20))).To(Equal([]string{"a", "b", "c"}))
		})

		It("compares exactly where truncating division would tie", func() {
			p := mempool.New(10)
			a := mkTx("a", "pa", 0, 0)
			b := mkTx("b", "pb", 0, 3)
			sa, sb := uint64(mempool.Size(a)), uint64(mempool.Size(b))
			// feeB/sb is just above feeA/sa but both integer-divide to the same value.
			feeA := 2 * sa
			feeB := 2*sb + 1
			Expect(feeB / sb).To(Equal(feeA / sa))
			Expect(p.Add(a, feeA)).To(Succeed())
			Expect(p.Add(b, feeB)).To(Succeed())
			Expect(ids(p.Select(1 << 20))).To(Equal([]string{"b", "a"}))
		})

		It("does not overflow with fees near MaxUint64", func() {
			p := mempool.New(10)
			Expect(p.Add(mkTx("a", "pa", 0, 0), math.MaxUint64-1)).To(Succeed())
			Expect(p.Add(mkTx("b", "pb", 0, 0), math.MaxUint64)).To(Succeed())
			Expect(ids(p.Select(1 << 20))).To(Equal([]string{"b", "a"}))
		})

		It("compares the high word of the product", func() {
			p := mempool.New(10)
			small, large := mkTx("a", "pa", 0, 0), mkTx("b", "pb", 0, 100)
			// equal maximal fees: the product with the larger size has the larger high word
			Expect(p.Add(large, math.MaxUint64)).To(Succeed())
			Expect(p.Add(small, math.MaxUint64)).To(Succeed())
			Expect(ids(p.Select(1 << 20))).To(Equal([]string{"a", "b"}))
		})

		It("uses the low word when the high words are equal", func() {
			p := mempool.New(10)
			// same size, fees differing only in low bits of the product
			Expect(p.Add(mkTx("a", "pa", 0, 0), 5)).To(Succeed())
			Expect(p.Add(mkTx("b", "pb", 0, 0), 6)).To(Succeed())
			Expect(ids(p.Select(1 << 20))).To(Equal([]string{"b", "a"}))
		})

		It("gives the same output whatever the Add order", func() {
			txs := sameSize(20)

			fees := make([]uint64, len(txs))
			for i := range fees {
				fees[i] = uint64(i % 4)
			}

			build := func(order []int) []string {
				p := mempool.New(100)
				for _, i := range order {
					Expect(p.Add(txs[i], fees[i])).To(Succeed())
				}

				return ids(p.Select(1 << 20))
			}
			order := rand.New(rand.NewPCG(1, 2)).Perm(len(txs))
			want := build(slices.Sorted(slices.Values(order)))
			Expect(build(order)).To(Equal(want))
			slices.Reverse(order)
			Expect(build(order)).To(Equal(want))
		})
	})

	Describe("Select budget", func() {
		It("skips a transaction that does not fit and keeps going", func() {
			p := mempool.New(10)
			big := mkTx("big", "p1", 0, 300)
			s1 := mkTx("s1", "p2", 0, 0)
			s2 := mkTx("s2", "p3", 0, 0)

			Expect(p.Add(big, 1_000_000)).To(Succeed()) // ranks first
			Expect(p.Add(s1, 10)).To(Succeed())
			Expect(p.Add(s2, 5)).To(Succeed())
			budget := mempool.Size(s1) + mempool.Size(s2)
			Expect(ids(p.Select(budget))).To(Equal([]string{"s1", "s2"}))
		})

		It("accepts a set that exactly fills the budget", func() {
			p := mempool.New(10)
			a, b := mkTx("a", "pa", 0, 0), mkTx("b", "pb", 0, 0)
			Expect(p.Add(a, 2)).To(Succeed())
			Expect(p.Add(b, 1)).To(Succeed())
			Expect(p.Select(mempool.Size(a) + mempool.Size(b))).To(HaveLen(2))
			Expect(p.Select(mempool.Size(a) + mempool.Size(b) - 1)).To(HaveLen(1))
		})

		It("returns nothing for a zero budget and does not remove anything", func() {
			p := mempool.New(10)
			Expect(p.Add(mkTx("a", "pa", 0, 0), 2)).To(Succeed())
			Expect(p.Select(0)).To(BeEmpty())
			Expect(p.Len()).To(Equal(1))
		})
	})

	Describe("Add", func() {
		var p *mempool.Pool

		BeforeEach(func() { p = mempool.New(2) })

		It("rejects a duplicate ID and leaves the pool unchanged", func() {
			Expect(p.Add(mkTx("a", "pa", 0, 0), 1)).To(Succeed())
			Expect(p.Add(mkTx("a", "pz", 0, 0), 99)).To(MatchError(mempool.ErrDuplicate))
			Expect(p.Len()).To(Equal(1))
			// pz must not have been reserved
			Expect(p.Add(mkTx("b", "pz", 0, 0), 1)).To(Succeed())
		})

		It("rejects a conflicting outpoint and leaves the pool unchanged", func() {
			Expect(p.Add(mkTx("a", "pa", 0, 0), 1)).To(Succeed())
			Expect(p.Add(mkTx("b", "pa", 0, 0), 99)).To(MatchError(mempool.ErrConflict))
			Expect(ids(p.Select(1 << 20))).To(Equal([]string{"a"}))
			// same prev with another index is a different outpoint
			Expect(p.Add(mkTx("c", "pa", 1, 0), 1)).To(Succeed())
		})

		It("detects a conflict on any input, not only the first", func() {
			two := mkTx("two", "p1", 0, 0)
			two.Inputs = append(two.Inputs, domain.TxIn{PrevTxID: "p2", OutIndex: 0})

			Expect(p.Add(mkTx("a", "p2", 0, 0), 1)).To(Succeed())
			Expect(p.Add(two, 1)).To(MatchError(mempool.ErrConflict))
		})

		It("evicts the last when full and the newcomer ranks strictly above", func() {
			Expect(p.Add(mkTx("a", "pa", 0, 0), 10)).To(Succeed())
			Expect(p.Add(mkTx("b", "pb", 0, 0), 5)).To(Succeed())
			Expect(p.Add(mkTx("c", "pc", 0, 0), 7)).To(Succeed())
			Expect(ids(p.Select(1 << 20))).To(Equal([]string{"a", "c"}))
			Expect(p.Len()).To(Equal(2))
			// b's outpoint is free again
			Expect(p.Add(mkTx("d", "pb", 0, 0), 8)).To(Succeed())
			Expect(ids(p.Select(1 << 20))).To(Equal([]string{"a", "d"}))
		})

		It("rejects with ErrFull when the newcomer does not rank strictly above", func() {
			Expect(p.Add(mkTx("a", "pa", 0, 0), 10)).To(Succeed())
			Expect(p.Add(mkTx("b", "pb", 0, 0), 5)).To(Succeed())
			Expect(p.Add(mkTx("c", "pc", 0, 0), 4)).To(MatchError(mempool.ErrFull))
			// equal rate, larger ID: ranks after, so rejected
			Expect(p.Add(mkTx("z", "pz", 0, 0), 5)).To(MatchError(mempool.ErrFull))
			Expect(ids(p.Select(1 << 20))).To(Equal([]string{"a", "b"}))
			// equal rate, smaller ID: ranks before, so admitted
			Expect(p.Add(mkTx("0", "p0", 0, 0), 5)).To(Succeed())
			Expect(ids(p.Select(1 << 20))).To(Equal([]string{"a", "0"}))
		})

		It("reports a conflict before fullness", func() {
			Expect(p.Add(mkTx("a", "pa", 0, 0), 10)).To(Succeed())
			Expect(p.Add(mkTx("b", "pb", 0, 0), 5)).To(Succeed())
			Expect(p.Add(mkTx("c", "pa", 0, 0), 1)).To(MatchError(mempool.ErrConflict))
		})
	})

	Describe("Remove", func() {
		It("drops the given IDs, frees their outpoints and ignores unknown IDs", func() {
			p := mempool.New(10)
			Expect(p.Add(mkTx("a", "pa", 0, 0), 3)).To(Succeed())
			Expect(p.Add(mkTx("b", "pb", 0, 0), 2)).To(Succeed())
			Expect(p.Add(mkTx("c", "pc", 0, 0), 1)).To(Succeed())
			p.Remove("a", "c", "nope")
			Expect(ids(p.Select(1 << 20))).To(Equal([]string{"b"}))
			Expect(p.Len()).To(Equal(1))
			Expect(p.Add(mkTx("a", "pa", 0, 0), 3)).To(Succeed())
		})
	})

	Describe("Prune", func() {
		It("removes mined transactions and those with a missing input, keeps the rest", func() {
			p := mempool.New(10)
			Expect(p.Add(mkTx("mined", "p1", 0, 0), 4)).To(Succeed())
			Expect(p.Add(mkTx("stale", "p2", 0, 0), 3)).To(Succeed())
			two := mkTx("half", "p3", 0, 0)
			two.Inputs = append(two.Inputs, domain.TxIn{PrevTxID: "p4", OutIndex: 0})
			Expect(p.Add(two, 2)).To(Succeed())
			Expect(p.Add(mkTx("ok", "p5", 0, 0), 1)).To(Succeed())

			b := &domain.Block{Transactions: []domain.Transaction{{ID: "mined"}, {ID: "other"}}}
			v := view{
				{TxID: "p1", Index: 0}: true, // still there: only removed because mined
				{TxID: "p3", Index: 0}: true, // p4 missing
				{TxID: "p5", Index: 0}: true,
			}
			p.Prune(b, v)
			Expect(ids(p.Select(1 << 20))).To(Equal([]string{"ok"}))
			Expect(p.Len()).To(Equal(1))
			// outpoints of pruned transactions are free
			Expect(p.Add(mkTx("n", "p2", 0, 0), 1)).To(Succeed())
		})

		It("keeps everything when nothing is mined and every input is present", func() {
			p := mempool.New(10)
			Expect(p.Add(mkTx("a", "p1", 0, 0), 1)).To(Succeed())
			p.Prune(&domain.Block{}, view{{TxID: "p1"}: true})
			Expect(p.Len()).To(Equal(1))
		})
	})

	Describe("invariants", func() {
		It("holds for random Add/Remove/Prune sequences", func() {
			rng := rand.New(rand.NewPCG(42, 7))

			const maxTxs = 8

			p := mempool.New(maxTxs)

			for step := range 3000 {
				switch rng.IntN(5) {
				case 0, 1, 2:
					tx := mkTx(fmt.Sprintf("t%d", rng.IntN(40)), fmt.Sprintf("p%d", rng.IntN(25)), uint32(rng.IntN(2)), rng.IntN(60))
					if rng.IntN(3) == 0 { // a second input on index 5, never equal to the first
						tx.Inputs = append(tx.Inputs, domain.TxIn{PrevTxID: fmt.Sprintf("p%d", rng.IntN(25)), OutIndex: 5})
					}

					_ = p.Add(tx, uint64(rng.IntN(500)))
				case 3:
					p.Remove(fmt.Sprintf("t%d", rng.IntN(40)))
				default:
					v := view{}
					for i := range 25 {
						v[domain.OutPoint{TxID: fmt.Sprintf("p%d", i)}] = rng.IntN(4) != 0
						v[domain.OutPoint{TxID: fmt.Sprintf("p%d", i), Index: 1}] = rng.IntN(4) != 0
					}

					p.Prune(&domain.Block{Transactions: []domain.Transaction{{ID: fmt.Sprintf("t%d", rng.IntN(40))}}}, v)
				}

				Expect(p.Len()).To(BeNumerically("<=", maxTxs), "step %d", step)

				all := p.Select(math.MaxInt)
				Expect(all).To(HaveLen(p.Len()))

				seenID := map[string]bool{}
				seenOp := map[domain.OutPoint]bool{}

				for _, tx := range all {
					Expect(seenID[tx.ID]).To(BeFalse())

					seenID[tx.ID] = true
					for _, in := range tx.Inputs {
						op := domain.OutPoint{TxID: in.PrevTxID, Index: in.OutIndex}
						Expect(seenOp[op]).To(BeFalse())
						seenOp[op] = true
					}
				}

				budget := rng.IntN(800)
				sel := p.Select(budget)

				sum := 0
				for _, tx := range sel {
					sum += mempool.Size(tx)
				}

				Expect(sum).To(BeNumerically("<=", budget))
				// selection is a subsequence of the full order
				pos := 0
				for _, tx := range sel {
					for pos < len(all) && all[pos].ID != tx.ID {
						pos++
					}

					Expect(pos).To(BeNumerically("<", len(all)))
					pos++
				}
			}
		})

		It("is safe under concurrent use", func() {
			p := mempool.New(16)

			var wg sync.WaitGroup
			for g := range 8 {
				wg.Go(func() {
					for i := range 200 {
						id := fmt.Sprintf("g%dt%d", g, i)
						_ = p.Add(mkTx(id, fmt.Sprintf("p%d", i%30), 0, 0), uint64(i))
						_ = p.Select(500)
						_ = p.Len()
						p.Remove(id)
						p.Prune(&domain.Block{}, view{})
					}
				})
			}

			wg.Wait()
			Expect(p.Len()).To(BeNumerically("<=", 16))
		})
	})
})
