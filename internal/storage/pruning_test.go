package storage_test

import (
	"errors"
	"testing"

	"github.com/petersonsalme/blockchain-with-go/internal/storage"
)

func TestPruningValidate(t *testing.T) {
	cases := []struct {
		name string
		p    storage.Pruning
		ok   bool
	}{
		{"zero", storage.Pruning{}, true},
		{"snapshots only", storage.Pruning{SnapshotInterval: 3}, true},
		{"snapshots and pruning", storage.Pruning{SnapshotInterval: 3, KeepBlocks: 2}, true},
		{"keep without interval", storage.Pruning{KeepBlocks: 1}, false},
		{"negative interval", storage.Pruning{SnapshotInterval: -1}, false},
		{"negative keep", storage.Pruning{SnapshotInterval: 3, KeepBlocks: -1}, false},
	}
	for _, c := range cases {
		err := c.p.Validate()
		if c.ok && err != nil {
			t.Errorf("%s: unexpected %v", c.name, err)
		}
		if !c.ok && !errors.Is(err, storage.ErrInvalidPruning) {
			t.Errorf("%s: got %v, want ErrInvalidPruning", c.name, err)
		}
	}
}

func TestPruningSnapshotHeight(t *testing.T) {
	cases := []struct {
		p     storage.Pruning
		t     int
		want  int
		found bool
	}{
		{storage.Pruning{}, 5, 0, false},
		{storage.Pruning{SnapshotInterval: 3}, 3, 3, true},
		{storage.Pruning{SnapshotInterval: 3}, 4, 0, false},
		{storage.Pruning{SnapshotInterval: 3}, 6, 6, true},
		{storage.Pruning{SnapshotInterval: 1}, 0, 0, false},
		{storage.Pruning{SnapshotInterval: 1}, 1, 1, true},
		{storage.Pruning{SnapshotInterval: 3, KeepBlocks: 2}, 5, 3, true},
		{storage.Pruning{SnapshotInterval: 3, KeepBlocks: 2}, 4, 0, false},
		{storage.Pruning{SnapshotInterval: 3, KeepBlocks: 2}, 2, 0, false},
		{storage.Pruning{SnapshotInterval: 3, KeepBlocks: 3}, 3, 0, false},
		{storage.Pruning{SnapshotInterval: 5, KeepBlocks: 4}, 9, 5, true},
	}
	for _, c := range cases {
		got, ok := c.p.SnapshotHeight(c.t)
		if got != c.want || ok != c.found {
			t.Errorf("%+v t=%d: got (%d,%v), want (%d,%v)", c.p, c.t, got, ok, c.want, c.found)
		}
	}
}
