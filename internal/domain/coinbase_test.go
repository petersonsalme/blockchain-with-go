package domain_test

import (
	"math"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

var _ = Describe("Coinbase rule", func() {
	var (
		alice, bob string
		set        domain.UTXOSet
		g0         domain.OutPoint
	)

	BeforeEach(func() {
		alice = seededWallet(1).Address()
		bob = seededWallet(100).Address()
		gtx := newTx(0, nil, domain.TxOut{Amount: 100, Address: alice})

		var err error

		set, err = domain.BuildUTXOSet([]*domain.Block{blockOf(0, gtx)})
		Expect(err).NotTo(HaveOccurred())

		g0 = outpoint(gtx, 0)
	})

	coinbase := func(n int, amount uint64) domain.Transaction {
		return newTx(n, nil, domain.TxOut{Amount: amount, Address: bob})
	}
	// feeTx spends g0 (100) leaving fee 100-out as fee.
	feeTx := func(out uint64) domain.Transaction {
		return newTx(5, []domain.OutPoint{g0}, domain.TxOut{Amount: out, Address: alice})
	}

	It("accepts a coinbase at the subsidy limit and adds its outputs to Created", func() {
		cb := coinbase(1, domain.InitialSubsidy)
		d, err := domain.BlockDelta(set, blockOf(209_999, cb), false)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.Created).To(Equal(map[domain.OutPoint]domain.TxOut{
			outpoint(cb, 0): {Amount: domain.InitialSubsidy, Address: bob},
		}))
		Expect(d.Spent).To(BeEmpty())
	})

	It("rejects a coinbase one above the limit", func() {
		_, err := domain.BlockDelta(set, blockOf(1, coinbase(1, domain.InitialSubsidy+1)), false)
		Expect(err).To(MatchError(domain.ErrCoinbaseTooLarge))
	})

	It("halves the limit at Index 210000", func() {
		_, err := domain.BlockDelta(set, blockOf(210_000, coinbase(1, 2_500_000_000+40), feeTx(60)), false)
		Expect(err).NotTo(HaveOccurred())
		_, err = domain.BlockDelta(set, blockOf(210_000, coinbase(1, 2_500_000_000+41), feeTx(60)), false)
		Expect(err).To(MatchError(domain.ErrCoinbaseTooLarge))
	})

	It("accepts only the subsidy when the coinbase is the sole transaction after the last halving", func() {
		_, err := domain.BlockDelta(set, blockOf(64*domain.HalvingInterval, coinbase(1, 1)), false)
		Expect(err).To(MatchError(domain.ErrCoinbaseTooLarge))
	})

	It("counts the fees of every other transaction", func() {
		_, err := domain.BlockDelta(set, blockOf(1, coinbase(1, domain.InitialSubsidy+10), feeTx(90)), false)
		Expect(err).NotTo(HaveOccurred())
	})

	It("does not wrap subsidy plus fees", func() {
		huge := newTx(0, nil, domain.TxOut{Amount: math.MaxUint64, Address: alice})
		big, err := domain.BuildUTXOSet([]*domain.Block{blockOf(0, huge)})
		Expect(err).NotTo(HaveOccurred())

		spend := newTx(5, []domain.OutPoint{outpoint(huge, 0)}, domain.TxOut{Amount: 1, Address: alice})
		_, err = domain.BlockDelta(big, blockOf(1, coinbase(1, math.MaxUint64), spend), false)
		Expect(err).NotTo(HaveOccurred())
	})

	It("still rejects input-less transactions at position 1 or later", func() {
		_, err := domain.BlockDelta(set, blockOf(1, coinbase(1, 1), coinbase(2, 1)), false)
		Expect(err).To(MatchError(domain.ErrMissingInputs))
	})

	It("does not change the genesis rule", func() {
		_, err := domain.BlockDelta(domain.UTXOSet{}, blockOf(0, coinbase(1, domain.InitialSubsidy+1)), true)
		Expect(err).NotTo(HaveOccurred())
	})

	It("keeps created minus spent within the subsidy", func() {
		d, err := domain.BlockDelta(set, blockOf(1, coinbase(1, domain.InitialSubsidy+10), feeTx(90)), false)
		Expect(err).NotTo(HaveOccurred())

		var created, spent uint64
		for _, o := range d.Created {
			created += o.Amount
		}

		for _, o := range d.Spent {
			spent += o.Amount
		}

		Expect(created - spent).To(BeNumerically("<=", domain.Subsidy(1)))
	})

	It("replays a chain with a coinbase to the same set as incremental application", func() {
		gtx := newTx(0, nil, domain.TxOut{Amount: 100, Address: alice})
		blocks := []*domain.Block{blockOf(0, gtx), blockOf(1, coinbase(1, 7), feeTx(60))}
		built, err := domain.BuildUTXOSet(blocks)
		Expect(err).NotTo(HaveOccurred())

		inc := domain.UTXOSet{}
		for i, b := range blocks {
			d, err := domain.BlockDelta(inc, b, i == 0)
			Expect(err).NotTo(HaveOccurred())
			inc.Apply(d)
		}

		Expect(built).To(Equal(inc))
	})
})
