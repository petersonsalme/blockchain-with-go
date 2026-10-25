package vm_test

import (
	"encoding/hex"
	"errors"
	"reflect"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/vm"
)

// seq makes every transaction (and so every deploy ID) unique.
var seq uint32

func contractTx(op *domain.ContractOp) domain.Transaction {
	seq++
	return domain.Transaction{
		Inputs:    []domain.TxIn{{PrevTxID: "aa", OutIndex: seq}},
		Outputs:   []domain.TxOut{{Amount: 1, Address: "bob"}},
		Timestamp: time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC),
		Contract:  op,
	}
}

func deployTx(code []byte) domain.Transaction {
	return contractTx(&domain.ContractOp{Kind: domain.ContractDeploy, Code: hex.EncodeToString(code), GasLimit: 100})
}

func callTx(target string, gas uint64, args ...uint64) domain.Transaction {
	return contractTx(&domain.ContractOp{Kind: domain.ContractCall, Target: target, Args: args, GasLimit: gas})
}

func blockOf(txs ...domain.Transaction) *domain.Block { return &domain.Block{Transactions: txs} }

func cloneState(s vm.State) vm.State {
	out := vm.State{}
	for id, c := range s {
		st := map[uint64]uint64{}
		for k, v := range c.Storage {
			st[k] = v
		}
		out[id] = vm.Contract{Code: append([]byte(nil), c.Code...), Storage: st}
	}
	return out
}

func counterOf(s vm.State, id string) uint64 {
	c, ok := s.Contract(id)
	Expect(ok).To(BeTrue())
	return c.Storage[0]
}

var _ = Describe("contract state", func() {
	// argCode stores args[0] at storage[args[1]]: ARG(0)... SSTORE pops key first.
	argCode := asm(0, vm.OpARG, 1, vm.OpARG, vm.OpSSTORE)

	Describe("BlockDelta", func() {
		It("deploys with ID = transaction hash and empty storage", func() {
			tx := deployTx(vm.CounterCode)
			delta, err := vm.BlockDelta(vm.State{}, blockOf(tx))
			Expect(err).NotTo(HaveOccurred())

			var s vm.State
			s.Apply(delta)
			c, ok := s.Contract(tx.CalculateHash())
			Expect(ok).To(BeTrue())
			Expect(c.Code).To(Equal(vm.CounterCode))
			Expect(c.Storage).To(BeEmpty())
			Expect(c.Storage).NotTo(BeNil())
		})

		It("uses the calculated hash, not a stale ID", func() {
			tx := deployTx(vm.CounterCode)
			tx.ID = "stale"
			delta, err := vm.BlockDelta(vm.State{}, blockOf(tx))
			Expect(err).NotTo(HaveOccurred())
			var s vm.State
			s.Apply(delta)
			_, ok := s.Contract(tx.CalculateHash())
			Expect(ok).To(BeTrue())
		})

		It("ignores transactions without a contract", func() {
			plain := contractTx(nil)
			delta, err := vm.BlockDelta(vm.State{}, blockOf(plain))
			Expect(err).NotTo(HaveOccurred())
			Expect(delta).To(Equal(vm.Delta{}))
		})

		It("runs a call on the storage of a contract from an earlier block", func() {
			dep := deployTx(vm.CounterCode)
			id := dep.CalculateHash()
			s, err := vm.BuildState([]*domain.Block{blockOf(dep), blockOf(callTx(id, 29))})
			Expect(err).NotTo(HaveOccurred())

			delta, err := vm.BlockDelta(s, blockOf(callTx(id, 29)))
			Expect(err).NotTo(HaveOccurred())
			s.Apply(delta)
			Expect(counterOf(s, id)).To(Equal(uint64(2)))
		})

		It("lets a call see the writes of earlier transactions in the same block", func() {
			dep := deployTx(vm.CounterCode)
			id := dep.CalculateHash()
			s, err := vm.BuildState([]*domain.Block{blockOf(dep)})
			Expect(err).NotTo(HaveOccurred())

			delta, err := vm.BlockDelta(s, blockOf(callTx(id, 29), callTx(id, 29), callTx(id, 29)))
			Expect(err).NotTo(HaveOccurred())
			s.Apply(delta)
			Expect(counterOf(s, id)).To(Equal(uint64(3)))
		})

		It("keeps the storage of different contracts apart", func() {
			depA, depB := deployTx(vm.CounterCode), deployTx(vm.CounterCode)
			a, b := depA.CalculateHash(), depB.CalculateHash()
			s, err := vm.BuildState([]*domain.Block{blockOf(depA, depB, callTx(a, 29), callTx(a, 29), callTx(b, 29))})
			Expect(err).NotTo(HaveOccurred())
			Expect(counterOf(s, a)).To(Equal(uint64(2)))
			Expect(counterOf(s, b)).To(Equal(uint64(1)))
		})

		It("passes the args and gas limit to Execute", func() {
			dep := deployTx(argCode)
			id := dep.CalculateHash()
			s, err := vm.BuildState([]*domain.Block{blockOf(dep, callTx(id, 100, 42, 7))})
			Expect(err).NotTo(HaveOccurred())
			c, _ := s.Contract(id)
			Expect(c.Storage).To(Equal(map[uint64]uint64{7: 42}))

			_, err = vm.BlockDelta(s, blockOf(callTx(id, 3, 42, 7)))
			Expect(err).To(MatchError(vm.ErrOutOfGas))
		})

		DescribeTable("rejects the block with an empty delta",
			func(build func() (vm.State, *domain.Block), want error) {
				s, b := build()
				before := cloneState(s)
				delta, err := vm.BlockDelta(s, b)
				Expect(err).To(MatchError(want))
				Expect(delta).To(Equal(vm.Delta{}))
				Expect(reflect.DeepEqual(s, before)).To(BeTrue(), "view must be untouched")
			},
			Entry("invalid contract op", func() (vm.State, *domain.Block) {
				return vm.State{}, blockOf(contractTx(&domain.ContractOp{Kind: "x", GasLimit: 1}))
			}, domain.ErrInvalidContractOp),
			Entry("invalid code", func() (vm.State, *domain.Block) {
				return vm.State{}, blockOf(deployTx([]byte{0xEE}))
			}, vm.ErrInvalidCode),
			Entry("deploy of an existing contract in the view", func() (vm.State, *domain.Block) {
				dep := deployTx(vm.CounterCode)
				s, err := vm.BuildState([]*domain.Block{blockOf(dep)})
				Expect(err).NotTo(HaveOccurred())
				return s, blockOf(dep)
			}, vm.ErrContractExists),
			Entry("deploy of the same transaction twice in a block", func() (vm.State, *domain.Block) {
				dep := deployTx(vm.CounterCode)
				return vm.State{}, blockOf(dep, dep)
			}, vm.ErrContractExists),
			Entry("call to an unknown contract", func() (vm.State, *domain.Block) {
				return vm.State{}, blockOf(callTx(hex.EncodeToString(make([]byte, 32)), 29))
			}, vm.ErrUnknownContract),
			Entry("call before the deploy in the same block", func() (vm.State, *domain.Block) {
				dep := deployTx(vm.CounterCode)
				return vm.State{}, blockOf(callTx(dep.CalculateHash(), 29), dep)
			}, vm.ErrUnknownContract),
			Entry("out of gas after a good call", func() (vm.State, *domain.Block) {
				dep := deployTx(vm.CounterCode)
				id := dep.CalculateHash()
				s, err := vm.BuildState([]*domain.Block{blockOf(dep)})
				Expect(err).NotTo(HaveOccurred())
				return s, blockOf(callTx(id, 29), callTx(id, 28))
			}, vm.ErrOutOfGas),
		)
	})

	Describe("BuildState", func() {
		It("is empty for no blocks", func() {
			s, err := vm.BuildState(nil)
			Expect(err).NotTo(HaveOccurred())
			_, ok := s.Contract("x")
			Expect(ok).To(BeFalse())
		})

		It("counts calls across blocks", func() {
			dep := deployTx(vm.CounterCode)
			id := dep.CalculateHash()
			s, err := vm.BuildState([]*domain.Block{
				blockOf(dep),
				blockOf(callTx(id, 29)),
				blockOf(callTx(id, 29), callTx(id, 29)),
				blockOf(),
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(counterOf(s, id)).To(Equal(uint64(3)))
		})

		It("accepts deploy then call in one block and rejects the reverse", func() {
			dep := deployTx(vm.CounterCode)
			id := dep.CalculateHash()
			s, err := vm.BuildState([]*domain.Block{blockOf(dep, callTx(id, 29))})
			Expect(err).NotTo(HaveOccurred())
			Expect(counterOf(s, id)).To(Equal(uint64(1)))

			_, err = vm.BuildState([]*domain.Block{blockOf(callTx(id, 29), dep)})
			Expect(err).To(MatchError(vm.ErrUnknownContract))
		})

		It("invalidates a chain whose call has GasLimit 28", func() {
			dep := deployTx(vm.CounterCode)
			_, err := vm.BuildState([]*domain.Block{blockOf(dep), blockOf(callTx(dep.CalculateHash(), 28))})
			Expect(err).To(MatchError(vm.ErrOutOfGas))
		})

		It("reports the failing block index", func() {
			_, err := vm.BuildState([]*domain.Block{blockOf(), blockOf(deployTx([]byte{0xEE}))})
			Expect(err).To(MatchError(vm.ErrInvalidCode))
			Expect(err.Error()).To(ContainSubstring("block 1"))
			Expect(errors.Unwrap(err)).NotTo(BeNil())
		})
	})
})
