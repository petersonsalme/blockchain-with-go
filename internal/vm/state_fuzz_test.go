package vm_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/vm"
)

// FuzzBuildStateMatchesIncremental reads the input as a sequence of deploy and
// call operations on the counter, cut into blocks, and checks that BuildState
// equals the block-by-block BlockDelta + Apply, and is deterministic.
func FuzzBuildStateMatchesIncremental(f *testing.F) {
	f.Add([]byte{0, 1, 1, 1, 255, 1, 1})
	f.Add([]byte{0, 255, 0, 1, 1, 255, 1})
	f.Add([]byte{1, 0, 0, 1, 2, 255, 0, 3, 1})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		var (
			blocks []*domain.Block
			cur    []domain.Transaction
			ids    []string
		)

		for _, b := range data {
			switch {
			case b == 255: // seal the block
				blocks = append(blocks, blockOf(cur...))
				cur = nil
			case b%3 == 0 || len(ids) == 0:
				tx := deployTx(vm.CounterCode)
				ids = append(ids, tx.CalculateHash())
				cur = append(cur, tx)
			default:
				cur = append(cur, callTx(ids[int(b)%len(ids)], 29))
			}
		}

		blocks = append(blocks, blockOf(cur...))

		built, err := vm.BuildState(blocks)
		if err != nil {
			t.Fatalf("BuildState: %v", err)
		}

		again, err := vm.BuildState(blocks)
		if err != nil || !reflect.DeepEqual(built, again) {
			t.Fatalf("BuildState not deterministic: %v", err)
		}

		inc := vm.State{}
		for i, b := range blocks {
			delta, err := vm.BlockDelta(inc, b)
			if err != nil {
				t.Fatalf("block %d: %v", i, err)
			}

			inc.Apply(delta)
		}

		if !reflect.DeepEqual(built, inc) {
			t.Fatalf("BuildState %v != incremental %v", built, inc)
		}
	})
}

// FuzzBuildStateWithArgs drives an args-taking contract with good and failing
// calls, checking that BuildState matches incremental replay and does not alter
// views.
func FuzzBuildStateWithArgs(f *testing.F) {
	storeArgs := asm(0, vm.OpARG, 1, vm.OpARG, vm.OpSSTORE)

	f.Add([]byte("\x00\x01\x05\xff\t\r"))
	f.Add([]byte("\x00\x01\xff\x02"))
	f.Add([]byte("\x00\xff\x03\x01"))
	f.Add([]byte("\x00\x01"))

	f.Fuzz(func(t *testing.T, data []byte) {
		var (
			blocks []*domain.Block
			cur    []domain.Transaction
			ids    []string
		)

		for _, b := range data {
			switch {
			case b == 255:
				blocks = append(blocks, blockOf(cur...))
				cur = nil
			case b%4 == 0 || len(ids) == 0:
				tx := deployTx(storeArgs)
				ids = append(ids, tx.CalculateHash())
				cur = append(cur, tx)
			case b%4 == 1:
				cur = append(cur, callTx(ids[int(b)%len(ids)], 24, uint64(b), uint64(b%8)))
			case b%4 == 2:
				cur = append(cur, callTx(ids[int(b)%len(ids)], 24, uint64(b)))
			case b%4 == 3:
				cur = append(cur, callTx(ids[int(b)%len(ids)], 23, uint64(b), 0))
			}
		}

		blocks = append(blocks, blockOf(cur...))

		var (
			failAt = -1
			incErr error
		)

		inc := vm.State{}

		for i, b := range blocks {
			before := cloneState(inc)

			d, err := vm.BlockDelta(inc, b)
			if !reflect.DeepEqual(inc, before) {
				t.Fatalf("BlockDelta altered view: before %v != after %v", before, inc)
			}

			if err != nil {
				failAt, incErr = i, err

				break
			}

			inc.Apply(d)
		}

		built, err := vm.BuildState(blocks)
		if failAt >= 0 {
			if err == nil {
				t.Fatalf("BuildState succeeded, expected error at block %d: %v", failAt, incErr)
			}

			expected := fmt.Sprintf("block %d: %v", failAt, incErr)
			if err.Error() != expected {
				t.Fatalf("BuildState error %q != expected %q", err.Error(), expected)
			}

			return
		}

		if err != nil {
			t.Fatalf("BuildState: %v", err)
		}

		if !reflect.DeepEqual(built, inc) {
			t.Fatalf("BuildState %v != incremental %v", built, inc)
		}
	})
}
