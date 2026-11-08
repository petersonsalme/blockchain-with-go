package contracts_test

import (
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/petersonsalme/blockchain-with-go/internal/vm"
)

type store map[uint64]uint64

func (s store) Load(k uint64) uint64 { return s[k] }

func loadHex(t *testing.T, name string) []byte {
	t.Helper()

	raw, err := os.ReadFile(name + ".hex")
	if err != nil {
		t.Fatal(err)
	}

	text := string(raw)
	if !strings.HasSuffix(text, "\n") || strings.Count(text, "\n") != 1 {
		t.Fatalf("%s.hex must hold the hex on one line followed by a newline", name)
	}

	code, err := hex.DecodeString(strings.TrimSuffix(text, "\n"))
	if err != nil {
		t.Fatal(err)
	}

	if hex.EncodeToString(code)+"\n" != text {
		t.Fatalf("%s.hex is not lower-case hex", name)
	}

	return code
}

func TestExampleContracts(t *testing.T) {
	cases := []struct {
		name      string
		contract  string
		args      []uint64
		store     store
		gasLimit  uint64
		wantErr   error
		wantWrite map[uint64]uint64
		wantGas   uint64
	}{
		{"counter", "counter", nil, store{}, 100, nil, map[uint64]uint64{0: 1}, 29},
		{"counter out of gas", "counter", nil, store{}, 28, vm.ErrOutOfGas, nil, 0},
		{"adder", "adder", []uint64{7}, store{0: 5}, 100, nil, map[uint64]uint64{0: 12}, 30},
		{"adder without args", "adder", nil, store{}, 100, vm.ErrArgIndex, nil, 0},
		{"guard", "guard", []uint64{42}, store{}, 100, nil, map[uint64]uint64{1: 42}, 28},
		{"guard zero", "guard", []uint64{0}, store{}, 100, vm.ErrReverted, nil, 0},
		{"capped", "capped", nil, store{0: 2}, 100, nil, map[uint64]uint64{0: 3}, 35},
		{"capped full", "capped", nil, store{0: 3}, 100, vm.ErrReverted, nil, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code := loadHex(t, tc.contract)

			err := vm.ValidateCode(code)
			if err != nil {
				t.Fatalf("ValidateCode: %v", err)
			}

			res, err := vm.Execute(code, tc.args, tc.store, tc.gasLimit)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Execute error = %v, want %v", err, tc.wantErr)
			}

			if tc.wantErr != nil {
				return
			}

			if res.GasUsed != tc.wantGas {
				t.Errorf("GasUsed = %d, want %d", res.GasUsed, tc.wantGas)
			}

			if len(res.Writes) != len(tc.wantWrite) {
				t.Fatalf("Writes = %v, want %v", res.Writes, tc.wantWrite)
			}

			for k, v := range tc.wantWrite {
				if res.Writes[k] != v {
					t.Errorf("Writes[%d] = %d, want %d", k, res.Writes[k], v)
				}
			}
		})
	}
}

func TestCounterHexIsVMCounterCode(t *testing.T) {
	if got := hex.EncodeToString(loadHex(t, "counter")); got != hex.EncodeToString(vm.CounterCode) {
		t.Fatalf("counter.hex = %s, want %s", got, hex.EncodeToString(vm.CounterCode))
	}
}
