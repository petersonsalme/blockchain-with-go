package vm_test

import (
	"encoding/binary"
	"reflect"
	"testing"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/vm"
)

// FuzzExecuteDeterministic checks that Execute never panics, is a pure function
// of its inputs, stays within the gas limit and leaves the storage alone.
func FuzzExecuteDeterministic(f *testing.F) {
	f.Add(vm.CounterCode, []byte{}, uint64(29))
	f.Add(vm.CounterCode, []byte{}, uint64(28))
	f.Add(asm(0, vm.OpJUMP), []byte{}, uint64(100))
	f.Add(asm(1, 1, vm.OpSSTORE, vm.OpREVERT), []byte{1, 2, 3, 4, 5, 6, 7, 8}, uint64(50))
	f.Add([]byte{vm.OpARG}, []byte{}, uint64(0))
	f.Fuzz(func(t *testing.T, code, rawArgs []byte, gas uint64) {
		var args []uint64
		for len(rawArgs) >= 8 {
			args = append(args, binary.BigEndian.Uint64(rawArgs))
			rawArgs = rawArgs[8:]
		}

		store := mapStorage{0: 7, 1: 9}
		res1, err1 := vm.Execute(code, args, store, gas)

		res2, err2 := vm.Execute(code, args, store, gas)
		if err1 != err2 || !reflect.DeepEqual(res1, res2) {
			t.Fatalf("not deterministic: (%v, %v) vs (%v, %v)", res1, err1, res2, err2)
		}

		if res1.GasUsed > gas {
			t.Fatalf("GasUsed %d above limit %d", res1.GasUsed, gas)
		}

		if err1 != nil && len(res1.Writes) != 0 {
			t.Fatalf("error %v with writes %v", err1, res1.Writes)
		}

		if gas == 0 || gas > domain.MaxContractGas {
			if err1 != vm.ErrInvalidGas {
				t.Fatalf("gas %d: want ErrInvalidGas, got %v", gas, err1)
			}
		}

		if !reflect.DeepEqual(store, mapStorage{0: 7, 1: 9}) {
			t.Fatalf("storage mutated: %v", store)
		}
	})
}
