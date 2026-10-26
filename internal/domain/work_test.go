package domain_test

import (
	"math/big"
	"math/rand"
	"strconv"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

var _ = Describe("Work", func() {
	It("weighs a block 2^Difficulty, difficulty counting leading zero bits, and a nil block zero", func() {
		for d, want := range map[int]string{0: "1", 1: "2", 2: "4", 8: "256", 32: "4294967296", 256: new(big.Int).Lsh(big.NewInt(1), 256).String()} {
			Expect(domain.BlockWork(&domain.Block{Difficulty: d}).String()).To(Equal(want), "difficulty "+strconv.Itoa(d))
		}

		Expect(domain.BlockWork(&domain.Block{Difficulty: -3}).Cmp(big.NewInt(1))).To(Equal(0))
		Expect(domain.BlockWork(nil).Sign()).To(Equal(0))
	})

	It("sums the work of the blocks and is zero for none", func() {
		blocks := []*domain.Block{{Difficulty: 0}, {Difficulty: 1}, {Difficulty: 2}, {Difficulty: 2}}
		Expect(domain.ChainWork(blocks).String()).To(Equal("11"))
		Expect(domain.ChainWork(nil).Sign()).To(Equal(0))
	})

	It("does not alias the weight of a block with the sum", func() {
		a := &domain.Block{Difficulty: 1}
		sum := domain.ChainWork([]*domain.Block{a})
		sum.Add(sum, big.NewInt(1))
		Expect(domain.BlockWork(a).String()).To(Equal("2"))
	})
})

var _ = Describe("DiffUTXO", func() {
	randomSet := func(r *rand.Rand, pool []domain.OutPoint) domain.UTXOSet {
		s := domain.UTXOSet{}

		for _, op := range pool {
			if r.Intn(2) == 0 {
				s[op] = domain.TxOut{Amount: uint64(1 + r.Intn(3)), Address: "a" + strconv.Itoa(r.Intn(3))}
			}
		}

		return s
	}

	It("is empty between equal sets", func() {
		s := domain.UTXOSet{{TxID: "x"}: {Amount: 1, Address: "a"}}
		d := domain.DiffUTXO(s, clone(s))
		Expect(d.Spent).To(BeEmpty())
		Expect(d.Created).To(BeEmpty())
	})

	It("lists removed outputs as spent, new ones as created and changed ones as both", func() {
		keep := domain.OutPoint{TxID: "keep"}
		gone := domain.OutPoint{TxID: "gone"}
		fresh := domain.OutPoint{TxID: "fresh"}
		moved := domain.OutPoint{TxID: "moved"}
		from := domain.UTXOSet{keep: {Amount: 1, Address: "a"}, gone: {Amount: 2, Address: "a"}, moved: {Amount: 3, Address: "a"}}
		to := domain.UTXOSet{keep: {Amount: 1, Address: "a"}, fresh: {Amount: 4, Address: "b"}, moved: {Amount: 5, Address: "a"}}

		d := domain.DiffUTXO(from, to)

		Expect(d.Spent).To(Equal(map[domain.OutPoint]domain.TxOut{gone: from[gone], moved: from[moved]}))
		Expect(d.Created).To(Equal(map[domain.OutPoint]domain.TxOut{fresh: to[fresh], moved: to[moved]}))
	})

	It("takes the from-set to the to-set when applied, over 300 random pairs of sets", func() {
		pool := make([]domain.OutPoint, 12)
		for i := range pool {
			pool[i] = domain.OutPoint{TxID: "tx" + strconv.Itoa(i/3), Index: uint32(i % 3)}
		}

		for seed := range int64(300) {
			r := rand.New(rand.NewSource(seed))
			from, to := randomSet(r, pool), randomSet(r, pool)
			fromBefore := clone(from)

			d := domain.DiffUTXO(from, to)

			Expect(from).To(Equal(fromBefore), "seed %d: DiffUTXO changed from", seed)
			applied := clone(from)
			applied.Apply(d)
			Expect(applied).To(Equal(to), "seed %d", seed)
			applied.Revert(d)
			Expect(applied).To(Equal(from), "seed %d: revert", seed)
		}
	})
})
