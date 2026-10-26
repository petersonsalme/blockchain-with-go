// Package vm is a pure stack-machine interpreter for contract bytecode
// (ADR-0014). It does no I/O, reads no clock and uses no randomness, so the
// same code, arguments, storage and gas limit always give the same outcome.
package vm

import (
	"encoding/binary"
	"errors"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

// MaxStack is the deepest the operand stack may grow.
const MaxStack = 64

// Opcodes.
const (
	OpSTOP   byte = 0x00
	OpPUSH8  byte = 0x01
	OpPOP    byte = 0x02
	OpDUP    byte = 0x03
	OpSWAP   byte = 0x04
	OpADD    byte = 0x10
	OpSUB    byte = 0x11
	OpMUL    byte = 0x12
	OpLT     byte = 0x13
	OpEQ     byte = 0x14
	OpISZERO byte = 0x15
	OpJUMP   byte = 0x20
	OpJUMPI  byte = 0x21
	OpSLOAD  byte = 0x30
	OpSSTORE byte = 0x31
	OpARG    byte = 0x40
	OpREVERT byte = 0xFF
)

// Execution errors.
var (
	ErrOutOfGas       = errors.New("vm: out of gas")
	ErrStackOverflow  = errors.New("vm: stack overflow")
	ErrStackUnderflow = errors.New("vm: stack underflow")
	ErrBadJump        = errors.New("vm: bad jump destination")
	ErrArgIndex       = errors.New("vm: argument index out of range")
	ErrInvalidCode    = errors.New("vm: invalid code")
	ErrReverted       = errors.New("vm: reverted")
	ErrInvalidGas     = errors.New("vm: invalid gas limit")
)

// CounterCode increments storage[0]: PUSH8 0, SLOAD, PUSH8 1, ADD, PUSH8 0, SSTORE, STOP.
var CounterCode = []byte{
	OpPUSH8, 0, 0, 0, 0, 0, 0, 0, 0,
	OpSLOAD,
	OpPUSH8, 0, 0, 0, 0, 0, 0, 0, 1,
	OpADD,
	OpPUSH8, 0, 0, 0, 0, 0, 0, 0, 0,
	OpSSTORE,
	OpSTOP,
}

// Storage is the read-only view of a contract's storage; absent keys load as 0.
type Storage interface {
	Load(key uint64) uint64
}

// Result is the outcome of a successful execution. On error Writes is empty.
type Result struct {
	Writes  map[uint64]uint64
	GasUsed uint64
}

// gasCost is the gas of each opcode; ok is false for an unknown opcode.
func gasCost(op byte) (cost uint64, ok bool) {
	switch op {
	case OpSTOP, OpREVERT:
		return 0, true
	case OpPUSH8, OpPOP, OpDUP, OpSWAP, OpADD, OpSUB, OpMUL, OpLT, OpEQ, OpISZERO, OpJUMP, OpJUMPI, OpARG:
		return 1, true
	case OpSLOAD:
		return 5, true
	case OpSSTORE:
		return 20, true
	}

	return 0, false
}

// ValidateCode checks the structural rules of deployable code: 1 to
// domain.MaxContractCodeBytes bytes, only known opcodes, no truncated PUSH8.
func ValidateCode(code []byte) error {
	_, err := opcodeOffsets(code)
	return err
}

// opcodeOffsets validates code and returns which offsets start an opcode.
func opcodeOffsets(code []byte) ([]bool, error) {
	if len(code) == 0 || len(code) > domain.MaxContractCodeBytes {
		return nil, ErrInvalidCode
	}

	starts := make([]bool, len(code))

	skip := 0 // bytes of a PUSH8 immediate still to pass
	for pc, op := range code {
		if skip > 0 {
			skip--
			continue
		}

		if _, ok := gasCost(op); !ok {
			return nil, ErrInvalidCode
		}

		starts[pc] = true
		if op == OpPUSH8 {
			if pc+8 >= len(code) {
				return nil, ErrInvalidCode
			}

			skip = 8
		}
	}

	return starts, nil
}

// Execute runs code with args against store, spending at most gasLimit gas.
// It never mutates store; storage writes are buffered in Result.Writes and
// dropped on any error. gasLimit must be in 1..domain.MaxContractGas.
func Execute(code []byte, args []uint64, store Storage, gasLimit uint64) (Result, error) {
	if gasLimit == 0 || gasLimit > domain.MaxContractGas {
		return Result{}, ErrInvalidGas
	}

	starts, err := opcodeOffsets(code)
	if err != nil {
		return Result{}, err
	}

	m := machine{code: code, args: args, store: store, starts: starts, writes: map[uint64]uint64{}}
	for m.pc < len(code) {
		op := code[m.pc]

		cost, _ := gasCost(op)
		if m.used+cost > gasLimit {
			return Result{GasUsed: m.used}, ErrOutOfGas
		}

		m.used += cost

		done, err := m.step(op)
		if err != nil {
			return Result{GasUsed: m.used}, err
		}

		if done {
			break
		}
	}

	return Result{Writes: m.writes, GasUsed: m.used}, nil
}

type machine struct {
	code   []byte
	args   []uint64
	store  Storage
	starts []bool
	writes map[uint64]uint64
	stack  []uint64
	pc     int
	used   uint64
}

func (m *machine) pop() (uint64, error) {
	if len(m.stack) == 0 {
		return 0, ErrStackUnderflow
	}

	v := m.stack[len(m.stack)-1]
	m.stack = m.stack[:len(m.stack)-1]

	return v, nil
}

func (m *machine) push(v uint64) error {
	if len(m.stack) >= MaxStack {
		return ErrStackOverflow
	}

	m.stack = append(m.stack, v)

	return nil
}

// pop2 pops the top (b) and the one under it (a).
func (m *machine) pop2() (a, b uint64, err error) {
	if len(m.stack) < 2 {
		return 0, 0, ErrStackUnderflow
	}

	b, _ = m.pop()
	a, _ = m.pop()

	return a, b, nil
}

// jump validates a destination and returns it as the next pc.
func (m *machine) jump(dest uint64) (int, error) {
	if dest >= uint64(len(m.code)) || !m.starts[dest] {
		return 0, ErrBadJump
	}

	return int(dest), nil
}

func (m *machine) load(key uint64) uint64 {
	if v, ok := m.writes[key]; ok {
		return v
	}

	return m.store.Load(key)
}

// step runs one instruction whose gas is already charged; done reports a halt.
func (m *machine) step(op byte) (done bool, err error) {
	next := m.pc + 1

	switch op {
	case OpSTOP:
		return true, nil
	case OpREVERT:
		return true, ErrReverted
	case OpPUSH8:
		err = m.push(binary.BigEndian.Uint64(m.code[m.pc+1 : m.pc+9]))
		next = m.pc + 9
	case OpPOP:
		_, err = m.pop()
	case OpDUP:
		if len(m.stack) == 0 {
			return false, ErrStackUnderflow
		}

		err = m.push(m.stack[len(m.stack)-1])
	case OpSWAP:
		if len(m.stack) < 2 {
			return false, ErrStackUnderflow
		}

		n := len(m.stack)
		m.stack[n-1], m.stack[n-2] = m.stack[n-2], m.stack[n-1]
	case OpADD, OpSUB, OpMUL, OpLT, OpEQ:
		var a, b uint64
		if a, b, err = m.pop2(); err != nil {
			return false, err
		}

		err = m.push(binaryOp(op, a, b))
	case OpISZERO:
		var v uint64
		if v, err = m.pop(); err != nil {
			return false, err
		}

		err = m.push(boolWord(v == 0))
	case OpJUMP:
		var dest uint64
		if dest, err = m.pop(); err != nil {
			return false, err
		}

		next, err = m.jump(dest)
	case OpJUMPI:
		var dest, cond uint64
		if dest, err = m.pop(); err != nil {
			return false, err
		}

		if cond, err = m.pop(); err != nil {
			return false, err
		}

		if cond != 0 {
			next, err = m.jump(dest)
		}
	case OpSLOAD:
		var key uint64
		if key, err = m.pop(); err != nil {
			return false, err
		}

		err = m.push(m.load(key))
	case OpSSTORE:
		var key, val uint64
		if key, err = m.pop(); err != nil {
			return false, err
		}

		if val, err = m.pop(); err != nil {
			return false, err
		}

		m.writes[key] = val
	case OpARG:
		var i uint64
		if i, err = m.pop(); err != nil {
			return false, err
		}

		if i >= uint64(len(m.args)) {
			return false, ErrArgIndex
		}

		err = m.push(m.args[i])
	}

	if err != nil {
		return false, err
	}

	m.pc = next

	return false, nil
}

func binaryOp(op byte, a, b uint64) uint64 {
	switch op {
	case OpADD:
		return a + b
	case OpSUB:
		return a - b
	case OpMUL:
		return a * b
	case OpLT:
		return boolWord(a < b)
	}

	return boolWord(a == b) // OpEQ
}

func boolWord(b bool) uint64 {
	if b {
		return 1
	}

	return 0
}
