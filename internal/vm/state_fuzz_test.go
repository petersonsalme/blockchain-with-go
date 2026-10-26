package vm_test

import (
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
