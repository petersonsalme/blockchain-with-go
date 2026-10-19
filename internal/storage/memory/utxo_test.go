package memory_test

import (
	"fmt"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage"
	. "github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
)

var _ = Describe("UTXOSet", func() {
	var (
		set       *UTXOSet
		a, b, c   domain.OutPoint
		oa, ob    domain.TxOut
		oc        domain.TxOut
		seed      domain.UTXODelta
		spendAtoC domain.UTXODelta
	)

	BeforeEach(func() {
		set = NewUTXOSet()
		a, b, c = domain.OutPoint{TxID: "a"}, domain.OutPoint{TxID: "b", Index: 1}, domain.OutPoint{TxID: "c"}
		oa, ob, oc = domain.TxOut{Amount: 1, Address: "x"}, domain.TxOut{Amount: 2, Address: "y"}, domain.TxOut{Amount: 3, Address: "z"}
		seed = domain.UTXODelta{Spent: map[domain.OutPoint]domain.TxOut{}, Created: map[domain.OutPoint]domain.TxOut{a: oa, b: ob}}
		spendAtoC = domain.UTXODelta{Spent: map[domain.OutPoint]domain.TxOut{a: oa}, Created: map[domain.OutPoint]domain.TxOut{c: oc}}
	})

	It("satisfies the storage port and the domain view", func() {
		var _ storage.UTXOSet = set
		var _ domain.UTXOView = set
	})

	It("starts empty with a non-nil snapshot", func() {
		Expect(set.Snapshot()).NotTo(BeNil())
		Expect(set.Snapshot()).To(BeEmpty())
		_, ok := set.Lookup(a)
		Expect(ok).To(BeFalse())
	})

	It("applies a delta: creates and spends", func() {
		Expect(set.Apply(seed)).To(Succeed())
		Expect(set.Snapshot()).To(Equal(domain.UTXOSet{a: oa, b: ob}))

		Expect(set.Apply(spendAtoC)).To(Succeed())
		Expect(set.Snapshot()).To(Equal(domain.UTXOSet{b: ob, c: oc}))
		out, ok := set.Lookup(c)
		Expect(ok).To(BeTrue())
		Expect(out).To(Equal(oc))
		_, ok = set.Lookup(a)
		Expect(ok).To(BeFalse())
	})

	It("reverts a delta back to the previous state", func() {
		Expect(set.Apply(seed)).To(Succeed())
		before := set.Snapshot()
		Expect(set.Apply(spendAtoC)).To(Succeed())
		Expect(set.Revert(spendAtoC)).To(Succeed())
		Expect(set.Snapshot()).To(Equal(before))
	})

	Describe("Apply atomicity", func() {
		BeforeEach(func() { Expect(set.Apply(seed)).To(Succeed()) })

		It("rejects spending a missing outpoint and changes nothing", func() {
			d := domain.UTXODelta{
				Spent:   map[domain.OutPoint]domain.TxOut{a: oa, c: oc},
				Created: map[domain.OutPoint]domain.TxOut{{TxID: "n"}: oa},
			}
			before := set.Snapshot()
			Expect(set.Apply(d)).To(MatchError(storage.ErrUTXOConflict))
			Expect(set.Snapshot()).To(Equal(before))
		})

		It("rejects spending an outpoint with a different value", func() {
			d := domain.UTXODelta{Spent: map[domain.OutPoint]domain.TxOut{a: ob}}
			before := set.Snapshot()
			Expect(set.Apply(d)).To(MatchError(storage.ErrUTXOConflict))
			Expect(set.Snapshot()).To(Equal(before))
		})

		It("rejects creating an existing outpoint and changes nothing", func() {
			d := domain.UTXODelta{
				Spent:   map[domain.OutPoint]domain.TxOut{a: oa},
				Created: map[domain.OutPoint]domain.TxOut{b: ob},
			}
			before := set.Snapshot()
			Expect(set.Apply(d)).To(MatchError(storage.ErrUTXOConflict))
			Expect(set.Snapshot()).To(Equal(before))
		})
	})

	Describe("Revert atomicity", func() {
		BeforeEach(func() { Expect(set.Apply(seed)).To(Succeed()) })

		It("rejects removing a missing created outpoint and changes nothing", func() {
			before := set.Snapshot()
			Expect(set.Revert(spendAtoC)).To(MatchError(storage.ErrUTXOConflict))
			Expect(set.Snapshot()).To(Equal(before))
		})

		It("rejects removing a created outpoint with a different value", func() {
			Expect(set.Apply(spendAtoC)).To(Succeed())
			d := domain.UTXODelta{
				Spent:   map[domain.OutPoint]domain.TxOut{a: oa},
				Created: map[domain.OutPoint]domain.TxOut{c: oa},
			}
			before := set.Snapshot()
			Expect(set.Revert(d)).To(MatchError(storage.ErrUTXOConflict))
			Expect(set.Snapshot()).To(Equal(before))
		})

		It("rejects restoring an outpoint that is already unspent and changes nothing", func() {
			d := domain.UTXODelta{
				Spent:   map[domain.OutPoint]domain.TxOut{b: ob},
				Created: map[domain.OutPoint]domain.TxOut{a: oa},
			}
			before := set.Snapshot()
			Expect(set.Revert(d)).To(MatchError(storage.ErrUTXOConflict))
			Expect(set.Snapshot()).To(Equal(before))
		})
	})

	It("returns a snapshot that is independent of the set", func() {
		Expect(set.Apply(seed)).To(Succeed())
		snap := set.Snapshot()
		snap[c] = oc
		delete(snap, a)
		Expect(set.Snapshot()).To(Equal(domain.UTXOSet{a: oa, b: ob}))
	})

	It("does not keep references to the applied delta", func() {
		Expect(set.Apply(seed)).To(Succeed())
		seed.Created[a] = ob
		got, _ := set.Lookup(a)
		Expect(got).To(Equal(oa))
	})

	It("lets exactly one of many concurrent Apply calls spend the same outpoint", func() {
		Expect(set.Apply(seed)).To(Succeed())
		const n = 50
		var wg sync.WaitGroup
		results := make(chan error, n)
		for i := 0; i < n; i++ {
			i := i
			wg.Add(1)
			go func() {
				defer wg.Done()
				results <- set.Apply(domain.UTXODelta{
					Spent:   map[domain.OutPoint]domain.TxOut{a: oa},
					Created: map[domain.OutPoint]domain.TxOut{{TxID: fmt.Sprint("n", i)}: oa},
				})
			}()
		}
		wg.Wait()
		close(results)

		ok := 0
		for err := range results {
			if err == nil {
				ok++
			}
		}
		Expect(ok).To(Equal(1))
		Expect(set.Snapshot()).To(HaveLen(2))
	})

	It("allows concurrent readers during writes", func() {
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			i := i
			wg.Add(2)
			go func() {
				defer wg.Done()
				op := domain.OutPoint{TxID: fmt.Sprint("w", i)}
				d := domain.UTXODelta{Created: map[domain.OutPoint]domain.TxOut{op: oa}}
				Expect(set.Apply(d)).To(Succeed())
			}()
			go func() {
				defer wg.Done()
				set.Lookup(a)
				set.Snapshot()
			}()
		}
		wg.Wait()
		Expect(set.Snapshot()).To(HaveLen(20))
	})
})
