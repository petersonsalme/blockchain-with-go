package vm_test

import (
	"bytes"
	"encoding/binary"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/vm"
)

type mapStorage map[uint64]uint64

func (s mapStorage) Load(key uint64) uint64 { return s[key] }

// asm builds code from opcodes and uint64 immediates: a uint64 becomes PUSH8 <v>.
func asm(parts ...any) []byte {
	var out []byte

	for _, p := range parts {
		switch v := p.(type) {
		case byte:
			out = append(out, v)
		case int:
			out = append(out, vm.OpPUSH8)
			out = binary.BigEndian.AppendUint64(out, uint64(v))
		case uint64:
			out = append(out, vm.OpPUSH8)
			out = binary.BigEndian.AppendUint64(out, v)
		}
	}

	return out
}

type vmCase struct {
	name    string
	code    []byte
	args    []uint64
	store   mapStorage
	gas     uint64
	writes  map[uint64]uint64
	used    uint64
	wantErr error
}

var _ = Describe("Execute", func() {
	// Each program leaves its answer in storage[0] so success is observable.
	// SSTORE pops the key first, then the value.
	out := func(body ...any) []byte { return asm(append(body, 0, vm.OpSSTORE)...) }
	maxWord := ^uint64(0)

	cases := []vmCase{
		{name: "STOP halts at zero cost", code: asm(vm.OpSTOP, 1), gas: 5, writes: map[uint64]uint64{}, used: 0},
		{name: "running past the end is STOP", code: asm(1, vm.OpPOP), gas: 5, writes: map[uint64]uint64{}, used: 2},
		{name: "PUSH8 is big-endian", code: out(0x0102030405060708), gas: 50, writes: map[uint64]uint64{0: 0x0102030405060708}, used: 22},
		{name: "POP", code: out(7, 9, vm.OpPOP), gas: 50, writes: map[uint64]uint64{0: 7}, used: 24},
		{name: "POP underflow", code: asm(vm.OpPOP), gas: 5, used: 1, wantErr: vm.ErrStackUnderflow},
		{name: "DUP", code: out(3, vm.OpDUP, vm.OpMUL), gas: 50, writes: map[uint64]uint64{0: 9}, used: 24},
		{name: "DUP underflow", code: asm(vm.OpDUP), gas: 5, used: 1, wantErr: vm.ErrStackUnderflow},
		{name: "SWAP", code: out(3, 10, vm.OpSWAP, vm.OpSUB), gas: 50, writes: map[uint64]uint64{0: 7}, used: 25},
		{name: "SWAP underflow", code: asm(1, vm.OpSWAP), gas: 5, used: 2, wantErr: vm.ErrStackUnderflow},
		{name: "ADD wraps", code: out(maxWord, 2, vm.OpADD), gas: 50, writes: map[uint64]uint64{0: 1}, used: 24},
		{name: "ADD underflow", code: asm(1, vm.OpADD), gas: 5, used: 2, wantErr: vm.ErrStackUnderflow},
		{name: "SUB is a-b with b on top", code: out(10, 3, vm.OpSUB), gas: 50, writes: map[uint64]uint64{0: 7}, used: 24},
		{name: "SUB wraps", code: out(0, 1, vm.OpSUB), gas: 50, writes: map[uint64]uint64{0: maxWord}, used: 24},
		{name: "SUB underflow", code: asm(1, vm.OpSUB), gas: 5, used: 2, wantErr: vm.ErrStackUnderflow},
		{name: "MUL wraps", code: out(maxWord, 2, vm.OpMUL), gas: 50, writes: map[uint64]uint64{0: maxWord - 1}, used: 24},
		{name: "MUL underflow", code: asm(vm.OpMUL), gas: 5, used: 1, wantErr: vm.ErrStackUnderflow},
		{name: "LT true", code: out(1, 2, vm.OpLT), gas: 50, writes: map[uint64]uint64{0: 1}, used: 24},
		{name: "LT false when equal", code: out(2, 2, vm.OpLT), gas: 50, writes: map[uint64]uint64{0: 0}, used: 24},
		{name: "LT false when greater", code: out(3, 2, vm.OpLT), gas: 50, writes: map[uint64]uint64{0: 0}, used: 24},
		{name: "LT underflow", code: asm(1, vm.OpLT), gas: 5, used: 2, wantErr: vm.ErrStackUnderflow},
		{name: "EQ true", code: out(4, 4, vm.OpEQ), gas: 50, writes: map[uint64]uint64{0: 1}, used: 24},
		{name: "EQ false", code: out(4, 5, vm.OpEQ), gas: 50, writes: map[uint64]uint64{0: 0}, used: 24},
		{name: "EQ underflow", code: asm(vm.OpEQ), gas: 5, used: 1, wantErr: vm.ErrStackUnderflow},
		{name: "ISZERO of zero", code: out(0, vm.OpISZERO), gas: 50, writes: map[uint64]uint64{0: 1}, used: 23},
		{name: "ISZERO of non-zero", code: out(5, vm.OpISZERO), gas: 50, writes: map[uint64]uint64{0: 0}, used: 23},
		{name: "ISZERO underflow", code: asm(vm.OpISZERO), gas: 5, used: 1, wantErr: vm.ErrStackUnderflow},
		{name: "JUMP underflow", code: asm(vm.OpJUMP), gas: 5, used: 1, wantErr: vm.ErrStackUnderflow},
		{name: "JUMP out of range", code: asm(1000, vm.OpJUMP), gas: 5, used: 2, wantErr: vm.ErrBadJump},
		{name: "JUMP to code length", code: asm(10, vm.OpJUMP), gas: 5, used: 2, wantErr: vm.ErrBadJump},
		{name: "JUMPI underflow on destination", code: asm(vm.OpJUMPI), gas: 5, used: 1, wantErr: vm.ErrStackUnderflow},
		{name: "JUMPI underflow on condition", code: asm(0, vm.OpJUMPI), gas: 5, used: 2, wantErr: vm.ErrStackUnderflow},
		{name: "JUMPI not taken ignores a bad destination", code: asm(0, 1000, vm.OpJUMPI), gas: 5, writes: map[uint64]uint64{}, used: 3},
		{name: "JUMPI taken to a bad destination", code: asm(1, 1000, vm.OpJUMPI), gas: 5, used: 3, wantErr: vm.ErrBadJump},
		{name: "SLOAD reads storage", code: asm(7, vm.OpSLOAD, 0, vm.OpSSTORE), store: mapStorage{7: 42}, gas: 50, writes: map[uint64]uint64{0: 42}, used: 27},
		{name: "SLOAD absent is zero", code: asm(7, vm.OpSLOAD, 0, vm.OpSSTORE), gas: 50, writes: map[uint64]uint64{0: 0}, used: 27},
		{name: "SLOAD underflow", code: asm(vm.OpSLOAD), gas: 10, used: 5, wantErr: vm.ErrStackUnderflow},
		{name: "SLOAD sees an earlier SSTORE of the same run", code: asm(5, 7, vm.OpSSTORE, 7, vm.OpSLOAD, 0, vm.OpSSTORE), store: mapStorage{7: 1}, gas: 100, writes: map[uint64]uint64{7: 5, 0: 5}, used: 1 + 1 + 20 + 1 + 5 + 1 + 20},
		{name: "SSTORE underflow on key", code: asm(vm.OpSSTORE), gas: 30, used: 20, wantErr: vm.ErrStackUnderflow},
		{name: "SSTORE underflow on value", code: asm(1, vm.OpSSTORE), gas: 30, used: 21, wantErr: vm.ErrStackUnderflow},
		{name: "SSTORE of zero is a write", code: asm(0, 3, vm.OpSSTORE), store: mapStorage{3: 9}, gas: 30, writes: map[uint64]uint64{3: 0}, used: 22},
		{name: "ARG", code: out(1, vm.OpARG), args: []uint64{10, 20}, gas: 50, writes: map[uint64]uint64{0: 20}, used: 23},
		{name: "ARG index past args", code: asm(2, vm.OpARG), args: []uint64{10, 20}, gas: 50, used: 2, wantErr: vm.ErrArgIndex},
		{name: "ARG with no args", code: asm(0, vm.OpARG), gas: 50, used: 2, wantErr: vm.ErrArgIndex},
		{name: "ARG underflow", code: asm(vm.OpARG), gas: 50, used: 1, wantErr: vm.ErrStackUnderflow},
		{name: "REVERT drops writes", code: asm(1, 1, vm.OpSSTORE, vm.OpREVERT), gas: 50, used: 22, wantErr: vm.ErrReverted},
		{name: "stack holds MaxStack words", code: bytes.Repeat(asm(1), vm.MaxStack), gas: 100, writes: map[uint64]uint64{}, used: vm.MaxStack},
		{name: "PUSH8 overflow", code: bytes.Repeat(asm(1), vm.MaxStack+1), gas: 100, used: vm.MaxStack + 1, wantErr: vm.ErrStackOverflow},
		{name: "DUP overflow", code: append(bytes.Repeat(asm(1), vm.MaxStack), vm.OpDUP), gas: 100, used: vm.MaxStack + 1, wantErr: vm.ErrStackOverflow},
		{name: "arithmetic overflow cannot happen on a full stack", code: append(bytes.Repeat(asm(1), vm.MaxStack), vm.OpADD), gas: 100, writes: map[uint64]uint64{}, used: vm.MaxStack + 1},
		{name: "ISZERO on a full stack", code: append(bytes.Repeat(asm(1), vm.MaxStack), vm.OpISZERO), gas: 100, writes: map[uint64]uint64{}, used: vm.MaxStack + 1},
		{name: "gas exactly spent", code: asm(1, vm.OpPOP), gas: 2, writes: map[uint64]uint64{}, used: 2},
		{name: "out of gas before an instruction", code: asm(1, vm.OpPOP), gas: 1, used: 1, wantErr: vm.ErrOutOfGas},
		{name: "SSTORE out of gas keeps no writes", code: asm(1, 1, vm.OpSSTORE), gas: 21, used: 2, wantErr: vm.ErrOutOfGas},
		{name: "SLOAD costs 5", code: asm(1, vm.OpSLOAD), gas: 5, used: 1, wantErr: vm.ErrOutOfGas},
		{name: "infinite loop runs out of gas", code: asm(0, vm.OpJUMP), gas: 100, used: 100, wantErr: vm.ErrOutOfGas},
	}

	for _, c := range cases {
		It(c.name, func() {
			store := c.store
			if store == nil {
				store = mapStorage{}
			}

			res, err := vm.Execute(c.code, c.args, store, c.gas)
			if c.wantErr != nil {
				Expect(err).To(MatchError(c.wantErr))
				Expect(res.Writes).To(BeEmpty())
			} else {
				Expect(err).NotTo(HaveOccurred())
				Expect(res.Writes).To(Equal(c.writes))
			}

			Expect(res.GasUsed).To(Equal(c.used))
		})
	}

	It("JUMP lands on an opcode offset", func() {
		// 0: PUSH8 19; 9: JUMP; 10: PUSH8 1 (skipped); 19: PUSH8 5; 28: PUSH8 0; 37: SSTORE
		code := asm(19, vm.OpJUMP, 1, 5, 0, vm.OpSSTORE)
		res, err := vm.Execute(code, nil, mapStorage{}, 100)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Writes).To(Equal(map[uint64]uint64{0: 5}))
		Expect(res.GasUsed).To(Equal(uint64(1 + 1 + 1 + 1 + 20)))
	})

	It("JUMP into a PUSH8 immediate is ErrBadJump", func() {
		_, err := vm.Execute(asm(11, vm.OpJUMP, 1), nil, mapStorage{}, 100)
		Expect(err).To(MatchError(vm.ErrBadJump))
	})

	It("JUMPI jumps on non-zero and falls through on zero, popping destination then condition", func() {
		// 0: PUSH8 c; 9: PUSH8 28; 18: JUMPI; 19: PUSH8 1; 28: PUSH8 2; 37: PUSH8 0; 46: SSTORE
		build := func(c int) []byte { return asm(c, 28, vm.OpJUMPI, 1, 2, 0, vm.OpSSTORE) }
		taken, err := vm.Execute(build(1), nil, mapStorage{}, 100)
		Expect(err).NotTo(HaveOccurred())
		Expect(taken.Writes).To(Equal(map[uint64]uint64{0: 2}))
		Expect(taken.GasUsed).To(Equal(uint64(1 + 1 + 1 + 1 + 1 + 20)))
		// Falling through leaves 1 then 2 on the stack: SSTORE(key 0, value 2) keeps 1 under it.
		fall, err := vm.Execute(build(0), nil, mapStorage{}, 100)
		Expect(err).NotTo(HaveOccurred())
		Expect(fall.Writes).To(Equal(map[uint64]uint64{0: 2}))
		Expect(fall.GasUsed).To(Equal(uint64(1 + 1 + 1 + 1 + 1 + 1 + 20)))
	})

	Describe("domain limits", func() {
		It("rejects gas 0 and gas above MaxContractGas without running anything", func() {
			for _, g := range []uint64{0, domain.MaxContractGas + 1, ^uint64(0)} {
				res, err := vm.Execute(vm.CounterCode, nil, mapStorage{}, g)
				Expect(err).To(MatchError(vm.ErrInvalidGas), strconv.FormatUint(g, 10))
				Expect(res).To(Equal(vm.Result{}))
			}

			_, err := vm.Execute(vm.CounterCode, nil, mapStorage{}, domain.MaxContractGas)
			Expect(err).NotTo(HaveOccurred())
			_, err = vm.Execute(vm.CounterCode, nil, mapStorage{}, 1)
			Expect(err).To(MatchError(vm.ErrOutOfGas))
		})

		It("rejects invalid code in Execute", func() {
			_, err := vm.Execute([]byte{0x77}, nil, mapStorage{}, 10)
			Expect(err).To(MatchError(vm.ErrInvalidCode))
		})
	})

	Describe("CounterCode", func() {
		It("increments storage[0] for 29 gas without touching the storage", func() {
			for _, n := range []uint64{0, 1, 41, maxWord} {
				store := mapStorage{0: n}
				res, err := vm.Execute(vm.CounterCode, nil, store, 29)
				Expect(err).NotTo(HaveOccurred())
				Expect(res.Writes).To(Equal(map[uint64]uint64{0: n + 1}))
				Expect(res.GasUsed).To(Equal(uint64(29)))
				Expect(store).To(Equal(mapStorage{0: n}))
			}
		})

		It("runs out of gas at 28 and returns no writes", func() {
			res, err := vm.Execute(vm.CounterCode, nil, mapStorage{0: 1}, 28)
			Expect(err).To(MatchError(vm.ErrOutOfGas))
			Expect(res.Writes).To(BeEmpty())
		})
	})
})

var _ = Describe("ValidateCode", func() {
	It("accepts the counter", func() {
		Expect(vm.ValidateCode(vm.CounterCode)).To(Succeed())
	})

	It("accepts every known opcode and a PUSH8 ending the code", func() {
		for _, op := range []byte{vm.OpSTOP, vm.OpPOP, vm.OpDUP, vm.OpSWAP, vm.OpADD, vm.OpSUB, vm.OpMUL, vm.OpLT, vm.OpEQ, vm.OpISZERO, vm.OpJUMP, vm.OpJUMPI, vm.OpSLOAD, vm.OpSSTORE, vm.OpARG, vm.OpREVERT} {
			Expect(vm.ValidateCode([]byte{op})).To(Succeed(), "opcode %#x", op)
		}

		Expect(vm.ValidateCode(asm(1))).To(Succeed())
	})

	It("rejects empty code", func() {
		Expect(vm.ValidateCode(nil)).To(MatchError(vm.ErrInvalidCode))
		Expect(vm.ValidateCode([]byte{})).To(MatchError(vm.ErrInvalidCode))
	})

	It("bounds the length at MaxContractCodeBytes", func() {
		Expect(vm.ValidateCode(make([]byte, domain.MaxContractCodeBytes))).To(Succeed())
		Expect(vm.ValidateCode(make([]byte, domain.MaxContractCodeBytes+1))).To(MatchError(vm.ErrInvalidCode))
	})

	It("rejects unknown opcodes, including after a PUSH8 immediate", func() {
		for _, b := range []byte{0x05, 0x16, 0x22, 0x32, 0x41, 0x80, 0xFE} {
			Expect(vm.ValidateCode([]byte{b})).To(MatchError(vm.ErrInvalidCode), "opcode %#x", b)
		}

		Expect(vm.ValidateCode(append(asm(1), 0x77))).To(MatchError(vm.ErrInvalidCode))
	})

	It("does not read PUSH8 immediates as opcodes", func() {
		Expect(vm.ValidateCode(asm(0x7777777777777777))).To(Succeed())
	})

	It("rejects a truncated PUSH8", func() {
		full := asm(1)
		for n := 1; n < len(full); n++ {
			Expect(vm.ValidateCode(full[:n])).To(MatchError(vm.ErrInvalidCode), "kept %d bytes", n)
		}
	})
})

var _ = Describe("package isolation", func() {
	It("imports only the standard library it needs and internal/domain", func() {
		names, err := filepath.Glob("*.go")
		Expect(err).NotTo(HaveOccurred())
		Expect(names).NotTo(BeEmpty())

		for _, name := range names {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}

			f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
			Expect(err).NotTo(HaveOccurred())

			for _, imp := range f.Imports {
				path, _ := strconv.Unquote(imp.Path.Value)
				Expect([]string{"os", "time", "math/rand", "math/rand/v2", "net", "net/http", "crypto/rand"}).NotTo(ContainElement(path), name)

				if strings.Contains(path, "/internal/") {
					Expect(path).To(HaveSuffix("/internal/domain"), name)
				}
			}
		}
	})
})
